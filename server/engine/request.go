package engine

import (
	"context"
	"errors"
	"slices"

	"cursortab/ctx"
	"cursortab/logger"
	"cursortab/text"
	"cursortab/types"
	"cursortab/utils"
)

func completionInputCompatible(kind CompletionKind, current ctx.CurrentSnapshot) bool {
	if kind != CompletionInline {
		return true
	}
	if current.Cursor.Row < 1 || current.Cursor.Row > len(current.File.Lines) {
		return false
	}
	line := current.File.Lines[current.Cursor.Row-1]
	if current.Cursor.Col < 0 {
		return false
	}
	if current.Cursor.Col >= len(line) {
		return true
	}
	return inertSuffixPattern.MatchString(line[current.Cursor.Col:])
}

func (e *Engine) collectCompletionInput(parent context.Context, sourceInput ctx.ContextSourceInput, requirements ctx.Materials) (ctx.CompletionInput, error) {
	input := ctx.CompletionInput{Current: sourceInput.Current}
	collected, err := ctx.Collect(parent, sourceInput, requirements)
	if err != nil {
		return input, err
	}
	input.Materials = collected
	return input, nil
}

func (e *Engine) suppressCompletionRequest(source types.CompletionSource, manual bool) string {
	if manual {
		return ""
	}
	if e.suppressForNoEdits() {
		return "no-edits"
	}
	if e.suppressForDisabledScope() != "" {
		return "disabled-scope"
	}
	if e.suppressForMidLine() {
		return "mid-line"
	}
	if source == types.CompletionSourceTyping && e.suppressForSingleDeletion() {
		return "single-deletion"
	}
	return ""
}

func logCompletionSuppression(reason string) {
	switch reason {
	case "no-edits":
		logger.Debug("suppressed: no recent edits")
	case "disabled-scope":
		logger.Debug("suppressed: disabled treesitter scope")
	case "mid-line":
		logger.Debug("suppressed: mid-line cursor position")
	case "single-deletion":
		logger.Debug("suppressed: single deletion")
	}
}

func (e *Engine) requestCompletion(source types.CompletionSource, manual bool) {
	if e.stopped {
		return
	}

	// Drop any leftover stream from a prior accept-during-streaming. The
	// new request supersedes its "next prediction" output; without this,
	// the leftover stream's late completion hits handleStreamCompleteAfterAccept
	// and rewrites state we're about to set up here.
	e.cancelStreaming()

	e.syncBuffer()

	if reason := e.suppressCompletionRequest(source, manual); reason != "" {
		logCompletionSuppression(reason)
		return
	}

	e.lastCompletionSource = source

	requirements := e.provider.RequiredMaterials()
	sourceInput := e.buildContextSourceInput(completionInputOptions{}, requirements)
	e.completionRequestID++
	requestID := e.completionRequestID
	if !completionInputCompatible(e.provider.CompletionKind(), sourceInput.Current) {
		e.state = statePendingCompletion
		select {
		case e.eventChan <- Event{Type: EventCompletionReady, RequestID: requestID, Manual: manual, Response: &types.CompletionResponse{}}:
		case <-e.mainCtx.Done():
		}
		return
	}

	input, err := e.collectCompletionInput(e.mainCtx, sourceInput, requirements)
	if err != nil {
		select {
		case e.eventChan <- Event{Type: EventCompletionError, RequestID: requestID, Err: err}:
		case <-e.mainCtx.Done():
		}
		return
	}

	e.state = statePendingCompletion
	reqCtx, cancel := context.WithTimeout(e.mainCtx, e.config.CompletionTimeout)
	e.currentCancel = cancel
	go func() {
		result, stream, err := e.provider.StartCompletion(reqCtx, input, true)
		if err != nil {
			cancel()
			select {
			case e.eventChan <- Event{Type: EventCompletionError, RequestID: requestID, Err: err}:
			case <-e.mainCtx.Done():
			}
			return
		}
		if stream != nil {
			select {
			case e.eventChan <- Event{Type: EventCompletionReady, RequestID: requestID, Manual: manual, Stream: stream}:
			case <-e.mainCtx.Done():
				cancel()
			}
			return
		}
		cancel()
		select {
		case e.eventChan <- Event{Type: EventCompletionReady, RequestID: requestID, Manual: manual, Response: result}:
		case <-e.mainCtx.Done():
		}
	}()
}

// getViewportHeightConstraint returns the viewport height constraint for completion requests.
func (e *Engine) getViewportHeightConstraint() int {
	if e.config.CursorPrediction.Enabled {
		return 0
	}
	_, viewportBottom := e.buffer.ViewportBounds()
	if viewportBottom > 0 && e.buffer.Row() > 0 {
		// +1 because both cursor and viewport bottom are inclusive (cursor on
		// last visible line means 1 visible line remaining, not 0).
		if constraint := viewportBottom - e.buffer.Row() + 1; constraint > 0 {
			return constraint
		}
	}
	return 0
}

type prefetchOpts struct {
	Lines []string // Override buffer lines (nil = use current buffer)
}

func (e *Engine) requestPrefetch(overrideRow, overrideCol int, opts prefetchOpts) {
	if e.stopped {
		return
	}

	if e.suppressForNoEdits() {
		logger.Debug("prefetch suppressed: no recent edits")
		return
	}

	e.cancelPrefetch()

	e.syncBuffer()

	e.prefetchState = prefetchInFlight

	// Build the frozen request input before the goroutine starts so it cannot
	// race with later buffer or file-state mutations.
	requirements := e.provider.RequiredMaterials()
	sourceInput := e.buildContextSourceInput(completionInputOptions{
		lines:             opts.Lines,
		cursorRow:         overrideRow,
		cursorCol:         overrideCol,
		hasCursorOverride: true,
	}, requirements)
	if !completionInputCompatible(e.provider.CompletionKind(), sourceInput.Current) {
		select {
		case e.eventChan <- Event{Type: EventPrefetchReady, Response: &types.CompletionResponse{}}:
		case <-e.mainCtx.Done():
		}
		return
	}
	input, err := e.collectCompletionInput(e.mainCtx, sourceInput, requirements)
	if err != nil {
		select {
		case e.eventChan <- Event{Type: EventPrefetchError, Err: err}:
		case <-e.mainCtx.Done():
		}
		return
	}

	reqCtx, cancel := context.WithTimeout(e.mainCtx, e.config.CompletionTimeout)
	e.prefetchCancel = cancel
	go func() {
		defer cancel()
		result, stream, err := e.provider.StartCompletion(reqCtx, input, false)
		if err != nil {
			select {
			case e.eventChan <- Event{Type: EventPrefetchError, Err: err}:
			case <-e.mainCtx.Done():
			}
			return
		}
		if stream != nil {
			stream.Cancel()
			select {
			case e.eventChan <- Event{Type: EventPrefetchError, Err: errors.New("provider returned stream for prefetch")}:
			case <-e.mainCtx.Done():
			}
			return
		}
		select {
		case e.eventChan <- Event{Type: EventPrefetchReady, Response: result}:
		case <-e.mainCtx.Done():
		}
	}()
}

func (e *Engine) handlePrefetchReady(resp *types.CompletionResponse) {
	e.prefetchedResponse = &prefetchedCompletion{CompletionResponse: resp}
	previousPrefetchState := e.prefetchState
	e.prefetchState = prefetchReady

	if previousPrefetchState == prefetchWaitingForTab {
		e.handleDeferredCursorTarget()
		return
	}

	if previousPrefetchState == prefetchWaitingForCursorPrediction {
		if e.state == stateHasCompletion || e.state == stateStreamingCompletion {
			return
		}
		// Don't replace a staged completion's cursor target — it points to the
		// next stage the user needs to accept. The prefetch result stays stored
		// and will be consumed after all stages are finished.
		if e.state == stateHasCursorTarget && e.hasMoreStages() {
			return
		}
		e.handlePrefetchCursorPrediction()
	}
}

func (e *Engine) handlePrefetchCursorPrediction() {
	if e.prefetchedResponse == nil || e.prefetchedResponse.Completion == nil {
		return
	}

	comp := e.prefetchedResponse.Completion

	bufferLines := e.buffer.Lines()
	var oldLines []string
	for i := comp.StartLine; i <= comp.EndLineInc && i-1 < len(bufferLines); i++ {
		oldLines = append(oldLines, bufferLines[i-1])
	}

	targetLine := text.FindFirstChangedLine(oldLines, comp.Lines, comp.StartLine-1)
	if targetLine <= 0 {
		return
	}

	distance := utils.Abs(targetLine - e.buffer.Row())
	if distance <= e.config.CursorPrediction.ProximityThreshold {
		e.tryShowPrefetchedCompletion()
	} else {
		e.showCursorTargetWithCandidate(&types.CursorPredictionTarget{
			LineNumber:      int32(targetLine),
			ShouldRetrigger: false,
		}, e.rejectedCompletionFor(comp))
	}
}

func (e *Engine) tryShowPrefetchedCompletion() bool {
	return e.tryShowPrefetchedCompletionWithManual(false)
}

func (e *Engine) tryShowPrefetchedCompletionWithManual(manual bool) bool {
	if e.prefetchedResponse == nil || e.prefetchedResponse.Completion == nil {
		return false
	}

	e.syncBuffer()

	resp := e.prefetchedResponse
	e.clearPrefetchResult()
	return e.processCompletionWithManual(resp.CompletionResponse, resp.Manual || manual) == completionShown
}

func (e *Engine) handlePrefetchError(err error) {
	if err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("prefetch error: %v", err)
	}

	previousPrefetchState := e.prefetchState
	e.prefetchState = prefetchNone

	if previousPrefetchState == prefetchWaitingForTab {
		e.handleDeferredCursorTarget()
	}
}

func (e *Engine) handleDeferredCursorTarget() {
	if e.cursorTarget == nil {
		return
	}

	if e.prefetchedResponse != nil && e.prefetchedResponse.Completion != nil {
		e.syncBuffer()

		resp := e.prefetchedResponse
		e.clearPrefetchResult()

		if e.processCompletionWithManual(resp.CompletionResponse, resp.Manual) == completionShown {
			return
		}

		e.handleCursorTarget()
		return
	}

	if e.cursorTarget.ShouldRetrigger {
		e.requestCompletion(types.CompletionSourceTyping, false)
		e.state = stateIdle
		e.cursorTarget = nil
		return
	}

	e.state = stateIdle
	e.cursorTarget = nil
}

func (e *Engine) prefetchAtNMinusOne() {
	if !e.canPrefetchWithSyntheticCurrent() {
		return
	}
	if e.stagedCompletion == nil {
		return
	}

	if e.stagedCompletion.CurrentIdx != len(e.stagedCompletion.Stages)-1 {
		return
	}

	stage := e.getStage(len(e.stagedCompletion.Stages) - 1)
	if stage == nil || stage.CursorTarget == nil || !stage.CursorTarget.ShouldRetrigger {
		return
	}

	lines := applyStageToLines(slices.Clone(e.buffer.Lines()), stage)

	overrideRow := max(1, int(stage.CursorTarget.LineNumber))

	e.requestPrefetch(overrideRow, 0, prefetchOpts{Lines: lines})
	e.prefetchState = prefetchWaitingForCursorPrediction
}

func (e *Engine) prefetchAtCursorTarget() {
	if !e.canPrefetchWithSyntheticCurrent() {
		return
	}
	if e.cursorTarget == nil || !e.cursorTarget.ShouldRetrigger {
		return
	}

	if e.prefetchState != prefetchNone {
		return
	}

	overrideRow := max(1, int(e.cursorTarget.LineNumber))
	e.requestPrefetch(overrideRow, 0, prefetchOpts{})
	e.prefetchState = prefetchWaitingForCursorPrediction
}

func (e *Engine) canPrefetchWithSyntheticCurrent() bool {
	return e.provider.CompletionKind() == CompletionEdit &&
		e.provider.CompletionInputAuthority() == InputSuppliedCurrent
}
