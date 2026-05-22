package engine

import (
	"context"
	"cursortab/buffer"
	"cursortab/text"
	"cursortab/types"
	"sync"
	"time"
)

// --- Mock implementations ---

// mockBuffer implements the Buffer interface for testing
type mockBuffer struct {
	mu             sync.Mutex
	lines          []string
	row            int
	col            int
	path           string
	version        int
	viewportTop    int
	viewportBottom int
	previousLines  []string
	originalLines  []string
	diffHistories  []*types.DiffEntry
	// Track method calls
	syncCalls              int
	clearUICalls           int
	commitPendingCalls     int
	showCursorTargetLine   int
	prepareCompletionCalls int
	lastPreparedCompletion struct {
		startLine  int
		endLineInc int
		lines      []string
	}

	// Partial accept tracking
	lastInsertedText    string
	lastInsertLine      int
	lastInsertCol       int
	lastReplacedLine    int
	lastReplacedContent string
}

func newMockBuffer() *mockBuffer {
	return &mockBuffer{
		lines:          []string{"line 1", "line 2", "line 3"},
		row:            1,
		col:            0,
		path:           "test.go",
		version:        1,
		viewportTop:    1,
		viewportBottom: 50,
	}
}

func (b *mockBuffer) Sync(workspacePath string) (*buffer.SyncResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.syncCalls++
	return &buffer.SyncResult{BufferChanged: false}, nil
}

func (b *mockBuffer) Lines() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lines
}

func (b *mockBuffer) Row() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.row
}

func (b *mockBuffer) Col() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.col
}

func (b *mockBuffer) Path() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.path
}

func (b *mockBuffer) Version() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.version
}

func (b *mockBuffer) ViewportBounds() (top, bottom int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.viewportTop, b.viewportBottom
}

func (b *mockBuffer) AvailableWidth() int {
	return 120
}

func (b *mockBuffer) PreviousLines() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.previousLines
}

func (b *mockBuffer) OriginalLines() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.originalLines
}

func (b *mockBuffer) DiffHistories() []*types.DiffEntry {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.diffHistories
}

func (b *mockBuffer) DiskLines() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return nil
}

func (b *mockBuffer) SetFileContext(ctx buffer.FileContext) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.previousLines = ctx.PreviousLines
	b.originalLines = ctx.OriginalLines
	b.diffHistories = ctx.DiffHistories
}

func (b *mockBuffer) HasChanges(startLine, endLineInc int, lines []string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	// Simple implementation: check if any line differs
	for i := startLine; i <= endLineInc && i-1 < len(b.lines); i++ {
		relIdx := i - startLine
		if relIdx < len(lines) {
			if i-1 < len(b.lines) && lines[relIdx] != b.lines[i-1] {
				return true
			}
		}
	}
	return len(lines) != (endLineInc - startLine + 1)
}

func (b *mockBuffer) PrepareCompletion(startLine, endLineInc int, lines []string, groups []*text.Group) buffer.Batch {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.prepareCompletionCalls++
	b.lastPreparedCompletion.startLine = startLine
	b.lastPreparedCompletion.endLineInc = endLineInc
	b.lastPreparedCompletion.lines = lines
	return &mockBatch{}
}

func (b *mockBuffer) CommitPending() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.commitPendingCalls++
}

func (b *mockBuffer) CommitUserEdits() bool {
	return false
}

func (b *mockBuffer) ClearDiffHistory() {
}

func (b *mockBuffer) IsModified() bool {
	return true // Default to modified so completions aren't suppressed in tests
}

func (b *mockBuffer) CursorScopes() []string { return nil }

func (b *mockBuffer) SkipHistory() bool {
	return false
}

func (b *mockBuffer) ShowCursorTarget(line int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.showCursorTargetLine = line
	return nil
}

func (b *mockBuffer) ClearUI() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.clearUICalls++
	return nil
}

func (b *mockBuffer) MoveCursor(line int, center, mark bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.row = line
	return nil
}

func (b *mockBuffer) RegisterEventHandler(handler func(event string)) error {
	return nil
}

func (b *mockBuffer) InsertText(line, col int, text string, keepUI bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lastInsertLine = line
	b.lastInsertCol = col
	b.lastInsertedText = text
	// Also update lines for state consistency
	if line >= 1 && line <= len(b.lines) {
		idx := line - 1
		currentLine := b.lines[idx]
		if col <= len(currentLine) {
			b.lines[idx] = currentLine[:col] + text + currentLine[col:]
		}
	}
	return nil
}

func (b *mockBuffer) ReplaceLine(line int, content string, keepUI bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lastReplacedLine = line
	b.lastReplacedContent = content
	if line >= 1 && line <= len(b.lines) {
		b.lines[line-1] = content
	}
	return nil
}

func (b *mockBuffer) InsertLine(line int, content string, keepUI bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	// Insert new line at position, pushing existing lines down
	if line >= 1 && line <= len(b.lines)+1 {
		newLines := make([]string, len(b.lines)+1)
		copy(newLines[:line-1], b.lines[:line-1])
		newLines[line-1] = content
		copy(newLines[line:], b.lines[line-1:])
		b.lines = newLines
	}
	return nil
}

// mockBatch implements buffer.Batch
type mockBatch struct {
	executed bool
	err      error
}

func (b *mockBatch) Execute() error {
	b.executed = true
	return b.err
}

// mockProvider implements the Provider interface for testing
type mockProvider struct {
	mu              sync.Mutex
	completionResp  *types.CompletionResponse
	completionErr   error
	completionCalls int
	lastRequest     *types.CompletionRequest
}

func newMockProvider() *mockProvider {
	return &mockProvider{
		completionResp: &types.CompletionResponse{
			Completions: []*types.Completion{{
				StartLine:  1,
				EndLineInc: 1,
				Lines:      []string{"completed line 1"},
			}},
		},
	}
}

func (p *mockProvider) GetContextLimits() ContextLimits {
	return DefaultContextLimits()
}

func (p *mockProvider) GetCompletion(ctx context.Context, req *types.CompletionRequest) (*types.CompletionResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.completionCalls++
	p.lastRequest = req
	if p.completionErr != nil {
		return nil, p.completionErr
	}
	return p.completionResp, nil
}

// mockClock implements Clock for testing
type mockClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*mockTimer
}

func newMockClock() *mockClock {
	return &mockClock{
		now: time.Now(),
	}
}

func (c *mockClock) AfterFunc(d time.Duration, f func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &mockTimer{
		fireTime: c.now.Add(d),
		f:        f,
		stopped:  false,
	}
	c.timers = append(c.timers, t)
	return t
}

func (c *mockClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *mockClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	// Copy timers to avoid holding lock during callback
	var toFire []*mockTimer
	for _, t := range c.timers {
		if !t.stopped && !t.now.After(c.now) {
			toFire = append(toFire, t)
		}
	}
	c.mu.Unlock()

	for _, t := range toFire {
		t.fire()
	}
}

type mockTimer struct {
	fireTime time.Time
	now      time.Time
	f        func()
	stopped  bool
	mu       sync.Mutex
}

func (t *mockTimer) Stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	wasActive := !t.stopped
	t.stopped = true
	return wasActive
}

func (t *mockTimer) fire() {
	t.mu.Lock()
	if t.stopped {
		t.mu.Unlock()
		return
	}
	t.stopped = true
	f := t.f
	t.mu.Unlock()
	if f != nil {
		f()
	}
}

// --- Helper functions ---

func createTestEngine(buf *mockBuffer, prov *mockProvider, clock *mockClock) *Engine {
	eng, _ := NewEngine(prov, buf, EngineConfig{
		NsID:                1,
		CompletionTimeout:   5 * time.Second,
		IdleCompletionDelay: 500 * time.Millisecond,
		TextChangeDebounce:  100 * time.Millisecond,
		CursorPrediction: CursorPredictionConfig{
			Enabled:            true,
			AutoAdvance:        true,
			ProximityThreshold: 3,
		},
		CompleteInInsert:       true,
		CompleteInNormal:       true,
		EditCompletionProvider: true,
	}, clock, nil, nil)
	return eng
}

// createTestEngineWithContext creates an engine with mainCtx set (needed for prefetch tests)
func createTestEngineWithContext(buf *mockBuffer, prov *mockProvider, clock *mockClock) (*Engine, context.CancelFunc) {
	eng := createTestEngine(buf, prov, clock)
	ctx, cancel := context.WithCancel(context.Background())
	eng.mainCtx = ctx
	eng.mainCancel = cancel
	return eng, cancel
}
