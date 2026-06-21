// Package inline implements a simple end-of-line completion provider.
//
// Prompt format (sent as a single text prompt to /v1/completions):
//
//	...all lines before cursor line...
//	...text before cursor on current line...
//
// The model completes from the cursor position to end of line.
// Stop token: \n (single-line completions only).
// Lines are trimmed to a window around the cursor via the TrimContent preprocessor.
package inline

import (
	"strings"

	"cursortab/client/openai"
	"cursortab/provider"
	"cursortab/types"
)

// NewProvider creates a new inline completion provider
func NewProvider(config *types.ProviderConfig) *provider.Provider {
	return &provider.Provider{
		Name:          "inline",
		Config:        config,
		Client:        openai.NewClient(config.ProviderURL, config.CompletionPath, config.APIKey),
		StreamingType: provider.StreamingNone,
		BuildBatch:    buildBatch,
		ParseBatch:    parseBatch,
		StopTokens:    []string{"\n"},
	}
}

func buildBatch(p *provider.Provider, ctx *provider.BatchContext) *openai.CompletionRequest {
	var promptBuilder strings.Builder

	if len(ctx.TrimmedLines) == 0 {
		return &openai.CompletionRequest{
			Model:       p.Config.ProviderModel,
			Prompt:      "",
			Temperature: p.Config.ProviderTemperature,
			MaxTokens:   p.Config.ProviderMaxTokens,
			TopK:        p.Config.ProviderTopK,
			Stop:        []string{"\n"},
			N:           1,
			Echo:        false,
		}
	}

	for i := range ctx.CursorLine {
		promptBuilder.WriteString(ctx.TrimmedLines[i])
		promptBuilder.WriteString("\n")
	}

	if ctx.CursorLine < len(ctx.TrimmedLines) {
		currentLine := ctx.TrimmedLines[ctx.CursorLine]
		cursorCol := ctx.Input.Current.Cursor.Col
		var prefix string
		if cursorCol <= len(currentLine) {
			prefix = currentLine[:cursorCol]
		} else {
			prefix = currentLine
		}
		promptBuilder.WriteString(strings.TrimRight(prefix, " \t"))
	}

	return &openai.CompletionRequest{
		Model:       p.Config.ProviderModel,
		Prompt:      promptBuilder.String(),
		Temperature: p.Config.ProviderTemperature,
		MaxTokens:   p.Config.ProviderMaxTokens,
		TopK:        p.Config.ProviderTopK,
		Stop:        []string{"\n"},
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
	if resp, done := provider.RejectTruncatedBatch(p, result); done {
		return resp, true
	}

	current := ctx.Input.Current
	completionText := result.Text
	currentLine := current.File.Lines[current.Cursor.Row-1]
	cursorCol := min(current.Cursor.Col, len(currentLine))
	beforeCursor := strings.TrimRight(currentLine[:cursorCol], " \t")

	newLine := beforeCursor + completionText
	return p.BuildBatchCompletion(ctx, current.Cursor.Row, current.Cursor.Row, []string{newLine})
}
