package engine

import (
	"errors"
	"slices"
	"sync"
	"testing"

	"cursortab/assert"
	"cursortab/types"
)

func TestEngineCreation(t *testing.T) {
	buf := newMockBuffer()
	prov := newMockProvider()
	clock := newMockClock()

	eng := createTestEngine(buf, prov, clock)

	assert.NotNil(t, eng, "New")
	assert.Equal(t, stateIdle, eng.state, "initial state")
}

func TestStateNotifier(t *testing.T) {
	eng := createTestEngine(newMockBuffer(), newMockProvider(), newMockClock())
	var mu sync.Mutex
	var statuses []types.EngineStatus
	setStateNotifier(eng, func(value types.EngineStatus) {
		mu.Lock()
		defer mu.Unlock()
		statuses = append(statuses, value)
	})
	last := func() (types.EngineStatus, int) {
		mu.Lock()
		defer mu.Unlock()
		if len(statuses) == 0 {
			return types.EngineStatus{}, 0
		}
		return statuses[len(statuses)-1], len(statuses)
	}
	go eng.Run()
	defer eng.Close()

	waitFor(t, func() bool { _, n := last(); return n == 1 }, "initial status is published")
	eng.send(Event{Type: EventTrigger})
	waitFor(t, func() bool { s, _ := last(); return s.State == "HasCompletion" }, "request finishes with a suggestion")
	status, count := last()
	assert.False(t, status.Pending, "nothing pending once the suggestion is shown")
	mu.Lock()
	assert.True(t, slices.Contains(statuses, types.EngineStatus{State: "PendingCompletion", Pending: true}), "request start is published")
	mu.Unlock()

	eng.send(Event{Type: EventInsertEnter})
	eng.send(Event{Type: EventEsc})
	waitFor(t, func() bool { s, _ := last(); return s.State == "Idle" }, "suggestion rejected")
	_, n := last()
	assert.Equal(t, count+1, n, "unchanged status is not repeated")
}

func TestStateNotifierRequestResults(t *testing.T) {
	for _, result := range []EventType{EventCompletionReady, EventCompletionError} {
		t.Run(string(result), func(t *testing.T) {
			eng := createTestEngine(newMockBuffer(), newMockProvider(), newMockClock())
			var status types.EngineStatus
			setStateNotifier(eng, func(value types.EngineStatus) { status = value })
			eng.state = statePendingCompletion
			eng.completionRequestID = 2
			eng.currentCancel = func() {}
			eng.handleEvent(Event{Type: result, RequestID: 1, Err: errors.New("request failed")})
			assert.True(t, status.Pending, "old result cannot finish the current request")
			eng.handleEvent(Event{Type: result, RequestID: 2, Err: errors.New("request failed")})
			assert.False(t, status.Pending, "current result finishes the request")
		})
	}
}

func TestStateNotifierOverlappingRequests(t *testing.T) {
	eng := createTestEngine(newMockBuffer(), newMockProvider(), newMockClock())
	var status types.EngineStatus
	setStateNotifier(eng, func(value types.EngineStatus) { status = value })
	eng.state = statePendingCompletion
	eng.completionRequestID = 1
	eng.currentCancel = func() {}
	seedInflightPrefetch(eng, prefetchNoWait)
	prefetchID := seedInflightPrefetch(eng, prefetchNoWait)
	eng.handleEvent(Event{Type: EventCompletionReady, RequestID: 1})
	assert.Equal(t, types.EngineStatus{State: "Idle", Pending: true}, status, "prefetch keeps idle engine pending")
	eng.handleEvent(Event{Type: EventPrefetchReady, RequestID: prefetchID - 1})
	assert.True(t, status.Pending, "stale prefetch result is ignored")
	eng.handleEvent(Event{Type: EventPrefetchReady, RequestID: prefetchID, Response: &types.CompletionResponse{}})
	assert.False(t, status.Pending, "last request finished")
}

func TestStateNotifierCancellation(t *testing.T) {
	for _, action := range []string{"reject", "file-switch", "stop"} {
		t.Run(action, func(t *testing.T) {
			eng := createTestEngine(newMockBuffer(), newMockProvider(), newMockClock())
			var status types.EngineStatus
			setStateNotifier(eng, func(value types.EngineStatus) { status = value })
			eng.state = stateStreamingCompletion
			eng.stream = &activeStream{stream: newMockCompletionStream(nil)}
			eng.currentCancel = func() {}
			seedInflightPrefetch(eng, prefetchNoWait)
			eng.notifyStateChange(false)
			assert.True(t, status.Pending, "active requests")
			switch action {
			case "reject":
				eng.handleEvent(Event{Type: EventEsc})
			case "file-switch":
				eng.handleFileSwitch()
				eng.notifyStateChange(false)
			case "stop":
				eng.Close()
			}
			assert.Equal(t, types.EngineStatus{State: "Idle"}, status, "cancellation clears all activity")
		})
	}
}
