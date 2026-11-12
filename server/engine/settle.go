package engine

import (
	"context"
	"errors"
)

// Snapshot is the engine state once the engine settled.
type Snapshot struct {
	// State is the name of the engine state, as in types.EngineStatus.
	State string
	// StagedLines is the buffer with every stage of the staged completion
	// applied, or nil without a staged completion.
	StagedLines []string
}

var errEngineClosed = errors.New("engine closed")

// Settle returns once the engine handled every event sent before the call
// and no request, stream or prefetch is in flight. A pending text change
// debounce fires at once, as if the user paused; the idle timer does not
// count. Run must be running.
func (e *Engine) Settle(ctx context.Context) (Snapshot, error) {
	settled := make(chan Snapshot, 1)
	select {
	case e.events <- Event{Type: eventSettle, settled: settled}:
	case <-ctx.Done():
		return Snapshot{}, ctx.Err()
	case <-e.ctx.Done():
		return Snapshot{}, errEngineClosed
	}
	select {
	case snapshot := <-settled:
		return snapshot, nil
	case <-ctx.Done():
		return Snapshot{}, ctx.Err()
	case <-e.ctx.Done():
		return Snapshot{}, errEngineClosed
	}
}

// settled reports whether no event is queued and nothing is in flight.
func (e *Engine) settled() bool {
	return len(e.queue) == 0 && e.currentCancel == nil && e.stream == nil && !e.hasInflightPrefetch()
}

// flushDebounce fires the pending text change debounce when a Settle call
// waits and nothing else is left to do. It reports whether it fired.
func (e *Engine) flushDebounce() bool {
	if len(e.settleWaiters) == 0 || e.textChangeTimer == nil || !e.settled() {
		return false
	}
	e.textChangeTimer.Stop()
	e.step(Event{Type: EventTextChangeTimeout})
	return true
}

// releaseSettled answers the Settle calls waiting for the engine, once it
// settled.
func (e *Engine) releaseSettled() {
	if len(e.settleWaiters) == 0 || !e.settled() {
		return
	}
	snapshot := Snapshot{State: e.state.String()}
	if e.stagedCompletion != nil {
		snapshot.StagedLines = applyAllStages(e.buffer.Lines(), e.stagedCompletion.Stages)
	}
	for _, waiter := range e.settleWaiters {
		waiter <- snapshot
	}
	e.settleWaiters = nil
}
