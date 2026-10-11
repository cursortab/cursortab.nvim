package engine

import (
	"errors"
	"testing"
	"time"

	"cursortab/assert"
	"cursortab/types"
)

func TestEngineCreation(t *testing.T) {
	buf := newMockBuffer()
	prov := newMockProvider()
	clock := newMockClock()

	eng := createTestEngine(buf, prov, clock)

	assert.NotNil(t, eng, "NewEngine")
	assert.Equal(t, stateIdle, eng.state, "initial state")
}

func TestStateNotifier(t *testing.T) {
	eng, cancel := createTestEngineWithContext(newMockBuffer(), newMockProvider(), newMockClock())
	defer cancel()
	var status types.EngineStatus
	notifications := 0
	connected := false
	eng.SetStateNotifier(func(value types.EngineStatus) {
		assert.True(t, connected, "editor is connected before its status is sent")
		status = value
		notifications++
	})
	eng.ConnectEditor(func() { connected = true })
	assert.Equal(t, types.EngineStatus{State: "Idle"}, status, "initial status")
	eng.handleEvent(Event{Type: EventTrigger})
	assert.Equal(t, types.EngineStatus{State: "PendingCompletion", Pending: true}, status, "request started")

	select {
	case event := <-eng.eventChan:
		eng.handleEvent(event)
	case <-time.After(time.Second):
		t.Fatal("completion did not return")
	}
	assert.Equal(t, types.EngineStatus{State: "HasCompletion"}, status, "request finished but suggestion remains")
	count := notifications
	eng.handleEvent(Event{Type: EventInsertEnter})
	assert.Equal(t, count, notifications, "unchanged status is not repeated")
	eng.handleEvent(Event{Type: EventEsc})
	assert.Equal(t, types.EngineStatus{State: "Idle"}, status, "suggestion rejected")
	connected = false
	eng.ConnectEditor(func() { connected = true })
	assert.Equal(t, count+2, notifications, "new connection receives current status")
}

func TestStateNotifierRequestResults(t *testing.T) {
	for _, result := range []EventType{EventCompletionReady, EventCompletionError} {
		t.Run(string(result), func(t *testing.T) {
			eng := createTestEngine(newMockBuffer(), newMockProvider(), newMockClock())
			var status types.EngineStatus
			eng.SetStateNotifier(func(value types.EngineStatus) { status = value })
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
	eng.SetStateNotifier(func(value types.EngineStatus) { status = value })
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
			eng.SetStateNotifier(func(value types.EngineStatus) { status = value })
			eng.state = stateStreamingCompletion
			eng.streamingState = &streamingState{}
			eng.completionStream = newMockCompletionStream(nil)
			eng.currentCancel = func() {}
			seedInflightPrefetch(eng, prefetchNoWait)
			eng.notifyStateChange(false)
			assert.True(t, status.Pending, "active requests")
			switch action {
			case "reject":
				eng.handleEvent(Event{Type: EventEsc})
			case "file-switch":
				eng.handleFileSwitch("old.go", "new.go", nil)
				eng.notifyStateChange(false)
			case "stop":
				eng.Stop()
			}
			assert.Equal(t, types.EngineStatus{State: "Idle"}, status, "cancellation clears all activity")
		})
	}
}
