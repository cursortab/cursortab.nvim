package engine

import (
	"context"
	"errors"

	"cursortab/ctx"
	"cursortab/logger"
	"cursortab/text"
	"cursortab/types"
	"cursortab/utils"
)

// gatherContext delegates to the context gatherer if configured.
func (e *Engine) gatherContext(filePath string) *types.ContextResult {
	if e.contextGatherer == nil {
		return nil
	}
	return e.contextGatherer.Gather(e.mainCtx, &ctx.SourceRequest{
		FilePath:          filePath,
		CursorRow:         e.buffer.Row(),
		CursorCol:         e.buffer.Col(),
		WorkspacePath:     e.WorkspacePath,
		MaxDiffBytes:      e.contextLimits.MaxDiffBytes,
		MaxChangedSymbols: e.contextLimits.MaxChangedSymbols,
		MaxSiblings:       e.contextLimits.MaxSiblings,
	})
}

// requestCompletion initiates a completion request.
func (e *Engine) requestCompletion(source types.CompletionSource) {
	if e.stopped {
		return
	}

	e.syncBuffer()

	req := &types.CompletionRequest{
		Source:                source,
		WorkspacePath:         e.WorkspacePath,
		WorkspaceID:           e.WorkspaceID,
		FilePath:              e.buffer.Path(),
		Lines:                 e.buffer.Lines(),
		Version:               e.buffer.Version(),
		PreviousLines:         e.buffer.PreviousLines(),
		FileDiffHistories:     e.getAllFileDiffHistories(),
		CursorRow:             e.buffer.Row(),
		CursorCol:             e.buffer.Col(),
		ViewportHeight:        e.getViewportHeightConstraint(),
		MaxVisibleLines:       e.config.MaxVisibleLines,
		AdditionalContext:     e.gatherContext(e.buffer.Path()),
		RecentBufferSnapshots: e.getRecentBufferSnapshots(e.buffer.Path(), e.contextLimits.MaxRecentSnapshots),
		UserActions:           e.getUserActionsForFile(e.buffer.Path()),
	}

	// Check if provider supports streaming
	if streamProvider, ok := e.provider.(LineStreamProvider); ok {
		switch streamProvider.GetStreamingType() {
		case StreamingTypeLines:
			e.requestStreamingCompletion(streamProvider, req)
			return
		case StreamingTypeTokens:
			if tokenProvider, ok := e.provider.(TokenStreamProvider); ok {
				e.requestTokenStreamingCompletion(tokenProvider, req)
				return
			}
		}
	}

	// Fallback to batch mode
	e.state = statePendingCompletion

	ctx, cancel := context.WithTimeout(e.mainCtx, e.config.CompletionTimeout)
	e.currentCancel = cancel

	go func() {
		defer cancel()

		result, err := e.provider.GetCompletion(ctx, req)

		if err != nil {
			select {
			case e.eventChan <- Event{Type: EventCompletionError, Data: err}:
			case <-e.mainCtx.Done():
			}
			return
		}

		select {
		case e.eventChan <- Event{Type: EventCompletionReady, Data: result}:
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
		if constraint := viewportBottom - e.buffer.Row(); constraint > 0 {
			return constraint
		}
	}
	return 0
}

// requestPrefetch requests a completion for a specific cursor position without changing the engine state.
// Used to speculatively request completions ahead of user actions.
func (e *Engine) requestPrefetch(source types.CompletionSource, overrideRow int, overrideCol int) {
	if e.stopped {
		return
	}

	// Cancel existing prefetch if any
	if e.prefetchCancel != nil {
		e.prefetchCancel()
		e.prefetchCancel = nil
		e.prefetchState = prefetchNone
	}

	// Sync buffer to ensure latest context
	e.syncBuffer()

	ctx, cancel := context.WithTimeout(e.mainCtx, e.config.CompletionTimeout)
	e.prefetchCancel = cancel
	e.prefetchState = prefetchInFlight

	// Snapshot required values to avoid races with buffer mutation
	lines := append([]string{}, e.buffer.Lines()...)
	previousLines := append([]string{}, e.buffer.PreviousLines()...)
	version := e.buffer.Version()
	filePath := e.buffer.Path()
	viewportHeight := e.getViewportHeightConstraint()

	go func() {
		defer cancel()

		result, err := e.provider.GetCompletion(ctx, &types.CompletionRequest{
			Source:            source,
			WorkspacePath:     e.WorkspacePath,
			WorkspaceID:       e.WorkspaceID,
			FilePath:          filePath,
			Lines:             lines,
			Version:           version,
			PreviousLines:     previousLines,
			FileDiffHistories: e.getAllFileDiffHistories(),
			CursorRow:         overrideRow,
			CursorCol:         overrideCol,
			ViewportHeight:    viewportHeight,
			MaxVisibleLines:   e.config.MaxVisibleLines,
			AdditionalContext: e.gatherContext(filePath),
		})

		if err != nil {
			select {
			case e.eventChan <- Event{Type: EventPrefetchError, Data: err}:
			case <-e.mainCtx.Done():
			}
			return
		}

		select {
		case e.eventChan <- Event{Type: EventPrefetchReady, Data: result}:
		case <-e.mainCtx.Done():
		}
	}()
}

// handlePrefetchReady processes a successful prefetch response
func (e *Engine) handlePrefetchReady(resp *types.CompletionResponse) {
	e.prefetchedCompletions = resp.Completions
	e.prefetchedCursorTarget = resp.CursorTarget
	previousPrefetchState := e.prefetchState
	e.prefetchState = prefetchReady

	// If we were waiting for prefetch due to tab press, continue with cursor target logic
	if previousPrefetchState == prefetchWaitingForTab {
		e.handleDeferredCursorTarget()
		return
	}

	// If we were waiting for prefetch to show cursor prediction,
	// but only if we're not currently showing a completion to the user
	if previousPrefetchState == prefetchWaitingForCursorPrediction {
		// Don't interrupt an active completion - let user accept/reject it first
		if e.state == stateHasCompletion || e.state == stateStreamingCompletion {
			return
		}
		e.handlePrefetchCursorPrediction()
	}
}

// handlePrefetchCursorPrediction checks if prefetch should be shown immediately or as cursor target.
// If the first changed line is close to the cursor, shows completion directly.
// Otherwise, shows a cursor prediction indicator pointing to the target line.
func (e *Engine) handlePrefetchCursorPrediction() {
	if len(e.prefetchedCompletions) == 0 {
		return
	}

	comp := e.prefetchedCompletions[0]

	// Extract old lines for diff analysis
	bufferLines := e.buffer.Lines()
	var oldLines []string
	for i := comp.StartLine; i <= comp.EndLineInc && i-1 < len(bufferLines); i++ {
		oldLines = append(oldLines, bufferLines[i-1])
	}

	// Find first changed line
	targetLine := text.FindFirstChangedLine(oldLines, comp.Lines, comp.StartLine-1)
	if targetLine <= 0 {
		return
	}

	// Check distance to determine if show completion or cursor prediction
	distance := utils.Abs(targetLine - e.buffer.Row())
	if distance <= e.config.CursorPrediction.ProximityThreshold {
		e.tryShowPrefetchedCompletion()
	} else {
		// Show cursor prediction to the target line
		e.cursorTarget = &types.CursorPredictionTarget{
			RelativePath:    e.buffer.Path(),
			LineNumber:      int32(targetLine),
			ShouldRetrigger: false,
		}
		e.state = stateHasCursorTarget
		e.buffer.ShowCursorTarget(targetLine)
	}
}

// tryShowPrefetchedCompletion attempts to show prefetched completion immediately.
func (e *Engine) tryShowPrefetchedCompletion() bool {
	if len(e.prefetchedCompletions) == 0 {
		return false
	}

	e.syncBuffer()

	comp := e.prefetchedCompletions[0]

	e.prefetchedCompletions = nil
	e.prefetchedCursorTarget = nil
	e.prefetchState = prefetchNone

	return e.processCompletion(comp)
}

// handlePrefetchError processes a prefetch error
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

// handleDeferredCursorTarget handles cursor target logic that was deferred due to prefetch in progress.
// Called when prefetch completes and user had pressed Tab while waiting.
func (e *Engine) handleDeferredCursorTarget() {
	if e.cursorTarget == nil {
		return
	}

	// Check if we now have prefetched completions
	if len(e.prefetchedCompletions) > 0 {
		// Sync buffer to get updated cursor position
		e.syncBuffer()

		comp := e.prefetchedCompletions[0]

		// Clear prefetch state before processing
		e.prefetchedCompletions = nil
		e.prefetchedCursorTarget = nil
		e.prefetchState = prefetchNone

		if e.processCompletion(comp) {
			return
		}

		// No changes in prefetched completion
		e.handleCursorTarget()
		return
	}

	// Fall back to original behavior - trigger new completion if needed
	if e.cursorTarget.ShouldRetrigger {
		e.requestCompletion(types.CompletionSourceTyping)
		e.state = stateIdle
		e.cursorTarget = nil
		return
	}

	e.state = stateIdle
	e.cursorTarget = nil
}

// prefetchAtNMinusOne triggers prefetch when at the second-to-last stage.
// This allows the next completion to be ready when user accepts the last stage.
func (e *Engine) prefetchAtNMinusOne() {
	if e.stagedCompletion == nil {
		return
	}

	// Check if we're at n-1 (one stage remaining after current)
	if e.stagedCompletion.CurrentIdx != len(e.stagedCompletion.Stages)-1 {
		return
	}

	lastStage := e.getStage(len(e.stagedCompletion.Stages) - 1)
	if lastStage == nil || lastStage.CursorTarget == nil || !lastStage.CursorTarget.ShouldRetrigger {
		return
	}

	overrideRow := max(1, lastStage.BufferStart)
	e.requestPrefetch(types.CompletionSourceTyping, overrideRow, 0)
	e.prefetchState = prefetchWaitingForCursorPrediction
}

// prefetchAtCursorTarget triggers prefetch after accepting to cursor target position.
// This speculatively requests the next completion at the target location.
func (e *Engine) prefetchAtCursorTarget() {
	if e.cursorTarget == nil || !e.cursorTarget.ShouldRetrigger {
		return
	}

	// Skip if prefetch already in flight
	if e.prefetchState != prefetchNone {
		return
	}

	overrideRow := max(1, int(e.cursorTarget.LineNumber))
	e.requestPrefetch(types.CompletionSourceTyping, overrideRow, 0)
	e.prefetchState = prefetchWaitingForCursorPrediction
}
