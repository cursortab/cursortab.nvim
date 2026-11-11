// Package engine is the completion state machine of one editor session.
//
// All engine state is owned by the goroutine that runs [Engine.Run]. Editor
// events, timers, and provider results reach it as [Event] values through one
// channel; nothing else touches the state, so the engine needs no locks.
package engine

import (
	"context"
	"runtime/debug"
	"sync"
	"time"

	"cursortab/buffer"
	"cursortab/history"
	"cursortab/logger"
	"cursortab/metrics"
	"cursortab/text"
	"cursortab/types"
)

// Timer represents a timer that can be stopped.
type Timer interface {
	Stop() bool
}

// Clock provides time-related operations for dependency injection.
type Clock interface {
	AfterFunc(d time.Duration, f func()) Timer
	Now() time.Time
}

// SystemClock is the default Clock implementation using the standard library.
var SystemClock Clock = systemClock{}

type systemClock struct{}

func (systemClock) AfterFunc(d time.Duration, f func()) Timer {
	return time.AfterFunc(d, f)
}

func (systemClock) Now() time.Time {
	return time.Now()
}

// Deps are the collaborators of an engine.
type Deps struct {
	Provider Provider
	Buffer   Buffer
	Clock    Clock
	// History is optional; a new store is used when nil.
	History *history.Store
	// Metrics is an optional extra sender, combined with the provider when
	// the provider also sends metrics.
	Metrics metrics.Sender
	// StateNotifier is optional. It receives engine status changes on the
	// engine goroutine and must not block.
	StateNotifier func(types.EngineStatus)
}

type Engine struct {
	provider      Provider
	buffer        Buffer
	history       *history.Store
	clock         Clock
	config        Config
	stateNotifier func(types.EngineStatus)
	lastNotified  types.EngineStatus

	// ctx is the engine lifetime. Requests derive from it; Close cancels it.
	ctx    context.Context
	cancel context.CancelFunc
	// events carries editor, timer, and request events to the engine goroutine.
	events chan Event
	// queue holds events raised while an event is handled. They run right
	// after it, so the engine never blocks sending to its own channel.
	queue []Event

	lifecycle sync.Mutex
	running   bool
	closed    bool
	done      chan struct{}

	state        state
	inInsertMode bool

	idleTimer       Timer
	textChangeTimer Timer

	completionRequestID uint64
	currentCancel       context.CancelFunc
	prefetchRequestID   uint64
	prefetch            prefetchSlot
	stream              *activeStream

	display          displayedCompletion
	cursorTarget     *types.CursorPredictionTarget
	stagedCompletion *text.StagedCompletion

	rejectedCompletions map[string][]*rejectedCompletion

	// User action tracking for RecentUserActions
	userActions      []*types.UserAction // Last defaultMaxUserActions actions
	lastBufferSize   int                 // Bytes at the last text change, -1 before the first
	lastCursorOffset int                 // For cursor movement detection

	// Metrics tracking (engine owns state, provider implements Sender)
	metricSender           metrics.Sender
	metricsCh              chan metrics.Event
	currentMetrics         metrics.CompletionInfo
	currentSnapshot        *metrics.Snapshot
	lastCompletionSource   types.CompletionSource
	completionsSinceAccept int
	pendingMetricsInfo     *types.MetricsInfo // From the provider response, recorded when shown
}

// New creates an engine. Call Run to process events and Close to stop it.
func New(deps Deps, config Config) *Engine {
	ctx, cancel := context.WithCancel(context.Background())
	e := &Engine{
		provider:            deps.Provider,
		buffer:              deps.Buffer,
		history:             deps.History,
		clock:               deps.Clock,
		config:              config,
		stateNotifier:       deps.StateNotifier,
		ctx:                 ctx,
		cancel:              cancel,
		events:              make(chan Event, 100),
		done:                make(chan struct{}),
		state:               stateIdle,
		rejectedCompletions: make(map[string][]*rejectedCompletion),
		lastBufferSize:      -1,
	}
	if e.history == nil {
		e.history = history.NewStore(defaultMaxRecentSnapshots)
	}

	var providerSender metrics.Sender
	if !config.DisableProviderMetrics {
		providerSender, _ = deps.Provider.(metrics.Sender)
	}
	switch {
	case providerSender != nil && deps.Metrics != nil:
		e.metricSender = metrics.NewMultiSender(providerSender, deps.Metrics)
	case providerSender != nil:
		e.metricSender = providerSender
	case deps.Metrics != nil:
		e.metricSender = deps.Metrics
	}
	if e.metricSender != nil {
		e.metricsCh = make(chan metrics.Event, 64)
	}
	return e
}

// Run processes events until Close is called. It publishes the initial status
// first and shuts the engine down before returning.
func (e *Engine) Run() {
	e.lifecycle.Lock()
	if e.running || e.closed {
		e.lifecycle.Unlock()
		return
	}
	e.running = true
	e.lifecycle.Unlock()
	defer close(e.done)

	if e.metricSender != nil {
		go e.metricsWorker()
	}
	logger.Info("engine started")
	e.notifyStateChange(true)

	for {
		var lines <-chan string
		if e.stream != nil {
			lines = e.stream.lines
		}
		select {
		case <-e.ctx.Done():
			e.shutdown()
			return
		case line, ok := <-lines:
			e.safely("stream line", func() { e.handleStreamEvent(line, ok) })
		case event := <-e.events:
			e.safely(string(event.Type), func() { e.handleEvent(event) })
		}
	}
}

// Close stops the engine: it cancels all requests and timers. It leaves the
// UI to the editor, which may already be gone. It waits for Run to return
// when Run is active.
func (e *Engine) Close() {
	e.cancel()
	e.lifecycle.Lock()
	running := e.running
	alreadyClosed := e.closed
	e.closed = true
	e.lifecycle.Unlock()
	if running {
		<-e.done
	} else if !alreadyClosed {
		e.shutdown()
	}
}

// safely runs f and recovers from a panic by dropping the in-progress
// completion, so one bad event cannot stop the session.
func (e *Engine) safely(what string, f func()) {
	defer func() {
		if r := recover(); r != nil {
			logger.Error("engine panic while handling %s: %v\n%s", what, r, debug.Stack())
			e.queue = nil
			func() {
				defer func() {
					if r := recover(); r != nil {
						logger.Error("engine panic while recovering: %v", r)
					}
				}()
				e.reject()
				e.notifyStateChange(false)
			}()
		}
	}()
	f()
}

func (e *Engine) shutdown() {
	logger.Info("stopping engine...")
	e.cancelInFlight()
	e.cancelPrefetch()
	e.stopIdleTimer()
	e.stopTextChangeTimer()
	e.state = stateIdle
	e.cursorTarget = nil
	e.stagedCompletion = nil
	e.resetCompletionFields()
	e.notifyStateChange(false)
	if e.metricsCh != nil {
		close(e.metricsCh)
	}
	logger.Info("engine stopped")
}

// HandleEditorEvent queues a known editor event with the cursor and buffer
// size at the time the editor sent it. Safe to call from any goroutine;
// events after Close are dropped.
func (e *Engine) HandleEditorEvent(event string, pos buffer.Position) {
	if eventType := EventTypeFromString(event); eventType != "" {
		e.send(Event{Type: eventType, Position: pos})
	}
}

// send delivers an event from another goroutine to the engine goroutine.
func (e *Engine) send(event Event) {
	select {
	case e.events <- event:
	case <-e.ctx.Done():
	}
}

// post queues an event raised on the engine goroutine.
func (e *Engine) post(event Event) {
	e.queue = append(e.queue, event)
}

// notifyStateChange publishes a coherent status snapshot after an event,
// rather than intermediate states during cancellation, replacement, or
// acceptance. force publishes even an unchanged status.
func (e *Engine) notifyStateChange(force bool) {
	if e.stateNotifier == nil {
		return
	}
	status := types.EngineStatus{
		State:   e.state.String(),
		Pending: e.currentCancel != nil || e.stream != nil || e.hasInflightPrefetch(),
	}
	if force || status != e.lastNotified {
		e.lastNotified = status
		e.stateNotifier(status)
	}
}

// resetCompletionFields drops the displayed completion and ends its metrics.
func (e *Engine) resetCompletionFields() {
	e.display.reset()
	e.pendingMetricsInfo = nil
	e.currentMetrics = metrics.CompletionInfo{}
	e.currentSnapshot = nil
}

func (e *Engine) cancelCurrentRequest() {
	if e.currentCancel != nil {
		e.currentCancel()
		e.currentCancel = nil
	}
}

// cancelPrefetch cancels any in-flight prefetch and discards buffered results.
// "Cancel" here means both: stop the request if running, and drop any completion
// that was already returned but not yet consumed.
func (e *Engine) cancelPrefetch() {
	if e.prefetch.inflight != nil && e.prefetch.inflight.cancel != nil {
		e.prefetch.inflight.cancel()
	}
	e.clearPrefetch()
}

// clearPrefetch resets the in-memory prefetch result and request state.
// Does not cancel an in-flight prefetch (use cancelPrefetch for that).
func (e *Engine) clearPrefetch() {
	e.prefetch = prefetchSlot{}
}

func (e *Engine) storeReadyPrefetch(resp *types.CompletionResponse, manual bool) {
	e.prefetch.inflight = nil
	e.prefetch.ready = &prefetchedCompletion{CompletionResponse: resp, Manual: manual}
}

func (e *Engine) hasInflightPrefetch() bool {
	return e.prefetch.inflight != nil
}

func (e *Engine) inflightPrefetchWait() prefetchWait {
	if e.prefetch.inflight == nil {
		return prefetchNoWait
	}
	return e.prefetch.inflight.wait
}

func (e *Engine) setInflightPrefetchWait(wait prefetchWait) {
	if e.prefetch.inflight != nil {
		e.prefetch.inflight.wait = wait
	}
}

func (e *Engine) currentPrefetchRequestID() uint64 {
	if e.prefetch.inflight == nil {
		return 0
	}
	return e.prefetch.inflight.requestID
}

func (e *Engine) readyPrefetch() *prefetchedCompletion {
	return e.prefetch.ready
}

func (e *Engine) readyPrefetchCompletion() *types.Completion {
	if prefetch := e.readyPrefetch(); prefetch != nil {
		return prefetch.Completion
	}
	return nil
}

// Timer management

func (e *Engine) startIdleTimer() {
	e.stopIdleTimer()
	e.idleTimer = e.startTimer(e.config.IdleCompletionDelay, EventIdleTimeout)
}

func (e *Engine) stopIdleTimer() {
	if e.idleTimer != nil {
		e.idleTimer.Stop()
		e.idleTimer = nil
	}
}

func (e *Engine) resetIdleTimer() {
	e.startIdleTimer()
}

func (e *Engine) startTextChangeTimer() {
	e.stopTextChangeTimer()
	e.textChangeTimer = e.startTimer(e.config.TextChangeDebounce, EventTextChangeTimeout)
}

func (e *Engine) stopTextChangeTimer() {
	if e.textChangeTimer != nil {
		e.textChangeTimer.Stop()
		e.textChangeTimer = nil
	}
}

// startTimer sends eventType after delay. A negative delay disables the timer,
// as do modes with completions turned off.
func (e *Engine) startTimer(delay time.Duration, eventType EventType) Timer {
	if delay < 0 || !e.isModeEnabled(false) {
		return nil
	}
	return e.clock.AfterFunc(delay, func() { e.send(Event{Type: eventType}) })
}

// isModeEnabled returns true if completions are enabled for the current mode
// or if the current request was manually triggered.
func (e *Engine) isModeEnabled(manual bool) bool {
	if manual {
		return true
	}
	if e.inInsertMode {
		return e.config.CompleteInInsert
	}
	return e.config.CompleteInNormal
}

// recordUserAction adds an action to the ring buffer, evicting oldest if full
func (e *Engine) recordUserAction(action *types.UserAction) {
	if len(e.userActions) >= defaultMaxUserActions {
		e.userActions = e.userActions[1:]
	}
	e.userActions = append(e.userActions, action)
}

// recordTextChangeAction classifies and records a text change from the
// buffer size the editor sent with it, since the synced lines may be older
// than the change.
func (e *Engine) recordTextChangeAction(pos buffer.Position) {
	lastSize := e.lastBufferSize
	e.lastBufferSize = pos.Size
	if lastSize < 0 {
		return
	}
	actionType := classifySizeChange(lastSize, pos.Size)
	if actionType == "" {
		return
	}
	e.recordUserAction(&types.UserAction{
		ActionType:  actionType,
		FilePath:    e.buffer.Path(),
		LineNumber:  pos.Row,
		Offset:      pos.Offset,
		TimestampMs: e.clock.Now().UnixMilli(),
	})
}

// recordCursorMovementAction records a cursor movement if position changed
func (e *Engine) recordCursorMovementAction(pos buffer.Position) {
	if pos.Offset == e.lastCursorOffset {
		return
	}
	e.recordUserAction(&types.UserAction{
		ActionType:  types.ActionCursorMovement,
		FilePath:    e.buffer.Path(),
		LineNumber:  pos.Row,
		Offset:      pos.Offset,
		TimestampMs: e.clock.Now().UnixMilli(),
	})
	e.lastCursorOffset = pos.Offset
}

// classifySizeChange determines the action type of an edit from the buffer
// size before and after it.
func classifySizeChange(oldSize, newSize int) types.UserActionType {
	switch delta := newSize - oldSize; {
	case delta == 1:
		return types.ActionInsertChar
	case delta > 1:
		return types.ActionInsertSelection
	case delta == -1:
		return types.ActionDeleteChar
	case delta < -1:
		return types.ActionDeleteSelection
	default:
		return ""
	}
}
