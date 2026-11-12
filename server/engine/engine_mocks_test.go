package engine

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"cursortab/buffer"
	"cursortab/ctx"
	"cursortab/history"
	"cursortab/metrics"
	"cursortab/text"
	"cursortab/types"
)

// mockBuffer is an in-memory Buffer. It applies the engine's edits and
// records what the engine displays. Tests change it only while the engine is
// settled.
type mockBuffer struct {
	mu             sync.Mutex
	lines          []string
	row            int // 1-indexed
	col            int // 0-indexed byte column
	path           string
	viewportTop    int
	viewportBottom int
	unmodified     bool
	switchOnSync   bool   // Next Sync reports a buffer switch
	pathOnSync     string // Next Sync renames the buffer to this path
	diagnostics    *types.Diagnostics
	treesitter     *types.TreesitterContext
	executeErr     error // Error of applying a displayed completion

	diagnosticsCalls int
	treesitterCalls  int
	renders          int  // Completions displayed
	shown            bool // A completion is displayed
	target           int  // Line of the displayed cursor target, 0 for none
}

func newMockBuffer(lines ...string) *mockBuffer {
	if len(lines) == 0 {
		lines = []string{"line 1", "line 2", "line 3"}
	}
	return &mockBuffer{
		lines:          lines,
		row:            1,
		path:           "test.go",
		viewportTop:    1,
		viewportBottom: 50,
	}
}

func (b *mockBuffer) Sync() (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
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

func (b *mockBuffer) AvailableWidth() int { return 120 }

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

func (b *mockBuffer) CursorScopes() []string { return nil }

func (b *mockBuffer) PrepareCompletion(startLine, endLineInc int, lines []string, groups []*text.Group) buffer.Batch {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.renders++
	b.shown = true
	b.target = 0
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
	if b.executeErr != nil {
		return b.executeErr
	}
	b.shown = false
	return b.apply(m.edit)
}

func (b *mockBuffer) apply(edit history.Edit) error {
	start, end := edit.StartLine-1, edit.EndLineInc
	if start < 0 || end > len(b.lines) {
		return fmt.Errorf("index out of bounds: %d..%d of %d lines", start, end, len(b.lines))
	}
	if start > end {
		return fmt.Errorf("'start' is higher than 'end': %d > %d", start, end)
	}
	b.lines = edit.Apply(b.lines)
	return nil
}

func (b *mockBuffer) ShowCursorTarget(line int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.shown = false
	b.target = line
}

func (b *mockBuffer) ClearUI() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.shown = false
	b.target = 0
}

func (b *mockBuffer) MoveCursor(line int, center, mark bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.row = line
	b.col = len(b.lines[line-1]) - len(strings.TrimLeft(b.lines[line-1], " \t"))
	return nil
}

func (b *mockBuffer) AppendText(line int, txt string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if line < 1 || line > len(b.lines) {
		return fmt.Errorf("index out of bounds: line %d of %d", line, len(b.lines))
	}
	b.lines = slices.Clone(b.lines)
	b.lines[line-1] += txt
	b.row, b.col = line, len(b.lines[line-1])
	return nil
}

func (b *mockBuffer) SetLines(edit history.Edit) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.apply(edit)
}

// position returns the cursor and buffer size as the editor reports them
// with each event.
func (b *mockBuffer) position() buffer.Position {
	b.mu.Lock()
	defer b.mu.Unlock()
	pos := buffer.Position{Row: b.row, Col: b.col}
	for i, line := range b.lines {
		if i < b.row-1 {
			pos.Offset += len(line) + 1
		}
		pos.Size += len(line) + 1
	}
	pos.Offset += b.col
	return pos
}

// mockProvider answers each request with the next queued response, or an
// empty one once the queue is empty, and with err when set. Both are taken
// when the request starts.
type mockProvider struct {
	mu               sync.Mutex
	kind             CompletionKind
	syntheticCurrent bool
	materials        ctx.Materials
	responses        []*types.CompletionResponse
	err              error
	// gate, when set, holds each request until a value arrives, also when
	// the request is cancelled, like a provider that answers late.
	gate   chan struct{}
	inputs []ctx.CompletionInput
	ctxs   []context.Context
}

func newMockProvider() *mockProvider {
	return &mockProvider{kind: CompletionEdit, syntheticCurrent: true}
}

// push queues a response with completion.
func (p *mockProvider) push(completion *types.Completion) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.responses = append(p.responses, &types.CompletionResponse{Completion: completion})
}

// hold makes requests wait for release until the test ends.
func (p *mockProvider) hold(t *testing.T) {
	p.gate = make(chan struct{})
	t.Cleanup(func() { close(p.gate) })
}

// release lets one held request answer.
func (p *mockProvider) release() {
	p.gate <- struct{}{}
}

func (p *mockProvider) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.inputs)
}

func (p *mockProvider) input(i int) ctx.CompletionInput {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.inputs[i]
}

func (p *mockProvider) CompletionKind() CompletionKind        { return p.kind }
func (p *mockProvider) CanPrefetchFromSyntheticCurrent() bool { return p.syntheticCurrent }
func (p *mockProvider) RequiredMaterials() ctx.Materials      { return p.materials }

func (p *mockProvider) Complete(c context.Context, input ctx.CompletionInput) (*types.CompletionResponse, error) {
	p.mu.Lock()
	p.inputs = append(p.inputs, input)
	p.ctxs = append(p.ctxs, c)
	gate, err := p.gate, p.err
	resp := &types.CompletionResponse{}
	if len(p.responses) > 0 {
		resp = p.responses[0]
		p.responses = p.responses[1:]
	}
	p.mu.Unlock()
	if gate != nil {
		<-gate
	}
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// mockStreamingProvider streams the queued streams, and falls back to the
// batch responses once none is left.
type mockStreamingProvider struct {
	*mockProvider
	streams []*mockCompletionStream
}

func (p *mockStreamingProvider) StreamCompletion(c context.Context, input ctx.CompletionInput) (CompletionStream, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.streams) == 0 {
		return nil, fmt.Errorf("no stream left")
	}
	p.inputs = append(p.inputs, input)
	s := p.streams[0]
	p.streams = p.streams[1:]
	return s, nil
}

// mockCompletionStream emits the lines the test sends, buffered so that a
// dropped stream does not block the test, and finishes with
// response once the test closes lines.
type mockCompletionStream struct {
	lines       chan string
	windowStart int
	oldLines    []string
	response    *types.CompletionResponse
}

func newMockCompletionStream(windowStart int, oldLines []string, response *types.CompletionResponse) *mockCompletionStream {
	return &mockCompletionStream{lines: make(chan string, 16), windowStart: windowStart, oldLines: oldLines, response: response}
}

func (s *mockCompletionStream) Lines() <-chan string { return s.lines }
func (s *mockCompletionStream) Window() (int, []string) {
	return s.windowStart, s.oldLines
}
func (s *mockCompletionStream) Cancel() {}
func (s *mockCompletionStream) Finish() (*types.CompletionResponse, error) {
	return s.response, nil
}

// mockClock is a Clock that only moves with Advance.
type mockClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*mockTimer
}

func newMockClock() *mockClock {
	return &mockClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *mockClock) AfterFunc(d time.Duration, f func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &mockTimer{fireAt: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return t
}

func (c *mockClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock and fires the timers that are due.
func (c *mockClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []*mockTimer
	for _, t := range c.timers {
		if !t.fireAt.After(c.now) {
			due = append(due, t)
		}
	}
	c.mu.Unlock()
	for _, t := range due {
		t.fire()
	}
}

type mockTimer struct {
	mu      sync.Mutex
	fireAt  time.Time
	f       func()
	stopped bool
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
	t.mu.Unlock()
	t.f()
}

// recordingSender records the metrics events it receives. When hold is set,
// it waits for a value before recording each event.
type recordingSender struct {
	mu     sync.Mutex
	hold   chan struct{}
	events []metrics.Event
	errs   []error
}

func (r *recordingSender) SendMetric(c context.Context, event metrics.Event) {
	if r.hold != nil {
		<-r.hold
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	r.errs = append(r.errs, c.Err())
}

func (r *recordingSender) find(eventType metrics.EventType) *metrics.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.events {
		if r.events[i].Type == eventType {
			return &r.events[i]
		}
	}
	return nil
}

// harness runs an engine on a mock buffer and drives it through editor
// events, as an editor does.
type harness struct {
	t       *testing.T
	eng     *Engine
	buf     *mockBuffer
	clock   *mockClock
	history *history.Store

	mu       sync.Mutex
	statuses []types.EngineStatus
}

// harnessOption adjusts the dependencies or the config of a harness engine.
type harnessOption func(*Deps, *Config)

// newHarness starts an engine with the plugin defaults, without idle
// completions, and enters the buffer in insert mode.
func newHarness(t *testing.T, buf *mockBuffer, prov Provider, opts ...harnessOption) *harness {
	t.Helper()
	h := &harness{t: t, buf: buf, clock: newMockClock(), history: history.NewStore(3)}
	deps := Deps{
		Provider:      prov,
		Buffer:        buf,
		Clock:         h.clock,
		History:       h.history,
		StateNotifier: h.recordStatus,
	}
	config := Config{
		CompletionTimeout:   5 * time.Second,
		IdleCompletionDelay: -1,
		TextChangeDebounce:  50 * time.Millisecond,
		CursorPrediction: CursorPredictionConfig{
			Enabled:            true,
			AutoAdvance:        true,
			ProximityThreshold: 2,
		},
		MaxVisibleLines:  12,
		CompleteInInsert: true,
		CompleteInNormal: true,
	}
	for _, opt := range opts {
		opt(&deps, &config)
	}
	h.eng = New(deps, config)
	go h.eng.Run()
	t.Cleanup(h.eng.Close)
	h.send("buf_enter", "insert_enter")
	return h
}

func (h *harness) recordStatus(status types.EngineStatus) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.statuses = append(h.statuses, status)
}

// status returns the last published status.
func (h *harness) status() types.EngineStatus {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.statuses[len(h.statuses)-1]
}

func (h *harness) published(status types.EngineStatus) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Contains(h.statuses, status)
}

// post sends editor events without waiting.
func (h *harness) post(events ...string) {
	h.t.Helper()
	for _, name := range events {
		if EventTypeFromString(name) == "" {
			h.t.Fatalf("unknown editor event %q", name)
		}
		h.eng.HandleEditorEvent(name, h.buf.position())
	}
}

// send sends editor events and waits until the engine settled.
func (h *harness) send(events ...string) Snapshot {
	h.t.Helper()
	h.post(events...)
	return h.settle()
}

func (h *harness) settle() Snapshot {
	h.t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	snapshot, err := h.eng.Settle(c)
	if err != nil {
		h.t.Fatalf("settle: %v", err)
	}
	return snapshot
}

// waitFor polls cond until it holds. Use it only for state the engine
// reaches while something is in flight, when Settle would wait.
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

// display returns what the buffer displays: the completions rendered so far,
// whether one is shown, and the cursor target line.
func (b *mockBuffer) display() (renders int, shown bool, target int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.renders, b.shown, b.target
}
