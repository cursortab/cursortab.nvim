package text

import "sort"

// Group represents consecutive changes of the same type for rendering
type Group struct {
	Type      string   // "modification", "addition", "deletion"
	StartLine int      // 1-indexed, relative to stage content
	EndLine   int      // 1-indexed, inclusive
	Lines     []string // New content
	OldLines  []string // Old content (modifications only)

	// BufferLine is the 1-indexed absolute buffer position for rendering.
	// Computed by staging/grouping using LineMapping.GetBufferLine for correct coordinate mapping.
	BufferLine int

	// Character-level rendering hints (single-line only)
	RenderHint string // "", "append_chars", "replace_chars", "delete_chars"
	ColStart   int    // For character-level changes
	ColEnd     int    // For character-level changes
}

// GroupChanges groups consecutive same-type changes for efficient rendering.
// Returns groups sorted by StartLine. Deletions are not grouped (they don't render as content).
// Group content is populated from change.Content and change.OldContent fields.
func GroupChanges(changes map[int]LineChange) []*Group {
	if len(changes) == 0 {
		return nil
	}

	// Get sorted line numbers, excluding deletions
	var lineNums []int
	for lineNum, change := range changes {
		if change.Type != ChangeDeletion {
			lineNums = append(lineNums, lineNum)
		}
	}

	if len(lineNums) == 0 {
		return nil
	}

	sort.Ints(lineNums)

	var groups []*Group
	var currentGroup *Group

	for _, lineNum := range lineNums {
		change := changes[lineNum]
		groupType := change.Type.GroupType()
		changeHasHint := change.Type.RenderHint() != ""

		// Determine if we should extend current group or start new one
		// Never extend a group if:
		// - Group types differ
		// - Lines are not consecutive
		// - Current group has a render hint (keep hinted groups single-line)
		// - New change has a render hint (each hinted change gets its own group)
		shouldStartNew := currentGroup == nil ||
			currentGroup.Type != groupType ||
			lineNum != currentGroup.EndLine+1 ||
			currentGroup.RenderHint != "" ||
			changeHasHint

		if shouldStartNew {
			// Flush current group and start new
			if currentGroup != nil {
				groups = append(groups, currentGroup)
			}
			currentGroup = &Group{
				Type:      groupType,
				StartLine: lineNum,
				EndLine:   lineNum,
				Lines:     []string{change.Content},
			}
			if groupType == "modification" {
				currentGroup.OldLines = []string{change.OldContent}
			}
			// Set RenderHint for this single-line group
			setRenderHint(currentGroup, change)
		} else {
			// Extend current group (only happens when neither has a hint)
			currentGroup.EndLine = lineNum
			currentGroup.Lines = append(currentGroup.Lines, change.Content)
			if groupType == "modification" {
				currentGroup.OldLines = append(currentGroup.OldLines, change.OldContent)
			}
		}
	}

	// Flush final group
	if currentGroup != nil {
		groups = append(groups, currentGroup)
	}

	return groups
}

// setRenderHint sets the render hint for character-level optimizations
func setRenderHint(group *Group, change LineChange) {
	group.RenderHint = change.Type.RenderHint()
	if group.RenderHint != "" {
		group.ColStart = change.ColStart
		group.ColEnd = change.ColEnd
	} else {
		group.ColStart = 0
		group.ColEnd = 0
	}
}

// ValidateRenderHintsForCursor downgrades character-level hints to regular modification
// when the cursor would be hidden under the overlay. This switches to side-by-side
// rendering so the cursor remains visible.
//
// Both append_chars and replace_chars: downgrade when cursor is past the change start (ColStart < cursorCol)
func ValidateRenderHintsForCursor(groups []*Group, cursorRow, cursorCol int) {
	for _, g := range groups {
		if g.BufferLine != cursorRow {
			continue
		}
		switch g.RenderHint {
		case "append_chars":
			if g.ColStart < cursorCol {
				g.RenderHint = ""
			}
		case "replace_chars":
			if g.ColStart < cursorCol {
				g.RenderHint = ""
			}
		}
	}
}

// StageContext provides context for finalizing groups within a stage
type StageContext struct {
	BufferStart         int         // Stage's buffer start line (1-indexed)
	CursorRow           int         // Current cursor row (1-indexed)
	CursorCol           int         // Current cursor col (0-indexed)
	LineNumToBufferLine map[int]int // Pre-computed relative line -> buffer line
}

// FinalizeStageGroups creates groups, populates BufferLine for each group,
// validates render hints, and computes cursor position.
// Returns (groups, cursorLine, cursorCol).
func FinalizeStageGroups(changes map[int]LineChange, newLines []string, ctx *StageContext) ([]*Group, int, int) {
	groups := GroupChanges(changes)

	// Find modification positions for anchoring additions:
	// - lastModLine/lastModBufLine: for anchoring additions that come after
	// - cursorModLine/cursorModBufLine: for anchoring additions that precede the cursor
	lastModLine, lastModBufLine := 0, ctx.BufferStart
	cursorModLine, cursorModBufLine := 0, 0

	for relLine, change := range changes {
		if change.Type == ChangeModification || change.Type.IsCharacterLevel() {
			bufLine := ctx.LineNumToBufferLine[relLine]
			if bufLine == 0 {
				bufLine = ctx.BufferStart + relLine - 1
			}
			if relLine > lastModLine {
				lastModLine, lastModBufLine = relLine, bufLine
			}
			if bufLine == ctx.CursorRow {
				cursorModLine, cursorModBufLine = relLine, bufLine
			}
		}
	}

	// Set BufferLine for each group
	for _, g := range groups {
		if g.Type == "addition" && lastModLine > 0 && g.StartLine > lastModLine {
			// Addition after the last modification - render below
			g.BufferLine = lastModBufLine + 1
		} else if g.Type == "addition" && cursorModLine > 0 && g.StartLine < cursorModLine {
			// Addition before cursor line's modification - anchor at cursor line
			g.BufferLine = cursorModBufLine
		} else if bufLine := ctx.LineNumToBufferLine[g.StartLine]; bufLine > 0 {
			g.BufferLine = bufLine
		} else {
			g.BufferLine = ctx.BufferStart + g.StartLine - 1
		}
	}

	ValidateRenderHintsForCursor(groups, ctx.CursorRow, ctx.CursorCol)
	cursorLine, cursorCol := CalculateCursorPosition(changes, newLines)
	return groups, cursorLine, cursorCol
}

// CalculateCursorPosition computes optimal cursor position from changes
// Priority: modifications > additions > char-level > deletions
// Returns (line, col) where line is 1-indexed and col is 0-indexed
// Returns (-1, -1) if no cursor positioning is needed
func CalculateCursorPosition(changes map[int]LineChange, newLines []string) (int, int) {
	if len(changes) == 0 {
		return -1, -1
	}

	// Check if all changes are deletions (no cursor positioning)
	allDeletions := true
	for _, change := range changes {
		if change.Type != ChangeDeletion {
			allDeletions = false
			break
		}
	}
	if allDeletions {
		return -1, -1
	}

	// Find the best target line based on priority
	var lastModification, lastAddition, lastAppendChars, lastReplaceChars, lastDeleteChars int = -1, -1, -1, -1, -1

	for lineNum, change := range changes {
		switch change.Type {
		case ChangeModification:
			if lineNum > lastModification {
				lastModification = lineNum
			}
		case ChangeAddition:
			if lineNum > lastAddition {
				lastAddition = lineNum
			}
		case ChangeAppendChars:
			if lineNum > lastAppendChars {
				lastAppendChars = lineNum
			}
		case ChangeReplaceChars:
			if lineNum > lastReplaceChars {
				lastReplaceChars = lineNum
			}
		case ChangeDeleteChars:
			if lineNum > lastDeleteChars {
				lastDeleteChars = lineNum
			}
		}
	}

	// Priority order: modifications > additions > append/replace/delete chars
	var targetLine int = -1
	if lastModification != -1 {
		targetLine = lastModification
	} else if lastAddition != -1 {
		targetLine = lastAddition
	} else if lastAppendChars != -1 {
		targetLine = lastAppendChars
	} else if lastReplaceChars != -1 {
		targetLine = lastReplaceChars
	} else if lastDeleteChars != -1 {
		targetLine = lastDeleteChars
	}

	if targetLine <= 0 {
		return -1, -1
	}

	// Clamp to valid range
	if targetLine > len(newLines) {
		targetLine = len(newLines)
	}

	if targetLine <= 0 {
		return -1, -1
	}

	// For character-level changes, position at end of the actual change
	// For DeleteChars, use ColStart (deletion point) since ColEnd is in old coordinates
	// For AppendChars/ReplaceChars, use ColEnd (end of inserted text)
	// For full-line changes, position at end of line
	change, exists := changes[targetLine]
	if exists && change.Type.IsCharacterLevel() {
		if change.Type == ChangeDeleteChars {
			return targetLine, change.ColStart
		}
		return targetLine, change.ColEnd
	}

	col := len(newLines[targetLine-1])
	return targetLine, col
}
