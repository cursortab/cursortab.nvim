package sweepapi

import (
	"context"
	"path/filepath"
	"strings"

	"cursortab/client/sweepapi"
	"cursortab/logger"
	"cursortab/types"
)

// Provider implements the Sweep hosted API provider
type Provider struct {
	config *types.ProviderConfig
	client *sweepapi.Client
}

// NewProvider creates a new Sweep API provider
func NewProvider(config *types.ProviderConfig) *Provider {
	return &Provider{
		config: config,
		client: sweepapi.NewClient(config.ProviderURL, config.AuthorizationTokenEnv),
	}
}

// GetCompletion implements engine.Provider
func (p *Provider) GetCompletion(ctx context.Context, req *types.CompletionRequest) (*types.CompletionResponse, error) {
	defer logger.Trace("sweepapi.GetCompletion")()

	// Build file contents from lines
	fileContents := strings.Join(req.Lines, "\n")
	originalFileContents := fileContents
	if len(req.PreviousLines) > 0 {
		originalFileContents = strings.Join(req.PreviousLines, "\n")
	}

	// Convert cursor to byte offset
	cursorPosition := sweepapi.CursorToByteOffset(req.Lines, req.CursorRow, req.CursorCol)

	// Format recent changes from diff histories
	recentChanges := formatRecentChanges(req.FileDiffHistories)

	// Format diagnostics as retrieval chunks
	retrievalChunks := formatDiagnostics(req.LinterErrors)

	// Build API request
	apiReq := &sweepapi.AutocompleteRequest{
		FilePath:             req.FilePath,
		FileContents:         fileContents,
		OriginalFileContents: originalFileContents,
		CursorPosition:       cursorPosition,
		RecentChanges:        recentChanges,
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
	_, origEndLine := sweepapi.ByteOffsetToLineCol(fileContents, apiResp.EndIndex)

	logger.Debug("sweepapi: byte range [%d:%d] -> lines [%d:%d], origEndLine=%d",
		apiResp.StartIndex, apiResp.EndIndex, startLine, endLine, origEndLine)

	return &types.CompletionResponse{
		Completions: []*types.Completion{{
			StartLine:  startLine,
			EndLineInc: origEndLine,
			Lines:      newLines,
		}},
	}, nil
}

// formatRecentChanges converts FileDiffHistories to FileChunks for the API
func formatRecentChanges(histories []*types.FileDiffHistory) []sweepapi.FileChunk {
	if len(histories) == 0 {
		return []sweepapi.FileChunk{}
	}

	var chunks []sweepapi.FileChunk
	for _, history := range histories {
		if len(history.DiffHistory) == 0 {
			continue
		}

		// Format each diff entry
		var sb strings.Builder
		for _, entry := range history.DiffHistory {
			if entry.Original != "" || entry.Updated != "" {
				sb.WriteString("<<<<<<< ORIGINAL\n")
				sb.WriteString(entry.Original)
				if !strings.HasSuffix(entry.Original, "\n") && entry.Original != "" {
					sb.WriteString("\n")
				}
				sb.WriteString("=======\n")
				sb.WriteString(entry.Updated)
				if !strings.HasSuffix(entry.Updated, "\n") && entry.Updated != "" {
					sb.WriteString("\n")
				}
				sb.WriteString(">>>>>>> UPDATED\n")
			}
		}

		if sb.Len() > 0 {
			chunks = append(chunks, sweepapi.FileChunk{
				FilePath: history.FileName + ".diff",
				Content:  sb.String(),
			})
		}
	}

	return chunks
}

// formatDiagnostics converts LinterErrors to FileChunks for the API
func formatDiagnostics(linterErrors *types.LinterErrors) []sweepapi.FileChunk {
	if linterErrors == nil || len(linterErrors.Errors) == 0 {
		return []sweepapi.FileChunk{}
	}

	var sb strings.Builder
	for _, err := range linterErrors.Errors {
		if err.Range != nil {
			sb.WriteString("Line ")
			sb.WriteString(itoa(err.Range.StartLine))
			sb.WriteString(": ")
		}
		if err.Source != "" {
			sb.WriteString("[")
			sb.WriteString(err.Source)
			sb.WriteString("] ")
		}
		sb.WriteString(err.Message)
		sb.WriteString("\n")
	}

	if sb.Len() == 0 {
		return []sweepapi.FileChunk{}
	}

	return []sweepapi.FileChunk{{
		FilePath: filepath.Base(linterErrors.RelativeWorkspacePath) + ".diagnostics",
		Content:  sb.String(),
	}}
}

// itoa converts an int to a string (simple helper to avoid strconv import)
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	if n < 0 {
		return "-" + itoa(-n)
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
