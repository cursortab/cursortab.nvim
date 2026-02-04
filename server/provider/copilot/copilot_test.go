package copilot

import (
	"context"
	"cursortab/assert"
	"cursortab/types"
	"sync"
	"testing"
)

func TestIsNoOp_IdenticalLines(t *testing.T) {
	newLines := []string{"line 1", "line 2"}
	origLines := []string{"line 1", "line 2"}

	result := isNoOp(newLines, origLines)

	assert.True(t, result, "identical lines should be no-op")
}

func TestIsNoOp_DifferentContent(t *testing.T) {
	newLines := []string{"line 1", "modified"}
	origLines := []string{"line 1", "line 2"}

	result := isNoOp(newLines, origLines)

	assert.False(t, result, "different content should not be no-op")
}

func TestIsNoOp_DifferentLength(t *testing.T) {
	newLines := []string{"line 1", "line 2", "line 3"}
	origLines := []string{"line 1", "line 2"}

	result := isNoOp(newLines, origLines)

	assert.False(t, result, "different length should not be no-op")
}

func TestIsNoOp_Empty(t *testing.T) {
	newLines := []string{}
	origLines := []string{}

	result := isNoOp(newLines, origLines)

	assert.True(t, result, "both empty should be no-op")
}

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

func TestConvertEdits_StaleVersion(t *testing.T) {
	p := &Provider{
		pendingResult: make(chan *CopilotResult, 1),
	}
	req := &types.CompletionRequest{
		Lines:   []string{"hello"},
		Version: 5,
	}
	edits := []CopilotEdit{{
		Text: "hello world",
		Range: CopilotRange{
			Start: CopilotPos{Line: 0, Character: 0},
			End:   CopilotPos{Line: 0, Character: 5},
		},
		TextDoc: CopilotDoc{Version: 3}, // Stale version
	}}

	resp, err := p.convertEdits(edits, req)

	assert.NoError(t, err, "no error")
	assert.Nil(t, resp.Completions, "no completions for stale version")
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

func TestConvertEdits_StoresCommand(t *testing.T) {
	p := &Provider{
		pendingResult: make(chan *CopilotResult, 1),
	}
	req := &types.CompletionRequest{
		Lines:   []string{"hello"},
		Version: 1,
	}
	cmd := &CopilotCmd{
		Command:   "copilot/telemetry",
		Arguments: []any{"arg1"},
	}
	edits := []CopilotEdit{{
		Text: "hello world",
		Range: CopilotRange{
			Start: CopilotPos{Line: 0, Character: 0},
			End:   CopilotPos{Line: 0, Character: 5},
		},
		TextDoc: CopilotDoc{Version: 1},
		Command: cmd,
	}}

	p.convertEdits(edits, req)

	assert.NotNil(t, p.lastCommand, "command stored")
	assert.Equal(t, "copilot/telemetry", p.lastCommand.Command, "command name")
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

func TestAcceptCompletion_NoCommand(t *testing.T) {
	p := &Provider{
		lastCommand: nil,
	}

	// Should not panic
	p.AcceptCompletion(context.Background())

	assert.Nil(t, p.lastCommand, "still nil")
}

func TestLastCommand_ConcurrentAccess(t *testing.T) {
	p := &Provider{
		pendingResult: make(chan *CopilotResult, 1),
	}

	var wg sync.WaitGroup
	iterations := 100

	// Test that concurrent read/write of lastCommand is safe with mutex
	for i := 0; i < iterations; i++ {
		wg.Add(2)

		// Writer goroutine (simulates convertEdits setting lastCommand)
		go func() {
			defer wg.Done()
			p.mu.Lock()
			p.lastCommand = &CopilotCmd{Command: "test"}
			p.mu.Unlock()
		}()

		// Reader goroutine (simulates AcceptCompletion reading/clearing)
		go func() {
			defer wg.Done()
			p.mu.Lock()
			cmd := p.lastCommand
			p.lastCommand = nil
			p.mu.Unlock()
			// Just access cmd to prevent "unused" warning
			_ = cmd
		}()
	}

	wg.Wait()
	// If there's a race, the race detector will catch it
}

func TestEmptyResponse(t *testing.T) {
	p := &Provider{}

	resp := p.emptyResponse()

	assert.NotNil(t, resp, "response not nil")
	assert.Nil(t, resp.Completions, "no completions")
	assert.Nil(t, resp.CursorTarget, "no cursor target")
}
