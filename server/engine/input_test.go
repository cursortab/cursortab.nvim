package engine

import (
	"testing"
	"time"

	"cursortab/assert"
	"cursortab/ctx"
	"cursortab/types"
)

func TestBuildCompletionInput_CurrentSnapshotByKind(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"package main", "func main() {}"}
	buf.row = 2
	buf.col = 5
	buf.path = "main.go"
	buf.version = 7
	buf.viewportBottom = 12
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)
	eng.WorkspacePath = "/repo"
	eng.WorkspaceID = "workspace-id"
	eng.config.CursorPrediction.Enabled = false
	eng.config.MaxVisibleLines = 9

	tests := []struct {
		name   string
		kind   ctx.RequestKind
		source types.CompletionSource
	}{
		{name: "completion", kind: ctx.RequestCompletion, source: types.CompletionSourceTyping},
		{name: "prefetch", kind: ctx.RequestPrefetch, source: types.CompletionSourceTyping},
		{name: "eval", kind: ctx.RequestEval, source: types.CompletionSourceIdle},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input, sourceInput := eng.buildCompletionInput(completionInputOptions{
				kind:   tt.kind,
				source: tt.source,
			})

			assert.Equal(t, tt.kind, input.Kind, "kind")
			assert.Equal(t, tt.source, input.Trigger, "trigger")
			assert.Equal(t, "/repo", input.Current.Workspace.Path, "workspace path")
			assert.Equal(t, "workspace-id", input.Current.Workspace.ID, "workspace id")
			assert.Equal(t, "main.go", input.Current.File.Path, "file path")
			assert.Equal(t, []string{"package main", "func main() {}"}, input.Current.File.Lines, "lines")
			assert.Equal(t, 7, input.Current.File.Version, "version")
			assert.Equal(t, 2, input.Current.Cursor.Row, "row stays 1-indexed")
			assert.Equal(t, 5, input.Current.Cursor.Col, "col stays 0-indexed byte column")
			assert.Equal(t, 11, input.Current.View.ViewportHeight, "viewport height")
			assert.Equal(t, 9, input.Current.View.MaxVisibleLines, "max visible lines")
			assert.Equal(t, input.Current, sourceInput.Current, "source input current")
		})
	}
}

func TestBuildCompletionInput_PrefetchOverrideAndExplicitBuffer(t *testing.T) {
	buf := newMockBuffer()
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)
	overrideLines := []string{"one", "two"}

	input, sourceInput := eng.buildCompletionInput(completionInputOptions{
		kind:              ctx.RequestPrefetch,
		source:            types.CompletionSourceTyping,
		lines:             overrideLines,
		cursorRow:         3,
		cursorCol:         0,
		hasCursorOverride: true,
	})
	overrideLines[0] = "mutated"

	assert.Equal(t, []string{"one", "two"}, input.Current.File.Lines, "override lines cloned")
	assert.Equal(t, 3, input.Current.Cursor.Row, "override row stays 1-indexed")
	assert.Equal(t, 0, input.Current.Cursor.Col, "override col stays 0-indexed byte column")
	assert.Equal(t, buf, sourceInput.Buffer, "explicit buffer reader")
}

func TestBuildFileContextSnapshot_ClonesRawContext(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"current a", "current b", "current c"}
	buf.path = "current.go"
	buf.diffHistories = []*types.DiffEntry{
		{Original: "old current", Updated: "new current", Source: types.DiffSourceManual, TimestampNs: 1, StartLine: 1},
		{Original: "old current 2", Updated: "new current 2", Source: types.DiffSourceManual, TimestampNs: 2, StartLine: 2},
	}
	prov := newMockProvider()
	clock := newMockClock()
	clock.now = time.Unix(1000, 0)
	eng := createTestEngine(buf, prov, clock)
	eng.config.MaxDiffTokens = 1
	eng.fileStateStore["recent.go"] = &FileState{
		FirstLines: []string{"recent first"},
		DiffHistories: []*types.DiffEntry{
			{Original: "old recent", Updated: "new recent", Source: types.DiffSourcePredicted, TimestampNs: 3, StartLine: 4},
		},
		LastAccessNs: 55,
	}
	eng.userActions = []*types.UserAction{
		{ActionType: types.ActionInsertChar, FilePath: "current.go", LineNumber: 1, TimestampMs: 10},
		{ActionType: types.ActionDeleteChar, FilePath: "other.go", LineNumber: 2, TimestampMs: 20},
	}

	snapshot := eng.buildFileContextSnapshot()

	buf.lines[0] = "mutated current"
	buf.diffHistories[0].Original = "mutated diff"
	eng.fileStateStore["recent.go"].FirstLines[0] = "mutated recent"
	eng.fileStateStore["recent.go"].DiffHistories[0].Original = "mutated recent diff"
	eng.userActions[0].LineNumber = 99

	assert.Equal(t, "current.go", snapshot.CurrentFile.Path, "current path")
	assert.Equal(t, []string{"current a", "current b", "current c"}, snapshot.CurrentFile.FirstLines, "current first lines clone")
	assert.Len(t, 2, snapshot.CurrentFile.DiffHistories, "raw current diff count")
	assert.Equal(t, "old current", snapshot.CurrentFile.DiffHistories[0].Original, "raw current diff clone")
	assert.Len(t, 1, snapshot.RecentFiles, "recent files")
	assert.Equal(t, []string{"recent first"}, snapshot.RecentFiles[0].FirstLines, "recent first lines clone")
	assert.Equal(t, "old recent", snapshot.RecentFiles[0].DiffHistories[0].Original, "recent diff clone")
	assert.Len(t, 2, snapshot.UserActions, "full raw user action ring")
	assert.Equal(t, "other.go", snapshot.UserActions[1].FilePath, "cross-file action retained")
	assert.Equal(t, 1, snapshot.UserActions[0].LineNumber, "user action clone")
	assert.Equal(t, clock.now.UnixNano(), snapshot.NowNs, "now ns")

	if snapshot.CurrentFile.DiffHistories[0] == buf.diffHistories[0] {
		t.Errorf("current diff entry should be cloned")
	}
	if snapshot.UserActions[0] == eng.userActions[0] {
		t.Errorf("user action entry should be cloned")
	}
}

func TestBuildFileContextSnapshot_DoesNotProcessOrTrimDiffHistory(t *testing.T) {
	buf := newMockBuffer()
	buf.path = "current.go"
	buf.diffHistories = []*types.DiffEntry{
		{Original: "same", Updated: "other", Source: types.DiffSourceManual, TimestampNs: 1, StartLine: 1},
		{Original: "other", Updated: "same", Source: types.DiffSourceManual, TimestampNs: 2, StartLine: 1},
	}
	prov := newMockProvider()
	clock := newMockClock()
	clock.now = time.Unix(1000, 0)
	eng := createTestEngine(buf, prov, clock)
	eng.config.MaxDiffTokens = 1

	snapshot := eng.buildFileContextSnapshot()

	assert.Len(t, 2, snapshot.CurrentFile.DiffHistories, "raw diff entries are not collapsed")
	assert.Equal(t, "same", snapshot.CurrentFile.DiffHistories[0].Original, "first raw entry retained")
	assert.Equal(t, "other", snapshot.CurrentFile.DiffHistories[1].Original, "second raw entry retained")
}
