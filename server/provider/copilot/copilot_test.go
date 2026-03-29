package copilot

import (
	"cursortab/assert"
	"cursortab/types"
	"testing"
)

func TestApplyCharacterEdit_FullLineReplacement(t *testing.T) {
	p := &Provider{}
	origLines := []string{"hello world"}
	edit := CopilotEdit{
		Text: "hello universe",
		Range: CopilotRange{
			Start: CopilotPos{Line: 0, Character: 0},
			End:   CopilotPos{Line: 0, Character: 11},
		},
	}

	result := p.applyCharacterEdit(origLines, edit)

	assert.Equal(t, "hello universe", result, "full line replacement")
}

func TestApplyCharacterEdit_PartialReplacement(t *testing.T) {
	p := &Provider{}
	origLines := []string{"hello world"}
	edit := CopilotEdit{
		Text: "beautiful",
		Range: CopilotRange{
			Start: CopilotPos{Line: 0, Character: 6},
			End:   CopilotPos{Line: 0, Character: 11},
		},
	}

	result := p.applyCharacterEdit(origLines, edit)

	assert.Equal(t, "hello beautiful", result, "partial replacement")
}

func TestApplyCharacterEdit_Insertion(t *testing.T) {
	p := &Provider{}
	origLines := []string{"helloworld"}
	edit := CopilotEdit{
		Text: " ",
		Range: CopilotRange{
			Start: CopilotPos{Line: 0, Character: 5},
			End:   CopilotPos{Line: 0, Character: 5},
		},
	}

	result := p.applyCharacterEdit(origLines, edit)

	assert.Equal(t, "hello world", result, "insertion")
}

func TestApplyCharacterEdit_MultiLine(t *testing.T) {
	p := &Provider{}
	origLines := []string{"first line", "second line"}
	edit := CopilotEdit{
		Text: "replaced",
		Range: CopilotRange{
			Start: CopilotPos{Line: 0, Character: 6},
			End:   CopilotPos{Line: 1, Character: 6},
		},
	}

	result := p.applyCharacterEdit(origLines, edit)

	assert.Equal(t, "first replaced line", result, "multi-line replacement")
}

func TestApplyCharacterEdit_EmptyOrigLines(t *testing.T) {
	p := &Provider{}
	origLines := []string{}
	edit := CopilotEdit{
		Text: "new content",
		Range: CopilotRange{
			Start: CopilotPos{Line: 0, Character: 0},
			End:   CopilotPos{Line: 0, Character: 0},
		},
	}

	result := p.applyCharacterEdit(origLines, edit)

	assert.Equal(t, "new content", result, "empty orig returns edit text")
}

func TestApplyCharacterEdit_CharacterBeyondLineLength(t *testing.T) {
	p := &Provider{}
	origLines := []string{"short"}
	edit := CopilotEdit{
		Text: " extended",
		Range: CopilotRange{
			Start: CopilotPos{Line: 0, Character: 100}, // Beyond line length
			End:   CopilotPos{Line: 0, Character: 100},
		},
	}

	result := p.applyCharacterEdit(origLines, edit)

	assert.Equal(t, "short extended", result, "character clamped to line length")
}

func TestApplyCharacterEdit_PrefixHeuristic(t *testing.T) {
	p := &Provider{}
	origLines := []string{"func main() {"}
	edit := CopilotEdit{
		Text: "func main() {\n\tfmt.Println(\"hello\")\n}",
		Range: CopilotRange{
			Start: CopilotPos{Line: 0, Character: 0},
			End:   CopilotPos{Line: 0, Character: 13}, // Covers "func main() {"
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
	edit := CopilotEdit{
		Text: "replacement",
		Range: CopilotRange{
			Start: CopilotPos{Line: 0, Character: 0},
			End:   CopilotPos{Line: 1, Character: 10},
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
	edit := CopilotEdit{
		Text: "there",
		Range: CopilotRange{
			Start: CopilotPos{Line: 0, Character: 0},
			End:   CopilotPos{Line: 0, Character: 5}, // "hello" is 5 UTF-16 units
		},
	}

	result := p.applyCharacterEdit(origLines, edit)

	assert.Equal(t, "there 😀 world", result, "UTF-16 offset handled correctly")
}

func TestApplyCharacterEdit_UTF16_AfterEmoji(t *testing.T) {
	p := &Provider{}
	// 😀 is U+1F600, takes 2 UTF-16 code units (4 bytes in UTF-8)
	origLines := []string{"a😀b"}
	edit := CopilotEdit{
		Text: "X",
		Range: CopilotRange{
			Start: CopilotPos{Line: 0, Character: 3}, // After 'a' (1) + 😀 (2) = position 3
			End:   CopilotPos{Line: 0, Character: 4}, // Replace 'b'
		},
	}

	result := p.applyCharacterEdit(origLines, edit)

	assert.Equal(t, "a😀X", result, "position after emoji calculated correctly")
}

func TestApplyCharacterEdit_UTF16_CJK(t *testing.T) {
	p := &Provider{}
	// CJK characters are in BMP, so 1 UTF-16 unit each (but 3 bytes in UTF-8)
	origLines := []string{"你好世界"}
	edit := CopilotEdit{
		Text: "X",
		Range: CopilotRange{
			Start: CopilotPos{Line: 0, Character: 2}, // After "你好"
			End:   CopilotPos{Line: 0, Character: 3}, // Replace "世"
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
		pendingResult: make(chan *CopilotResult, 1),
	}
	req := &types.CompletionRequest{
		Lines: []string{"test"},
	}

	resp, err := p.convertEdits([]CopilotEdit{}, req)

	assert.NoError(t, err, "no error")
	assert.Nil(t, resp.Completions, "no completions for empty edits")
}

func TestConvertEdits_SingleLineEdit(t *testing.T) {
	p := &Provider{
		pendingResult: make(chan *CopilotResult, 1),
	}
	req := &types.CompletionRequest{
		Lines:   []string{"hello"},
		Version: 1,
	}
	edits := []CopilotEdit{{
		Text: "hello world",
		Range: CopilotRange{
			Start: CopilotPos{Line: 0, Character: 0},
			End:   CopilotPos{Line: 0, Character: 5},
		},
		TextDoc: CopilotDoc{Version: 1},
	}}

	resp, err := p.convertEdits(edits, req)

	assert.NoError(t, err, "no error")
	assert.Len(t, 1, resp.Completions, "one completion")
	assert.Equal(t, 1, resp.Completions[0].StartLine, "start line")
	assert.Equal(t, 1, resp.Completions[0].EndLineInc, "end line")
	assert.Len(t, 1, resp.Completions[0].Lines, "one line")
	assert.Equal(t, "hello world", resp.Completions[0].Lines[0], "content")
}

func TestConvertEdits_MultiLineEdit(t *testing.T) {
	p := &Provider{
		pendingResult: make(chan *CopilotResult, 1),
	}
	req := &types.CompletionRequest{
		Lines:   []string{"line 1", "line 2"},
		Version: 1,
	}
	edits := []CopilotEdit{{
		Text: "modified 1\nmodified 2\nmodified 3",
		Range: CopilotRange{
			Start: CopilotPos{Line: 0, Character: 0},
			End:   CopilotPos{Line: 1, Character: 6},
		},
		TextDoc: CopilotDoc{Version: 1},
	}}

	resp, err := p.convertEdits(edits, req)

	assert.NoError(t, err, "no error")
	assert.Len(t, 1, resp.Completions, "one completion")
	assert.Equal(t, 3, len(resp.Completions[0].Lines), "three lines")
}

func TestConvertEdits_NoOpEdit(t *testing.T) {
	p := &Provider{
		pendingResult: make(chan *CopilotResult, 1),
	}
	req := &types.CompletionRequest{
		Lines:   []string{"hello"},
		Version: 1,
	}
	edits := []CopilotEdit{{
		Text: "hello", // Same content
		Range: CopilotRange{
			Start: CopilotPos{Line: 0, Character: 0},
			End:   CopilotPos{Line: 0, Character: 5},
		},
		TextDoc: CopilotDoc{Version: 1},
	}}

	resp, err := p.convertEdits(edits, req)

	assert.NoError(t, err, "no error")
	assert.Nil(t, resp.Completions, "no completions for no-op")
}

func TestConvertEdits_StartLineOutOfBounds(t *testing.T) {
	p := &Provider{
		pendingResult: make(chan *CopilotResult, 1),
	}
	req := &types.CompletionRequest{
		Lines:   []string{"hello"},
		Version: 1,
	}
	edits := []CopilotEdit{{
		Text: "new",
		Range: CopilotRange{
			Start: CopilotPos{Line: 100, Character: 0}, // Way out of bounds
			End:   CopilotPos{Line: 100, Character: 0},
		},
		TextDoc: CopilotDoc{Version: 1},
	}}

	resp, err := p.convertEdits(edits, req)

	assert.NoError(t, err, "no error")
	assert.Nil(t, resp.Completions, "no completions for out of bounds")
}

func TestConvertEdits_MultipleEdits(t *testing.T) {
	p := &Provider{
		pendingResult: make(chan *CopilotResult, 1),
	}
	req := &types.CompletionRequest{
		Lines:   []string{"line 1", "line 2", "line 3"},
		Version: 1,
	}
	edits := []CopilotEdit{
		{
			Text: "modified 1",
			Range: CopilotRange{
				Start: CopilotPos{Line: 0, Character: 0},
				End:   CopilotPos{Line: 0, Character: 6},
			},
			TextDoc: CopilotDoc{Version: 1},
		},
		{
			Text: "modified 3",
			Range: CopilotRange{
				Start: CopilotPos{Line: 2, Character: 0},
				End:   CopilotPos{Line: 2, Character: 6},
			},
			TextDoc: CopilotDoc{Version: 1},
		},
	}

	resp, err := p.convertEdits(edits, req)

	assert.NoError(t, err, "no error")
	assert.Len(t, 2, resp.Completions, "two completions")
	assert.Equal(t, 1, resp.Completions[0].StartLine, "first edit start line")
	assert.Equal(t, 3, resp.Completions[1].StartLine, "second edit start line")
}

func TestHandleNESResponse_ValidResponse(t *testing.T) {
	p := &Provider{
		pendingResult: make(chan *CopilotResult, 1),
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
		pendingResult: make(chan *CopilotResult, 1),
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
		pendingResult: make(chan *CopilotResult, 1),
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
		pendingResult: make(chan *CopilotResult, 1),
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
	assert.Nil(t, resp.Completions, "no completions")
	assert.Nil(t, resp.CursorTarget, "no cursor target")
}
