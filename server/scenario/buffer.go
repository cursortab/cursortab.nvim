package scenario

import (
	"slices"
	"strings"

	"cursortab/buffer"
	"cursortab/history"
	"cursortab/text"
	"cursortab/types"
)

// fakeBuffer is an in-memory engine.Buffer. It applies the engine's edits to
// its lines and records what the engine displays, as Neovim would. The
// runner only touches it while the engine is settled.
type fakeBuffer struct {
	path           string
	lines          []string
	row            int // 1-indexed
	col            int // 0-indexed byte column
	viewportTop    int
	viewportBottom int
	modified       bool

	display      []string // Group summaries of the displayed completion
	cursorTarget int      // Line of the displayed cursor target
}

func newFakeBuffer(path string, state BufferState, row, col int) *fakeBuffer {
	return &fakeBuffer{
		path:           path,
		lines:          slices.Clone(state.Lines),
		row:            row,
		col:            col,
		viewportTop:    state.ViewportTop,
		viewportBottom: state.ViewportBottom,
		modified:       state.Modified,
	}
}

func (b *fakeBuffer) Sync() (bool, error)             { return false, nil }
func (b *fakeBuffer) WorkspacePath() string           { return "/workspace" }
func (b *fakeBuffer) Path() string                    { return b.path }
func (b *fakeBuffer) Modified() bool                  { return b.modified }
func (b *fakeBuffer) Lines() []string                 { return b.lines }
func (b *fakeBuffer) Row() int                        { return b.row }
func (b *fakeBuffer) Col() int                        { return b.col }
func (b *fakeBuffer) ViewportBounds() (int, int)      { return b.viewportTop, b.viewportBottom }
func (b *fakeBuffer) AvailableWidth() int             { return 120 }
func (b *fakeBuffer) Diagnostics() *types.Diagnostics { return nil }
func (b *fakeBuffer) CursorScopes() []string          { return nil }
func (b *fakeBuffer) TreesitterSymbols(int, int, int) *types.TreesitterContext {
	return nil
}

func (b *fakeBuffer) PrepareCompletion(startLine, endLineInc int, lines []string, groups []*text.Group) buffer.Batch {
	b.cursorTarget = 0
	b.display = []string{}
	for _, g := range groups {
		b.display = append(b.display, GroupSummary(string(g.Type), g.BufferLine, g.EndLine-g.StartLine+1, string(g.RenderHint)))
	}
	diff := text.ComputeDiff(text.JoinLines(text.LineRange(b.lines, startLine, endLineInc)), text.JoinLines(lines))
	row, col := text.CalculateCursorPosition(diff.ChangesMap(), lines)
	return &fakeBatch{
		buf:  b,
		edit: history.Edit{StartLine: startLine, EndLineInc: endLineInc, Lines: slices.Clone(lines)},
		row:  startLine + row - 1,
		col:  col,
		move: row >= 0 && col >= 0,
	}
}

func (b *fakeBuffer) ShowCursorTarget(line int) {
	b.display = nil
	b.cursorTarget = line
}

func (b *fakeBuffer) ClearUI() {
	b.display = nil
	b.cursorTarget = 0
}

func (b *fakeBuffer) MoveCursor(line int, center, mark bool) error {
	b.row = line
	b.col = len(b.lines[line-1]) - len(strings.TrimLeft(b.lines[line-1], " \t"))
	return nil
}

func (b *fakeBuffer) AppendText(line int, txt string) error {
	b.lines[line-1] += txt
	b.row, b.col = line, len(b.lines[line-1])
	return nil
}

func (b *fakeBuffer) SetLines(edit history.Edit) error {
	b.lines = edit.Apply(b.lines)
	b.row, b.col = edit.StartLine, 0
	if n := len(edit.Lines); n > 0 {
		b.row, b.col = edit.StartLine+n-1, len(edit.Lines[n-1])
	}
	b.row = max(1, min(b.row, len(b.lines)))
	return nil
}

// insert types text at the cursor and moves the cursor after it.
func (b *fakeBuffer) insert(txt string) {
	if len(b.lines) == 0 {
		b.lines = []string{""}
	}
	line := b.lines[b.row-1]
	typed := strings.Split(line[:b.col]+txt, "\n")
	rest := line[b.col:]
	b.row += len(typed) - 1
	b.col = len(typed[len(typed)-1])
	typed[len(typed)-1] += rest
	b.lines = slices.Concat(b.lines[:b.row-len(typed)], typed, b.lines[b.row-len(typed)+1:])
}

// position returns the cursor and buffer size as the editor reports them
// with each event.
func (b *fakeBuffer) position() buffer.Position {
	pos := buffer.Position{Row: b.row, Col: b.col}
	for i, line := range b.lines {
		if i < b.row-1 {
			pos.Offset += len(line) + 1
		}
		pos.Size += len(line) + 1
	}
	pos.Offset += b.col
	if len(b.lines) == 0 {
		pos.Size = 1
	}
	return pos
}

func (b *fakeBuffer) observe() Observation {
	return Observation{
		Buffer:       slices.Clone(b.lines),
		Row:          b.row,
		Col:          b.col,
		Display:      slices.Clone(b.display),
		CursorTarget: b.cursorTarget,
	}
}

// fakeBatch applies a displayed completion, as the Neovim apply batch does.
type fakeBatch struct {
	buf      *fakeBuffer
	edit     history.Edit
	row, col int
	move     bool
}

func (b *fakeBatch) Execute() error {
	b.buf.lines = b.edit.Apply(b.buf.lines)
	b.buf.display = nil
	if b.move {
		b.buf.row, b.buf.col = b.row, b.col
	}
	return nil
}
