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
//	line 10: [ERROR] undefined: foo (source: gopls)
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
	sourcectx "cursortab/ctx"
	"cursortab/provider"
	"cursortab/types"
)

// NewProvider creates a new Zeta provider (Zed's native model)
func NewProvider(config *types.ProviderConfig) *provider.Provider {
	return &provider.Provider{
		Name:                          "zeta",
		Config:                        config,
		Client:                        openai.NewClient(config.ProviderURL, config.CompletionPath, config.APIKey),
		StreamingType:                 provider.StreamingLines,
		CompleteWithTextRightOfCursor: true,
		PrefetchAfterCursorTarget:     true,
		Preprocessors: []provider.Preprocessor{
			provider.TrimContent(),
		},
		BuildContextRequirements: provider.Materials(provider.MaterialOptions{
			Diagnostics: true,
			Treesitter:  true,
			GitDiff:     true,
			RecentFiles: true,
			EditHistory: true,
		}),
		DiffBuilder: provider.FormatDiffHistory(provider.DiffHistoryOptions{
			HeaderTemplate: "User edited %q:\n",
			Prefix:         "```diff\n",
			Suffix:         "\n```",
			Separator:      "\n\n",
		}),
		PromptBuilder: buildPrompt,
		BuildBatch:    buildBatch,
		ParseBatch:    parseBatch,
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

func buildBatch(p *provider.Provider, ctx *provider.BatchContext) *openai.CompletionRequest {
	return buildPromptFromBatch(p, ctx)
}

func buildPrompt(p *provider.Provider, ctx *provider.Context) *openai.CompletionRequest {
	return buildPromptFromBatch(p, &provider.BatchContext{
		Input:        ctx.Input,
		TrimmedLines: ctx.TrimmedLines,
		WindowStart:  ctx.WindowStart,
		WindowEnd:    ctx.WindowEnd,
		CursorLine:   ctx.CursorLine,
		MaxLines:     ctx.MaxLines,
		EndLineInc:   ctx.EndLineInc,
	})
}

func buildPromptFromBatch(p *provider.Provider, ctx *provider.BatchContext) *openai.CompletionRequest {
	input := ctx.Input

	userExcerpt := buildUserExcerpt(input.Current, ctx)
	userEdits := ""
	if editHistory, ok := sourcectx.Find[sourcectx.EditHistory](input.Context); ok && p.DiffBuilder != nil {
		userEdits = p.DiffBuilder(editHistory.Files)
	}
	diagnosticsText := ""
	if diagnostics, ok := sourcectx.Find[sourcectx.Diagnostics](input.Context); ok {
		diagnosticsText = formatDiagnosticsForPrompt(diagnostics.Data)
	}
	treesitterText := ""
	if treesitter, ok := sourcectx.Find[sourcectx.Treesitter](input.Context); ok {
		treesitterText = formatTreesitterForPrompt(treesitter.Data)
	}
	gitDiffText := ""
	if gitDiff, ok := sourcectx.Find[sourcectx.GitDiff](input.Context); ok {
		gitDiffText = formatGitDiffForPrompt(gitDiff.Data)
	}
	recentFilesText := ""
	if recentFiles, ok := sourcectx.Find[sourcectx.RecentFiles](input.Context); ok {
		recentFilesText = formatRecentFilesForPrompt(recentFiles.Files)
	}
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

func buildUserExcerpt(current sourcectx.CurrentSnapshot, ctx *provider.BatchContext) string {
	var promptBuilder strings.Builder
	lines := current.File.Lines

	if len(lines) == 0 {
		promptBuilder.WriteString("```")
		promptBuilder.WriteString(current.File.Path)
		promptBuilder.WriteString("\n<|start_of_file|>\n<|editable_region_start|>\n<|user_cursor_is_here|>\n<|editable_region_end|>\n```")
		return promptBuilder.String()
	}

	cursorRow := current.Cursor.Row
	cursorCol := current.Cursor.Col
	cursorLine := cursorRow - 1

	editableStart := ctx.WindowStart
	editableEnd := ctx.WindowEnd

	contextLinesBefore := 5
	contextLinesAfter := 5

	contextStart := max(0, editableStart-contextLinesBefore)
	contextEnd := min(len(lines), editableEnd+contextLinesAfter)

	promptBuilder.WriteString("```")
	promptBuilder.WriteString(current.File.Path)
	promptBuilder.WriteString("\n")

	if contextStart == 0 {
		promptBuilder.WriteString("<|start_of_file|>\n")
	}

	for i := contextStart; i < editableStart; i++ {
		promptBuilder.WriteString(lines[i])
		promptBuilder.WriteString("\n")
	}

	promptBuilder.WriteString("<|editable_region_start|>\n")

	for i := editableStart; i < cursorLine; i++ {
		promptBuilder.WriteString(lines[i])
		promptBuilder.WriteString("\n")
	}

	if cursorLine < len(lines) {
		currentLine := lines[cursorLine]
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
		promptBuilder.WriteString(lines[i])
	}

	promptBuilder.WriteString("\n<|editable_region_end|>")

	for i := editableEnd; i < contextEnd; i++ {
		promptBuilder.WriteString("\n")
		promptBuilder.WriteString(lines[i])
	}

	promptBuilder.WriteString("\n```")

	return promptBuilder.String()
}

func formatDiagnosticsForPrompt(diag *types.Diagnostics) string {
	text := provider.FormatDiagnosticsText(diag)
	if text == "" {
		return ""
	}

	var b strings.Builder
	b.WriteString("Diagnostics in \"")
	b.WriteString(diag.FilePath)
	b.WriteString("\":\n```diagnostics\n")
	b.WriteString(text)
	b.WriteString("```")
	return b.String()
}

func formatTreesitterForPrompt(ts *types.TreesitterContext) string {
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

func formatGitDiffForPrompt(gd *types.GitDiffContext) string {
	if gd == nil || gd.Diff == "" {
		return ""
	}
	return gd.Diff
}

// formatRecentFilesForPrompt renders recent files as fenced code blocks.
func formatRecentFilesForPrompt(snapshots []*types.RecentBufferSnapshot) string {
	if len(snapshots) == 0 {
		return ""
	}
	var b strings.Builder
	for i, snap := range snapshots {
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

func parseBatch(p *provider.Provider, ctx *provider.BatchContext, result *openai.StreamResult) (*types.CompletionResponse, bool) {
	if resp, done := provider.RejectEmptyBatch(p, result); done {
		return resp, true
	}
	if resp, done := provider.StripRepetitionBatch(p, result); done {
		return resp, true
	}
	if resp, done := provider.ValidateAnchorPositionBatch(p, ctx, result, 0.25); done {
		return resp, true
	}
	if resp, done := provider.AnchorTruncationBatch(p, ctx, result, 0.75); done {
		return resp, true
	}
	return parseBatchCompletion(p, ctx, result)
}

func parseCompletion(p *provider.Provider, ctx *provider.Context) (*types.CompletionResponse, bool) {
	input := ctx.Input
	if ctx.Request != nil && input.Current.File.Lines == nil {
		input.Trigger = ctx.Request.Source
		input.Current.Workspace.Path = ctx.Request.WorkspacePath
		input.Current.Workspace.ID = ctx.Request.WorkspaceID
		input.Current.File.Path = ctx.Request.FilePath
		input.Current.File.Lines = ctx.Request.Lines
		input.Current.File.Version = ctx.Request.Version
		input.Current.Cursor.Row = ctx.Request.CursorRow
		input.Current.Cursor.Col = ctx.Request.CursorCol
		input.Current.View.ViewportHeight = ctx.Request.ViewportHeight
		input.Current.View.MaxVisibleLines = ctx.Request.MaxVisibleLines
	}
	return parseBatchCompletion(p, &provider.BatchContext{
		Input:        input,
		TrimmedLines: ctx.TrimmedLines,
		WindowStart:  ctx.WindowStart,
		WindowEnd:    ctx.WindowEnd,
		CursorLine:   ctx.CursorLine,
		MaxLines:     ctx.MaxLines,
		EndLineInc:   ctx.EndLineInc,
		Prefill:      ctx.Prefill,
	}, ctx.Result)
}

func parseBatchCompletion(p *provider.Provider, ctx *provider.BatchContext, result *openai.StreamResult) (*types.CompletionResponse, bool) {
	completionText := result.Text
	lines := ctx.Input.Current.File.Lines

	content := strings.ReplaceAll(completionText, "<|user_cursor_is_here|>", "")

	startMarker := "<|editable_region_start|>"
	endMarker := "<|editable_region_end|>"

	startIdx := strings.Index(content, startMarker)
	if startIdx == -1 {
		return parseSimpleBatchCompletion(p, ctx, result)
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
	oldLines := lines[editableStart:editableEnd]
	oldText := strings.Join(oldLines, "\n")

	if newText == oldText {
		return p.EmptyResponse(), true
	}

	newLines := strings.Split(newText, "\n")

	endLineInc := ctx.EndLineInc
	if endLineInc == 0 {
		endLineInc = min(editableStart+len(newLines), editableEnd)
	}

	return p.BuildBatchCompletion(ctx, editableStart+1, endLineInc, newLines)
}

func parseSimpleBatchCompletion(p *provider.Provider, ctx *provider.BatchContext, result *openai.StreamResult) (*types.CompletionResponse, bool) {
	completionText := result.Text
	current := ctx.Input.Current

	completionLines := strings.Split(completionText, "\n")
	if len(completionLines) == 0 {
		return p.EmptyResponse(), true
	}

	cursorRow := current.Cursor.Row
	cursorCol := current.Cursor.Col

	var resultLines []string

	if cursorRow <= len(current.File.Lines) {
		currentLine := current.File.Lines[cursorRow-1]
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

	return p.BuildBatchCompletion(ctx, cursorRow, endLine, resultLines)
}
