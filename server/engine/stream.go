package engine

import (
	"slices"

	"cursortab/logger"
	"cursortab/text"
	"cursortab/types"
	"cursortab/utils"
)

func (e *Engine) startCompletionStream(stream CompletionStream, manual bool) {
	e.state = stateStreamingCompletion

	viewportTop, viewportBottom := e.buffer.ViewportBounds()
	windowStart, oldLines := stream.Window()
	e.stream = &activeStream{
		stream: stream,
		lines:  stream.Lines(),
		manual: manual,
		stageBuilder: text.NewIncrementalStageBuilder(
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
}

// cancelInFlight cancels the current request and stops its stream, if any. A
// stage rendered from the stream stays displayed.
func (e *Engine) cancelInFlight() {
	if e.stream != nil {
		e.stream.stream.Cancel()
		e.stream = nil
	}
	e.cancelCurrentRequest()
}

// handleStreamEvent handles one received line, or the end of the stream when
// ok is false.
func (e *Engine) handleStreamEvent(line string, ok bool) {
	e.process(func() {
		if ok {
			e.handleStreamLine(line)
		} else {
			e.handleStreamComplete()
		}
	})
}

func (e *Engine) handleStreamLine(line string) {
	s := e.stream
	if s == nil || s.acceptedDuring {
		return
	}

	if s.hasPendingLine {
		finalized := s.stageBuilder.AddLine(s.pendingLine)
		if finalized != nil && !s.firstStageRendered {
			viewportTop, viewportBottom := e.buffer.ViewportBounds()
			needsNav := text.StageNeedsNavigation(
				finalized,
				e.buffer.Row(),
				viewportTop, viewportBottom,
				e.config.CursorPrediction.ProximityThreshold,
			)
			if !needsNav && e.renderStreamedStage(finalized) {
				e.recordMetricsShown(nil, s.manual)
				s.firstStageRendered = true
			}
		}
	}

	s.pendingLine = line
	s.hasPendingLine = true
}

func (e *Engine) handleStreamComplete() {
	s := e.stream
	if s == nil {
		return
	}
	e.stream = nil
	resp, err := s.stream.Finish()
	e.cancelCurrentRequest()

	if s.acceptedDuring {
		if err != nil {
			logger.Error("stream completion error after accept: %v", err)
			return
		}
		e.handleStreamCompleteAfterAccept(resp, s.manual)
		return
	}

	if err != nil {
		logger.Error("stream completion error: %v", err)
		e.reject()
		return
	}

	if s.hasPendingLine {
		s.stageBuilder.AddLine(s.pendingLine)
	}
	stagingResult := s.stageBuilder.Finalize()

	if stagingResult == nil || len(stagingResult.Stages) == 0 {
		// A stream may emit no preview lines and deliver its completion only
		// via Finish (zeta-2.1: the rewritten span is chosen by the trailing
		// marker's number, so nothing can be previewed mid-stream). Adopt the
		// parsed batch result instead of dropping it.
		e.state = stateIdle
		if resp != nil {
			e.handleCompletionReadyImpl(resp, s.manual)
		}
		return
	}

	if resp != nil && resp.CursorTarget != nil {
		stagingResult.Stages[len(stagingResult.Stages)-1].CursorTarget = resp.CursorTarget
	}
	e.stagedCompletion = &text.StagedCompletion{
		Stages:     stagingResult.Stages,
		CurrentIdx: 0,
		Manual:     s.manual,
	}

	// If the first stage matches a recent rejection, drop everything. A stage
	// rendered during streaming is live, so route through reject() to clear
	// the UI and the display batch.
	firstStage := stagingResult.Stages[0]
	if e.suppressRejectedCompletionForStage(firstStage, s.manual) {
		e.reject()
		return
	}

	// Keep a stage rendered during streaming when it makes the same edit as
	// the first final stage; re-rendering it would only flicker, as groups
	// may differ. Otherwise the final stages replace it: accepting a stage
	// that is not Stages[0] would apply the wrong edit.
	if s.firstStageRendered && rendersStage(e.display.current(), firstStage) {
		e.cursorTarget = firstStage.CursorTarget
		e.state = stateHasCompletion
		return
	}

	e.buffer.ClearUI()
	if stagingResult.FirstNeedsNavigation {
		// The streamed stage is gone from the screen; its metrics carry over
		// to the stage shown after the jump.
		e.display.reset()
		e.showStageCursorTarget(firstStage)
	} else {
		e.showCurrentStage()
	}
}

func (e *Engine) handleStreamCompleteAfterAccept(resp *types.CompletionResponse, manual bool) {
	if resp == nil {
		return
	}
	if resp.Completion == nil {
		if resp.CursorTarget != nil {
			e.cursorTarget = resp.CursorTarget
			e.handleCursorTarget()
		}
		return
	}

	e.syncBuffer()

	comp := resp.Completion
	bufferLines := e.buffer.Lines()

	var oldLines []string
	endLine := max(comp.EndLineInc, comp.StartLine+len(comp.Lines)-1)
	for i := comp.StartLine; i <= endLine && i-1 < len(bufferLines); i++ {
		oldLines = append(oldLines, bufferLines[i-1])
	}

	targetLine := text.FindFirstChangedLine(oldLines, comp.Lines, comp.StartLine-1)
	if targetLine <= 0 {
		if resp.CursorTarget != nil {
			e.cursorTarget = resp.CursorTarget
			e.handleCursorTarget()
		}
		return
	}

	distance := utils.Abs(targetLine - e.buffer.Row())

	if distance <= e.config.CursorPrediction.ProximityThreshold {
		e.storeReadyPrefetch(resp, manual)
		e.tryShowPrefetchedCompletionWithManual(manual)
	} else {
		e.showCursorTargetWithCandidate(&types.CursorPredictionTarget{
			LineNumber:      int32(targetLine),
			ShouldRetrigger: false,
		}, e.rejectedCompletionFor(comp))
		e.storeReadyPrefetch(resp, manual)
	}
}

func (e *Engine) renderStreamedStage(stage *text.Stage) bool {
	s := e.stream
	if s == nil || stage == nil || len(stage.Groups) == 0 {
		return false
	}

	// Suppress before rendering so cached rejections do not flash during
	// streaming and then disappear when the full stream finalizes.
	if e.suppressRejectedCompletionForStage(stage, s.manual) {
		e.cancelInFlight()
		e.reject()
		return false
	}

	e.cursorTarget = stage.CursorTarget
	e.setDisplayedStage(stage, stage.Groups)

	return true
}

// rendersStage reports whether the displayed completion makes the edit of
// stage.
func rendersStage(displayed *types.Completion, stage *text.Stage) bool {
	return displayed != nil &&
		displayed.StartLine == stage.BufferStart &&
		displayed.EndLineInc == stage.BufferEnd &&
		slices.Equal(displayed.Lines, stage.Lines)
}
