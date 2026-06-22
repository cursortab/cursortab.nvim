package engine

import (
	"context"
	"strings"

	sourcectx "cursortab/ctx"
	"cursortab/text"
	"cursortab/types"
	"cursortab/utils"
)

// requestStreamingCompletion handles line-by-line streaming completions
func (e *Engine) requestStreamingCompletion(provider LineStreamProvider, input sourcectx.CompletionInput) {
	e.requestStreamingCompletionPrepared(input, func(ctx context.Context) (LineStream, ProviderStreamState, LineStreamConfig, error) {
		return provider.PrepareLineStream(ctx, input)
	})
}

func (e *Engine) requestStreamingCompletionPrepared(
	input sourcectx.CompletionInput,
	prepare func(context.Context) (LineStream, ProviderStreamState, LineStreamConfig, error),
) {
	e.state = stateStreamingCompletion

	ctx, cancel := context.WithTimeout(e.mainCtx, e.config.CompletionTimeout)
	e.streamingCancel = cancel

	// Prepare the stream
	stream, providerCtx, streamConfig, err := prepare(ctx)
	if err != nil {
		cancel()
		e.state = stateIdle
		return
	}

	viewportTop, viewportBottom := e.buffer.ViewportBounds()

	// Initialize streaming state
	e.streamingState = &StreamingState{
		StageBuilder: text.NewIncrementalStageBuilder(
			streamConfig.OldLines,
			streamConfig.WindowStart+1, // baseLineOffset (1-indexed)
			e.config.CursorPrediction.ProximityThreshold,
			e.config.MaxVisibleLines,
			viewportTop,
			viewportBottom,
			e.buffer.Row(),
			e.buffer.Col(),
			input.Current.File.Path,
			e.buffer.AvailableWidth(),
		),
		ProviderState: providerCtx,
	}

	// Inject prefill lines through handleStreamLine so the stage
	// builder, accumulated text, and validation all see a complete line sequence
	// starting from the top of the window.
	if prefill := streamConfig.Prefill; prefill != "" {
		for _, line := range strings.Split(strings.TrimSuffix(prefill, "\n"), "\n") {
			e.handleStreamLine(line)
		}
	}

	// Set stream channel directly - event loop will select on it
	e.streamLinesChan = stream.LinesChan()
}

// cancelStreaming cancels an in-progress streaming completion.
func (e *Engine) cancelStreaming() {
	// Clear channels first - this immediately stops event loop from reading
	e.streamLinesChan = nil
	// Then cancel the HTTP request
	if e.streamingCancel != nil {
		e.streamingCancel()
		e.streamingCancel = nil
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
	// Cancel the HTTP request
	if e.streamingCancel != nil {
		e.streamingCancel()
		e.streamingCancel = nil
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

	var skip bool
	line, skip = ss.ProviderState.TransformLine(line)
	if skip {
		return
	}

	// Accumulate text for provider parsing on stream completion.
	ss.AccumulatedText.WriteString(line)
	ss.AccumulatedText.WriteString("\n")

	// First line validation
	if !ss.Validated {
		if sp, ok := e.provider.(LineStreamProvider); ok {
			if err := sp.ValidateFirstLine(ss.ProviderState, line); err != nil {
				e.cancelStreaming()
				e.state = stateIdle
				return
			}
		}
		ss.Validated = true
	}

	// If user accepted during streaming, skip stage building (diffs would be wrong).
	// Just accumulate text for cursor prediction computation when streaming completes.
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
		e.handleStreamCompleteAfterAccept(ss)
		e.streamingState = nil
		e.streamingCancel = nil
		return
	}

	firstStageRendered := ss.FirstStageRendered

	// Process pending line if not truncated.
	if ss.HasPendingLine {
		ss.StageBuilder.AddLine(ss.PendingLine)
		ss.HasPendingLine = false
	}

	// Let the provider finalize/log the accumulated stream. Ordinary streaming
	// UI is finalized from the incremental stage builder below; the returned
	// response is only consumed by the after-accept path.
	sp, ok := e.provider.(LineStreamProvider)
	if ok {
		accumulatedText := ss.AccumulatedText.String()
		_, _ = sp.FinishLineStream(ss.ProviderState, accumulatedText, "stop", false)
	}

	// Finalize remaining stages
	stagingResult := ss.StageBuilder.Finalize()

	// Clear streaming state
	e.streamingState = nil
	e.streamingCancel = nil

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

// handleStreamCompleteAfterAccept handles stream completion when user accepted during streaming.
// It recomputes diff from accumulated text against current buffer and shows cursor prediction.
func (e *Engine) handleStreamCompleteAfterAccept(ss *StreamingState) {
	// Get the line stream provider to parse the accumulated text.
	sp, ok := e.provider.(LineStreamProvider)
	if !ok {
		return
	}

	accumulatedText := ss.AccumulatedText.String()
	resp, err := sp.FinishLineStream(ss.ProviderState, accumulatedText, "stop", false)
	if err != nil {
		return
	}

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
