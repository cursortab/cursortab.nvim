package engine

import (
	"testing"

	"cursortab/assert"
	"cursortab/types"
)

// newHistoryHarness opens a.go with lines, so its content is the history
// baseline.
func newHistoryHarness(t *testing.T, prov *mockProvider, lines ...string) *harness {
	buf := newMockBuffer(lines...)
	buf.path = "a.go"
	buf.switchOnSync = true
	return newHarness(t, buf, prov)
}

func TestBufEnterSetsHistoryBaseline(t *testing.T) {
	h := newHistoryHarness(t, newMockProvider(), "one", "two")

	h.buf.lines = []string{"one", "changed"}
	h.send("insert_leave")

	diffs := h.history.Diffs("a.go")
	assert.Equal(t, 1, len(diffs), "one recorded edit")
	assert.Equal(t, "two", diffs[0].Original, "edit is against the entered content")
	assert.Equal(t, "changed", diffs[0].Updated, "edited content")
}

func TestPathChangeKeepsHistory(t *testing.T) {
	h := newHistoryHarness(t, newMockProvider(), "one", "two")
	h.buf.lines = []string{"one", "changed"}
	h.send("insert_leave")

	h.buf.pathOnSync = "pkg/a.go"
	h.send("buf_enter")

	assert.Equal(t, 1, len(h.history.Diffs("pkg/a.go")), "history follows the new path")
	assert.Equal(t, 0, len(h.history.Recent("pkg/a.go")), "old path is not a recent file")
}

func TestFileSyncedMakesFileContentTheBaseline(t *testing.T) {
	h := newHistoryHarness(t, newMockProvider(), "one")

	h.buf.lines = []string{"changed on disk"} // :edit! or autoread
	h.send("file_synced", "insert_leave")

	assert.Equal(t, 0, len(h.history.Diffs("a.go")), "reloaded content is not a user edit")
}

func TestAcceptRecordsPredictedEdit(t *testing.T) {
	prov := newMockProvider()
	prov.push(&types.Completion{StartLine: 2, EndLineInc: 2, Lines: []string{"B"}})
	h := newHistoryHarness(t, prov, "a", "b", "c")
	h.buf.row = 2

	h.send("trigger_completion")
	h.send("accept")

	assert.Equal(t, []string{"a", "B", "c"}, h.buf.lines, "edit applied")
	diffs := h.history.Diffs("a.go")
	assert.Equal(t, 1, len(diffs), "one recorded edit")
	assert.Equal(t, "b", diffs[0].Original, "replaced line")
	assert.Equal(t, "B", diffs[0].Updated, "accepted line")
	assert.Equal(t, types.DiffSourcePredicted, diffs[0].Source, "recorded as predicted")
}

func TestAcceptRecordsTypingBeforeThePrediction(t *testing.T) {
	prov := newMockProvider()
	prov.push(&types.Completion{StartLine: 1, EndLineInc: 1, Lines: []string{"hello! big world"}})
	h := newHistoryHarness(t, prov, "hello")

	// Typed in insert mode, no InsertLeave yet.
	h.buf.lines = []string{"hello!"}
	h.buf.col = 6
	h.send("trigger_completion")
	h.send("accept")

	diffs := h.history.Diffs("a.go")
	assert.Equal(t, 2, len(diffs), "typing and prediction recorded separately")
	assert.Equal(t, types.DiffSourceManual, diffs[0].Source, "typing first")
	assert.Equal(t, "hello", diffs[0].Original, "typing original")
	assert.Equal(t, "hello!", diffs[0].Updated, "typing result")
	assert.Equal(t, types.DiffSourcePredicted, diffs[1].Source, "prediction second")
	assert.Equal(t, "hello!", diffs[1].Original, "prediction applies to typed content")
	assert.Equal(t, "hello! big world", diffs[1].Updated, "prediction result")
}

func TestPartialAcceptRecordsPrediction(t *testing.T) {
	prov := newMockProvider()
	prov.push(&types.Completion{StartLine: 1, EndLineInc: 1, Lines: []string{"function foo()"}})
	h := newHistoryHarness(t, prov, "func")
	h.buf.col = 4

	h.send("trigger_completion")
	for i := 0; i < 5 && h.buf.shown; i++ {
		h.send("partial_accept")
	}

	assert.Equal(t, []string{"function foo()"}, h.buf.lines, "completion fully accepted")
	diffs := h.history.Diffs("a.go")
	assert.Equal(t, 1, len(diffs), "one recorded edit")
	assert.Equal(t, types.DiffSourcePredicted, diffs[0].Source, "partial accepts are predicted edits")
	assert.Equal(t, "func", diffs[0].Original, "original line")
	assert.Equal(t, "function foo()", diffs[0].Updated, "accepted line")
}
