package engine

import (
	"context"
	"errors"

	"cursortab/buffer"
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
	EventFileSynced        EventType = "file_synced"
	EventBufEnter          EventType = "buf_enter"
	EventIdleTimeout       EventType = "idle_timeout"
	EventCompletionReady   EventType = "completion_ready"
	EventCompletionError   EventType = "completion_error"
	EventPrefetchReady     EventType = "prefetch_ready"
	EventPrefetchError     EventType = "prefetch_error"

	// eventSettle is sent by Settle, which waits until the engine settled.
	eventSettle EventType = "settle"
)

type Event struct {
	Type      EventType
	RequestID uint64
	Manual    bool
	Response  *types.CompletionResponse
	Stream    CompletionStream
	Err       error
	Position  buffer.Position // Editor events: cursor and buffer size when sent

	settled chan<- Snapshot // eventSettle: receives the snapshot once settled
}

// EventTypeFromString returns the EventType of an event the editor may send,
// or "" for anything else.
func EventTypeFromString(s string) EventType {
	switch EventType(s) {
	case EventEsc, EventTextChanged, EventTrigger, EventCursorMoved,
		EventInsertEnter, EventInsertLeave, EventAccept, EventPartialAccept,
		EventFileSynced, EventBufEnter:
		return EventType(s)
	}
	return ""
}

// transitions maps each state and event to its action. Events without an
// entry are ignored in that state.
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
//	CursorMoved: rejects and resets idle timer (any state)
//	TextChanged: also resets idle timer (any state)
var transitions = map[state]map[EventType]func(*Engine){
	stateIdle: {
		EventTextChangeTimeout: (*Engine).doRequestCompletion,
		EventTrigger:           (*Engine).doManualTrigger,
		EventIdleTimeout:       (*Engine).doRequestIdleCompletion,
		EventCursorMoved:       (*Engine).doResetIdleTimer,
		EventInsertEnter:       (*Engine).stopIdleTimer,
		EventInsertLeave:       (*Engine).startIdleTimer,
		EventEsc:               (*Engine).stopIdleTimer,
		EventTextChanged:       (*Engine).startTextChangeTimer,
	},
	statePendingCompletion: {
		EventTextChanged: (*Engine).doTextChangePending,
		EventEsc:         (*Engine).doReject,
		EventInsertLeave: (*Engine).doRejectAndStartIdleTimer,
		EventCursorMoved: (*Engine).doResetIdleTimer,
	},
	stateHasCompletion: {
		EventAccept:        (*Engine).acceptCompletion,
		EventPartialAccept: (*Engine).partialAcceptCompletion,
		EventEsc:           (*Engine).doReject,
		EventTextChanged:   (*Engine).handleTextChangeImpl,
		EventInsertLeave:   (*Engine).doRejectAndStartIdleTimer,
		EventCursorMoved:   (*Engine).doResetIdleTimer,
	},
	stateHasCursorTarget: {
		EventAccept:      (*Engine).acceptCursorTarget,
		EventEsc:         (*Engine).doReject,
		EventTextChanged: (*Engine).doRejectAndDebounce,
		EventInsertLeave: (*Engine).doRejectAndStartIdleTimer,
		EventCursorMoved: (*Engine).doResetIdleTimer,
	},
	stateStreamingCompletion: {
		EventAccept:        (*Engine).doAcceptStreamingCompletion,
		EventEsc:           (*Engine).doRejectStreaming,
		EventPartialAccept: (*Engine).doPartialAcceptStreaming,
		EventTextChanged:   (*Engine).doRejectStreamingAndDebounce,
		EventInsertLeave:   (*Engine).doRejectStreamingAndStartIdleTimer,
		EventCursorMoved:   (*Engine).doResetIdleTimer,
	},
}

// handleEvent handles one event and every event it raises.
func (e *Engine) handleEvent(event Event) {
	e.process(func() { e.step(event) })
}

// process runs one unit of engine work, then every event it raised, and
// publishes the resulting status once. Nothing runs after Close.
func (e *Engine) process(work func()) {
	if e.ctx.Err() != nil {
		return
	}
	defer e.notifyStateChange(false)
	work()
	for e.ctx.Err() == nil {
		if len(e.queue) > 0 {
			next := e.queue[0]
			e.queue = e.queue[1:]
			e.step(next)
		} else if !e.flushDebounce() {
			break
		}
	}
	e.releaseSettled()
}

func (e *Engine) step(event Event) {
	if event.Type == eventSettle {
		e.settleWaiters = append(e.settleWaiters, event.settled)
		return
	}
	logger.Debug("handle event: %v (state=%s)", event.Type, e.state)
	defer func() {
		logger.Debug("after event: %v (state=%s)", event.Type, e.state)
	}()

	switch event.Type {
	case EventTextChangeTimeout:
		// A timeout of a timer that was stopped or flushed is stale.
		if e.textChangeTimer == nil {
			return
		}
		e.textChangeTimer = nil
	case EventBufEnter:
		// Opening the entered buffer before any edit makes its unedited
		// content the baseline of its history.
		e.syncBuffer()
		return
	case EventFileSynced:
		// The buffer was written or read: its content equals the file, so
		// earlier edits are no longer pending changes.
		e.syncBuffer()
		e.history.Synced(e.buffer.Path(), e.buffer.Lines())
		return
	case EventInsertEnter:
		e.inInsertMode = true
	case EventInsertLeave:
		e.inInsertMode = false
	}

	if e.handleBackgroundEvent(event) {
		return
	}

	if action := transitions[e.state][event.Type]; action != nil {
		action(e)
	}

	switch event.Type {
	case EventTextChanged:
		// The editor reports an edit as this event alone, also when it moved
		// the cursor, so the edit restarts the idle timer like a cursor move.
		e.resetIdleTimer()
		e.recordTextChangeAction(event.Position)
	case EventCursorMoved:
		e.recordCursorMovementAction(event.Position)
	case EventInsertLeave:
		// Leaving insert mode always commits the user's edits to history.
		e.stopTextChangeTimer()
		e.syncBuffer()
		e.history.CommitUserEdits(e.buffer.Path(), e.buffer.Lines(), e.clock.Now().UnixNano())
	}

	// Cancel completions when entering a disabled mode (e.g. InsertEnter
	// from PendingCompletion, which has no transition).
	if !e.isModeEnabled(false) && e.state != stateIdle {
		e.cancelInFlight()
		e.reject()
	}
}

// handleBackgroundEvent handles async completion and prefetch results.
func (e *Engine) handleBackgroundEvent(event Event) bool {
	switch event.Type {
	case EventCompletionReady:
		current := event.RequestID != 0 && event.RequestID == e.completionRequestID &&
			e.state == statePendingCompletion
		if !current || !e.isModeEnabled(event.Manual) {
			if event.Stream != nil {
				event.Stream.Cancel()
			}
			if current {
				e.reject()
			}
			return true
		}
		if event.Stream != nil {
			e.startCompletionStream(event.Stream, event.Manual)
			return true
		}
		e.cancelCurrentRequest()
		e.handleCompletionReadyImpl(event.Response, event.Manual)
		return true

	case EventCompletionError:
		if event.RequestID == 0 || event.RequestID != e.completionRequestID {
			return true
		}
		if !errors.Is(event.Err, context.Canceled) {
			logger.Error("completion error: %v", event.Err)
		}
		e.cancelCurrentRequest()
		if e.state == statePendingCompletion {
			e.state = stateIdle
		}
		return true

	case EventPrefetchReady:
		if event.RequestID == 0 || event.RequestID != e.currentPrefetchRequestID() {
			return true
		}
		e.handlePrefetchReady(event.Response)
		return true

	case EventPrefetchError:
		if event.RequestID == 0 || event.RequestID != e.currentPrefetchRequestID() {
			return true
		}
		e.handlePrefetchError(event.Err)
		return true
	}
	return false
}

// Action functions for state transitions

func (e *Engine) doRequestCompletion() {
	e.requestCompletion(types.CompletionSourceTyping, false)
}

func (e *Engine) doManualTrigger() {
	e.requestCompletion(types.CompletionSourceTyping, true)
}

func (e *Engine) doRequestIdleCompletion() {
	if e.state == stateIdle {
		e.requestCompletion(types.CompletionSourceIdle, false)
	}
}

func (e *Engine) doResetIdleTimer() {
	e.reject()
	e.resetIdleTimer()
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
	if e.stream != nil && e.stream.firstStageRendered {
		e.cancelInFlight()
		e.partialAcceptCompletion()
	}
}

func (e *Engine) doRejectStreaming() {
	e.cancelInFlight()
	e.rejectAndRemember()
	e.stopIdleTimer()
}

// doRejectStreamingAndDebounce stops the stream and handles the typing like
// any text change: a rendered stage the user is typing through stays.
func (e *Engine) doRejectStreamingAndDebounce() {
	e.cancelInFlight()
	e.handleTextChangeImpl()
}

func (e *Engine) doRejectStreamingAndStartIdleTimer() {
	e.cancelInFlight()
	e.reject()
	e.startIdleTimer()
}

// doAcceptStreamingCompletion accepts the rendered stage, if any. The rest of
// the stream then becomes the next prediction.
func (e *Engine) doAcceptStreamingCompletion() {
	if e.stream != nil {
		e.stream.acceptedDuring = true
	}
	switch {
	case e.display.hasCompletion():
		e.state = stateHasCompletion
		e.acceptCompletion()
	case e.stream == nil:
		e.reject()
	}
}
