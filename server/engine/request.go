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

	// Drop any leftover stream from a prior accept-during-streaming. The
	// new request supersedes its "next prediction" output; without this,
	// the leftover stream's late completion hits handleStreamCompleteAfterAccept
	// and rewrites state we're about to set up here.
	e.cancelStreaming()

	e.syncBuffer()

	// No-edit suppression: don't show completions until the file has been edited
	if !e.manuallyTriggered && e.suppressForNoEdits() {
		logger.Debug("suppressed: no recent edits")
		return
	}

	// Scope suppression: don't complete inside disabled treesitter scopes
	if scope := e.suppressForDisabledScope(); !e.manuallyTriggered && scope != "" {
		logger.Debug("suppressed: disabled treesitter scope %q", scope)
		return
	}

	// Mid-line suppression applies to all sources for insertion-only providers
	if !e.manuallyTriggered && e.suppressForMidLine() {
		logger.Debug("suppressed: mid-line cursor position")
		return
	}

	// Deletion suppression only applies to typing (idle completions after deleting are fine)
	if source == types.CompletionSourceTyping && !e.manuallyTriggered && e.suppressForSingleDeletion() {
		logger.Debug("suppressed: single deletion")
		return
	}

	e.lastCompletionSource = source

	req := &types.CompletionRequest{
		Source:                source,
		WorkspacePath:         e.WorkspacePath,
		WorkspaceID:           e.WorkspaceID,
		FilePath:              e.buffer.Path(),
		Lines:                 e.buffer.Lines(),
		Version:               e.buffer.Version(),
		PreviousLines:         e.buffer.PreviousLines(),
		OriginalLines:         e.buffer.OriginalLines(),
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
		// +1 because both cursor and viewport bottom are inclusive (cursor on
		// last visible line means 1 visible line remaining, not 0).
		if constraint := viewportBottom - e.buffer.Row() + 1; constraint > 0 {
			return constraint
		}
	}
	return 0
}

// prefetchOpts configures a prefetch request. Zero value uses buffer defaults.
type prefetchOpts struct {
	Lines []string // Override buffer lines (nil = use current buffer)
}

// requestPrefetch requests a completion for a specific cursor position without changing the engine state.
// Used to speculatively request completions ahead of user actions.
func (e *Engine) requestPrefetch(source types.CompletionSource, overrideRow, overrideCol int, opts prefetchOpts) {
	if e.stopped {
		return
	}

	if e.suppressForNoEdits() {
		logger.Debug("prefetch suppressed: no recent edits")
		return
	}

	e.cancelPrefetch()

	// Sync buffer to ensure latest context
	e.syncBuffer()

	ctx, cancel := context.WithTimeout(e.mainCtx, e.config.CompletionTimeout)
	e.prefetchCancel = cancel
	e.prefetchState = prefetchInFlight

	// Snapshot required values under the event loop's lock so the goroutine
	// doesn't race with concurrent mutations of buffer state or fileStateStore.
	lines := opts.Lines
	if lines == nil {
		lines = slices.Clone(e.buffer.Lines())
	}
	req := &types.CompletionRequest{
		Source:            source,
		WorkspacePath:     e.WorkspacePath,
		WorkspaceID:       e.WorkspaceID,
		FilePath:          e.buffer.Path(),
		Lines:             lines,
		Version:           e.buffer.Version(),
		PreviousLines:     slices.Clone(e.buffer.PreviousLines()),
		FileDiffHistories: e.getAllFileDiffHistories(),
		CursorRow:         overrideRow,
		CursorCol:         overrideCol,
		ViewportHeight:    e.getViewportHeightConstraint(),
		MaxVisibleLines:   e.config.MaxVisibleLines,
		AdditionalContext: e.gatherContext(e.buffer.Path()),
	}

	go func() {
		defer cancel()

		result, err := e.provider.GetCompletion(ctx, req)

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
		// Don't replace a staged completion's cursor target — it points to the
		// next stage the user needs to accept. The prefetch result stays stored
		// and will be consumed after all stages are finished.
		if e.state == stateHasCursorTarget && e.hasMoreStages() {
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
		e.showCursorTargetWithCandidate(&types.CursorPredictionTarget{
			RelativePath:    e.buffer.Path(),
			LineNumber:      int32(targetLine),
			ShouldRetrigger: false,
		}, e.rejectedCompletionFor(comp))
	}
}

// tryShowPrefetchedCompletion attempts to show prefetched completion immediately.
func (e *Engine) tryShowPrefetchedCompletion() bool {
	if len(e.prefetchedCompletions) == 0 {
		return false
	}

	e.syncBuffer()

	comp := e.prefetchedCompletions[0]
	e.clearPrefetchResult()
	return e.processCompletion(comp) == completionShown
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
		e.clearPrefetchResult()

		if e.processCompletion(comp) == completionShown {
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

// prefetchAtNMinusOne triggers prefetch when showing the last stage.
// Applies the last stage's edit to the buffer snapshot so the model sees the
// post-accept state, and centers the request on the cursor target position.
// Disabled for insertion-only providers (FIM/inline) which duplicate content on prefetch.
func (e *Engine) prefetchAtNMinusOne() {
	if !e.config.EditCompletionProvider {
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

	// Build a synthetic buffer with the last stage's edit applied.
	// This is what the buffer will look like after the user accepts.
	lines := applyStageToLines(slices.Clone(e.buffer.Lines()), stage)

	// The cursor target accounts for line count changes from the stage.
	overrideRow := max(1, int(stage.CursorTarget.LineNumber))

	e.requestPrefetch(types.CompletionSourceTyping, overrideRow, 0, prefetchOpts{Lines: lines})
	e.prefetchState = prefetchWaitingForCursorPrediction
}

// prefetchAtCursorTarget triggers prefetch after accepting to cursor target position.
// This speculatively requests the next completion at the target location.
// Disabled for insertion-only providers (FIM/inline) which duplicate content on prefetch.
func (e *Engine) prefetchAtCursorTarget() {
	if !e.config.EditCompletionProvider {
		return
	}
	if e.cursorTarget == nil || !e.cursorTarget.ShouldRetrigger {
		return
	}

	// Skip if prefetch already in flight
	if e.prefetchState != prefetchNone {
		return
	}

	overrideRow := max(1, int(e.cursorTarget.LineNumber))
	e.requestPrefetch(types.CompletionSourceTyping, overrideRow, 0, prefetchOpts{})
	e.prefetchState = prefetchWaitingForCursorPrediction
}
