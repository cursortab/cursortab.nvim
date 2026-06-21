// Package sweep implements the Sweep Next-Edit model provider.
//
// Prompt format (sent as a single text prompt to /v1/completions):
//
//	<|file_sep|>{file_path}               (broad file context ~300 lines)
//	{initial_file_contents}
//
//	<|file_sep|>context/retrieval         (other open files for context)
//	<|file_sep|>utils.py
//	def helper(): ...
//
//	<|file_sep|>context/treesitter        (omitted if no treesitter context)
//	Enclosing scope: func handleRequest(...) {
//	Sibling: func otherFunc() {
//	Import: import "net/http"
//
//	<|file_sep|>context/diagnostics       (omitted if no LSP diagnostics)
//	Line 10: [gopls] undefined: foo
//	Line 15: [gopls] unused variable: bar
//
//	<|file_sep|>{file_path}.diff          (diff history, if any)
//	original:
//	{old_code}
//	updated:
//	{new_code}
//
//	<|file_sep|>context/staged_diff       (omitted if not COMMIT_EDITMSG)
//	+func newHelper(ctx context.Context) error {
//	-func oldHelper() error {
//
//	<|file_sep|>original/file.go:10:30    (current window, no cursor marker)
//	...current lines...
//
//	<|file_sep|>current/file.go:10:30     (current window with cursor marker)
//	...lines before cursor...
//	<|cursor|>line at cursor...
//	...lines after cursor...
//
//	<|file_sep|>updated/file.go:10:30     (prefilled, model completes from here)
//	{prefilled_content}...
//
// Stop tokens: <|file_sep|>, <|endoftext|>
package sweep

import (
	"fmt"
	"strconv"
	"strings"

	"cursortab/client/openai"
	sourcectx "cursortab/ctx"
	"cursortab/provider"
	"cursortab/types"
)

const (
	broadContextLinesBefore = 150
	broadContextLinesAfter  = 150
)

func NewProvider(config *types.ProviderConfig) *provider.Provider {
	return &provider.Provider{
		Name:                          "sweep",
		Config:                        config,
		Client:                        openai.NewClient(config.ProviderURL, config.CompletionPath, config.APIKey),
		StreamingType:                 provider.StreamingLines,
		CompleteWithTextRightOfCursor: true,
		PrefetchAfterCursorTarget:     true,
		BuildContextRequirements: provider.Materials(provider.MaterialOptions{
			Diagnostics: true,
			Treesitter:  true,
			GitDiff:     true,
			RecentFiles: true,
			EditHistory: true,
			UserActions: true,
		}),
		DiffBuilder: provider.FormatDiffHistoryOriginalUpdated("<|file_sep|>%s.diff\n"),
		BuildBatch:  buildBatch,
		ParseBatch:  parseBatch,
		Validators: []provider.Validator{
			provider.ValidateFirstLineAnchor(0.25),
		},
		StopTokens: []string{"<|file_sep|>", "<|endoftext|>"},
	}
}

func buildBatch(p *provider.Provider, ctx *provider.BatchContext) *openai.CompletionRequest {
	return buildPromptFromBatch(p, ctx, func(prefill string) {
		ctx.Prefill = prefill
	})
}

func buildPromptFromBatch(p *provider.Provider, ctx *provider.BatchContext, setPrefill func(string)) *openai.CompletionRequest {
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

		return &openai.CompletionRequest{
			Model:       p.Config.ProviderModel,
			Prompt:      promptBuilder.String(),
			Temperature: p.Config.ProviderTemperature,
			MaxTokens:   p.Config.ProviderMaxTokens,
			TopK:        p.Config.ProviderTopK,
			Stop:        p.StopTokens,
			N:           1,
			Echo:        false,
		}
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
	if recent, ok := sourcectx.Find[sourcectx.RecentFiles](input.Context); ok {
		if section := formatRetrievalSection(recent.Files); section != "" {
			promptBuilder.WriteString(section)
		}
	}

	// Treesitter context
	if treesitter, ok := sourcectx.Find[sourcectx.Treesitter](input.Context); ok {
		if section := formatTreesitterSection(treesitter.Data); section != "" {
			promptBuilder.WriteString(section)
		}
	}

	// Diagnostics context
	if diagnostics, ok := sourcectx.Find[sourcectx.Diagnostics](input.Context); ok {
		if section := formatDiagnosticsSection(diagnostics.Data); section != "" {
			promptBuilder.WriteString(section)
		}
	}

	// Diff history section (recent_changes)
	if editHistory, ok := sourcectx.Find[sourcectx.EditHistory](input.Context); ok && p.DiffBuilder != nil {
		diffSection := p.DiffBuilder(editHistory.Files)
		if diffSection != "" {
			promptBuilder.WriteString(diffSection)
		}
	}

	// Git diff context
	if gitDiff, ok := sourcectx.Find[sourcectx.GitDiff](input.Context); ok {
		if section := formatGitDiffSection(gitDiff.Data); section != "" {
			promptBuilder.WriteString(section)
		}
	}

	cursorLineInWindow := ctx.CursorLine
	codeBlock := strings.Join(ctx.TrimmedLines, "\n")
	relativeCursor := computeRelativeCursor(ctx.TrimmedLines, cursorLineInWindow, current.Cursor.Col)
	if relativeCursor > len(codeBlock) {
		relativeCursor = len(codeBlock)
	}

	startLine := ctx.WindowStart + 1
	endLine := ctx.WindowEnd

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

	// Compute and add prefill
	var actions []*types.UserAction
	if userActions, ok := sourcectx.Find[sourcectx.UserActions](input.Context); ok {
		actions = userActions.Actions
	}
	changesAboveCursor := hasRecentInsertionAboveCursor(actions, cursorLineInWindow, ctx.WindowStart)
	prefill := computePrefill(codeBlock, relativeCursor, changesAboveCursor)
	setPrefill(prefill)
	promptBuilder.WriteString(prefill)

	return &openai.CompletionRequest{
		Model:       p.Config.ProviderModel,
		Prompt:      promptBuilder.String(),
		Temperature: p.Config.ProviderTemperature,
		MaxTokens:   p.Config.ProviderMaxTokens,
		TopK:        p.Config.ProviderTopK,
		Stop:        p.StopTokens,
		N:           1,
		Echo:        false,
	}
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

func computeRelativeCursor(lines []string, cursorLine, cursorCol int) int {
	offset := 0
	for i := 0; i < cursorLine && i < len(lines); i++ {
		offset += len(lines[i]) + 1 // +1 for newline
	}
	return offset + cursorCol
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

func formatGitDiffSection(gd *types.GitDiffContext) string {
	if gd == nil || gd.Diff == "" {
		return ""
	}
	return "<|file_sep|>context/staged_diff\n" + gd.Diff
}

func parseBatchCompletion(p *provider.Provider, ctx *provider.BatchContext, result *openai.StreamResult) (*types.CompletionResponse, bool) {
	completionText := result.Text
	lines := ctx.Input.Current.File.Lines

	completionText = strings.TrimSuffix(completionText, "<|file_sep|>")
	completionText = strings.TrimSuffix(completionText, "<|endoftext|>")
	completionText = strings.TrimRight(completionText, " \t\n\r")

	windowStart := ctx.WindowStart
	windowEnd := ctx.WindowEnd
	if windowStart < 0 {
		windowStart = 0
	}
	if windowEnd > len(lines) {
		windowEnd = len(lines)
	}
	if windowStart >= windowEnd || windowStart >= len(lines) {
		return p.EmptyResponse(), true
	}

	oldLines := lines[windowStart:windowEnd]
	oldText := strings.TrimRight(strings.Join(oldLines, "\n"), " \t\n\r")

	if completionText == oldText {
		return p.EmptyResponse(), true
	}

	newLines := strings.Split(completionText, "\n")

	endLineInc := ctx.EndLineInc
	if endLineInc == 0 {
		endLineInc = min(windowStart+len(newLines), windowEnd)
	}

	return p.BuildBatchCompletion(ctx, windowStart+1, endLineInc, newLines)
}
