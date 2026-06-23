// Package inline implements end-of-line completion.
package inline

import (
	"strings"

	"cursortab/client/openai"
	sourcectx "cursortab/ctx"
	"cursortab/engine"
	"cursortab/logger"
	"cursortab/provider"
	"cursortab/types"
)

// NewProvider creates a new inline completion provider
func NewProvider(config *types.ProviderConfig) *provider.Provider {
	return provider.NewProvider(
		"inline",
		config,
		openai.NewClient(config.ProviderURL, config.CompletionPath, config.APIKey),
		engine.CompletionInline,
		sourcectx.Materials{sourcectx.Treesitter{}},
		buildRequest,
		parseResult,
	)
}

func buildRequest(p *provider.Provider, ctx *provider.RequestState) *openai.CompletionRequest {
	var promptBuilder strings.Builder

	if len(ctx.TrimmedLines) == 0 {
		return &openai.CompletionRequest{
			Model:       p.Config().ProviderModel,
			Prompt:      "",
			Temperature: p.Config().ProviderTemperature,
			MaxTokens:   p.Config().ProviderMaxTokens,
			TopK:        p.Config().ProviderTopK,
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
		Model:       p.Config().ProviderModel,
		Prompt:      promptBuilder.String(),
		Temperature: p.Config().ProviderTemperature,
		MaxTokens:   p.Config().ProviderMaxTokens,
		TopK:        p.Config().ProviderTopK,
		Stop:        []string{"\n"},
		N:           1,
		Echo:        false,
	}
}

func parseResult(p *provider.Provider, ctx *provider.RequestState, result *openai.StreamResult) *types.CompletionResponse {
	text := result.Text
	if resp, done := provider.RejectEmptyText(p, text); done {
		return resp
	}
	if stripped, resp, done := provider.StripRepetitionText(p, text); done {
		return resp
	} else {
		text = stripped
	}
	if resp, done := rejectTruncatedResult(p, result.FinishReason); done {
		return resp
	}

	current := ctx.Input.Current
	currentLine := current.File.Lines[current.Cursor.Row-1]
	cursorCol := min(current.Cursor.Col, len(currentLine))
	beforeCursor := strings.TrimRight(currentLine[:cursorCol], " \t")

	newLine := beforeCursor + text
	return p.BuildCompletion(ctx, current.Cursor.Row, current.Cursor.Row, []string{newLine})
}

func rejectTruncatedResult(p *provider.Provider, finishReason string) (*types.CompletionResponse, bool) {
	if finishReason == "length" {
		logger.Info("inline: rejected, truncated (finish_reason=length)")
		return p.EmptyResponse(), true
	}
	return nil, false
}
