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
	"cursortab/ctx"
	"cursortab/engine"
	"cursortab/logger"
	"cursortab/provider"
	"cursortab/types"
)

type copilotEdit struct {
	Text    string       `json:"text"`
	Range   copilotRange `json:"range"`
	Command *copilotCmd  `json:"command,omitempty"`
	TextDoc copilotDoc   `json:"textDocument"`
}

type copilotRange struct {
	Start copilotPos `json:"start"`
	End   copilotPos `json:"end"`
}

type copilotPos struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type copilotCmd struct {
	Command   string `json:"command"`
	Arguments []any  `json:"arguments"`
}

type copilotDoc struct {
	URI     string `json:"uri"`
	Version int    `json:"version"`
}

type copilotResult struct {
	Edits []copilotEdit
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

type Provider struct {
	provider.Base
	buffer LSPBuffer

	mu            sync.Mutex
	reqIDCounter  int64
	pendingReqID  int64
	pendingResult chan *copilotResult

	lastFocusedURI string

	handlerRegistered bool
	lastClientID      int
}

var _ engine.Provider = (*Provider)(nil)
var _ provider.CompletionFlow[copilotRequest, []copilotEdit] = (*Provider)(nil)

type copilotRequest struct {
	uri       string
	cursorRow int
	cursorCol int
}

func NewProvider(buf LSPBuffer) *Provider {
	return &Provider{
		Base:          provider.NewBase(engine.CompletionEdit, nil, provider.SyntheticPrefetchDisabled),
		buffer:        buf,
		pendingResult: make(chan *copilotResult, 1),
	}
}

func (p *Provider) Complete(reqCtx context.Context, input ctx.CompletionInput) (*types.CompletionResponse, error) {
	return provider.StartBatch(reqCtx, input, nil, p)
}

func (p *Provider) Build(state *provider.RequestState) (copilotRequest, error) {
	current := state.Input.Current
	return copilotRequest{
		uri:       buildDocumentURI(current),
		cursorRow: current.Cursor.Row,
		cursorCol: current.Cursor.Col,
	}, nil
}

func (p *Provider) Call(reqCtx context.Context, req copilotRequest) ([]copilotEdit, error) {
	clientInfo, err := p.buffer.GetCopilotClient()
	if err != nil {
		return nil, fmt.Errorf("check copilot client: %w", err)
	}
	if clientInfo == nil {
		logger.Debug("copilot: no client attached")
		return nil, nil
	}

	if err := p.ensureHandlerRegistered(clientInfo.ID); err != nil {
		return nil, fmt.Errorf("register copilot handler: %w", err)
	}

	reqID := atomic.AddInt64(&p.reqIDCounter, 1)

	p.mu.Lock()
	if req.uri != p.lastFocusedURI {
		if err := p.buffer.SendCopilotDidFocus(req.uri); err != nil {
			p.mu.Unlock()
			return nil, fmt.Errorf("send copilot didFocus: %w", err)
		}
		p.lastFocusedURI = req.uri
	}

	p.pendingReqID = reqID
	select {
	case <-p.pendingResult:
	default:
	}
	p.mu.Unlock()

	logger.Debug("copilot request:\n  ReqID: %d\n  URI: %s\n  CursorRow: %d\n  CursorCol: %d",
		reqID, req.uri, req.cursorRow, req.cursorCol)
	if err := p.buffer.SendCopilotNESRequest(reqID, req.uri); err != nil {
		return nil, fmt.Errorf("send copilot NES request: %w", err)
	}

	// Wait for response with context timeout
	select {
	case <-reqCtx.Done():
		logger.Debug("copilot: request cancelled")
		return nil, reqCtx.Err()
	case result := <-p.pendingResult:
		if result.Error != nil {
			return nil, result.Error
		}

		p.logResponse(result.Edits)
		return result.Edits, nil
	}
}

func (p *Provider) Parse(state *provider.RequestState, edits []copilotEdit) (*types.CompletionResponse, error) {
	return p.convertEdits(edits, state.Input.Current)
}

func buildDocumentURI(current ctx.CurrentSnapshot) string {
	filePath := current.File.Path
	if strings.HasPrefix(filePath, "/") {
		return "file://" + filePath
	}
	return "file://" + current.WorkspacePath + "/" + filePath
}

// HandleNESResponse is called by the RPC handler when Copilot responds
func (p *Provider) HandleNESResponse(reqID int64, editsJSON string, errMsg string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if reqID != p.pendingReqID {
		logger.Debug("copilot: ignoring stale response reqID=%d (pending=%d)", reqID, p.pendingReqID)
		return
	}

	result := &copilotResult{}

	if errMsg != "" {
		result.Error = fmt.Errorf("copilot error: %s", errMsg)
	} else {
		var edits []copilotEdit
		if err := json.Unmarshal([]byte(editsJSON), &edits); err != nil {
			result.Error = fmt.Errorf("failed to parse edits: %w", err)
		} else {
			result.Edits = edits
		}
	}

	// Non-blocking send to avoid deadlock if no one is waiting
	// Safe to send while holding mutex since Complete releases lock before receiving
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

func (p *Provider) logResponse(edits []copilotEdit) {
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

func (p *Provider) convertEdits(edits []copilotEdit, current ctx.CurrentSnapshot) (*types.CompletionResponse, error) {
	if len(edits) == 0 {
		return p.emptyResponse(), nil
	}

	var editsToApply []*types.Completion

	for i, edit := range edits {
		// Store command for telemetry
		if edit.Command != nil {
			logger.Debug("copilot: edit %d has command: %s", i, edit.Command.Command)
		}

		completion := p.convertSingleEdit(edit, current, i)
		if completion != nil {
			editsToApply = append(editsToApply, completion)
		}
	}

	completion := mergeCompletionEdits(current.File.Lines, editsToApply)
	if completion == nil {
		return p.emptyResponse(), nil
	}

	logger.Debug("copilot: converted %d edits to one completion", len(edits))

	return &types.CompletionResponse{
		Completion: completion,
	}, nil
}

func mergeCompletionEdits(original []string, edits []*types.Completion) *types.Completion {
	if len(edits) == 0 {
		return nil
	}

	ordered := slices.Clone(edits)
	slices.SortFunc(ordered, func(a, b *types.Completion) int {
		return b.StartLine - a.StartLine
	})

	updated := slices.Clone(original)
	nextStart := len(original) + 2
	for _, edit := range ordered {
		if edit == nil || edit.StartLine < 1 || edit.EndLineInc < edit.StartLine || edit.EndLineInc >= nextStart {
			continue
		}
		start := edit.StartLine - 1
		end := edit.EndLineInc
		if start > len(updated) {
			continue
		}
		if end > len(updated) {
			end = len(updated)
		}
		merged := make([]string, 0, len(updated)-end+start+len(edit.Lines))
		merged = append(merged, updated[:start]...)
		merged = append(merged, edit.Lines...)
		merged = append(merged, updated[end:]...)
		updated = merged
		nextStart = edit.StartLine
	}

	start := 0
	for start < len(original) && start < len(updated) && original[start] == updated[start] {
		start++
	}
	if start == len(original) && start == len(updated) {
		return nil
	}

	oldEnd := len(original) - 1
	newEnd := len(updated) - 1
	for oldEnd >= start && newEnd >= start && original[oldEnd] == updated[newEnd] {
		oldEnd--
		newEnd--
	}

	startLine := start + 1
	endLineInc := oldEnd + 1
	if endLineInc < startLine {
		endLineInc = startLine
	}

	newLines := slices.Clone(updated[start : newEnd+1])
	return &types.Completion{
		StartLine:  startLine,
		EndLineInc: endLineInc,
		Lines:      newLines,
	}
}

func (p *Provider) convertSingleEdit(edit copilotEdit, current ctx.CurrentSnapshot, editIdx int) *types.Completion {
	lines := current.File.Lines

	startLine := edit.Range.Start.Line + 1
	endLine := edit.Range.End.Line + 1

	if startLine < 1 || startLine > len(lines)+1 {
		logger.Debug("copilot: edit %d start line %d out of bounds", editIdx, startLine)
		return nil
	}

	// Handle case where end line is beyond buffer (insertion at end)
	if endLine > len(lines) {
		endLine = len(lines)
	}
	if endLine < startLine {
		endLine = startLine
	}

	// Get original lines being replaced (0-indexed slice)
	var origLines []string
	if edit.Range.Start.Line < len(lines) {
		endIdx := min(edit.Range.End.Line+1, len(lines))
		origLines = lines[edit.Range.Start.Line:endIdx]
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
func (p *Provider) applyCharacterEdit(origLines []string, edit copilotEdit) string {
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

func (p *Provider) emptyResponse() *types.CompletionResponse { return &types.CompletionResponse{} }
