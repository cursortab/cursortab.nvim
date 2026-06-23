// Package provider contains the shared OpenAI-compatible provider implementation.
//
// Leaf providers supply a request builder, result parser, required public
// context materials, completion kind, and optional line stream mode. The shared
// Provider implements engine.Provider and keeps prompt/transport/parse details
// inside the provider package.
//
// Use this concrete base for providers whose transport is an OpenAI-compatible
// completion request. Providers with a different protocol, such as Copilot,
// Mercury API, or Windsurf, implement engine.Provider directly and keep an
// explicit compile-time assertion in their package.
package provider

import (
	"context"
	"cursortab/client/openai"
	"cursortab/ctx"
	"cursortab/engine"
	"cursortab/logger"
	"cursortab/types"
	"cursortab/utils"
	"fmt"
	"net/http"
)

var _ engine.Provider = (*Provider)(nil)

type client interface {
	DoCompletion(ctx context.Context, req *openai.CompletionRequest) (*openai.CompletionResponse, error)
	DoLineStream(ctx context.Context, req *openai.CompletionRequest, maxLines int) *openai.LineStream
}

type requestBuilder func(p *Provider, ctx *RequestState) *openai.CompletionRequest
type resultParser func(p *Provider, ctx *RequestState, result *openai.StreamResult) *types.CompletionResponse
type providerOption func(*Provider)

// RequestState is provider-owned state for one collected [ctx.CompletionInput].
// Leaf renderers use it to keep the collected input, trimmed request window,
// and cursor line aligned across build, parse, and optional stream execution.
type RequestState struct {
	Input        ctx.CompletionInput
	TrimmedLines []string
	WindowStart  int
	CursorLine   int
}

// Provider is the shared concrete implementation for OpenAI-compatible
// providers. Leaf packages provide only the facts that vary: completion kind,
// required materials, request rendering, response parsing, and optional line
// stream mechanics.
type Provider struct {
	name         string
	config       *types.ProviderConfig
	client       client
	completion   engine.CompletionKind
	buildRequest requestBuilder
	parseResult  resultParser
	lineStream   *lineStreamMode
	materials    ctx.Materials
}

func NewProvider(name string, config *types.ProviderConfig, client client, completion engine.CompletionKind, materials ctx.Materials, build requestBuilder, parse resultParser, opts ...providerOption) *Provider {
	p := &Provider{
		name:         name,
		config:       config,
		client:       client,
		completion:   completion,
		materials:    materials,
		buildRequest: build,
		parseResult:  parse,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

func (p *Provider) Config() *types.ProviderConfig {
	return p.config
}

func (p *Provider) CompletionKind() engine.CompletionKind {
	return p.completion
}

func (p *Provider) CompletionInputAuthority() engine.CompletionInputAuthority {
	return engine.InputSuppliedCurrent
}

func (p *Provider) RequiredMaterials() ctx.Materials {
	return p.materials
}

// SetHTTPTransport forwards the transport override to the underlying client
// if it supports it. Used by the eval harness.
func (p *Provider) SetHTTPTransport(rt http.RoundTripper) {
	if setter, ok := p.client.(interface{ SetHTTPTransport(http.RoundTripper) }); ok {
		setter.SetHTTPTransport(rt)
	}
}

func (p *Provider) StartCompletion(ctx context.Context, input ctx.CompletionInput, allowStream bool) (*types.CompletionResponse, engine.CompletionStream, error) {
	defer logger.Trace("Provider.StartCompletion")()
	if allowStream && p.lineStream != nil {
		stream, err := p.startLineStream(ctx, input)
		if err != nil {
			return nil, nil, err
		}
		return nil, stream, nil
	}

	pctx, req, _, err := p.BuildOpenAIRequest(input)
	if err != nil {
		return p.EmptyResponse(), nil, err
	}

	resp, err := p.client.DoCompletion(ctx, req)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", p.name, err)
	}

	result := &openai.StreamResult{}
	if len(resp.Choices) > 0 {
		result = &openai.StreamResult{
			Text:         resp.Choices[0].Text,
			FinishReason: resp.Choices[0].FinishReason,
		}
	}
	p.LogOpenAIResponse(result)

	return p.parseResult(p, pctx, result), nil, nil
}

func (p *Provider) BuildOpenAIRequest(input ctx.CompletionInput) (*RequestState, *openai.CompletionRequest, int, error) {
	if p.buildRequest == nil || p.parseResult == nil {
		return nil, nil, 0, fmt.Errorf("%s: request flow is not configured", p.name)
	}

	pctx, maxLines := p.prepareRequestState(input)
	req := p.buildRequest(p, pctx)
	p.logRequest(req, maxLines)
	return pctx, req, maxLines, nil
}

func (p *Provider) prepareRequestState(input ctx.CompletionInput) (*RequestState, int) {
	current := input.Current
	pctx := &RequestState{Input: input}
	maxLines := 0
	cursorLine := current.Cursor.Row - 1
	var syntaxRanges []*types.LineRange
	if material, ok := ctx.Find[ctx.Treesitter](input.Materials); ok && material.Data != nil {
		syntaxRanges = material.Data.SyntaxRanges
	}
	contextSize := p.config.ProviderContextSize
	if contextSize == 0 {
		contextSize = p.config.ProviderMaxTokens
	}
	trimmedLines, newCursorLine, _, trimOffset, didTrim := utils.TrimContentAroundCursor(
		current.File.Lines,
		cursorLine,
		current.Cursor.Col,
		contextSize,
		syntaxRanges,
	)
	pctx.TrimmedLines = trimmedLines
	pctx.CursorLine = newCursorLine
	pctx.WindowStart = trimOffset

	if didTrim {
		maxLines = len(trimmedLines)
	}
	if current.ViewportHeight > 0 {
		if maxLines == 0 || current.ViewportHeight < maxLines {
			maxLines = current.ViewportHeight
		}
	}

	return pctx, maxLines
}

func (p *Provider) EmptyResponse() *types.CompletionResponse {
	return &types.CompletionResponse{}
}

func (p *Provider) BuildCompletion(ctx *RequestState, startLine, endLineInc int, lines []string) *types.CompletionResponse {
	currentLines := ctx.Input.Current.File.Lines
	if endLineInc <= len(currentLines) && isNoOpReplacement(lines, currentLines[startLine-1:endLineInc]) {
		return p.EmptyResponse()
	}

	completion := &types.Completion{
		StartLine:  startLine,
		EndLineInc: endLineInc,
		Lines:      lines,
	}

	return &types.CompletionResponse{
		Completion:   completion,
		CursorTarget: nil,
	}
}

func (p *Provider) logRequest(req *openai.CompletionRequest, maxLines int) {
	logger.Debug("%s provider request:\n  URL: %s%s\n  Model: %s\n  Temperature: %.2f\n  MaxTokens: %d\n  MaxLines: %d\n  Prompt length: %d chars\n  Prompt:\n%s",
		p.name,
		p.config.ProviderURL,
		p.config.CompletionPath,
		req.Model,
		req.Temperature,
		req.MaxTokens,
		maxLines,
		len(req.Prompt),
		req.Prompt)
}

func (p *Provider) LogOpenAIResponse(result *openai.StreamResult) {
	logger.Debug("%s provider response:\n  Text length: %d chars\n  FinishReason: %s\n  StoppedEarly: %v\n  Text:\n%s",
		p.name,
		len(result.Text),
		result.FinishReason,
		result.StoppedEarly,
		result.Text)
}
