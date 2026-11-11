package engine

import (
	"slices"
	"strings"

	"cursortab/history"
	"cursortab/logger"
	"cursortab/metrics"
	"cursortab/text"
	"cursortab/types"
	"cursortab/utils"
)

// reject clears all state and returns to idle without caching the current
// completion as rejected. Cancels in-flight, prefetch, and any leftover stream
// from a prior accept-during-streaming, drops staged completions, clears the
// UI, and sends a reject metric if a completion was shown, even when only its
// cursor target is still on screen.
func (e *Engine) reject() {
	e.cancelInFlight()
	e.cancelPrefetch()
	e.buffer.ClearUI()
	e.sendMetric(metrics.EventRejected)
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
	if e.syncBuffer() {
		return
	}

	batch, completion := e.display.batchToApply(), e.display.current()
	if batch == nil || completion == nil {
		return
	}

	// Typing since the last commit is recorded first, as manual edits, so the
	// prediction's edit covers only what it changed.
	path, now := e.buffer.Path(), e.clock.Now().UnixNano()
	e.history.CommitUserEdits(path, e.buffer.Lines(), now)
	if err := batch.Execute(); err != nil {
		logger.Error("acceptCompletion: batch execution failed: %v", err)
		e.reject()
		return
	}
	e.history.CommitPredicted(path, history.Edit{
		StartLine:  completion.StartLine,
		EndLineInc: completion.EndLineInc,
		Lines:      completion.Lines,
	}, e.buffer.Lines(), now)
	e.completeCurrentStage()
}

// completeCurrentStage moves on once the current stage is in the buffer: it
// shows the next stage, a ready prefetch, or the cursor target.
func (e *Engine) completeCurrentStage() {
	e.sendMetric(metrics.EventAccepted)

	// Accept = forward progress; any cached rejections for this file are stale.
	e.forgetRejectedCompletions(e.buffer.Path())

	e.resetCompletionFields()

	isLastStage := e.stagedCompletion != nil &&
		e.stagedCompletion.CurrentIdx == len(e.stagedCompletion.Stages)-1
	if isLastStage && e.cursorTarget != nil && e.cursorTarget.ShouldRetrigger {
		if prefetch := e.readyPrefetchCompletion(); prefetch != nil {
			currentStage := e.getStage(e.stagedCompletion.CurrentIdx)
			prefetchResultEnd := prefetch.StartLine + len(prefetch.Lines) - 1

			// Only use prefetch if it has content beyond the stage just applied
			if currentStage != nil && prefetchResultEnd > currentStage.LastLine() {
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

	applied := e.getStage(e.stagedCompletion.CurrentIdx)
	e.stagedCompletion.CurrentIdx++

	if e.stagedCompletion.CurrentIdx >= len(e.stagedCompletion.Stages) {
		// Clear prefetch only if it overlaps with the stage just applied.
		// If prefetch is for a different line range, it can still be used.
		// Note: Use the resulting line range (StartLine + len(Lines) - 1) since
		// the completion may add lines beyond EndLineInc.
		if applied != nil {
			prefetch := e.readyPrefetchCompletion()
			if prefetch != nil {
				prefetchResultEnd := prefetch.StartLine + len(prefetch.Lines) - 1
				if prefetch.StartLine <= applied.LastLine() && prefetchResultEnd >= applied.BufferStart {
					e.clearPrefetch()
				}
			}
		}
		e.stagedCompletion = nil
		return
	}

	if applied != nil {
		text.ShiftAfter(e.stagedCompletion.Stages[e.stagedCompletion.CurrentIdx:], applied)
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

// partialAcceptCompletion accepts the next word of an appended line, or the
// next line of the first change in buffer order, then shows the rest of the
// completion. Accepting the last of it is a full accept.
func (e *Engine) partialAcceptCompletion() {
	if !e.display.hasCompletion() || e.syncBuffer() {
		return
	}
	groups := e.display.textGroups()
	g := firstChange(groups)
	if g == nil {
		return
	}

	before := slices.Clone(e.buffer.Lines())
	edit, completesGroup, ok := partialStep(g, before)
	if !ok {
		return
	}
	if completesGroup && len(groups) == 1 {
		e.acceptCompletion()
		return
	}

	var err error
	if edit.IsCharAppend {
		err = e.buffer.AppendText(edit.StartLine, edit.Appended)
	} else {
		err = e.buffer.SetLines(edit.Edit)
	}
	if err != nil {
		logger.Error("partialAcceptCompletion: applying the step failed: %v", err)
		e.reject()
		return
	}

	path, now := e.buffer.Path(), e.clock.Now().UnixNano()
	e.history.CommitUserEdits(path, before, now)
	e.history.CommitPredicted(path, edit.Edit, e.buffer.Lines(), now)
	e.forgetRejectedCompletions(path)
	e.rerenderActiveCompletion()
}

// partialEdit is one partial accept step.
type partialEdit struct {
	history.Edit
	// IsCharAppend marks a step that appends Appended to the end of the line.
	IsCharAppend bool
	Appended     string
}

// firstChange returns the group that comes first in the buffer. At the same
// line, an addition comes first: it is inserted before that line.
func firstChange(groups []*text.Group) *text.Group {
	var first *text.Group
	for _, g := range groups {
		if first == nil || g.BufferLine < first.BufferLine ||
			(g.BufferLine == first.BufferLine && g.Type == text.GroupAddition && first.Type != text.GroupAddition) {
			first = g
		}
	}
	return first
}

// partialStep returns the edit that accepts the next part of g in lines, and
// whether it completes g. An appended line advances by a word, a deletion is
// accepted whole, and other changes advance by a line.
func partialStep(g *text.Group, lines []string) (step partialEdit, completesGroup, ok bool) {
	line := g.BufferLine
	switch g.Type {
	case text.GroupDeletion:
		step.Edit = history.Edit{StartLine: line, EndLineInc: line + g.EndLine - g.StartLine}
		return step, true, line >= 1 && step.EndLineInc <= len(lines)
	case text.GroupAddition:
		if len(g.Lines) == 0 {
			return step, false, false
		}
		step.Edit = history.Edit{StartLine: line, EndLineInc: line - 1, Lines: g.Lines[:1]}
		return step, len(g.Lines) == 1, line >= 1 && line <= len(lines)+1
	}

	if len(g.Lines) == 0 || line < 1 || line > len(lines) {
		return step, false, false
	}
	current, target := lines[line-1], g.Lines[0]
	step.Edit = history.Edit{StartLine: line, EndLineInc: line, Lines: []string{target}}
	if g.RenderHint == text.HintAppendChars && len(g.Lines) == 1 && strings.HasPrefix(target, current) && current != target {
		rest := target[len(current):]
		step.IsCharAppend = true
		step.Appended = rest[:text.FindNextWordBoundary(rest)]
		step.Lines = []string{current + step.Appended}
		return step, len(step.Appended) == len(rest), true
	}
	return step, len(g.Lines) == 1, true
}
