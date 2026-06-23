package engine

import (
	"cursortab/text"
	"cursortab/types"
	"cursortab/utils"
)

func (e *Engine) startStreamingCompletion(stream CompletionStream) {
	e.state = stateStreamingCompletion

	viewportTop, viewportBottom := e.buffer.ViewportBounds()
	windowStart, oldLines := stream.Window()

	e.streamingState = &StreamingState{
		StageBuilder: text.NewIncrementalStageBuilder(
			oldLines,
			windowStart+1, // baseLineOffset (1-indexed)
			e.config.CursorPrediction.ProximityThreshold,
			e.config.MaxVisibleLines,
			viewportTop,
			viewportBottom,
			e.buffer.Row(),
			e.buffer.Col(),
			e.buffer.Path(),
			e.buffer.AvailableWidth(),
		),
	}
	e.completionStream = stream
	e.streamLinesChan = stream.Lines()
}

// cancelStreaming cancels an in-progress streaming completion.
func (e *Engine) cancelStreaming() {
	// Clear channels first - this immediately stops event loop from reading
	e.streamLinesChan = nil
	if e.completionStream != nil {
		e.completionStream.Cancel()
		e.completionStream = nil
	}
	e.streamingState = nil
	e.acceptedDuringStreaming = false
}

// cancelLineStreamingKeepPartial cancels line streaming but preserves the partial
// completion state (completions and completionOriginalLines) for typing match validation.
// Used when user types during line streaming after first stage was rendered.
func (e *Engine) cancelLineStreamingKeepPartial() {
	// Clear channel first - stops event loop from reading
	e.streamLinesChan = nil
	if e.completionStream != nil {
		e.completionStream.Cancel()
		e.completionStream = nil
	}
	// Clear streaming state but keep completions and completionOriginalLines
	// These were populated by renderStreamedStage and are needed for checkTypingMatchesPrediction
	e.streamingState = nil
}

// handleStreamLine processes a line received from the streaming provider.
// Caller must verify stream ID matches before calling.
func (e *Engine) handleStreamLine(line string) {
	ss := e.streamingState
	if ss == nil {
		return
	}

	// If user accepted during streaming, skip stage building (diffs would be wrong).
	// The provider-owned stream still keeps enough text to finish the response.
	if e.acceptedDuringStreaming {
		return
	}

	// Process pending line through stage builder (if any)
	if ss.HasPendingLine {
		finalized := ss.StageBuilder.AddLine(ss.PendingLine)
		if finalized != nil && !ss.FirstStageRendered {
			// Check if this stage is close enough to render immediately
			viewportTop, viewportBottom := e.buffer.ViewportBounds()
			needsNav := text.StageNeedsNavigation(
				finalized,
				e.buffer.Row(),
				viewportTop, viewportBottom,
				e.config.CursorPrediction.ProximityThreshold,
			)
			if !needsNav {
				// Stage is close to cursor - render it immediately
				if e.renderStreamedStage(finalized) {
					e.recordMetricsShown(nil)
					ss.FirstStageRendered = true
				}
			}
			// If needsNav, don't render - let Finalize() handle it with cursor prediction
		}
	}

	// Buffer current line (will be processed on next line or completion).
	ss.PendingLine = line
	ss.HasPendingLine = true
}

// handleStreamCompleteSimple processes stream completion when lines channel closes.
// Called directly from event loop.
func (e *Engine) handleStreamCompleteSimple() {
	// Clear stream channel first
	e.streamLinesChan = nil

	if e.streamingState == nil {
		return
	}

	ss := e.streamingState

	// Handle case where user accepted during streaming
	// We need to recompute diff from accumulated text against current buffer
	if e.acceptedDuringStreaming {
		e.acceptedDuringStreaming = false
		if e.completionStream != nil {
			resp, err := e.completionStream.Finish()
			if err == nil {
				e.handleStreamCompleteAfterAccept(resp)
			}
			e.completionStream = nil
		}
		e.streamingState = nil
		return
	}

	firstStageRendered := ss.FirstStageRendered

	// Process pending line if not truncated.
	if ss.HasPendingLine {
		ss.StageBuilder.AddLine(ss.PendingLine)
		ss.HasPendingLine = false
	}

	if e.completionStream != nil {
		_, _ = e.completionStream.Finish()
	}

	// Finalize remaining stages
	stagingResult := ss.StageBuilder.Finalize()

	// Clear streaming state
	e.streamingState = nil
	e.completionStream = nil

	if stagingResult == nil || len(stagingResult.Stages) == 0 {
		e.state = stateIdle
		return
	}

	e.stagedCompletion = &text.StagedCompletion{
		Stages:     stagingResult.Stages,
		CurrentIdx: 0,
	}

	// If the first stage matches a recent rejection, drop everything.
	// Important: if we already rendered a stage during streaming, ghost text
	// and applyBatch are live; a plain state flip would leave them visible
	// with no (idle, accept) transition, so Tab would do nothing. Route
	// through reject() so ClearUI and completion/applyBatch cleanup happen.
	firstStage := stagingResult.Stages[0]
	if e.suppressRejectedCompletionForStage(firstStage) {
		e.reject()
		return
	}

	// If we already rendered a stage during streaming, keep it as-is.
	// Re-rendering would cause visible flicker since Finalize() diffs against
	// full old lines (vs partial during streaming), producing different groups.
	// The accept path reconciles any boundary mismatch (accept.go:54-63).
	if firstStageRendered {
		e.state = stateHasCompletion
		return
	}

	// Clear any UI (nothing was rendered during streaming)
	e.buffer.ClearUI()

	// Transition to appropriate state
	if stagingResult.FirstNeedsNavigation {
		e.showStageCursorTarget(stagingResult.Stages[0])
	} else {
		e.showCurrentStage()
	}
}

// handleStreamCompleteAfterAccept recomputes diff from the final streamed
// response against the buffer state after the already-accepted partial stage.
func (e *Engine) handleStreamCompleteAfterAccept(resp *types.CompletionResponse) {
	if resp == nil || len(resp.Completions) == 0 {
		return
	}

	// Sync buffer to get current state after accept
	e.syncBuffer()

	// Get first completion and compute diff against current buffer
	comp := resp.Completions[0]
	bufferLines := e.buffer.Lines()

	// Extract old lines (current buffer content in the completion range)
	var oldLines []string
	endLine := max(comp.EndLineInc, comp.StartLine+len(comp.Lines)-1)
	for i := comp.StartLine; i <= endLine && i-1 < len(bufferLines); i++ {
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
		// Close enough - show completion
		e.prefetchedCompletions = resp.Completions
		e.prefetchedCursorTarget = resp.CursorTarget
		e.prefetchState = prefetchReady
		e.tryShowPrefetchedCompletion()
	} else {
		// Far away - show cursor prediction
		e.showCursorTargetWithCandidate(&types.CursorPredictionTarget{
			RelativePath:    e.buffer.Path(),
			LineNumber:      int32(targetLine),
			ShouldRetrigger: false,
		}, e.rejectedCompletionFor(comp))
		// Store the completions for when user jumps to target
		e.prefetchedCompletions = resp.Completions
		e.prefetchedCursorTarget = resp.CursorTarget
		e.prefetchState = prefetchReady
	}
}

// renderStreamedStage renders a finalized stage during streaming.
// Returns true only when the stage was actually rendered.
func (e *Engine) renderStreamedStage(stage *text.Stage) bool {
	if stage == nil || len(stage.Groups) == 0 {
		return false
	}

	// Suppress before rendering so cached rejections do not flash during
	// streaming and then disappear when the full stream finalizes.
	if e.suppressRejectedCompletionForStage(stage) {
		e.cancelStreaming()
		e.reject()
		return false
	}

	// Prepare completion for this stage and render it
	e.applyBatch = e.buffer.PrepareCompletion(
		stage.BufferStart,
		stage.BufferEnd,
		stage.Lines,
		stage.Groups,
	)

	// Store for partial typing optimization
	bufferLines := e.buffer.Lines()
	e.completionOriginalLines = nil
	for i := stage.BufferStart; i <= stage.BufferEnd && i-1 < len(bufferLines); i++ {
		e.completionOriginalLines = append(e.completionOriginalLines, bufferLines[i-1])
	}

	e.completions = []*types.Completion{{
		StartLine:  stage.BufferStart,
		EndLineInc: stage.BufferEnd,
		Lines:      stage.Lines,
	}}
	e.cursorTarget = stage.CursorTarget
	e.currentRejectedCompletion = e.currentRejectedCompletionCandidate()

	// Store groups for partial accept
	e.currentGroups = stage.Groups

	return true
}
