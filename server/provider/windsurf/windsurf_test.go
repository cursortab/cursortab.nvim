package windsurf

import (
	"testing"

	"cursortab/assert"
	"cursortab/types"
)

func makeRequest(lines []string, cursorRow, cursorCol int) *types.CompletionRequest {
	return &types.CompletionRequest{
		Lines:         lines,
		CursorRow:     cursorRow,
		CursorCol:     cursorCol,
		FilePath:      "/test/main.go",
		WorkspacePath: "/test",
	}
}

func makeRange(startRow, endRow, endCol string) windsurfResponseRange {
	return windsurfResponseRange{
		StartPosition: struct {
			Row string `json:"row"`
		}{Row: startRow},
		EndPosition: struct {
			Row string `json:"row"`
			Col string `json:"col"`
		}{Row: endRow, Col: endCol},
	}
}

func TestConvertResponse_EmptyItems(t *testing.T) {
	p := &Provider{}
	wsResp := &windsurfResponse{
		State:           windsurfState{State: "CODEIUM_STATE_SUCCESS"},
		CompletionItems: []windsurfCompletionItem{},
	}
	req := makeRequest([]string{"line1", "line2"}, 1, 0)

	resp, err := p.convertResponse(wsResp, req)
	assert.NoError(t, err, "convertResponse error")
	assert.Len(t, 0, resp.Completions, "completions")
}

func TestConvertResponse_EmptyState(t *testing.T) {
	p := &Provider{}
	wsResp := &windsurfResponse{
		State: windsurfState{State: ""},
	}
	req := makeRequest([]string{"line1"}, 1, 0)

	resp, err := p.convertResponse(wsResp, req)
	assert.NoError(t, err, "convertResponse error")
	assert.Len(t, 0, resp.Completions, "completions")
}

func TestConvertSingleItem_SingleLineReplacement(t *testing.T) {
	p := &Provider{}
	item := windsurfCompletionItem{
		Completion: windsurfCompletion{
			CompletionID: "abc123",
			Text:         "hello world",
		},
		Range: makeRange("0", "0", "5"),
	}
	req := makeRequest([]string{"hello"}, 1, 5)

	comp := p.convertSingleItem(item, req, 0)
	assert.NotNil(t, comp, "completion")
	assert.Equal(t, 1, comp.StartLine, "startLine")
	assert.Equal(t, 1, comp.EndLineInc, "endLineInc")
	assert.Equal(t, []string{"hello world"}, comp.Lines, "lines")
}

func TestConvertSingleItem_SingleLineWithSuffix(t *testing.T) {
	p := &Provider{}
	item := windsurfCompletionItem{
		Completion: windsurfCompletion{
			CompletionID: "abc123",
			Text:         "goodbye",
		},
		Range: makeRange("0", "0", "5"),
	}
	req := makeRequest([]string{"hello world"}, 1, 5)

	comp := p.convertSingleItem(item, req, 0)
	assert.NotNil(t, comp, "completion")
	assert.Equal(t, []string{"goodbye world"}, comp.Lines, "lines with suffix preserved")
}

func TestConvertSingleItem_MultiLineReplacement(t *testing.T) {
	p := &Provider{}
	item := windsurfCompletionItem{
		Completion: windsurfCompletion{
			CompletionID: "abc123",
			Text:         "new line 1\nnew line 2\nnew line 3",
		},
		Range: makeRange("1", "3", "5"),
	}
	req := makeRequest([]string{"line0", "old1", "old2", "old3", "line4"}, 2, 0)

	comp := p.convertSingleItem(item, req, 0)
	assert.NotNil(t, comp, "completion")
	assert.Equal(t, 2, comp.StartLine, "startLine")
	assert.Equal(t, 4, comp.EndLineInc, "endLineInc")
	assert.Equal(t, 3, len(comp.Lines), "num lines")
}

func TestConvertSingleItem_NoOp(t *testing.T) {
	p := &Provider{}
	item := windsurfCompletionItem{
		Completion: windsurfCompletion{
			CompletionID: "abc123",
			Text:         "hello",
		},
		Range: makeRange("0", "0", "5"),
	}
	req := makeRequest([]string{"hello"}, 1, 5)

	comp := p.convertSingleItem(item, req, 0)
	assert.Nil(t, comp, "completion should be nil for no-op")
}

func TestConvertSingleItem_StartLineOutOfBounds(t *testing.T) {
	p := &Provider{}
	item := windsurfCompletionItem{
		Completion: windsurfCompletion{
			CompletionID: "abc123",
			Text:         "something",
		},
		Range: makeRange("99", "99", "0"),
	}
	req := makeRequest([]string{"line1", "line2"}, 1, 0)

	comp := p.convertSingleItem(item, req, 0)
	assert.Nil(t, comp, "completion should be nil for out of bounds")
}

func TestConvertSingleItem_EmptyBuffer(t *testing.T) {
	p := &Provider{}
	item := windsurfCompletionItem{
		Completion: windsurfCompletion{
			CompletionID: "abc123",
			Text:         "package main",
		},
		Range: makeRange("0", "0", "0"),
	}
	req := makeRequest([]string{""}, 1, 0)

	comp := p.convertSingleItem(item, req, 0)
	assert.NotNil(t, comp, "completion")
	assert.Equal(t, []string{"package main"}, comp.Lines, "lines")
}

func TestConvertSingleItem_EmptyText(t *testing.T) {
	p := &Provider{}
	item := windsurfCompletionItem{
		Completion: windsurfCompletion{
			CompletionID: "abc123",
			Text:         "",
		},
		Range: makeRange("0", "0", "5"),
	}
	req := makeRequest([]string{"hello"}, 1, 5)

	comp := p.convertSingleItem(item, req, 0)
	assert.Nil(t, comp, "completion should be nil for empty text")
}

func TestConvertResponse_MetricsInfo(t *testing.T) {
	p := &Provider{}
	wsResp := &windsurfResponse{
		State: windsurfState{State: "CODEIUM_STATE_SUCCESS"},
		CompletionItems: []windsurfCompletionItem{
			{
				Completion: windsurfCompletion{
					CompletionID: "comp-id-1",
					Text:         "new text",
				},
				Range: makeRange("0", "0", "4"),
			},
		},
	}
	req := makeRequest([]string{"old"}, 1, 3)

	resp, err := p.convertResponse(wsResp, req)
	assert.NoError(t, err, "convertResponse error")
	assert.Len(t, 1, resp.Completions, "completions")
	assert.NotNil(t, resp.MetricsInfo, "metricsInfo")
	assert.Equal(t, "comp-id-1", resp.MetricsInfo.ID, "metricsInfo.ID")
}

func TestResolveLanguage(t *testing.T) {
	tests := []struct {
		path     string
		expected string
	}{
		{"/foo/bar/main.go", "go"},
		{"/foo/test.py", "python"},
		{"/foo/app.js", "javascript"},
		{"/foo/app.ts", "typescript"},
		{"/foo/App.java", "java"},
		{"/foo/main.rs", "rust"},
		{"/foo/main.c", "c"},
		{"/foo/main.cpp", "cpp"},
		{"/foo/init.lua", "lua"},
		{"/foo/config.yaml", "yaml"},
		{"/foo/config.yml", "yaml"},
		{"/foo/script.sh", "shell"},
		{"/foo/script.bash", "shell"},
		{"/foo/Makefile", "plaintext"},
		{"/foo/readme", "plaintext"},
	}

	for _, tt := range tests {
		result := resolveLanguage(tt.path)
		assert.Equal(t, tt.expected, result, "resolveLanguage("+tt.path+")")
	}
}

func TestConvertSingleItem_ColOutOfBounds(t *testing.T) {
	p := &Provider{}
	item := windsurfCompletionItem{
		Completion: windsurfCompletion{
			CompletionID: "abc123",
			Text:         "more",
		},
		Range: makeRange("0", "0", "200"),
	}
	req := makeRequest([]string{"short"}, 1, 5)

	comp := p.convertSingleItem(item, req, 0)
	assert.NotNil(t, comp, "completion")
	assert.Equal(t, []string{"more"}, comp.Lines, "lines with clamped col")
}
