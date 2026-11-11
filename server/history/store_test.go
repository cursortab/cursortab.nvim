package history

import (
	"testing"

	"cursortab/assert"
	"cursortab/types"
)

func TestCommitUserEditsRecordsChangesOnce(t *testing.T) {
	s := NewStore(3)
	s.Open("a.go", []string{"line 1", "line 2"}, 1)

	assert.False(t, s.CommitUserEdits("a.go", []string{"line 1", "line 2"}, 2), "unchanged content")
	assert.Equal(t, 0, len(s.Diffs("a.go")), "no diffs for unchanged content")

	assert.True(t, s.CommitUserEdits("a.go", []string{"line 1", "changed"}, 3), "changed content")
	diffs := s.Diffs("a.go")
	assert.Equal(t, 1, len(diffs), "one diff")
	assert.Equal(t, "line 2", diffs[0].Original, "original")
	assert.Equal(t, "changed", diffs[0].Updated, "updated")
	assert.Equal(t, types.DiffSourceManual, diffs[0].Source, "source")
	assert.Equal(t, int64(3), diffs[0].TimestampNs, "timestamp")
	assert.Equal(t, 2, diffs[0].StartLine, "start line")

	assert.False(t, s.CommitUserEdits("a.go", []string{"line 1", "changed"}, 4), "checkpoint advanced")
}

func TestCommitPredictedDiffsAgainstCheckpoint(t *testing.T) {
	s := NewStore(3)
	s.Open("a.go", []string{"a", "b", "c"}, 1)

	after := []string{"a", "B", "c"}
	s.CommitPredicted("a.go", Edit{StartLine: 2, EndLineInc: 2, Lines: []string{"B"}}, after, 2)

	diffs := s.Diffs("a.go")
	assert.Equal(t, 1, len(diffs), "one diff")
	assert.Equal(t, "b", diffs[0].Original, "original")
	assert.Equal(t, "B", diffs[0].Updated, "updated")
	assert.Equal(t, types.DiffSourcePredicted, diffs[0].Source, "source")
	assert.False(t, s.CommitUserEdits("a.go", after, 3), "checkpoint is the post-edit content")
}

func TestCommitPredictedPureInsertion(t *testing.T) {
	s := NewStore(3)
	s.Open("a.go", []string{"a", "b"}, 1)

	s.CommitPredicted("a.go", Edit{StartLine: 2, EndLineInc: 1, Lines: []string{"new"}}, []string{"a", "new", "b"}, 2)

	diffs := s.Diffs("a.go")
	assert.Equal(t, 1, len(diffs), "one diff")
	assert.Equal(t, "", diffs[0].Original, "nothing replaced")
	assert.Equal(t, "new", diffs[0].Updated, "inserted line")
}

func TestSyncedClearsHistory(t *testing.T) {
	s := NewStore(3)
	s.Open("a.go", []string{"a"}, 1)
	s.CommitUserEdits("a.go", []string{"b"}, 2)
	s.Synced("a.go", []string{"b"})

	assert.Equal(t, 0, len(s.Diffs("a.go")), "history cleared")
	assert.False(t, s.CommitUserEdits("a.go", []string{"b"}, 3), "synced content is the baseline")
}

func TestSkipHistoryFilesRecordNoDiffs(t *testing.T) {
	s := NewStore(3)
	s.Open(".git/COMMIT_EDITMSG", []string{""}, 1)
	assert.True(t, s.CommitUserEdits(".git/COMMIT_EDITMSG", []string{"fix"}, 2), "content changed")
	assert.Equal(t, 0, len(s.Diffs(".git/COMMIT_EDITMSG")), "no diffs recorded")
}

func TestOpenRestoresMatchingHistory(t *testing.T) {
	s := NewStore(3)
	lines := []string{"a", "b", "c"}
	s.Open("a.go", lines, 1)
	s.CommitUserEdits("a.go", []string{"a", "x", "c"}, 2)
	s.Open("b.go", []string{"other"}, 3)

	s.Open("a.go", []string{"a", "x", "c"}, 4)
	assert.Equal(t, 1, len(s.Diffs("a.go")), "history kept for the same content")

	s.Open("b.go", []string{"other"}, 5)
	s.Open("a.go", []string{"completely", "different", "file", "now"}, 6)
	assert.Equal(t, 0, len(s.Diffs("a.go")), "history dropped when content no longer matches")
}

func TestRecentExcludesActiveAndOrdersByAccess(t *testing.T) {
	s := NewStore(3)
	s.Open("a.go", []string{"a"}, 1)
	s.Open("b.go", []string{"b"}, 2)
	s.Observe("b.go", []string{"b edited"})
	s.Open("c.go", []string{"c"}, 3)

	recent := s.Recent("c.go")
	assert.Equal(t, 2, len(recent), "other files")
	assert.Equal(t, "b.go", recent[0].Path, "most recent first")
	assert.Equal(t, "b edited", recent[0].FirstLines[0], "latest observed content")
	assert.Equal(t, "a.go", recent[1].Path, "older file")
}

func TestStoreKeepsActiveAndMostRecentFiles(t *testing.T) {
	s := NewStore(2)
	s.Open("a.go", []string{"a"}, 1)
	s.Open("b.go", []string{"b"}, 2)
	s.Open("c.go", []string{"c"}, 3)
	s.Open("d.go", []string{"d"}, 4)

	recent := s.Recent("d.go")
	assert.Equal(t, 2, len(recent), "recent files besides the active one")
	assert.Equal(t, "c.go", recent[0].Path, "newest kept")
	assert.Equal(t, "b.go", recent[1].Path, "second newest kept")
}

func TestRenameMovesHistory(t *testing.T) {
	s := NewStore(3)
	s.Open("a.go", []string{"a", "b"}, 1)
	s.CommitUserEdits("a.go", []string{"a", "x"}, 2)

	s.Rename("a.go", "pkg/a.go")

	assert.Equal(t, 1, len(s.Diffs("pkg/a.go")), "diffs under the new path")
	assert.Equal(t, 0, len(s.Recent("pkg/a.go")), "old path not listed as recent")
	assert.False(t, s.CommitUserEdits("pkg/a.go", []string{"a", "x"}, 3), "checkpoint kept")
}

func TestUnnamedBufferHistory(t *testing.T) {
	s := NewStore(3)
	s.Open("a.go", []string{"a"}, 1)
	s.Open("", []string{"scratch"}, 2)
	s.CommitUserEdits("", []string{"edited"}, 3)
	assert.Equal(t, 1, len(s.Diffs("")), "the active unnamed buffer records edits")
	assert.Equal(t, 1, len(s.Recent("")), "only named files are cross-file context")

	s.Open("", []string{"edited"}, 4)
	assert.Equal(t, 0, len(s.Diffs("")), "another unnamed buffer starts a new history")

	s.CommitUserEdits("", []string{"edited", "more"}, 5)
	s.Rename("", "notes.txt")
	assert.Equal(t, 1, len(s.Diffs("notes.txt")), "naming the buffer keeps its history")
	assert.False(t, s.CommitUserEdits("notes.txt", []string{"edited", "more"}, 6), "and its baseline")

	s.Open("", []string{"x"}, 7)
	s.Open("a.go", []string{"a"}, 8)
	assert.Nil(t, s.Diffs(""), "an inactive unnamed buffer is dropped")
}

func TestPlausiblySameFile(t *testing.T) {
	assert.False(t, plausiblySameFile(nil, []string{"a"}), "empty stored content")
	assert.True(t, plausiblySameFile([]string{"a", "b", "c"}, []string{"a", "b", "c"}), "identical")
	assert.True(t, plausiblySameFile([]string{"a", "b", "c"}, []string{"a", "x", "c"}), "one sampled line differs")
	assert.False(t, plausiblySameFile([]string{"a", "b", "c"}, []string{"x", "y", "z"}), "all sampled lines differ")
}

func TestEditApply(t *testing.T) {
	tests := []struct {
		name string
		edit Edit
		want []string
	}{
		{"replacement", Edit{StartLine: 2, EndLineInc: 2, Lines: []string{"B"}}, []string{"a", "B", "c"}},
		{"pure insertion", Edit{StartLine: 2, EndLineInc: 1, Lines: []string{"x", "y"}}, []string{"a", "x", "y", "b", "c"}},
		{"append at end", Edit{StartLine: 4, EndLineInc: 3, Lines: []string{"d"}}, []string{"a", "b", "c", "d"}},
		{"deletion", Edit{StartLine: 2, EndLineInc: 3}, []string{"a"}},
		{"deleting every line leaves an empty line", Edit{StartLine: 1, EndLineInc: 3}, []string{""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.edit.Apply([]string{"a", "b", "c"}), "lines")
		})
	}
}
