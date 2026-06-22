// Package inline implements a simple end-of-line completion provider.
//
// Prompt format (sent as a single text prompt to /v1/completions):
//
//	...all lines before cursor line...
//	...text before cursor on current line...
//
// The model completes from the cursor position to end of line.
// Stop token: \n (single-line completions only).
// Lines are trimmed to a window around the cursor before the prompt is rendered.
package inline

import (
	"strings"

	"cursortab/client/openai"
	sourcectx "cursortab/ctx"
	"cursortab/logger"
	"cursortab/provider"
	"cursortab/types"
)

// NewProvider creates a new inline completion provider
func NewProvider(config *types.ProviderConfig) *provider.Provider {
	return &provider.Provider{
		Name:         "inline",
		Config:       config,
		Client:       openai.NewClient(config.ProviderURL, config.CompletionPath, config.APIKey),
		Materials:    sourcectx.Materials{sourcectx.Treesitter{}},
		BuildRequest: buildRequest,
		ParseResult:  parseResult,
	}
}

func buildRequest(p *provider.Provider, ctx *provider.RequestState) provider.PreparedRequest {
	var promptBuilder strings.Builder

	if len(ctx.TrimmedLines) == 0 {
		return provider.PreparedRequest{Completion: &openai.CompletionRequest{
			Model:       p.Config.ProviderModel,
			Prompt:      "",
			Temperature: p.Config.ProviderTemperature,
			MaxTokens:   p.Config.ProviderMaxTokens,
			TopK:        p.Config.ProviderTopK,
			Stop:        []string{"\n"},
			N:           1,
			Echo:        false,
		}}
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

	return provider.PreparedRequest{Completion: &openai.CompletionRequest{
		Model:       p.Config.ProviderModel,
		Prompt:      promptBuilder.String(),
		Temperature: p.Config.ProviderTemperature,
		MaxTokens:   p.Config.ProviderMaxTokens,
		TopK:        p.Config.ProviderTopK,
		Stop:        []string{"\n"},
		N:           1,
		Echo:        false,
	}}
}

func parseResult(p *provider.Provider, ctx *provider.RequestState, result *openai.StreamResult) *types.CompletionResponse {
	if resp, done := provider.RejectEmptyResult(p, result); done {
		return resp
	}
	if resp, done := provider.StripRepetitionResult(p, result); done {
		return resp
	}
	if resp, done := rejectTruncatedResult(p, result); done {
		return resp
	}

	current := ctx.Input.Current
	completionText := result.Text
	currentLine := current.File.Lines[current.Cursor.Row-1]
	cursorCol := min(current.Cursor.Col, len(currentLine))
	beforeCursor := strings.TrimRight(currentLine[:cursorCol], " \t")

	newLine := beforeCursor + completionText
	return p.BuildCompletion(ctx, current.Cursor.Row, current.Cursor.Row, []string{newLine})
}

func rejectTruncatedResult(p *provider.Provider, result *openai.StreamResult) (*types.CompletionResponse, bool) {
	if result.FinishReason == "length" {
		logger.Info("%s: rejected, truncated (finish_reason=length)", p.Name)
		return p.EmptyResponse(), true
	}
	return nil, false
}
