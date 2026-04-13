package engine

import (
	"regexp"

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
// with meaningful code to the right, and the provider is inline (not FIM or edit).
func (e *Engine) suppressForMidLine() bool {
	if e.config.EditCompletionProvider || e.config.ProviderName == string(types.ProviderTypeFIM) {
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

// suppressForDisabledScope returns the matched scope name if the cursor is
// inside a treesitter scope listed in DisabledIn, or "" if not suppressed.
func (e *Engine) suppressForDisabledScope() string {
	if len(e.config.DisabledIn) == 0 {
		return ""
	}

	scopes := e.buffer.CursorScopes()
	if len(scopes) == 0 {
		return ""
	}

	disabled := make(map[string]bool, len(e.config.DisabledIn))
	for _, s := range e.config.DisabledIn {
		disabled[s] = true
	}

	for _, scope := range scopes {
		if disabled[scope] {
			return scope
		}
	}
	return ""
}

func isDeletion(action types.UserActionType) bool {
	return action == types.ActionDeleteChar || action == types.ActionDeleteSelection
}
