package engine

import (
	"cursortab/assert"
	"testing"
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
	eng := createTestEngine(newMockBuffer(), newMockProvider(), newMockClock())

	var transitions []string
	eng.SetStateNotifier(func(state string) {
		transitions = append(transitions, state)
	})

	eng.setState(statePendingCompletion)
	eng.setState(stateIdle)

	assert.Equal(t, []string{"PendingCompletion", "Idle"}, transitions, "notifier receives transitions")
}
