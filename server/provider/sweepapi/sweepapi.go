package sweepapi

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"cursortab/client/sweepapi"
	"cursortab/logger"
	"cursortab/types"
)

// SweepAPI input limits
const (
	MaxInputLines = 50000      // Maximum lines for any input
	MaxInputChars = 10_000_000 // Maximum characters for any input
)

// truncateContext limits content to MaxInputLines and MaxInputChars while
// preserving cursor position. Returns truncated lines, adjusted cursor row/col,
// and the offset (lines removed from start) for response adjustment.
func truncateContext(lines []string, cursorRow, cursorCol int) ([]string, int, int, int) {
	if len(lines) == 0 {
		return lines, cursorRow, cursorCol, 0
	}

	// Fast path: check if within both limits
	if len(lines) <= MaxInputLines {
		totalChars := 0
		for _, line := range lines {
			totalChars += len(line) + 1 // +1 for newline
		}
		if totalChars <= MaxInputChars {
			return lines, cursorRow, cursorCol, 0
		}
	}

	// Clamp cursor to valid range (1-indexed)
	if cursorRow < 1 {
		cursorRow = 1
	}
	if cursorRow > len(lines) {
		cursorRow = len(lines)
	}
	cursorIdx := cursorRow - 1 // 0-indexed

	// Calculate effective max lines based on both limits
	maxLines := min(MaxInputLines, len(lines))

	// Balanced window around cursor
	halfWindow := maxLines / 2
	startLine := max(0, cursorIdx-halfWindow)
	endLine := min(len(lines), startLine+maxLines)

	// Adjust if we hit the end
	if endLine == len(lines) {
		startLine = max(0, endLine-maxLines)
	}

	result := lines[startLine:endLine]

	// Apply character limit if still over
	totalChars := 0
	for _, line := range result {
		totalChars += len(line) + 1
	}

	if totalChars > MaxInputChars {
		result, startLine = trimByChars(result, cursorIdx-startLine, startLine)
	}

	newCursorRow := cursorRow - startLine
	return result, newCursorRow, cursorCol, startLine
}

// trimByChars further trims lines to fit within MaxInputChars while keeping
// content centered around the cursor. Returns the trimmed lines and the new
// base offset for line number adjustment.
func trimByChars(lines []string, cursorIdxInWindow, baseOffset int) ([]string, int) {
	if len(lines) == 0 {
		return lines, baseOffset
	}

	// Clamp cursor to valid range
	if cursorIdxInWindow < 0 {
		cursorIdxInWindow = 0
	}
	if cursorIdxInWindow >= len(lines) {
		cursorIdxInWindow = len(lines) - 1
	}

	// Start with cursor line
	cursorLineChars := len(lines[cursorIdxInWindow]) + 1
	remainingBudget := MaxInputChars - cursorLineChars
	halfBudget := remainingBudget / 2

	// Expand before cursor
	startIdx := cursorIdxInWindow
	charsBefore := 0
	for startIdx > 0 && charsBefore < halfBudget {
		newChars := len(lines[startIdx-1]) + 1
		if charsBefore+newChars <= halfBudget {
			startIdx--
			charsBefore += newChars
		} else {
			break
		}
	}

	// Expand after cursor with remaining budget
	unusedBefore := halfBudget - charsBefore
	budgetAfter := halfBudget + unusedBefore
	endIdx := cursorIdxInWindow
	charsAfter := 0
	for endIdx < len(lines)-1 && charsAfter < budgetAfter {
		newChars := len(lines[endIdx+1]) + 1
		if charsAfter+newChars <= budgetAfter {
			endIdx++
			charsAfter += newChars
		} else {
			break
		}
	}

	// Use any remaining budget to expand before
	unusedAfter := budgetAfter - charsAfter
	if unusedAfter > 0 {
		for startIdx > 0 {
			newChars := len(lines[startIdx-1]) + 1
			if charsBefore+newChars <= halfBudget+unusedAfter {
				startIdx--
				charsBefore += newChars
			} else {
				break
			}
		}
	}

	return lines[startIdx : endIdx+1], baseOffset + startIdx
}

// truncateDiffHistories limits diff history to MaxInputChars and MaxInputLines,
// keeping the most recent entries (from the end of each file's history).
func truncateDiffHistories(histories []*types.FileDiffHistory) []*types.FileDiffHistory {
	if len(histories) == 0 {
		return histories
	}

	// Calculate total size
	totalChars := 0
	totalLines := 0
	for _, h := range histories {
		for _, entry := range h.DiffHistory {
			totalChars += len(entry.Original) + len(entry.Updated)
			totalLines += strings.Count(entry.Original, "\n") + strings.Count(entry.Updated, "\n") + 2
		}
	}

	if totalChars <= MaxInputChars && totalLines <= MaxInputLines {
		return histories
	}

	// Trim from the end of the list (oldest files first), then oldest entries
	result := make([]*types.FileDiffHistory, 0, len(histories))
	remainingChars := MaxInputChars
	remainingLines := MaxInputLines

	// Process in reverse order (most recent files first)
	for i := len(histories) - 1; i >= 0 && remainingChars > 0 && remainingLines > 0; i-- {
		h := histories[i]
		if len(h.DiffHistory) == 0 {
			continue
		}

		// Trim entries within this file's history (keep recent ones)
		var keptEntries []*types.DiffEntry
		for j := len(h.DiffHistory) - 1; j >= 0 && remainingChars > 0 && remainingLines > 0; j-- {
			entry := h.DiffHistory[j]
			entryChars := len(entry.Original) + len(entry.Updated)
			entryLines := strings.Count(entry.Original, "\n") + strings.Count(entry.Updated, "\n") + 2
			if entryChars <= remainingChars && entryLines <= remainingLines {
				keptEntries = append([]*types.DiffEntry{entry}, keptEntries...)
				remainingChars -= entryChars
				remainingLines -= entryLines
			}
		}

		if len(keptEntries) > 0 {
			result = append([]*types.FileDiffHistory{{
				FileName:    h.FileName,
				DiffHistory: keptEntries,
			}}, result...)
		}
	}

	return result
}

// metricsEvent represents a metrics tracking event to be sent to the API.
type metricsEvent struct {
	eventType    sweepapi.EventType
	completionID string
	additions    int
	deletions    int
}

// Provider implements the Sweep hosted API provider
type Provider struct {
	config *types.ProviderConfig
	client *sweepapi.Client

	// Acceptance tracking state
	mu               sync.Mutex
	lastCompletionID string
	lastAdditions    int
	lastDeletions    int

	// Metrics channel for ordered event processing
	metricsCh chan metricsEvent
}

// NewProvider creates a new Sweep API provider
func NewProvider(config *types.ProviderConfig) *Provider {
	// Use constant URL, but allow override for testing (httptest servers use 127.0.0.1)
	url := sweepapi.CompletionURL
	if strings.HasPrefix(config.ProviderURL, "http://127.0.0.1") {
		url = config.ProviderURL
	}

	p := &Provider{
		config:    config,
		client:    sweepapi.NewClient(url, config.APIKey, config.CompletionTimeout),
		metricsCh: make(chan metricsEvent, 64),
	}
	go p.metricsWorker()
	return p
}

// GetCompletion implements engine.Provider
func (p *Provider) GetCompletion(ctx context.Context, req *types.CompletionRequest) (*types.CompletionResponse, error) {
	defer logger.Trace("sweepapi.GetCompletion")()

	// Apply context limits
	lines, cursorRow, cursorCol, trimOffset := truncateContext(req.Lines, req.CursorRow, req.CursorCol)
	if trimOffset > 0 {
		logger.Debug("sweepapi: truncated context, removed %d lines from start", trimOffset)
	}

	// Build file contents from lines
	fileContents := strings.Join(lines, "\n")

	// Convert cursor to byte offset
	cursorPosition := sweepapi.CursorToByteOffset(lines, cursorRow, cursorCol)

	// Truncate and format recent changes from diff histories
	diffHistories := truncateDiffHistories(req.FileDiffHistories)
	recentChanges := formatRecentChanges(diffHistories)

	// Format diagnostics as retrieval chunks
	retrievalChunks := formatDiagnostics(req.LinterErrors)

	// Extract repo name from workspace path
	repoName := filepath.Base(req.WorkspacePath)
	if repoName == "" || repoName == "." {
		repoName = "untitled"
	}

	// Build API request
	apiReq := &sweepapi.AutocompleteRequest{
		RepoName:             repoName,
		FilePath:             req.FilePath,
		FileContents:         fileContents,
		OriginalFileContents: fileContents,
		CursorPosition:       cursorPosition,
		RecentChanges:        recentChanges,
		ChangesAboveCursor:   true,
		MultipleSuggestions:  false,
		UseBytes:             true,
		PrivacyModeEnabled:   p.config.PrivacyMode,
		FileChunks:           buildFileChunks(req.RecentBufferSnapshots),
		RecentUserActions:    convertUserActions(req.UserActions),
		RetrievalChunks:      retrievalChunks,
	}

	// Call API
	apiResp, err := p.client.DoCompletion(ctx, apiReq)
	if err != nil {
		return nil, err
	}

	// Handle empty response
	if apiResp.Completion == "" {
		return &types.CompletionResponse{}, nil
	}

	// Convert byte offsets to line-based completion
	newText, startLine, endLine := sweepapi.ApplyByteRangeEdit(
		fileContents,
		apiResp.StartIndex,
		apiResp.EndIndex,
		apiResp.Completion,
	)

	// Extract the affected lines from the new text
	newLines := sweepapi.ExtractLines(newText, startLine, endLine)
	if len(newLines) == 0 {
		return &types.CompletionResponse{}, nil
	}

	// Calculate original end line for buffer replacement
	// Use endIdx-1 to get the line of the last replaced byte (not the position after)
	endOffset := apiResp.EndIndex
	if endOffset > apiResp.StartIndex {
		endOffset--
	}
	origEndLine, _ := sweepapi.ByteOffsetToLineCol(fileContents, endOffset)

	logger.Debug("sweepapi: byte range [%d:%d] -> lines [%d:%d], origEndLine=%d",
		apiResp.StartIndex, apiResp.EndIndex, startLine, endLine, origEndLine)

	// Store completion info for acceptance tracking
	additions, deletions := countChanges(origEndLine-startLine+1, len(newLines))
	p.mu.Lock()
	p.lastCompletionID = apiResp.AutocompleteID
	p.lastAdditions = additions
	p.lastDeletions = deletions
	p.mu.Unlock()

	// Send "shown" event to register completion for tracking
	p.trackMetric(sweepapi.EventShown, apiResp.AutocompleteID, additions, deletions)

	return &types.CompletionResponse{
		Completions: []*types.Completion{{
			StartLine:  startLine + trimOffset,
			EndLineInc: origEndLine + trimOffset,
			Lines:      newLines,
		}},
	}, nil
}

// countChanges calculates additions and deletions based on line counts.
func countChanges(oldLineCount, newLineCount int) (additions, deletions int) {
	return max(newLineCount, 1), max(oldLineCount, 1)
}

// AcceptCompletion implements engine.CompletionAccepter
func (p *Provider) AcceptCompletion(ctx context.Context) {
	p.mu.Lock()
	completionID := p.lastCompletionID
	additions := p.lastAdditions
	deletions := p.lastDeletions
	p.lastCompletionID = "" // Clear after use
	p.mu.Unlock()

	p.trackMetric(sweepapi.EventAccepted, completionID, additions, deletions)
}

// metricsWorker processes metrics events from the channel in order.
func (p *Provider) metricsWorker() {
	for ev := range p.metricsCh {
		req := &sweepapi.MetricsRequest{
			EventType:          ev.eventType,
			SuggestionType:     sweepapi.SuggestionGhostText,
			Additions:          ev.additions,
			Deletions:          ev.deletions,
			AutocompleteID:     ev.completionID,
			DebugInfo:          "cursortab-nvim",
			PrivacyModeEnabled: p.config.PrivacyMode,
		}
		if err := p.client.TrackMetrics(context.Background(), req); err != nil {
			logger.Warn("sweepapi: failed to track %s: %v", ev.eventType, err)
		}
	}
}

// trackMetric queues a metrics event for processing.
func (p *Provider) trackMetric(eventType sweepapi.EventType, completionID string, additions, deletions int) {
	if completionID == "" {
		return
	}
	select {
	case p.metricsCh <- metricsEvent{eventType, completionID, additions, deletions}:
	default:
		logger.Warn("sweepapi: metrics channel full, dropping %s event", eventType)
	}
}

// formatRecentChanges converts FileDiffHistories to a string for the API
// Format: "File: path:\n{diff}\n"
func formatRecentChanges(histories []*types.FileDiffHistory) string {
	if len(histories) == 0 {
		return ""
	}

	var sb strings.Builder
	for _, history := range histories {
		if len(history.DiffHistory) == 0 {
			continue
		}

		// Format each diff entry for this file
		var diffContent strings.Builder
		for _, entry := range history.DiffHistory {
			if entry.Original != "" || entry.Updated != "" {
				diffContent.WriteString("<<<<<<< ORIGINAL\n")
				diffContent.WriteString(entry.Original)
				if !strings.HasSuffix(entry.Original, "\n") && entry.Original != "" {
					diffContent.WriteString("\n")
				}
				diffContent.WriteString("=======\n")
				diffContent.WriteString(entry.Updated)
				if !strings.HasSuffix(entry.Updated, "\n") && entry.Updated != "" {
					diffContent.WriteString("\n")
				}
				diffContent.WriteString(">>>>>>> UPDATED\n")
			}
		}

		if diffContent.Len() > 0 {
			sb.WriteString("File: ")
			sb.WriteString(history.FileName)
			sb.WriteString(":\n")
			sb.WriteString(diffContent.String())
			sb.WriteString("\n")
		}
	}

	return sb.String()
}

// formatDiagnostics converts LinterErrors to FileChunks for the API
func formatDiagnostics(linterErrors *types.LinterErrors) []sweepapi.FileChunk {
	if linterErrors == nil || len(linterErrors.Errors) == 0 {
		return []sweepapi.FileChunk{}
	}

	var sb strings.Builder
	lineCount := 0
	for _, err := range linterErrors.Errors {
		if sb.Len() >= MaxInputChars || lineCount >= MaxInputLines {
			break
		}
		if err.Range != nil {
			sb.WriteString("Line ")
			sb.WriteString(strconv.Itoa(err.Range.StartLine))
			sb.WriteString(": ")
		}
		if err.Source != "" {
			sb.WriteString("[")
			sb.WriteString(err.Source)
			sb.WriteString("] ")
		}
		sb.WriteString(err.Message)
		sb.WriteString("\n")
		lineCount++
	}

	if sb.Len() == 0 {
		return []sweepapi.FileChunk{}
	}

	return []sweepapi.FileChunk{{
		FilePath:  "diagnostics",
		Content:   sb.String(),
		StartLine: 1,
		EndLine:   lineCount,
	}}
}

// buildFileChunks converts RecentBufferSnapshots to FileChunks for the API.
// Respects MaxInputChars and MaxInputLines limits.
func buildFileChunks(snapshots []*types.RecentBufferSnapshot) []sweepapi.FileChunk {
	if len(snapshots) == 0 {
		return []sweepapi.FileChunk{}
	}

	chunks := make([]sweepapi.FileChunk, 0, len(snapshots))
	totalChars := 0
	totalLines := 0

	for _, snap := range snapshots {
		content := strings.Join(snap.Lines, "\n")
		lineCount := len(snap.Lines)

		// Check if adding this chunk would exceed limits
		if totalChars+len(content) > MaxInputChars || totalLines+lineCount > MaxInputLines {
			break
		}

		ts := uint64(snap.TimestampMs)
		chunks = append(chunks, sweepapi.FileChunk{
			FilePath:  snap.FilePath,
			Content:   content,
			StartLine: 0,
			EndLine:   lineCount,
			Timestamp: &ts,
		})

		totalChars += len(content)
		totalLines += lineCount
	}
	return chunks
}

// convertUserActions converts types.UserAction to sweepapi.UserAction.
// Since actions are small fixed-size records, we just convert them all
// (the engine already limits to MaxUserActions=16).
func convertUserActions(actions []*types.UserAction) []sweepapi.UserAction {
	if len(actions) == 0 {
		return []sweepapi.UserAction{}
	}

	result := make([]sweepapi.UserAction, 0, len(actions))
	for _, a := range actions {
		result = append(result, sweepapi.UserAction{
			ActionType: string(a.ActionType),
			FilePath:   a.FilePath,
			LineNumber: a.LineNumber,
			Offset:     a.Offset,
			Timestamp:  a.TimestampMs,
		})
	}
	return result
}
