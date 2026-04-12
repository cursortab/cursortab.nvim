// Package zeta implements the Zeta provider (Zed's native model).
//
// Prompt format (sent as a single text prompt to /v1/completions):
//
//	### Instruction:
//	You are a code completion assistant and your task is to analyze user edits
//	and then rewrite an excerpt that the user provides, suggesting the appropriate
//	edits within the excerpt, taking into account the cursor location.
//
//	### User Edits:
//	User edited "file.go":
//	```diff
//	-old line
//	+new line
//	```
//
//	### Diagnostics:                       (omitted if no diagnostics)
//	Diagnostics in "file.go":
//	```diagnostics
//	line 10: [DIAGNOSTIC_SEVERITY_ERROR] undefined: foo (source: gopls)
//	```
//
//	### Code Context:                      (omitted if no treesitter context)
//	Enclosing scope: func handleRequest(w http.ResponseWriter, r *http.Request) {
//	Sibling symbols:
//	  line 5: func otherFunc() {
//	Imports:
//	  import "net/http"
//
//	### Staged Changes:                    (omitted if not COMMIT_EDITMSG)
//	(full unified diff if ≤4KB, or extracted symbols in git diff format:)
//	+func newHelper(ctx context.Context) error {
//	-func oldHelper() error {
//
//	### Recent Files:                      (omitted if no recent snapshots)
//	```other.go
//	... first N lines of a recently viewed file ...
//	```
//
//	### User Excerpt:
//	```file.go
//	<|start_of_file|>
//	... context lines ...
//	<|editable_region_start|>
//	... before cursor ...<|user_cursor_is_here|>... after cursor ...
//	... more lines ...
//	<|editable_region_end|>
//	... context lines ...
//	```
//
//	### Response:
package zeta

import (
	"fmt"
	"strings"

	"cursortab/client/openai"
	"cursortab/provider"
	"cursortab/types"
)

// NewProvider creates a new Zeta provider (Zed's native model)
func NewProvider(config *types.ProviderConfig) *provider.Provider {
	return &provider.Provider{
		Name:          "zeta",
		Config:        config,
		Client:        openai.NewClient(config.ProviderURL, config.CompletionPath, config.APIKey),
		StreamingType: provider.StreamingLines,
		Preprocessors: []provider.Preprocessor{
			provider.TrimContent(),
		},
		DiffBuilder: provider.FormatDiffHistory(provider.DiffHistoryOptions{
			HeaderTemplate: "User edited %q:\n",
			Prefix:         "```diff\n",
			Suffix:         "\n```",
			Separator:      "\n\n",
		}),
		PromptBuilder: buildPrompt,
		Postprocessors: []provider.Postprocessor{
			provider.RejectEmpty(),
			provider.StripRepetition(),
			provider.ValidateAnchorPosition(0.25),
			provider.AnchorTruncation(0.75),
			parseCompletion,
		},
		Validators: []provider.Validator{
			provider.ValidateFirstLineAnchor(0.25),
		},
		StopTokens: []string{"\n<|editable_region_end|>"},
	}
}

func buildPrompt(p *provider.Provider, ctx *provider.Context) *openai.CompletionRequest {
	req := ctx.Request

	userExcerpt := buildUserExcerpt(req, ctx)
	userEdits := ""
	if p.DiffBuilder != nil {
		userEdits = p.DiffBuilder(req.FileDiffHistories)
	}
	diagnosticsText := formatDiagnosticsForPrompt(req)
	treesitterText := formatTreesitterForPrompt(req)
	gitDiffText := formatGitDiffForPrompt(req)
	recentFilesText := formatRecentFilesForPrompt(req)
	prompt := buildInstructionPrompt(userEdits, diagnosticsText, treesitterText, gitDiffText, recentFilesText, userExcerpt)

	return &openai.CompletionRequest{
		Model:       p.Config.ProviderModel,
		Prompt:      prompt,
		Temperature: p.Config.ProviderTemperature,
		MaxTokens:   p.Config.ProviderMaxTokens,
		TopK:        p.Config.ProviderTopK,
		Stop:        []string{"\n<|editable_region_end|>"},
		N:           1,
		Echo:        false,
	}
}

func buildUserExcerpt(req *types.CompletionRequest, ctx *provider.Context) string {
	var promptBuilder strings.Builder

	if len(req.Lines) == 0 {
		promptBuilder.WriteString("```")
		promptBuilder.WriteString(req.FilePath)
		promptBuilder.WriteString("\n<|start_of_file|>\n<|editable_region_start|>\n<|user_cursor_is_here|>\n<|editable_region_end|>\n```")
		return promptBuilder.String()
	}

	cursorRow := req.CursorRow
	cursorCol := req.CursorCol
	cursorLine := cursorRow - 1

	editableStart := ctx.WindowStart
	editableEnd := ctx.WindowEnd

	contextLinesBefore := 5
	contextLinesAfter := 5

	contextStart := max(0, editableStart-contextLinesBefore)
	contextEnd := min(len(req.Lines), editableEnd+contextLinesAfter)

	promptBuilder.WriteString("```")
	promptBuilder.WriteString(req.FilePath)
	promptBuilder.WriteString("\n")

	if contextStart == 0 {
		promptBuilder.WriteString("<|start_of_file|>\n")
	}

	for i := contextStart; i < editableStart; i++ {
		promptBuilder.WriteString(req.Lines[i])
		promptBuilder.WriteString("\n")
	}

	promptBuilder.WriteString("<|editable_region_start|>\n")

	for i := editableStart; i < cursorLine; i++ {
		promptBuilder.WriteString(req.Lines[i])
		promptBuilder.WriteString("\n")
	}

	if cursorLine < len(req.Lines) {
		currentLine := req.Lines[cursorLine]
		if cursorCol <= len(currentLine) {
			beforeCursor := currentLine[:cursorCol]
			afterCursor := currentLine[cursorCol:]

			promptBuilder.WriteString(beforeCursor)
			promptBuilder.WriteString("<|user_cursor_is_here|>")
			promptBuilder.WriteString(afterCursor)
		} else {
			promptBuilder.WriteString(currentLine)
			promptBuilder.WriteString("<|user_cursor_is_here|>")
		}
	} else {
		promptBuilder.WriteString("<|user_cursor_is_here|>")
	}

	for i := cursorLine + 1; i < editableEnd; i++ {
		promptBuilder.WriteString("\n")
		promptBuilder.WriteString(req.Lines[i])
	}

	promptBuilder.WriteString("\n<|editable_region_end|>")

	for i := editableEnd; i < contextEnd; i++ {
		promptBuilder.WriteString("\n")
		promptBuilder.WriteString(req.Lines[i])
	}

	promptBuilder.WriteString("\n```")

	return promptBuilder.String()
}

func formatDiagnosticsForPrompt(req *types.CompletionRequest) string {
	diag := req.GetDiagnostics()
	if diag == nil || len(diag.Errors) == 0 {
		return ""
	}

	var diagBuilder strings.Builder

	diagBuilder.WriteString("Diagnostics in \"")
	diagBuilder.WriteString(diag.RelativeWorkspacePath)
	diagBuilder.WriteString("\":\n")
	diagBuilder.WriteString("```diagnostics\n")

	for _, err := range diag.Errors {
		if err.Range != nil {
			fmt.Fprintf(&diagBuilder, "line %d: ", err.Range.StartLine)
		}

		fmt.Fprintf(&diagBuilder, "[%s] %s", err.Severity, err.Message)

		if err.Source != "" {
			fmt.Fprintf(&diagBuilder, " (source: %s)", err.Source)
		}
		diagBuilder.WriteString("\n")
	}

	diagBuilder.WriteString("```")
	return diagBuilder.String()
}

func formatTreesitterForPrompt(req *types.CompletionRequest) string {
	ts := req.GetTreesitter()
	if ts == nil {
		return ""
	}

	var b strings.Builder

	if ts.EnclosingSignature != "" {
		fmt.Fprintf(&b, "Enclosing scope: %s\n", ts.EnclosingSignature)
	}

	if len(ts.Siblings) > 0 {
		b.WriteString("Sibling symbols:\n")
		for _, s := range ts.Siblings {
			fmt.Fprintf(&b, "  line %d: %s\n", s.Line, s.Signature)
		}
	}

	if len(ts.Imports) > 0 {
		b.WriteString("Imports:\n")
		for _, imp := range ts.Imports {
			fmt.Fprintf(&b, "  %s\n", imp)
		}
	}

	return b.String()
}

func formatGitDiffForPrompt(req *types.CompletionRequest) string {
	gd := req.GetGitDiff()
	if gd == nil || gd.Diff == "" {
		return ""
	}
	return gd.Diff
}

// formatRecentFilesForPrompt renders RecentBufferSnapshots as fenced code blocks.
func formatRecentFilesForPrompt(req *types.CompletionRequest) string {
	if len(req.RecentBufferSnapshots) == 0 {
		return ""
	}
	var b strings.Builder
	for i, snap := range req.RecentBufferSnapshots {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("```")
		b.WriteString(snap.FilePath)
		b.WriteString("\n")
		b.WriteString(strings.Join(snap.Lines, "\n"))
		if len(snap.Lines) > 0 && !strings.HasSuffix(snap.Lines[len(snap.Lines)-1], "\n") {
			b.WriteString("\n")
		}
		b.WriteString("```")
	}
	return b.String()
}

func buildInstructionPrompt(userEdits, diagnostics, treesitterCtx, gitDiffCtx, recentFiles, userExcerpt string) string {
	var promptBuilder strings.Builder

	promptBuilder.WriteString("### Instruction:\n")
	promptBuilder.WriteString("You are a code completion assistant and your task is to analyze user edits and then rewrite an excerpt that the user provides, suggesting the appropriate edits within the excerpt, taking into account the cursor location.\n\n")

	promptBuilder.WriteString("### User Edits:\n\n")
	promptBuilder.WriteString(userEdits)
	promptBuilder.WriteString("\n\n")

	if diagnostics != "" {
		promptBuilder.WriteString("### Diagnostics:\n\n")
		promptBuilder.WriteString(diagnostics)
		promptBuilder.WriteString("\n\n")
	}

	if treesitterCtx != "" {
		promptBuilder.WriteString("### Code Context:\n\n")
		promptBuilder.WriteString(treesitterCtx)
		promptBuilder.WriteString("\n\n")
	}

	if gitDiffCtx != "" {
		promptBuilder.WriteString("### Staged Changes:\n\n")
		promptBuilder.WriteString(gitDiffCtx)
		promptBuilder.WriteString("\n\n")
	}

	if recentFiles != "" {
		promptBuilder.WriteString("### Recent Files:\n\n")
		promptBuilder.WriteString(recentFiles)
		promptBuilder.WriteString("\n\n")
	}

	promptBuilder.WriteString("### User Excerpt:\n\n")
	promptBuilder.WriteString(userExcerpt)
	promptBuilder.WriteString("\n\n")

	promptBuilder.WriteString("### Response:\n")

	return promptBuilder.String()
}

func parseCompletion(p *provider.Provider, ctx *provider.Context) (*types.CompletionResponse, bool) {
	completionText := ctx.Result.Text
	req := ctx.Request

	content := strings.ReplaceAll(completionText, "<|user_cursor_is_here|>", "")

	startMarker := "<|editable_region_start|>"
	endMarker := "<|editable_region_end|>"

	startIdx := strings.Index(content, startMarker)
	if startIdx == -1 {
		return parseSimpleCompletion(p, ctx)
	}

	content = content[startIdx:]

	newlineIdx := strings.Index(content, "\n")
	if newlineIdx == -1 {
		return p.EmptyResponse(), true
	}
	content = content[newlineIdx+1:]

	endIdx := strings.Index(content, "\n"+endMarker)
	var newText string
	if endIdx == -1 {
		newText = content
	} else {
		newText = content[:endIdx]
	}

	editableStart := ctx.WindowStart
	editableEnd := ctx.WindowEnd
	oldLines := req.Lines[editableStart:editableEnd]
	oldText := strings.Join(oldLines, "\n")

	if newText == oldText {
		return p.EmptyResponse(), true
	}

	newLines := strings.Split(newText, "\n")

	endLineInc := ctx.EndLineInc
	if endLineInc == 0 {
		endLineInc = min(editableStart+len(newLines), editableEnd)
	}

	return p.BuildCompletion(ctx, editableStart+1, endLineInc, newLines)
}

func parseSimpleCompletion(p *provider.Provider, ctx *provider.Context) (*types.CompletionResponse, bool) {
	completionText := ctx.Result.Text
	req := ctx.Request

	completionLines := strings.Split(completionText, "\n")
	if len(completionLines) == 0 {
		return p.EmptyResponse(), true
	}

	cursorRow := req.CursorRow
	cursorCol := req.CursorCol

	var resultLines []string

	if cursorRow <= len(req.Lines) {
		currentLine := req.Lines[cursorRow-1]
		beforeCursor := ""
		if cursorCol <= len(currentLine) {
			beforeCursor = currentLine[:cursorCol]
		} else {
			beforeCursor = currentLine
		}
		resultLines = append(resultLines, beforeCursor+completionLines[0])
	} else {
		resultLines = append(resultLines, completionLines[0])
	}

	resultLines = append(resultLines, completionLines[1:]...)

	endLine := cursorRow + len(completionLines) - 1
	if ctx.EndLineInc > 0 {
		endLine = ctx.EndLineInc
	}

	return p.BuildCompletion(ctx, cursorRow, endLine, resultLines)
}
