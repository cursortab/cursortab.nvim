// Package buffer is the Neovim side of a session: it reads the current editor
// state, renders completions, and applies edits through RPC.
package buffer

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"

	"cursortab/history"
	"cursortab/logger"
	"cursortab/text"
	"cursortab/types"

	"github.com/neovim/go-client/msgpack/rpc"
	"github.com/neovim/go-client/nvim"
)

// NvimBuffer caches the state of the current Neovim buffer and window. Sync
// refreshes it; edits and rendering go straight to Neovim.
type NvimBuffer struct {
	client *nvim.Nvim
	nsID   int

	id            nvim.Buffer
	tick          int // b:changedtick of the synced lines
	lines         []string
	row           int // 1-indexed
	col           int // 0-indexed
	path          string
	workspacePath string
	modified      bool

	viewportTop    int // 1-indexed
	viewportBottom int // 1-indexed
	availableWidth int // Window width minus sign/number column (textoff)

}

// New returns a buffer that talks to Neovim through client and draws in the
// nsID namespace.
func New(client *nvim.Nvim, nsID int) *NvimBuffer {
	return &NvimBuffer{client: client, nsID: nsID, row: 1}
}

func (b *NvimBuffer) Lines() []string { return b.lines }

func (b *NvimBuffer) Row() int { return b.row }

func (b *NvimBuffer) Col() int { return b.col }

// Path is the buffer path relative to the Neovim working directory.
func (b *NvimBuffer) Path() string { return b.path }

// Modified reports whether the buffer has changes since it was last written.
func (b *NvimBuffer) Modified() bool { return b.modified }

// WorkspacePath is the Neovim working directory.
func (b *NvimBuffer) WorkspacePath() string { return b.workspacePath }

func (b *NvimBuffer) ViewportBounds() (top, bottom int) {
	return b.viewportTop, b.viewportBottom
}

func (b *NvimBuffer) AvailableWidth() int { return b.availableWidth }

// Sync reads the current editor state in one round trip. It reports whether
// the current buffer changed since the previous sync. The lines are sent only
// when the buffer or its b:changedtick differs from the synced ones.
func (b *NvimBuffer) Sync() (bool, error) {
	defer logger.Trace("buffer.Sync")()

	var (
		currentBuf nvim.Buffer
		path       string
		lines      [][]byte // nil when the synced lines are current
		cursor     [2]int
		cwd        string
		modified   bool
		tick       int
		// Window height instead of w$ so that short files still report the
		// full visible area (w$ only returns the last line with content).
		viewport [3]int
	)
	batch := b.client.NewBatch()
	batch.CurrentBuffer(&currentBuf)
	batch.BufferName(nvim.Buffer(0), &path)
	batch.ExecLua(`
		local buf, tick = ...
		if vim.api.nvim_get_current_buf() == buf and vim.b.changedtick == tick then
			return nil
		end
		return vim.api.nvim_buf_get_lines(0, 0, -1, false)
	`, &lines, int(b.id), b.tick)
	batch.WindowCursor(nvim.Window(0), &cursor)
	batch.ExecLua(`return vim.fn.getcwd()`, &cwd, nil)
	batch.ExecLua(`return vim.bo.modified`, &modified, nil)
	batch.ExecLua(`return vim.b.changedtick`, &tick, nil)
	batch.ExecLua(`
		local top = vim.fn.line("w0")
		local height = vim.api.nvim_win_get_height(0)
		local win_width = vim.api.nvim_win_get_width(0)
		local textoff = vim.fn.getwininfo(vim.api.nvim_get_current_win())[1].textoff or 0
		return {top, top + height - 1, win_width - textoff}
	`, &viewport, nil)
	if err := batch.Execute(); err != nil {
		return false, err
	}

	// A buffer always has a line, so no lines means the cached ones are current.
	if lines != nil {
		b.lines = make([]string, len(lines))
		for i, line := range lines {
			b.lines[i] = string(line)
		}
	}
	b.row, b.col = cursor[0], cursor[1]
	b.viewportTop, b.viewportBottom, b.availableWidth = viewport[0], viewport[1], viewport[2]
	b.workspacePath = cwd
	b.modified = modified
	b.tick = tick
	b.path = relativeToWorkspace(path, cwd)

	switched := b.id != currentBuf
	b.id = currentBuf
	return switched, nil
}

func relativeToWorkspace(absolutePath, workspacePath string) string {
	if absolutePath == "" {
		return ""
	}
	absolutePath = filepath.Clean(absolutePath)
	workspacePath = filepath.Clean(workspacePath)
	if rel, found := strings.CutPrefix(absolutePath, workspacePath); found {
		return strings.TrimPrefix(rel, string(filepath.Separator))
	}
	return absolutePath
}

// finishEdit ends a batch that edits the buffer or moves the cursor, after
// the edits queued before it. It tells the editor that the edit is cursortab's
// own, then reads b:changedtick, the cursor and the lines that replace edit's
// range. Only the range and line count of edit are used: the returned
// function stores the lines Neovim holds, so the snapshot is the edited
// buffer. A nil edit changed no lines.
func (b *NvimBuffer) finishEdit(batch *nvim.Batch, edit *history.Edit) func() {
	var (
		lines  [][]byte
		tick   int
		cursor [2]int
	)
	batch.ExecLua("require('cursortab.events').on_edit_applied()", nil, nil)
	if edit != nil {
		start := max(edit.StartLine-1, 0)
		batch.BufferLines(b.id, start, start+len(edit.Lines), false, &lines)
	}
	batch.BufferChangedTick(b.id, &tick)
	batch.WindowCursor(0, &cursor)
	return func() {
		if edit != nil {
			read := history.Edit{StartLine: edit.StartLine, EndLineInc: edit.EndLineInc, Lines: make([]string, len(lines))}
			for i, line := range lines {
				read.Lines[i] = string(line)
			}
			b.lines = read.Apply(b.lines)
		}
		b.tick = tick
		b.row, b.col = cursor[0], cursor[1]
	}
}

// nvimBatch applies a completion in Neovim and to the snapshot.
type nvimBatch struct {
	batch *nvim.Batch
	buf   *NvimBuffer
	edit  history.Edit
	store func()
}

func (nb *nvimBatch) Execute() error {
	if err := nb.batch.Execute(); err != nil {
		return err
	}
	nb.store()
	return nil
}

// PrepareCompletion renders a completion and returns the batch that applies it.
func (b *NvimBuffer) PrepareCompletion(startLine, endLineInc int, lines []string, groups []*text.Group) Batch {
	diffResult := text.ComputeDiff(text.JoinLines(text.LineRange(b.lines, startLine, endLineInc)), text.JoinLines(lines))
	applyBatch := b.getApplyBatch(startLine, endLineInc, lines, groups, diffResult)

	luaDiffResult := text.ToLuaFormat(&text.Stage{
		Changes: diffResult.ChangesMap(),
		Groups:  groups,
		Lines:   lines,
	}, startLine)
	// Neovim drops the render when the buffer changed after this snapshot.
	luaDiffResult["bufnr"] = int(b.id)
	luaDiffResult["changedtick"] = b.tick

	if jsonData, err := json.Marshal(luaDiffResult); err == nil {
		logger.Debug("sending to lua on_completion_ready:\n  startLine: %d\n  endLineInclusive: %d\n  lines: %d\n  diffResult: %s",
			startLine, endLineInc, len(lines), string(jsonData))
	}

	b.executeLuaFunction("require('cursortab').on_completion_ready(...)", luaDiffResult)

	return &nvimBatch{
		batch: applyBatch,
		buf:   b,
		edit:  history.Edit{StartLine: startLine, EndLineInc: endLineInc, Lines: slices.Clone(lines)},
		store: b.finishEdit(applyBatch, &history.Edit{StartLine: startLine, EndLineInc: endLineInc, Lines: lines}),
	}
}

// ShowCursorTarget displays a cursor prediction indicator at the given line
func (b *NvimBuffer) ShowCursorTarget(line int) {
	logger.Debug("sending to lua on_cursor_prediction_ready: line=%d", line)
	b.executeLuaFunction("require('cursortab').on_cursor_prediction_ready(...)", line)
}

// ClearUI clears the completion UI
func (b *NvimBuffer) ClearUI() {
	logger.Debug("sending to lua on_reject")
	b.executeLuaFunction("require('cursortab').on_reject()")
}

// MoveCursor moves the cursor to the start of the specified line
func (b *NvimBuffer) MoveCursor(line int, center bool, mark bool) error {
	batch := b.client.NewBatch()
	applyCursorMove(batch, line, 0, center, mark)
	batch.ExecLua("vim.cmd('normal! ^')", nil, nil) // Move cursor to start of line
	store := b.finishEdit(batch, nil)
	if err := batch.Execute(); err != nil {
		return err
	}
	store()
	return nil
}

// AppendText appends text to the end of line (1-indexed) and moves the cursor
// after it.
func (b *NvimBuffer) AppendText(line int, text string) error {
	batch := b.client.NewBatch()
	batch.ExecLua(`
		local buf, row, text = ...
		local col = #vim.api.nvim_buf_get_lines(buf, row - 1, row, true)[1]
		vim.api.nvim_buf_set_text(buf, row - 1, col, row - 1, col, { text })
		vim.fn.setpos("''", vim.fn.getpos("."))
		vim.api.nvim_win_set_cursor(0, { row, col + #text })
	`, nil, int(b.id), line, text)
	store := b.finishEdit(batch, &history.Edit{StartLine: line, EndLineInc: line, Lines: []string{text}})
	if err := batch.Execute(); err != nil {
		return err
	}
	store()
	return nil
}

// SetLines applies edit and moves the cursor to the end of its last line.
func (b *NvimBuffer) SetLines(edit history.Edit) error {
	line, col := edit.StartLine, 0
	if n := len(edit.Lines); n > 0 {
		line, col = edit.StartLine+n-1, len(edit.Lines[n-1])
	}
	batch := b.client.NewBatch()
	setBufferLines(batch, b.id, edit.StartLine, edit.EndLineInc, edit.Lines)
	applyCursorMove(batch, max(1, min(line, len(edit.Apply(b.lines)))), col, false, true)
	store := b.finishEdit(batch, &edit)
	if err := batch.Execute(); err != nil {
		return err
	}
	store()
	return nil
}

// Diagnostics retrieves LSP diagnostics for the current buffer.
// Returns raw Neovim diagnostic data; providers handle formatting.
func (b *NvimBuffer) Diagnostics() *types.Diagnostics {
	var rawDiags []map[string]any
	batch := b.client.NewBatch()
	batch.ExecLua(`
		local bufnr = ...
		local get_clients = vim.lsp.get_clients or vim.lsp.get_active_clients
		if #get_clients({ bufnr = bufnr }) == 0 then
			return {}
		end
		return vim.diagnostic.get(bufnr)
	`, &rawDiags, int(b.id))
	if err := batch.Execute(); err != nil {
		logger.Error("error getting diagnostics: %v", err)
		return nil
	}
	if len(rawDiags) == 0 {
		return nil
	}

	items := make([]*types.Diagnostic, 0, len(rawDiags))
	for _, diag := range rawDiags {
		d := &types.Diagnostic{
			Message:  getString(diag, "message"),
			Source:   getString(diag, "source"),
			Severity: types.DiagnosticSeverity(max(1, getNumber(diag, "severity"))),
		}

		if lnum := getNumber(diag, "lnum"); lnum != -1 {
			if col := getNumber(diag, "col"); col != -1 {
				endLnum := lnum
				endCol := col
				if v := getNumber(diag, "end_lnum"); v != -1 {
					endLnum = v
				}
				if v := getNumber(diag, "end_col"); v != -1 {
					endCol = v
				}
				d.Range = &types.CursorRange{
					StartLine:      lnum,
					StartCharacter: col,
					EndLine:        endLnum,
					EndCharacter:   endCol,
				}
			}
		}

		items = append(items, d)
	}

	return &types.Diagnostics{
		FilePath: b.path,
		Items:    items,
	}
}

// CursorScopes returns treesitter node types from the cursor position to the root.
func (b *NvimBuffer) CursorScopes() []string {
	var result []string
	batch := b.client.NewBatch()
	batch.ExecLua(
		`return require('cursortab.treesitter').cursor_scopes(...)`,
		&result, int(b.id), b.row, b.col,
	)

	if err := batch.Execute(); err != nil {
		logger.Debug("error getting cursor scopes: %v", err)
		return nil
	}

	return result
}

// TreesitterSymbols retrieves treesitter scope context around the cursor position.
// Returns nil gracefully if no treesitter parser is available for the buffer.
func (b *NvimBuffer) TreesitterSymbols(row, col, maxSiblings int) *types.TreesitterContext {
	var result map[string]any
	batch := b.client.NewBatch()
	batch.ExecLua(
		`return require('cursortab.treesitter').get_context(...)`,
		&result, int(b.id), row, col, maxSiblings,
	)

	if err := batch.Execute(); err != nil {
		logger.Error("error getting treesitter symbols: %v", err)
		return nil
	}

	if result == nil {
		return nil
	}

	ctx := &types.TreesitterContext{
		EnclosingSignature: getString(result, "enclosing_signature"),
	}

	// Parse siblings
	if sibs, ok := result["siblings"].([]any); ok {
		for _, s := range sibs {
			if sm, ok := s.(map[string]any); ok {
				ctx.Siblings = append(ctx.Siblings, &types.TreesitterSymbol{
					Name:      getString(sm, "name"),
					Signature: getString(sm, "signature"),
					Line:      getNumber(sm, "line"),
				})
			}
		}
	}

	// Parse imports
	if imps, ok := result["imports"].([]any); ok {
		for _, imp := range imps {
			if s, ok := imp.(string); ok {
				ctx.Imports = append(ctx.Imports, s)
			}
		}
	}

	// Parse syntax ranges (ancestor AST node line ranges, innermost to outermost)
	if ranges, ok := result["syntax_ranges"].([]any); ok {
		for _, r := range ranges {
			if rm, ok := r.(map[string]any); ok {
				ctx.SyntaxRanges = append(ctx.SyntaxRanges, &types.LineRange{
					StartLine: getNumber(rm, "start_line"),
					EndLine:   getNumber(rm, "end_line"),
				})
			}
		}
	}

	// Return nil if we got nothing useful
	if ctx.EnclosingSignature == "" && len(ctx.Siblings) == 0 && len(ctx.Imports) == 0 && len(ctx.SyntaxRanges) == 0 {
		return nil
	}

	return ctx
}

// Internal helper methods

// NotifyState forwards engine state and request activity to Neovim.
func (b *NvimBuffer) NotifyState(status types.EngineStatus) {
	b.executeLuaFunction("require('cursortab').on_state_change(...)", status)
}

func (b *NvimBuffer) executeLuaFunction(luaCode string, args ...any) {
	batch := b.client.NewBatch()
	if len(args) > 0 {
		batch.ExecLua(luaCode, nil, args...)
	} else {
		batch.ExecLua(luaCode, nil, nil)
	}
	// A closed session only happens while the session shuts down.
	if err := batch.Execute(); err != nil && !errors.Is(err, rpc.ErrClosed) {
		logger.Error("error executing lua function: %v", err)
	}
}

func applyCursorMove(batch *nvim.Batch, line, col int, center bool, mark bool) {
	if mark {
		// Use vim.fn.setpos to set the ' mark without triggering mode changes
		// (normal! m' would exit insert mode and cause ModeChanged events)
		// The mark name "''" means: ' prefix for marks + ' as the mark name
		batch.ExecLua("vim.fn.setpos(\"''\", vim.fn.getpos('.'))", nil, nil)
	}
	batch.SetWindowCursor(0, [2]int{line, col})
	if center {
		batch.ExecLua("vim.cmd('normal! zz')", nil, nil)
	}
}

type bufferTextEdit struct {
	row         int
	startCol    int
	endCol      int
	replacement []byte
}

func textEditForCharGroup(group *text.Group) (bufferTextEdit, bool) {
	if group == nil || group.StartLine != group.EndLine || group.BufferLine <= 0 {
		return bufferTextEdit{}, false
	}
	switch group.RenderHint {
	case text.HintAppendChars, text.HintReplaceChars, text.HintDeleteChars:
	default:
		return bufferTextEdit{}, false
	}
	if len(group.Lines) != 1 || len(group.OldLines) != 1 {
		return bufferTextEdit{}, false
	}

	oldLine := group.OldLines[0]
	newLine := group.Lines[0]
	if oldLine == newLine {
		return bufferTextEdit{}, false
	}

	startCol, oldEnd, newEnd := text.ChangedByteSpan(oldLine, newLine)
	if oldEnd < startCol || newEnd < startCol {
		return bufferTextEdit{}, false
	}

	return bufferTextEdit{
		row:         group.BufferLine - 1,
		startCol:    startCol,
		endCol:      oldEnd,
		replacement: []byte(newLine[startCol:newEnd]),
	}, true
}

func charLevelTextEdits(groups []*text.Group) ([]bufferTextEdit, bool) {
	if len(groups) == 0 {
		return nil, false
	}

	edits := make([]bufferTextEdit, 0, len(groups))
	for _, group := range groups {
		edit, ok := textEditForCharGroup(group)
		if !ok {
			return nil, false
		}
		edits = append(edits, edit)
	}

	slices.SortFunc(edits, func(a, b bufferTextEdit) int {
		if a.row != b.row {
			return b.row - a.row
		}
		return b.startCol - a.startCol
	})

	return edits, true
}

func setBufferLines(batch *nvim.Batch, buf nvim.Buffer, startLine, replaceEnd int, lines []string) {
	placeBytes := make([][]byte, len(lines))
	for i, line := range lines {
		placeBytes[i] = []byte(line)
	}

	// nvim_buf_set_lines uses 0-indexed [start, end) range.
	// Replacement: 1-indexed inclusive [startLine, replaceEnd] maps to
	// 0-indexed exclusive [startLine-1, replaceEnd) by indexing coincidence.
	// Pure insertion: replaceEnd = startLine-1, so start == end inserts without replacing.
	batch.SetBufferLines(buf, startLine-1, replaceEnd, false, placeBytes)
}

func (b *NvimBuffer) getApplyBatch(startLine, replaceEnd int, lines []string, groups []*text.Group, diffResult *text.DiffResult) *nvim.Batch {
	applyBatch := b.client.NewBatch()

	b.clearNamespace(applyBatch, b.nsID)

	if edits, ok := charLevelTextEdits(groups); ok {
		for _, edit := range edits {
			applyBatch.SetBufferText(b.id, edit.row, edit.startCol, edit.row, edit.endCol, [][]byte{edit.replacement})
		}
	} else {
		setBufferLines(applyBatch, b.id, startLine, replaceEnd, lines)
	}

	// Apply cursor positioning from diff changes
	cursorLine, cursorCol := text.CalculateCursorPosition(diffResult.ChangesMap(), lines)
	if cursorLine >= 0 && cursorCol >= 0 {
		bufferLine := startLine + cursorLine - 1
		applyCursorMove(applyBatch, bufferLine, cursorCol, false, true)
	}

	return applyBatch
}

func (b *NvimBuffer) clearNamespace(batch *nvim.Batch, nsID int) {
	batch.ClearBufferNamespace(b.id, nsID, 0, -1)
}

// Helper function to safely get string from map
func getString(m map[string]any, key string) string {
	if val, ok := m[key].(string); ok {
		return val
	}
	return ""
}

// Helper function to safely get number from map, handling common msgpack number types
func getNumber(m map[string]any, key string) int {
	if val, ok := m[key].(int); ok {
		return val
	}
	if val, ok := m[key].(float64); ok {
		return int(val)
	}
	if val, ok := m[key].(int32); ok {
		return int(val)
	}
	if val, ok := m[key].(int64); ok {
		return int(val)
	}
	if val, ok := m[key].(uint64); ok {
		return int(val)
	}
	return -1
}
