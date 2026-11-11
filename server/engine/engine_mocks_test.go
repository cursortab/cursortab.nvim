package engine

import (
	"context"
	"cursortab/assert"
	"cursortab/buffer"
	"cursortab/ctx"
	"cursortab/history"
	"cursortab/text"
	"cursortab/types"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"
)

// --- Mock implementations ---

// mockBuffer implements the Buffer interface for testing
type mockBuffer struct {
	mu               sync.Mutex
	lines            []string
	row              int
	col              int
	path             string
	viewportTop      int
	viewportBottom   int
	unmodified       bool
	switchOnSync     bool   // Next Sync reports a buffer switch
	pathOnSync       string // Next Sync renames the buffer to this path
	diagnostics      *types.Diagnostics
	treesitter       *types.TreesitterContext
	diagnosticsCalls int
	treesitterCalls  int
	// Track method calls
	syncCalls              int
	clearUICalls           int
	showCursorTargetLine   int
	prepareCompletionCalls int
	lastPreparedCompletion struct {
		startLine  int
		endLineInc int
		lines      []string
	}

	// Partial accept tracking
	lastInsertedText string
}

func newMockBuffer() *mockBuffer {
	return &mockBuffer{
		lines:          []string{"line 1", "line 2", "line 3"},
		row:            1,
		col:            0,
		path:           "test.go",
		viewportTop:    1,
		viewportBottom: 50,
	}
}

func (b *mockBuffer) Sync() (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.syncCalls++
	switched := b.switchOnSync
	b.switchOnSync = false
	if b.pathOnSync != "" {
		b.path, b.pathOnSync = b.pathOnSync, ""
	}
	return switched, nil
}

func (b *mockBuffer) WorkspacePath() string { return "/workspace" }

// Modified defaults to true so completions are not suppressed in tests.
func (b *mockBuffer) Modified() bool { return !b.unmodified }

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

func (b *mockBuffer) ViewportBounds() (top, bottom int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.viewportTop, b.viewportBottom
}

func (b *mockBuffer) AvailableWidth() int {
	return 120
}

func (b *mockBuffer) Diagnostics() *types.Diagnostics {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.diagnosticsCalls++
	return b.diagnostics
}

func (b *mockBuffer) TreesitterSymbols(row int, col int, maxSiblings int) *types.TreesitterContext {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.treesitterCalls++
	return b.treesitter
}

func (b *mockBuffer) PrepareCompletion(startLine, endLineInc int, lines []string, groups []*text.Group) buffer.Batch {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.prepareCompletionCalls++
	b.lastPreparedCompletion.startLine = startLine
	b.lastPreparedCompletion.endLineInc = endLineInc
	b.lastPreparedCompletion.lines = lines
	return &mockApplyBatch{buf: b, edit: history.Edit{
		StartLine:  startLine,
		EndLineInc: endLineInc,
		Lines:      slices.Clone(lines),
	}}
}

// mockApplyBatch applies an edit the way nvim_buf_set_lines does: the range
// must lie in the buffer and must not end before it starts.
type mockApplyBatch struct {
	buf  *mockBuffer
	edit history.Edit
}

func (m *mockApplyBatch) Execute() error {
	b := m.buf
	b.mu.Lock()
	defer b.mu.Unlock()
	start, end := m.edit.StartLine-1, m.edit.EndLineInc
	if start < 0 || end > len(b.lines) {
		return fmt.Errorf("index out of bounds: %d..%d of %d lines", start, end, len(b.lines))
	}
	if start > end {
		return fmt.Errorf("'start' is higher than 'end': %d > %d", start, end)
	}
	b.lines = m.edit.Apply(b.lines)
	return nil
}

func (b *mockBuffer) CursorScopes() []string { return nil }

func (b *mockBuffer) ShowCursorTarget(line int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.showCursorTargetLine = line
}

func (b *mockBuffer) ClearUI() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.clearUICalls++
}

func (b *mockBuffer) MoveCursor(line int, center, mark bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.row = line
	return nil
}

func (b *mockBuffer) AppendText(line int, text string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lastInsertedText = text
	if line < 1 || line > len(b.lines) {
		return fmt.Errorf("index out of bounds: line %d of %d", line, len(b.lines))
	}
	b.lines = slices.Clone(b.lines)
	b.lines[line-1] += text
	return nil
}

func (b *mockBuffer) SetLines(edit history.Edit) error {
	return (&mockApplyBatch{buf: b, edit: edit}).Execute()
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
	mu                              sync.Mutex
	completion                      CompletionKind
	canPrefetchFromSyntheticCurrent bool
	completionResp                  *types.CompletionResponse
	completionErr                   error
	materials                       ctx.Materials
	completionCalls                 int
	lastInput                       ctx.CompletionInput
}

func newMockProvider() *mockProvider {
	return &mockProvider{
		completion:                      CompletionEdit,
		canPrefetchFromSyntheticCurrent: true,
		completionResp: &types.CompletionResponse{
			Completion: &types.Completion{
				StartLine:  1,
				EndLineInc: 1,
				Lines:      []string{"completed line 1"},
			},
		},
	}
}

func newMockProviderWithKind(kind CompletionKind) *mockProvider {
	p := newMockProvider()
	p.completion = kind
	return p
}

func newMockProviderWithLiveEditorState() *mockProvider {
	p := newMockProvider()
	p.canPrefetchFromSyntheticCurrent = false
	return p
}

func completionResponse(completion *types.Completion) *types.CompletionResponse {
	return &types.CompletionResponse{Completion: completion}
}

func showDisplayedCompletionForTest(
	eng *Engine,
	completion *types.Completion,
	originalLines []string,
	groups []*text.Group,
) {
	showDisplayedCompletionWithBatchForTest(eng, completion, nil, originalLines, groups)
}

func showDisplayedCompletionWithBatchForTest(
	eng *Engine,
	completion *types.Completion,
	batch buffer.Batch,
	originalLines []string,
	groups []*text.Group,
) {
	linesAfter := 0
	if completion != nil {
		linesAfter = max(0, len(eng.buffer.Lines())-completion.EndLineInc)
		if buf, ok := eng.buffer.(*mockBuffer); ok && batch == nil {
			batch = &mockApplyBatch{buf: buf, edit: history.Edit{
				StartLine:  completion.StartLine,
				EndLineInc: completion.EndLineInc,
				Lines:      completion.Lines,
			}}
		}
	}
	eng.display.show(
		completion,
		batch,
		originalLines,
		linesAfter,
		groups,
		eng.rejectedCompletionFor(completion),
	)
}

func assertNoPrefetch(t *testing.T, eng *Engine, label string) {
	t.Helper()
	assert.Nil(t, eng.prefetch.inflight, label+" inflight")
	assert.Nil(t, eng.prefetch.ready, label+" ready")
}

func assertReadyPrefetch(t *testing.T, eng *Engine, label string) {
	t.Helper()
	assert.NotNil(t, eng.readyPrefetch(), label)
}

func assertInflightPrefetch(t *testing.T, eng *Engine, wait prefetchWait, label string) {
	t.Helper()
	assert.NotNil(t, eng.prefetch.inflight, label)
	assert.Nil(t, eng.prefetch.ready, label+" ready")
	assert.Equal(t, wait, eng.prefetch.inflight.wait, label+" wait")
}

func seedInflightPrefetch(eng *Engine, wait prefetchWait) uint64 {
	eng.prefetchRequestID++
	requestID := eng.prefetchRequestID
	eng.prefetch = prefetchSlot{inflight: &prefetchInflight{requestID: requestID, wait: wait}}
	return requestID
}

func (p *mockProvider) CompletionKind() CompletionKind {
	return p.completion
}

func (p *mockProvider) CanPrefetchFromSyntheticCurrent() bool {
	return p.canPrefetchFromSyntheticCurrent
}

func (p *mockProvider) RequiredMaterials() ctx.Materials {
	return p.materials
}

func (p *mockProvider) Complete(_ context.Context, input ctx.CompletionInput) (*types.CompletionResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.completionCalls++
	p.lastInput = input
	if p.completionErr != nil {
		return nil, p.completionErr
	}
	return p.completionResp, nil
}

type mockStreamingProvider struct {
	*mockProvider
	stream    CompletionStream
	streamErr error
}

func newMockStreamingProvider(stream CompletionStream) *mockStreamingProvider {
	return &mockStreamingProvider{
		mockProvider: newMockProvider(),
		stream:       stream,
	}
}

func (p *mockStreamingProvider) StreamCompletion(_ context.Context, _ ctx.CompletionInput) (CompletionStream, error) {
	if p.streamErr != nil {
		return nil, p.streamErr
	}
	return p.stream, nil
}

type mockCompletionStream struct {
	lines       chan string
	cancel      context.CancelFunc
	windowStart int
	oldLines    []string
	response    *types.CompletionResponse
	err         error
}

func newMockCompletionStream(cancel context.CancelFunc) *mockCompletionStream {
	return &mockCompletionStream{lines: make(chan string), cancel: cancel}
}

func (s *mockCompletionStream) Lines() <-chan string {
	return s.lines
}

func (s *mockCompletionStream) Window() (int, []string) {
	return s.windowStart, s.oldLines
}

func (s *mockCompletionStream) Cancel() {
	if s.cancel != nil {
		s.cancel()
	}
}

func (s *mockCompletionStream) Finish() (*types.CompletionResponse, error) {
	return s.response, s.err
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

func createTestEngine(buf *mockBuffer, prov Provider, clock *mockClock) *Engine {
	return New(Deps{Provider: prov, Buffer: buf, Clock: clock}, Config{
		CompletionTimeout:   5 * time.Second,
		IdleCompletionDelay: 500 * time.Millisecond,
		TextChangeDebounce:  100 * time.Millisecond,
		CursorPrediction: CursorPredictionConfig{
			Enabled:            true,
			AutoAdvance:        true,
			ProximityThreshold: 3,
		},
		CompleteInInsert: true,
		CompleteInNormal: true,
	})
}

func setStateNotifier(eng *Engine, f func(types.EngineStatus)) {
	eng.stateNotifier = f
}

// waitFor polls cond until it holds. Use it only with a running engine; the
// condition must read state the engine goroutine publishes through callbacks.
func waitFor(t *testing.T, cond func() bool, label string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(label)
		}
		time.Sleep(time.Millisecond)
	}
}
