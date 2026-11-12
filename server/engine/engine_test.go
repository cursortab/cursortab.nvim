package engine

import (
	"errors"
	"testing"

	"cursortab/assert"
	"cursortab/metrics"
	"cursortab/types"
)

func TestStateNotifier(t *testing.T) {
	prov := newMockProvider()
	prov.push(&types.Completion{StartLine: 1, EndLineInc: 1, Lines: []string{"line one"}})
	h := newHarness(t, newMockBuffer(), prov)
	assert.Equal(t, types.EngineStatus{State: "Idle"}, h.status(), "initial status")

	h.send("trigger_completion")
	assert.True(t, h.published(types.EngineStatus{State: "PendingCompletion", Pending: true}), "request start is published")
	assert.Equal(t, types.EngineStatus{State: "HasCompletion"}, h.status(), "nothing pending once the completion is shown")

	h.mu.Lock()
	count := len(h.statuses)
	h.mu.Unlock()
	h.send("insert_enter", "esc")
	assert.Equal(t, types.EngineStatus{State: "Idle"}, h.status(), "completion rejected")
	h.mu.Lock()
	assert.Equal(t, count+1, len(h.statuses), "unchanged status is not repeated")
	h.mu.Unlock()
}

// A result of a request that was replaced cannot finish the current request.
func TestStaleRequestResultIsIgnored(t *testing.T) {
	for _, staleErr := range []error{nil, errors.New("request failed")} {
		prov := newMockProvider()
		prov.hold(t)
		prov.push(&types.Completion{StartLine: 1, EndLineInc: 1, Lines: []string{"stale"}})
		prov.push(&types.Completion{StartLine: 1, EndLineInc: 1, Lines: []string{"current"}})
		buf := newMockBuffer()
		h := newHarness(t, buf, prov)

		prov.err = staleErr
		h.post("trigger_completion")
		waitFor(t, func() bool { return prov.calls() == 1 }, "first request started")
		prov.mu.Lock()
		prov.err = nil
		prov.mu.Unlock()
		h.post("esc", "trigger_completion")
		waitFor(t, func() bool { return prov.calls() == 2 }, "second request started")

		prov.release()
		prov.release()
		snapshot := h.settle()

		assert.Equal(t, "HasCompletion", snapshot.State, "current request shown")
		assert.Equal(t, []string{"current", "line 2", "line 3"}, snapshot.StagedLines, "current completion")
	}
}

// A prefetch after an accept keeps the idle engine pending until it returns.
func TestStateNotifierPrefetch(t *testing.T) {
	prov := newMockProvider()
	prov.push(&types.Completion{StartLine: 1, EndLineInc: 1, Lines: []string{"line one"}})
	h := newHarness(t, newMockBuffer(), prov)
	h.send("trigger_completion")

	prov.hold(t)
	h.post("accept")
	waitFor(t, func() bool { return h.published(types.EngineStatus{State: "Idle", Pending: true}) }, "prefetch keeps the engine pending")
	prov.release()
	h.settle()

	assert.Equal(t, types.EngineStatus{State: "Idle"}, h.status(), "prefetch finished")
}

func TestStateNotifierCancellation(t *testing.T) {
	for _, action := range []string{"reject", "file-switch", "stop"} {
		t.Run(action, func(t *testing.T) {
			prov := newMockProvider()
			prov.hold(t)
			buf := newMockBuffer()
			h := newHarness(t, buf, prov)
			h.post("trigger_completion")
			waitFor(t, func() bool { return h.status().Pending }, "request pending")

			switch action {
			case "reject":
				h.send("esc")
			case "file-switch":
				buf.mu.Lock()
				buf.switchOnSync = true
				buf.mu.Unlock()
				h.send("buf_enter")
			case "stop":
				h.eng.Close()
			}

			assert.Equal(t, types.EngineStatus{State: "Idle"}, h.status(), "cancellation clears all activity")
			prov.mu.Lock()
			assert.NotNil(t, prov.ctxs[0].Err(), "request cancelled")
			prov.mu.Unlock()
		})
	}
}

// Metrics queued when the engine stops are still delivered, with a live
// context.
func TestQueuedMetricsAreDeliveredAfterClose(t *testing.T) {
	sender := &recordingSender{hold: make(chan struct{})}
	prov := newMockProvider()
	prov.push(&types.Completion{StartLine: 1, EndLineInc: 1, Lines: []string{"line one"}})
	h := newHarness(t, newMockBuffer(), prov, func(d *Deps, _ *Config) { d.Metrics = sender })
	h.send("trigger_completion")
	h.send("accept")
	h.eng.Close()
	close(sender.hold)

	waitFor(t, func() bool { return sender.find(metrics.EventAccepted) != nil }, "accept delivered")
	sender.mu.Lock()
	defer sender.mu.Unlock()
	for _, err := range sender.errs {
		assert.Nil(t, err, "delivery context is not cancelled by Close")
	}
}
