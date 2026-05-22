package provider

import (
	"context"
	"cursortab/client/openai"
	"cursortab/engine"
	"cursortab/logger"
	"cursortab/types"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// HTTPTransportSetter is implemented by clients that allow their outgoing
// HTTP transport to be replaced. Used by the eval harness for record/replay.
type HTTPTransportSetter interface {
	SetHTTPTransport(rt http.RoundTripper)
}

// Re-exports of engine streaming-type constants so sub-provider packages
// (zeta, fim, inline, sweep, ...) can reference them as provider.StreamingX
// without taking a direct dependency on the engine package.
const (
	StreamingNone   = engine.StreamingTypeNone
	StreamingLines  = engine.StreamingTypeLines
	StreamingTokens = engine.StreamingTypeTokens
)

// Compile-time checks that Provider implements the required interfaces
var _ engine.Provider = (*Provider)(nil)
var _ engine.LineStreamProvider = (*Provider)(nil)
var _ engine.TokenStreamProvider = (*Provider)(nil)

// Client interface for API calls (enables mocking in tests)
type Client interface {
	DoCompletion(ctx context.Context, req *openai.CompletionRequest) (*openai.CompletionResponse, error)
	DoLineStream(ctx context.Context, req *openai.CompletionRequest, maxLines int, stopTokens []string) *openai.LineStream
	DoTokenStream(ctx context.Context, req *openai.CompletionRequest, maxChars int, stopTokens []string) *openai.LineStream
}

// Validator validates streaming content (e.g., first line anchor validation)
// Called after receiving the first line. Return error to cancel the stream.
type Validator func(p *Provider, ctx *Context, firstLine string) error

// DiffHistoryBuilder processes multi-file diff history into a string for the prompt
type DiffHistoryBuilder func(history []*types.FileDiffHistory) string

// Context carries data through the completion pipeline
type Context struct {
	Request      *types.CompletionRequest
	TrimmedLines []string
	WindowStart  int    // 0-indexed
	WindowEnd    int    // 0-indexed, exclusive
	CursorLine   int    // 0-indexed within trimmed lines
	MaxLines     int    // for streaming limit (0 = no limit)
	EndLineInc   int    // 1-indexed inclusive end line, set by AnchorTruncation (0 = not set)
	Prefill      string // prompt suffix prepended to result before postprocessors
	Result       *openai.StreamResult

	// Streaming state
	CompletionRequest *openai.CompletionRequest // Built request for streaming

	// Stream context for FIM-style providers (implements engine.StreamContext)
	StreamOldLines []string // custom old lines for streaming diff (nil = not applicable)
	StreamBaseOff  int      // 0-indexed base offset in buffer
	FirstLinePfx   string   // prefix to prepend to first streamed line
	LastLineSfx    string   // suffix to append to last streamed line

	// Per-line cursor-marker stripping (e.g. Zeta2's <|user_cursor|> sentinel).
	// Providers that need to strip in-band markers from every streamed line set
	// CursorMarker in a preprocessor. TransformLine then strips the marker and
	// records the position of its first occurrence in CursorMarkerLine /
	// CursorMarkerCol (measured in the post-strip line sequence and the
	// post-strip byte column, respectively). LinesReceived counts every line
	// that has flowed through TransformLine so far, giving the marker's line
	// index within the streamed response.
	CursorMarker     string // sentinel to strip, or "" for no-op
	CursorMarkerSeen bool   // true once the marker has been observed
	CursorMarkerLine int    // 0-indexed line in the streamed response
	CursorMarkerCol  int    // 0-indexed byte offset within the stripped line
	LinesReceived    int    // running count of streamed lines seen by TransformLine
	SkipLine         bool   // true when TransformLine consumed a marker-only line

	// Cached editable range for FIM providers. Set by assemblePrompt,
	// reused by parseCompletion to avoid recomputing.
	EditableStart int // [start, end) within TrimmedLines
	EditableEnd   int
}

// GetWindowStart returns the 0-indexed start offset of the trimmed window.
func (c *Context) GetWindowStart() int {
	return c.WindowStart
}

// GetTrimmedLines returns the trimmed lines sent to the model.
func (c *Context) GetTrimmedLines() []string {
	return c.TrimmedLines
}

// GetPrefill returns the prompt prefill text.
func (c *Context) GetPrefill() string {
	return c.Prefill
}

// GetStreamOldLines returns custom old lines for streaming diff.
func (c *Context) GetStreamOldLines() []string {
	return c.StreamOldLines
}

// GetStreamBaseOffset returns the 0-indexed base offset in buffer.
func (c *Context) GetStreamBaseOffset() int {
	return c.StreamBaseOff
}

// TransformFirstLine prepends the stored prefix to the first streamed line.
func (c *Context) TransformFirstLine(line string) string {
	return c.FirstLinePfx + line
}

// TransformLastLine appends the stored suffix to the last streamed line.
func (c *Context) TransformLastLine(line string) string {
	return line + c.LastLineSfx
}

// TransformLine strips the provider-configured CursorMarker (if any) from
// every streamed line and records the marker's first observed position.
// No-op counter increment when no CursorMarker is set.
//
// When the marker constitutes the entire line, SkipLine is set so the caller
// can drop the line from accumulation and stage building.
func (c *Context) TransformLine(line string) string {
	c.SkipLine = false
	defer func() { c.LinesReceived++ }()
	if c.CursorMarker == "" {
		return line
	}
	if !c.CursorMarkerSeen {
		if idx := strings.Index(line, c.CursorMarker); idx >= 0 {
			c.CursorMarkerSeen = true
			c.CursorMarkerLine = c.LinesReceived
			c.CursorMarkerCol = idx
		}
	}
	stripped := strings.ReplaceAll(line, c.CursorMarker, "")
	if strings.TrimSpace(stripped) == "" && strings.Contains(line, c.CursorMarker) {
		c.SkipLine = true
	}
	return stripped
}

// ShouldSkipLine returns true when the last TransformLine call consumed a
// marker-only line that should be dropped from the stream.
func (c *Context) ShouldSkipLine() bool {
	return c.SkipLine
}

// Provider implements engine.Provider with a configurable pipeline
type Provider struct {
	Name           string
	Config         *types.ProviderConfig
	Client         Client
	StreamingType  engine.StreamingType // Type of streaming (None, Lines, Tokens)
	Preprocessors  []Preprocessor
	PromptBuilder  PromptBuilder
	Postprocessors []Postprocessor
	Validators     []Validator        // Validators run on first line during streaming
	StopTokens     []string           // Stop tokens for streaming (provider-specific)
	DiffBuilder    DiffHistoryBuilder // Processes diff history for the prompt
	ContextLimits  engine.ContextLimits
}

// GetContextLimits implements engine.Provider
func (p *Provider) GetContextLimits() engine.ContextLimits {
	return p.ContextLimits.WithDefaults()
}

// SetHTTPTransport forwards the transport override to the underlying client
// if it supports it. Used by the eval harness.
func (p *Provider) SetHTTPTransport(rt http.RoundTripper) {
	if setter, ok := p.Client.(HTTPTransportSetter); ok {
		setter.SetHTTPTransport(rt)
	}
}

// GetCompletion implements engine.Provider
func (p *Provider) GetCompletion(ctx context.Context, req *types.CompletionRequest) (*types.CompletionResponse, error) {
	defer logger.Trace("Provider.GetCompletion")()
	pctx := &Context{Request: req}

	for _, pre := range p.Preprocessors {
		if err := pre(p, pctx); err != nil {
			if errors.Is(err, ErrSkipCompletion) {
				return p.EmptyResponse(), nil
			}
			return nil, fmt.Errorf("%s: %w", p.Name, err)
		}
	}

	completionReq := p.PromptBuilder(p, pctx)
	p.logRequest(completionReq, pctx.MaxLines)

	resp, err := p.Client.DoCompletion(ctx, completionReq)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p.Name, err)
	}

	result := &openai.StreamResult{}
	if len(resp.Choices) > 0 {
		result.Text = resp.Choices[0].Text
		result.FinishReason = resp.Choices[0].FinishReason
	}
	pctx.Result = result
	if pctx.Prefill != "" {
		pctx.Result.Text = pctx.Prefill + pctx.Result.Text
	}
	p.logResponse(result)

	for _, post := range p.Postprocessors {
		if resp, done := post(p, pctx); done {
			return resp, nil
		}
	}

	return p.EmptyResponse(), nil
}

// EmptyResponse returns an empty completion response
func (p *Provider) EmptyResponse() *types.CompletionResponse {
	return &types.CompletionResponse{
		Completions:  []*types.Completion{},
		CursorTarget: nil,
	}
}

// BuildCompletion creates a completion response, returning empty if it's a no-op.
// startLine and endLineInc are 1-indexed.
func (p *Provider) BuildCompletion(ctx *Context, startLine, endLineInc int, lines []string) (*types.CompletionResponse, bool) {
	req := ctx.Request
	if endLineInc <= len(req.Lines) && IsNoOpReplacement(lines, req.Lines[startLine-1:endLineInc]) {
		return p.EmptyResponse(), true
	}

	completion := &types.Completion{
		StartLine:  startLine,
		EndLineInc: endLineInc,
		Lines:      lines,
	}

	return &types.CompletionResponse{
		Completions:  []*types.Completion{completion},
		CursorTarget: nil,
	}, true
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

// GetStreamingType returns the streaming type for this provider.
func (p *Provider) GetStreamingType() engine.StreamingType {
	return p.StreamingType
}

// prepareStream runs preprocessors and builds the prompt, returning the
// completion request and provider context ready to be passed to the client.
func (p *Provider) prepareStream(req *types.CompletionRequest) (*openai.CompletionRequest, *Context, error) {
	pctx := &Context{Request: req}

	for _, pre := range p.Preprocessors {
		if err := pre(p, pctx); err != nil {
			if errors.Is(err, ErrSkipCompletion) {
				return nil, pctx, ErrSkipCompletion
			}
			return nil, nil, fmt.Errorf("%s: %w", p.Name, err)
		}
	}

	completionReq := p.PromptBuilder(p, pctx)
	pctx.CompletionRequest = completionReq
	return completionReq, pctx, nil
}

// finishStream applies the streamed text to the context and runs postprocessors.
func (p *Provider) finishStream(providerCtx any, result *openai.StreamResult) (*types.CompletionResponse, error) {
	pctx, ok := providerCtx.(*Context)
	if !ok {
		return p.EmptyResponse(), fmt.Errorf("invalid provider context type")
	}
	pctx.Result = result
	p.logResponse(result)

	for _, post := range p.Postprocessors {
		if resp, done := post(p, pctx); done {
			return resp, nil
		}
	}
	return p.EmptyResponse(), nil
}

// PrepareLineStream runs preprocessors, builds the prompt, and returns the stream.
// Returns (stream, providerContext, error). Implements engine.LineStreamProvider.
func (p *Provider) PrepareLineStream(ctx context.Context, req *types.CompletionRequest) (engine.LineStream, any, error) {
	defer logger.Trace("Provider.PrepareLineStream")()
	completionReq, pctx, err := p.prepareStream(req)
	if err != nil {
		return nil, pctx, err
	}
	p.logRequest(completionReq, pctx.MaxLines)
	return p.Client.DoLineStream(ctx, completionReq, pctx.MaxLines, p.StopTokens), pctx, nil
}

// ValidateFirstLine runs validators on the first received line (implements engine.LineStreamProvider)
func (p *Provider) ValidateFirstLine(providerCtx any, firstLine string) error {
	pctx, ok := providerCtx.(*Context)
	if !ok {
		return fmt.Errorf("invalid provider context type")
	}

	for _, validator := range p.Validators {
		if err := validator(p, pctx, firstLine); err != nil {
			logger.Debug("%s: first line validation failed: %v", p.Name, err)
			return err
		}
	}
	return nil
}

// FinishLineStream runs postprocessors on the accumulated result (implements engine.LineStreamProvider)
func (p *Provider) FinishLineStream(providerCtx any, text string, finishReason string, stoppedEarly bool) (*types.CompletionResponse, error) {
	return p.finishStream(providerCtx, &openai.StreamResult{
		Text:         text,
		FinishReason: finishReason,
		StoppedEarly: stoppedEarly,
	})
}

// PrepareTokenStream runs preprocessors, builds the prompt, and returns a token stream.
// Returns (stream, providerContext, error). Implements engine.TokenStreamProvider.
func (p *Provider) PrepareTokenStream(ctx context.Context, req *types.CompletionRequest) (engine.LineStream, any, error) {
	defer logger.Trace("Provider.PrepareTokenStream")()
	completionReq, pctx, err := p.prepareStream(req)
	if err != nil {
		return nil, pctx, err
	}
	p.logRequest(completionReq, 0) // maxLines=0 for token streaming
	return p.Client.DoTokenStream(ctx, completionReq, 0, p.StopTokens), pctx, nil
}

// FinishTokenStream runs postprocessors on the final accumulated result (implements engine.TokenStreamProvider)
func (p *Provider) FinishTokenStream(providerCtx any, text string) (*types.CompletionResponse, error) {
	return p.finishStream(providerCtx, &openai.StreamResult{
		Text:         text,
		FinishReason: "stop",
		StoppedEarly: false,
	})
}
