// Package inline implements end-of-line completion.
package inline

import (
	"context"
	"strings"

	"cursortab/client/openai"
	sourcectx "cursortab/ctx"
	"cursortab/engine"
	"cursortab/logger"
	"cursortab/provider"
	"cursortab/types"
)

const providerName = "inline"

type Provider struct {
	provider.Base
	provider.OpenAI
}

var _ engine.Provider = (*Provider)(nil)
var _ provider.CompletionFlow[*openai.CompletionRequest, *openai.CompletionResult] = (*Provider)(nil)

func NewProvider(config *types.ProviderConfig) *Provider {
	return &Provider{
		Base:   provider.NewBase(engine.CompletionInline, sourcectx.Materials{sourcectx.Treesitter{}}, provider.SyntheticPrefetchDisabled),
		OpenAI: provider.NewOpenAI(providerName, config),
	}
}

func (p *Provider) Complete(ctx context.Context, input sourcectx.CompletionInput) (*types.CompletionResponse, error) {
	return provider.StartBatch(ctx, input, p.ProviderConfig(), p)
}

func (p *Provider) Build(ctx *provider.RequestState) (*openai.CompletionRequest, error) {
	var promptBuilder strings.Builder

	if len(ctx.Window.Lines) == 0 {
		req := p.Request("", []string{"\n"})
		p.LogRequest(req, ctx.Window.MaxLines)
		return req, nil
	}

	for i := range ctx.Window.CursorLine {
		promptBuilder.WriteString(ctx.Window.Lines[i])
		promptBuilder.WriteString("\n")
	}

	if ctx.Window.CursorLine < len(ctx.Window.Lines) {
		currentLine := ctx.Window.Lines[ctx.Window.CursorLine]
		cursorCol := ctx.Input.Current.Cursor.Col
		var prefix string
		if cursorCol <= len(currentLine) {
			prefix = currentLine[:cursorCol]
		} else {
			prefix = currentLine
		}
		promptBuilder.WriteString(strings.TrimRight(prefix, " \t"))
	}

	req := p.Request(promptBuilder.String(), []string{"\n"})
	p.LogRequest(req, ctx.Window.MaxLines)
	return req, nil
}

func (p *Provider) Parse(ctx *provider.RequestState, result *openai.CompletionResult) (*types.CompletionResponse, error) {
	text := result.Text
	if resp, done := provider.RejectEmptyText(providerName, text); done {
		return resp, nil
	}
	if stripped, resp, done := provider.StripRepetitionText(text); done {
		return resp, nil
	} else {
		text = stripped
	}
	if resp, done := rejectTruncatedResult(result.FinishReason); done {
		return resp, nil
	}

	current := ctx.Input.Current
	currentLine := current.File.Lines[current.Cursor.Row-1]
	cursorCol := min(current.Cursor.Col, len(currentLine))
	beforeCursor := strings.TrimRight(currentLine[:cursorCol], " \t")

	newLine := beforeCursor + text
	return provider.BuildCompletion(ctx, current.Cursor.Row, current.Cursor.Row, []string{newLine}), nil
}

func rejectTruncatedResult(finishReason string) (*types.CompletionResponse, bool) {
	if finishReason == "length" {
		logger.Info("inline: rejected, truncated (finish_reason=length)")
		return provider.EmptyResponse(), true
	}
	return nil, false
}
