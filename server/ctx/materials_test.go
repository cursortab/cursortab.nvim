package ctx

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cursortab/assert"
	"cursortab/types"
)

type materialTestBuffer struct {
	diagnostics      *types.Diagnostics
	treesitter       *types.TreesitterContext
	treesitterRow    int
	treesitterCol    int
	treesitterMaxSib int
}

func (b *materialTestBuffer) Diagnostics() *types.Diagnostics {
	return b.diagnostics
}

func (b *materialTestBuffer) TreesitterSymbols(row int, col int, maxSiblings int) *types.TreesitterContext {
	b.treesitterRow = row
	b.treesitterCol = col
	b.treesitterMaxSib = maxSiblings
	return b.treesitter
}

func TestDiagnosticsCollectsFromBuffer(t *testing.T) {
	buf := &materialTestBuffer{
		diagnostics: &types.Diagnostics{
			FilePath: "main.go",
			Items: []*types.Diagnostic{
				{Message: "first", Severity: types.SeverityError, Range: &types.CursorRange{StartLine: 1}},
				{Message: "second", Severity: types.SeverityWarning},
			},
		},
	}

	material, err := (Diagnostics{MaxItems: 1}).collect(context.Background(), ContextSourceInput{Buffer: buf})

	assert.NoError(t, err, "diagnostics collect")
	diagnostics := material.(Diagnostics)
	assert.Equal(t, SourceDiagnostics, diagnostics.SourceID(), "source id")
	assert.Len(t, 1, diagnostics.Data.Items, "diagnostic max items")
	assert.Equal(t, "first", diagnostics.Data.Items[0].Message, "diagnostic message")
	assert.Equal(t, 1, diagnostics.Data.Items[0].Range.StartLine, "diagnostic range clone")

	buf.diagnostics.Items[0].Message = "mutated"
	buf.diagnostics.Items[0].Range.StartLine = 99
	assert.Equal(t, "first", diagnostics.Data.Items[0].Message, "diagnostic item cloned")
	assert.Equal(t, 1, diagnostics.Data.Items[0].Range.StartLine, "diagnostic range cloned")
}

func TestTreesitterCollectsFromBuffer(t *testing.T) {
	buf := &materialTestBuffer{
		treesitter: &types.TreesitterContext{
			EnclosingSignature: "func main()",
			Imports:            []string{"fmt"},
			Siblings:           []*types.TreesitterSymbol{{Name: "helper", Line: 8}},
			SyntaxRanges:       []*types.LineRange{{StartLine: 1, EndLine: 10}},
		},
	}
	input := ContextSourceInput{
		Current: CurrentSnapshot{Cursor: CursorPosition{Row: 3, Col: 4}},
		Buffer:  buf,
	}

	material, err := (Treesitter{MaxSiblings: 7}).collect(context.Background(), input)

	assert.NoError(t, err, "treesitter collect")
	treesitter := material.(Treesitter)
	assert.Equal(t, 3, buf.treesitterRow, "treesitter row")
	assert.Equal(t, 4, buf.treesitterCol, "treesitter col")
	assert.Equal(t, 7, buf.treesitterMaxSib, "treesitter max siblings")
	assert.Equal(t, "func main()", treesitter.Data.EnclosingSignature, "enclosing signature")
	assert.Equal(t, []string{"fmt"}, treesitter.Data.Imports, "imports")
	assert.Equal(t, "helper", treesitter.Data.Siblings[0].Name, "sibling")
	assert.Equal(t, 1, treesitter.Data.SyntaxRanges[0].StartLine, "syntax range")

	buf.treesitter.Siblings[0].Name = "mutated"
	buf.treesitter.SyntaxRanges[0].StartLine = 99
	assert.Equal(t, "helper", treesitter.Data.Siblings[0].Name, "sibling cloned")
	assert.Equal(t, 1, treesitter.Data.SyntaxRanges[0].StartLine, "syntax range cloned")
}

func TestGitDiffCollectsStagedDiff(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	runGitForTest(t, dir, "init")
	path := filepath.Join(dir, "main.go")
	err := os.WriteFile(path, []byte("package main\n\nfunc Added() {}\n"), 0o644)
	assert.NoError(t, err, "write file")
	runGitForTest(t, dir, "add", "main.go")

	input := ContextSourceInput{
		Current: CurrentSnapshot{
			Workspace: WorkspaceRef{Path: dir},
			File:      FileSnapshot{Path: filepath.Join(dir, "COMMIT_EDITMSG")},
		},
	}

	material, err := (GitDiff{MaxBytes: 8, MaxChangedSymbols: 10}).collect(context.Background(), input)

	assert.NoError(t, err, "git diff collect")
	gitDiff := material.(GitDiff)
	assert.NotNil(t, gitDiff.Data, "git diff data")
	assert.Contains(t, gitDiff.Data.Diff, "+func Added() {}", "changed symbol")
}

func TestRecentFilesCollectsFromSnapshot(t *testing.T) {
	input := ContextSourceInput{
		Snapshot: FileContextSnapshot{
			RecentFiles: []FileContextFile{
				{Path: "new.go", FirstLines: []string{"n1", "n2"}, LastAccessNs: 3_000_000},
				{Path: "old.go", FirstLines: []string{"o1", "o2"}, LastAccessNs: 1_000_000},
			},
		},
	}

	material, err := (RecentFiles{Limit: 1, FirstLines: 1}).collect(context.Background(), input)

	assert.NoError(t, err, "recent files collect")
	recentFiles := material.(RecentFiles)
	assert.Len(t, 1, recentFiles.Files, "recent file limit")
	assert.Equal(t, "new.go", recentFiles.Files[0].FilePath, "recent file path")
	assert.Equal(t, []string{"n1"}, recentFiles.Files[0].Lines, "first lines limit")
	assert.Equal(t, int64(3), recentFiles.Files[0].TimestampMs, "timestamp ms")
}

func TestEditHistoryCollectsProcessedSnapshotDiffs(t *testing.T) {
	now := time.Unix(1000, 0).UnixNano()
	input := ContextSourceInput{
		Snapshot: FileContextSnapshot{
			NowNs: now,
			CurrentFile: FileContextFile{
				Path: "current.go",
				DiffHistories: []*types.DiffEntry{
					{Original: strings.Repeat("a", 10), Updated: strings.Repeat("b", 10), Source: types.DiffSourceManual, TimestampNs: now, StartLine: 1},
					{Original: strings.Repeat("c", 10), Updated: strings.Repeat("d", 10), Source: types.DiffSourceManual, TimestampNs: now, StartLine: 20},
				},
			},
			RecentFiles: []FileContextFile{
				{
					Path: "newer.go",
					DiffHistories: []*types.DiffEntry{
						{Original: "keep newer", Updated: "changed newer", Source: types.DiffSourceManual, TimestampNs: now, StartLine: 1},
					},
					LastAccessNs: 20,
				},
				{
					Path: "older.go",
					DiffHistories: []*types.DiffEntry{
						{Original: "x", Updated: "y", Source: types.DiffSourceManual, TimestampNs: now, StartLine: 1},
						{Original: "y", Updated: "x", Source: types.DiffSourceManual, TimestampNs: now, StartLine: 1},
						{Original: "keep older", Updated: "changed older", Source: types.DiffSourceManual, TimestampNs: now, StartLine: 2},
					},
					LastAccessNs: 10,
				},
			},
		},
	}

	material, err := (EditHistory{MaxTokens: 1}).collect(context.Background(), input)

	assert.NoError(t, err, "edit history collect")
	editHistory := material.(EditHistory)
	assert.Len(t, 3, editHistory.Files, "cross-file first and current last")
	assert.Equal(t, "older.go", editHistory.Files[0].FileName, "older cross-file first")
	assert.Equal(t, "newer.go", editHistory.Files[1].FileName, "newer cross-file second")
	assert.Equal(t, "current.go", editHistory.Files[2].FileName, "current file last")
	assert.Len(t, 1, editHistory.Files[0].DiffHistory, "cross-file inverse pair processed")
	assert.Equal(t, "keep older", editHistory.Files[0].DiffHistory[0].Original, "cross-file processed entry")
	assert.Len(t, 1, editHistory.Files[2].DiffHistory, "current file trimmed")
	assert.Equal(t, strings.Repeat("c", 10), editHistory.Files[2].DiffHistory[0].Original, "current keeps most recent after trim")
}

func TestUserActionsCollectsCurrentFileFromRawSnapshot(t *testing.T) {
	input := ContextSourceInput{
		Current: CurrentSnapshot{File: FileSnapshot{Path: "current.go"}},
		Snapshot: FileContextSnapshot{
			UserActions: []*types.UserAction{
				{ActionType: types.ActionInsertChar, FilePath: "current.go", LineNumber: 1},
				{ActionType: types.ActionDeleteChar, FilePath: "other.go", LineNumber: 2},
				{ActionType: types.ActionInsertSelection, FilePath: "current.go", LineNumber: 3},
			},
		},
	}

	material, err := (UserActions{Limit: 1}).collect(context.Background(), input)

	assert.NoError(t, err, "user actions collect")
	userActions := material.(UserActions)
	assert.Len(t, 1, userActions.Actions, "user action limit")
	assert.Equal(t, "current.go", userActions.Actions[0].FilePath, "current file only")
	assert.Equal(t, 3, userActions.Actions[0].LineNumber, "most recent current-file action")
}

func runGitForTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
}
