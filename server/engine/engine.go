package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"cursortab/buffer"
	"cursortab/ctx"
	"cursortab/logger"
	"cursortab/metrics"
	"cursortab/text"
	"cursortab/types"
)

var actionAbbrev = map[types.UserActionType]string{
	types.ActionInsertChar:      "IC",
	types.ActionInsertSelection: "IS",
	types.ActionDeleteChar:      "DC",
	types.ActionDeleteSelection: "DS",
	types.ActionCursorMovement:  "CM",
}

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

type Engine struct {
	WorkspacePath string
	WorkspaceID   string

	provider        Provider
	buffer          Buffer
	clock           Clock
	state           state
	ctx             context.Context
	currentCancel   context.CancelFunc
	prefetchCancel  context.CancelFunc
	idleTimer       Timer
	textChangeTimer Timer
	mu              sync.RWMutex
	eventChan       chan Event

	// Main context and cancel for the engine lifecycle
	mainCtx    context.Context
	mainCancel context.CancelFunc
	stopped    bool
	stopOnce   sync.Once

	// Completion state
	completions  []*types.Completion
	applyBatch   buffer.Batch
	cursorTarget *types.CursorPredictionTarget

	// Staged completion state (for multi-stage completions)
	stagedCompletion *text.StagedCompletion

	// Original buffer lines when completion was shown (for partial typing optimization)
	completionOriginalLines []string

	// Current groups for partial accept (stored when showing completion)
	currentGroups []*text.Group

	// Prefetch state
	prefetchedCompletions  []*types.Completion
	prefetchedCursorTarget *types.CursorPredictionTarget
	prefetchState          prefetchState

	// Streaming state (line-by-line)
	streamingState          *StreamingState
	streamingCancel         context.CancelFunc
	streamLinesChan         <-chan string // Lines channel (nil when not streaming)
	streamLineNum           int           // Line counter for current stream
	acceptedDuringStreaming bool          // True if user accepted partial during streaming

	// Token streaming state (token-by-token for inline)
	tokenStreamingState *TokenStreamingState
	tokenStreamChan     <-chan string // Token stream channel (nil when not streaming)

	// Context gatherer for additional completion context
	contextGatherer *ctx.Gatherer

	// Mode tracking
	inInsertMode      bool
	manuallyTriggered bool

	// Config options
	config        EngineConfig
	contextLimits ContextLimits

	// Per-file state that persists across file switches (for context restoration)
	fileStateStore map[string]*FileState

	// User action tracking for RecentUserActions
	userActions      []*types.UserAction // Ring buffer of last MaxUserActions actions
	lastBufferLines  []string            // For detecting text changes
	lastCursorOffset int                 // For cursor movement detection

	// Metrics tracking (engine owns state, provider implements Sender)
	metricSender    metrics.Sender
	currentMetrics  metrics.CompletionInfo
	currentSnapshot *metrics.Snapshot
	contextResultCh chan *types.ContextResult // async context gather for snapshot
	metricsCh       chan metrics.Event

	lastCompletionSource      types.CompletionSource
	completionsSinceAccept    int
	pendingMetricsInfo        *types.MetricsInfo // stored from batch completion for showCurrentStage
	rejectedCompletions       map[string][]*rejectedCompletion
	currentRejectedCompletion *rejectedCompletion
}

// NewEngine creates a new Engine instance.
// communitySender is optional — pass nil to disable community metrics.
func NewEngine(provider Provider, buf Buffer, config EngineConfig, clock Clock, contextGatherer *ctx.Gatherer, communitySender metrics.Sender) (*Engine, error) {
	workspacePath, err := os.Getwd()
	if err != nil {
		logger.Warn("error getting current directory, using home: %v", err)
		workspacePath = "~"
	}
	workspaceID := fmt.Sprintf("%s-%d", workspacePath, os.Getpid())

	e := &Engine{
		WorkspacePath:          workspacePath,
		WorkspaceID:            workspaceID,
		provider:               provider,
		buffer:                 buf,
		clock:                  clock,
		contextGatherer:        contextGatherer,
		state:                  stateIdle,
		ctx:                    nil,
		eventChan:              make(chan Event, 100),
		config:                 config,
		contextLimits:          provider.GetContextLimits(),
		idleTimer:              nil,
		textChangeTimer:        nil,
		mu:                     sync.RWMutex{},
		completions:            nil,
		cursorTarget:           nil,
		prefetchedCompletions:  nil,
		prefetchedCursorTarget: nil,
		prefetchState:          prefetchNone,
		stopped:                false,
		fileStateStore:         make(map[string]*FileState),
		rejectedCompletions:    make(map[string][]*rejectedCompletion),
	}

	// Initialize metrics: combine provider sender + community sender if available
	var providerSender metrics.Sender
	if !config.DisableProviderMetrics {
		providerSender, _ = provider.(metrics.Sender)
	}
	switch {
	case providerSender != nil && communitySender != nil:
		e.metricSender = metrics.NewMultiSender(providerSender, communitySender)
	case providerSender != nil:
		e.metricSender = providerSender
	case communitySender != nil:
		e.metricSender = communitySender
	}
	if e.metricSender != nil {
		e.metricsCh = make(chan metrics.Event, 64)
	}

	return e, nil
}

// Start begins the engine event loop.
func (e *Engine) Start(ctx context.Context) {
	e.mu.Lock()
	if e.stopped {
		e.mu.Unlock()
		return
	}

	e.mainCtx, e.mainCancel = context.WithCancel(ctx)
	e.mu.Unlock()

	go e.eventLoop(e.mainCtx)
	if e.metricSender != nil {
		go e.metricsWorker(e.mainCtx)
	}
	logger.Info("engine started")
}

// Stop gracefully shuts down the engine and cleans up all resources.
func (e *Engine) Stop() {
	e.stopOnce.Do(func() {
		e.mu.Lock()
		defer e.mu.Unlock()

		logger.Info("stopping engine...")

		e.stopped = true
		e.cancelCurrentRequest()
		e.cancelPrefetch()
		e.stopIdleTimer()
		e.stopTextChangeTimer()
		e.state = stateIdle
		e.cursorTarget = nil
		e.completions = nil
		e.applyBatch = nil
		e.stagedCompletion = nil
		e.completionOriginalLines = nil
		// Cancel the main context to signal all in-flight goroutines and
		// loops to exit. We deliberately do NOT close eventChan or metricsCh:
		// senders use `select { case ch <- ...: case <-mainCtx.Done(): }` and
		// closing the channel would race with that select (Go picks randomly
		// between a closed-channel-send and a Done-channel-recv when both are
		// ready). Letting the channels be GC'd avoids the panic class
		// entirely; the eventLoop and metricsWorker exit on Done.
		if e.mainCancel != nil {
			e.mainCancel()
		}

		logger.Info("engine stopped")
	})
}

// resetCompletionFields clears per-completion state. Used after accepting a
// stage to prepare for the next one. Does not cancel requests, drop the staged
// completion, or send metrics.
func (e *Engine) resetCompletionFields() {
	e.completions = nil
	e.applyBatch = nil
	e.completionOriginalLines = nil
	e.currentGroups = nil
	e.currentRejectedCompletion = nil
	e.manuallyTriggered = false
	e.pendingMetricsInfo = nil
	e.contextResultCh = nil
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
	if e.prefetchCancel != nil {
		e.prefetchCancel()
		e.prefetchCancel = nil
	}
	e.clearPrefetchResult()
}

// clearPrefetchResult resets the in-memory prefetch result and state.
// Does not cancel an in-flight prefetch (use cancelPrefetch for that).
func (e *Engine) clearPrefetchResult() {
	e.prefetchState = prefetchNone
	e.prefetchedCompletions = nil
	e.prefetchedCursorTarget = nil
}

// RegisterEventHandler registers the event handler for nvim RPC callbacks.
func (e *Engine) RegisterEventHandler() {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.stopped {
		return
	}

	if err := e.buffer.RegisterEventHandler(func(event string) {
		e.mu.RLock()
		stopped := e.stopped
		e.mu.RUnlock()

		if stopped {
			return
		}

		eventType := EventTypeFromString(event)
		if eventType != "" {
			select {
			case e.eventChan <- Event{Type: eventType, Data: nil}:
			case <-e.mainCtx.Done():
				return
			}
		}
	}); err != nil {
		logger.Error("error registering event handler for new connection: %v", err)
	}
}

// Timer management

func (e *Engine) startIdleTimer() {
	// When delay is -1, idle completions are disabled
	if e.config.IdleCompletionDelay < 0 {
		return
	}
	if !e.isModeEnabled() {
		return
	}
	e.stopIdleTimer()
	e.idleTimer = e.clock.AfterFunc(e.config.IdleCompletionDelay, func() {
		e.mu.RLock()
		stopped := e.stopped
		mainCtx := e.mainCtx
		e.mu.RUnlock()

		if stopped || mainCtx == nil {
			return
		}

		select {
		case e.eventChan <- Event{Type: EventIdleTimeout}:
		case <-mainCtx.Done():
		}
	})
}

func (e *Engine) stopIdleTimer() {
	if e.idleTimer != nil {
		e.idleTimer.Stop()
		e.idleTimer = nil
	}
}

func (e *Engine) resetIdleTimer() {
	e.stopIdleTimer()
	e.startIdleTimer()
}

func (e *Engine) startTextChangeTimer() {
	// When debounce is -1, automatic text change completions are disabled
	if e.config.TextChangeDebounce < 0 {
		return
	}
	if !e.isModeEnabled() {
		return
	}
	e.stopTextChangeTimer()
	e.textChangeTimer = e.clock.AfterFunc(e.config.TextChangeDebounce, func() {
		e.mu.RLock()
		stopped := e.stopped
		mainCtx := e.mainCtx
		e.mu.RUnlock()

		if stopped || mainCtx == nil {
			return
		}

		select {
		case e.eventChan <- Event{Type: EventTextChangeTimeout, Data: nil}:
		case <-mainCtx.Done():
		}
	})
}

func (e *Engine) stopTextChangeTimer() {
	if e.textChangeTimer != nil {
		e.textChangeTimer.Stop()
		e.textChangeTimer = nil
	}
}

// isModeEnabled returns true if completions are enabled for the current mode
// or if the completion was manually triggered.
func (e *Engine) isModeEnabled() bool {
	if e.manuallyTriggered {
		return true
	}
	if e.inInsertMode {
		return e.config.CompleteInInsert
	}
	return e.config.CompleteInNormal
}

// recordUserAction adds an action to the ring buffer, evicting oldest if full
func (e *Engine) recordUserAction(action *types.UserAction) {
	if e.contextLimits.MaxUserActions < 0 {
		return
	}
	if len(e.userActions) >= e.contextLimits.MaxUserActions {
		e.userActions = e.userActions[1:] // Evict oldest
	}
	e.userActions = append(e.userActions, action)
}

// getUserActionsForFile returns all tracked actions for the given file path
func (e *Engine) getUserActionsForFile(filePath string) []*types.UserAction {
	var result []*types.UserAction
	for _, a := range e.userActions {
		if a.FilePath == filePath {
			result = append(result, a)
		}
	}
	return result
}

// recordTextChangeAction classifies and records a text change action
func (e *Engine) recordTextChangeAction() {
	currentLines := e.buffer.Lines()

	if e.lastBufferLines == nil {
		e.lastBufferLines = slices.Clone(currentLines)
		return
	}

	// Classify the action based on diff
	actionType := classifyEdit(e.lastBufferLines, currentLines)
	if actionType == "" {
		e.lastBufferLines = slices.Clone(currentLines)
		return
	}

	e.recordUserAction(&types.UserAction{
		ActionType:  actionType,
		FilePath:    e.buffer.Path(),
		LineNumber:  e.buffer.Row(),
		Offset:      calculateOffset(currentLines, e.buffer.Row(), e.buffer.Col()),
		TimestampMs: e.clock.Now().UnixMilli(),
	})

	e.lastBufferLines = slices.Clone(currentLines)
}

// recordCursorMovementAction records a cursor movement if position changed
func (e *Engine) recordCursorMovementAction() {
	currentOffset := calculateOffset(e.buffer.Lines(), e.buffer.Row(), e.buffer.Col())
	if currentOffset != e.lastCursorOffset {
		e.recordUserAction(&types.UserAction{
			ActionType:  types.ActionCursorMovement,
			FilePath:    e.buffer.Path(),
			LineNumber:  e.buffer.Row(),
			Offset:      currentOffset,
			TimestampMs: e.clock.Now().UnixMilli(),
		})
		e.lastCursorOffset = currentOffset
	}
}

// classifyEdit determines the action type based on character count changes
func classifyEdit(oldLines, newLines []string) types.UserActionType {
	oldLen := totalChars(oldLines)
	newLen := totalChars(newLines)

	inserted := max(0, newLen-oldLen)
	deleted := max(0, oldLen-newLen)

	switch {
	case deleted == 0 && inserted == 1:
		return types.ActionInsertChar
	case deleted == 0 && inserted > 1:
		return types.ActionInsertSelection
	case deleted == 1 && inserted == 0:
		return types.ActionDeleteChar
	case deleted > 1 && inserted == 0:
		return types.ActionDeleteSelection
	case inserted > 0:
		return types.ActionInsertSelection // Replace = delete + insert
	default:
		return ""
	}
}

// calculateOffset computes byte offset from line/column position
func calculateOffset(lines []string, row, col int) int {
	offset := 0
	for i := 0; i < row-1 && i < len(lines); i++ {
		offset += len(lines[i]) + 1 // +1 for newline
	}
	if row >= 1 && row <= len(lines) {
		offset += min(col, len(lines[row-1]))
	}
	return offset
}

// totalChars counts total characters including newlines
func totalChars(lines []string) int {
	total := 0
	for _, line := range lines {
		total += len(line) + 1
	}
	return total
}

// Metrics tracking

// recordMetricsShown records that a completion was shown. Pass nil for info
// when no provider metrics ID is available (e.g. streaming completions).
func (e *Engine) recordMetricsShown(info *types.MetricsInfo) {
	now := e.clock.Now()
	e.currentMetrics = metrics.CompletionInfo{ShownAt: now}

	if info != nil && info.ID != "" {
		e.currentMetrics.ID = info.ID
		e.currentMetrics.Additions = info.Additions
		e.currentMetrics.Deletions = info.Deletions
	} else if len(e.completions) > 0 {
		// Estimate additions/deletions from completion line counts
		comp := e.completions[0]
		bufferLines := e.buffer.Lines()
		origCount := 0
		for i := comp.StartLine; i <= comp.EndLineInc && i-1 < len(bufferLines); i++ {
			origCount++
		}
		newCount := len(comp.Lines)
		if newCount > origCount {
			e.currentMetrics.Additions = newCount - origCount
		} else if origCount > newCount {
			e.currentMetrics.Deletions = origCount - newCount
		}
	}

	e.currentSnapshot = e.captureSnapshot()
	e.gatherContextForSnapshot()
	e.sendMetric(metrics.EventShown)
}

func (e *Engine) gatherContextForSnapshot() {
	if e.contextGatherer == nil {
		return
	}
	ch := make(chan *types.ContextResult, 1)
	e.contextResultCh = ch
	filePath := e.buffer.Path()
	row := e.buffer.Row()
	col := e.buffer.Col()
	go func() {
		ch <- e.contextGatherer.Gather(e.mainCtx, &ctx.SourceRequest{
			FilePath:          filePath,
			CursorRow:         row,
			CursorCol:         col,
			WorkspacePath:     e.WorkspacePath,
			MaxDiffBytes:      e.contextLimits.MaxDiffBytes,
			MaxChangedSymbols: e.contextLimits.MaxChangedSymbols,
			MaxSiblings:       e.contextLimits.MaxSiblings,
		})
	}()
}

func (e *Engine) sendMetric(eventType metrics.EventType) {
	if e.metricSender == nil {
		return
	}
	// Need either a provider ID or a snapshot to send anything useful
	if e.currentMetrics.ID == "" && e.currentSnapshot == nil {
		return
	}

	// On outcome events, fill in async context if available
	if eventType != metrics.EventShown && e.currentSnapshot != nil && e.contextResultCh != nil {
		select {
		case result := <-e.contextResultCh:
			if result != nil {
				if result.Diagnostics != nil {
					e.currentSnapshot.HasDiagnostics = len(result.Diagnostics.Items) > 0
				}
				if result.Treesitter != nil {
					e.currentSnapshot.TreesitterScope = classifyScope(result.Treesitter.EnclosingSignature)
				}
			}
		default:
			// Context gather not ready yet — leave defaults
		}
	}

	event := metrics.Event{
		Type:     eventType,
		Info:     e.currentMetrics,
		Snapshot: e.currentSnapshot,
	}

	if eventType != metrics.EventShown {
		e.currentMetrics = metrics.CompletionInfo{}
		e.currentSnapshot = nil
		e.contextResultCh = nil
		if eventType == metrics.EventAccepted {
			e.completionsSinceAccept = 0
		} else {
			e.completionsSinceAccept++
		}
	}

	select {
	case e.metricsCh <- event:
	default:
		logger.Warn("metrics: event queue full, dropping %s event for %s", eventType, event.Info.ID)
	}
}

// classifyScope maps a treesitter enclosing signature to a coarse scope bucket.
func classifyScope(signature string) string {
	if signature == "" {
		return "top_level"
	}
	sig := strings.ToLower(signature)
	switch {
	case strings.Contains(sig, "func") || strings.Contains(sig, "function") ||
		strings.Contains(sig, "method") || strings.Contains(sig, "def "):
		return "function"
	case strings.Contains(sig, "class") || strings.Contains(sig, "struct") ||
		strings.Contains(sig, "impl") || strings.Contains(sig, "interface"):
		return "class"
	case strings.Contains(sig, "comment"):
		return "comment"
	case strings.Contains(sig, "string") || strings.Contains(sig, "template"):
		return "string"
	default:
		return "other"
	}
}

func (e *Engine) captureSnapshot() *metrics.Snapshot {
	lines := e.buffer.Lines()
	row := e.buffer.Row()

	line, col := currentLine(lines, row, e.buffer.Col())
	prefix := line[:col]
	trimmedPrefix := strings.TrimRight(prefix, " \t")

	docLen := documentByteLength(lines)
	cursorOffset := byteOffset(lines, row, col)
	relativePosition := 0.0
	if docLen > 0 {
		relativePosition = (float64(cursorOffset) + 0.5) / (1.0 + float64(docLen))
	}

	lastChar := ""
	if len(prefix) > 0 {
		lastChar = string(prefix[len(prefix)-1])
	}
	lastNWS := ""
	if nwc, ok := lastNonWSChar(line, col); ok {
		lastNWS = string(nwc)
	}

	// Count leading whitespace characters (raw count, not indent units)
	leadingWS := 0
	for _, ch := range line {
		if ch != ' ' && ch != '\t' {
			break
		}
		leadingWS++
	}

	fileExt := strings.ToLower(filepath.Ext(e.buffer.Path()))
	language := extToLanguage[fileExt]
	if language == "" {
		language = "unknown"
	}

	source := "typing"
	if e.lastCompletionSource == types.CompletionSourceIdle {
		source = "idle"
	}

	completionLines := 0
	if len(e.completions) > 0 {
		completionLines = len(e.completions[0].Lines)
	}

	// Diff history stats (edit count, predicted ratio, most recent edit across all files)
	editCount, predictedCount, timeSinceLastEditMs := 0, 0, 0
	now := e.clock.Now()
	if diffs := e.getAllFileDiffHistories(); diffs != nil {
		var latestTimestampNs int64
		for _, fdh := range diffs {
			for _, d := range fdh.DiffHistory {
				editCount++
				if d.Source == types.DiffSourcePredicted {
					predictedCount++
				}
				if d.TimestampNs > latestTimestampNs {
					latestTimestampNs = d.TimestampNs
				}
			}
		}
		if latestTimestampNs > 0 {
			timeSinceLastEditMs = int(now.UnixMilli() - latestTimestampNs/1_000_000)
		}
	}
	predictedEditRatio := 0.0
	if editCount > 0 {
		predictedEditRatio = float64(predictedCount) / float64(editCount)
	}

	typingSpeed := 0.0
	if len(e.userActions) >= 2 {
		insertCount := 0
		for _, a := range e.userActions {
			if a.ActionType == types.ActionInsertChar {
				insertCount++
			}
		}
		first := e.userActions[0]
		last := e.userActions[len(e.userActions)-1]
		if durationSec := float64(last.TimestampMs-first.TimestampMs) / 1000.0; durationSec > 0 {
			typingSpeed = float64(insertCount) / durationSec
		}
	}

	recentActions := make([]string, 0, 5)
	start := len(e.userActions) - 5
	if start < 0 {
		start = 0
	}
	for _, a := range e.userActions[start:] {
		if abbr, ok := actionAbbrev[a.ActionType]; ok {
			recentActions = append(recentActions, abbr)
		}
	}

	stageIndex := 0
	if e.stagedCompletion != nil {
		stageIndex = e.stagedCompletion.CurrentIdx
	}

	cursorTargetDistance := 0
	if e.cursorTarget != nil {
		dist := int(e.cursorTarget.LineNumber) - row
		if dist < 0 {
			dist = -dist
		}
		cursorTargetDistance = dist
	}

	return &metrics.Snapshot{
		FileExt:                fileExt,
		Language:               language,
		PrefixLength:           len(prefix),
		TrimmedPrefixLength:    len(trimmedPrefix),
		LineCount:              len(lines),
		RelativePosition:       relativePosition,
		AfterCursorWS:          afterCursorIsWhitespace(lines, row, col),
		LastChar:               lastChar,
		LastNonWSChar:          lastNWS,
		IndentationLevel:       leadingWS,
		CompletionLines:        completionLines,
		CompletionAdditions:    e.currentMetrics.Additions,
		CompletionDeletions:    e.currentMetrics.Deletions,
		CompletionSource:       source,
		ManuallyTriggered:      e.manuallyTriggered,
		Provider:               e.config.ProviderName,
		StageIndex:             stageIndex,
		CursorTargetDistance:   cursorTargetDistance,
		IsPrefetched:           e.prefetchState == prefetchReady,
		TimeSinceLastEditMs:    timeSinceLastEditMs,
		TypingSpeed:            typingSpeed,
		RecentActions:          recentActions,
		HasDiagnostics:         false,   // filled async in sendMetric
		TreesitterScope:        "other", // filled async in sendMetric
		EditCount:              editCount,
		PredictedEditRatio:     predictedEditRatio,
		CompletionsSinceAccept: e.completionsSinceAccept,
	}
}

// metricsWorker processes metrics events asynchronously.
func (e *Engine) metricsWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-e.metricsCh:
			e.metricSender.SendMetric(ctx, event)
		}
	}
}
