package engine

import (
	"testing"
	"time"

	"cursortab/assert"
	"cursortab/types"
)

var streamBuffer = numbered(12, "line %d")

// newStreamHarness runs an engine whose provider streams streams, in order.
func newStreamHarness(t *testing.T, streams ...*mockCompletionStream) (*harness, *mockStreamingProvider) {
	prov := &mockStreamingProvider{mockProvider: newMockProvider(), streams: streams}
	return newHarness(t, newMockBuffer(streamBuffer...), prov), prov
}

// streamFirstStage starts a stream of the buffer with line 2 changed and waits
// until the first stage is displayed while the stream continues.
func streamFirstStage(t *testing.T, h *harness, stream *mockCompletionStream) {
	t.Helper()
	h.post("trigger_completion")
	for _, line := range []string{"line 1", "line two", "line 3", "line 4", "line 5", "line 6"} {
		stream.lines <- line
	}
	waitFor(t, func() bool { _, shown, _ := h.buf.display(); return shown }, "first stage displayed while streaming")
}

func TestStreamWithoutLinesUsesItsResult(t *testing.T) {
	t.Run("completion", func(t *testing.T) {
		stream := newMockCompletionStream(0, streamBuffer, &types.CompletionResponse{Completion: &types.Completion{
			StartLine: 1, EndLineInc: 1, Lines: []string{"line one"},
		}})
		close(stream.lines)
		h, _ := newStreamHarness(t, stream)

		snapshot := h.send("trigger_completion")

		assert.Equal(t, "HasCompletion", snapshot.State, "result shown")
		assert.Equal(t, "line one", snapshot.StagedLines[0], "result lines")
	})
	t.Run("empty", func(t *testing.T) {
		stream := newMockCompletionStream(0, streamBuffer, &types.CompletionResponse{})
		close(stream.lines)
		h, _ := newStreamHarness(t, stream)

		assert.Equal(t, "Idle", h.send("trigger_completion").State, "nothing shown")
	})
}

// After an accept during streaming, the rest of the stream still finishes:
// the engine stays pending, and a cursor target of the result is shown.
func TestAcceptDuringStream(t *testing.T) {
	stream := newMockCompletionStream(0, streamBuffer, &types.CompletionResponse{
		CursorTarget: &types.CursorPredictionTarget{LineNumber: 10, ShouldRetrigger: true},
	})
	h, _ := newStreamHarness(t, stream)
	streamFirstStage(t, h, stream)

	h.post("accept")
	waitFor(t, func() bool { return h.status() == types.EngineStatus{State: "Idle", Pending: true} }, "stream continues after the accept")
	assert.Equal(t, "line two", h.buf.Lines()[1], "first stage applied")

	close(stream.lines)
	snapshot := h.settle()

	assert.Equal(t, "HasCursorTarget", snapshot.State, "cursor target of the result")
	assert.Equal(t, 10, h.buf.target, "cursor target line")
	assert.Equal(t, types.EngineStatus{State: "HasCursorTarget"}, h.status(), "stream finished")
}

// A new request after an accept during streaming drops the rest of the
// stream, so its late result cannot show a stale cursor target.
func TestRequestAfterAcceptDuringStreamDropsTheStream(t *testing.T) {
	stream := newMockCompletionStream(0, streamBuffer, &types.CompletionResponse{
		CursorTarget: &types.CursorPredictionTarget{LineNumber: 10, ShouldRetrigger: true},
	})
	prefetch := newMockCompletionStream(0, streamBuffer, &types.CompletionResponse{})
	close(prefetch.lines)
	next := newMockCompletionStream(0, streamBuffer, &types.CompletionResponse{})
	close(next.lines)
	h, prov := newStreamHarness(t, stream, prefetch, next)
	streamFirstStage(t, h, stream)
	h.post("accept")
	waitFor(t, func() bool { return h.buf.Lines()[1] == "line two" }, "first stage applied")

	h.buf.mu.Lock()
	h.buf.lines[1], h.buf.row, h.buf.col = "line twox", 2, 9
	h.buf.mu.Unlock()
	h.post("text_changed")
	// Settle waits for the stream, so the debounce fires by the clock.
	waitFor(t, func() bool {
		h.clock.Advance(50 * time.Millisecond)
		return prov.calls() == 3
	}, "new request")
	snapshot := h.settle()
	close(stream.lines)

	assert.Equal(t, "Idle", snapshot.State, "stream dropped")
	assert.Equal(t, types.EngineStatus{State: "Idle"}, h.status(), "nothing pending")
	assert.Equal(t, 0, h.buf.target, "no cursor target")
}

// A streamed stage like one the user rejected is not displayed.
func TestRejectedStreamedStageIsNotDisplayed(t *testing.T) {
	first := newMockCompletionStream(0, streamBuffer, &types.CompletionResponse{})
	second := newMockCompletionStream(0, streamBuffer, &types.CompletionResponse{})
	h, _ := newStreamHarness(t, first, second)
	streamFirstStage(t, h, first)
	h.send("esc")
	renders, _, _ := h.buf.display()

	h.post("text_changed")
	for _, line := range []string{"line 1", "line two", "line 3", "line 4", "line 5", "line 6"} {
		second.lines <- line
	}
	close(second.lines)
	snapshot := h.settle()

	after, shown, _ := h.buf.display()
	assert.Equal(t, renders, after, "rejected stage not rendered")
	assert.False(t, shown, "nothing displayed")
	assert.Equal(t, "Idle", snapshot.State, "state")
}

// A prefetch streams when the provider streams, and its result predicts the
// next edit. A prefetch interrupted by a file switch is not used.
func TestStreamingPrefetch(t *testing.T) {
	far := &types.CompletionResponse{Completion: &types.Completion{StartLine: 10, EndLineInc: 10, Lines: []string{"line ten"}}}
	t.Run("used", func(t *testing.T) {
		shown := newMockCompletionStream(0, streamBuffer, &types.CompletionResponse{Completion: &types.Completion{StartLine: 1, EndLineInc: 1, Lines: []string{"line one"}}})
		close(shown.lines)
		prefetch := newMockCompletionStream(0, streamBuffer, far)
		close(prefetch.lines)
		h, prov := newStreamHarness(t, shown, prefetch)
		h.send("trigger_completion")

		snapshot := h.send("accept")

		assert.Equal(t, 2, prov.calls(), "prefetch requested")
		assert.Equal(t, "HasCursorTarget", snapshot.State, "prefetch predicts the next edit")
		assert.Equal(t, 10, h.buf.target, "cursor target line")
	})
	t.Run("interrupted", func(t *testing.T) {
		shown := newMockCompletionStream(0, streamBuffer, &types.CompletionResponse{Completion: &types.Completion{StartLine: 1, EndLineInc: 1, Lines: []string{"line one"}}})
		close(shown.lines)
		prefetch := newMockCompletionStream(0, streamBuffer, far)
		h, prov := newStreamHarness(t, shown, prefetch)
		h.send("trigger_completion")
		h.post("accept")
		waitFor(t, func() bool { return prov.calls() == 2 }, "prefetch requested")

		h.buf.mu.Lock()
		h.buf.switchOnSync = true
		h.buf.mu.Unlock()
		h.post("buf_enter")
		close(prefetch.lines)
		h.settle()

		assert.Equal(t, 0, h.buf.target, "interrupted prefetch not used")
	})
}
