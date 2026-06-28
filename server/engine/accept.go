package engine

import (
	"cursortab/logger"
	"cursortab/metrics"
	"cursortab/text"
	"cursortab/types"
	"cursortab/utils"
)

// reject clears all state and returns to idle without caching the current
// completion as rejected. Cancels in-flight, prefetch, and any leftover stream
// from a prior accept-during-streaming, drops staged completions, clears the
// UI, and sends a reject metric if a completion was shown.
func (e *Engine) reject() {
	e.cancelCurrentRequest()
	e.cancelPrefetch()
	e.cancelStreaming()
	e.buffer.ClearUI()
	if e.display.hasCompletion() {
		e.sendMetric(metrics.EventRejected)
	}
	e.cursorTarget = nil
	e.stagedCompletion = nil
	e.resetCompletionFields()
	e.state = stateIdle
}

// rejectAndRemember clears all state and caches the current completion so
// similar completions are suppressed for a short TTL.
func (e *Engine) rejectAndRemember() {
	e.rememberRejectedCompletion()
	e.reject()
}

func (e *Engine) acceptCompletion() {
	result, _ := e.buffer.Sync(e.WorkspacePath)
	if result != nil && result.BufferChanged {
		e.reject()
		return
	}

	batch := e.display.batchToApply()
	if batch == nil {
		return
	}

	if err := batch.Execute(); err != nil {
		logger.Error("acceptCompletion: batch execution failed: %v", err)
		e.reject()
		return
	}
	e.buffer.CommitPending()
	e.saveCurrentFileState()

	e.sendMetric(metrics.EventAccepted)

	// Accept = forward progress; any cached rejections for this file are stale.
	e.forgetRejectedCompletions(e.buffer.Path())

	// Sync the current staged completion with what was actually rendered.
	// When streaming renders a stage incrementally, Finalize() recomputes stages
	// from scratch and may produce different boundaries. The staged completion's
	// current stage must match the rendered completion for correct offset calculation
	// in advanceStagedCompletion.
	if e.stagedCompletion != nil && e.display.hasCompletion() {
		currentStage := e.getStage(e.stagedCompletion.CurrentIdx)
		if currentStage != nil {
			rendered := e.display.current()
			currentStage.Lines = rendered.Lines
			currentStage.BufferStart = rendered.StartLine
			currentStage.BufferEnd = rendered.EndLineInc
			currentStage.Groups = e.display.textGroups()
		}
	}

	e.resetCompletionFields()

	isLastStage := e.stagedCompletion != nil &&
		e.stagedCompletion.CurrentIdx == len(e.stagedCompletion.Stages)-1
	if isLastStage && e.cursorTarget != nil && e.cursorTarget.ShouldRetrigger {
		if prefetch := e.readyPrefetchCompletion(); prefetch != nil {
			currentStage := e.getStage(e.stagedCompletion.CurrentIdx)
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
		if e.readyPrefetchCompletion() != nil {
			if e.tryShowPrefetchedCompletion() {
				return
			}
		}
		if e.hasInflightPrefetch() {
			e.setInflightPrefetchWait(prefetchAfterTab)
			e.buffer.ClearUI()
			e.state = stateIdle
			return
		}
		e.prefetchAtCursorTarget()
	}
	e.transitionAfterAccept()
}

func (e *Engine) acceptCursorTarget() {
	if e.cursorTarget == nil {
		return
	}

	targetLine := int(e.cursorTarget.LineNumber)
	if err := e.buffer.MoveCursor(targetLine, true, true); err != nil {
		logger.Error("acceptCursorTarget: move cursor failed: %v", err)
	}

	// Accept = forward progress; any cached rejections for this file are stale.
	e.forgetRejectedCompletions(e.buffer.Path())

	if e.hasMoreStages() {
		e.syncBuffer()
		e.showCurrentStage()
		return
	}

	e.syncBuffer()

	if e.readyPrefetchCompletion() != nil {
		if e.tryShowPrefetchedCompletion() {
			return
		}
	}

	if e.hasInflightPrefetch() {
		e.setInflightPrefetchWait(prefetchAfterTab)
		return
	}

	if e.cursorTarget.ShouldRetrigger {
		e.requestCompletion(types.CompletionSourceTyping, false)
		e.cursorTarget = nil
		return
	}

	e.buffer.ClearUI()
	e.cursorTarget = nil
	e.state = stateIdle
}

func (e *Engine) advanceStagedCompletion() {
	if e.stagedCompletion == nil {
		return
	}

	currentStage := e.getStage(e.stagedCompletion.CurrentIdx)
	if currentStage != nil {
		var oldLineCount int
		if stageIsPureInsertion(currentStage) {
			oldLineCount = 0
		} else {
			oldLineCount = currentStage.BufferEnd - currentStage.BufferStart + 1
		}
		newLineCount := len(currentStage.Lines)
		e.stagedCompletion.CumulativeOffset += newLineCount - oldLineCount
	}

	e.stagedCompletion.CurrentIdx++

	if e.stagedCompletion.CurrentIdx >= len(e.stagedCompletion.Stages) {
		// Clear prefetch only if it overlaps with the stage just applied.
		// If prefetch is for a different line range, it can still be used.
		// Note: Use the resulting line range (StartLine + len(Lines) - 1) since
		// the completion may add lines beyond EndLineInc.
		if currentStage != nil {
			prefetch := e.readyPrefetchCompletion()
			if prefetch != nil {
				prefetchResultEnd := prefetch.StartLine + len(prefetch.Lines) - 1
				if prefetch.StartLine <= currentStage.BufferEnd && prefetchResultEnd >= currentStage.BufferStart {
					e.clearPrefetch()
				}
			}
		}
		e.stagedCompletion = nil
		return
	}

	// Apply cumulative offset to remaining stages that are at or after the
	// applied stage's buffer position. Stages before the applied position
	// are unaffected by the line count change.
	if e.stagedCompletion.CumulativeOffset != 0 && currentStage != nil {
		appliedStart := currentStage.BufferStart
		for i := e.stagedCompletion.CurrentIdx; i < len(e.stagedCompletion.Stages); i++ {
			stage := e.getStage(i)
			if stage != nil && stage.BufferStart >= appliedStart {
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

func (e *Engine) hasMoreStages() bool {
	return e.stagedCompletion != nil &&
		e.stagedCompletion.CurrentIdx < len(e.stagedCompletion.Stages)
}

func (e *Engine) showOrNavigateToNextStage() {
	nextStage := e.getStage(e.stagedCompletion.CurrentIdx)
	if nextStage == nil {
		return
	}

	cursorRow := e.buffer.Row()
	viewportTop, viewportBottom := e.buffer.ViewportBounds()
	needsNav := text.StageNeedsNavigation(nextStage, cursorRow, viewportTop, viewportBottom, e.config.CursorPrediction.ProximityThreshold)

	if !needsNav {
		e.showCurrentStage()
		return
	}

	e.showStageCursorTarget(nextStage)
}

func (e *Engine) transitionAfterAccept() {
	if e.cursorTarget == nil || !e.config.CursorPrediction.Enabled {
		e.buffer.ClearUI()
		e.state = stateIdle
		return
	}

	cursorRow := e.buffer.Row()
	targetLine := int(e.cursorTarget.LineNumber)
	distance := utils.Abs(targetLine - cursorRow)
	if distance <= e.config.CursorPrediction.ProximityThreshold {
		e.buffer.ClearUI()
		e.cursorTarget = nil
		e.state = stateIdle
		return
	}

	e.buffer.ShowCursorTarget(targetLine)
	e.state = stateHasCursorTarget
}

func (e *Engine) partialAcceptCompletion() {
	if !e.display.hasCompletion() {
		return
	}

	groups := e.display.textGroups()
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

func (e *Engine) partialAcceptAppendChars(group *text.Group) {
	completion := e.display.current()
	if group == nil || completion == nil || len(completion.Lines) == 0 {
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
	targetLine := completion.Lines[0]

	if len(currentLine) >= len(targetLine) {
		e.advanceToNextLineOrFinalize()
		return
	}

	remainingGhost := targetLine[len(currentLine):]

	acceptLen := text.FindNextWordBoundary(remainingGhost)
	textToAccept := remainingGhost[:acceptLen]

	if err := e.buffer.InsertText(lineIdx+1, len(currentLine), textToAccept, true); err != nil {
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

func (e *Engine) advanceToNextLineOrFinalize() {
	if !e.display.hasCompletion() {
		e.finalizePartialAccept()
		return
	}

	completion := e.display.current()

	if len(completion.Lines) > 1 {
		if !e.display.advanceLine(false) {
			e.finalizePartialAccept()
			return
		}
		e.rerenderPartial()
		return
	}

	e.finalizePartialAccept()
}

func (e *Engine) partialAcceptNextLine() {
	completion := e.display.current()
	if completion == nil || len(completion.Lines) == 0 {
		return
	}

	e.syncBuffer()
	bufferLines := e.buffer.Lines()

	firstLine := completion.Lines[0]

	isInsertion := completion.StartLine > len(bufferLines)
	groups := e.display.textGroups()
	if !isInsertion && len(groups) > 0 && groups[0].StartLine == 1 && groups[0].Type == "addition" {
		isInsertion = true
	}

	var err error
	if isInsertion {
		logger.Debug("partialAcceptNextLine: INSERT line %d (buffer has %d lines), content=%q",
			completion.StartLine, len(bufferLines), firstLine)
		err = e.buffer.InsertLine(completion.StartLine, firstLine, true)
	} else {
		logger.Debug("partialAcceptNextLine: REPLACE line %d (buffer has %d lines), content=%q",
			completion.StartLine, len(bufferLines), firstLine)
		err = e.buffer.ReplaceLine(completion.StartLine, firstLine, true)
	}
	if err != nil {
		logger.Error("partialAcceptNextLine: line operation failed: %v", err)
		return
	}

	if len(completion.Lines) == 1 {
		e.finalizePartialAccept()
		return
	}

	if !e.display.advanceLine(isInsertion) {
		e.finalizePartialAccept()
		return
	}

	e.rerenderPartial()
}

func (e *Engine) finalizePartialAccept() {
	result, _ := e.buffer.Sync(e.WorkspacePath)
	if result != nil && result.BufferChanged {
		e.reject()
		return
	}

	e.buffer.CommitPending()
	e.saveCurrentFileState()
	e.forgetRejectedCompletions(e.buffer.Path())
	e.resetCompletionFields()

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
		if e.readyPrefetchCompletion() != nil {
			if e.tryShowPrefetchedCompletion() {
				return
			}
		}
		e.prefetchAtCursorTarget()
	}
	e.transitionAfterAccept()
}

// advanceGroupsAfterAccept adjusts groups after accepting one completion line.
// Groups have StartLine/EndLine relative to the completion's Lines array. After
// slicing Lines[1:], all positions shift by -1. Groups covering the accepted line
// (position 1) are trimmed or removed. wasInsertion is true when the accepted line
// was inserted into the buffer (addition), causing all buffer positions to shift.
func advanceGroupsAfterAccept(groups []*text.Group, wasInsertion bool) []*text.Group {
	if len(groups) == 0 {
		return nil
	}

	var result []*text.Group
	for _, g := range groups {
		if g.StartLine == 1 && g.EndLine == 1 {
			// Single-line group at the accepted line: fully consumed, remove
			continue
		}
		if g.StartLine == 1 {
			// Multi-line group starting at the accepted line: remove first line
			g.Lines = g.Lines[1:]
			if len(g.OldLines) > 0 {
				g.OldLines = g.OldLines[1:]
			}
			// After removing line 1, the group starts at old position 2.
			// The decrement below will adjust it to 1 in the new numbering.
			g.StartLine = 2
			g.RenderHint = ""
			g.ColStart = 0
			g.ColEnd = 0
		}

		// Shift to match sliced completion (Lines[1:])
		g.StartLine--
		g.EndLine--

		if wasInsertion {
			g.BufferLine++
		}

		result = append(result, g)
	}

	return result
}

func (e *Engine) rerenderPartial() {
	completion := e.display.current()
	groups := e.display.textGroups()
	if completion == nil || len(groups) == 0 {
		return
	}

	e.syncBuffer()

	// For append_chars groups, update ColStart and OldLines from current buffer state
	if groups[0].RenderHint == "append_chars" {
		bufferLines := e.buffer.Lines()
		lineIdx := groups[0].BufferLine - 1
		if lineIdx >= 0 && lineIdx < len(bufferLines) {
			groups[0].ColStart = len(bufferLines[lineIdx])
			groups[0].OldLines = []string{bufferLines[lineIdx]}
		}
	}

	e.setDisplayedStage(&text.Stage{
		BufferStart: completion.StartLine,
		BufferEnd:   completion.EndLineInc,
		Lines:       completion.Lines,
		Groups:      groups,
	}, groups)
}
