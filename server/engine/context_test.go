package engine

import (
	"context"
	"cursortab/assert"
	"cursortab/text"
	"cursortab/types"
	"testing"
)

func TestIsFileStateValid(t *testing.T) {
	buf := newMockBuffer()
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	tests := []struct {
		name         string
		state        *FileState
		currentLines []string
		want         bool
	}{
		{
			name:         "empty original lines",
			state:        &FileState{OriginalLines: []string{}},
			currentLines: []string{"a", "b"},
			want:         false,
		},
		{
			name:         "same content",
			state:        &FileState{OriginalLines: []string{"a", "b", "c"}},
			currentLines: []string{"a", "b", "c"},
			want:         true,
		},
		{
			name:         "minor difference",
			state:        &FileState{OriginalLines: []string{"a", "b", "c"}},
			currentLines: []string{"a", "b", "c", "d"},
			want:         true,
		},
		{
			name:         "major line count difference",
			state:        &FileState{OriginalLines: []string{"a", "b", "c"}},
			currentLines: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m", "n"},
			want:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := eng.isFileStateValid(tt.state, tt.currentLines)
			assert.Equal(t, tt.want, got, "isFileStateValid")
		})
	}
}

func TestTrimFileStateStore(t *testing.T) {
	buf := newMockBuffer()
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	// Add 5 file states
	for i := 0; i < 5; i++ {
		eng.fileStateStore[string(rune('a'+i))+".go"] = &FileState{
			LastAccessNs: int64(i * 1000),
		}
	}

	eng.trimFileStateStore(2)

	assert.Equal(t, 2, len(eng.fileStateStore), "file state store size")

	// Should keep the most recently accessed (highest LastAccessNs)
	_, existsD := eng.fileStateStore["d.go"]
	assert.True(t, existsD, "should keep d.go (second most recent)")
	_, existsE := eng.fileStateStore["e.go"]
	assert.True(t, existsE, "should keep e.go (most recent)")
}

// TestHandleFileSwitch_DropsInFlightWork verifies that switching files cancels
// in-flight prefetch/streaming/current requests and clears completion UI
// state. Without this, late-arriving responses for the OLD file would be
// applied to the NEW buffer using the old file's row indices, producing
// either a wrong-file completion or a garbage diff.
func TestHandleFileSwitch_DropsInFlightWork(t *testing.T) {
	buf := newMockBuffer()
	prov := newMockProvider()
	clock := newMockClock()
	eng, cancel := createTestEngineWithContext(buf, prov, clock)
	defer cancel()

	prefetchCtx, prefetchCancel := context.WithCancel(context.Background())
	currentCtx, currentCancel := context.WithCancel(context.Background())
	streamCtx, streamCancel := context.WithCancel(context.Background())

	eng.prefetchCancel = prefetchCancel
	eng.prefetchState = prefetchInFlight
	eng.prefetchedCompletions = []*types.Completion{{
		StartLine: 5, EndLineInc: 5, Lines: []string{"old file completion"},
	}}
	eng.currentCancel = currentCancel
	eng.streamingCancel = streamCancel
	eng.streamingState = &StreamingState{}
	eng.state = stateStreamingCompletion
	eng.completions = []*types.Completion{{
		StartLine: 1, EndLineInc: 1, Lines: []string{"old"},
	}}
	eng.stagedCompletion = &text.StagedCompletion{CurrentIdx: 0}
	eng.cursorTarget = &types.CursorPredictionTarget{LineNumber: 5}

	eng.handleFileSwitch("a.go", "b.go", []string{"new content"})

	assert.Equal(t, prefetchNone, eng.prefetchState, "prefetch state reset")
	assert.Nil(t, eng.prefetchedCompletions, "prefetched completions cleared")
	assert.Nil(t, eng.prefetchCancel, "prefetch cancel func cleared")
	assert.Nil(t, eng.currentCancel, "current request cancel cleared")
	assert.Nil(t, eng.streamingCancel, "streaming cancel cleared")
	assert.Nil(t, eng.streamingState, "streaming state cleared")
	assert.Nil(t, eng.completions, "completions cleared")
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
