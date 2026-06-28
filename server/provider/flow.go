// Package provider contains shared execution plumbing for leaf providers.
// Engine reads provider facts, then leaf providers keep protocol-specific
// request construction, transport calls, and response parsing in Build, Call,
// and Parse.
package provider

import (
	"context"

	sourcectx "cursortab/ctx"
	"cursortab/engine"
	"cursortab/types"
	"cursortab/utils"
)

// RequestWindow is the source window shared by Build, Parse, and streaming.
type RequestWindow struct {
	Lines      []string
	Start      int
	CursorLine int
	MaxLines   int
}

// RequestState is the shared fact source for one provider call.
type RequestState struct {
	Input  sourcectx.CompletionInput
	Window RequestWindow
}

// Base carries the provider facts engine reads before execution.
type Base struct {
	kind                            engine.CompletionKind
	canPrefetchFromSyntheticCurrent bool
	materials                       sourcectx.Materials
}

// SyntheticPrefetch declares whether an edit provider can consume
// engine-created current snapshots for cursor-target prefetch.
type SyntheticPrefetch bool

const (
	SyntheticPrefetchDisabled SyntheticPrefetch = false
	SyntheticPrefetchEnabled  SyntheticPrefetch = true
)

// NewBase declares a provider whose Build stage uses the supplied
// CompletionInput snapshot as its editor state.
func NewBase(kind engine.CompletionKind, materials sourcectx.Materials, syntheticPrefetch SyntheticPrefetch) Base {
	return Base{
		kind:                            kind,
		canPrefetchFromSyntheticCurrent: kind == engine.CompletionEdit && bool(syntheticPrefetch),
		materials:                       materials,
	}
}

func (b Base) CompletionKind() engine.CompletionKind {
	return b.kind
}

func (b Base) CanPrefetchFromSyntheticCurrent() bool {
	return b.canPrefetchFromSyntheticCurrent
}

func (b Base) RequiredMaterials() sourcectx.Materials {
	return b.materials
}

// CompletionFlow is the provider-local Build -> Call -> Parse pipeline.
//
// RequestPayload and RawResult are generic because each provider has its own
// protocol shape. OpenAI providers use *openai.CompletionRequest and
// *openai.CompletionResult. Mercury, Windsurf, and Copilot use package-local
// request/result types. The shared runner requires one relation: Build produces
// exactly the value Call accepts, and Call produces exactly the value Parse
// accepts.
type CompletionFlow[RequestPayload any, RawResult any] interface {
	Build(*RequestState) (RequestPayload, error)
	Call(context.Context, RequestPayload) (RawResult, error)
	Parse(*RequestState, RawResult) (*types.CompletionResponse, error)
}

// prepareRequestState derives the source frame shared by Build, Parse, and
// stream windowing.
func prepareRequestState(input sourcectx.CompletionInput, config *types.ProviderConfig) *RequestState {
	current := input.Current
	state := &RequestState{Input: input}
	cursorLine := current.Cursor.Row - 1
	var syntaxRanges []*types.LineRange
	if material, ok := sourcectx.Find[sourcectx.Treesitter](input.Materials); ok && material.Data != nil {
		syntaxRanges = material.Data.SyntaxRanges
	}
	contextSize := 0
	if config != nil {
		contextSize = config.ProviderContextSize
		if contextSize == 0 {
			contextSize = config.ProviderMaxTokens
		}
	}
	trimmedLines, newCursorLine, _, trimOffset, didTrim := utils.TrimContentAroundCursor(
		current.File.Lines,
		cursorLine,
		current.Cursor.Col,
		contextSize,
		syntaxRanges,
	)
	state.Window.Lines = trimmedLines
	state.Window.CursorLine = newCursorLine
	state.Window.Start = trimOffset

	if didTrim {
		state.Window.MaxLines = len(trimmedLines)
	}
	if current.ViewportHeight > 0 {
		if state.Window.MaxLines == 0 || current.ViewportHeight < state.Window.MaxLines {
			state.Window.MaxLines = current.ViewportHeight
		}
	}

	return state
}

func StartBatch[RequestPayload any, RawResult any](
	reqCtx context.Context,
	input sourcectx.CompletionInput,
	config *types.ProviderConfig,
	flow CompletionFlow[RequestPayload, RawResult],
) (*types.CompletionResponse, error) {
	state := prepareRequestState(input, config)
	payload, err := flow.Build(state)
	if err != nil {
		return nil, err
	}
	raw, err := flow.Call(reqCtx, payload)
	if err != nil {
		return nil, err
	}
	response, err := flow.Parse(state, raw)
	return response, err
}

func EmptyResponse() *types.CompletionResponse {
	return &types.CompletionResponse{}
}

// BuildCompletion applies the provider's parsed replacement unless it is a
// no-op against the current buffer lines.
func BuildCompletion(state *RequestState, startLine, endLineInc int, lines []string) *types.CompletionResponse {
	currentLines := state.Input.Current.File.Lines
	if endLineInc <= len(currentLines) && isNoOpReplacement(lines, currentLines[startLine-1:endLineInc]) {
		return EmptyResponse()
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
