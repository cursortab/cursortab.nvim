// Package harness wires eval scenarios through the real engine.
//
// EvalBuffer is a minimal in-memory engine.Buffer implementation sufficient
// to drive scenarios: it holds lines, cursor, and viewport, and applies
// engine buffer mutations directly to the slice.
package harness

import (
	"slices"

	"cursortab/buffer"
	"cursortab/history"
	"cursortab/text"
	"cursortab/types"
)

// EvalBuffer implements engine.Buffer for evaluation scenarios. It has no
// Neovim wiring and is only used from the harness goroutine.
type EvalBuffer struct {
	path           string
	lines          []string
	row            int // 1-indexed
	col            int // 0-indexed
	viewportTop    int
	viewportBottom int
	modified       bool
}

// NewEvalBuffer returns a buffer seeded with the given lines.
func NewEvalBuffer(path string, lines []string, row, col int) *EvalBuffer {
	return &EvalBuffer{
		path:           path,
		lines:          slices.Clone(lines),
		row:            row,
		col:            col,
		viewportTop:    1,
		viewportBottom: len(lines) + 20,
		modified:       true,
	}
}

// SetModified sets whether the buffer has unsaved changes.
func (b *EvalBuffer) SetModified(modified bool) { b.modified = modified }

// SetViewport overrides the default viewport bounds.
func (b *EvalBuffer) SetViewport(top, bottom int) {
	b.viewportTop = top
	b.viewportBottom = bottom
}

func (b *EvalBuffer) Sync() (bool, error)             { return false, nil }
func (b *EvalBuffer) WorkspacePath() string           { return "/workspace" }
func (b *EvalBuffer) Path() string                    { return b.path }
func (b *EvalBuffer) Modified() bool                  { return b.modified }
func (b *EvalBuffer) Lines() []string                 { return b.lines }
func (b *EvalBuffer) Row() int                        { return b.row }
func (b *EvalBuffer) Col() int                        { return b.col }
func (b *EvalBuffer) ViewportBounds() (int, int)      { return b.viewportTop, b.viewportBottom }
func (b *EvalBuffer) AvailableWidth() int             { return 120 }
func (b *EvalBuffer) Diagnostics() *types.Diagnostics { return nil }
func (b *EvalBuffer) CursorScopes() []string          { return nil }
func (b *EvalBuffer) ShowCursorTarget(int)            {}
func (b *EvalBuffer) ClearUI()                        {}
func (b *EvalBuffer) TreesitterSymbols(int, int, int) *types.TreesitterContext {
	return nil
}

// PrepareCompletion returns the batch that applies the completion.
func (b *EvalBuffer) PrepareCompletion(startLine, endLineInc int, lines []string, groups []*text.Group) buffer.Batch {
	return evalBatch{buf: b, edit: history.Edit{StartLine: startLine, EndLineInc: endLineInc, Lines: slices.Clone(lines)}}
}

func (b *EvalBuffer) MoveCursor(line int, center, mark bool) error {
	b.row = line
	return nil
}

func (b *EvalBuffer) AppendText(line int, txt string) error {
	if line < 1 || line > len(b.lines) {
		return nil
	}
	b.lines[line-1] += txt
	return nil
}

func (b *EvalBuffer) SetLines(edit history.Edit) error {
	b.lines = edit.Apply(b.lines)
	return nil
}

// Snapshot returns a copy of the current buffer lines. Used for scoring.
func (b *EvalBuffer) Snapshot() []string {
	return slices.Clone(b.lines)
}

// evalBatch applies a staged completion to an EvalBuffer.
type evalBatch struct {
	buf  *EvalBuffer
	edit history.Edit
}

func (b evalBatch) Execute() error {
	b.buf.lines = b.edit.Apply(b.buf.lines)
	return nil
}
