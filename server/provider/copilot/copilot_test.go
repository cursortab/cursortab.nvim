package copilot

import (
	"context"
	"errors"
	"testing"

	"cursortab/assert"
	"cursortab/buffer"
	"cursortab/ctx"
)

type mockLSPBuffer struct {
	client      *buffer.CopilotClientInfo
	clientErr   error
	didFocusErr error

	didFocusURIs []string
	nesRequests  []int64
	handler      func(reqID int64, editsJSON string, errMsg string)
}

func (m *mockLSPBuffer) GetCopilotClient() (*buffer.CopilotClientInfo, error) {
	return m.client, m.clientErr
}

func (m *mockLSPBuffer) SendCopilotDidFocus(uri string) error {
	m.didFocusURIs = append(m.didFocusURIs, uri)
	return m.didFocusErr
}

func (m *mockLSPBuffer) SendCopilotNESRequest(reqID int64, _ string) error {
	m.nesRequests = append(m.nesRequests, reqID)
	if m.handler != nil {
		m.handler(reqID, "[]", "")
	}
	return nil
}

func (m *mockLSPBuffer) RegisterCopilotHandler(handler func(reqID int64, editsJSON string, errMsg string)) error {
	m.handler = handler
	return nil
}

func makeCurrent(lines []string) ctx.CurrentSnapshot {
	return ctx.CurrentSnapshot{
		File: ctx.FileSnapshot{
			Lines: lines,
		},
	}
}

func TestApplyCharacterEdit_FullLineReplacement(t *testing.T) {
	p := &Provider{}
	origLines := []string{"hello world"}
	edit := copilotEdit{
		Text: "hello universe",
		Range: copilotRange{
			Start: copilotPos{Line: 0, Character: 0},
			End:   copilotPos{Line: 0, Character: 11},
		},
	}

	result := p.applyCharacterEdit(origLines, edit)

	assert.Equal(t, "hello universe", result, "full line replacement")
}

func TestApplyCharacterEdit_PartialReplacement(t *testing.T) {
	p := &Provider{}
	origLines := []string{"hello world"}
	edit := copilotEdit{
		Text: "beautiful",
		Range: copilotRange{
			Start: copilotPos{Line: 0, Character: 6},
			End:   copilotPos{Line: 0, Character: 11},
		},
	}

	result := p.applyCharacterEdit(origLines, edit)

	assert.Equal(t, "hello beautiful", result, "partial replacement")
}

func TestApplyCharacterEdit_Insertion(t *testing.T) {
	p := &Provider{}
	origLines := []string{"helloworld"}
	edit := copilotEdit{
		Text: " ",
		Range: copilotRange{
			Start: copilotPos{Line: 0, Character: 5},
			End:   copilotPos{Line: 0, Character: 5},
		},
	}

	result := p.applyCharacterEdit(origLines, edit)

	assert.Equal(t, "hello world", result, "insertion")
}

func TestApplyCharacterEdit_MultiLine(t *testing.T) {
	p := &Provider{}
	origLines := []string{"first line", "second line"}
	edit := copilotEdit{
		Text: "replaced",
		Range: copilotRange{
			Start: copilotPos{Line: 0, Character: 6},
			End:   copilotPos{Line: 1, Character: 6},
		},
	}

	result := p.applyCharacterEdit(origLines, edit)

	assert.Equal(t, "first replaced line", result, "multi-line replacement")
}

func TestApplyCharacterEdit_EmptyOrigLines(t *testing.T) {
	p := &Provider{}
	origLines := []string{}
	edit := copilotEdit{
		Text: "new content",
		Range: copilotRange{
			Start: copilotPos{Line: 0, Character: 0},
			End:   copilotPos{Line: 0, Character: 0},
		},
	}

	result := p.applyCharacterEdit(origLines, edit)

	assert.Equal(t, "new content", result, "empty orig returns edit text")
}

func TestApplyCharacterEdit_CharacterBeyondLineLength(t *testing.T) {
	p := &Provider{}
	origLines := []string{"short"}
	edit := copilotEdit{
		Text: " extended",
		Range: copilotRange{
			Start: copilotPos{Line: 0, Character: 100}, // Beyond line length
			End:   copilotPos{Line: 0, Character: 100},
		},
	}

	result := p.applyCharacterEdit(origLines, edit)

	assert.Equal(t, "short extended", result, "character clamped to line length")
}

func TestApplyCharacterEdit_PrefixHeuristic(t *testing.T) {
	p := &Provider{}
	origLines := []string{"func main() {"}
	edit := copilotEdit{
		Text: "func main() {\n\tfmt.Println(\"hello\")\n}",
		Range: copilotRange{
			Start: copilotPos{Line: 0, Character: 0},
			End:   copilotPos{Line: 0, Character: 13}, // Covers "func main() {"
		},
	}

	result := p.applyCharacterEdit(origLines, edit)

	// The heuristic should detect that edit.Text starts with the replaced content
	// and avoid appending the suffix
	assert.Equal(t, "func main() {\n\tfmt.Println(\"hello\")\n}", result, "prefix heuristic applied")
}

func TestApplyCharacterEdit_MultiLineWithPartialEnd(t *testing.T) {
	p := &Provider{}
	origLines := []string{"short", "much longer line here"}
	edit := copilotEdit{
		Text: "replacement",
		Range: copilotRange{
			Start: copilotPos{Line: 0, Character: 0},
			End:   copilotPos{Line: 1, Character: 10},
		},
	}

	result := p.applyCharacterEdit(origLines, edit)

	// Should preserve suffix from last line: "r line here"
	assert.Equal(t, "replacementr line here", result, "multi-line with partial end")
}

func TestApplyCharacterEdit_UTF16_Emoji(t *testing.T) {
	p := &Provider{}
	// 😀 is U+1F600, which is outside BMP and takes 2 UTF-16 code units
	origLines := []string{"hello 😀 world"}
	edit := copilotEdit{
		Text: "there",
		Range: copilotRange{
			Start: copilotPos{Line: 0, Character: 0},
			End:   copilotPos{Line: 0, Character: 5}, // "hello" is 5 UTF-16 units
		},
	}

	result := p.applyCharacterEdit(origLines, edit)

	assert.Equal(t, "there 😀 world", result, "UTF-16 offset handled correctly")
}

func TestApplyCharacterEdit_UTF16_AfterEmoji(t *testing.T) {
	p := &Provider{}
	// 😀 is U+1F600, takes 2 UTF-16 code units (4 bytes in UTF-8)
	origLines := []string{"a😀b"}
	edit := copilotEdit{
		Text: "X",
		Range: copilotRange{
			Start: copilotPos{Line: 0, Character: 3}, // After 'a' (1) + 😀 (2) = position 3
			End:   copilotPos{Line: 0, Character: 4}, // Replace 'b'
		},
	}

	result := p.applyCharacterEdit(origLines, edit)

	assert.Equal(t, "a😀X", result, "position after emoji calculated correctly")
}

func TestApplyCharacterEdit_UTF16_CJK(t *testing.T) {
	p := &Provider{}
	// CJK characters are in BMP, so 1 UTF-16 unit each (but 3 bytes in UTF-8)
	origLines := []string{"你好世界"}
	edit := copilotEdit{
		Text: "X",
		Range: copilotRange{
			Start: copilotPos{Line: 0, Character: 2}, // After "你好"
			End:   copilotPos{Line: 0, Character: 3}, // Replace "世"
		},
	}

	result := p.applyCharacterEdit(origLines, edit)

	assert.Equal(t, "你好X界", result, "CJK UTF-16 offset handled correctly")
}

func TestUtf16OffsetToBytes(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		utf16Offset int
		expected    int
	}{
		{"empty string", "", 0, 0},
		{"ascii only", "hello", 3, 3},
		{"ascii beyond length", "hi", 10, 2},
		{"emoji at start", "😀hello", 2, 4}, // emoji is 2 UTF-16 units, 4 bytes
		{"after emoji", "a😀b", 3, 5},       // 'a'(1) + 😀(4 bytes) = 5
		{"CJK characters", "你好", 1, 3},     // each CJK is 1 UTF-16 unit but 3 bytes
		{"mixed content", "a😀你b", 4, 8},    // a(1) + 😀(4) + 你(3) = 8 bytes at UTF-16 pos 4
		{"zero offset", "anything", 0, 0},
		{"negative offset", "test", -1, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := utf16OffsetToBytes(tt.input, tt.utf16Offset)
			assert.Equal(t, tt.expected, result, tt.name)
		})
	}
}

func TestConvertEdits_EmptyEdits(t *testing.T) {
	p := &Provider{
		pendingResult: make(chan *copilotResult, 1),
	}
	current := makeCurrent([]string{"test"})

	resp, err := p.convertEdits([]copilotEdit{}, current)

	assert.NoError(t, err, "no error")
	assert.Nil(t, resp.Completion, "no completions for empty edits")
}

func TestConvertEdits_SingleLineEdit(t *testing.T) {
	p := &Provider{
		pendingResult: make(chan *copilotResult, 1),
	}
	current := makeCurrent([]string{"hello"})
	edits := []copilotEdit{{
		Text: "hello world",
		Range: copilotRange{
			Start: copilotPos{Line: 0, Character: 0},
			End:   copilotPos{Line: 0, Character: 5},
		},
		TextDoc: copilotDoc{Version: 1},
	}}

	resp, err := p.convertEdits(edits, current)

	assert.NoError(t, err, "no error")
	assert.NotNil(t, resp.Completion, "one completion")
	assert.Equal(t, 1, resp.Completion.StartLine, "start line")
	assert.Equal(t, 1, resp.Completion.EndLineInc, "end line")
	assert.Len(t, 1, resp.Completion.Lines, "one line")
	assert.Equal(t, "hello world", resp.Completion.Lines[0], "content")
}

func TestConvertEdits_MultiLineEdit(t *testing.T) {
	p := &Provider{
		pendingResult: make(chan *copilotResult, 1),
	}
	current := makeCurrent([]string{"line 1", "line 2"})
	edits := []copilotEdit{{
		Text: "modified 1\nmodified 2\nmodified 3",
		Range: copilotRange{
			Start: copilotPos{Line: 0, Character: 0},
			End:   copilotPos{Line: 1, Character: 6},
		},
		TextDoc: copilotDoc{Version: 1},
	}}

	resp, err := p.convertEdits(edits, current)

	assert.NoError(t, err, "no error")
	assert.NotNil(t, resp.Completion, "one completion")
	assert.Equal(t, 3, len(resp.Completion.Lines), "three lines")
}

func TestConvertEdits_NoOpEdit(t *testing.T) {
	p := &Provider{
		pendingResult: make(chan *copilotResult, 1),
	}
	current := makeCurrent([]string{"hello"})
	edits := []copilotEdit{{
		Text: "hello", // Same content
		Range: copilotRange{
			Start: copilotPos{Line: 0, Character: 0},
			End:   copilotPos{Line: 0, Character: 5},
		},
		TextDoc: copilotDoc{Version: 1},
	}}

	resp, err := p.convertEdits(edits, current)

	assert.NoError(t, err, "no error")
	assert.Nil(t, resp.Completion, "no completions for no-op")
}

func TestConvertEdits_StartLineOutOfBounds(t *testing.T) {
	p := &Provider{
		pendingResult: make(chan *copilotResult, 1),
	}
	current := makeCurrent([]string{"hello"})
	edits := []copilotEdit{{
		Text: "new",
		Range: copilotRange{
			Start: copilotPos{Line: 100, Character: 0}, // Way out of bounds
			End:   copilotPos{Line: 100, Character: 0},
		},
		TextDoc: copilotDoc{Version: 1},
	}}

	resp, err := p.convertEdits(edits, current)

	assert.NoError(t, err, "no error")
	assert.Nil(t, resp.Completion, "no completions for out of bounds")
}

func TestConvertEdits_MultipleEdits(t *testing.T) {
	p := &Provider{
		pendingResult: make(chan *copilotResult, 1),
	}
	current := makeCurrent([]string{"line 1", "line 2", "line 3"})
	edits := []copilotEdit{
		{
			Text: "modified 1",
			Range: copilotRange{
				Start: copilotPos{Line: 0, Character: 0},
				End:   copilotPos{Line: 0, Character: 6},
			},
			TextDoc: copilotDoc{Version: 1},
		},
		{
			Text: "modified 3",
			Range: copilotRange{
				Start: copilotPos{Line: 2, Character: 0},
				End:   copilotPos{Line: 2, Character: 6},
			},
			TextDoc: copilotDoc{Version: 1},
		},
	}

	resp, err := p.convertEdits(edits, current)

	assert.NoError(t, err, "no error")
	assert.NotNil(t, resp.Completion, "one candidate")
	assert.Equal(t, 1, resp.Completion.StartLine, "start line")
	assert.Equal(t, 3, resp.Completion.EndLineInc, "end line")
	assert.Equal(t, []string{"modified 1", "line 2", "modified 3"}, resp.Completion.Lines, "merged lines")
}

func TestConvertEdits_InsertsAtBufferEnd(t *testing.T) {
	tests := []struct {
		name       string
		lines      []string
		editText   string
		rangeLine  int
		startLine  int
		endLineInc int
		wantLines  []string
	}{
		{
			name:       "after existing content",
			lines:      []string{"line 1"},
			editText:   "line 2",
			rangeLine:  1,
			startLine:  2,
			endLineInc: 2,
			wantLines:  []string{"line 2"},
		},
		{
			name:       "empty file",
			editText:   "package main",
			startLine:  1,
			endLineInc: 1,
			wantLines:  []string{"package main"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Provider{pendingResult: make(chan *copilotResult, 1)}
			current := makeCurrent(tt.lines)
			edits := []copilotEdit{{
				Text: tt.editText,
				Range: copilotRange{
					Start: copilotPos{Line: tt.rangeLine, Character: 0},
					End:   copilotPos{Line: tt.rangeLine, Character: 0},
				},
				TextDoc: copilotDoc{Version: 1},
			}}

			resp, err := p.convertEdits(edits, current)

			assert.NoError(t, err, "no error")
			assert.NotNil(t, resp.Completion, "one candidate")
			assert.Equal(t, tt.startLine, resp.Completion.StartLine, "start line")
			assert.Equal(t, tt.endLineInc, resp.Completion.EndLineInc, "end line")
			assert.Equal(t, tt.wantLines, resp.Completion.Lines, "inserted lines")
		})
	}
}

func TestCall_DidFocusErrorReturnsError(t *testing.T) {
	buf := &mockLSPBuffer{
		client:      &buffer.CopilotClientInfo{ID: 1},
		didFocusErr: errors.New("focus failed"),
	}
	p := NewProvider(buf)

	_, err := p.Call(context.Background(), copilotRequest{uri: "file:///x.go"})

	assert.Error(t, err, "didFocus failure")
	assert.Contains(t, err.Error(), "didFocus", "error names didFocus")
	assert.Equal(t, 0, len(buf.nesRequests), "NES request not sent")
	assert.Equal(t, "", p.lastFocusedURI, "focus cache unchanged")
}

func TestCall_DidFocusOnlySentForNewURI(t *testing.T) {
	buf := &mockLSPBuffer{
		client: &buffer.CopilotClientInfo{ID: 1},
	}
	p := NewProvider(buf)
	req := copilotRequest{uri: "file:///x.go"}

	_, err := p.Call(context.Background(), req)
	assert.NoError(t, err, "first call")

	_, err = p.Call(context.Background(), req)
	assert.NoError(t, err, "second call")

	assert.Equal(t, []string{"file:///x.go"}, buf.didFocusURIs, "focus sent once")
	assert.Equal(t, 2, len(buf.nesRequests), "NES request sent for each call")
	assert.Equal(t, "file:///x.go", p.lastFocusedURI, "focus cache updated")
}

func TestHandleNESResponse_ValidResponse(t *testing.T) {
	p := &Provider{
		pendingResult: make(chan *copilotResult, 1),
		pendingReqID:  1,
	}

	editsJSON := `[{"text":"hello world","range":{"start":{"line":0,"character":0},"end":{"line":0,"character":5}}}]`
	p.HandleNESResponse(1, editsJSON, "")

	select {
	case result := <-p.pendingResult:
		assert.NoError(t, result.Error, "no error")
		assert.Len(t, 1, result.Edits, "one edit")
		assert.Equal(t, "hello world", result.Edits[0].Text, "edit text")
	default:
		t.Fatal("expected result on channel")
	}
}

func TestHandleNESResponse_ErrorResponse(t *testing.T) {
	p := &Provider{
		pendingResult: make(chan *copilotResult, 1),
		pendingReqID:  1,
	}

	p.HandleNESResponse(1, "[]", "some error occurred")

	select {
	case result := <-p.pendingResult:
		assert.Error(t, result.Error, "should have error")
		assert.Contains(t, result.Error.Error(), "some error occurred", "error message")
	default:
		t.Fatal("expected result on channel")
	}
}

func TestHandleNESResponse_StaleResponse(t *testing.T) {
	p := &Provider{
		pendingResult: make(chan *copilotResult, 1),
		pendingReqID:  5, // Current pending is 5
	}

	// Send response for old request ID 3
	p.HandleNESResponse(3, `[{"text":"stale"}]`, "")

	// Channel should be empty (stale response ignored)
	select {
	case <-p.pendingResult:
		t.Fatal("stale response should be ignored")
	default:
		// Expected
	}
}

func TestHandleNESResponse_InvalidJSON(t *testing.T) {
	p := &Provider{
		pendingResult: make(chan *copilotResult, 1),
		pendingReqID:  1,
	}

	p.HandleNESResponse(1, "invalid json", "")

	select {
	case result := <-p.pendingResult:
		assert.Error(t, result.Error, "should have parse error")
		assert.Contains(t, result.Error.Error(), "failed to parse", "error message")
	default:
		t.Fatal("expected result on channel")
	}
}

func TestEmptyResponse(t *testing.T) {
	p := &Provider{}

	resp := p.emptyResponse()

	assert.NotNil(t, resp, "response not nil")
	assert.Nil(t, resp.Completion, "no completions")
	assert.Nil(t, resp.CursorTarget, "no cursor target")
}
