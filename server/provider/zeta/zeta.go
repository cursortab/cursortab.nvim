// Package zeta implements Zed-style edit prediction prompts.
package zeta

import (
	"context"
	"fmt"
	"strings"

	"cursortab/client/openai"
	sourcectx "cursortab/ctx"
	"cursortab/engine"
	"cursortab/provider"
	"cursortab/types"
)

var stopTokens = []string{"\n<|editable_region_end|>"}

const providerName = "zeta"

type Provider struct {
	provider.Base
	provider.OpenAI
}

var _ engine.Provider = (*Provider)(nil)
var _ engine.StreamingProvider = (*Provider)(nil)
var _ provider.OpenAIStreamFlow = (*Provider)(nil)

func NewProvider(config *types.ProviderConfig) *Provider {
	return &Provider{
		Base: provider.NewBase(engine.CompletionEdit, sourcectx.Materials{
			sourcectx.Diagnostics{}, sourcectx.Treesitter{}, sourcectx.GitDiff{},
			sourcectx.RecentFiles{}, sourcectx.EditHistory{},
		}, provider.SyntheticPrefetchEnabled),
		OpenAI: provider.NewOpenAI(providerName, config),
	}
}

func (p *Provider) Complete(ctx context.Context, input sourcectx.CompletionInput) (*types.CompletionResponse, error) {
	return provider.StartBatch(ctx, input, p.ProviderConfig(), p)
}

func (p *Provider) StreamCompletion(ctx context.Context, input sourcectx.CompletionInput) (engine.CompletionStream, error) {
	return p.OpenAI.StartStream(ctx, input, p.ProviderConfig(), p)
}

func (p *Provider) Build(ctx *provider.RequestState) (*openai.CompletionRequest, error) {
	input := ctx.Input

	userExcerpt := buildUserExcerpt(input.Current, ctx)
	userEdits := ""
	if editHistory, ok := sourcectx.Find[sourcectx.EditHistory](input.Materials); ok {
		userEdits = provider.FormatDiffHistory(editHistory.Files, provider.DiffHistoryOptions{
			HeaderTemplate: "User edited %q:\n",
			Prefix:         "```diff\n",
			Suffix:         "\n```",
			Separator:      "\n\n",
		})
	}
	diagnosticsText := ""
	if diagnostics, ok := sourcectx.Find[sourcectx.Diagnostics](input.Materials); ok {
		diagnosticsText = formatDiagnosticsForPrompt(diagnostics.Data)
	}
	treesitterText := ""
	if treesitter, ok := sourcectx.Find[sourcectx.Treesitter](input.Materials); ok {
		treesitterText = formatTreesitterForPrompt(treesitter.Data)
	}
	gitDiffText := ""
	if gitDiff, ok := sourcectx.Find[sourcectx.GitDiff](input.Materials); ok {
		gitDiffText = formatGitDiffForPrompt(gitDiff.Data)
	}
	recentFilesText := ""
	if recentFiles, ok := sourcectx.Find[sourcectx.RecentFiles](input.Materials); ok {
		recentFilesText = formatRecentFilesForPrompt(recentFiles.Files)
	}
	prompt := buildInstructionPrompt(userEdits, diagnosticsText, treesitterText, gitDiffText, recentFilesText, userExcerpt)

	req := p.Request(prompt, stopTokens)
	p.LogRequest(req, ctx.Window.MaxLines)
	return req, nil
}

func buildUserExcerpt(current sourcectx.CurrentSnapshot, ctx *provider.RequestState) string {
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

	editableStart := ctx.Window.Start
	editableEnd := ctx.Window.Start + len(ctx.Window.Lines)

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

func (p *Provider) Parse(ctx *provider.RequestState, result *openai.CompletionResult) (*types.CompletionResponse, error) {
	text := result.Text
	if resp, done := provider.RejectEmptyText(providerName, text); done {
		return resp, nil
	}
	if stripped, resp, done := provider.StripRepetitionText(text); done {
		return resp, nil
	} else {
		text = stripped
	}
	if resp, done := provider.ValidateAnchorPositionText(providerName, ctx, text, 0.25); done {
		return resp, nil
	}
	text, endLineInc, resp, done := provider.AnchorTruncationText(providerName, ctx, text, result.FinishReason, result.StoppedEarly, 0.75)
	if done {
		return resp, nil
	}
	return parseCompletion(ctx, text, endLineInc), nil
}

func (p *Provider) StreamArgs(state *provider.RequestState) provider.OpenAIStreamArgs {
	windowStart, oldLines := defaultStreamWindow(state)
	return provider.OpenAIStreamArgs{
		WindowStart:        windowStart,
		OldLines:           oldLines,
		FirstLineValidator: provider.FirstLineAnchorChecker(0.25),
	}
}

func defaultStreamWindow(state *provider.RequestState) (int, []string) {
	oldLines := state.Window.Lines
	if len(oldLines) == 0 {
		oldLines = state.Input.Current.File.Lines
	}
	return state.Window.Start, oldLines
}

func parseCompletion(ctx *provider.RequestState, completionText string, endLineInc int) *types.CompletionResponse {
	lines := ctx.Input.Current.File.Lines

	content := strings.ReplaceAll(completionText, "<|user_cursor_is_here|>", "")

	startMarker := "<|editable_region_start|>"
	endMarker := "<|editable_region_end|>"

	startIdx := strings.Index(content, startMarker)
	if startIdx == -1 {
		return parseSimpleCompletion(ctx, completionText, endLineInc)
	}

	content = content[startIdx:]

	newlineIdx := strings.Index(content, "\n")
	if newlineIdx == -1 {
		return provider.EmptyResponse()
	}
	content = content[newlineIdx+1:]

	endIdx := strings.Index(content, "\n"+endMarker)
	var newText string
	if endIdx == -1 {
		newText = content
	} else {
		newText = content[:endIdx]
	}

	editableStart := ctx.Window.Start
	editableEnd := ctx.Window.Start + len(ctx.Window.Lines)
	oldLines := lines[editableStart:editableEnd]
	oldText := strings.Join(oldLines, "\n")

	if newText == oldText {
		return provider.EmptyResponse()
	}

	newLines := strings.Split(newText, "\n")

	if endLineInc == 0 {
		endLineInc = min(editableStart+len(newLines), editableEnd)
	}

	return provider.BuildCompletion(ctx, editableStart+1, endLineInc, newLines)
}

func parseSimpleCompletion(ctx *provider.RequestState, completionText string, endLineInc int) *types.CompletionResponse {
	current := ctx.Input.Current

	completionLines := strings.Split(completionText, "\n")
	if len(completionLines) == 0 {
		return provider.EmptyResponse()
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
	if endLineInc > 0 {
		endLine = endLineInc
	}

	return provider.BuildCompletion(ctx, cursorRow, endLine, resultLines)
}
