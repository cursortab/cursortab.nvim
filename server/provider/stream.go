package provider

import (
	"context"
	"strings"
	"sync"

	"cursortab/client/openai"
	sourcectx "cursortab/ctx"
	"cursortab/engine"
	"cursortab/types"
)

// lineStreamMode holds provider-private mechanics for a streaming request:
// visible prefill, stream window, model-line validation, line normalization,
// and final response parsing.
type lineStreamMode struct {
	prefill            func(*RequestState) string
	window             func(*RequestState) (int, []string)
	firstLineValidator func(*Provider, *RequestState, string) error
	lineTransform      func(string) (string, bool)
	parser             resultParser
}

type lineStreamOption func(*lineStreamMode)

// WithLineStream enables provider-private line streaming for the shared
// Provider. Engine sees only the returned engine.CompletionStream.
func WithLineStream(opts ...lineStreamOption) providerOption {
	return func(p *Provider) {
		mode := &lineStreamMode{
			window: defaultLineStreamWindow,
			parser: p.parseResult,
		}
		for _, opt := range opts {
			opt(mode)
		}
		p.lineStream = mode
	}
}

// LineStreamPrefill emits provider-owned prefix lines before model output.
func LineStreamPrefill(fn func(*RequestState) string) lineStreamOption {
	return func(mode *lineStreamMode) {
		mode.prefill = fn
	}
}

// LineStreamWindow chooses the old line window exposed through
// engine.CompletionStream.Window.
func LineStreamWindow(fn func(*RequestState) (int, []string)) lineStreamOption {
	return func(mode *lineStreamMode) {
		mode.window = fn
	}
}

// LineStreamValidator checks the first emitted model line.
func LineStreamValidator(fn func(*Provider, *RequestState, string) error) lineStreamOption {
	return func(mode *lineStreamMode) {
		mode.firstLineValidator = fn
	}
}

// LineStreamLineTransform normalizes a raw model line before engine staging.
func LineStreamLineTransform(fn func(string) (string, bool)) lineStreamOption {
	return func(mode *lineStreamMode) {
		mode.lineTransform = fn
	}
}

// LineStreamParser converts the final provider stream result into an engine
// completion response.
func LineStreamParser(fn func(*Provider, *RequestState, *openai.StreamResult) *types.CompletionResponse) lineStreamOption {
	return func(mode *lineStreamMode) {
		mode.parser = resultParser(fn)
	}
}

func (p *Provider) startLineStream(ctx context.Context, input sourcectx.CompletionInput) (engine.CompletionStream, error) {
	state, req, maxLines, err := p.BuildOpenAIRequest(input)
	if err != nil {
		return nil, err
	}

	windowStart, oldLines := p.lineStream.window(state)
	run := &lineStreamRun{
		stream:      p.client.DoLineStream(ctx, req, maxLines),
		windowStart: windowStart,
		oldLines:    oldLines,
		lines:       make(chan string, 100),
		cancelCh:    make(chan struct{}),
		provider:    p,
		state:       state,
		mode:        p.lineStream,
	}
	go run.forward()
	return run, nil
}

func defaultLineStreamWindow(state *RequestState) (int, []string) {
	oldLines := state.TrimmedLines
	if len(oldLines) == 0 {
		oldLines = state.Input.Current.File.Lines
	}
	return state.WindowStart, oldLines
}

type lineStreamRun struct {
	stream      *openai.LineStream
	windowStart int
	oldLines    []string
	lines       chan string
	cancelCh    chan struct{}
	cancelOnce  sync.Once

	provider        *Provider
	state           *RequestState
	mode            *lineStreamMode
	accumulatedText strings.Builder
	validated       bool
	err             error
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
	rawResult := r.doneResult()
	if r.err != nil {
		return nil, r.err
	}
	if rawResult.Err != nil {
		return nil, rawResult.Err
	}

	result := openai.StreamResult{
		Text:         r.accumulatedText.String(),
		FinishReason: rawResult.FinishReason,
		StoppedEarly: rawResult.StoppedEarly,
	}
	r.provider.LogOpenAIResponse(&result)
	return r.mode.parser(r.provider, r.state, &result), nil
}

func (r *lineStreamRun) forward() {
	defer close(r.lines)

	if r.mode.prefill != nil {
		prefill := r.mode.prefill(r.state)
		if prefill != "" {
			for _, line := range strings.Split(strings.TrimSuffix(prefill, "\n"), "\n") {
				if !r.send(line) {
					return
				}
			}
		}
	}

	for rawLine := range r.stream.LinesChan() {
		r.accumulatedText.WriteString(rawLine)
		r.accumulatedText.WriteString("\n")

		line := rawLine
		emit := true
		if r.mode.lineTransform != nil {
			line, emit = r.mode.lineTransform(rawLine)
		}
		if emit && !r.emit(line) {
			return
		}
	}
}

func (r *lineStreamRun) emit(line string) bool {
	if r.mode.firstLineValidator != nil && !r.validated {
		if err := r.mode.firstLineValidator(r.provider, r.state, line); err != nil {
			r.err = err
			r.Cancel()
			return false
		}
		r.validated = true
	}
	return r.send(line)
}

func (r *lineStreamRun) send(line string) bool {
	select {
	case r.lines <- line:
		return true
	case <-r.cancelCh:
		return false
	}
}

func (r *lineStreamRun) doneResult() openai.StreamResult {
	result, ok := <-r.stream.DoneChan()
	if !ok {
		return openai.StreamResult{FinishReason: "cancelled", StoppedEarly: true}
	}
	return result
}
