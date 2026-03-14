package utils

import (
	"cursortab/assert"
	"cursortab/types"
	"testing"
)

func TestTrimContentAroundCursor_EmptyFile(t *testing.T) {
	lines := []string{}
	trimmed, cursorRow, cursorCol, offset, didTrim := TrimContentAroundCursor(lines, 0, 0, 100, nil)

	assert.Equal(t, 0, len(trimmed), "trimmed length")
	assert.Equal(t, 0, cursorRow, "cursorRow")
	assert.Equal(t, 0, cursorCol, "cursorCol")
	assert.Equal(t, 0, offset, "offset")
	assert.False(t, didTrim, "didTrim should be false")
}

func TestTrimContentAroundCursor_SmallFile(t *testing.T) {
	lines := []string{"line 1", "line 2", "line 3"}
	trimmed, cursorRow, cursorCol, offset, didTrim := TrimContentAroundCursor(lines, 1, 5, 1000, nil)

	// Small file should not be trimmed
	assert.Equal(t, 3, len(trimmed), "trimmed length")
	assert.Equal(t, 1, cursorRow, "cursorRow")
	assert.Equal(t, 5, cursorCol, "cursorCol")
	assert.Equal(t, 0, offset, "offset")
	assert.False(t, didTrim, "didTrim should be false")
}

func TestTrimContentAroundCursor_LargeFileTrims(t *testing.T) {
	// Create a large file
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = "this is line content that takes up space"
	}

	// Very small token limit forces trimming
	trimmed, cursorRow, _, _, didTrim := TrimContentAroundCursor(lines, 50, 0, 20, nil)

	assert.True(t, didTrim, "didTrim should be true")

	assert.True(t, len(trimmed) < 100, "trimmed length should be less than 100")

	// Cursor should be within trimmed lines
	assert.True(t, cursorRow >= 0 && cursorRow < len(trimmed), "cursorRow should be within trimmed range")
}

func TestTrimContentAroundCursor_CursorClamping(t *testing.T) {
	lines := []string{"line 1", "line 2", "line 3"}

	// Test cursor beyond file
	_, cursorRow, _, _, _ := TrimContentAroundCursor(lines, 100, 0, 1000, nil)
	assert.Equal(t, 2, cursorRow, "cursorRow clamped to last line")

	// Test negative cursor
	_, cursorRow, _, _, _ = TrimContentAroundCursor(lines, -5, 0, 1000, nil)
	assert.Equal(t, 0, cursorRow, "cursorRow clamped to first line")
}

func TestTrimContentAroundCursor_ZeroMaxTokens(t *testing.T) {
	lines := []string{"line 1", "line 2", "line 3"}
	trimmed, _, _, _, didTrim := TrimContentAroundCursor(lines, 1, 0, 0, nil)

	// maxTokens <= 0 should return content as-is
	assert.Equal(t, 3, len(trimmed), "trimmed length")
	assert.False(t, didTrim, "didTrim should be false")
}

func TestTrimContentAroundCursor_BalancedTrimming(t *testing.T) {
	// Create 50 lines
	lines := make([]string, 50)
	for i := range lines {
		lines[i] = "x" // Each line is 1 char + newline = 2 chars
	}

	// Cursor at line 25 (middle), budget for ~10 lines
	// Each line is 2 chars, so 20 tokens = 40 chars = ~20 lines
	_, _, _, _, didTrim := TrimContentAroundCursor(lines, 25, 0, 20, nil)

	assert.True(t, didTrim, "didTrim should be true")
}

func TestSnapToSyntaxBoundaries(t *testing.T) {
	// Simulate a Go file:
	//   0: package main
	//   1: (blank)
	//   2: func process() {
	//   3:     x := 1
	//   4:     for i := range items {
	//   5:         result = append(result, i)   <- cursor here
	//   6:     }
	//   7:     return result
	//   8: }
	//   9: (blank)
	//  10: func main() {
	//  11:     process()
	//  12: }
	lines := []string{
		"package main",
		"",
		"func process() {",
		"    x := 1",
		"    for i := range items {",
		"        result = append(result, i)",
		"    }",
		"    return result",
		"}",
		"",
		"func main() {",
		"    process()",
		"}",
	}

	// Syntax ranges (1-indexed, innermost to outermost):
	//   for loop body: lines 5-7
	//   func process:  lines 3-9
	//   file root:     lines 1-13
	syntaxRanges := []*types.LineRange{
		{StartLine: 5, EndLine: 7},
		{StartLine: 3, EndLine: 9},
		{StartLine: 1, EndLine: 13},
	}

	t.Run("snaps to for-loop boundary", func(t *testing.T) {
		// Start with a narrow region around cursor (line 5, 0-indexed)
		// Budget enough for the for-loop (lines 4-6)
		start, end := SnapToSyntaxBoundaries(lines, 5, 5, 200, syntaxRanges)
		// Should snap to at least the for-loop (0-indexed 4-6)
		assert.True(t, start <= 4, "start should include for-loop start")
		assert.True(t, end >= 6, "end should include for-loop end")
	})

	t.Run("snaps to function boundary when budget allows", func(t *testing.T) {
		// Budget enough for the function but not the whole file root
		// Function (lines 3-9) is ~130 chars; whole file is ~185 chars
		start, end := SnapToSyntaxBoundaries(lines, 5, 5, 150, syntaxRanges)
		assert.Equal(t, 2, start, "start should be func start (0-indexed)")
		assert.Equal(t, 8, end, "end should be func end (0-indexed)")
	})

	t.Run("stops at budget limit", func(t *testing.T) {
		// Very tight budget: only fits cursor line + a couple more
		start, end := SnapToSyntaxBoundaries(lines, 5, 5, 60, syntaxRanges)
		// for-loop is ~3 lines × ~30 chars = ~90 chars, won't fit in 60
		// Should stay at original
		assert.Equal(t, 5, start, "start unchanged")
		assert.Equal(t, 5, end, "end unchanged")
	})

	t.Run("no ranges is no-op", func(t *testing.T) {
		start, end := SnapToSyntaxBoundaries(lines, 5, 5, 500, nil)
		assert.Equal(t, 5, start, "start unchanged")
		assert.Equal(t, 5, end, "end unchanged")
	})
}

func TestTrimContentWithSyntaxRanges(t *testing.T) {
	// 20 lines of short content, cursor at line 10
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = "x" // 1 char + newline = 2 chars each
	}

	// Syntax range spanning lines 8-13 (1-indexed)
	ranges := []*types.LineRange{{StartLine: 8, EndLine: 13}}

	// Budget that forces trimming but can fit the syntax range
	_, _, _, offset, didTrim := TrimContentAroundCursor(lines, 10, 0, 10, ranges)

	assert.True(t, didTrim, "should have trimmed")
	// The window should include the syntax range boundaries
	assert.True(t, offset <= 7, "should include syntax range start (0-indexed 7)")
}

// Mock DiffEntry for testing TrimDiffEntries
type mockDiffEntry struct {
	original string
	updated  string
}

func (m *mockDiffEntry) GetOriginal() string { return m.original }
func (m *mockDiffEntry) GetUpdated() string  { return m.updated }

func TestTrimDiffEntries_EmptySlice(t *testing.T) {
	var diffs []*mockDiffEntry
	result := TrimDiffEntries(diffs, 100)

	assert.Equal(t, 0, len(result), "result length")
}

func TestTrimDiffEntries_ZeroMaxTokens(t *testing.T) {
	diffs := []*mockDiffEntry{
		{original: "old", updated: "new"},
	}
	result := TrimDiffEntries(diffs, 0)

	// Should return as-is when maxTokens <= 0
	assert.Equal(t, 1, len(result), "result length")
}

func TestTrimDiffEntries_FitsWithinLimit(t *testing.T) {
	diffs := []*mockDiffEntry{
		{original: "a", updated: "b"},
		{original: "c", updated: "d"},
	}

	// Each entry is ~2 chars, total ~4 chars = ~2 tokens
	result := TrimDiffEntries(diffs, 100)

	assert.Equal(t, 2, len(result), "result length")
}

func TestTrimDiffEntries_ExceedsLimit(t *testing.T) {
	diffs := []*mockDiffEntry{
		{original: "old1", updated: "new1"},
		{original: "old2", updated: "new2"},
		{original: "old3", updated: "new3"},
		{original: "old4", updated: "new4"},
	}

	// Very small limit - should keep only most recent
	result := TrimDiffEntries(diffs, 5)

	// Should keep only the most recent entries that fit
	assert.Less(t, len(result), 4, "result length")

	// Most recent entry should be last in the result
	if len(result) > 0 {
		assert.Equal(t, "old4", result[len(result)-1].original, "most recent entry")
	}
}

func TestTrimDiffEntries_KeepsMostRecent(t *testing.T) {
	diffs := []*mockDiffEntry{
		{original: "oldest", updated: "oldest_new"},
		{original: "middle", updated: "middle_new"},
		{original: "newest", updated: "newest_new"},
	}

	// Limit that allows only one or two entries
	result := TrimDiffEntries(diffs, 10)

	// Check that newest is included
	found := false
	for _, d := range result {
		if d.original == "newest" {
			found = true
			break
		}
	}
	if len(result) > 0 {
		assert.True(t, found, "newest entry should be included when space allows")
	}
}
