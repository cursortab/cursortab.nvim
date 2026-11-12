package engine

import (
	"errors"
	"slices"
	"testing"
	"time"

	"cursortab/assert"
	"cursortab/ctx"
	"cursortab/metrics"
	"cursortab/types"
)

func TestRequestErrorReturnsToIdle(t *testing.T) {
	prov := newMockProvider()
	prov.err = errors.New("provider failed")
	h := newHarness(t, newMockBuffer(), prov)

	snapshot := h.send("trigger_completion")

	assert.Equal(t, "Idle", snapshot.State, "provider error finishes the request")
	assert.Equal(t, types.EngineStatus{State: "Idle"}, h.status(), "nothing pending")
}

func TestRequestCollectsOnlyRequiredMaterials(t *testing.T) {
	buf := newMockBuffer("existing")
	buf.diagnostics = &types.Diagnostics{FilePath: "test.go"}
	buf.treesitter = &types.TreesitterContext{EnclosingSignature: "func main()"}
	prov := newMockProvider()
	prov.materials = ctx.Materials{ctx.Diagnostics{}}
	h := newHarness(t, buf, prov)

	h.send("trigger_completion")

	assert.Equal(t, 1, prov.calls(), "provider called")
	assert.Equal(t, 1, buf.diagnosticsCalls, "diagnostics collected")
	assert.Equal(t, 0, buf.treesitterCalls, "treesitter not collected")
	diagnostics, ok := ctx.Find[ctx.Diagnostics](prov.input(0).Materials)
	assert.True(t, ok, "diagnostics passed to the provider")
	assert.Equal(t, buf.diagnostics, diagnostics.Data, "diagnostics data")
}

// A provider that needs the live editor state cannot predict the next edit
// from the buffer as it will be after an accept.
func TestAcceptDoesNotPrefetchWithLiveEditorStateProvider(t *testing.T) {
	prov := newMockProvider()
	prov.syntheticCurrent = false
	prov.push(&types.Completion{StartLine: 1, EndLineInc: 1, Lines: []string{"hello world"}})
	h := newHarness(t, newMockBuffer("hello"), prov)

	h.send("trigger_completion")
	h.send("accept")

	assert.Equal(t, 1, prov.calls(), "no prefetch after accept")
}

func TestAcceptApplyErrorResetsToIdle(t *testing.T) {
	buf := newMockBuffer()
	buf.executeErr = errors.New("execute failed")
	prov := newMockProvider()
	prov.push(&types.Completion{StartLine: 1, EndLineInc: 1, Lines: []string{"x"}})
	h := newHarness(t, buf, prov)
	h.send("trigger_completion")

	snapshot := h.send("accept")

	assert.Equal(t, "Idle", snapshot.State, "state after a failed apply")
	assert.False(t, buf.shown, "completion cleared")
	assert.Equal(t, []string{"line 1", "line 2", "line 3"}, buf.lines, "buffer unchanged")
}

func TestTypingRestartsIdleTimer(t *testing.T) {
	prov := newMockProvider()
	h := newHarness(t, newMockBuffer(), prov, func(_ *Deps, c *Config) {
		c.TextChangeDebounce = -1 // Only the idle timer requests completions
		c.IdleCompletionDelay = 500 * time.Millisecond
	})

	h.buf.lines = []string{"line 1x", "line 2", "line 3"}
	h.buf.col = 7
	h.send("text_changed")
	h.clock.Advance(400 * time.Millisecond)
	h.buf.lines = []string{"line 1xy", "line 2", "line 3"}
	h.buf.col = 8
	h.send("text_changed")
	h.clock.Advance(400 * time.Millisecond)
	h.settle()
	assert.Equal(t, 0, prov.calls(), "typing restarted the idle delay")

	h.clock.Advance(100 * time.Millisecond)
	h.settle()
	assert.Equal(t, 1, prov.calls(), "idle completion after the delay")
}

// The manual flag of a request stays with its completion through rerenders
// while the user types through it.
func TestManualFlagReachesMetrics(t *testing.T) {
	sender := &recordingSender{}
	prov := newMockProvider()
	prov.push(&types.Completion{StartLine: 1, EndLineInc: 1, Lines: []string{"hello world"}})
	h := newHarness(t, newMockBuffer("hello"), prov, func(d *Deps, _ *Config) { d.Metrics = sender })
	h.buf.col = 5

	h.send("trigger_completion")
	h.buf.lines = []string{"hello w"}
	h.buf.col = 7
	h.send("text_changed")
	h.send("accept")
	h.eng.Close()

	waitFor(t, func() bool { return sender.find(metrics.EventAccepted) != nil }, "accept delivered")
	accepted := sender.find(metrics.EventAccepted)
	assert.NotNil(t, accepted.Snapshot, "metrics snapshot")
	assert.True(t, accepted.Snapshot.ManuallyTriggered, "manual flag survives the rerender")
}

// While the last stage is shown, the prefetch asks for the next edit of the
// buffer as it will be once the stage is accepted. A pure insertion keeps the
// line it is inserted before.
func TestPrefetchOfLastStageSeesItApplied(t *testing.T) {
	prov := newMockProvider()
	prov.push(&types.Completion{StartLine: 1, EndLineInc: 3, Lines: []string{"x = 10", "", "def f():", "    return 1"}})
	h := newHarness(t, newMockBuffer("x = 1", "", "def f():", "", "if True:", "    pass"), prov)

	h.send("trigger_completion")
	h.send("accept") // First stage, then the cursor target of the insertion
	h.send("accept") // Jump to the insertion
	assert.Equal(t, 2, prov.calls(), "prefetch while the last stage is shown")
	h.send("accept")

	want := []string{"x = 10", "", "def f():", "    return 1", "", "if True:", "    pass"}
	assert.Equal(t, want, h.buf.lines, "buffer after the last stage")
	assert.Equal(t, want, prov.input(1).Current.File.Lines, "prefetch input")
}

// A prefetch asks for the edit after the buffer as it was. Typing or Esc
// while it runs makes its answer stale: it shows no cursor target.
func TestUserActionDropsRunningPrefetch(t *testing.T) {
	for _, action := range []string{"type", "esc"} {
		t.Run(action, func(t *testing.T) {
			prov := newMockProvider()
			prov.push(&types.Completion{StartLine: 1, EndLineInc: 1, Lines: []string{"line one"}})
			prov.push(&types.Completion{StartLine: 10, EndLineInc: 10, Lines: []string{"line ten"}})
			buf := newMockBuffer("line 1", "line 2", "line 3", "line 4", "line 5", "line 6", "line 7", "line 8", "line 9", "line 10")
			h := newHarness(t, buf, prov)
			h.send("trigger_completion")

			prov.hold(t)
			h.post("accept")
			waitFor(t, func() bool { return prov.calls() == 2 }, "prefetch started")
			switch action {
			case "type":
				buf.mu.Lock()
				buf.lines = slices.Clone(buf.lines)
				buf.lines[0] += "!"
				buf.col = len(buf.lines[0])
				buf.mu.Unlock()
				h.post("text_changed")
			case "esc":
				h.post("esc")
			}
			prov.release()
			waitFor(t, func() bool { return !h.status().Pending }, "prefetch answered")

			assert.Equal(t, "Idle", h.status().State, "nothing shown")
			buf.mu.Lock()
			assert.Equal(t, 0, buf.target, "no cursor target from the stale prefetch")
			buf.mu.Unlock()
		})
	}
}
