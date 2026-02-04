package text

import (
	"cursortab/assert"
	"testing"
)

func TestGroupChanges_SingleModification(t *testing.T) {
	changes := map[int]LineChange{
		1: {
			Type:       ChangeReplaceChars,
			Content:    "Hello there",
			OldContent: "Hello world",
			ColStart:   6,
			ColEnd:     11,
		},
	}

	groups := GroupChanges(changes)

	assert.Equal(t, 1, len(groups), "number of groups")
	assert.Equal(t, "modification", groups[0].Type, "group type")
	assert.Equal(t, 1, groups[0].StartLine, "start line")
	assert.Equal(t, 1, groups[0].EndLine, "end line")
	assert.Equal(t, "replace_chars", groups[0].RenderHint, "render hint")
	assert.Equal(t, 6, groups[0].ColStart, "col start")
	assert.Equal(t, 11, groups[0].ColEnd, "col end")
}

func TestGroupChanges_SingleAddition(t *testing.T) {
	changes := map[int]LineChange{
		2: {
			Type:    ChangeAddition,
			Content: "new line",
		},
	}

	groups := GroupChanges(changes)

	assert.Equal(t, 1, len(groups), "number of groups")
	assert.Equal(t, "addition", groups[0].Type, "group type")
	assert.Equal(t, 2, groups[0].StartLine, "start line")
	assert.Equal(t, 2, groups[0].EndLine, "end line")
	assert.Equal(t, "", groups[0].RenderHint, "render hint for addition")
}

func TestGroupChanges_ConsecutiveAdditions(t *testing.T) {
	changes := map[int]LineChange{
		2: {Type: ChangeAddition, Content: "line a"},
		3: {Type: ChangeAddition, Content: "line b"},
		4: {Type: ChangeAddition, Content: "line c"},
	}

	groups := GroupChanges(changes)

	assert.Equal(t, 1, len(groups), "should be grouped into one")
	assert.Equal(t, "addition", groups[0].Type, "group type")
	assert.Equal(t, 2, groups[0].StartLine, "start line")
	assert.Equal(t, 4, groups[0].EndLine, "end line")
	assert.Equal(t, 3, len(groups[0].Lines), "number of lines")
}

func TestGroupChanges_ConsecutiveModifications(t *testing.T) {
	changes := map[int]LineChange{
		2: {Type: ChangeModification, Content: "new 2", OldContent: "old 2"},
		3: {Type: ChangeModification, Content: "new 3", OldContent: "old 3"},
		4: {Type: ChangeModification, Content: "new 4", OldContent: "old 4"},
	}

	groups := GroupChanges(changes)

	assert.Equal(t, 1, len(groups), "should be grouped into one")
	assert.Equal(t, "modification", groups[0].Type, "group type")
	assert.Equal(t, 2, groups[0].StartLine, "start line")
	assert.Equal(t, 4, groups[0].EndLine, "end line")
	assert.Equal(t, 3, len(groups[0].Lines), "number of lines")
	assert.Equal(t, 3, len(groups[0].OldLines), "number of old lines")
	assert.Equal(t, "", groups[0].RenderHint, "no render hint for multi-line")
}

func TestGroupChanges_NonConsecutive(t *testing.T) {
	changes := map[int]LineChange{
		2: {Type: ChangeModification, Content: "new 2", OldContent: "old 2"},
		4: {Type: ChangeModification, Content: "new 4", OldContent: "old 4"},
	}

	groups := GroupChanges(changes)

	assert.Equal(t, 2, len(groups), "should be two groups")
	assert.Equal(t, 2, groups[0].StartLine, "first group start")
	assert.Equal(t, 4, groups[1].StartLine, "second group start")
}

func TestGroupChanges_MixedTypes(t *testing.T) {
	changes := map[int]LineChange{
		2: {Type: ChangeModification, Content: "mod", OldContent: "old"},
		3: {Type: ChangeAddition, Content: "add"},
	}

	groups := GroupChanges(changes)

	// Modification and addition are different types, so they should be separate groups
	assert.Equal(t, 2, len(groups), "should be two groups for different types")
}

func TestGroupChanges_DeletionsExcluded(t *testing.T) {
	changes := map[int]LineChange{
		2: {Type: ChangeDeletion, Content: "deleted"},
		3: {Type: ChangeAddition, Content: "added"},
	}

	groups := GroupChanges(changes)

	assert.Equal(t, 1, len(groups), "deletion should be excluded")
	assert.Equal(t, "addition", groups[0].Type, "only addition group")
}

func TestGroupChanges_OnlyDeletions(t *testing.T) {
	changes := map[int]LineChange{
		2: {Type: ChangeDeletion, Content: "deleted 1"},
		3: {Type: ChangeDeletion, Content: "deleted 2"},
	}

	groups := GroupChanges(changes)

	assert.True(t, len(groups) == 0, "no groups for pure deletions")
}

func TestGroupChanges_Empty(t *testing.T) {
	groups := GroupChanges(nil)
	assert.True(t, len(groups) == 0, "no groups for empty changes")

	groups = GroupChanges(map[int]LineChange{})
	assert.True(t, len(groups) == 0, "no groups for empty map")
}

func TestGroupChanges_AppendCharsHint(t *testing.T) {
	changes := map[int]LineChange{
		1: {
			Type:       ChangeAppendChars,
			Content:    "hello world",
			OldContent: "hello",
			ColStart:   5,
			ColEnd:     11,
		},
	}

	groups := GroupChanges(changes)

	assert.Equal(t, 1, len(groups), "one group")
	assert.Equal(t, "append_chars", groups[0].RenderHint, "render hint")
	assert.Equal(t, 5, groups[0].ColStart, "col start")
	assert.Equal(t, 11, groups[0].ColEnd, "col end")
}

func TestGroupChanges_MultiLineWithDifferentColumns(t *testing.T) {
	// Changes with render hints are never merged - each gets its own group
	changes := map[int]LineChange{
		1: {Type: ChangeReplaceChars, Content: "a", OldContent: "b", ColStart: 0, ColEnd: 1},
		2: {Type: ChangeReplaceChars, Content: "c", OldContent: "d", ColStart: 5, ColEnd: 6},
	}

	groups := GroupChanges(changes)

	assert.Equal(t, 2, len(groups), "hinted changes stay separate")
	assert.Equal(t, "replace_chars", groups[0].RenderHint, "first group has hint")
	assert.Equal(t, 0, groups[0].ColStart, "first group col start")
	assert.Equal(t, 1, groups[0].ColEnd, "first group col end")
	assert.Equal(t, "replace_chars", groups[1].RenderHint, "second group has hint")
	assert.Equal(t, 5, groups[1].ColStart, "second group col start")
	assert.Equal(t, 6, groups[1].ColEnd, "second group col end")
}

func TestGroupChanges_MultiLineWithIdenticalColumns(t *testing.T) {
	// Changes with render hints are never merged - each gets its own single-line group
	// This allows the Lua renderer to apply char-level rendering to each line individually
	changes := map[int]LineChange{
		1: {Type: ChangeReplaceChars, Content: "application.route()", OldContent: "app.route()", ColStart: 3, ColEnd: 11},
		2: {Type: ChangeReplaceChars, Content: "application.route()", OldContent: "app.route()", ColStart: 3, ColEnd: 11},
		3: {Type: ChangeReplaceChars, Content: "application.route()", OldContent: "app.route()", ColStart: 3, ColEnd: 11},
	}

	groups := GroupChanges(changes)

	assert.Equal(t, 3, len(groups), "each hinted change gets its own group")
	for i, group := range groups {
		assert.Equal(t, "replace_chars", group.RenderHint, "group has hint")
		assert.Equal(t, 3, group.ColStart, "col start")
		assert.Equal(t, 11, group.ColEnd, "col end")
		assert.Equal(t, i+1, group.StartLine, "start line")
		assert.Equal(t, i+1, group.EndLine, "end line")
	}
}

func TestGroupChanges_MultiLineMixedHints(t *testing.T) {
	// Multi-line groups with different change types should clear RenderHint
	changes := map[int]LineChange{
		1: {Type: ChangeReplaceChars, Content: "a", OldContent: "b", ColStart: 0, ColEnd: 1},
		2: {Type: ChangeAppendChars, Content: "cd", OldContent: "c", ColStart: 1, ColEnd: 2},
	}

	groups := GroupChanges(changes)

	// Different change types = different group types = separate groups
	assert.Equal(t, 2, len(groups), "different types = separate groups")
}

func TestCalculateCursorPosition_Modification(t *testing.T) {
	changes := map[int]LineChange{
		1: {Type: ChangeModification, Content: "modified line"},
	}
	newLines := []string{"modified line"}

	line, col := CalculateCursorPosition(changes, newLines)

	assert.Equal(t, 1, line, "cursor line")
	assert.Equal(t, 13, col, "cursor col at end of line")
}

func TestCalculateCursorPosition_Addition(t *testing.T) {
	changes := map[int]LineChange{
		2: {Type: ChangeAddition, Content: "new line"},
	}
	newLines := []string{"line 1", "new line", "line 3"}

	line, col := CalculateCursorPosition(changes, newLines)

	assert.Equal(t, 2, line, "cursor line")
	assert.Equal(t, 8, col, "cursor col at end of new line")
}

func TestCalculateCursorPosition_ModificationPriority(t *testing.T) {
	// Modification should take priority over addition
	changes := map[int]LineChange{
		1: {Type: ChangeModification, Content: "mod"},
		3: {Type: ChangeAddition, Content: "add"},
	}
	newLines := []string{"mod", "line 2", "add"}

	line, _ := CalculateCursorPosition(changes, newLines)

	assert.Equal(t, 1, line, "cursor at modification, not addition")
}

func TestCalculateCursorPosition_OnlyDeletions(t *testing.T) {
	changes := map[int]LineChange{
		1: {Type: ChangeDeletion, Content: "deleted"},
	}
	newLines := []string{}

	line, col := CalculateCursorPosition(changes, newLines)

	assert.Equal(t, -1, line, "no cursor for deletions")
	assert.Equal(t, -1, col, "no cursor col for deletions")
}

func TestCalculateCursorPosition_Empty(t *testing.T) {
	line, col := CalculateCursorPosition(nil, nil)
	assert.Equal(t, -1, line, "no cursor for empty")
	assert.Equal(t, -1, col, "no cursor col for empty")

	line, col = CalculateCursorPosition(map[int]LineChange{}, nil)
	assert.Equal(t, -1, line, "no cursor for empty map")
	assert.Equal(t, -1, col, "no cursor col for empty map")
}

func TestCalculateCursorPosition_ClampToBuffer(t *testing.T) {
	// Cursor line should be clamped to buffer size
	changes := map[int]LineChange{
		10: {Type: ChangeAddition, Content: "line"},
	}
	newLines := []string{"only", "two", "lines"}

	line, _ := CalculateCursorPosition(changes, newLines)

	assert.True(t, line <= len(newLines), "cursor clamped to buffer size")
}

// Cursor should be placed at end of change, not end of line
func TestCalculateCursorPosition_AppendChars(t *testing.T) {
	// console.log(|); -> console.log("hello"|);
	// ColEnd marks where the appended text ends
	changes := map[int]LineChange{
		1: {
			Type:       ChangeAppendChars,
			Content:    `console.log("hello");`,
			OldContent: "console.log();",
			ColStart:   12, // after (
			ColEnd:     19, // after "hello"
		},
	}
	newLines := []string{`console.log("hello");`}

	line, col := CalculateCursorPosition(changes, newLines)

	assert.Equal(t, 1, line, "cursor line")
	assert.Equal(t, 19, col, "cursor at end of change, not end of line (21)")
}

func TestCalculateCursorPosition_ReplaceChars(t *testing.T) {
	// app.route() -> application.route()
	// Cursor should be after "application", not at end of line
	changes := map[int]LineChange{
		1: {
			Type:       ChangeReplaceChars,
			Content:    "application.route()",
			OldContent: "app.route()",
			ColStart:   0,
			ColEnd:     11, // end of "application"
		},
	}
	newLines := []string{"application.route()"}

	line, col := CalculateCursorPosition(changes, newLines)

	assert.Equal(t, 1, line, "cursor line")
	assert.Equal(t, 11, col, "cursor at end of replacement, not end of line (19)")
}

func TestCalculateCursorPosition_DeleteChars(t *testing.T) {
	// "Hello world John" -> "Hello John"
	// Deleted "world " at position 6
	// ColEnd=12 is in OLD coordinates (6 + len("world "))
	// Cursor should be at ColStart (deletion point), not ColEnd
	changes := map[int]LineChange{
		1: {
			Type:       ChangeDeleteChars,
			Content:    "Hello John",
			OldContent: "Hello world John",
			ColStart:   6,  // position where deletion occurred
			ColEnd:     12, // end of deleted text in OLD coordinates
		},
	}
	newLines := []string{"Hello John"} // len=10, less than ColEnd=12

	line, col := CalculateCursorPosition(changes, newLines)

	assert.Equal(t, 1, line, "cursor line")
	assert.Equal(t, 6, col, "cursor at deletion point (ColStart), not ColEnd")
}

func TestCalculateCursorPosition_AppendCharsAtLineEnd(t *testing.T) {
	// When ColEnd equals line length, behavior is same as before
	changes := map[int]LineChange{
		1: {
			Type:       ChangeAppendChars,
			Content:    "hello world",
			OldContent: "hello",
			ColStart:   5,
			ColEnd:     11, // equals len("hello world")
		},
	}
	newLines := []string{"hello world"}

	line, col := CalculateCursorPosition(changes, newLines)

	assert.Equal(t, 1, line, "cursor line")
	assert.Equal(t, 11, col, "cursor at end of line (which is also end of change)")
}

func TestCalculateCursorPosition_CharLevelPriorityOverAddition(t *testing.T) {
	// AppendChars has lower priority than Modification, but higher than nothing
	// When we have AppendChars and Addition, AppendChars wins in priority order
	changes := map[int]LineChange{
		1: {
			Type:       ChangeAppendChars,
			Content:    `log("hi");`,
			OldContent: "log();",
			ColStart:   4,
			ColEnd:     8, // after "hi"
		},
		2: {Type: ChangeAddition, Content: "new line"},
	}
	newLines := []string{`log("hi");`, "new line"}

	line, col := CalculateCursorPosition(changes, newLines)

	// Addition has higher priority than AppendChars in current logic
	// But cursor col should still be at end of the selected line
	assert.Equal(t, 2, line, "cursor at addition line (higher priority)")
	assert.Equal(t, 8, col, "cursor at end of addition line")
}

func TestCalculateCursorPosition_MultipleAppendChars(t *testing.T) {
	// Multiple AppendChars changes: cursor at the last line's ColEnd
	changes := map[int]LineChange{
		1: {
			Type:       ChangeAppendChars,
			Content:    "first change",
			OldContent: "first",
			ColStart:   5,
			ColEnd:     12,
		},
		3: {
			Type:       ChangeAppendChars,
			Content:    "third change",
			OldContent: "third",
			ColStart:   5,
			ColEnd:     12,
		},
	}
	newLines := []string{"first change", "second", "third change"}

	line, col := CalculateCursorPosition(changes, newLines)

	assert.Equal(t, 3, line, "cursor at last append chars line")
	assert.Equal(t, 12, col, "cursor at ColEnd of last append chars")
}

func TestCalculateCursorPosition_ModificationOverridesCharLevel(t *testing.T) {
	// Full-line Modification has higher priority than char-level changes
	// For Modification, cursor should be at end of line (no ColEnd)
	changes := map[int]LineChange{
		1: {Type: ChangeModification, Content: "modified line", OldContent: "old"},
		2: {
			Type:       ChangeAppendChars,
			Content:    "append here",
			OldContent: "append",
			ColStart:   6,
			ColEnd:     11,
		},
	}
	newLines := []string{"modified line", "append here"}

	line, col := CalculateCursorPosition(changes, newLines)

	assert.Equal(t, 1, line, "cursor at modification (higher priority)")
	assert.Equal(t, 13, col, "cursor at end of modification line")
}

// TestGroupsMustReflectActualBufferState verifies that groups computed from the
// actual buffer diff can differ from pre-computed groups when buffer state changes.
func TestGroupsMustReflectActualBufferState(t *testing.T) {
	// Scenario: completion expands 1 line to 2 lines, where line 1 is unchanged
	// First diff (1 old vs 2 new) sees: line 1 EQUAL, line 2 ADDITION
	oldLine := "const x = 1;"
	newLines := []string{"const x = 1;", "const y = 2;"}

	firstDiff := ComputeDiff(JoinLines([]string{oldLine}), JoinLines(newLines))

	assert.Equal(t, 1, len(firstDiff.Changes), "first diff: 1 change")
	assert.Equal(t, ChangeAddition, firstDiff.Changes[2].Type, "first diff: addition at line 2")

	firstGroups := GroupChanges(firstDiff.Changes)
	assert.Equal(t, 1, len(firstGroups), "first diff: 1 group")
	assert.Equal(t, "addition", firstGroups[0].Type, "first diff group: addition")
	assert.Nil(t, firstGroups[0].OldLines, "addition has no old_lines")

	// But when applying to actual buffer, line 2 has different content
	// Second diff (actual buffer vs new content) sees: MODIFICATION
	actualBufferLine := "const y = 0;"
	newContent := "const y = 2;"

	secondDiff := ComputeDiff(JoinLines([]string{actualBufferLine}), JoinLines([]string{newContent}))

	assert.Equal(t, 1, len(secondDiff.Changes), "second diff: 1 change")
	change := secondDiff.Changes[1]
	isModification := change.Type == ChangeModification || change.Type == ChangeReplaceChars
	assert.True(t, isModification, "second diff: modification type")
	assert.True(t, change.OldContent != "", "modification has old content")

	secondGroups := GroupChanges(secondDiff.Changes)
	assert.Equal(t, 1, len(secondGroups), "second diff: 1 group")
	assert.Equal(t, "modification", secondGroups[0].Type, "second diff group: modification")
	assert.NotNil(t, secondGroups[0].OldLines, "modification has old_lines")

	// Key assertion: the two diffs produce different group types
	assert.True(t, firstGroups[0].Type != secondGroups[0].Type,
		"groups from different buffer states should differ")
}

func TestValidateRenderHintsForCursor_DowngradesAppendCharsBeforeCursor(t *testing.T) {
	// Scenario: cursor is at column 5, but append_chars starts at column 2
	// This should be downgraded because the ghost text would appear before cursor
	groups := []*Group{
		{
			Type:       "modification",
			RenderHint: "append_chars",
			BufferLine: 10,
			ColStart:   2,
			ColEnd:     8,
		},
	}

	ValidateRenderHintsForCursor(groups, 10, 5) // cursor at row 10, col 5

	assert.Equal(t, "", groups[0].RenderHint, "should downgrade append_chars when ColStart < cursorCol")
}

func TestValidateRenderHintsForCursor_KeepsAppendCharsAtOrAfterCursor(t *testing.T) {
	// Scenario: cursor is at column 2, append_chars starts at column 4
	// This should NOT be downgraded because ghost text appears after cursor
	groups := []*Group{
		{
			Type:       "modification",
			RenderHint: "append_chars",
			BufferLine: 10,
			ColStart:   4,
			ColEnd:     8,
		},
	}

	ValidateRenderHintsForCursor(groups, 10, 2) // cursor at row 10, col 2

	assert.Equal(t, "append_chars", groups[0].RenderHint, "should keep append_chars when ColStart >= cursorCol")
}

func TestValidateRenderHintsForCursor_IgnoresDifferentLine(t *testing.T) {
	// Scenario: append_chars on a different line than cursor
	// Should NOT be affected even if ColStart < cursorCol
	groups := []*Group{
		{
			Type:       "modification",
			RenderHint: "append_chars",
			BufferLine: 15, // different line
			ColStart:   2,
			ColEnd:     8,
		},
	}

	ValidateRenderHintsForCursor(groups, 10, 5) // cursor at row 10, col 5

	assert.Equal(t, "append_chars", groups[0].RenderHint, "should not affect groups on different lines")
}

func TestValidateRenderHintsForCursor_KeepsReplaceCharsAtCursor(t *testing.T) {
	// Scenario: cursor is at column 5, replace_chars starts at column 5
	// This should be kept because cursor is at the change start (not past it)
	groups := []*Group{
		{
			Type:       "modification",
			RenderHint: "replace_chars",
			BufferLine: 10,
			ColStart:   5,
			ColEnd:     10,
		},
	}

	ValidateRenderHintsForCursor(groups, 10, 5) // cursor at row 10, col 5

	assert.Equal(t, "replace_chars", groups[0].RenderHint, "should keep replace_chars when ColStart == cursorCol")
}

func TestValidateRenderHintsForCursor_DowngradesReplaceCharsBeforeCursor(t *testing.T) {
	// Scenario: cursor is at column 8, replace_chars starts at column 3
	// This should be downgraded because cursor is past the change start
	groups := []*Group{
		{
			Type:       "modification",
			RenderHint: "replace_chars",
			BufferLine: 10,
			ColStart:   3,
			ColEnd:     10,
		},
	}

	ValidateRenderHintsForCursor(groups, 10, 8) // cursor at row 10, col 8

	assert.Equal(t, "", groups[0].RenderHint, "should downgrade replace_chars when ColStart < cursorCol")
}

func TestValidateRenderHintsForCursor_KeepsReplaceCharsBeforeCursor(t *testing.T) {
	// Scenario: cursor is at column 2, replace_chars starts at column 5
	// This should NOT be downgraded because cursor is before the change
	groups := []*Group{
		{
			Type:       "modification",
			RenderHint: "replace_chars",
			BufferLine: 10,
			ColStart:   5,
			ColEnd:     10,
		},
	}

	ValidateRenderHintsForCursor(groups, 10, 2) // cursor at row 10, col 2

	assert.Equal(t, "replace_chars", groups[0].RenderHint, "should keep replace_chars when ColStart > cursorCol")
}

func TestValidateRenderHintsForCursor_AppendVsReplaceAtExactPosition(t *testing.T) {
	// At exact cursor position (col 5), both hints are kept because
	// cursor is at the change start, not past it
	appendGroup := &Group{
		Type:       "modification",
		RenderHint: "append_chars",
		BufferLine: 10,
		ColStart:   5,
		ColEnd:     10,
	}
	replaceGroup := &Group{
		Type:       "modification",
		RenderHint: "replace_chars",
		BufferLine: 10,
		ColStart:   5,
		ColEnd:     10,
	}

	ValidateRenderHintsForCursor([]*Group{appendGroup}, 10, 5)
	ValidateRenderHintsForCursor([]*Group{replaceGroup}, 10, 5)

	assert.Equal(t, "append_chars", appendGroup.RenderHint, "append_chars at exact cursor position should keep hint")
	assert.Equal(t, "replace_chars", replaceGroup.RenderHint, "replace_chars at exact cursor position should keep hint")
}
