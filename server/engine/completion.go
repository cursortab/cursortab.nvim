package engine

import (
	"cursortab/logger"
	"cursortab/metrics"
	"cursortab/text"
	"cursortab/types"
	"cursortab/utils"
)

func (e *Engine) handleCompletionReadyImpl(response *types.CompletionResponse, manual bool) {
	e.syncBuffer()

	if response == nil {
		response = &types.CompletionResponse{}
	}
	if response.Completion == nil {
		e.cursorTarget = response.CursorTarget
		e.handleCursorTarget()
		return
	}

	switch e.processCompletionWithManual(response, manual) {
	case completionShown:
		return
	case completionSuppressed:
		e.pendingMetricsInfo = nil
		return
	}

	e.pendingMetricsInfo = nil
	e.handleCompletionNoChanges(response)
}

func (e *Engine) handleCompletionNoChanges(response *types.CompletionResponse) {
	if response != nil && response.CursorTarget != nil {
		e.cursorTarget = response.CursorTarget
	} else if response != nil && response.Completion != nil && e.config.CursorPrediction.AutoAdvance && e.config.CursorPrediction.Enabled {
		completion := response.Completion
		e.cursorTarget = &types.CursorPredictionTarget{
			LineNumber:      int32(completion.EndLineInc),
			ShouldRetrigger: true,
		}
	}
	e.handleCursorTarget()
}

func (e *Engine) handleTextChangeImpl() {
	if !e.display.hasCompletion() {
		e.reject()
		e.startTextChangeTimer()
		return
	}

	e.syncBuffer()

	matches, hasRemaining := e.checkTypingMatchesPrediction()
	if matches {
		if hasRemaining {
			e.rerenderActiveCompletion()
			return
		}
		e.reject()
		e.startTextChangeTimer()
		return
	}

	e.rejectAndRemember()
	e.startTextChangeTimer()
}

// checkTypingMatchesPrediction checks if the current buffer state (after user typed)
// matches the prediction, meaning the user typed content consistent with the completion.
// Returns (matches, hasRemaining) where:
// - matches: true if the current buffer is a valid prefix of the target
// - hasRemaining: true if there's still content left to predict
func (e *Engine) checkTypingMatchesPrediction() (bool, bool) {
	originalLines := e.display.oldLines()
	if !e.display.hasCompletion() || len(originalLines) == 0 {
		return false, false
	}

	completion := e.display.current()
	targetLines := completion.Lines
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

func (e *Engine) rerenderActiveCompletion() completionOutcome {
	if !e.display.hasCompletion() {
		return completionNoChanges
	}

	var tail []*text.Stage
	manual := false
	if e.stagedCompletion != nil {
		manual = e.stagedCompletion.Manual
		next := e.stagedCompletion.CurrentIdx + 1
		if next >= 0 && next < len(e.stagedCompletion.Stages) {
			tail = append(tail, e.stagedCompletion.Stages[next:]...)
		}
	}

	var metricsInfo *types.MetricsInfo
	if e.currentMetrics.ID != "" || e.currentMetrics.Additions != 0 || e.currentMetrics.Deletions != 0 {
		metricsInfo = &types.MetricsInfo{
			ID:        e.currentMetrics.ID,
			Additions: e.currentMetrics.Additions,
			Deletions: e.currentMetrics.Deletions,
		}
	}

	outcome := e.processCompletionCandidate(e.display.current(), e.cursorTarget, metricsInfo, manual)
	if outcome == completionShown && e.stagedCompletion != nil && len(tail) > 0 {
		e.stagedCompletion.Stages = append(e.stagedCompletion.Stages, tail...)
	}
	return outcome
}

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

		if e.readyPrefetchCompletion() != nil && e.tryShowPrefetchedCompletion() {
			return
		}
		if e.hasInflightPrefetch() {
			e.setInflightPrefetchWait(prefetchForCursorPrediction)
		}
		e.clearCompletionUIOnly()
		return
	}

	e.state = stateHasCursorTarget
	e.buffer.ShowCursorTarget(int(e.cursorTarget.LineNumber))
}

func (e *Engine) clearCompletionUIOnly() {
	if e.display.hasCompletion() {
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
	e.display.setRejectionCandidate(candidate)
	e.buffer.ShowCursorTarget(int(target.LineNumber))
}

func (e *Engine) showStageCursorTarget(stage *text.Stage) {
	if stage == nil {
		return
	}
	e.showCursorTargetWithCandidate(&types.CursorPredictionTarget{
		LineNumber:      int32(stage.BufferStart),
		ShouldRetrigger: false,
	}, e.rejectedCompletionForStage(stage))
}

func (e *Engine) showCurrentStage() {
	if e.stagedCompletion == nil || e.stagedCompletion.CurrentIdx >= len(e.stagedCompletion.Stages) {
		return
	}
	manual := e.stagedCompletion.Manual

	stage := e.getStage(e.stagedCompletion.CurrentIdx)
	if stage == nil {
		return
	}

	e.cursorTarget = stage.CursorTarget
	e.state = stateHasCompletion

	// Deep copy groups so that partial accept mutations (advanceGroupsAfterAccept)
	// don't corrupt the stage's original Groups, which advanceStagedCompletion
	// needs for correct isPureInsertion/offset calculations.
	e.setDisplayedStage(stage, text.CopyGroups(stage.Groups))
	e.recordMetricsShown(e.pendingMetricsInfo, manual) // nil for streaming
	e.pendingMetricsInfo = nil
}

func (e *Engine) setDisplayedStage(stage *text.Stage, groups []*text.Group) {
	if stage == nil {
		e.resetCompletionFields()
		return
	}

	completion := &types.Completion{
		StartLine:  stage.BufferStart,
		EndLineInc: stage.BufferEnd,
		Lines:      stage.Lines,
	}

	e.display.show(
		completion,
		e.buffer.PrepareCompletion(
			stage.BufferStart,
			stage.BufferEnd,
			stage.Lines,
			groups,
		),
		e.displayOriginalLines(stage.BufferStart, stage.BufferEnd),
		groups,
		e.rejectedCompletionFor(completion),
	)
}

func (e *Engine) displayOriginalLines(startLine, endLineInc int) []string {
	bufferLines := e.buffer.Lines()
	var originalLines []string
	for i := startLine; i <= endLineInc && i-1 < len(bufferLines); i++ {
		originalLines = append(originalLines, bufferLines[i-1])
	}
	return originalLines
}

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
	// continuation (e.g. handleCompletionNoChanges).
	completionNoChanges completionOutcome = iota
	// completionShown means the completion was rendered (or a cursor target
	// was shown) and the engine transitioned to a non-idle state.
	completionShown
	// completionSuppressed means the completion matched a recently rejected
	// entry and was intentionally dropped. Engine is now idle; caller should
	// not fall back to cursor target handling.
	completionSuppressed
)

func (e *Engine) processCompletion(response *types.CompletionResponse) completionOutcome {
	return e.processCompletionWithManual(response, false)
}

func (e *Engine) processCompletionWithManual(response *types.CompletionResponse, manual bool) completionOutcome {
	defer logger.Trace("engine.processCompletion")()
	if response == nil || response.Completion == nil {
		return completionNoChanges
	}

	completion := response.Completion
	return e.processCompletionCandidate(completion, response.CursorTarget, response.MetricsInfo, manual)
}

func (e *Engine) processCompletionCandidate(completion *types.Completion, cursorTarget *types.CursorPredictionTarget, metricsInfo *types.MetricsInfo, manual bool) completionOutcome {
	if completion == nil {
		return completionNoChanges
	}

	e.pendingMetricsInfo = metricsInfo

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
		if cursorTarget != nil {
			stagingResult.Stages[len(stagingResult.Stages)-1].CursorTarget = cursorTarget
		}

		// Suppression compares against what the user actually sees: the first
		// stage. Doing this post-staging means cached single-stage entries can
		// match the visible portion of an incoming multi-stage completion.
		if e.suppressRejectedCompletionForStage(firstStage, manual) {
			e.pendingMetricsInfo = nil
			e.stagedCompletion = nil
			e.state = stateIdle
			return completionSuppressed
		}

		e.stagedCompletion = &text.StagedCompletion{
			Stages:     stagingResult.Stages,
			CurrentIdx: 0,
			Manual:     manual,
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
