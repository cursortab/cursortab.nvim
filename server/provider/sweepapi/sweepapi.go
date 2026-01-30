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

// Provider implements the Sweep hosted API provider
type Provider struct {
	config *types.ProviderConfig
	client *sweepapi.Client

	// Acceptance tracking state
	mu               sync.Mutex
	lastCompletionID string
	lastAdditions    int
	lastDeletions    int
}

// NewProvider creates a new Sweep API provider
func NewProvider(config *types.ProviderConfig) *Provider {
	return &Provider{
		config: config,
		client: sweepapi.NewClient(config.ProviderURL, config.APIKey, config.CompletionTimeout),
	}
}

// GetCompletion implements engine.Provider
func (p *Provider) GetCompletion(ctx context.Context, req *types.CompletionRequest) (*types.CompletionResponse, error) {
	defer logger.Trace("sweepapi.GetCompletion")()

	// Build file contents from lines
	fileContents := strings.Join(req.Lines, "\n")

	// Convert cursor to byte offset
	cursorPosition := sweepapi.CursorToByteOffset(req.Lines, req.CursorRow, req.CursorCol)

	// Format recent changes from diff histories
	recentChanges := formatRecentChanges(req.FileDiffHistories)

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
		FileChunks:           []sweepapi.FileChunk{}, // Not implemented: would need engine changes
		RecentUserActions:    []sweepapi.UserAction{}, // Not implemented: would need engine changes
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
	p.lastCompletionID = apiResp.ID
	p.lastAdditions = additions
	p.lastDeletions = deletions
	p.mu.Unlock()

	return &types.CompletionResponse{
		Completions: []*types.Completion{{
			StartLine:  startLine,
			EndLineInc: origEndLine,
			Lines:      newLines,
		}},
	}, nil
}

// countChanges calculates additions and deletions based on line counts
func countChanges(oldLineCount, newLineCount int) (additions, deletions int) {
	if newLineCount > oldLineCount {
		additions = newLineCount - oldLineCount
	}
	if oldLineCount > newLineCount {
		deletions = oldLineCount - newLineCount
	}
	// Count the overlapping lines as modifications (both add and delete)
	minLines := min(oldLineCount, newLineCount)
	additions += minLines
	deletions += minLines
	return additions, deletions
}

// AcceptCompletion implements engine.CompletionAccepter
func (p *Provider) AcceptCompletion(ctx context.Context) {
	p.mu.Lock()
	completionID := p.lastCompletionID
	additions := p.lastAdditions
	deletions := p.lastDeletions
	p.lastCompletionID = "" // Clear after use
	p.mu.Unlock()

	if completionID == "" {
		return
	}

	req := &sweepapi.MetricsRequest{
		EventType:          sweepapi.EventAccepted,
		SuggestionType:     sweepapi.SuggestionGhostText,
		Additions:          additions,
		Deletions:          deletions,
		AutocompleteID:     completionID,
		DebugInfo:          "cursortab-nvim",
		PrivacyModeEnabled: p.config.PrivacyMode,
	}

	if err := p.client.TrackMetrics(ctx, req); err != nil {
		logger.Warn("sweepapi: failed to track acceptance: %v", err)
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
