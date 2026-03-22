package engine

import (
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"cursortab/contextfilter"
	"cursortab/logger"
	"cursortab/types"
)

// inertSuffixPattern matches cursor suffixes where insertion-only completions
// are still useful: whitespace, closing brackets, trailing punctuation.
// Matches Copilot's heuristic: /^\s*[)>}\]"'`]*\s*[:{;,]?\s*$/
var inertSuffixPattern = regexp.MustCompile(`^\s*[)>}\]"'` + "`" + `]*\s*[:{;,]?\s*$`)

// consecutiveDeletionThreshold is the number of consecutive deletion actions
// after which completions are re-enabled (user is rewriting, not correcting).
const consecutiveDeletionThreshold = 3

// suppressForSingleDeletion returns true if the last action was a single
// deletion (typo correction) without a streak of deletions (rewrite).
func (e *Engine) suppressForSingleDeletion() bool {
	if len(e.userActions) == 0 {
		return false
	}

	last := e.userActions[len(e.userActions)-1]
	if !isDeletion(last.ActionType) {
		return false
	}

	// Count consecutive deletions from the end
	consecutive := 0
	for i := len(e.userActions) - 1; i >= 0; i-- {
		if isDeletion(e.userActions[i].ActionType) {
			consecutive++
		} else {
			break
		}
	}

	// A streak of deletions means the user is rewriting → allow completions
	return consecutive < consecutiveDeletionThreshold
}

// suppressForMidLine returns true if the cursor is in the middle of a line
// with meaningful code to the right, and the provider is insertion-only.
func (e *Engine) suppressForMidLine() bool {
	if e.config.EditCompletionProvider {
		return false
	}

	lines := e.buffer.Lines()
	row := e.buffer.Row() // 1-indexed
	col := e.buffer.Col() // 0-indexed

	if row < 1 || row > len(lines) {
		return false
	}

	line := lines[row-1]
	if col >= len(line) {
		return false // cursor at end of line
	}

	suffix := line[col:]
	return !inertSuffixPattern.MatchString(suffix)
}

// suppressForNoEdits returns true if the buffer hasn't changed since the last
// save (or initial open). Files that skip history (e.g. COMMIT_EDITMSG) are
// never suppressed.
func (e *Engine) suppressForNoEdits() bool {
	if e.buffer.SkipHistory() {
		return false
	}
	return !e.buffer.IsModified()
}

func isDeletion(action types.UserActionType) bool {
	return action == types.ActionDeleteChar || action == types.ActionDeleteSelection
}

// contextualFilterState tracks state across filter invocations for momentum features.
type contextualFilterState struct {
	lastShown        bool      // Did the previous filter call result in "show"?
	lastDecisionTime time.Time // When the last accept/reject/suppress happened
}

// suppressForContextualFilter returns true if the contextual filter score is
// below the acceptance threshold. Updates filter state as a side effect.
func (e *Engine) suppressForContextualFilter() bool {
	score := contextfilter.Score(contextfilter.Input{
		Lines:         e.buffer.Lines(),
		Row:           e.buffer.Row(),
		Col:           e.buffer.Col(),
		FileExtension: strings.ToLower(filepath.Ext(e.buffer.Path())),
		PreviousLabel: e.filterState.lastShown,
		LastDecision:  e.filterState.lastDecisionTime,
		Now:           e.clock.Now(),
	})

	suppress := contextfilter.ShouldSuppress(score)

	e.filterState.lastShown = !suppress
	e.filterState.lastDecisionTime = e.clock.Now()

	if suppress {
		logger.Debug("contextual filter suppressed (score=%.3f, threshold=%.3f)", score, contextfilter.Threshold)
	} else {
		logger.Debug("contextual filter passed (score=%.3f)", score)
	}

	return suppress
}
