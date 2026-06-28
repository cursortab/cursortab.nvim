package ctx

import (
	"context"
	"testing"
	"time"

	"cursortab/assert"
	"cursortab/types"
)

type materialBuffer struct {
	diagnostics *types.Diagnostics
	treesitter  *types.TreesitterContext
	row         int
	col         int
	maxSiblings int
}

func (b *materialBuffer) Diagnostics() *types.Diagnostics {
	return b.diagnostics
}

func (b *materialBuffer) TreesitterSymbols(row int, col int, maxSiblings int) *types.TreesitterContext {
	b.row = row
	b.col = col
	b.maxSiblings = maxSiblings
	return b.treesitter
}

func TestFileContextNeedsReportsMaterialInputs(t *testing.T) {
	needs := Materials{RecentFiles{}, EditHistory{}, UserActions{}}.FileContextNeeds()

	assert.True(t, needs.RecentFileLines, "recent file lines")
	assert.True(t, needs.RecentFileDiffHistories, "recent file diff histories")
	assert.True(t, needs.CurrentDiffHistories, "current diff histories")
	assert.True(t, needs.UserActions, "user actions")
}

func TestDiagnosticsAndTreesitterCollectFromBuffer(t *testing.T) {
	diagnostics := &types.Diagnostics{FilePath: "main.go"}
	treesitter := &types.TreesitterContext{EnclosingSignature: "func main()"}
	buffer := &materialBuffer{diagnostics: diagnostics, treesitter: treesitter}
	input := ContextSourceInput{
		Current: CurrentSnapshot{
			Cursor: CursorPosition{Row: 7, Col: 11},
		},
		Buffer: buffer,
		Limits: CollectionLimits{MaxSiblings: 4},
	}

	diagnosticsMaterial, err := Diagnostics{}.collect(context.Background(), input)
	assert.NoError(t, err, "diagnostics collect")
	assert.Equal(t, diagnostics, diagnosticsMaterial.(Diagnostics).Data, "diagnostics data")

	treesitterMaterial, err := Treesitter{}.collect(context.Background(), input)
	assert.NoError(t, err, "treesitter collect")
	assert.Equal(t, treesitter, treesitterMaterial.(Treesitter).Data, "treesitter data")
	assert.Equal(t, 7, buffer.row, "treesitter row")
	assert.Equal(t, 11, buffer.col, "treesitter col")
	assert.Equal(t, 4, buffer.maxSiblings, "treesitter max siblings")
}

func TestUserActionsCollectFiltersCurrentFileAppliesLimitAndClones(t *testing.T) {
	actions := []*types.UserAction{
		{FilePath: "current.go", Offset: 1},
		{FilePath: "other.go", Offset: 10},
		{FilePath: "current.go", Offset: 2},
		{FilePath: "current.go", Offset: 3},
	}
	input := ContextSourceInput{
		Current:  CurrentSnapshot{File: FileSnapshot{Path: "current.go"}},
		Snapshot: FileContextSnapshot{UserActions: actions},
		Limits:   CollectionLimits{MaxUserActions: 2},
	}

	material, err := UserActions{}.collect(context.Background(), input)
	assert.NoError(t, err, "user actions collect")
	result := material.(UserActions)

	assert.Len(t, 2, result.Actions, "filtered actions")
	assert.Equal(t, 2, result.Actions[0].Offset, "first retained action")
	assert.Equal(t, 3, result.Actions[1].Offset, "second retained action")

	actions[2].Offset = 99
	assert.Equal(t, 2, result.Actions[0].Offset, "actions are cloned")
}

func TestEditHistoryCollectsCrossFileBeforeCurrent(t *testing.T) {
	now := time.Now().UnixNano()
	diff := func(original, updated string) *types.DiffEntry {
		return &types.DiffEntry{
			Original:    original,
			Updated:     updated,
			Source:      types.DiffSourceManual,
			TimestampNs: now,
			StartLine:   1,
		}
	}
	input := ContextSourceInput{
		Current: CurrentSnapshot{File: FileSnapshot{Path: "current.go"}},
		Snapshot: FileContextSnapshot{
			RecentFiles: []RecentFileSnapshot{
				{Path: "old.go", DiffHistories: []*types.DiffEntry{diff("old", "old changed")}},
				{Path: "new.go", DiffHistories: []*types.DiffEntry{diff("new", "new changed")}},
			},
			CurrentDiffHistories: []*types.DiffEntry{diff("current", "current changed")},
			NowNs:                now,
		},
	}

	material, err := EditHistory{}.collect(context.Background(), input)
	assert.NoError(t, err, "edit history collect")
	result := material.(EditHistory)

	assert.Len(t, 3, result.Files, "edit history files")
	assert.Equal(t, "new.go", result.Files[0].FileName, "most recent cross-file first")
	assert.Equal(t, "old.go", result.Files[1].FileName, "older cross-file second")
	assert.Equal(t, "current.go", result.Files[2].FileName, "current file last")
}
