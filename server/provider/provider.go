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
	"sync"
)

// httpTransportSetter is implemented by clients that allow their outgoing
// HTTP transport to be replaced. Used by the eval harness for record/replay.
type httpTransportSetter interface {
	SetHTTPTransport(rt http.RoundTripper)
}

var _ engine.Provider = (*Provider)(nil)

type client interface {
	DoCompletion(ctx context.Context, req *openai.CompletionRequest) (*openai.CompletionResponse, error)
	DoLineStream(ctx context.Context, req *openai.CompletionRequest, maxLines int, stopTokens []string) *openai.LineStream
}

type firstLineValidator func(p *Provider, ctx *StreamState, firstLine string) error

type requestBuilder func(p *Provider, ctx *RequestState) PreparedRequest
type resultParser func(p *Provider, ctx *RequestState, result *openai.StreamResult) *types.CompletionResponse
type streamResultParser func(p *Provider, ctx *StreamState, result *openai.StreamResult) *types.CompletionResponse

type lineStreamMode struct {
	stopTokens         []string
	firstLineValidator firstLineValidator
	cursorMarker       string
	parseStreamResult  streamResultParser
}

// PreparedRequest is the provider request plus response/stream handling facts
// derived while rendering that request.
type PreparedRequest struct {
	Completion        *openai.CompletionRequest
	Prefill           string
	StreamWindowStart int
	StreamOldLines    []string
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
	Name           string
	Config         *types.ProviderConfig
	Client         client
	Completion     engine.CompletionKind
	InputAuthority engine.CompletionInputAuthority
	BuildRequest   requestBuilder
	ParseResult    resultParser
	lineStream     *lineStreamMode
	Materials      ctx.Materials
}

func (p *Provider) CompletionKind() engine.CompletionKind {
	return p.Completion
}

func (p *Provider) CompletionInputAuthority() engine.CompletionInputAuthority {
	return p.InputAuthority
}

func (p *Provider) RequiredMaterials() ctx.Materials {
	return p.Materials
}

func (p *Provider) UseLineStream(stopTokens []string, validator firstLineValidator, cursorMarker string, parser streamResultParser) *Provider {
	p.lineStream = &lineStreamMode{
		stopTokens:         stopTokens,
		firstLineValidator: validator,
		cursorMarker:       cursorMarker,
		parseStreamResult:  parser,
	}
	return p
}

// SetHTTPTransport forwards the transport override to the underlying client
// if it supports it. Used by the eval harness.
func (p *Provider) SetHTTPTransport(rt http.RoundTripper) {
	if setter, ok := p.Client.(httpTransportSetter); ok {
		setter.SetHTTPTransport(rt)
	}
}

func (p *Provider) StartCompletion(ctx context.Context, input ctx.CompletionInput, allowStream bool) (*types.CompletionResponse, engine.CompletionStream, error) {
	defer logger.Trace("Provider.StartCompletion")()
	if p.BuildRequest == nil || p.ParseResult == nil {
		return p.EmptyResponse(), nil, fmt.Errorf("%s: request flow is not configured", p.Name)
	}

	pctx, maxLines, skip := p.prepareRequestState(input)
	if skip {
		return p.EmptyResponse(), nil, nil
	}

	prepared := p.BuildRequest(p, pctx)
	p.logRequest(prepared.Completion, maxLines)

	if allowStream && p.lineStream != nil {
		streamCtx := &StreamState{
			RequestState: pctx,
			cursorMarker: p.lineStream.cursorMarker,
		}
		windowStart := pctx.WindowStart
		oldLines := pctx.TrimmedLines
		if prepared.StreamOldLines != nil {
			windowStart = prepared.StreamWindowStart
			oldLines = prepared.StreamOldLines
		} else if len(oldLines) == 0 {
			oldLines = input.Current.File.Lines
		}
		stream := p.Client.DoLineStream(ctx, prepared.Completion, maxLines, p.lineStream.stopTokens)
		return nil, newLineStreamRun(p, streamCtx, stream, windowStart, oldLines, prepared.Prefill), nil
	}

	resp, err := p.Client.DoCompletion(ctx, prepared.Completion)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", p.Name, err)
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

	return p.ParseResult(p, pctx, result), nil, nil
}

func (p *Provider) prepareRequestState(input ctx.CompletionInput) (*RequestState, int, bool) {
	current := input.Current
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

// finishStream applies the streamed text to the provider state and parses it.
func (p *Provider) finishStream(pctx *StreamState, result *openai.StreamResult) (*types.CompletionResponse, error) {
	p.logResponse(result)

	if p.ParseResult == nil {
		return p.EmptyResponse(), fmt.Errorf("%s: result parser is not configured", p.Name)
	}

	if p.lineStream != nil && p.lineStream.parseStreamResult != nil {
		return p.lineStream.parseStreamResult(p, pctx, result), nil
	}

	return p.ParseResult(p, pctx.RequestState, result), nil
}

type lineStreamRun struct {
	provider    *Provider
	state       *StreamState
	stream      *openai.LineStream
	windowStart int
	oldLines    []string

	lines      chan string
	cancelCh   chan struct{}
	cancelOnce sync.Once
	finishOnce sync.Once

	mu              sync.Mutex
	accumulatedText strings.Builder
	validated       bool
	response        *types.CompletionResponse
	err             error
}

func newLineStreamRun(p *Provider, state *StreamState, stream *openai.LineStream, windowStart int, oldLines []string, prefill string) *lineStreamRun {
	run := &lineStreamRun{
		provider:    p,
		state:       state,
		stream:      stream,
		windowStart: windowStart,
		oldLines:    oldLines,
		lines:       make(chan string, 100),
		cancelCh:    make(chan struct{}),
	}
	go run.forwardLines(prefill)
	return run
}

func (r *lineStreamRun) Lines() <-chan string {
	return r.lines
}

func (r *lineStreamRun) Window() (int, []string) {
	return r.windowStart, r.oldLines
}

func (r *lineStreamRun) Cancel() {
	r.cancelOnce.Do(func() {
		r.stream.Cancel()
		close(r.cancelCh)
	})
}

func (r *lineStreamRun) Finish() (*types.CompletionResponse, error) {
	r.finishOnce.Do(func() {
		result, ok := <-r.stream.DoneChan()
		if !ok {
			result = openai.StreamResult{FinishReason: "cancelled", StoppedEarly: true}
		}
		r.mu.Lock()
		if r.err != nil {
			r.mu.Unlock()
			return
		}
		result.Text = r.accumulatedText.String()
		r.mu.Unlock()
		r.response, r.err = r.provider.finishStream(r.state, &result)
	})
	return r.response, r.err
}

func (r *lineStreamRun) forwardLines(prefill string) {
	defer close(r.lines)
	if prefill != "" {
		for _, line := range strings.Split(strings.TrimSuffix(prefill, "\n"), "\n") {
			if !r.emitLine(line) {
				return
			}
		}
	}
	for line := range r.stream.LinesChan() {
		if !r.emitLine(line) {
			return
		}
	}
}

func (r *lineStreamRun) emitLine(line string) bool {
	line, skip := r.state.TransformLine(line)
	if skip {
		return true
	}
	if !r.validated {
		if mode := r.provider.lineStream; mode != nil && mode.firstLineValidator != nil {
			if err := mode.firstLineValidator(r.provider, r.state, line); err != nil {
				logger.Debug("%s: first line validation failed: %v", r.provider.Name, err)
				r.mu.Lock()
				r.err = err
				r.mu.Unlock()
				r.Cancel()
				return false
			}
		}
		r.validated = true
	}

	r.mu.Lock()
	r.accumulatedText.WriteString(line)
	r.accumulatedText.WriteString("\n")
	r.mu.Unlock()

	select {
	case r.lines <- line:
		return true
	case <-r.cancelCh:
		return false
	}
}
