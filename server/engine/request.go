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

func (e *Engine) prepareCompletionInput(parent context.Context, opts completionInputOptions) (ctx.CompletionInput, bool, error) {
	requirements := e.provider.RequiredMaterials()
	sourceInput := e.buildContextSourceInput(opts, requirements)
	input := ctx.CompletionInput{Current: sourceInput.Current}
	if !completionInputCompatible(e.provider.CompletionKind(), sourceInput.Current) {
		return input, false, nil
	}
	collected, err := e.collectCompletionInput(parent, sourceInput, requirements)
	if err != nil {
		return input, false, err
	}
	return collected, true, nil
}

func (e *Engine) startProviderCompletion(reqCtx context.Context, input ctx.CompletionInput) (*types.CompletionResponse, CompletionStream, error) {
	if streamingProvider, ok := e.provider.(StreamingProvider); ok {
		stream, err := streamingProvider.StreamCompletion(reqCtx, input)
		if err != nil {
			return nil, nil, err
		}
		if stream != nil {
			return nil, stream, nil
		}
	}

	result, err := e.provider.Complete(reqCtx, input)
	if err != nil {
		return nil, nil, err
	}
	return result, nil, nil
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

	e.completionRequestID++
	requestID := e.completionRequestID
	input, compatible, err := e.prepareCompletionInput(e.mainCtx, completionInputOptions{})
	if err != nil {
		select {
		case e.eventChan <- Event{Type: EventCompletionError, RequestID: requestID, Err: err}:
		case <-e.mainCtx.Done():
		}
		return
	}
	if !compatible {
		e.state = statePendingCompletion
		select {
		case e.eventChan <- Event{Type: EventCompletionReady, RequestID: requestID, Manual: manual, Response: &types.CompletionResponse{}}:
		case <-e.mainCtx.Done():
		}
		return
	}

	e.state = statePendingCompletion
	reqCtx, cancel := context.WithTimeout(e.mainCtx, e.config.CompletionTimeout)
	e.currentCancel = cancel
	go func() {
		result, stream, err := e.startProviderCompletion(reqCtx, input)
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

func (e *Engine) requestPrefetch(overrideRow, overrideCol int, opts prefetchOpts, wait prefetchWait) {
	if e.stopped {
		return
	}

	if e.suppressForNoEdits() {
		logger.Debug("prefetch suppressed: no recent edits")
		return
	}

	e.cancelPrefetch()

	e.syncBuffer()
	e.prefetchRequestID++
	requestID := e.prefetchRequestID
	e.prefetch = prefetchSlot{
		inflight: &prefetchInflight{requestID: requestID, wait: wait},
	}

	// Build the frozen request input before the goroutine starts so it cannot
	// race with later buffer or file-state mutations.
	input, compatible, err := e.prepareCompletionInput(e.mainCtx, completionInputOptions{
		lines:             opts.Lines,
		cursorRow:         overrideRow,
		cursorCol:         overrideCol,
		hasCursorOverride: true,
	})
	if err != nil {
		select {
		case e.eventChan <- Event{Type: EventPrefetchError, RequestID: requestID, Err: err}:
		case <-e.mainCtx.Done():
		}
		return
	}
	if !compatible {
		e.clearPrefetch()
		return
	}

	reqCtx, cancel := context.WithTimeout(e.mainCtx, e.config.CompletionTimeout)
	if e.prefetch.inflight != nil && e.prefetch.inflight.requestID == requestID {
		e.prefetch.inflight.cancel = cancel
	}
	go func() {
		defer cancel()
		result, err := e.provider.Complete(reqCtx, input)
		if err != nil {
			select {
			case e.eventChan <- Event{Type: EventPrefetchError, RequestID: requestID, Err: err}:
			case <-e.mainCtx.Done():
			}
			return
		}
		select {
		case e.eventChan <- Event{Type: EventPrefetchReady, RequestID: requestID, Response: result}:
		case <-e.mainCtx.Done():
		}
	}()
}

func (e *Engine) handlePrefetchReady(resp *types.CompletionResponse) {
	if !e.hasInflightPrefetch() {
		return
	}
	wait := e.inflightPrefetchWait()
	e.storeReadyPrefetch(resp, false)

	if wait == prefetchAfterTab {
		e.handleDeferredCursorTarget()
		return
	}

	if wait == prefetchForCursorPrediction {
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
	comp := e.readyPrefetchCompletion()
	if comp == nil {
		return
	}

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
	resp := e.readyPrefetch()
	if resp == nil || resp.Completion == nil {
		return false
	}

	e.syncBuffer()

	e.clearPrefetch()
	return e.processCompletionWithManual(resp.CompletionResponse, resp.Manual || manual) == completionShown
}

func (e *Engine) handlePrefetchError(err error) {
	if err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("prefetch error: %v", err)
	}

	if !e.hasInflightPrefetch() {
		return
	}
	wait := e.inflightPrefetchWait()
	e.clearPrefetch()

	if wait == prefetchAfterTab {
		e.handleDeferredCursorTarget()
	}
}

func (e *Engine) handleDeferredCursorTarget() {
	if e.cursorTarget == nil {
		return
	}

	if resp := e.readyPrefetch(); resp != nil && resp.Completion != nil {
		e.syncBuffer()

		e.clearPrefetch()

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

	e.requestPrefetch(overrideRow, 0, prefetchOpts{Lines: lines}, prefetchForCursorPrediction)
}

func (e *Engine) prefetchAtCursorTarget() {
	if !e.canPrefetchWithSyntheticCurrent() {
		return
	}
	if e.cursorTarget == nil || !e.cursorTarget.ShouldRetrigger {
		return
	}

	if e.hasInflightPrefetch() || e.readyPrefetch() != nil {
		return
	}

	overrideRow := max(1, int(e.cursorTarget.LineNumber))
	e.requestPrefetch(overrideRow, 0, prefetchOpts{}, prefetchForCursorPrediction)
}

func (e *Engine) canPrefetchWithSyntheticCurrent() bool {
	return e.provider.CompletionKind() == CompletionEdit &&
		e.provider.CanPrefetchFromSyntheticCurrent()
}
