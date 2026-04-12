// Package copilot implements the GitHub Copilot NES (Next-Edit Suggestion) provider.
//
// This provider delegates to the Copilot LSP server already running in Neovim.
// No prompt is built in Go — instead, a textDocument/copilotInlineEdit LSP request
// is sent via Neovim RPC:
//
//	{
//	  "textDocument": {"uri": "file:///path/to/file.go", "version": 5},
//	  "position":     {"line": 9, "character": 12},     // 0-indexed, UTF-16
//	  "context":      {"triggerKind": 2}
//	}
//
// The Copilot LSP responds with an array of edits (LSP TextEdit format with
// UTF-16 character offsets), which are converted to line-based completions.
package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf16"
	"unicode/utf8"

	"cursortab/buffer"
	"cursortab/engine"
	"cursortab/logger"
	"cursortab/types"
)

// CopilotEdit represents a single edit from Copilot NES response
type CopilotEdit struct {
	Text    string       `json:"text"`
	Range   CopilotRange `json:"range"`
	Command *CopilotCmd  `json:"command,omitempty"`
	TextDoc CopilotDoc   `json:"textDocument"`
}

// CopilotRange represents an LSP range (0-indexed)
type CopilotRange struct {
	Start CopilotPos `json:"start"`
	End   CopilotPos `json:"end"`
}

// CopilotPos represents an LSP position (0-indexed)
type CopilotPos struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// CopilotCmd represents a command to execute (for telemetry)
type CopilotCmd struct {
	Command   string `json:"command"`
	Arguments []any  `json:"arguments"`
}

// CopilotDoc represents a text document identifier
type CopilotDoc struct {
	URI     string `json:"uri"`
	Version int    `json:"version"`
}

// CopilotResult holds the result of a Copilot NES request
type CopilotResult struct {
	Edits []CopilotEdit
	Error error
}

// LSPBuffer is the minimum set of buffer methods the Copilot provider needs.
// NvimBuffer satisfies this interface via duck typing. The eval harness
// provides a cassette-backed implementation that replays recorded LSP
// exchanges without requiring a live Neovim session.
type LSPBuffer interface {
	GetCopilotClient() (*buffer.CopilotClientInfo, error)
	SendCopilotDidFocus(uri string) error
	SendCopilotNESRequest(reqID int64, uri string) error
	RegisterCopilotHandler(handler func(reqID int64, editsJSON string, errMsg string)) error
}

// Provider implements engine.Provider for Copilot NES
type Provider struct {
	buffer LSPBuffer

	// Async request state
	mu            sync.Mutex
	reqIDCounter  int64
	pendingReqID  int64
	pendingResult chan *CopilotResult

	// Track last focused URI to avoid redundant didFocus notifications
	lastFocusedURI string

	// Handler registration tracking
	handlerRegistered bool
	lastClientID      int // Track client ID to detect reconnection
}

// NewProvider creates a new Copilot provider
func NewProvider(buf LSPBuffer) *Provider {
	return &Provider{
		buffer:        buf,
		pendingResult: make(chan *CopilotResult, 1),
	}
}

// GetContextLimits implements engine.Provider.
// Copilot delegates all context gathering to its LSP server.
func (p *Provider) GetContextLimits() engine.ContextLimits {
	return engine.ContextLimits{
		MaxUserActions:     -1,
		FileChunkLines:     -1,
		MaxRecentSnapshots: -1,
		MaxDiffBytes:       -1,
		MaxChangedSymbols:  -1,
		MaxSiblings:        -1,
		MaxInputLines:      -1,
		MaxInputBytes:      -1,
	}
}

// GetCompletion implements engine.Provider
func (p *Provider) GetCompletion(ctx context.Context, req *types.CompletionRequest) (*types.CompletionResponse, error) {
	defer logger.Trace("copilot.GetCompletion")()

	// Check if Copilot client is available
	clientInfo, err := p.buffer.GetCopilotClient()
	if err != nil {
		logger.Error("failed to check copilot client: %v", err)
		return p.emptyResponse(), nil
	}
	if clientInfo == nil {
		logger.Debug("copilot: no client attached")
		return p.emptyResponse(), nil
	}

	// Ensure handler is registered (and re-register on reconnection)
	if err := p.ensureHandlerRegistered(clientInfo.ID); err != nil {
		logger.Error("failed to register copilot handler: %v", err)
		return p.emptyResponse(), nil
	}

	// Build URI from file path
	uri := "file://" + req.FilePath
	if !strings.HasPrefix(req.FilePath, "/") {
		// Relative path - prepend workspace
		uri = "file://" + req.WorkspacePath + "/" + req.FilePath
	}

	// Generate unique request ID
	reqID := atomic.AddInt64(&p.reqIDCounter, 1)

	// Set up pending request (mutex protects all shared state)
	p.mu.Lock()
	// Send didFocus if URI changed
	if uri != p.lastFocusedURI {
		if err := p.buffer.SendCopilotDidFocus(uri); err != nil {
			logger.Warn("failed to send didFocus: %v", err)
		}
		p.lastFocusedURI = uri
	}

	p.pendingReqID = reqID
	// Drain any stale results
	select {
	case <-p.pendingResult:
	default:
	}
	p.mu.Unlock()

	logger.Debug("copilot request:\n  ReqID: %d\n  URI: %s\n  CursorRow: %d\n  CursorCol: %d",
		reqID, uri, req.CursorRow, req.CursorCol)
	if err := p.buffer.SendCopilotNESRequest(reqID, uri); err != nil {
		logger.Error("failed to send NES request: %v", err)
		return p.emptyResponse(), nil
	}

	// Wait for response with context timeout
	select {
	case <-ctx.Done():
		logger.Debug("copilot: request cancelled")
		return p.emptyResponse(), nil
	case result := <-p.pendingResult:
		if result.Error != nil {
			logger.Warn("copilot: NES request failed: %v", result.Error)
			return p.emptyResponse(), nil
		}

		p.logResponse(result.Edits)
		return p.convertEdits(result.Edits, req)
	}
}

// HandleNESResponse is called by the RPC handler when Copilot responds
func (p *Provider) HandleNESResponse(reqID int64, editsJSON string, errMsg string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if reqID != p.pendingReqID {
		logger.Debug("copilot: ignoring stale response reqID=%d (pending=%d)", reqID, p.pendingReqID)
		return
	}

	result := &CopilotResult{}

	if errMsg != "" {
		result.Error = fmt.Errorf("copilot error: %s", errMsg)
	} else {
		var edits []CopilotEdit
		if err := json.Unmarshal([]byte(editsJSON), &edits); err != nil {
			result.Error = fmt.Errorf("failed to parse edits: %w", err)
		} else {
			result.Edits = edits
		}
	}

	// Non-blocking send to avoid deadlock if no one is waiting
	// Safe to send while holding mutex since GetCompletion releases lock before receiving
	select {
	case p.pendingResult <- result:
	default:
		logger.Debug("copilot: result channel full, dropping response")
	}
}

// ensureHandlerRegistered registers the RPC handler for Copilot responses.
// Re-registers if the client ID changed (indicating a reconnection).
func (p *Provider) ensureHandlerRegistered(clientID int) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Re-register if client changed (reconnection) or not yet registered
	if p.handlerRegistered && p.lastClientID == clientID {
		return nil
	}

	if err := p.buffer.RegisterCopilotHandler(p.HandleNESResponse); err != nil {
		return err
	}
	p.handlerRegistered = true
	p.lastClientID = clientID
	return nil
}

func (p *Provider) logResponse(edits []CopilotEdit) {
	var sb strings.Builder
	for i, edit := range edits {
		fmt.Fprintf(&sb, "  Edit %d: range=[%d:%d-%d:%d] version=%d textLen=%d\n    Text:\n%s\n",
			i,
			edit.Range.Start.Line, edit.Range.Start.Character,
			edit.Range.End.Line, edit.Range.End.Character,
			edit.TextDoc.Version,
			len(edit.Text),
			edit.Text)
	}
	logger.Debug("copilot response: %d edits\n%s", len(edits), sb.String())
}

// convertEdits transforms Copilot LSP edits to cursortab's CompletionResponse format.
// Processes all edits and returns multiple completions for staging to handle.
func (p *Provider) convertEdits(edits []CopilotEdit, req *types.CompletionRequest) (*types.CompletionResponse, error) {
	if len(edits) == 0 {
		return p.emptyResponse(), nil
	}

	var completions []*types.Completion

	for i, edit := range edits {
		// Store command for telemetry
		if edit.Command != nil {
			logger.Debug("copilot: edit %d has command: %s", i, edit.Command.Command)
		}

		completion := p.convertSingleEdit(edit, req, i)
		if completion != nil {
			completions = append(completions, completion)
		}
	}

	if len(completions) == 0 {
		return p.emptyResponse(), nil
	}

	logger.Debug("copilot: converted %d edits to %d completions", len(edits), len(completions))

	return &types.CompletionResponse{
		Completions: completions,
	}, nil
}

// convertSingleEdit converts a single Copilot edit to a Completion
func (p *Provider) convertSingleEdit(edit CopilotEdit, req *types.CompletionRequest, editIdx int) *types.Completion {
	// Convert 0-indexed LSP range to 1-indexed buffer lines
	startLine := edit.Range.Start.Line + 1
	endLine := edit.Range.End.Line + 1

	// Bounds check
	if startLine < 1 || startLine > len(req.Lines)+1 {
		logger.Debug("copilot: edit %d start line %d out of bounds", editIdx, startLine)
		return nil
	}

	// Handle case where end line is beyond buffer (insertion at end)
	if endLine > len(req.Lines) {
		endLine = len(req.Lines)
	}
	if endLine < startLine {
		endLine = startLine
	}

	// Get original lines being replaced (0-indexed slice)
	var origLines []string
	if edit.Range.Start.Line < len(req.Lines) {
		endIdx := min(edit.Range.End.Line+1, len(req.Lines))
		origLines = req.Lines[edit.Range.Start.Line:endIdx]
	}
	if len(origLines) == 0 {
		origLines = []string{""}
	}

	// Apply character-level edit to get new text
	newText := p.applyCharacterEdit(origLines, edit)
	newLines := strings.Split(newText, "\n")

	// Check if this is actually a change
	if slices.Equal(newLines, origLines) {
		logger.Debug("copilot: edit %d is no-op", editIdx)
		return nil
	}

	logger.Debug("copilot: converted edit %d startLine=%d endLine=%d newLines=%d", editIdx, startLine, endLine, len(newLines))

	return &types.Completion{
		StartLine:  startLine,
		EndLineInc: endLine,
		Lines:      newLines,
	}
}

// applyCharacterEdit applies an LSP edit with character positions to original lines.
// LSP uses UTF-16 code units for character positions, so we convert to byte offsets.
func (p *Provider) applyCharacterEdit(origLines []string, edit CopilotEdit) string {
	if len(origLines) == 0 {
		return edit.Text
	}

	firstLine := origLines[0]
	lastLine := origLines[len(origLines)-1]

	// Convert UTF-16 code unit positions to byte offsets
	startByte := utf16OffsetToBytes(firstLine, edit.Range.Start.Character)
	endByte := utf16OffsetToBytes(lastLine, edit.Range.End.Character)

	prefix := firstLine[:startByte]
	suffix := lastLine[endByte:]

	// Copilot NES sometimes returns ranges that don't cover the full line,
	// but the edit text is meant as a complete replacement. Detect this case:
	// Only apply heuristic for single-line edits where we can safely compare.
	if len(origLines) == 1 && startByte == 0 && suffix != "" {
		// Check if the original line content (minus suffix) is a prefix of the edit text
		origWithoutSuffix := firstLine[:endByte]
		if strings.HasPrefix(edit.Text, origWithoutSuffix) {
			// Edit text already includes what was being replaced, don't add suffix
			suffix = ""
		}
	}

	return prefix + edit.Text + suffix
}

// utf16OffsetToBytes converts a UTF-16 code unit offset to a byte offset in a UTF-8 string.
// LSP specifies positions in UTF-16 code units, but Go strings are UTF-8.
func utf16OffsetToBytes(s string, utf16Offset int) int {
	if utf16Offset <= 0 {
		return 0
	}

	byteOffset := 0
	utf16Pos := 0

	for _, r := range s {
		if utf16Pos >= utf16Offset {
			break
		}

		// Use standard library to determine UTF-16 code units for this rune
		utf16Pos += len(utf16.Encode([]rune{r}))
		byteOffset += utf8.RuneLen(r)
	}

	// If utf16Offset is beyond the string, clamp to string length
	if byteOffset > len(s) {
		return len(s)
	}

	return byteOffset
}

// emptyResponse returns an empty completion response
func (p *Provider) emptyResponse() *types.CompletionResponse {
	return &types.CompletionResponse{
		Completions:  []*types.Completion{},
		CursorTarget: nil,
	}
}
