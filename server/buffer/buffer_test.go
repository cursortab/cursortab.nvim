package buffer

import (
	"cursortab/assert"
	"cursortab/text"
	"cursortab/types"
	"testing"
)

func TestCommitUserEdits_NoChanges(t *testing.T) {
	buf := New(Config{NsID: 1})
	buf.lines = []string{"line 1", "line 2"}
	buf.originalLines = []string{"line 1", "line 2"}

	result := buf.CommitUserEdits()

	assert.False(t, result, "should return false when no changes")
	assert.Equal(t, 0, len(buf.diffHistories), "no diffs committed")
}

func TestCommitUserEdits_WithChanges(t *testing.T) {
	buf := New(Config{NsID: 1})
	buf.lines = []string{"line 1", "modified line 2"}
	buf.originalLines = []string{"line 1", "line 2"}

	result := buf.CommitUserEdits()

	assert.True(t, result, "should return true when changes exist")
	assert.True(t, len(buf.diffHistories) > 0, "diffs committed")
	// Check checkpoint reset
	assert.Equal(t, buf.lines[1], buf.originalLines[1], "originalLines reset to current")
}

func TestCommitUserEdits_PreventsDuplicates(t *testing.T) {
	buf := New(Config{NsID: 1})
	buf.lines = []string{"line 1", "modified"}
	buf.originalLines = []string{"line 1", "original"}

	// First commit
	result1 := buf.CommitUserEdits()
	assert.True(t, result1, "first commit should succeed")
	diffCount := len(buf.diffHistories)

	// Second commit with no new changes
	result2 := buf.CommitUserEdits()
	assert.False(t, result2, "second commit should return false")
	assert.Equal(t, diffCount, len(buf.diffHistories), "no new diffs added")
}

func TestCommitUserEdits_LineCountChange(t *testing.T) {
	buf := New(Config{NsID: 1})
	buf.lines = []string{"line 1", "line 2", "line 3"} // Added a line
	buf.originalLines = []string{"line 1", "line 2"}

	result := buf.CommitUserEdits()

	assert.True(t, result, "should detect line count changes")
	assert.True(t, len(buf.diffHistories) > 0, "diffs committed")
}

func TestCommitUserEdits_UpdatesPreviousLines(t *testing.T) {
	buf := New(Config{NsID: 1})
	buf.lines = []string{"new content"}
	buf.originalLines = []string{"old content"}

	buf.CommitUserEdits()

	// previousLines should be set to what originalLines was BEFORE the commit
	assert.Equal(t, "old content", buf.previousLines[0], "previousLines set to old checkpoint")
}

// --- HasChanges Tests ---

func TestHasChanges_NoChanges(t *testing.T) {
	buf := New(Config{NsID: 1})
	buf.lines = []string{"line 1", "line 2", "line 3"}

	// Replacing with identical content
	result := buf.HasChanges(1, 3, []string{"line 1", "line 2", "line 3"})

	assert.False(t, result, "should return false for identical content")
}

func TestHasChanges_ContentDiffers(t *testing.T) {
	buf := New(Config{NsID: 1})
	buf.lines = []string{"line 1", "line 2", "line 3"}

	// Replacing with different content
	result := buf.HasChanges(1, 3, []string{"line 1", "modified", "line 3"})

	assert.True(t, result, "should return true when content differs")
}

func TestHasChanges_LineCountIncrease(t *testing.T) {
	buf := New(Config{NsID: 1})
	buf.lines = []string{"line 1", "line 2"}

	// Replacing 2 lines with 3 lines
	result := buf.HasChanges(1, 2, []string{"line 1", "line 2", "new line"})

	assert.True(t, result, "should return true when line count increases")
}

func TestHasChanges_LineCountDecrease(t *testing.T) {
	buf := New(Config{NsID: 1})
	buf.lines = []string{"line 1", "line 2", "line 3"}

	// Replacing 3 lines with 2 lines
	result := buf.HasChanges(1, 3, []string{"line 1", "line 2"})

	assert.True(t, result, "should return true when line count decreases")
}

func TestHasChanges_PartialRange(t *testing.T) {
	buf := New(Config{NsID: 1})
	buf.lines = []string{"line 1", "line 2", "line 3", "line 4"}

	// Replacing only middle lines
	result := buf.HasChanges(2, 3, []string{"modified 2", "modified 3"})

	assert.True(t, result, "should return true when middle lines change")
}

// --- SetFileContext Tests ---

func TestSetFileContext_WithAllValues(t *testing.T) {
	buf := New(Config{NsID: 1})
	prev := []string{"prev 1", "prev 2"}
	orig := []string{"orig 1", "orig 2"}
	disk := []string{"disk 1", "disk 2"}
	diffs := []*types.DiffEntry{{Original: "a", Updated: "b"}}

	buf.SetFileContext(FileContext{
		PreviousLines: prev,
		OriginalLines: orig,
		DiskLines:     disk,
		DiffHistories: diffs,
	})

	assert.Equal(t, 2, len(buf.previousLines), "previousLines set")
	assert.Equal(t, 2, len(buf.originalLines), "originalLines set")
	assert.Equal(t, 2, len(buf.diskLines), "diskLines set")
	assert.Equal(t, "disk 1", buf.diskLines[0], "diskLines content")
	assert.Equal(t, 1, len(buf.diffHistories), "diffHistories set")
}

func TestSetFileContext_NewFile(t *testing.T) {
	buf := New(Config{NsID: 1})
	lines := []string{"line 1", "line 2"}

	buf.SetFileContext(FileContext{
		PreviousLines: lines,
		OriginalLines: lines,
		DiskLines:     lines,
	})

	assert.Equal(t, 2, len(buf.previousLines), "previousLines set")
	assert.Equal(t, "line 1", buf.previousLines[0], "previousLines content")
	assert.Equal(t, 2, len(buf.diskLines), "diskLines set")
}

func TestSetFileContext_Empty(t *testing.T) {
	buf := New(Config{NsID: 1})
	buf.previousLines = []string{"should be cleared"}

	buf.SetFileContext(FileContext{})

	assert.Nil(t, buf.previousLines, "previousLines should be nil")
	assert.Equal(t, 0, len(buf.diffHistories), "diffHistories empty")
}

// --- extractGranularDiffs Tests ---

func TestExtractGranularDiffs_NoChanges(t *testing.T) {
	oldLines := []string{"line 1", "line 2"}
	newLines := []string{"line 1", "line 2"}

	result := extractGranularDiffs(oldLines, newLines, 1)

	assert.Nil(t, result, "no diffs for identical content")
}

func TestExtractGranularDiffs_Modification(t *testing.T) {
	oldLines := []string{"line 1", "original"}
	newLines := []string{"line 1", "modified"}

	result := extractGranularDiffs(oldLines, newLines, 1)

	assert.True(t, len(result) > 0, "should have diffs")
	// Should capture the change from "original" to "modified"
	found := false
	for _, diff := range result {
		if diff.Original == "original" && diff.Updated == "modified" {
			found = true
			break
		}
	}
	assert.True(t, found, "should capture modification")
}

func TestExtractGranularDiffs_Addition(t *testing.T) {
	oldLines := []string{"line 1"}
	newLines := []string{"line 1", "new line"}

	result := extractGranularDiffs(oldLines, newLines, 1)

	assert.True(t, len(result) > 0, "should have diffs for addition")
}

func TestExtractGranularDiffs_Deletion(t *testing.T) {
	oldLines := []string{"line 1", "line 2"}
	newLines := []string{"line 1"}

	result := extractGranularDiffs(oldLines, newLines, 1)

	assert.True(t, len(result) > 0, "should have diffs for deletion")
}

func TestExtractGranularDiffs_TracksStartLine(t *testing.T) {
	oldLines := []string{"line 1", "original", "line 3"}
	newLines := []string{"line 1", "modified", "line 3"}

	result := extractGranularDiffs(oldLines, newLines, 10)

	assert.True(t, len(result) > 0, "should have diffs")
	assert.Equal(t, 11, result[0].StartLine, "change at line 2 with base 10 = line 11")
}

func TestStampEntries(t *testing.T) {
	entries := []*types.DiffEntry{
		{Original: "a", Updated: "b", StartLine: 1},
		{Original: "c", Updated: "d", StartLine: 5},
	}

	stampEntries(entries, types.DiffSourcePredicted, 42)

	assert.Equal(t, types.DiffSourcePredicted, entries[0].Source, "source stamped")
	assert.Equal(t, int64(42), entries[0].TimestampNs, "timestamp stamped")
	assert.Equal(t, types.DiffSourcePredicted, entries[1].Source, "source stamped")
	assert.Equal(t, int64(42), entries[1].TimestampNs, "timestamp stamped")
}

func TestMakeRelativeToWorkspace(t *testing.T) {
	tests := []struct {
		name          string
		absolutePath  string
		workspacePath string
		want          string
	}{
		{
			name:          "file in workspace",
			absolutePath:  "/home/user/project/src/main.go",
			workspacePath: "/home/user/project",
			want:          "src/main.go",
		},
		{
			name:          "file outside workspace",
			absolutePath:  "/other/path/file.go",
			workspacePath: "/home/user/project",
			want:          "/other/path/file.go",
		},
		{
			name:          "file at workspace root",
			absolutePath:  "/home/user/project/main.go",
			workspacePath: "/home/user/project",
			want:          "main.go",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := makeRelativeToWorkspace(tt.absolutePath, tt.workspacePath)
			assert.Equal(t, tt.want, got, "relative path mismatch")
		})
	}
}

// --- CommitPending Tests ---

func TestCommitPending_PureInsertion(t *testing.T) {
	buf := New(Config{NsID: 1})
	buf.lines = []string{
		"// 1: Bandpass filter",
		"// 2: Differentiation",
		"// 3: Derivative computation",
		"// 4: Squaring and integration",
		"// 5: Peak detection",
	}
	buf.originalLines = make([]string, len(buf.lines))
	copy(buf.originalLines, buf.lines)

	// Pure insertion at line 4: insert 2 new lines BEFORE "// 4: Squaring..."
	// For pure insertions, EndLineInclusive = StartLine - 1 (no replacement)
	buf.pending = &PendingEdit{
		StartLine:        4,
		EndLineInclusive: 3, // startLine - 1: means insert, don't replace
		Lines:            []string{"double derivative[n_samples - 1];", "ecg_derivative(signal, derivative, n_samples);"},
	}

	buf.CommitPending()

	// The existing line "// 4: Squaring and integration" must be preserved
	assert.Equal(t, 7, len(buf.lines), "should have 7 lines (5 original + 2 inserted)")
	assert.Equal(t, "// 3: Derivative computation", buf.lines[2], "line 3 unchanged")
	assert.Equal(t, "double derivative[n_samples - 1];", buf.lines[3], "inserted line 1")
	assert.Equal(t, "ecg_derivative(signal, derivative, n_samples);", buf.lines[4], "inserted line 2")
	assert.Equal(t, "// 4: Squaring and integration", buf.lines[5], "original line 4 preserved")
	assert.Equal(t, "// 5: Peak detection", buf.lines[6], "original line 5 preserved")
}

func TestCommitPending_Replacement(t *testing.T) {
	buf := New(Config{NsID: 1})
	buf.lines = []string{"line 1", "old line 2", "line 3"}
	buf.originalLines = make([]string, len(buf.lines))
	copy(buf.originalLines, buf.lines)

	buf.pending = &PendingEdit{
		StartLine:        2,
		EndLineInclusive: 2,
		Lines:            []string{"new line 2"},
	}

	buf.CommitPending()

	assert.Equal(t, 3, len(buf.lines), "should still have 3 lines")
	assert.Equal(t, "new line 2", buf.lines[1], "line 2 replaced")
}

func TestComputeReplaceEnd(t *testing.T) {
	additionGroups := []*text.Group{
		{Type: "addition", StartLine: 1, EndLine: 2, BufferLine: 6},
		{Type: "addition", StartLine: 5, EndLine: 5, BufferLine: 8},
	}
	modificationGroups := []*text.Group{
		{Type: "modification", StartLine: 1, EndLine: 1, BufferLine: 6},
	}

	// Pure insertion: all additions, single old line, line count matches groups
	pureLines := []string{"a", "b", "c"} // 3 lines = group total (2+1)
	assert.Equal(t, 5, computeReplaceEnd(6, 6, pureLines, additionGroups), "single line additions → insert")

	// All additions but spanning multiple old lines → must replace
	assert.Equal(t, 7, computeReplaceEnd(6, 7, pureLines, additionGroups), "multi-line additions → replace")

	// All additions, single old line, but lines exceed group count → replace
	extraLines := []string{"a", "b", "c", "d"} // 4 lines > group total (3)
	assert.Equal(t, 6, computeReplaceEnd(6, 6, extraLines, additionGroups), "additions with absorbed old lines → replace")

	// Modification groups → replace
	modLines := []string{"a"}
	assert.Equal(t, 6, computeReplaceEnd(6, 6, modLines, modificationGroups), "modification → replace")
}

// --- isPureInsertion Tests ---

func TestIsPureInsertion(t *testing.T) {
	assert.True(t, isPureInsertion([]*text.Group{
		{Type: "addition"},
		{Type: "addition"},
	}), "all addition groups")

	assert.False(t, isPureInsertion([]*text.Group{
		{Type: "modification"},
	}), "modification group")

	assert.False(t, isPureInsertion([]*text.Group{
		{Type: "addition"},
		{Type: "modification"},
	}), "mixed groups")

	assert.False(t, isPureInsertion([]*text.Group{}), "empty groups")
	assert.False(t, isPureInsertion(nil), "nil groups")
}
