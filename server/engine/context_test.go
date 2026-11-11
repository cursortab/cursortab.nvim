package engine

import (
	"context"
	"cursortab/assert"
	"cursortab/text"
	"cursortab/types"
	"testing"
)

func TestHandleFileSwitch_DropsInFlightWork(t *testing.T) {
	buf := newMockBuffer()
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)
	defer eng.Close()

	prefetchCtx, cancelPrefetch := context.WithCancel(context.Background())
	currentCtx, currentCancel := context.WithCancel(context.Background())
	streamCtx, streamCancel := context.WithCancel(context.Background())
	defer cancelPrefetch()
	defer currentCancel()
	defer streamCancel()

	eng.prefetch = prefetchSlot{
		inflight: &prefetchInflight{cancel: cancelPrefetch},
		ready: &prefetchedCompletion{CompletionResponse: &types.CompletionResponse{Completion: &types.Completion{
			StartLine: 5, EndLineInc: 5, Lines: []string{"old file completion"},
		}}},
	}
	eng.currentCancel = currentCancel
	eng.stream = &activeStream{stream: newMockCompletionStream(streamCancel)}
	eng.state = stateStreamingCompletion
	showDisplayedCompletionForTest(
		eng,
		&types.Completion{
			StartLine: 1, EndLineInc: 1, Lines: []string{"old"},
		},
		nil,
		nil,
	)
	eng.stagedCompletion = &text.StagedCompletion{CurrentIdx: 0}
	eng.cursorTarget = &types.CursorPredictionTarget{LineNumber: 5}

	eng.handleFileSwitch()

	assertNoPrefetch(t, eng, "prefetch cleared")
	assert.Nil(t, eng.currentCancel, "current request cancel cleared")
	assert.Nil(t, eng.stream, "streaming state cleared")
	assert.Nil(t, eng.display.current(), "completions cleared")
	assert.Nil(t, eng.stagedCompletion, "staged completion cleared")
	assert.Nil(t, eng.cursorTarget, "cursor target cleared")
	assert.Equal(t, stateIdle, eng.state, "state reset to idle")

	for _, c := range []struct {
		name string
		ctx  context.Context
	}{
		{"prefetch", prefetchCtx},
		{"current", currentCtx},
		{"stream", streamCtx},
	} {
		select {
		case <-c.ctx.Done():
		default:
			t.Errorf("%s context should be cancelled", c.name)
		}
	}
}

func TestBufEnterSetsHistoryBaseline(t *testing.T) {
	buf := newMockBuffer()
	buf.path = "a.go"
	buf.lines = []string{"one", "two"}
	eng := createTestEngine(buf, newMockProvider(), newMockClock())
	defer eng.Close()

	buf.switchOnSync = true
	eng.handleEvent(Event{Type: EventBufEnter})

	buf.lines = []string{"one", "changed"}
	eng.handleEvent(Event{Type: EventInsertLeave})

	diffs := eng.history.Diffs("a.go")
	assert.Equal(t, 1, len(diffs), "one recorded edit")
	assert.Equal(t, "two", diffs[0].Original, "edit is against the entered content")
	assert.Equal(t, "changed", diffs[0].Updated, "edited content")
}

func TestPathChangeKeepsHistory(t *testing.T) {
	buf := newMockBuffer()
	buf.path = "a.go"
	buf.lines = []string{"one", "two"}
	eng := createTestEngine(buf, newMockProvider(), newMockClock())
	defer eng.Close()

	buf.switchOnSync = true
	eng.handleEvent(Event{Type: EventBufEnter})
	buf.lines = []string{"one", "changed"}
	eng.handleEvent(Event{Type: EventInsertLeave})

	buf.pathOnSync = "pkg/a.go"
	eng.handleEvent(Event{Type: EventBufEnter})

	assert.Equal(t, 1, len(eng.history.Diffs("pkg/a.go")), "history follows the new path")
	assert.Equal(t, 0, len(eng.history.Recent("pkg/a.go")), "old path is not a recent file")
}

func TestFileSyncedMakesFileContentTheBaseline(t *testing.T) {
	buf := newMockBuffer()
	buf.path = "a.go"
	buf.lines = []string{"one"}
	eng := createTestEngine(buf, newMockProvider(), newMockClock())
	defer eng.Close()
	buf.switchOnSync = true
	eng.handleEvent(Event{Type: EventBufEnter})

	buf.lines = []string{"changed on disk"} // :edit! or autoread
	eng.handleEvent(Event{Type: EventFileSynced})
	eng.handleEvent(Event{Type: EventInsertLeave})

	assert.Equal(t, 0, len(eng.history.Diffs("a.go")), "reloaded content is not a user edit")
}
