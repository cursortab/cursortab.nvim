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
	"strings"
)

// httpTransportSetter is implemented by clients that allow their outgoing
// HTTP transport to be replaced. Used by the eval harness for record/replay.
type httpTransportSetter interface {
	SetHTTPTransport(rt http.RoundTripper)
}

var _ engine.Provider = (*Provider)(nil)
var _ engine.LineStreamProvider = (*Provider)(nil)

type client interface {
	DoCompletion(ctx context.Context, req *openai.CompletionRequest) (*openai.CompletionResponse, error)
	DoLineStream(ctx context.Context, req *openai.CompletionRequest, maxLines int, stopTokens []string) *openai.LineStream
}

type firstLineValidator func(p *Provider, ctx *StreamState, firstLine string) error

type requestBuilder func(p *Provider, ctx *RequestState) PreparedRequest
type resultParser func(p *Provider, ctx *RequestState, result *openai.StreamResult) *types.CompletionResponse
type streamResultParser func(p *Provider, ctx *StreamState, result *openai.StreamResult) *types.CompletionResponse

// PreparedRequest is the provider request plus response/stream handling facts
// derived while rendering that request.
type PreparedRequest struct {
	Completion       *openai.CompletionRequest
	Prefill          string
	LineStreamConfig engine.LineStreamConfig
}

// RequestState is provider-owned state for one collected CompletionInput.
// It carries the collected input and shared trimmed window.
type RequestState struct {
	Input        ctx.CompletionInput
	TrimmedLines []string
	WindowStart  int
	CursorLine   int
}

type StreamState struct {
	*RequestState

	cursorMarker     string
	cursorMarkerSeen bool
	cursorMarkerLine int
	linesReceived    int
}

func (c *StreamState) TransformLine(line string) (string, bool) {
	defer func() { c.linesReceived++ }()
	if c.cursorMarker == "" {
		return line, false
	}
	if !c.cursorMarkerSeen {
		if idx := strings.Index(line, c.cursorMarker); idx >= 0 {
			c.cursorMarkerSeen = true
			c.cursorMarkerLine = c.linesReceived
		}
	}
	stripped := strings.ReplaceAll(line, c.cursorMarker, "")
	return stripped, strings.TrimSpace(stripped) == "" && strings.Contains(line, c.cursorMarker)
}

func (c *StreamState) CursorMarkerPosition() (int, bool) {
	return c.cursorMarkerLine, c.cursorMarkerSeen
}

// Provider implements engine.Provider with provider-specific render and parse functions.
type Provider struct {
	Name                          string
	Config                        *types.ProviderConfig
	Client                        client
	LineStreaming                 bool
	CompleteWithTextRightOfCursor bool
	PrefetchAfterCursorTarget     bool
	BuildRequest                  requestBuilder
	ParseResult                   resultParser
	ParseStreamResult             streamResultParser
	StreamCursorMarker            string
	FirstLineValidator            firstLineValidator
	StopTokens                    []string // Stop tokens for streaming (provider-specific)
	Materials                     ctx.Materials
}

func (p *Provider) CanDo() engine.ProviderCanDo {
	return engine.ProviderCanDo{
		CompleteWithTextRightOfCursor: p.CompleteWithTextRightOfCursor,
		PrefetchAfterCursorTarget:     p.PrefetchAfterCursorTarget,
	}
}

func (p *Provider) RequiredMaterials() ctx.Materials {
	return p.Materials
}

// SetHTTPTransport forwards the transport override to the underlying client
// if it supports it. Used by the eval harness.
func (p *Provider) SetHTTPTransport(rt http.RoundTripper) {
	if setter, ok := p.Client.(httpTransportSetter); ok {
		setter.SetHTTPTransport(rt)
	}
}

// GetCompletion implements engine.Provider
func (p *Provider) GetCompletion(ctx context.Context, input ctx.CompletionInput) (*types.CompletionResponse, error) {
	defer logger.Trace("Provider.GetCompletion")()
	if p.BuildRequest == nil || p.ParseResult == nil {
		return p.EmptyResponse(), fmt.Errorf("%s: request flow is not configured", p.Name)
	}

	pctx, maxLines, skip := p.prepareRequestState(input)
	if skip {
		return p.EmptyResponse(), nil
	}

	prepared := p.BuildRequest(p, pctx)
	p.logRequest(prepared.Completion, maxLines)

	resp, err := p.Client.DoCompletion(ctx, prepared.Completion)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p.Name, err)
	}

	result := &openai.StreamResult{}
	if len(resp.Choices) > 0 {
		result.Text = resp.Choices[0].Text
		result.FinishReason = resp.Choices[0].FinishReason
	}
	if prepared.Prefill != "" {
		result.Text = prepared.Prefill + result.Text
	}
	p.logResponse(result)

	return p.ParseResult(p, pctx, result), nil
}

func (p *Provider) prepareRequestState(input ctx.CompletionInput) (*RequestState, int, bool) {
	current := input.Current
	if !p.CompleteWithTextRightOfCursor && current.Cursor.Row >= 1 && current.Cursor.Row <= len(current.File.Lines) {
		currentLine := current.File.Lines[current.Cursor.Row-1]
		if current.Cursor.Col < len(currentLine) {
			logger.Debug("%s: skipping, text after cursor", p.Name)
			return nil, 0, true
		}
	}

	pctx := &RequestState{Input: input}
	maxLines := 0
	cursorLine := current.Cursor.Row - 1
	var syntaxRanges []*types.LineRange
	if material, ok := ctx.Find[ctx.Treesitter](input.Materials); ok && material.Data != nil {
		syntaxRanges = material.Data.SyntaxRanges
	}
	contextSize := p.Config.ProviderContextSize
	if contextSize == 0 {
		contextSize = p.Config.ProviderMaxTokens
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

	return pctx, maxLines, false
}

func (p *Provider) EmptyResponse() *types.CompletionResponse {
	return &types.CompletionResponse{
		Completions:  []*types.Completion{},
		CursorTarget: nil,
	}
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
		Completions:  []*types.Completion{completion},
		CursorTarget: nil,
	}
}

func (p *Provider) logRequest(req *openai.CompletionRequest, maxLines int) {
	logger.Debug("%s provider request:\n  URL: %s%s\n  Model: %s\n  Temperature: %.2f\n  MaxTokens: %d\n  MaxLines: %d\n  Prompt length: %d chars\n  Prompt:\n%s",
		p.Name,
		p.Config.ProviderURL,
		p.Config.CompletionPath,
		req.Model,
		req.Temperature,
		req.MaxTokens,
		maxLines,
		len(req.Prompt),
		req.Prompt)
}

func (p *Provider) logResponse(result *openai.StreamResult) {
	logger.Debug("%s provider response:\n  Text length: %d chars\n  FinishReason: %s\n  StoppedEarly: %v\n  Text:\n%s",
		p.Name,
		len(result.Text),
		result.FinishReason,
		result.StoppedEarly,
		result.Text)
}

func (p *Provider) StreamsLines() bool {
	return p.LineStreaming
}

func (p *Provider) prepareStreamInput(input ctx.CompletionInput) (*openai.CompletionRequest, *StreamState, engine.LineStreamConfig, int, error) {
	if p.BuildRequest == nil {
		return nil, nil, engine.LineStreamConfig{}, 0, fmt.Errorf("%s: request flow is not configured", p.Name)
	}

	state, maxLines, skip := p.prepareRequestState(input)
	if skip {
		return nil, nil, engine.LineStreamConfig{}, 0, fmt.Errorf("%s: skip completion", p.Name)
	}

	prepared := p.BuildRequest(p, state)
	pctx := &StreamState{
		RequestState: state,
		cursorMarker: p.StreamCursorMarker,
	}
	streamConfig := engine.LineStreamConfig{
		WindowStart: state.WindowStart,
		OldLines:    state.TrimmedLines,
		Prefill:     prepared.Prefill,
	}
	if prepared.LineStreamConfig.OldLines != nil {
		streamConfig.WindowStart = prepared.LineStreamConfig.WindowStart
		streamConfig.OldLines = prepared.LineStreamConfig.OldLines
	} else if len(streamConfig.OldLines) == 0 {
		streamConfig.OldLines = input.Current.File.Lines
	}
	return prepared.Completion, pctx, streamConfig, maxLines, nil
}

// finishStream applies the streamed text to the provider state and parses it.
func (p *Provider) finishStream(providerState engine.ProviderStreamState, result *openai.StreamResult) (*types.CompletionResponse, error) {
	pctx, ok := providerState.(*StreamState)
	if !ok {
		return p.EmptyResponse(), fmt.Errorf("invalid provider context type")
	}
	p.logResponse(result)

	if p.ParseResult == nil {
		return p.EmptyResponse(), fmt.Errorf("%s: result parser is not configured", p.Name)
	}

	if p.ParseStreamResult != nil {
		return p.ParseStreamResult(p, pctx, result), nil
	}

	return p.ParseResult(p, pctx.RequestState, result), nil
}

func (p *Provider) PrepareLineStream(ctx context.Context, input ctx.CompletionInput) (engine.LineStream, engine.ProviderStreamState, engine.LineStreamConfig, error) {
	defer logger.Trace("Provider.PrepareLineStream")()
	completionReq, pctx, streamConfig, maxLines, err := p.prepareStreamInput(input)
	if err != nil {
		return nil, pctx, streamConfig, err
	}
	p.logRequest(completionReq, maxLines)
	return p.Client.DoLineStream(ctx, completionReq, maxLines, p.StopTokens), pctx, streamConfig, nil
}

func (p *Provider) ValidateFirstLine(providerState engine.ProviderStreamState, firstLine string) error {
	pctx, ok := providerState.(*StreamState)
	if !ok {
		return fmt.Errorf("invalid provider context type")
	}

	if p.FirstLineValidator != nil {
		if err := p.FirstLineValidator(p, pctx, firstLine); err != nil {
			logger.Debug("%s: first line validation failed: %v", p.Name, err)
			return err
		}
	}
	return nil
}

func (p *Provider) FinishLineStream(providerState engine.ProviderStreamState, text string, finishReason string, stoppedEarly bool) (*types.CompletionResponse, error) {
	return p.finishStream(providerState, &openai.StreamResult{
		Text:         text,
		FinishReason: finishReason,
		StoppedEarly: stoppedEarly,
	})
}
