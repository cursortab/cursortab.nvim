package mercuryapi

import (
	"context"
	"slices"
	"strings"

	"cursortab/client/mercuryapi"
	"cursortab/engine"
	"cursortab/logger"
	"cursortab/metrics"
	"cursortab/types"
)

// Token limits (characters, approximating 1 token ~= 3 chars)
const (
	MaxRewriteChars = 450  // ~150 tokens for editable region
	MaxContextChars = 1050 // ~350 tokens for surrounding context
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
	editableStart, editableEnd, contextStart, contextEnd := computeRegions(req.Lines, req.CursorRow)

	// Build the prompt
	prompt := buildPrompt(
		req.FilePath,
		req.Lines,
		editableStart, editableEnd,
		contextStart, contextEnd,
		req.CursorRow, req.CursorCol,
		req.FileDiffHistories,
		req.RecentBufferSnapshots,
	)

	logger.Debug("mercuryapi: prompt length=%d, editable=[%d:%d], context=[%d:%d]",
		len(prompt), editableStart, editableEnd, contextStart, contextEnd)

	// Build API request
	apiReq := &mercuryapi.Request{
		Model: mercuryapi.Model,
		Messages: []mercuryapi.Message{
			{Role: "user", Content: prompt},
		},
		Stream: false,
	}

	// Call API
	apiResp, err := p.client.DoCompletion(ctx, apiReq)
	if err != nil {
		return nil, err
	}

	// Extract completion text
	completionText := mercuryapi.ExtractCompletion(apiResp)
	if completionText == "" {
		return &types.CompletionResponse{}, nil
	}

	// The completion text replaces the editable region
	newLines := strings.Split(completionText, "\n")

	// Check if it's a no-op (same content)
	originalEditable := req.Lines[editableStart-1 : editableEnd]
	if slices.Equal(newLines, originalEditable) {
		return &types.CompletionResponse{}, nil
	}

	logger.Debug("mercuryapi: completion replaces lines [%d:%d] with %d lines",
		editableStart, editableEnd, len(newLines))

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

// countChanges calculates additions and deletions based on line counts.
func countChanges(oldLineCount, newLineCount int) (additions, deletions int) {
	return max(newLineCount, 1), max(oldLineCount, 1)
}

// computeRegions calculates the editable and context regions around the cursor.
// Returns 1-indexed line numbers: editableStart, editableEnd, contextStart, contextEnd
func computeRegions(lines []string, cursorRow int) (int, int, int, int) {
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

	// Calculate context region (expand around editable within char budget)
	contextStart, contextEnd := expandRegionAround(lines, editableStart, editableEnd, MaxContextChars)

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
