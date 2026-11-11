package engine

import (
	"slices"
	"strings"

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

	switch e.processCompletion(response, manual) {
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

// checkTypingMatchesPrediction reports whether the user's typing in the
// displayed completion's range follows the completion: every typed line is a
// prefix of its completion line, and the typing grew a line or added one.
// hasRemaining reports whether some of the completion is still untyped.
func (e *Engine) checkTypingMatchesPrediction() (matches, hasRemaining bool) {
	if !e.display.hasCompletion() {
		return false, false
	}
	typed, ok := e.typedRange()
	target, original := e.display.current().Lines, e.display.oldLines()
	if !ok || len(typed) == 0 || len(typed) > len(target) || len(target) < len(original) {
		return false, false
	}

	progress := len(typed) > len(original)
	for i, line := range typed {
		if !strings.HasPrefix(target[i], line) {
			return false, false
		}
		if i < len(original) && len(line) > len(original[i]) {
			progress = true
		}
		if len(line) < len(target[i]) {
			hasRemaining = true
		}
	}
	if !progress {
		return false, false
	}
	return true, hasRemaining || len(typed) < len(target)
}

// typedRange returns the buffer lines that now stand in the displayed
// completion's range. Typing changes only the range, so it is what lies
// between the lines before the range and the lines that followed it.
func (e *Engine) typedRange() ([]string, bool) {
	lines := e.buffer.Lines()
	start, end := e.display.current().StartLine-1, len(lines)-e.display.linesAfter
	if start < 0 || end < start {
		return nil, false
	}
	return lines[start:end], true
}

// rerenderActiveCompletion shows the rest of the displayed completion after
// typing or a partial accept changed its range. Later stages shift by the
// change in line count. When nothing is left, the stage is complete.
func (e *Engine) rerenderActiveCompletion() {
	typed, ok := e.typedRange()
	if !ok {
		e.reject()
		return
	}
	completion := e.display.current()
	if slices.Equal(typed, completion.Lines) {
		e.completeCurrentStage()
		return
	}

	var tail []*text.Stage
	manual := false
	if sc := e.stagedCompletion; sc != nil {
		manual = sc.Manual
		tail = sc.Stages[min(sc.CurrentIdx+1, len(sc.Stages)):]
		changed := &text.Stage{BufferStart: completion.StartLine, BufferEnd: completion.EndLineInc, Lines: typed}
		text.ShiftAfter(tail, changed)
		if t := e.cursorTarget; t != nil && int(t.LineNumber) > changed.BufferEnd {
			t.LineNumber += int32(changed.LineDelta())
		}
	}

	e.pendingMetricsInfo = nil
	outcome := e.stageCompletion(completion.StartLine, slices.Clone(typed), completion.Lines, e.cursorTarget, manual)
	if outcome == completionShown && e.stagedCompletion != nil {
		e.stagedCompletion.Stages = append(e.stagedCompletion.Stages, tail...)
	}
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
	e.sendMetric(metrics.EventIgnored)
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

	// Partial accept mutates the displayed groups, so display a copy and keep
	// the stage intact.
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
		slices.Clone(text.LineRange(e.buffer.Lines(), stage.BufferStart, stage.BufferEnd)),
		len(e.buffer.Lines())-stage.BufferEnd,
		groups,
		e.rejectedCompletionFor(completion),
	)
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

// processCompletion stages a completion against the current buffer and shows
// its first stage.
func (e *Engine) processCompletion(response *types.CompletionResponse, manual bool) completionOutcome {
	defer logger.Trace("engine.processCompletion")()
	if response == nil || response.Completion == nil {
		return completionNoChanges
	}
	completion, cursorTarget := response.Completion, response.CursorTarget

	e.pendingMetricsInfo = response.MetricsInfo

	if !hasChanges(e.buffer.Lines(), completion) {
		return completionNoChanges
	}

	originalLines, newLines := reconcileRange(e.buffer.Lines(), completion)
	return e.stageCompletion(completion.StartLine, originalLines, newLines, cursorTarget, manual)
}

// stageCompletion stages the replacement of originalLines, starting at
// startLine, by newLines and shows its first stage.
func (e *Engine) stageCompletion(startLine int, originalLines, newLines []string, cursorTarget *types.CursorPredictionTarget, manual bool) completionOutcome {
	viewportTop, viewportBottom := e.buffer.ViewportBounds()
	originalText := text.JoinLines(originalLines)
	newText := text.JoinLines(newLines)
	diffResult := text.ComputeDiff(originalText, newText)

	stagingResult := text.CreateStages(&text.StagingParams{
		Diff:               diffResult,
		CursorRow:          e.buffer.Row(),
		CursorCol:          e.buffer.Col(),
		ViewportTop:        viewportTop,
		ViewportBottom:     viewportBottom,
		BaseLineOffset:     startLine,
		ProximityThreshold: e.config.CursorPrediction.ProximityThreshold,
		MaxLines:           e.config.MaxVisibleLines,
		AvailableWidth:     e.buffer.AvailableWidth(),
		FilePath:           e.buffer.Path(),
		NewLines:           newLines,
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

// reconcileRange returns the buffer lines the completion replaces and the
// lines that replace them.
//
// The replaced range extends past EndLineInc while the following buffer lines
// equal the corresponding completion lines: an accepted streaming stage may
// already have applied part of the completion, which makes EndLineInc stale.
// Extending only on matches keeps unrelated lines from showing as deletions.
//
// Trailing completion lines that repeat the buffer right after the range are
// dropped: models sometimes generate past the editable region.
func reconcileRange(lines []string, completion *types.Completion) (oldLines, newLines []string) {
	start := completion.StartLine
	newLines = completion.Lines

	end := completion.EndLineInc
	for end+1 < start+len(newLines) && end < len(lines) && lines[end] == newLines[end+1-start] {
		end++
	}
	oldLines = text.LineRange(lines, start, end)

	excess := len(newLines) - len(oldLines)
	trim := 0
	for trim < excess {
		next := end + excess - 1 - trim // 0-indexed buffer line after the range
		if next >= len(lines) || newLines[len(newLines)-1-trim] != lines[next] {
			break
		}
		trim++
	}
	return oldLines, newLines[:len(newLines)-trim]
}

// hasChanges reports whether applying completion would change lines.
func hasChanges(lines []string, completion *types.Completion) bool {
	if completion.StartLine+len(completion.Lines)-1 > completion.EndLineInc {
		return true
	}
	for i := completion.StartLine; i <= completion.EndLineInc; i++ {
		rel := i - completion.StartLine
		inBuffer := i >= 1 && i <= len(lines)
		inCompletion := rel < len(completion.Lines)
		if inBuffer != inCompletion || (inBuffer && lines[i-1] != completion.Lines[rel]) {
			return true
		}
	}
	return false
}
