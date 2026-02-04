package copilot

import (
	"context"
	"cursortab/buffer"
	"cursortab/logger"
	"cursortab/types"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

// CopilotEdit represents a single edit from Copilot NES response
type CopilotEdit struct {
	Text     string       `json:"text"`
	Range    CopilotRange `json:"range"`
	Command  *CopilotCmd  `json:"command,omitempty"`
	TextDoc  CopilotDoc   `json:"textDocument"`
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

// Provider implements engine.Provider for Copilot NES
type Provider struct {
	buffer *buffer.NvimBuffer

	// Async request state
	mu            sync.Mutex
	reqIDCounter  int64
	pendingReqID  int64
	pendingResult chan *CopilotResult

	// Last command for telemetry on accept
	lastCommand *CopilotCmd

	// Track last focused URI to avoid redundant didFocus notifications
	lastFocusedURI string

	// Handler registration (done once on first connection)
	handlerRegistered bool
}

// NewProvider creates a new Copilot provider
func NewProvider(buf *buffer.NvimBuffer) *Provider {
	return &Provider{
		buffer:        buf,
		pendingResult: make(chan *CopilotResult, 1),
	}
}

// GetCompletion implements engine.Provider
func (p *Provider) GetCompletion(ctx context.Context, req *types.CompletionRequest) (*types.CompletionResponse, error) {
	defer logger.Trace("copilot.GetCompletion")()

	// Ensure handler is registered
	if err := p.ensureHandlerRegistered(); err != nil {
		logger.Error("failed to register copilot handler: %v", err)
		return p.emptyResponse(), nil
	}

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

	// Build URI from file path
	uri := "file://" + req.FilePath
	if !strings.HasPrefix(req.FilePath, "/") {
		// Relative path - prepend workspace
		uri = "file://" + req.WorkspacePath + "/" + req.FilePath
	}

	// Send didFocus if URI changed
	if uri != p.lastFocusedURI {
		if err := p.buffer.SendCopilotDidFocus(uri); err != nil {
			logger.Warn("failed to send didFocus: %v", err)
		}
		p.lastFocusedURI = uri
	}

	// Generate unique request ID
	reqID := atomic.AddInt64(&p.reqIDCounter, 1)

	// Set up pending request
	p.mu.Lock()
	p.pendingReqID = reqID
	// Drain any stale results
	select {
	case <-p.pendingResult:
	default:
	}
	p.mu.Unlock()

	// Send NES request
	logger.Debug("copilot: sending NES request reqID=%d uri=%s version=%d row=%d col=%d",
		reqID, uri, req.Version, req.CursorRow, req.CursorCol)
	if err := p.buffer.SendCopilotNESRequest(reqID, uri, req.Version, req.CursorRow, req.CursorCol); err != nil {
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

		logger.Debug("copilot: received %d edits", len(result.Edits))
		return p.convertEdits(result.Edits, req)
	}
}

// HandleNESResponse is called by the RPC handler when Copilot responds
func (p *Provider) HandleNESResponse(reqID int64, editsJSON string, errMsg string) {
	p.mu.Lock()
	if reqID != p.pendingReqID {
		p.mu.Unlock()
		logger.Debug("copilot: ignoring stale response reqID=%d (pending=%d)", reqID, p.pendingReqID)
		return
	}
	p.mu.Unlock()

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
	select {
	case p.pendingResult <- result:
	default:
		logger.Debug("copilot: result channel full, dropping response")
	}
}

// AcceptCompletion implements engine.CompletionAccepter for telemetry
func (p *Provider) AcceptCompletion(ctx context.Context) {
	p.mu.Lock()
	cmd := p.lastCommand
	p.lastCommand = nil
	p.mu.Unlock()

	if cmd == nil {
		return
	}

	logger.Debug("copilot: executing telemetry command: %s", cmd.Command)
	if err := p.buffer.ExecuteCopilotCommand(cmd.Command, cmd.Arguments); err != nil {
		logger.Warn("failed to execute copilot command: %v", err)
	}
}

// ensureHandlerRegistered registers the RPC handler for Copilot responses (once per connection)
func (p *Provider) ensureHandlerRegistered() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.handlerRegistered {
		return nil
	}

	if err := p.buffer.RegisterCopilotHandler(p.HandleNESResponse); err != nil {
		return err
	}
	p.handlerRegistered = true
	return nil
}

// convertEdits transforms Copilot LSP edits to cursortab's CompletionResponse format
func (p *Provider) convertEdits(edits []CopilotEdit, req *types.CompletionRequest) (*types.CompletionResponse, error) {
	if len(edits) == 0 {
		return p.emptyResponse(), nil
	}

	// Process first edit (NES typically returns one edit)
	edit := edits[0]

	// Store command for telemetry on accept
	p.mu.Lock()
	p.lastCommand = edit.Command
	p.mu.Unlock()

	// Validate version matches (avoid stale edits)
	if edit.TextDoc.Version != 0 && edit.TextDoc.Version != req.Version {
		logger.Debug("copilot: discarding stale edit (version %d != %d)", edit.TextDoc.Version, req.Version)
		return p.emptyResponse(), nil
	}

	// Convert 0-indexed LSP range to 1-indexed buffer lines
	startLine := edit.Range.Start.Line + 1
	endLine := edit.Range.End.Line + 1

	// Bounds check
	if startLine < 1 || startLine > len(req.Lines)+1 {
		logger.Debug("copilot: edit start line %d out of bounds", startLine)
		return p.emptyResponse(), nil
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
		endIdx := edit.Range.End.Line + 1
		if endIdx > len(req.Lines) {
			endIdx = len(req.Lines)
		}
		origLines = req.Lines[edit.Range.Start.Line:endIdx]
	}
	if len(origLines) == 0 {
		origLines = []string{""}
	}

	// Apply character-level edit to get new text
	newText := p.applyCharacterEdit(origLines, edit)
	newLines := strings.Split(newText, "\n")

	// Check if this is actually a change
	if isNoOp(newLines, origLines) {
		logger.Debug("copilot: edit is no-op")
		return p.emptyResponse(), nil
	}

	logger.Debug("copilot: converted edit startLine=%d endLine=%d newLines=%d", startLine, endLine, len(newLines))

	return &types.CompletionResponse{
		Completions: []*types.Completion{{
			StartLine:  startLine,
			EndLineInc: endLine,
			Lines:      newLines,
		}},
	}, nil
}

// applyCharacterEdit applies an LSP edit with character positions to original lines
func (p *Provider) applyCharacterEdit(origLines []string, edit CopilotEdit) string {
	if len(origLines) == 0 {
		return edit.Text
	}

	firstLine := origLines[0]
	lastLine := origLines[len(origLines)-1]

	// Calculate prefix from first line (before start character)
	startChar := edit.Range.Start.Character
	if startChar > len(firstLine) {
		startChar = len(firstLine)
	}
	prefix := firstLine[:startChar]

	// Calculate suffix from last line (after end character)
	endChar := edit.Range.End.Character
	if endChar > len(lastLine) {
		endChar = len(lastLine)
	}
	suffix := lastLine[endChar:]

	// Copilot NES sometimes returns ranges that don't cover the full line,
	// but the edit text is meant as a complete replacement. Detect this case:
	// If start is 0 (full line replacement) and suffix would create duplicate/garbage,
	// check if the edit text already contains a logical completion of the original content.
	if startChar == 0 && suffix != "" {
		// Check if the original line content (minus suffix) is a prefix of the edit text
		origWithoutSuffix := firstLine[:endChar]
		if strings.HasPrefix(edit.Text, origWithoutSuffix) {
			// Edit text already includes what was being replaced, don't add suffix
			suffix = ""
		}
	}

	return prefix + edit.Text + suffix
}

// emptyResponse returns an empty completion response
func (p *Provider) emptyResponse() *types.CompletionResponse {
	return &types.CompletionResponse{
		Completions:  []*types.Completion{},
		CursorTarget: nil,
	}
}

// isNoOp checks if new lines are identical to original lines
func isNoOp(newLines, origLines []string) bool {
	if len(newLines) != len(origLines) {
		return false
	}
	for i := range newLines {
		if newLines[i] != origLines[i] {
			return false
		}
	}
	return true
}
