package engine

import (
	"cursortab/logger"
	"cursortab/text"
	"cursortab/types"
)

// reject clears all state and returns to idle.
func (e *Engine) reject() {
	e.clearState(ClearOptions{
		CancelCurrent:     true,
		CancelPrefetch:    true,
		ClearStaged:       true,
		ClearCursorTarget: true,
		CallOnReject:      true,
	})
	e.state = stateIdle
}

// clearCompletionState clears completion-related state after accept.
// Does not affect prefetch, staged completion, or cursor target.
func (e *Engine) clearCompletionState() {
	e.completions = nil
	e.applyBatch = nil
	e.currentGroups = nil
	e.completionOriginalLines = nil
}

// acceptCompletion handles Tab key acceptance of completions.
func (e *Engine) acceptCompletion() {
	// Sync buffer first to detect file switches
	result, _ := e.buffer.Sync(e.WorkspacePath)
	if result != nil && result.BufferChanged {
		// File switched - reject stale completion to prevent mixing diff histories from different files
		e.reject()
		return
	}

	if e.applyBatch == nil {
		return
	}

	// 1. Apply and commit
	if err := e.applyBatch.Execute(); err != nil {
		logger.Error("acceptCompletion: batch execution failed: %v", err)
		e.clearAll()
		return
	}
	e.buffer.CommitPending()
	e.saveCurrentFileState()

	// Notify provider of acceptance (for usage tracking/billing)
	if accepter, ok := e.provider.(CompletionAccepter); ok {
		go accepter.AcceptCompletion(e.mainCtx)
	}

	// 2. Clear completion state (keep prefetch)
	e.clearCompletionState()

	// 3. Advance staged completion if any
	if e.stagedCompletion != nil {
		e.advanceStagedCompletion()
	}

	// 4. Determine next state
	if e.hasMoreStages() {
		e.syncBuffer()
		e.prefetchAtNMinusOne()
		e.showOrNavigateToNextStage()
		return
	}

	// 5. No more stages - handle cursor target
	e.syncBuffer()
	if e.cursorTarget != nil && e.cursorTarget.ShouldRetrigger {
		// If prefetch is ready, use it to show next completion/cursor prediction
		if e.prefetchState == prefetchReady && len(e.prefetchedCompletions) > 0 {
			if e.tryShowPrefetchedCompletion() {
				return
			}
		}
		e.prefetchAtCursorTarget()
	}
	e.transitionAfterAccept()
}

// acceptCursorTarget handles Tab key from HasCursorTarget state.
// Moves cursor to target and shows next stage or handles prefetch.
func (e *Engine) acceptCursorTarget() {
	if e.cursorTarget == nil {
		return
	}

	// 1. Move cursor to target line
	targetLine := int(e.cursorTarget.LineNumber)
	if err := e.buffer.MoveCursor(targetLine, true, true); err != nil {
		logger.Error("acceptCursorTarget: move cursor failed: %v", err)
	}

	// 2. If more staged completions, show current stage
	if e.hasMoreStages() {
		e.syncBuffer()
		e.showCurrentStage()
		return
	}

	// 3. No staged completions - handle prefetch/retrigger logic
	e.syncBuffer()

	// 3a. Try to use prefetched completion
	if e.prefetchState == prefetchReady && len(e.prefetchedCompletions) > 0 {
		if e.tryShowPrefetchedCompletion() {
			return
		}
	}

	// 3b. If prefetch in flight, wait for it
	if e.prefetchState == prefetchInFlight {
		e.prefetchState = prefetchWaitingForTab
		return
	}

	// 3c. If should retrigger, request new completion
	if e.cursorTarget.ShouldRetrigger {
		e.requestCompletion(types.CompletionSourceTyping)
		e.cursorTarget = nil
		return
	}

	// 3d. Otherwise, clear and go idle
	e.buffer.ClearUI()
	e.cursorTarget = nil
	e.state = stateIdle
}

// advanceStagedCompletion advances to the next stage and applies line offset
// to remaining stages when line counts change.
func (e *Engine) advanceStagedCompletion() {
	if e.stagedCompletion == nil {
		return
	}

	// Calculate cumulative offset from current stage
	currentStage := e.getStage(e.stagedCompletion.CurrentIdx)
	if currentStage != nil {
		oldLineCount := currentStage.BufferEnd - currentStage.BufferStart + 1
		newLineCount := len(currentStage.Lines)
		e.stagedCompletion.CumulativeOffset += newLineCount - oldLineCount
	}

	// Advance to next stage
	e.stagedCompletion.CurrentIdx++

	// Check if we're done
	if e.stagedCompletion.CurrentIdx >= len(e.stagedCompletion.Stages) {
		e.stagedCompletion = nil
		return
	}

	// Apply cumulative offset to remaining stages
	if e.stagedCompletion.CumulativeOffset != 0 {
		for i := e.stagedCompletion.CurrentIdx; i < len(e.stagedCompletion.Stages); i++ {
			stage := e.getStage(i)
			if stage != nil {
				stage.BufferStart += e.stagedCompletion.CumulativeOffset
				stage.BufferEnd += e.stagedCompletion.CumulativeOffset

				if stage.CursorTarget != nil {
					stage.CursorTarget.LineNumber += int32(e.stagedCompletion.CumulativeOffset)
				}
			}
		}
		e.stagedCompletion.CumulativeOffset = 0
	}
}

// hasMoreStages returns true if there are more stages to process.
func (e *Engine) hasMoreStages() bool {
	return e.stagedCompletion != nil &&
		e.stagedCompletion.CurrentIdx < len(e.stagedCompletion.Stages)
}

// showOrNavigateToNextStage checks distance to next stage and either shows it
// directly (if close) or shows a cursor target (if far).
func (e *Engine) showOrNavigateToNextStage() {
	nextStage := e.getStage(e.stagedCompletion.CurrentIdx)
	if nextStage == nil {
		return
	}

	// Calculate distance from cursor to next stage
	cursorRow := e.buffer.Row()
	var distance int
	if cursorRow < nextStage.BufferStart {
		distance = nextStage.BufferStart - cursorRow
	} else if cursorRow > nextStage.BufferEnd {
		distance = cursorRow - nextStage.BufferEnd
	} else {
		distance = 0
	}

	if distance <= e.config.CursorPrediction.ProximityThreshold {
		// Close enough - show stage directly
		e.showCurrentStage()
		return
	}

	// Too far - show cursor target instead
	e.cursorTarget = &types.CursorPredictionTarget{
		RelativePath:    e.buffer.Path(),
		LineNumber:      int32(nextStage.BufferStart),
		ShouldRetrigger: false,
	}
	e.state = stateHasCursorTarget
	e.buffer.ShowCursorTarget(nextStage.BufferStart)
}

// transitionAfterAccept handles state transition after accept based on cursor target.
func (e *Engine) transitionAfterAccept() {
	// If no cursor target or prediction disabled, go idle
	if e.cursorTarget == nil || !e.config.CursorPrediction.Enabled {
		e.buffer.ClearUI()
		e.state = stateIdle
		return
	}

	// Never show cursor target within proximity threshold
	cursorRow := e.buffer.Row()
	targetLine := int(e.cursorTarget.LineNumber)
	distance := abs(targetLine - cursorRow)
	if distance <= e.config.CursorPrediction.ProximityThreshold {
		e.buffer.ClearUI()
		e.cursorTarget = nil
		e.state = stateIdle
		return
	}

	// Show cursor target indicator
	e.buffer.ShowCursorTarget(targetLine)
	e.state = stateHasCursorTarget
}

// partialAcceptCompletion handles Ctrl+Right partial acceptance.
func (e *Engine) partialAcceptCompletion() {
	if len(e.completions) == 0 {
		return
	}

	groups := e.getCurrentGroups()
	if len(groups) == 0 {
		return
	}

	firstGroup := groups[0]

	if firstGroup.RenderHint == "append_chars" {
		e.partialAcceptAppendChars(firstGroup)
	} else {
		e.partialAcceptNextLine()
	}
}

// partialAcceptAppendChars accepts word-by-word for append_chars hint.
func (e *Engine) partialAcceptAppendChars(group *text.Group) {
	if group == nil || len(e.completions) == 0 || len(e.completions[0].Lines) == 0 {
		return
	}

	e.syncBuffer()
	bufferLines := e.buffer.Lines()
	lineIdx := group.BufferLine - 1

	if lineIdx < 0 || lineIdx >= len(bufferLines) {
		logger.Error("partialAcceptAppendChars: buffer line out of range: %d", group.BufferLine)
		return
	}

	currentLine := bufferLines[lineIdx]
	targetLine := e.completions[0].Lines[0]

	if len(currentLine) >= len(targetLine) {
		e.finalizePartialAccept()
		return
	}

	remainingGhost := targetLine[len(currentLine):]

	acceptLen := text.FindNextWordBoundary(remainingGhost)
	textToAccept := remainingGhost[:acceptLen]

	if err := e.buffer.InsertText(lineIdx+1, len(currentLine), textToAccept); err != nil {
		logger.Error("partialAcceptAppendChars: insert text failed: %v", err)
		return
	}

	newLineLen := len(currentLine) + acceptLen
	if newLineLen >= len(targetLine) {
		e.finalizePartialAccept()
	} else {
		e.rerenderPartial()
	}
}

// partialAcceptNextLine accepts line-by-line.
func (e *Engine) partialAcceptNextLine() {
	if len(e.completions) == 0 || len(e.completions[0].Lines) == 0 {
		return
	}

	completion := e.completions[0]
	firstLine := completion.Lines[0]

	if err := e.buffer.ReplaceLine(completion.StartLine, firstLine); err != nil {
		logger.Error("partialAcceptNextLine: replace line failed: %v", err)
		return
	}

	if len(completion.Lines) == 1 {
		e.finalizePartialAccept()
		return
	}

	e.completions[0].Lines = completion.Lines[1:]
	e.completions[0].StartLine++
	e.completions[0].EndLineInc = e.completions[0].StartLine + len(e.completions[0].Lines) - 1

	e.rerenderPartial()
}

// finalizePartialAccept commits partial accept and handles next stage.
func (e *Engine) finalizePartialAccept() {
	// Sync buffer first to detect file switches
	result, _ := e.buffer.Sync(e.WorkspacePath)
	if result != nil && result.BufferChanged {
		// File switched - reject stale completion to prevent mixing diff histories from different files
		e.reject()
		return
	}

	e.buffer.CommitPending()
	e.saveCurrentFileState()
	e.clearCompletionState()

	if e.stagedCompletion != nil {
		e.advanceStagedCompletion()
	}

	if e.hasMoreStages() {
		e.syncBuffer()
		e.prefetchAtNMinusOne()
		e.showOrNavigateToNextStage()
		return
	}

	e.syncBuffer()
	if e.cursorTarget != nil && e.cursorTarget.ShouldRetrigger {
		if e.prefetchState == prefetchReady && len(e.prefetchedCompletions) > 0 {
			if e.tryShowPrefetchedCompletion() {
				return
			}
		}
		e.prefetchAtCursorTarget()
	}
	e.transitionAfterAccept()
}

// rerenderPartial re-renders remaining ghost text after partial accept.
func (e *Engine) rerenderPartial() {
	if len(e.completions) == 0 {
		return
	}

	completion := e.completions[0]

	e.syncBuffer()

	bufferLines := e.buffer.Lines()
	var originalLines []string
	for i := completion.StartLine; i <= completion.EndLineInc && i-1 < len(bufferLines); i++ {
		originalLines = append(originalLines, bufferLines[i-1])
	}

	diffResult := text.ComputeDiff(
		text.JoinLines(originalLines),
		text.JoinLines(completion.Lines),
	)

	groups := text.GroupChanges(diffResult.Changes)

	for _, g := range groups {
		g.BufferLine = completion.StartLine + g.StartLine - 1
	}

	e.applyBatch = e.buffer.PrepareCompletion(
		completion.StartLine,
		completion.EndLineInc,
		completion.Lines,
		groups,
	)

	e.currentGroups = groups
	e.completionOriginalLines = originalLines
}

// getCurrentGroups returns the groups for the current completion/stage.
func (e *Engine) getCurrentGroups() []*text.Group {
	if e.stagedCompletion != nil {
		stage := e.getStage(e.stagedCompletion.CurrentIdx)
		if stage != nil {
			return stage.Groups
		}
	}
	return e.currentGroups
}
