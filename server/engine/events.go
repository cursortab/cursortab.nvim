package engine

import (
	"context"
	"errors"
	"runtime/debug"
	"sync/atomic"

	"cursortab/logger"
	"cursortab/types"
)

// EventType represents the type of event in the engine
type EventType string

// Event type constants
const (
	EventEsc               EventType = "esc"
	EventTextChanged       EventType = "text_changed"
	EventTextChangeTimeout EventType = "text_change_timeout"
	EventTrigger           EventType = "trigger_completion"
	EventCursorMoved       EventType = "cursor_moved"
	EventInsertEnter       EventType = "insert_enter"
	EventInsertLeave       EventType = "insert_leave"
	EventAccept            EventType = "accept"
	EventPartialAccept     EventType = "partial_accept"
	EventFileSaved         EventType = "file_saved"
	EventIdleTimeout       EventType = "idle_timeout"
	EventCompletionReady   EventType = "completion_ready"
	EventCompletionError   EventType = "completion_error"
	EventPrefetchReady     EventType = "prefetch_ready"
	EventPrefetchError     EventType = "prefetch_error"

	// Streaming events (handled directly via channel selection, not through eventChan)
	EventStreamLine     EventType = "stream_line"     // A line was received from the stream
	EventStreamComplete EventType = "stream_complete" // Stream completed
	EventStreamError    EventType = "stream_error"    // Stream error
)

// Event represents an event in the engine
type Event struct {
	Type EventType
	Data any
}

func init() {
	transitionMap = make(map[transitionKey]*Transition)
	for i := range transitions {
		t := &transitions[i]
		transitionMap[transitionKey{from: t.From, event: t.Event}] = t
	}
}

// EventTypeFromString returns the EventType for a known event string, or "" if unknown.
func EventTypeFromString(s string) EventType {
	switch EventType(s) {
	case EventEsc, EventTextChanged, EventTextChangeTimeout, EventTrigger,
		EventCursorMoved, EventInsertEnter, EventInsertLeave, EventAccept,
		EventPartialAccept, EventFileSaved, EventIdleTimeout,
		EventCompletionReady, EventCompletionError,
		EventPrefetchReady, EventPrefetchError,
		EventStreamLine, EventStreamComplete, EventStreamError:
		return EventType(s)
	}
	return ""
}

// Transition represents a valid state transition in the engine's state machine
type Transition struct {
	From   state
	Event  EventType
	Action func(*Engine)
}

// transitions defines all valid state transitions in the engine.
//
// State Machine:
//
//	                    TextChangeTimeout / IdleTimeout
//	  +-------+              +----------+            +-----------+
//	  | Idle  |------------->| Pending  |----------->| Streaming |
//	  +-------+              +----------+            +-----------+
//	      ^                       |                       |
//	      |                       | CompletionReady       | StreamComplete
//	      |                       v                       |
//	      |                  +-----------+                |
//	      |                  | HasCompl. |<---------------+
//	      |                  +-----------+
//	      |                       | Tab
//	      |         +-------------+-------------+
//	      |         | no cursor target          | has cursor target
//	      |         v                           v
//	      |    (prefetch?)               +--------------+
//	      |         |                    | HasCursorTgt |
//	      +<--------+                    +--------------+
//	                                          | Tab
//	                                          v
//	                                     (prefetch?) --> HasCompl. or Pending
//
//	Rejection (any -> Idle): Esc, InsertLeave, TextChanged mismatch
//	CursorMoved: resets idle timer (any state)
var transitions = []Transition{
	// From stateIdle
	{stateIdle, EventTextChangeTimeout, (*Engine).doRequestCompletion},
	{stateIdle, EventTrigger, (*Engine).doManualTrigger},
	{stateIdle, EventIdleTimeout, (*Engine).doRequestIdleCompletion},
	{stateIdle, EventCursorMoved, (*Engine).doResetIdleTimer},
	{stateIdle, EventInsertEnter, (*Engine).stopIdleTimer},
	{stateIdle, EventInsertLeave, (*Engine).startIdleTimer},
	{stateIdle, EventEsc, (*Engine).stopIdleTimer},
	{stateIdle, EventFileSaved, (*Engine).doFileSaved},
	{stateIdle, EventTextChanged, (*Engine).startTextChangeTimer},

	// From statePendingCompletion
	{statePendingCompletion, EventTextChanged, (*Engine).doTextChangePending},
	{statePendingCompletion, EventEsc, (*Engine).doReject},
	{statePendingCompletion, EventInsertLeave, (*Engine).doRejectAndStartIdleTimer},
	{statePendingCompletion, EventFileSaved, (*Engine).doFileSaved},
	{statePendingCompletion, EventCursorMoved, (*Engine).doResetIdleTimer},

	// From stateHasCompletion
	{stateHasCompletion, EventAccept, (*Engine).acceptCompletion},
	{stateHasCompletion, EventPartialAccept, (*Engine).partialAcceptCompletion},
	{stateHasCompletion, EventEsc, (*Engine).doReject},
	{stateHasCompletion, EventTextChanged, (*Engine).handleTextChangeImpl},
	{stateHasCompletion, EventFileSaved, (*Engine).doFileSaved},
	{stateHasCompletion, EventInsertLeave, (*Engine).doRejectAndStartIdleTimer},
	{stateHasCompletion, EventCursorMoved, (*Engine).doResetIdleTimer},

	// From stateHasCursorTarget
	{stateHasCursorTarget, EventAccept, (*Engine).acceptCursorTarget},
	{stateHasCursorTarget, EventEsc, (*Engine).doReject},
	{stateHasCursorTarget, EventTextChanged, (*Engine).doRejectAndDebounce},
	{stateHasCursorTarget, EventFileSaved, (*Engine).doFileSaved},
	{stateHasCursorTarget, EventInsertLeave, (*Engine).doRejectAndStartIdleTimer},
	{stateHasCursorTarget, EventCursorMoved, (*Engine).doResetIdleTimer},

	// From stateStreamingCompletion
	{stateStreamingCompletion, EventAccept, (*Engine).doAcceptStreamingCompletion},
	{stateStreamingCompletion, EventEsc, (*Engine).doRejectStreaming},
	{stateStreamingCompletion, EventPartialAccept, (*Engine).doPartialAcceptStreaming},
	{stateStreamingCompletion, EventTextChanged, (*Engine).doRejectStreamingAndDebounce},
	{stateStreamingCompletion, EventFileSaved, (*Engine).doFileSaved},
	{stateStreamingCompletion, EventInsertLeave, (*Engine).doRejectStreamingAndStartIdleTimer},
	{stateStreamingCompletion, EventCursorMoved, (*Engine).doResetIdleTimer},
}

// transitionMap provides O(1) lookup for transitions by (state, event) pair
var transitionMap map[transitionKey]*Transition

type transitionKey struct {
	from  state
	event EventType
}

// findTransition looks up a valid transition for the given state and event.
func findTransition(from state, event EventType) *Transition {
	return transitionMap[transitionKey{from: from, event: event}]
}

// dispatch finds and executes the appropriate transition for an event.
func (e *Engine) dispatch(event Event) bool {
	t := findTransition(e.state, event.Type)
	if t == nil {
		return false
	}
	if t.Action != nil {
		t.Action(e)
	}

	// Post-dispatch: Record user actions for RecentUserActions
	switch event.Type {
	case EventTextChanged:
		e.recordTextChangeAction()
	case EventCursorMoved:
		e.recordCursorMovementAction()
	}

	// Post-dispatch hook: InsertLeave always commits uncommitted user edits
	if event.Type == EventInsertLeave {
		e.stopTextChangeTimer()
		e.syncBuffer()
		if e.buffer.CommitUserEdits() {
			e.saveCurrentFileState()
		}
	}

	return true
}

// eventLoopRestarts tracks the number of event loop restarts for panic recovery
var eventLoopRestarts atomic.Int32

const maxEventLoopRestarts = 3

func (e *Engine) eventLoop(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			restarts := eventLoopRestarts.Add(1)
			logger.Error("event loop panic [%d/%d]: %v\n%s",
				restarts, maxEventLoopRestarts, r, debug.Stack())

			if int(restarts) < maxEventLoopRestarts {
				e.eventLoop(e.mainCtx) // Restart the event loop
			} else {
				logger.Error("max event loop restarts reached, stopping engine")
				go e.Stop() // async to avoid deadlock
			}
		}
	}()

	for {
		// Get current stream channels (nil when not streaming)
		e.mu.RLock()
		linesChan := e.streamLinesChan
		tokenChan := e.tokenStreamChan
		e.mu.RUnlock()

		select {
		case <-ctx.Done():
			return

		case line, ok := <-linesChan:
			e.mu.Lock()
			if e.stopped {
				e.mu.Unlock()
				return
			}
			if e.streamLinesChan != linesChan {
				e.mu.Unlock()
				continue
			}
			if !ok {
				e.handleStreamCompleteSimple()
				e.mu.Unlock()
				continue
			}
			e.streamLineNum++
			e.handleStreamLine(line)
			e.mu.Unlock()

		case text, ok := <-tokenChan:
			e.mu.Lock()
			if e.stopped {
				e.mu.Unlock()
				return
			}
			if e.tokenStreamChan != tokenChan {
				e.mu.Unlock()
				continue
			}
			if !ok {
				e.handleTokenStreamComplete()
				e.mu.Unlock()
				continue
			}
			e.handleTokenChunk(text)
			e.mu.Unlock()

		case event, ok := <-e.eventChan:
			if !ok {
				return
			}

			e.mu.RLock()
			stopped := e.stopped
			e.mu.RUnlock()

			if stopped {
				return
			}

			func() {
				defer func() {
					if r := recover(); r != nil {
						logger.Error("event handler panic recovered for event %v: %v", event.Type, r)
					}
				}()
				e.handleEvent(event)
			}()
		}
	}
}

func (e *Engine) handleEvent(event Event) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.stopped {
		return
	}

	logger.Debug("handle event: %v (state=%s)", event.Type, e.state)
	defer func() {
		logger.Debug("after event: %v (state=%s)", event.Type, e.state)
	}()

	// Track insert/normal mode (always, regardless of state)
	switch event.Type {
	case EventInsertEnter:
		e.inInsertMode = true
	case EventInsertLeave:
		e.inInsertMode = false
	}

	// Layer 1: Background/async results
	if e.handleBackgroundEvent(event) {
		return
	}

	// Layer 2: Dispatch table for user/timer events
	e.dispatch(event)

	// Cancel completions when entering a disabled mode
	// (handles transitions not in the table, e.g. InsertEnter from PendingCompletion)
	if !e.isModeEnabled() && e.state != stateIdle {
		e.cancelStreaming()
		e.reject()
	}
}

// handleBackgroundEvent handles async completion and prefetch results.
func (e *Engine) handleBackgroundEvent(event Event) bool {
	switch event.Type {
	case EventCompletionReady:
		if e.state != statePendingCompletion {
			return true
		}
		if !e.isModeEnabled() {
			e.reject()
			return true
		}
		e.handleCompletionReadyImpl(event.Data.(*types.CompletionResponse))
		return true

	case EventCompletionError:
		if err, ok := event.Data.(error); !ok || !errors.Is(err, context.Canceled) {
			logger.Error("completion error: %v", event.Data)
		}
		return true

	case EventPrefetchReady:
		if e.prefetchState != prefetchInFlight &&
			e.prefetchState != prefetchWaitingForTab &&
			e.prefetchState != prefetchWaitingForCursorPrediction {
			return true
		}
		e.handlePrefetchReady(event.Data.(*types.CompletionResponse))
		return true

	case EventPrefetchError:
		if e.prefetchState != prefetchInFlight &&
			e.prefetchState != prefetchWaitingForTab &&
			e.prefetchState != prefetchWaitingForCursorPrediction {
			return true
		}
		if err, ok := event.Data.(error); ok {
			e.handlePrefetchError(err)
		} else {
			e.handlePrefetchError(nil)
		}
		return true
	}
	return false
}

// Action functions for state transitions

func (e *Engine) doRequestCompletion() {
	e.requestCompletion(types.CompletionSourceTyping)
}

func (e *Engine) doManualTrigger() {
	e.manuallyTriggered = true
	e.requestCompletion(types.CompletionSourceTyping)
}

func (e *Engine) doRequestIdleCompletion() {
	if e.state == stateIdle {
		e.requestCompletion(types.CompletionSourceIdle)
	}
}

func (e *Engine) doResetIdleTimer() {
	e.reject()
	e.resetIdleTimer()
}

func (e *Engine) doFileSaved() {
	e.syncBuffer()
	e.buffer.ClearDiffHistory()
	e.saveCurrentFileState()
}

func (e *Engine) doTextChangePending() {
	e.cancelCurrentRequest()
	e.state = stateIdle
	e.startTextChangeTimer()
}

func (e *Engine) doReject() {
	e.rejectAndRemember()
	e.stopIdleTimer()
}

func (e *Engine) doRejectAndDebounce() {
	e.rejectAndRemember()
	e.startTextChangeTimer()
}

func (e *Engine) doRejectAndStartIdleTimer() {
	e.reject()
	e.startIdleTimer()
}

func (e *Engine) doPartialAcceptStreaming() {
	if e.streamingState != nil && e.streamingState.FirstStageRendered {
		e.cancelLineStreamingKeepPartial()
		e.partialAcceptCompletion()
	}
}

// Streaming state action functions

func (e *Engine) doRejectStreaming() {
	e.cancelStreaming()
	e.rejectAndRemember()
	e.stopIdleTimer()
}

// cancelStreamAndCheckTyping cancels the given streaming mode, preserving partial state,
// then checks if user typing matches the prediction.
// Returns true if the event was handled (match or mismatch), false if no partial results.
func (e *Engine) cancelStreamAndCheckTyping(cancelFn func()) bool {
	cancelFn()
	e.syncBuffer()
	matches, hasRemaining := e.checkTypingMatchesPrediction()
	if matches && hasRemaining {
		e.state = stateHasCompletion
		return true
	}
	if matches {
		e.reject()
		e.startTextChangeTimer()
		return true
	}
	e.rejectAndRemember()
	e.startTextChangeTimer()
	return true
}

func (e *Engine) doRejectStreamingAndDebounce() {
	if e.tokenStreamingState != nil && len(e.completions) > 0 {
		e.cancelStreamAndCheckTyping(e.cancelTokenStreamingKeepPartial)
		return
	}

	if e.streamingState != nil && len(e.completions) > 0 {
		e.cancelStreamAndCheckTyping(e.cancelLineStreamingKeepPartial)
		return
	}

	e.cancelStreaming()
	e.reject()
	e.startTextChangeTimer()
}

func (e *Engine) doRejectStreamingAndStartIdleTimer() {
	e.cancelStreaming()
	e.reject()
	e.startIdleTimer()
}

func (e *Engine) doAcceptStreamingCompletion() {
	// For token streaming, cancel and keep partial (no continuation support)
	if e.tokenStreamingState != nil {
		e.cancelTokenStreamingKeepPartial()
	}

	// For line streaming, keep streaming running to show cursor prediction when done
	hasLineStreaming := e.streamingState != nil

	// If we have completions, accept them
	if len(e.completions) > 0 {
		// Mark that we accepted during streaming so handleStreamCompleteSimple
		// knows to compute cursor prediction from accumulated text
		if hasLineStreaming {
			e.acceptedDuringStreaming = true
		}
		e.state = stateHasCompletion
		e.acceptCompletion()
	} else {
		// No completions to accept
		if hasLineStreaming {
			// Keep streaming, will show result when complete
			e.acceptedDuringStreaming = true
		} else {
			e.reject()
		}
	}
}
