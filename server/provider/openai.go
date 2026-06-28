package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"cursortab/client/openai"
	sourcectx "cursortab/ctx"
	"cursortab/engine"
	"cursortab/logger"
	"cursortab/types"
)

// OpenAI supplies the Call stage for providers that use the OpenAI completions
// API. Leaf providers embed it, build their own prompt in Build, and parse the
// resulting openai.CompletionResult in Parse.
type OpenAI struct {
	config *types.ProviderConfig
	name   string
	client *openai.Client
}

func NewOpenAI(name string, config *types.ProviderConfig) OpenAI {
	return OpenAI{
		config: config,
		name:   name,
		client: openai.NewClient(config.ProviderURL, config.CompletionPath, config.APIKey),
	}
}

func (o OpenAI) ProviderConfig() *types.ProviderConfig {
	return o.config
}

// Call maps one OpenAI completion response to the RawResult used by OpenAI
// based CompletionFlow implementations.
func (o OpenAI) Call(ctx context.Context, req *openai.CompletionRequest) (*openai.CompletionResult, error) {
	resp, err := o.client.DoCompletion(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", o.name, err)
	}

	result := &openai.CompletionResult{}
	if len(resp.Choices) > 0 {
		result = &openai.CompletionResult{
			Text:         resp.Choices[0].Text,
			FinishReason: resp.Choices[0].FinishReason,
		}
	}
	logOpenAIResponse(o.name, result)
	return result, nil
}

// SetHTTPTransport is used by eval cassette record/replay.
func (o OpenAI) SetHTTPTransport(rt http.RoundTripper) {
	o.client.SetHTTPTransport(rt)
}

func (o OpenAI) LogRequest(req *openai.CompletionRequest, maxLines int) {
	logger.Debug("%s provider request:\n  URL: %s%s\n  Model: %s\n  Temperature: %.2f\n  MaxTokens: %d\n  MaxLines: %d\n  Prompt length: %d chars\n  Prompt:\n%s",
		o.name,
		o.config.ProviderURL,
		o.config.CompletionPath,
		req.Model,
		req.Temperature,
		req.MaxTokens,
		maxLines,
		len(req.Prompt),
		req.Prompt)
}

// Request fills the config-derived OpenAI fields shared by every OpenAI leaf.
// Prompt, suffix, and stop tokens remain leaf protocol facts.
func (o OpenAI) Request(prompt string, stop []string) *openai.CompletionRequest {
	return &openai.CompletionRequest{
		Model:       o.config.ProviderModel,
		Prompt:      prompt,
		Temperature: o.config.ProviderTemperature,
		MaxTokens:   o.config.ProviderMaxTokens,
		TopK:        o.config.ProviderTopK,
		Stop:        stop,
		N:           1,
		Echo:        false,
	}
}

func logOpenAIResponse(name string, result *openai.CompletionResult) {
	logger.Debug("%s provider response:\n  Text length: %d chars\n  FinishReason: %s\n  StoppedEarly: %v\n  Text:\n%s",
		name,
		len(result.Text),
		result.FinishReason,
		result.StoppedEarly,
		result.Text)
}

// OpenAIStreamArgs is the leaf-selected stream behavior for one built request.
// Sweep uses prefill and first-line validation. Zeta uses first-line
// validation. Zeta2 uses a cursor-marker line transform and its own stream
// window. Engine sees only the CompletionStream returned by StartStream.
type OpenAIStreamArgs struct {
	WindowStart        int
	OldLines           []string
	Prefill            string
	FirstLineValidator func(*RequestState, string) error
	LineTransform      func(string) (string, bool)
}

type OpenAIStreamFlow interface {
	CompletionFlow[*openai.CompletionRequest, *openai.CompletionResult]
	StreamArgs(*RequestState) OpenAIStreamArgs
}

// lineStreamSession is the streaming Call runtime. It forwards visible lines
// to engine while Finish parses the raw text collected by the OpenAI client.
type lineStreamSession struct {
	name        string
	stream      *openai.LineStream
	windowStart int
	oldLines    []string
	lines       chan string
	cancelCh    chan struct{}
	cancelOnce  sync.Once

	prefill            string
	firstLineValidator func(*RequestState, string) error
	lineTransform      func(string) (string, bool)
	parse              func(*RequestState, *openai.CompletionResult) (*types.CompletionResponse, error)
	state              *RequestState

	validated bool
	err       error
}

func (o OpenAI) StartStream(ctx context.Context, input sourcectx.CompletionInput, config *types.ProviderConfig, flow OpenAIStreamFlow) (engine.CompletionStream, error) {
	state := prepareRequestState(input, config)
	req, err := flow.Build(state)
	if err != nil {
		return nil, err
	}
	return o.startStream(ctx, state, req, flow.StreamArgs(state), flow.Parse)
}

func (o OpenAI) startStream(
	ctx context.Context,
	state *RequestState,
	req *openai.CompletionRequest,
	args OpenAIStreamArgs,
	parse func(*RequestState, *openai.CompletionResult) (*types.CompletionResponse, error),
) (engine.CompletionStream, error) {
	run := &lineStreamSession{
		name:               o.name,
		stream:             o.client.DoLineStream(ctx, req, state.Window.MaxLines),
		windowStart:        args.WindowStart,
		oldLines:           args.OldLines,
		lines:              make(chan string, 100),
		cancelCh:           make(chan struct{}),
		prefill:            args.Prefill,
		firstLineValidator: args.FirstLineValidator,
		lineTransform:      args.LineTransform,
		parse:              parse,
		state:              state,
	}
	go run.forward()
	return run, nil
}

func (s *lineStreamSession) Lines() <-chan string {
	return s.lines
}

func (s *lineStreamSession) Window() (int, []string) {
	return s.windowStart, s.oldLines
}

func (s *lineStreamSession) Cancel() {
	s.cancelOnce.Do(func() {
		s.stream.Cancel()
		close(s.cancelCh)
	})
}

// Finish turns the accumulated stream text into the same RawResult shape as
// batch Call, then invokes the leaf Parse function.
func (s *lineStreamSession) Finish() (*types.CompletionResponse, error) {
	rawResult := s.doneResult()
	if s.err != nil {
		return nil, s.err
	}
	if rawResult.Err != nil {
		return nil, rawResult.Err
	}

	result := &openai.CompletionResult{
		Text:         rawResult.Text,
		FinishReason: rawResult.FinishReason,
		StoppedEarly: rawResult.StoppedEarly,
	}
	logOpenAIResponse(s.name, result)
	return s.parse(s.state, result)
}

func (s *lineStreamSession) forward() {
	defer close(s.lines)

	if s.prefill != "" {
		for _, line := range strings.Split(strings.TrimSuffix(s.prefill, "\n"), "\n") {
			if !s.send(line) {
				return
			}
		}
	}

	for rawLine := range s.stream.LinesChan() {
		line := rawLine
		emit := true
		if s.lineTransform != nil {
			line, emit = s.lineTransform(rawLine)
		}
		if emit && !s.emit(line) {
			return
		}
	}
}

func (s *lineStreamSession) emit(line string) bool {
	if s.firstLineValidator != nil && !s.validated {
		if err := s.firstLineValidator(s.state, line); err != nil {
			s.err = err
			s.Cancel()
			return false
		}
		s.validated = true
	}
	return s.send(line)
}

func (s *lineStreamSession) send(line string) bool {
	select {
	case s.lines <- line:
		return true
	case <-s.cancelCh:
		return false
	}
}

func (s *lineStreamSession) doneResult() openai.CompletionResult {
	result, ok := <-s.stream.DoneChan()
	if !ok {
		return openai.CompletionResult{FinishReason: "cancelled", StoppedEarly: true}
	}
	return result
}
