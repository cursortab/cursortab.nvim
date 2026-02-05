package buffer

import (
	"cursortab/logger"
	"cursortab/text"
	"cursortab/types"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/neovim/go-client/nvim"
	"github.com/sergi/go-diff/diffmatchpatch"
)

type Config struct {
	NsID int
}

type NvimBuffer struct {
	client *nvim.Nvim // stored internally, set via SetClient

	// Private state
	lines         []string
	row           int // 1-indexed
	col           int // 0-indexed
	path          string
	version       int
	diffHistories []*types.DiffEntry // Structured diff history for provider consumption
	previousLines []string           // Buffer content before the most recent edit (for sweep provider)

	originalLines    []string // Original file content when editing session started
	lastModifiedLine int      // Track which line was last modified
	id               nvim.Buffer
	scrollOffsetX    int // Horizontal scroll offset (leftcol)

	// Viewport bounds (1-indexed line numbers)
	viewportTop    int // First visible line (1-indexed)
	viewportBottom int // Last visible line (1-indexed)

	config Config

	// Pending completion state (committed only on accept)
	pendingStartLine        int
	pendingEndLineInclusive int
	pendingLines            []string
	hasPending              bool
}

func New(config Config) *NvimBuffer {
	return &NvimBuffer{
		lines:                   []string{},
		row:                     1,
		col:                     0,
		path:                    "",
		version:                 0,
		diffHistories:           []*types.DiffEntry{},
		previousLines:           []string{},
		originalLines:           []string{},
		lastModifiedLine:        -1,
		id:                      nvim.Buffer(0),
		scrollOffsetX:           0,
		config:                  config,
		pendingStartLine:        0,
		pendingEndLineInclusive: 0,
		pendingLines:            nil,
		hasPending:              false,
	}
}

// SetClient stores the nvim client for all buffer operations
func (b *NvimBuffer) SetClient(n *nvim.Nvim) {
	b.client = n
}

// Accessor methods implementing engine.Buffer interface

func (b *NvimBuffer) Lines() []string { return b.lines }

func (b *NvimBuffer) Row() int { return b.row }

func (b *NvimBuffer) Col() int { return b.col }

func (b *NvimBuffer) Path() string { return b.path }

func (b *NvimBuffer) Version() int { return b.version }

func (b *NvimBuffer) ViewportBounds() (top, bottom int) {
	return b.viewportTop, b.viewportBottom
}

func (b *NvimBuffer) PreviousLines() []string { return b.previousLines }

func (b *NvimBuffer) OriginalLines() []string { return b.originalLines }

func (b *NvimBuffer) DiffHistories() []*types.DiffEntry { return b.diffHistories }

// SetFileContext restores file-specific state when switching back to a previously edited file.
// This is called by the engine after detecting a file switch.
func (b *NvimBuffer) SetFileContext(previousLines, originalLines []string, diffHistories []*types.DiffEntry) {
	if previousLines != nil {
		b.previousLines = make([]string, len(previousLines))
		copy(b.previousLines, previousLines)
	} else if originalLines != nil {
		// For new files without previous state, initialize previousLines to the original
		// file content. This ensures providers (like sweep) have a valid "before" state
		// to compare against, rather than falling back to current content.
		b.previousLines = make([]string, len(originalLines))
		copy(b.previousLines, originalLines)
	} else {
		b.previousLines = nil
	}

	if diffHistories != nil {
		b.diffHistories = make([]*types.DiffEntry, len(diffHistories))
		copy(b.diffHistories, diffHistories)
	} else {
		b.diffHistories = []*types.DiffEntry{}
	}

	if originalLines != nil {
		b.originalLines = make([]string, len(originalLines))
		copy(b.originalLines, originalLines)
	}
}

// Sync reads current state from the editor
func (b *NvimBuffer) Sync(workspacePath string) (*SyncResult, error) {
	defer logger.Trace("buffer.Sync")()
	if b.client == nil {
		return nil, fmt.Errorf("nvim client not set")
	}

	// Use batch API to make all calls in a single round-trip
	batch := b.client.NewBatch()

	var currentBuf nvim.Buffer
	var path string
	var lines [][]byte
	var window nvim.Window
	var cursor [2]int
	var scrollOffset int
	var viewportBounds [2]int
	var nvimCwd string

	batch.CurrentBuffer(&currentBuf)
	batch.BufferName(nvim.Buffer(0), &path) // Use 0 for current buffer
	batch.BufferLines(nvim.Buffer(0), 0, -1, false, &lines)
	batch.CurrentWindow(&window)
	batch.WindowCursor(nvim.Window(0), &cursor) // Use 0 for current window

	// Get Neovim's current working directory
	batch.ExecLua(`return vim.fn.getcwd()`, &nvimCwd, nil)

	// Get horizontal scroll offset (leftcol) from current window
	batch.ExecLua(`
		local view = vim.fn.winsaveview()
		return view.leftcol or 0
	`, &scrollOffset, nil)

	// Get vertical viewport bounds (first and last visible line numbers, 1-indexed)
	batch.ExecLua(`
		return {vim.fn.line("w0"), vim.fn.line("w$")}
	`, &viewportBounds, nil)

	if err := batch.Execute(); err != nil {
		logger.Error("error executing sync batch: %v", err)
		return nil, err
	}

	linesStr := make([]string, len(lines))
	for i, line := range lines {
		linesStr[i] = string(line[:])
	}

	// Store old path before updating
	oldPath := b.path

	// Update buffer state
	b.lines = linesStr
	b.row = cursor[0]              // Line (vertical position, 1-based in nvim cursor)
	b.col = cursor[1]              // Column (horizontal position, 0-based in nvim cursor)
	b.scrollOffsetX = scrollOffset // Horizontal scroll offset

	// Update viewport bounds (1-indexed)
	b.viewportTop = viewportBounds[0]
	b.viewportBottom = viewportBounds[1]

	// Convert absolute path to relative workspace path using Neovim's actual cwd
	relativePath := makeRelativeToWorkspace(path, nvimCwd)
	b.path = relativePath

	// Handle buffer change
	if b.id != currentBuf {
		// New buffer - update buffer ID and reset basic state
		// Note: previousLines, diffHistories, and originalLines are managed by the engine
		// to enable proper context restoration when switching back to this file
		b.id = currentBuf
		b.lastModifiedLine = -1
		b.version = 0

		return &SyncResult{
			BufferChanged: true,
			OldPath:       oldPath,
			NewPath:       relativePath,
		}, nil
	}

	// Same buffer - no change
	return &SyncResult{
		BufferChanged: false,
		OldPath:       oldPath,
		NewPath:       relativePath,
	}, nil
}

// Helper function to convert absolute path to relative workspace path
func makeRelativeToWorkspace(absolutePath, workspacePath string) string {
	absolutePath = filepath.Clean(absolutePath)
	workspacePath = filepath.Clean(workspacePath)

	// If the file is within the workspace, make it relative
	if relativePath, found := strings.CutPrefix(absolutePath, workspacePath); found {
		relativePath = strings.TrimPrefix(relativePath, string(filepath.Separator))
		return relativePath
	}

	return absolutePath
}

// HasChanges checks if the proposed completion would introduce actual changes
func (b *NvimBuffer) HasChanges(startLine, endLineInclusive int, lines []string) bool {
	// Check the original replacement range for changes
	for i := startLine; i <= endLineInclusive; i++ {
		relativeLineIdx := i - startLine

		var l *string
		var realL *string

		if i-1 >= 0 && i-1 < len(b.lines) {
			realL = &b.lines[i-1]
		}

		if relativeLineIdx < len(lines) {
			l = &lines[relativeLineIdx]
		}

		if (l != nil && realL != nil && *l != *realL) ||
			(l != nil && realL == nil) ||
			(l == nil && realL != nil) {
			return true
		}
	}

	// Check if there are additional lines beyond the replacement range (insertions)
	if startLine+len(lines)-1 > endLineInclusive {
		return true
	}

	return false
}

// nvimBatch wraps nvim.Batch to implement the Batch interface
type nvimBatch struct {
	batch *nvim.Batch
}

func (nb *nvimBatch) Execute() error {
	if nb.batch == nil {
		return nil
	}
	return nb.batch.Execute()
}

// PrepareCompletion prepares a completion for display and returns a batch to apply it
func (b *NvimBuffer) PrepareCompletion(startLine, endLineInc int, lines []string, groups []*text.Group) Batch {
	if b.client == nil {
		return &nvimBatch{batch: nil}
	}

	// Compute diff
	diffResult := b.getDiffResult(startLine, endLineInc, lines)

	// Get original lines for grouping
	var originalLines []string
	for i := startLine; i <= endLineInc && i-1 < len(b.lines); i++ {
		originalLines = append(originalLines, b.lines[i-1])
	}

	// Groups are pre-computed by staging with BufferLine already set

	applyBatch := b.getApplyBatch(startLine, endLineInc, lines, diffResult)

	// Convert to Lua format
	luaDiffResult := diffResultToLuaFormat(diffResult, groups, lines, startLine)

	// Debug logging for data sent to Lua
	if jsonData, err := json.Marshal(luaDiffResult); err == nil {
		logger.Debug("sending to lua on_completion_ready:\n  startLine: %d\n  endLineInclusive: %d\n  lines: %d\n  diffResult: %s",
			startLine, endLineInc, len(lines), string(jsonData))
	}

	b.executeLuaFunction("require('cursortab').on_completion_ready(...)", luaDiffResult)

	return &nvimBatch{batch: applyBatch}
}

// CommitPending applies the pending edit to buffer state, increments version,
// and appends structured diff entries showing before/after content. No-op if no pending edit.
func (b *NvimBuffer) CommitPending() {
	if !b.hasPending {
		return
	}

	startLine := b.pendingStartLine
	endLineInclusive := b.pendingEndLineInclusive
	lines := b.pendingLines

	// Extract only the affected original lines (the range being replaced)
	var originalRangeLines []string
	for i := startLine; i <= endLineInclusive && i-1 < len(b.originalLines); i++ {
		originalRangeLines = append(originalRangeLines, b.originalLines[i-1])
	}

	// Extract granular diffs - one DiffEntry per contiguous changed region
	diffEntries := extractGranularDiffs(originalRangeLines, lines)
	b.diffHistories = append(b.diffHistories, diffEntries...)

	// Compute the final buffer state after applying the completion
	newLines := make([]string, 0, len(b.lines)-((endLineInclusive-startLine)+1)+len(lines))
	if startLine-1 > 0 && startLine-1 <= len(b.lines) {
		newLines = append(newLines, b.lines[:startLine-1]...)
	}
	newLines = append(newLines, lines...)
	if endLineInclusive < len(b.lines) {
		newLines = append(newLines, b.lines[endLineInclusive:]...)
	}

	// Reset checkpoint to current state for next working diff
	b.originalLines = make([]string, len(newLines))
	copy(b.originalLines, newLines)

	// Save current lines as previous state BEFORE updating (for sweep provider)
	b.previousLines = make([]string, len(b.lines))
	copy(b.previousLines, b.lines)

	// Commit the new content and bump version
	b.lines = make([]string, len(newLines))
	copy(b.lines, newLines)
	b.version++

	// Clear pending
	b.pendingStartLine = 0
	b.pendingEndLineInclusive = 0
	b.pendingLines = nil
	b.hasPending = false
}

// CommitUserEdits extracts diffs between originalLines checkpoint and current lines,
// appends them to diffHistories, and resets the checkpoint.
// Call this when leaving insert mode to capture manual edits.
// Returns true if any changes were committed, false if no changes.
func (b *NvimBuffer) CommitUserEdits() bool {
	// Quick check: if lengths differ, there are changes
	if len(b.lines) != len(b.originalLines) {
		return b.commitUserEditsInternal()
	}

	// Check for content differences
	for i := range b.lines {
		if b.lines[i] != b.originalLines[i] {
			return b.commitUserEditsInternal()
		}
	}

	return false // No changes
}

func (b *NvimBuffer) commitUserEditsInternal() bool {
	// Extract granular diffs between checkpoint and current state
	diffEntries := extractGranularDiffs(b.originalLines, b.lines)
	if len(diffEntries) == 0 {
		return false
	}

	b.diffHistories = append(b.diffHistories, diffEntries...)

	// Save checkpoint as previous state (for sweep provider)
	b.previousLines = make([]string, len(b.originalLines))
	copy(b.previousLines, b.originalLines)

	// Reset checkpoint to current state
	b.originalLines = make([]string, len(b.lines))
	copy(b.originalLines, b.lines)

	b.version++
	return true
}

// ShowCursorTarget displays a cursor prediction indicator at the given line
func (b *NvimBuffer) ShowCursorTarget(line int) error {
	if b.client == nil {
		return fmt.Errorf("nvim client not set")
	}
	logger.Debug("sending to lua on_cursor_prediction_ready: line=%d", line)
	b.executeLuaFunction("require('cursortab').on_cursor_prediction_ready(...)", line)
	return nil
}

// ClearUI clears the completion UI
func (b *NvimBuffer) ClearUI() error {
	if b.client == nil {
		return fmt.Errorf("nvim client not set")
	}

	// Clear pending state to prevent stale data from being committed
	b.hasPending = false
	b.pendingLines = nil

	logger.Debug("sending to lua on_reject")
	b.executeLuaFunction("require('cursortab').on_reject()")
	return nil
}

// MoveCursor moves the cursor to the start of the specified line
func (b *NvimBuffer) MoveCursor(line int, center bool, mark bool) error {
	if b.client == nil {
		return fmt.Errorf("nvim client not set")
	}

	batch := b.client.NewBatch()
	applyCursorMove(batch, line, 0, center, mark)
	batch.ExecLua("vim.cmd('normal! ^')", nil, nil) // Move cursor to start of line
	return batch.Execute()
}

// InsertText inserts text at the specified position (1-indexed line, 0-indexed col)
func (b *NvimBuffer) InsertText(line, col int, text string) error {
	if b.client == nil {
		return fmt.Errorf("nvim client not set")
	}

	// Get current line content
	batch := b.client.NewBatch()
	var lines [][]byte
	batch.BufferLines(b.id, line-1, line, true, &lines)
	if err := batch.Execute(); err != nil {
		return err
	}

	if len(lines) == 0 {
		return nil
	}

	currentLine := string(lines[0])
	if col > len(currentLine) {
		col = len(currentLine)
	}

	// Build new line with inserted text
	newLine := currentLine[:col] + text + currentLine[col:]

	// Clear UI namespace and apply the change
	batch = b.client.NewBatch()
	b.clearNamespace(batch, b.config.NsID)
	batch.SetBufferLines(b.id, line-1, line, false, [][]byte{[]byte(newLine)})

	// Move cursor to end of inserted text
	newCol := col + len(text)
	applyCursorMove(batch, line, newCol, false, true)

	return batch.Execute()
}

// ReplaceLine replaces a single line (1-indexed)
func (b *NvimBuffer) ReplaceLine(line int, content string) error {
	if b.client == nil {
		return fmt.Errorf("nvim client not set")
	}

	batch := b.client.NewBatch()
	b.clearNamespace(batch, b.config.NsID)
	batch.SetBufferLines(b.id, line-1, line, false, [][]byte{[]byte(content)})

	// Move cursor to end of line
	applyCursorMove(batch, line, len(content), false, true)

	return batch.Execute()
}

// InsertLine inserts a new line at the given position (1-indexed), pushing existing lines down
func (b *NvimBuffer) InsertLine(line int, content string) error {
	if b.client == nil {
		return fmt.Errorf("nvim client not set")
	}

	// First batch: clear namespace and insert line
	batch := b.client.NewBatch()
	b.clearNamespace(batch, b.config.NsID)
	// Insert at line-1 without removing any lines (start == end)
	batch.SetBufferLines(b.id, line-1, line-1, false, [][]byte{[]byte(content)})
	if err := batch.Execute(); err != nil {
		return err
	}

	// Second batch: move cursor (must be after line is inserted)
	cursorBatch := b.client.NewBatch()
	applyCursorMove(cursorBatch, line, len(content), false, true)
	return cursorBatch.Execute()
}

// LinterErrors retrieves Neovim diagnostics for the current buffer and returns them in provider format
func (b *NvimBuffer) LinterErrors() *types.LinterErrors {
	if b.client == nil {
		return nil
	}

	// First check if LSP is available for this buffer
	batch := b.client.NewBatch()
	var hasLsp bool

	// Check if there are any active LSP clients attached to this buffer
	batch.ExecLua(fmt.Sprintf(`
		local clients = vim.lsp.get_clients and vim.lsp.get_clients({bufnr = %d}) or vim.lsp.get_active_clients({bufnr = %d})
		return #clients > 0
	`, int(b.id), int(b.id)), &hasLsp, nil)

	if err := batch.Execute(); err != nil {
		logger.Error("error checking LSP availability: %v", err)
		return nil
	}

	// If no LSP is available, return nil
	if !hasLsp {
		return nil
	}

	// Use batch API to get diagnostics for the current buffer
	batch = b.client.NewBatch()

	var diagnostics []map[string]any
	var bufferContents [][]byte

	// Get diagnostics using vim.diagnostic.get() for this specific buffer
	batch.ExecLua(fmt.Sprintf(`
		local diagnostics = vim.diagnostic.get(%d)
		return diagnostics
	`, int(b.id)), &diagnostics, nil)

	// Get buffer contents for the linter errors
	batch.BufferLines(b.id, 0, -1, false, &bufferContents)

	if err := batch.Execute(); err != nil {
		logger.Error("error getting linter errors: %v", err)
		return nil
	}

	if len(diagnostics) == 0 {
		return nil
	}

	// Convert buffer contents to string
	contentLines := make([]string, len(bufferContents))
	for i, line := range bufferContents {
		contentLines[i] = string(line)
	}
	fileContents := strings.Join(contentLines, "\n")

	// Convert Neovim diagnostics to types.LinterError format
	providerErrors := make([]*types.LinterError, 0, len(diagnostics))

	for _, diag := range diagnostics {
		linterError := &types.LinterError{
			Message: getString(diag, "message"),
			Source:  getString(diag, "source"),
		}

		// Convert severity
		if severity, ok := diag["severity"].(float64); ok {
			switch int(severity) {
			case 1:
				linterError.Severity = "DIAGNOSTIC_SEVERITY_ERROR"
			case 2:
				linterError.Severity = "DIAGNOSTIC_SEVERITY_WARNING"
			case 3:
				linterError.Severity = "DIAGNOSTIC_SEVERITY_INFORMATION"
			case 4:
				linterError.Severity = "DIAGNOSTIC_SEVERITY_HINT"
			default:
				linterError.Severity = "DIAGNOSTIC_SEVERITY_ERROR"
			}
		} else {
			linterError.Severity = "DIAGNOSTIC_SEVERITY_ERROR"
		}

		// Convert range information
		if lnum := getNumber(diag, "lnum"); lnum != -1 {
			if col := getNumber(diag, "col"); col != -1 {
				endLnum := lnum
				endCol := col

				if endL := getNumber(diag, "end_lnum"); endL != -1 {
					endLnum = endL
				}
				if endC := getNumber(diag, "end_col"); endC != -1 {
					endCol = endC
				}

				linterError.Range = &types.CursorRange{
					StartLine:      lnum,
					StartCharacter: col,
					EndLine:        endLnum,
					EndCharacter:   endCol,
				}
			}
		}

		providerErrors = append(providerErrors, linterError)
	}

	return &types.LinterErrors{
		RelativeWorkspacePath: b.path,
		Errors:                providerErrors,
		FileContents:          fileContents,
	}
}

// RegisterEventHandler registers a handler for nvim RPC events
func (b *NvimBuffer) RegisterEventHandler(handler func(event string)) error {
	if b.client == nil {
		return fmt.Errorf("nvim client not set")
	}
	return b.client.RegisterHandler("cursortab_event", func(_ *nvim.Nvim, event string) {
		handler(event)
	})
}

// Internal helper methods

func (b *NvimBuffer) executeLuaFunction(luaCode string, args ...any) {
	if b.client == nil {
		return
	}
	batch := b.client.NewBatch()
	if len(args) > 0 {
		batch.ExecLua(luaCode, nil, args...)
	} else {
		batch.ExecLua(luaCode, nil, nil)
	}
	if err := batch.Execute(); err != nil {
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

func (b *NvimBuffer) getDiffResult(startLine, endLineInclusive int, lines []string) *text.DiffResult {
	originalLines := []string{}
	for i := startLine; i <= endLineInclusive && i-1 < len(b.lines); i++ {
		originalLines = append(originalLines, b.lines[i-1])
	}
	oldText := text.JoinLines(originalLines)
	newText := text.JoinLines(lines)
	return text.ComputeDiff(oldText, newText)
}

func (b *NvimBuffer) getApplyBatch(startLine, endLineInclusive int, lines []string, diffResult *text.DiffResult) *nvim.Batch {
	// Create apply batch for the completion
	applyBatch := b.client.NewBatch()

	b.clearNamespace(applyBatch, b.config.NsID)

	placeBytes := make([][]byte, len(lines))
	for i, line := range lines {
		placeBytes[i] = []byte(line)
	}

	// execute lua to actually clear the lines within the range beforehand
	applyBatch.ExecLua(fmt.Sprintf("vim.cmd('normal! %v,%vd')", startLine, endLineInclusive), nil, nil)

	applyBatch.SetBufferLines(b.id, startLine-1, endLineInclusive, false, placeBytes)

	// Apply cursor positioning from diff changes
	cursorLine, cursorCol := text.CalculateCursorPosition(diffResult.Changes, lines)
	if cursorLine >= 0 && cursorCol >= 0 {
		// Convert from diff line numbers (relative to new text) to buffer line numbers
		bufferLine := startLine + cursorLine - 1
		applyCursorMove(applyBatch, bufferLine, cursorCol, false, true)
	}

	// Mark as pending; actual commit happens on accept
	b.pendingStartLine = startLine
	b.pendingEndLineInclusive = endLineInclusive
	b.pendingLines = append([]string{}, lines...)
	b.hasPending = true

	return applyBatch
}

func (b *NvimBuffer) clearNamespace(batch *nvim.Batch, nsID int) {
	batch.ClearBufferNamespace(b.id, nsID, 0, -1)
}

// diffResultToLuaFormat converts diff result and groups to a format suitable for Lua rendering
func diffResultToLuaFormat(diffResult *text.DiffResult, groups []*text.Group, newLines []string, startLine int) map[string]any {
	// Compute cursor position
	cursorLine, cursorCol := text.CalculateCursorPosition(diffResult.Changes, newLines)

	// Build groups array for Lua
	var luaGroups []map[string]any
	for _, g := range groups {
		luaGroup := map[string]any{
			"type":        g.Type,
			"start_line":  g.StartLine,
			"end_line":    g.EndLine,
			"buffer_line": g.BufferLine,
			"lines":       g.Lines,
			"old_lines":   g.OldLines,
		}

		// Add render hint for character-level optimizations
		if g.RenderHint != "" {
			luaGroup["render_hint"] = g.RenderHint
			luaGroup["col_start"] = g.ColStart
			luaGroup["col_end"] = g.ColEnd
		}

		luaGroups = append(luaGroups, luaGroup)
	}

	return map[string]any{
		"startLine":   startLine,
		"groups":      luaGroups,
		"cursor_line": cursorLine,
		"cursor_col":  cursorCol,
	}
}

// CopilotClientInfo contains information about an attached Copilot LSP client
type CopilotClientInfo struct {
	ID             int
	OffsetEncoding string
}

// copilotClientLookupLua is the shared Lua code for finding the Copilot LSP client.
// Checks for both copilot.lua ("copilot") and copilot.vim ("GitHub Copilot") client names.
const copilotClientLookupLua = `
local function find_copilot_client()
	local clients = vim.lsp.get_clients({name = "copilot"})
	if #clients > 0 then return clients[1] end
	clients = vim.lsp.get_clients({name = "GitHub Copilot"})
	if #clients > 0 then return clients[1] end
	return nil
end
`

// GetCopilotClient returns info about the Copilot LSP client if attached to the current buffer
func (b *NvimBuffer) GetCopilotClient() (*CopilotClientInfo, error) {
	if b.client == nil {
		return nil, fmt.Errorf("nvim client not set")
	}

	var result []map[string]any
	batch := b.client.NewBatch()
	batch.ExecLua(copilotClientLookupLua+`
		local client = find_copilot_client()
		if not client then
			return {}
		end
		return {{id = client.id, offset_encoding = client.offset_encoding or "utf-16"}}
	`, &result, nil)

	if err := batch.Execute(); err != nil {
		return nil, fmt.Errorf("failed to get Copilot client: %w", err)
	}

	if len(result) == 0 {
		return nil, nil // No Copilot client attached
	}

	return &CopilotClientInfo{
		ID:             getNumber(result[0], "id"),
		OffsetEncoding: getString(result[0], "offset_encoding"),
	}, nil
}

// SendCopilotDidFocus sends textDocument/didFocus notification to Copilot LSP
func (b *NvimBuffer) SendCopilotDidFocus(uri string) error {
	if b.client == nil {
		return fmt.Errorf("nvim client not set")
	}

	batch := b.client.NewBatch()
	batch.ExecLua(copilotClientLookupLua+`
		local uri = ...
		local client = find_copilot_client()
		if client then
			client:notify("textDocument/didFocus", { textDocument = { uri = uri } })
		end
	`, nil, uri)

	return batch.Execute()
}

// SendCopilotNESRequest sends textDocument/copilotInlineEdit request and delivers response via registered handler
func (b *NvimBuffer) SendCopilotNESRequest(reqID int64, uri string, version int, row, col int) error {
	if b.client == nil {
		return fmt.Errorf("nvim client not set")
	}

	// Get the channel ID for RPC communication back to Go
	chanID := b.client.ChannelID()

	batch := b.client.NewBatch()
	// The Lua code sends the request and uses rpcnotify to deliver the response back to Go
	batch.ExecLua(copilotClientLookupLua+`
		local chanID, reqID, uri, version, row, col = ...
		local client = find_copilot_client()
		if not client then
			vim.fn.rpcnotify(chanID, "cursortab_copilot_response", reqID, "[]", "no copilot client")
			return
		end

		local params = {
			textDocument = {
				uri = uri,
				version = version,
			},
			position = {
				line = row - 1,  -- Convert to 0-indexed
				character = col, -- Already 0-indexed
			},
			context = { triggerKind = 2 },
		}

		client:request("textDocument/copilotInlineEdit", params, function(err, result)
			local edits_json = "[]"
			local err_msg = ""
			if err then
				err_msg = vim.json.encode(err)
			elseif result and result.edits then
				edits_json = vim.json.encode(result.edits)
			end
			vim.fn.rpcnotify(chanID, "cursortab_copilot_response", reqID, edits_json, err_msg)
		end)
	`, nil, chanID, reqID, uri, version, row, col)

	return batch.Execute()
}

// ExecuteCopilotCommand executes a Copilot telemetry command (for accept tracking)
func (b *NvimBuffer) ExecuteCopilotCommand(command string, arguments []any) error {
	if b.client == nil {
		return fmt.Errorf("nvim client not set")
	}

	batch := b.client.NewBatch()
	batch.ExecLua(copilotClientLookupLua+`
		local command, arguments = ...
		local client = find_copilot_client()
		if client then
			client:exec_cmd({command = command, arguments = arguments})
		end
	`, nil, command, arguments)

	return batch.Execute()
}

// RegisterCopilotHandler registers a handler for Copilot NES responses
func (b *NvimBuffer) RegisterCopilotHandler(handler func(reqID int64, editsJSON string, errMsg string)) error {
	if b.client == nil {
		return fmt.Errorf("nvim client not set")
	}
	return b.client.RegisterHandler("cursortab_copilot_response", func(_ *nvim.Nvim, reqID int64, editsJSON string, errMsg string) {
		handler(reqID, editsJSON, errMsg)
	})
}

// extractGranularDiffs analyzes old and new lines and returns DiffEntry records
// for each contiguous region that changed.
func extractGranularDiffs(oldLines, newLines []string) []*types.DiffEntry {
	oldText := text.JoinLines(oldLines)
	newText := text.JoinLines(newLines)

	if oldText == newText {
		return nil
	}

	dmp := diffmatchpatch.New()
	chars1, chars2, lineArray := dmp.DiffLinesToChars(oldText, newText)
	diffs := dmp.DiffMain(chars1, chars2, false)
	lineDiffs := dmp.DiffCharsToLines(diffs, lineArray)

	var entries []*types.DiffEntry

	for i := 0; i < len(lineDiffs); i++ {
		diff := lineDiffs[i]

		switch diff.Type {
		case diffmatchpatch.DiffEqual:
			// No change, skip
			continue

		case diffmatchpatch.DiffDelete:
			// Get the deleted content (trim trailing newline from diff output)
			deletedText := strings.TrimSuffix(diff.Text, "\n")
			insertedText := ""

			// Check if followed by an insert (modification pattern)
			if i+1 < len(lineDiffs) && lineDiffs[i+1].Type == diffmatchpatch.DiffInsert {
				insertedText = strings.TrimSuffix(lineDiffs[i+1].Text, "\n")
				i++ // Skip the insert in next iteration
			}

			entries = append(entries, &types.DiffEntry{
				Original: deletedText,
				Updated:  insertedText,
			})

		case diffmatchpatch.DiffInsert:
			// Pure insertion (no preceding delete)
			insertedText := strings.TrimSuffix(diff.Text, "\n")
			entries = append(entries, &types.DiffEntry{
				Original: "",
				Updated:  insertedText,
			})
		}
	}

	return entries
}

// Helper function to safely get string from map
func getString(m map[string]any, key string) string {
	if val, ok := m[key].(string); ok {
		return val
	}
	return ""
}

// Helper function to safely get number from map, handling both int and float64
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
	return -1
}
