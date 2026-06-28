// Package sweep implements the Sweep next-edit provider.
package sweep

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"cursortab/client/openai"
	sourcectx "cursortab/ctx"
	"cursortab/engine"
	"cursortab/provider"
	"cursortab/types"
)

const (
	broadContextLinesBefore = 150
	broadContextLinesAfter  = 150
)

var stopTokens = []string{"<|file_sep|>", "<|endoftext|>"}

const providerName = "sweep"

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
			sourcectx.RecentFiles{}, sourcectx.EditHistory{}, sourcectx.UserActions{},
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
	current := input.Current
	lines := current.File.Lines
	var promptBuilder strings.Builder

	if len(lines) == 0 {
		promptBuilder.WriteString("<|file_sep|>original/")
		promptBuilder.WriteString(current.File.Path)
		promptBuilder.WriteString("\n\n")
		promptBuilder.WriteString("<|file_sep|>current/")
		promptBuilder.WriteString(current.File.Path)
		promptBuilder.WriteString("\n\n")
		promptBuilder.WriteString("<|file_sep|>updated/")
		promptBuilder.WriteString(current.File.Path)
		promptBuilder.WriteString("\n")

		req := p.Request(promptBuilder.String(), stopTokens)
		p.LogRequest(req, ctx.Window.MaxLines)
		return req, nil
	}

	// Broad file context (initial_file) - ~300 lines around cursor
	initialFile := getBroadFileContext(current)
	if initialFile != "" {
		promptBuilder.WriteString("<|file_sep|>")
		promptBuilder.WriteString(current.File.Path)
		promptBuilder.WriteString("\n")
		promptBuilder.WriteString(initialFile)
		promptBuilder.WriteString("\n")
	}

	// Cross-file context (retrieval chunks from recent files)
	if recent, ok := sourcectx.Find[sourcectx.RecentFiles](input.Materials); ok {
		if section := formatRetrievalSection(recent.Files); section != "" {
			promptBuilder.WriteString(section)
		}
	}

	// Treesitter context
	if treesitter, ok := sourcectx.Find[sourcectx.Treesitter](input.Materials); ok {
		if section := formatTreesitterSection(treesitter.Data); section != "" {
			promptBuilder.WriteString(section)
		}
	}

	// Diagnostics context
	if diagnostics, ok := sourcectx.Find[sourcectx.Diagnostics](input.Materials); ok {
		if section := formatDiagnosticsSection(diagnostics.Data); section != "" {
			promptBuilder.WriteString(section)
		}
	}

	// Diff history section (recent_changes)
	if editHistory, ok := sourcectx.Find[sourcectx.EditHistory](input.Materials); ok {
		if diffSection := formatDiffHistoryOriginalUpdated(editHistory.Files, "<|file_sep|>%s.diff\n"); diffSection != "" {
			promptBuilder.WriteString(diffSection)
		}
	}

	// Git diff context
	if gitDiff, ok := sourcectx.Find[sourcectx.GitDiff](input.Materials); ok {
		if section := formatGitDiffSection(gitDiff.Data); section != "" {
			promptBuilder.WriteString(section)
		}
	}

	cursorLineInWindow := ctx.Window.CursorLine
	codeBlock := strings.Join(ctx.Window.Lines, "\n")
	relativeCursor := computeRelativeCursor(ctx.Window.Lines, cursorLineInWindow, current.Cursor.Col)
	if relativeCursor > len(codeBlock) {
		relativeCursor = len(codeBlock)
	}

	startLine := ctx.Window.Start + 1
	endLine := ctx.Window.Start + len(ctx.Window.Lines)

	promptBuilder.WriteString("<|file_sep|>original/")
	promptBuilder.WriteString(current.File.Path)
	promptBuilder.WriteString(":")
	promptBuilder.WriteString(fmt.Sprintf("%d:%d", startLine, endLine))
	promptBuilder.WriteString("\n")
	promptBuilder.WriteString(codeBlock)
	promptBuilder.WriteString("\n")

	// Current section (with cursor marker)
	codeBlockWithCursor := codeBlock[:relativeCursor] + "<|cursor|>" + codeBlock[relativeCursor:]
	promptBuilder.WriteString("<|file_sep|>current/")
	promptBuilder.WriteString(current.File.Path)
	promptBuilder.WriteString(":")
	promptBuilder.WriteString(fmt.Sprintf("%d:%d", startLine, endLine))
	promptBuilder.WriteString("\n")
	promptBuilder.WriteString(codeBlockWithCursor)
	promptBuilder.WriteString("\n")

	// Updated section (with prefill)
	promptBuilder.WriteString("<|file_sep|>updated/")
	promptBuilder.WriteString(current.File.Path)
	promptBuilder.WriteString(":")
	promptBuilder.WriteString(fmt.Sprintf("%d:%d", startLine, endLine))
	promptBuilder.WriteString("\n")

	prefill := computePrefillForContext(ctx, codeBlock, relativeCursor)
	promptBuilder.WriteString(prefill)

	req := p.Request(promptBuilder.String(), stopTokens)
	p.LogRequest(req, ctx.Window.MaxLines)
	return req, nil
}

func (p *Provider) Parse(ctx *provider.RequestState, result *openai.CompletionResult) (*types.CompletionResponse, error) {
	text := prefillForState(ctx) + result.Text
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
		Prefill:            prefillForState(state),
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

func computeRelativeCursor(lines []string, cursorLine, cursorCol int) int {
	offset := 0
	for i := 0; i < cursorLine && i < len(lines); i++ {
		offset += len(lines[i]) + 1 // +1 for newline
	}
	return offset + cursorCol
}

func prefillForState(ctx *provider.RequestState) string {
	if len(ctx.Window.Lines) == 0 {
		return ""
	}
	codeBlock := strings.Join(ctx.Window.Lines, "\n")
	relativeCursor := computeRelativeCursor(ctx.Window.Lines, ctx.Window.CursorLine, ctx.Input.Current.Cursor.Col)
	if relativeCursor > len(codeBlock) {
		relativeCursor = len(codeBlock)
	}
	return computePrefillForContext(ctx, codeBlock, relativeCursor)
}

func computePrefillForContext(ctx *provider.RequestState, codeBlock string, relativeCursor int) string {
	var actions []*types.UserAction
	if userActions, ok := sourcectx.Find[sourcectx.UserActions](ctx.Input.Materials); ok {
		actions = userActions.Actions
	}
	changesAboveCursor := hasRecentInsertionAboveCursor(actions, ctx.Window.CursorLine, ctx.Window.Start)
	return computePrefill(codeBlock, relativeCursor, changesAboveCursor)
}

// computePrefill returns the prefix of the updated section that we feed to
// the model so it only generates from the edit point.
//
// Insertion mode (changesAboveCursor): prefill only the first line + trailing
// blank lines, giving the model freedom to rewrite lines shifted by the insert.
//
// Default mode: prefill everything before the cursor line.
func computePrefill(codeBlock string, relativeCursor int, changesAboveCursor bool) string {
	if changesAboveCursor {
		prefill := codeBlock[:relativeCursor]
		prefilledLines := strings.Split(prefill, "\n")
		if len(prefilledLines) <= 1 {
			return prefill
		}

		// strings.Split consumes the \n delimiter; restore it after the first line
		result := prefilledLines[0] + "\n"

		// Preserve consecutive blank lines but stop at first real content
		afterSplit := strings.Join(prefilledLines[1:], "\n")
		for _, ch := range afterSplit {
			if ch == '\n' {
				result += "\n"
			} else {
				break
			}
		}

		return result
	}

	prefixBeforeCursor := codeBlock[:relativeCursor]
	if !strings.Contains(prefixBeforeCursor, "\n") {
		return ""
	}
	prefillEnd := strings.LastIndex(prefixBeforeCursor, "\n") + 1
	return codeBlock[:prefillEnd]
}

func hasRecentInsertionAboveCursor(actions []*types.UserAction, cursorLineInWindow, windowStart int) bool {
	if len(actions) == 0 {
		return false
	}

	lastAction := actions[len(actions)-1]
	if lastAction.ActionType != types.ActionInsertChar &&
		lastAction.ActionType != types.ActionInsertSelection {
		return false
	}

	// Convert 1-indexed file line to 0-indexed window-relative line
	lastActionLineInWindow := lastAction.LineNumber - 1 - windowStart
	return lastActionLineInWindow < cursorLineInWindow
}

// getBroadFileContext returns ~300 lines of context around the cursor.
func getBroadFileContext(current sourcectx.CurrentSnapshot) string {
	lines := current.File.Lines
	if len(lines) == 0 {
		return ""
	}

	cursorLine := current.Cursor.Row - 1 // Convert to 0-indexed

	contextStart := cursorLine - broadContextLinesBefore
	if contextStart < 0 {
		contextStart = 0
	}

	contextEnd := cursorLine + broadContextLinesAfter + 1
	if contextEnd > len(lines) {
		contextEnd = len(lines)
	}

	return strings.Join(lines[contextStart:contextEnd], "\n")
}

func formatTreesitterSection(ts *types.TreesitterContext) string {
	if ts == nil {
		return ""
	}

	var b strings.Builder
	b.WriteString("<|file_sep|>context/treesitter\n")

	if ts.EnclosingSignature != "" {
		fmt.Fprintf(&b, "Enclosing scope: %s\n", ts.EnclosingSignature)
	}

	for _, s := range ts.Siblings {
		fmt.Fprintf(&b, "Sibling: %s\n", s.Signature)
	}

	for _, imp := range ts.Imports {
		fmt.Fprintf(&b, "Import: %s\n", imp)
	}

	return b.String()
}

func formatDiagnosticsSection(diag *types.Diagnostics) string {
	if diag == nil || len(diag.Items) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("<|file_sep|>context/diagnostics\n")

	for _, err := range diag.Items {
		if err.Range != nil {
			b.WriteString("Line ")
			b.WriteString(strconv.Itoa(err.Range.StartLine))
			b.WriteString(": ")
		}
		if err.Source != "" {
			b.WriteString("[")
			b.WriteString(err.Source)
			b.WriteString("] ")
		}
		b.WriteString(err.Message)
		b.WriteString("\n")
	}

	return b.String()
}

func formatRetrievalSection(snapshots []*types.RecentBufferSnapshot) string {
	if len(snapshots) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("<|file_sep|>context/retrieval\n")

	for _, snapshot := range snapshots {
		b.WriteString("<|file_sep|>")
		b.WriteString(snapshot.FilePath)
		b.WriteString("\n")
		b.WriteString(strings.Join(snapshot.Lines, "\n"))
		b.WriteString("\n")
	}

	return b.String()
}

func formatDiffHistoryOriginalUpdated(history []*types.FileDiffHistory, headerTemplate string) string {
	if len(history) == 0 {
		return ""
	}

	var b strings.Builder
	for _, fileHistory := range history {
		if len(fileHistory.DiffHistory) == 0 {
			continue
		}
		for _, diffEntry := range fileHistory.DiffHistory {
			if diffEntry.Original == diffEntry.Updated {
				continue
			}
			if headerTemplate != "" {
				fmt.Fprintf(&b, headerTemplate, fileHistory.FileName)
			}
			b.WriteString("original:\n")
			b.WriteString(diffEntry.Original)
			b.WriteString("\nupdated:\n")
			b.WriteString(diffEntry.Updated)
			b.WriteString("\n")
		}
	}
	return b.String()
}

func formatGitDiffSection(gd *types.GitDiffContext) string {
	if gd == nil || gd.Diff == "" {
		return ""
	}
	return "<|file_sep|>context/staged_diff\n" + gd.Diff
}

func parseCompletion(ctx *provider.RequestState, completionText string, endLineInc int) *types.CompletionResponse {
	lines := ctx.Input.Current.File.Lines

	completionText = strings.TrimSuffix(completionText, "<|file_sep|>")
	completionText = strings.TrimSuffix(completionText, "<|endoftext|>")
	completionText = strings.TrimRight(completionText, " \t\n\r")

	windowStart := ctx.Window.Start
	windowEnd := ctx.Window.Start + len(ctx.Window.Lines)
	if windowStart < 0 {
		windowStart = 0
	}
	if windowEnd > len(lines) {
		windowEnd = len(lines)
	}
	if windowStart >= windowEnd || windowStart >= len(lines) {
		return provider.EmptyResponse()
	}

	oldLines := lines[windowStart:windowEnd]
	oldText := strings.TrimRight(strings.Join(oldLines, "\n"), " \t\n\r")

	if completionText == oldText {
		return provider.EmptyResponse()
	}

	newLines := strings.Split(completionText, "\n")

	if endLineInc == 0 {
		endLineInc = min(windowStart+len(newLines), windowEnd)
	}

	return provider.BuildCompletion(ctx, windowStart+1, endLineInc, newLines)
}
