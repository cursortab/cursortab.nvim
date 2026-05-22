// Package harness wires eval scenarios through the real engine.
//
// EvalBuffer is a minimal in-memory engine.Buffer implementation sufficient
// to drive scenarios: it holds lines, cursor, viewport, and previous/original
// line snapshots, and applies engine buffer mutations (InsertText, ReplaceLine,
// InsertLine) directly to the slice.
package harness

import (
	"slices"
	"strings"
	"sync"
	"time"

	"cursortab/buffer"
	"cursortab/text"
	"cursortab/types"
)

// EvalBuffer implements engine.Buffer for evaluation scenarios.
// It has no Neovim wiring — everything is in-memory.
type EvalBuffer struct {
	mu sync.Mutex

	path           string
	version        int
	lines          []string
	row            int // 1-indexed
	col            int // 0-indexed
	viewportTop    int
	viewportBottom int

	previousLines []string
	originalLines []string
	diskLines     []string
	diffHistories []*types.DiffEntry

	skipHistory bool
	modified    bool
}

// NewEvalBuffer returns a buffer seeded with the given lines.
func NewEvalBuffer(path string, lines []string, row, col int) *EvalBuffer {
	cp := slices.Clone(lines)
	return &EvalBuffer{
		path:           path,
		version:        1,
		lines:          cp,
		row:            row,
		col:            col,
		viewportTop:    1,
		viewportBottom: len(cp) + 20,
		previousLines:  slices.Clone(cp),
		originalLines:  slices.Clone(cp),
		diskLines:      slices.Clone(cp),
		modified:       true,
	}
}

// SetViewport overrides the default viewport bounds.
func (b *EvalBuffer) SetViewport(top, bottom int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.viewportTop = top
	b.viewportBottom = bottom
}

// SetCursor moves the cursor.
func (b *EvalBuffer) SetCursor(row, col int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.row = row
	b.col = col
}

// SetModified controls the IsModified flag reported to the engine. Used by
// scenarios that want to exercise the no-edits suppression layer.
func (b *EvalBuffer) SetModified(v bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.modified = v
}

// SetSkipHistory overrides the SkipHistory flag. When true, the buffer
// behaves like COMMIT_EDITMSG and bypasses no-edits suppression.
func (b *EvalBuffer) SetSkipHistory(v bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.skipHistory = v
}

// Sync implements engine.Buffer.
func (b *EvalBuffer) Sync(workspacePath string) (*buffer.SyncResult, error) {
	return &buffer.SyncResult{BufferChanged: false}, nil
}

// Lines implements engine.Buffer.
func (b *EvalBuffer) Lines() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.lines)
}

// Row implements engine.Buffer.
func (b *EvalBuffer) Row() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.row
}

// Col implements engine.Buffer.
func (b *EvalBuffer) Col() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.col
}

// Path implements engine.Buffer.
func (b *EvalBuffer) Path() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.path
}

// Version implements engine.Buffer.
func (b *EvalBuffer) Version() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.version
}

// ViewportBounds implements engine.Buffer.
func (b *EvalBuffer) ViewportBounds() (int, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.viewportTop, b.viewportBottom
}

// AvailableWidth implements engine.Buffer.
func (b *EvalBuffer) AvailableWidth() int { return 120 }

// PreviousLines implements engine.Buffer.
func (b *EvalBuffer) PreviousLines() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.previousLines
}

// OriginalLines implements engine.Buffer.
func (b *EvalBuffer) OriginalLines() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.originalLines
}

// DiffHistories implements engine.Buffer.
func (b *EvalBuffer) DiffHistories() []*types.DiffEntry {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.diffHistories
}

// DiskLines implements engine.Buffer.
func (b *EvalBuffer) DiskLines() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.diskLines
}

// SetFileContext implements engine.Buffer.
func (b *EvalBuffer) SetFileContext(ctx buffer.FileContext) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.previousLines = ctx.PreviousLines
	b.originalLines = ctx.OriginalLines
	b.diffHistories = ctx.DiffHistories
}

// HasChanges implements engine.Buffer.
func (b *EvalBuffer) HasChanges(startLine, endLineInc int, lines []string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := startLine; i <= endLineInc && i-1 < len(b.lines); i++ {
		relIdx := i - startLine
		if relIdx >= len(lines) {
			return true
		}
		if b.lines[i-1] != lines[relIdx] {
			return true
		}
	}
	return len(lines) != (endLineInc - startLine + 1)
}

// PrepareCompletion implements engine.Buffer.
func (b *EvalBuffer) PrepareCompletion(startLine, endLineInc int, lines []string, groups []*text.Group) buffer.Batch {
	return &evalBatch{
		buf:        b,
		startLine:  startLine,
		endLineInc: endLineInc,
		lines:      slices.Clone(lines),
	}
}

// CommitPending implements engine.Buffer.
func (b *EvalBuffer) CommitPending() {}

// CommitUserEdits implements engine.Buffer.
func (b *EvalBuffer) CommitUserEdits() bool { return false }

// ClearDiffHistory implements engine.Buffer.
func (b *EvalBuffer) ClearDiffHistory() {}

// IsModified implements engine.Buffer.
func (b *EvalBuffer) IsModified() bool { return b.modified }

// CursorScopes implements engine.Buffer.
func (b *EvalBuffer) CursorScopes() []string { return nil }

// SkipHistory implements engine.Buffer.
func (b *EvalBuffer) SkipHistory() bool { return b.skipHistory }

// ShowCursorTarget implements engine.Buffer.
func (b *EvalBuffer) ShowCursorTarget(line int) error { return nil }

// ClearUI implements engine.Buffer.
func (b *EvalBuffer) ClearUI() error { return nil }

// MoveCursor implements engine.Buffer.
func (b *EvalBuffer) MoveCursor(line int, center, mark bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.row = line
	return nil
}

// RegisterEventHandler implements engine.Buffer.
func (b *EvalBuffer) RegisterEventHandler(handler func(event string)) error { return nil }

// InsertText implements engine.Buffer.
func (b *EvalBuffer) InsertText(line, col int, txt string, keepUI bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if line < 1 || line > len(b.lines) {
		return nil
	}
	cur := b.lines[line-1]
	if col < 0 {
		col = 0
	}
	if col > len(cur) {
		col = len(cur)
	}
	b.lines[line-1] = cur[:col] + txt + cur[col:]
	return nil
}

// ReplaceLine implements engine.Buffer.
func (b *EvalBuffer) ReplaceLine(line int, content string, keepUI bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if line >= 1 && line <= len(b.lines) {
		b.lines[line-1] = content
	}
	return nil
}

// InsertLine implements engine.Buffer.
func (b *EvalBuffer) InsertLine(line int, content string, keepUI bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if line < 1 || line > len(b.lines)+1 {
		return nil
	}
	newLines := make([]string, len(b.lines)+1)
	copy(newLines[:line-1], b.lines[:line-1])
	newLines[line-1] = content
	copy(newLines[line:], b.lines[line-1:])
	b.lines = newLines
	return nil
}

// Snapshot returns a copy of the current buffer lines. Used for scoring.
func (b *EvalBuffer) Snapshot() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.lines)
}

// evalBatch applies a staged completion to an EvalBuffer.
type evalBatch struct {
	buf        *EvalBuffer
	startLine  int
	endLineInc int
	lines      []string
}

// Execute implements buffer.Batch. Applies the staged edit to the buffer
// and appends a DiffEntry to the buffer's diff history so subsequent
// request-completion steps see the accepted edit as recent context. This
// is what lets multi-step refactor scenarios chain properly: after
// accept, the engine's next request includes the first edit in its
// history, and the provider can reason about "what was just done".
func (b *evalBatch) Execute() error {
	b.buf.mu.Lock()
	defer b.buf.mu.Unlock()

	buf := b.buf.lines
	if b.startLine < 1 {
		return nil
	}
	start := b.startLine - 1
	end := b.endLineInc
	if end > len(buf) {
		end = len(buf)
	}
	if start > len(buf) {
		start = len(buf)
	}

	// Capture the replaced region for the diff history entry.
	original := strings.Join(buf[start:end], "\n")
	updated := strings.Join(b.lines, "\n")

	out := make([]string, 0, len(buf)-(end-start)+len(b.lines))
	out = append(out, buf[:start]...)
	out = append(out, b.lines...)
	if end < len(buf) {
		out = append(out, buf[end:]...)
	}
	b.buf.lines = out

	// Only record a diff if something actually changed.
	if original != updated {
		b.buf.diffHistories = append(b.buf.diffHistories, &types.DiffEntry{
			Original:    original,
			Updated:     updated,
			Source:      types.DiffSourcePredicted,
			TimestampNs: time.Now().UnixNano(),
			StartLine:   b.startLine,
		})
	}
	return nil
}
