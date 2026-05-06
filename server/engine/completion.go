package engine

import (
	"cursortab/logger"
	"cursortab/metrics"
	"cursortab/text"
	"cursortab/types"
	"cursortab/utils"
)

// handleCompletionReadyImpl processes a successful completion response.
func (e *Engine) handleCompletionReadyImpl(response *types.CompletionResponse) {
	e.syncBuffer()

	if len(response.Completions) == 0 {
		e.handleCursorTarget()
		return
	}

	completion := response.Completions[0]

	// Store metrics info for showCurrentStage to use
	e.pendingMetricsInfo = response.MetricsInfo

	switch e.processCompletion(completion) {
	case completionShown:
		return
	case completionSuppressed:
		e.pendingMetricsInfo = nil
		return
	}

	e.pendingMetricsInfo = nil
	e.handleCompletionNoChanges(completion)
}

// handleCompletionNoChanges handles the case where completion has no changes.
func (e *Engine) handleCompletionNoChanges(completion *types.Completion) {
	if e.config.CursorPrediction.AutoAdvance && e.config.CursorPrediction.Enabled {
		e.cursorTarget = &types.CursorPredictionTarget{
			LineNumber:      int32(completion.EndLineInc),
			ShouldRetrigger: true,
		}
	}
	e.handleCursorTarget()
}

// handleTextChangeImpl handles text change when we have an active completion.
// It checks if the user typed content that matches the prediction.
func (e *Engine) handleTextChangeImpl() {
	if len(e.completions) == 0 {
		e.reject()
		e.startTextChangeTimer()
		return
	}

	e.syncBuffer()

	matches, hasRemaining := e.checkTypingMatchesPrediction()
	if matches {
		if hasRemaining {
			e.processCompletion(e.completions[0])
			return
		}
		// User typed everything - completion fully typed
		e.reject()
		e.startTextChangeTimer()
		return
	}

	// Typing does not match prediction
	e.rejectAndRemember()
	e.startTextChangeTimer()
}

// checkTypingMatchesPrediction checks if the current buffer state (after user typed)
// matches the prediction, meaning the user typed content consistent with the completion.
// Returns (matches, hasRemaining) where:
// - matches: true if the current buffer is a valid prefix of the target
// - hasRemaining: true if there's still content left to predict
func (e *Engine) checkTypingMatchesPrediction() (bool, bool) {
	if len(e.completions) == 0 || len(e.completionOriginalLines) == 0 {
		return false, false
	}

	completion := e.completions[0]
	targetLines := completion.Lines
	originalLines := e.completionOriginalLines
	bufferLines := e.buffer.Lines()

	if len(targetLines) == 0 {
		return false, false
	}

	startIdx := completion.StartLine - 1
	if startIdx < 0 || startIdx >= len(bufferLines) {
		return false, false
	}

	if len(targetLines) < len(originalLines) {
		return false, false
	}

	targetEndIdx := startIdx + len(targetLines) - 1
	var currentLines []string
	for i := startIdx; i <= targetEndIdx && i < len(bufferLines); i++ {
		currentLines = append(currentLines, bufferLines[i])
	}

	if len(currentLines) == 0 {
		return false, false
	}

	madeProgress := false
	for i, currentLine := range currentLines {
		if i >= len(targetLines) {
			return false, false
		}

		targetLine := targetLines[i]

		if len(currentLine) > len(targetLine) {
			return false, false
		}
		if currentLine != targetLine[:len(currentLine)] {
			return false, false
		}

		if i < len(originalLines) {
			if currentLine != originalLines[i] && len(currentLine) > len(originalLines[i]) {
				madeProgress = true
			}
		} else if currentLine != "" {
			madeProgress = true
		}
	}

	if !madeProgress {
		return false, false
	}

	hasRemaining := len(currentLines) < len(targetLines)
	if !hasRemaining {
		for i := range currentLines {
			if i < len(targetLines) && len(currentLines[i]) < len(targetLines[i]) {
				hasRemaining = true
				break
			}
		}
	}

	return true, hasRemaining
}

// handleCursorTarget handles cursor target state transitions.
func (e *Engine) handleCursorTarget() {
	if !e.config.CursorPrediction.Enabled {
		e.clearCompletionUIOnly()
		return
	}

	if e.cursorTarget == nil || e.cursorTarget.LineNumber < 1 {
		e.clearCompletionUIOnly()
		return
	}

	distance := utils.Abs(int(e.cursorTarget.LineNumber) - e.buffer.Row())
	if distance <= e.config.CursorPrediction.ProximityThreshold {
		// Close enough - don't show cursor prediction

		// If we have remaining staged completions, check if next stage is visible and close
		if e.stagedCompletion != nil && e.stagedCompletion.CurrentIdx < len(e.stagedCompletion.Stages) {
			nextStage := e.getStage(e.stagedCompletion.CurrentIdx)
			if nextStage == nil {
				return
			}

			viewportTop, viewportBottom := e.buffer.ViewportBounds()
			needsNav := text.StageNeedsNavigation(nextStage, e.buffer.Row(), viewportTop, viewportBottom, e.config.CursorPrediction.ProximityThreshold)

			if !needsNav {
				e.showCurrentStage()
				return
			}
			e.showStageCursorTarget(nextStage)
			return
		}

		if e.prefetchState == prefetchReady && e.tryShowPrefetchedCompletion() {
			return
		}
		if e.prefetchState == prefetchInFlight {
			e.prefetchState = prefetchWaitingForCursorPrediction
		}
		e.clearCompletionUIOnly()
		return
	}

	// Far away - show cursor prediction to the target line
	e.state = stateHasCursorTarget
	e.buffer.ShowCursorTarget(int(e.cursorTarget.LineNumber))
}

// clearCompletionUIOnly clears completion state but preserves prefetch.
func (e *Engine) clearCompletionUIOnly() {
	if len(e.completions) > 0 {
		e.sendMetric(metrics.EventIgnored)
	}
	e.cancelCurrentRequest()
	e.stagedCompletion = nil
	e.resetCompletionFields()
	e.state = stateIdle
	e.cursorTarget = nil
}

func (e *Engine) showCursorTargetWithCandidate(target *types.CursorPredictionTarget, candidate *rejectedCompletion) {
	if target == nil || target.LineNumber < 1 {
		return
	}
	e.cursorTarget = target
	e.state = stateHasCursorTarget
	e.currentRejectedCompletion = candidate
	e.buffer.ShowCursorTarget(int(target.LineNumber))
}

func (e *Engine) showStageCursorTarget(stage *text.Stage) {
	if stage == nil {
		return
	}
	e.showCursorTargetWithCandidate(&types.CursorPredictionTarget{
		RelativePath:    e.buffer.Path(),
		LineNumber:      int32(stage.BufferStart),
		ShouldRetrigger: false,
	}, e.rejectedCompletionForStage(stage))
}

// showCurrentStage displays the current stage of a multi-stage completion
func (e *Engine) showCurrentStage() {
	if e.stagedCompletion == nil || e.stagedCompletion.CurrentIdx >= len(e.stagedCompletion.Stages) {
		return
	}

	stage := e.getStage(e.stagedCompletion.CurrentIdx)
	if stage == nil {
		return
	}

	e.completions = []*types.Completion{{
		StartLine:  stage.BufferStart,
		EndLineInc: stage.BufferEnd,
		Lines:      stage.Lines,
	}}
	e.cursorTarget = stage.CursorTarget
	e.state = stateHasCompletion

	e.applyBatch = e.buffer.PrepareCompletion(
		stage.BufferStart,
		stage.BufferEnd,
		stage.Lines,
		stage.Groups,
	)

	bufferLines := e.buffer.Lines()
	e.completionOriginalLines = nil
	for i := stage.BufferStart; i <= stage.BufferEnd && i-1 < len(bufferLines); i++ {
		e.completionOriginalLines = append(e.completionOriginalLines, bufferLines[i-1])
	}

	// Deep copy groups so that partial accept mutations (advanceGroupsAfterAccept)
	// don't corrupt the stage's original Groups, which advanceStagedCompletion
	// needs for correct isPureInsertion/offset calculations.
	e.currentGroups = text.CopyGroups(stage.Groups)

	e.currentRejectedCompletion = e.currentRejectedCompletionCandidate()
	e.recordMetricsShown(e.pendingMetricsInfo) // nil for streaming
	e.pendingMetricsInfo = nil
}

// getStage returns the stage at the given index, or nil if out of bounds
func (e *Engine) getStage(idx int) *text.Stage {
	if e.stagedCompletion == nil || idx < 0 || idx >= len(e.stagedCompletion.Stages) {
		return nil
	}
	return e.stagedCompletion.Stages[idx]
}

// completionOutcome describes what processCompletion decided to do with an
// incoming completion.
type completionOutcome int

const (
	// completionNoChanges means the completion matched the current buffer or
	// staging produced no visible stage. Caller should handle cursor target
	// fallback (e.g. handleCompletionNoChanges).
	completionNoChanges completionOutcome = iota
	// completionShown means the completion was rendered (or a cursor target
	// was shown) and the engine transitioned to a non-idle state.
	completionShown
	// completionSuppressed means the completion matched a recently rejected
	// entry and was intentionally dropped. Engine is now idle; caller should
	// not fall back to cursor target handling.
	completionSuppressed
)

// processCompletion is the SINGLE ENTRY POINT for processing all completions.
func (e *Engine) processCompletion(completion *types.Completion) completionOutcome {
	defer logger.Trace("engine.processCompletion")()
	if completion == nil {
		return completionNoChanges
	}

	if !e.buffer.HasChanges(completion.StartLine, completion.EndLineInc, completion.Lines) {
		return completionNoChanges
	}

	bufferLines := e.buffer.Lines()
	var originalLines []string
	endLine := completion.EndLineInc
	// Extend the old range only when buffer lines beyond EndLineInc match the
	// corresponding completion lines. This handles the case where a streaming
	// stage accept already applied part of the completion to the buffer, making
	// EndLineInc stale. Without matching, unrelated buffer lines get pulled in
	// and appear as spurious deletions.
	for i := endLine + 1; i < completion.StartLine+len(completion.Lines) && i-1 < len(bufferLines); i++ {
		compIdx := i - completion.StartLine
		if compIdx < len(completion.Lines) && bufferLines[i-1] == completion.Lines[compIdx] {
			endLine = i
		} else {
			break
		}
	}
	for i := completion.StartLine; i <= endLine && i-1 < len(bufferLines); i++ {
		originalLines = append(originalLines, bufferLines[i-1])
	}

	// Trim trailing completion lines that duplicate post-editable buffer content.
	// The model sometimes generates beyond the editable range; trim the suffix
	// of the completion that matches the buffer past endLine.
	if len(completion.Lines) > len(originalLines) {
		excess := len(completion.Lines) - len(originalLines)
		trimCount := 0
		for i := excess - 1; i >= 0; i-- {
			compIdx := len(originalLines) + i
			bufIdx := endLine + i // 0-indexed: bufferLines[endLine+i] is post-editable
			if bufIdx < len(bufferLines) && completion.Lines[compIdx] == bufferLines[bufIdx] {
				trimCount++
			} else {
				break
			}
		}
		if trimCount > 0 {
			completion.Lines = completion.Lines[:len(completion.Lines)-trimCount]
		}
	}

	viewportTop, viewportBottom := e.buffer.ViewportBounds()
	originalText := text.JoinLines(originalLines)
	newText := text.JoinLines(completion.Lines)
	diffResult := text.ComputeDiff(originalText, newText)

	stagingResult := text.CreateStages(&text.StagingParams{
		Diff:               diffResult,
		CursorRow:          e.buffer.Row(),
		CursorCol:          e.buffer.Col(),
		ViewportTop:        viewportTop,
		ViewportBottom:     viewportBottom,
		BaseLineOffset:     completion.StartLine,
		ProximityThreshold: e.config.CursorPrediction.ProximityThreshold,
		MaxLines:           e.config.MaxVisibleLines,
		AvailableWidth:     e.buffer.AvailableWidth(),
		FilePath:           e.buffer.Path(),
		NewLines:           completion.Lines,
		OldLines:           originalLines,
	})

	if stagingResult != nil && len(stagingResult.Stages) > 0 {
		firstStage := stagingResult.Stages[0]

		// Suppression compares against what the user actually sees: the first
		// stage. Doing this post-staging means cached single-stage entries can
		// match the visible portion of an incoming multi-stage completion.
		if e.suppressRejectedCompletionForStage(firstStage) {
			e.pendingMetricsInfo = nil
			e.stagedCompletion = nil
			e.state = stateIdle
			return completionSuppressed
		}

		e.stagedCompletion = &text.StagedCompletion{
			Stages:     stagingResult.Stages,
			CurrentIdx: 0,
		}

		if stagingResult.FirstNeedsNavigation {
			e.showStageCursorTarget(firstStage)
			return completionShown
		}

		e.showCurrentStage()
		return completionShown
	}

	return completionNoChanges
}
