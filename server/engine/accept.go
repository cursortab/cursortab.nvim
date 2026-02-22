package engine

import (
	"cursortab/logger"
	"cursortab/metrics"
	"cursortab/text"
	"cursortab/types"
	"cursortab/utils"
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

	// Send accept metric
	e.sendMetric(metrics.EventAccepted)

	// Sync the current staged completion with what was actually rendered.
	// When streaming renders a stage incrementally, Finalize() recomputes stages
	// from scratch and may produce different boundaries. The staged completion's
	// current stage must match the rendered completion for correct offset calculation
	// in advanceStagedCompletion.
	if e.stagedCompletion != nil && len(e.completions) > 0 {
		currentStage := e.getStage(e.stagedCompletion.CurrentIdx)
		if currentStage != nil {
			rendered := e.completions[0]
			currentStage.Lines = rendered.Lines
			currentStage.BufferStart = rendered.StartLine
			currentStage.BufferEnd = rendered.EndLineInc
			currentStage.Groups = e.currentGroups
		}
	}

	// 2. Clear completion state (keep prefetch)
	e.clearState(ClearOptions{})

	// 3. Check if this is the last stage and prefetch extends beyond it
	// Must try BEFORE advanceStagedCompletion which may clear the prefetch
	isLastStage := e.stagedCompletion != nil &&
		e.stagedCompletion.CurrentIdx == len(e.stagedCompletion.Stages)-1
	if isLastStage && e.cursorTarget != nil && e.cursorTarget.ShouldRetrigger {
		if e.prefetchState == prefetchReady && len(e.prefetchedCompletions) > 0 {
			currentStage := e.getStage(e.stagedCompletion.CurrentIdx)
			prefetch := e.prefetchedCompletions[0]
			prefetchResultEnd := prefetch.StartLine + len(prefetch.Lines) - 1

			// Only use prefetch if it has content beyond the stage just applied
			if currentStage != nil && prefetchResultEnd > currentStage.BufferEnd {
				e.syncBuffer()
				if e.tryShowPrefetchedCompletion() {
					return
				}
			}
		}
	}

	// 4. Advance staged completion if any
	if e.stagedCompletion != nil {
		e.advanceStagedCompletion()
	}

	// 5. Determine next state
	if e.hasMoreStages() {
		e.syncBuffer()
		e.prefetchAtNMinusOne()
		e.showOrNavigateToNextStage()
		return
	}

	// 6. No more stages - handle cursor target
	e.syncBuffer()
	if e.cursorTarget != nil && e.cursorTarget.ShouldRetrigger {
		// If prefetch is ready, use it
		if e.prefetchState == prefetchReady && len(e.prefetchedCompletions) > 0 {
			if e.tryShowPrefetchedCompletion() {
				return
			}
		}
		// If prefetch is in-flight, wait for it instead of triggering a new request
		if e.prefetchState == prefetchInFlight || e.prefetchState == prefetchWaitingForCursorPrediction {
			e.prefetchState = prefetchWaitingForTab
			e.buffer.ClearUI()
			e.state = stateIdle
			return
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
		// For pure addition stages (all groups are "addition" type), we're inserting
		// new lines, not replacing existing ones. So oldLineCount should be 0.
		// For other stages, oldLineCount is the range being replaced.
		isPureAddition := len(currentStage.Groups) > 0
		for _, g := range currentStage.Groups {
			if g.Type != "addition" {
				isPureAddition = false
				break
			}
		}

		var oldLineCount int
		if isPureAddition {
			oldLineCount = 0 // Pure additions insert, they don't replace
		} else {
			oldLineCount = currentStage.BufferEnd - currentStage.BufferStart + 1
		}
		newLineCount := len(currentStage.Lines)
		e.stagedCompletion.CumulativeOffset += newLineCount - oldLineCount
	}

	// Advance to next stage
	e.stagedCompletion.CurrentIdx++

	// Check if we're done
	if e.stagedCompletion.CurrentIdx >= len(e.stagedCompletion.Stages) {
		// Clear prefetch only if it overlaps with the stage just applied.
		// If prefetch is for a different line range, it can still be used.
		// Note: Use the resulting line range (StartLine + len(Lines) - 1) since
		// the completion may add lines beyond EndLineInc.
		if currentStage != nil && len(e.prefetchedCompletions) > 0 {
			prefetch := e.prefetchedCompletions[0]
			prefetchResultEnd := prefetch.StartLine + len(prefetch.Lines) - 1
			if prefetch.StartLine <= currentStage.BufferEnd && prefetchResultEnd >= currentStage.BufferStart {
				e.prefetchState = prefetchNone
				e.prefetchedCompletions = nil
			}
		}
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

				for _, group := range stage.Groups {
					group.BufferLine += e.stagedCompletion.CumulativeOffset
				}

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
	distance := utils.Abs(targetLine - cursorRow)
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

	// Use currentGroups directly, not getCurrentGroups().
	// During partial accept, rerenderPartial() updates currentGroups with the
	// current state. The stage's groups are stale after the first partial accept.
	groups := e.currentGroups
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
		e.advanceToNextLineOrFinalize()
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
		e.advanceToNextLineOrFinalize()
	} else {
		e.rerenderPartial()
	}
}

// advanceToNextLineOrFinalize handles completion of a line in a multi-line completion.
// If there are more lines in the current completion, it advances to them.
// Otherwise, it finalizes the partial accept.
func (e *Engine) advanceToNextLineOrFinalize() {
	if len(e.completions) == 0 {
		e.finalizePartialAccept()
		return
	}

	completion := e.completions[0]

	// If there are more lines to process in this completion, advance to them
	if len(completion.Lines) > 1 {
		e.completions[0].Lines = completion.Lines[1:]
		e.completions[0].StartLine++
		e.completions[0].EndLineInc = e.completions[0].StartLine + len(e.completions[0].Lines) - 1
		e.rerenderPartial()
		return
	}

	// Only one line remaining (or none), finalize
	e.finalizePartialAccept()
}

// partialAcceptNextLine accepts line-by-line.
func (e *Engine) partialAcceptNextLine() {
	if len(e.completions) == 0 || len(e.completions[0].Lines) == 0 {
		return
	}

	e.syncBuffer()
	bufferLines := e.buffer.Lines()

	completion := e.completions[0]
	firstLine := completion.Lines[0]

	// If target line is beyond buffer end, insert; otherwise replace
	var err error
	if completion.StartLine > len(bufferLines) {
		logger.Debug("partialAcceptNextLine: INSERT line %d (buffer has %d lines), content=%q",
			completion.StartLine, len(bufferLines), firstLine)
		err = e.buffer.InsertLine(completion.StartLine, firstLine)
	} else {
		logger.Debug("partialAcceptNextLine: REPLACE line %d (buffer has %d lines), content=%q",
			completion.StartLine, len(bufferLines), firstLine)
		err = e.buffer.ReplaceLine(completion.StartLine, firstLine)
	}
	if err != nil {
		logger.Error("partialAcceptNextLine: line operation failed: %v", err)
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
	e.clearState(ClearOptions{})

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
