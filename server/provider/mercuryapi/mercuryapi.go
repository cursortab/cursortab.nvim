// Package mercuryapi implements the Mercury API provider (Inception Labs hosted).
//
// Prompt format (sent as a single user message to /v1/edit/completions):
//
//	<|recently_viewed_code_snippets|>
//	<|recently_viewed_code_snippet|>
//	code_snippet_file_path: helper.go
//	func helper() string { ... }
//	<|/recently_viewed_code_snippet|>
//
//	<|recently_viewed_code_snippet|>           (omitted if no diagnostics)
//	code_snippet_file_path: diagnostics
//	line 12: [error] undefined: foo (source: gopls)
//	<|/recently_viewed_code_snippet|>
//
//	<|recently_viewed_code_snippet|>           (omitted if no treesitter context)
//	code_snippet_file_path: treesitter_context
//	Enclosing scope: func process(items []string) []string {
//	Sibling: line 15: func main() {
//	Import: import "fmt"
//	<|/recently_viewed_code_snippet|>
//
//	<|recently_viewed_code_snippet|>           (omitted if no staged changes)
//	code_snippet_file_path: staged_git_diff
//	+func newHelper() {}
//	-func oldHelper() {}
//	<|/recently_viewed_code_snippet|>
//	<|/recently_viewed_code_snippets|>
//
//	<|current_file_content|>
//	current_file_path: main.go
//	...context lines above editable region...
//	<|code_to_edit|>
//	...before cursor...<|cursor|>...after cursor...
//	...more editable lines...
//	<|/code_to_edit|>
//	...context lines below editable region...
//	<|/current_file_content|>
//
//	<|edit_diff_history|>
//	--- main.go
//	+++ main.go
//	@@ -6,1 +6,1 @@
//	-old line
//	+new line
//	<|/edit_diff_history|>
//
// The editable region is expanded to syntax boundaries (AST nodes) when
// treesitter data is available. Context defaults to the entire file, trimmed
// only for files exceeding 150KB.
package mercuryapi

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"cursortab/client/mercuryapi"
	"cursortab/engine"
	"cursortab/logger"
	"cursortab/metrics"
	"cursortab/provider"
	"cursortab/types"
	"cursortab/utils"
)

// Token limits (characters, approximating 1 token ~= 3 chars)
const (
	MaxRewriteChars = 450    // ~150 tokens for editable region
	MaxFileChars    = 150000 // trim files larger than this
)

// Prompt format constants
const (
	RecentlyViewedSnippetsStart = "<|recently_viewed_code_snippets|>\n"
	RecentlyViewedSnippetsEnd   = "<|/recently_viewed_code_snippets|>\n"
	RecentlyViewedSnippetStart  = "<|recently_viewed_code_snippet|>\n"
	RecentlyViewedSnippetEnd    = "<|/recently_viewed_code_snippet|>\n"
	CurrentFileContentStart     = "<|current_file_content|>\n"
	CurrentFileContentEnd       = "<|/current_file_content|>\n"
	CodeToEditStart             = "<|code_to_edit|>\n"
	CodeToEditEnd               = "<|/code_to_edit|>\n"
	EditDiffHistoryStart        = "<|edit_diff_history|>\n"
	EditDiffHistoryEnd          = "<|/edit_diff_history|>\n"
	CursorTag                   = "<|cursor|>"
	CodeSnippetFilePathPrefix   = "code_snippet_file_path: "
	CurrentFilePathPrefix       = "current_file_path: "
)

// Provider implements the Mercury API provider
type Provider struct {
	config *types.ProviderConfig
	client *mercuryapi.Client
}

// NewProvider creates a new Mercury API provider
func NewProvider(config *types.ProviderConfig) *Provider {
	return &Provider{
		config: config,
		client: mercuryapi.NewClient(config.ProviderURL, config.APIKey, config.CompletionTimeout),
	}
}

// GetContextLimits implements engine.Provider
func (p *Provider) GetContextLimits() engine.ContextLimits {
	return engine.DefaultContextLimits()
}

// SetHTTPTransport forwards the transport override to the underlying client.
// Used by the eval harness for cassette record/replay.
func (p *Provider) SetHTTPTransport(rt http.RoundTripper) {
	p.client.SetHTTPTransport(rt)
}

// SendMetric implements metrics.Sender
func (p *Provider) SendMetric(ctx context.Context, event metrics.Event) {
	var action mercuryapi.FeedbackAction
	switch event.Type {
	case metrics.EventShown:
		// Mercury doesn't have a "shown" event, only accept/reject/ignore
		return
	case metrics.EventAccepted:
		action = mercuryapi.FeedbackAccept
	case metrics.EventRejected:
		action = mercuryapi.FeedbackReject
	case metrics.EventIgnored:
		action = mercuryapi.FeedbackIgnore
	default:
		return
	}

	req := &mercuryapi.FeedbackRequest{
		RequestID:       event.Info.ID,
		ProviderName:    "cursortab-nvim",
		UserAction:      action,
		ProviderVersion: p.config.Version,
	}
	if err := p.client.SendFeedback(ctx, req); err != nil {
		logger.Warn("mercuryapi: failed to send %s feedback: %v", event.Type, err)
	}
}

// GetCompletion implements engine.Provider
func (p *Provider) GetCompletion(ctx context.Context, req *types.CompletionRequest) (*types.CompletionResponse, error) {
	defer logger.Trace("mercuryapi.GetCompletion")()

	if len(req.Lines) == 0 {
		return &types.CompletionResponse{}, nil
	}

	// Calculate editable and context regions
	var syntaxRanges []*types.LineRange
	if ts := req.GetTreesitter(); ts != nil {
		syntaxRanges = ts.SyntaxRanges
	}
	editableStart, editableEnd, contextStart, contextEnd := computeRegions(req.Lines, req.CursorRow, syntaxRanges)

	// Build the prompt
	prompt := buildPrompt(
		req.FilePath,
		req.Lines,
		editableStart, editableEnd,
		contextStart, contextEnd,
		req.CursorRow, req.CursorCol,
		req.FileDiffHistories,
		req.RecentBufferSnapshots,
		req.GetDiagnostics(),
		req.GetTreesitter(),
		req.GetGitDiff(),
	)

	// Build API request
	model := p.config.ProviderModel
	if model == "" {
		model = mercuryapi.Model
	}

	apiReq := &mercuryapi.Request{
		Model: model,
		Messages: []mercuryapi.Message{
			{Role: "user", Content: prompt},
		},
		Stream: false,
	}

	p.logRequest(apiReq, editableStart, editableEnd, contextStart, contextEnd)

	apiResp, err := p.client.DoCompletion(ctx, apiReq)
	if err != nil {
		return nil, err
	}

	completionText := mercuryapi.ExtractCompletion(apiResp)

	p.logResponse(apiResp, completionText)

	if completionText == "" {
		return &types.CompletionResponse{}, nil
	}

	newLines := strings.Split(completionText, "\n")

	originalEditable := req.Lines[editableStart-1 : editableEnd]
	if slices.Equal(newLines, originalEditable) {
		return &types.CompletionResponse{}, nil
	}

	// Calculate metrics info for the engine
	additions, deletions := countChanges(editableEnd-editableStart+1, len(newLines))

	return &types.CompletionResponse{
		Completions: []*types.Completion{{
			StartLine:  editableStart,
			EndLineInc: editableEnd,
			Lines:      newLines,
		}},
		MetricsInfo: &types.MetricsInfo{
			ID:        apiResp.ID,
			Additions: additions,
			Deletions: deletions,
		},
	}, nil
}

func (p *Provider) logRequest(req *mercuryapi.Request, editableStart, editableEnd, contextStart, contextEnd int) {
	prompt := ""
	if len(req.Messages) > 0 {
		prompt = req.Messages[0].Content
	}
	logger.Debug("mercuryapi request:\n  URL: %s\n  Model: %s\n  Editable: [%d:%d]\n  Context: [%d:%d]\n  Prompt length: %d chars\n  Prompt:\n%s",
		p.client.URL,
		req.Model,
		editableStart, editableEnd,
		contextStart, contextEnd,
		len(prompt),
		prompt)
}

func (p *Provider) logResponse(resp *mercuryapi.Response, completionText string) {
	finishReason := ""
	if len(resp.Choices) > 0 {
		finishReason = resp.Choices[0].FinishReason
	}
	logger.Debug("mercuryapi response:\n  ID: %s\n  FinishReason: %s\n  Text length: %d chars\n  Text:\n%s",
		resp.ID,
		finishReason,
		len(completionText),
		completionText)
}

// countChanges calculates additions and deletions based on line counts.
func countChanges(oldLineCount, newLineCount int) (additions, deletions int) {
	return max(newLineCount, 1), max(oldLineCount, 1)
}

// computeRegions calculates the editable and context regions around the cursor.
// Returns 1-indexed line numbers: editableStart, editableEnd, contextStart, contextEnd.
// Context defaults to the entire file; only trimmed for extremely large files.
// When syntax ranges are available, the editable region snaps to AST boundaries.
func computeRegions(lines []string, cursorRow int, syntaxRanges []*types.LineRange) (int, int, int, int) {
	if len(lines) == 0 {
		return 1, 1, 1, 1
	}

	// Clamp cursor to valid range
	if cursorRow < 1 {
		cursorRow = 1
	}
	if cursorRow > len(lines) {
		cursorRow = len(lines)
	}

	cursorIdx := cursorRow - 1 // 0-indexed

	// Calculate editable region (expand around cursor within char budget)
	editableStart, editableEnd := expandRegion(lines, cursorIdx, MaxRewriteChars)

	// Snap editable region to syntax boundaries if available
	editableStart, editableEnd = utils.SnapToSyntaxBoundaries(lines, editableStart, editableEnd, MaxRewriteChars, syntaxRanges)

	// Context defaults to the entire file
	contextStart := 0
	contextEnd := len(lines) - 1

	// For extremely large files, trim distant regions while preserving the editable area
	totalChars := 0
	for _, l := range lines {
		totalChars += len(l) + 1
	}
	if totalChars > MaxFileChars {
		contextStart, contextEnd = expandRegionAround(lines, editableStart, editableEnd, MaxFileChars)
	}

	return editableStart + 1, editableEnd + 1, contextStart + 1, contextEnd + 1
}

// expandRegion expands a region around the cursor within a character budget.
// Returns 0-indexed start and end (inclusive).
func expandRegion(lines []string, cursorIdx int, maxChars int) (int, int) {
	if len(lines) == 0 {
		return 0, 0
	}

	start := cursorIdx
	end := cursorIdx
	chars := len(lines[cursorIdx]) + 1 // +1 for newline

	// Expand alternating up and down
	for {
		expandedUp := false
		expandedDown := false

		// Try expanding up
		if start > 0 {
			newChars := len(lines[start-1]) + 1
			if chars+newChars <= maxChars {
				start--
				chars += newChars
				expandedUp = true
			}
		}

		// Try expanding down
		if end < len(lines)-1 {
			newChars := len(lines[end+1]) + 1
			if chars+newChars <= maxChars {
				end++
				chars += newChars
				expandedDown = true
			}
		}

		if !expandedUp && !expandedDown {
			break
		}
	}

	return start, end
}

// expandRegionAround expands context around an existing region.
// Returns 0-indexed start and end (inclusive).
func expandRegionAround(lines []string, regionStart, regionEnd int, maxChars int) (int, int) {
	if len(lines) == 0 {
		return 0, 0
	}

	start := regionStart
	end := regionEnd

	// Calculate chars in region
	chars := 0
	for i := start; i <= end && i < len(lines); i++ {
		chars += len(lines[i]) + 1
	}

	// Expand alternating up and down
	for {
		expandedUp := false
		expandedDown := false

		// Try expanding up
		if start > 0 {
			newChars := len(lines[start-1]) + 1
			if chars+newChars <= maxChars {
				start--
				chars += newChars
				expandedUp = true
			}
		}

		// Try expanding down
		if end < len(lines)-1 {
			newChars := len(lines[end+1]) + 1
			if chars+newChars <= maxChars {
				end++
				chars += newChars
				expandedDown = true
			}
		}

		if !expandedUp && !expandedDown {
			break
		}
	}

	return start, end
}

// buildPrompt constructs the Mercury prompt format.
func buildPrompt(
	filePath string,
	lines []string,
	editableStart, editableEnd int, // 1-indexed
	contextStart, contextEnd int, // 1-indexed
	cursorRow, cursorCol int, // 1-indexed row, 0-indexed col
	diffHistories []*types.FileDiffHistory,
	recentSnapshots []*types.RecentBufferSnapshot,
	diagnostics *types.Diagnostics,
	treesitter *types.TreesitterContext,
	gitDiff *types.GitDiffContext,
) string {
	var sb strings.Builder

	// Recently viewed code snippets
	sb.WriteString(RecentlyViewedSnippetsStart)
	for _, snap := range recentSnapshots {
		sb.WriteString(RecentlyViewedSnippetStart)
		sb.WriteString(CodeSnippetFilePathPrefix)
		sb.WriteString(snap.FilePath)
		sb.WriteString("\n")
		sb.WriteString(strings.Join(snap.Lines, "\n"))
		if len(snap.Lines) > 0 && !strings.HasSuffix(snap.Lines[len(snap.Lines)-1], "\n") {
			sb.WriteString("\n")
		}
		sb.WriteString(RecentlyViewedSnippetEnd)
	}
	if diagText := provider.FormatDiagnosticsText(diagnostics); diagText != "" {
		sb.WriteString(RecentlyViewedSnippetStart)
		sb.WriteString(CodeSnippetFilePathPrefix)
		sb.WriteString("diagnostics\n")
		sb.WriteString(diagText)
		sb.WriteString(RecentlyViewedSnippetEnd)
	}
	if treesitter != nil {
		var tsContent strings.Builder
		if treesitter.EnclosingSignature != "" {
			fmt.Fprintf(&tsContent, "Enclosing scope: %s\n", treesitter.EnclosingSignature)
		}
		for _, s := range treesitter.Siblings {
			fmt.Fprintf(&tsContent, "Sibling: line %d: %s\n", s.Line, s.Signature)
		}
		for _, imp := range treesitter.Imports {
			fmt.Fprintf(&tsContent, "Import: %s\n", imp)
		}
		if tsContent.Len() > 0 {
			sb.WriteString(RecentlyViewedSnippetStart)
			sb.WriteString(CodeSnippetFilePathPrefix)
			sb.WriteString("treesitter_context\n")
			sb.WriteString(tsContent.String())
			sb.WriteString(RecentlyViewedSnippetEnd)
		}
	}
	if gitDiff != nil && gitDiff.Diff != "" {
		sb.WriteString(RecentlyViewedSnippetStart)
		sb.WriteString(CodeSnippetFilePathPrefix)
		sb.WriteString("staged_git_diff\n")
		sb.WriteString(gitDiff.Diff)
		if !strings.HasSuffix(gitDiff.Diff, "\n") {
			sb.WriteString("\n")
		}
		sb.WriteString(RecentlyViewedSnippetEnd)
	}
	sb.WriteString(RecentlyViewedSnippetsEnd)
	sb.WriteString("\n")

	// Current file content with editable region
	sb.WriteString(CurrentFileContentStart)
	sb.WriteString(CurrentFilePathPrefix)
	sb.WriteString(filePath)
	sb.WriteString("\n")

	// Content before editable region (within context)
	for i := contextStart; i < editableStart; i++ {
		sb.WriteString(lines[i-1])
		sb.WriteString("\n")
	}

	// Editable region with cursor marker
	sb.WriteString(CodeToEditStart)
	for i := editableStart; i <= editableEnd; i++ {
		line := lines[i-1]
		if i == cursorRow {
			// Insert cursor marker at cursor column
			col := min(cursorCol, len(line))
			sb.WriteString(line[:col])
			sb.WriteString(CursorTag)
			sb.WriteString(line[col:])
		} else {
			sb.WriteString(line)
		}
		sb.WriteString("\n")
	}
	sb.WriteString(CodeToEditEnd)

	// Content after editable region (within context)
	for i := editableEnd + 1; i <= contextEnd; i++ {
		sb.WriteString(lines[i-1])
		sb.WriteString("\n")
	}
	sb.WriteString(CurrentFileContentEnd)
	sb.WriteString("\n")

	// Edit diff history
	sb.WriteString(EditDiffHistoryStart)
	sb.WriteString(formatDiffHistories(diffHistories))
	sb.WriteString(EditDiffHistoryEnd)

	return sb.String()
}

// formatDiffHistories formats diff histories in unified diff format.
func formatDiffHistories(histories []*types.FileDiffHistory) string {
	if len(histories) == 0 {
		return ""
	}

	var sb strings.Builder
	for _, h := range histories {
		if len(h.DiffHistory) == 0 {
			continue
		}

		for _, entry := range h.DiffHistory {
			// Write in unified diff format
			sb.WriteString("--- ")
			sb.WriteString(h.FileName)
			sb.WriteString("\n")
			sb.WriteString("+++ ")
			sb.WriteString(h.FileName)
			sb.WriteString("\n")

			oldLines := countNonEmptyLines(entry.Original)
			newLines := countNonEmptyLines(entry.Updated)
			fmt.Fprintf(&sb, "@@ -%d,%d +%d,%d @@\n", 1, oldLines, 1, newLines)

			// Write old lines with - prefix
			for line := range strings.SplitSeq(entry.Original, "\n") {
				if line != "" {
					sb.WriteString("-")
					sb.WriteString(line)
					sb.WriteString("\n")
				}
			}

			// Write new lines with + prefix
			for line := range strings.SplitSeq(entry.Updated, "\n") {
				if line != "" {
					sb.WriteString("+")
					sb.WriteString(line)
					sb.WriteString("\n")
				}
			}
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// countNonEmptyLines counts the non-empty lines in a string.
func countNonEmptyLines(s string) int {
	n := 0
	for line := range strings.SplitSeq(s, "\n") {
		if line != "" {
			n++
		}
	}
	return n
}
