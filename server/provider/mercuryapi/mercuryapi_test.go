package mercuryapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"cursortab/assert"
	"cursortab/client/mercuryapi"
	"cursortab/types"
)

func TestComputeRegions(t *testing.T) {
	tests := []struct {
		name                                                         string
		lines                                                        []string
		cursorRow                                                    int
		wantEditStart, wantEditEnd, wantContextStart, wantContextEnd int
	}{
		{
			name:             "small file fits entirely",
			lines:            []string{"line1", "line2", "line3"},
			cursorRow:        2,
			wantEditStart:    1,
			wantEditEnd:      3,
			wantContextStart: 1,
			wantContextEnd:   3,
		},
		{
			name:             "cursor at start",
			lines:            []string{"a", "b", "c", "d", "e"},
			cursorRow:        1,
			wantEditStart:    1,
			wantEditEnd:      5,
			wantContextStart: 1,
			wantContextEnd:   5,
		},
		{
			name:             "cursor at end",
			lines:            []string{"a", "b", "c", "d", "e"},
			cursorRow:        5,
			wantEditStart:    1,
			wantEditEnd:      5,
			wantContextStart: 1,
			wantContextEnd:   5,
		},
		{
			name:             "single line file",
			lines:            []string{"hello world"},
			cursorRow:        1,
			wantEditStart:    1,
			wantEditEnd:      1,
			wantContextStart: 1,
			wantContextEnd:   1,
		},
		{
			name:             "cursor row out of bounds high",
			lines:            []string{"a", "b"},
			cursorRow:        100,
			wantEditStart:    1,
			wantEditEnd:      2,
			wantContextStart: 1,
			wantContextEnd:   2,
		},
		{
			name:             "cursor row out of bounds low",
			lines:            []string{"a", "b"},
			cursorRow:        0,
			wantEditStart:    1,
			wantEditEnd:      2,
			wantContextStart: 1,
			wantContextEnd:   2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			editStart, editEnd, contextStart, contextEnd := computeRegions(tt.lines, tt.cursorRow)
			assert.Equal(t, tt.wantEditStart, editStart, "editStart")
			assert.Equal(t, tt.wantEditEnd, editEnd, "editEnd")
			assert.Equal(t, tt.wantContextStart, contextStart, "contextStart")
			assert.Equal(t, tt.wantContextEnd, contextEnd, "contextEnd")
		})
	}
}

func TestBuildPrompt(t *testing.T) {
	lines := []string{
		"package main",
		"",
		"func main() {",
		"    fmt.Println(\"hello\")",
		"}",
	}

	prompt := buildPrompt(
		"main.go",
		lines,
		3, 4, // editable: lines 3-4
		1, 5, // context: all lines
		4, 4, // cursor at line 4, col 4
		nil,
		nil,
	)

	// Check required sections exist
	assert.Contains(t, prompt, RecentlyViewedSnippetsStart, "recently viewed start")
	assert.Contains(t, prompt, RecentlyViewedSnippetsEnd, "recently viewed end")
	assert.Contains(t, prompt, CurrentFileContentStart, "current file start")
	assert.Contains(t, prompt, CurrentFileContentEnd, "current file end")
	assert.Contains(t, prompt, CodeToEditStart, "code to edit start")
	assert.Contains(t, prompt, CodeToEditEnd, "code to edit end")
	assert.Contains(t, prompt, EditDiffHistoryStart, "edit history start")
	assert.Contains(t, prompt, EditDiffHistoryEnd, "edit history end")
	assert.Contains(t, prompt, CursorTag, "cursor tag")
	assert.Contains(t, prompt, "current_file_path: main.go", "file path")

	// Check cursor marker is in the right place (line 4, col 4)
	// The line is `    fmt.Println("hello")` and cursor at col 4 means after the indent
	assert.Contains(t, prompt, "    "+CursorTag+"fmt.Println", "cursor position")
}

func TestBuildPromptWithSnapshots(t *testing.T) {
	lines := []string{"code"}
	snapshots := []*types.RecentBufferSnapshot{
		{
			FilePath: "helper.go",
			Lines:    []string{"func helper() {}", "// end"},
		},
	}

	prompt := buildPrompt("main.go", lines, 1, 1, 1, 1, 1, 0, nil, snapshots)

	assert.Contains(t, prompt, RecentlyViewedSnippetStart, "snippet start")
	assert.Contains(t, prompt, "code_snippet_file_path: helper.go", "snippet path")
	assert.Contains(t, prompt, "func helper() {}", "snippet content")
}

func TestBuildPromptWithDiffHistory(t *testing.T) {
	lines := []string{"code"}
	histories := []*types.FileDiffHistory{
		{
			FileName: "main.go",
			DiffHistory: []*types.DiffEntry{
				{Original: "old", Updated: "new"},
			},
		},
	}

	prompt := buildPrompt("main.go", lines, 1, 1, 1, 1, 1, 0, histories, nil)

	assert.Contains(t, prompt, "--- main.go", "diff header old")
	assert.Contains(t, prompt, "+++ main.go", "diff header new")
	assert.Contains(t, prompt, "-old", "old line")
	assert.Contains(t, prompt, "+new", "new line")
}

func TestFormatDiffHistories(t *testing.T) {
	tests := []struct {
		name      string
		histories []*types.FileDiffHistory
		wantEmpty bool
		contains  []string
	}{
		{
			name:      "empty histories",
			histories: nil,
			wantEmpty: true,
		},
		{
			name: "single entry",
			histories: []*types.FileDiffHistory{
				{
					FileName: "test.go",
					DiffHistory: []*types.DiffEntry{
						{Original: "before", Updated: "after"},
					},
				},
			},
			contains: []string{"--- test.go", "+++ test.go", "-before", "+after"},
		},
		{
			name: "multiple entries",
			histories: []*types.FileDiffHistory{
				{
					FileName: "a.go",
					DiffHistory: []*types.DiffEntry{
						{Original: "a1", Updated: "a2"},
					},
				},
				{
					FileName: "b.go",
					DiffHistory: []*types.DiffEntry{
						{Original: "b1", Updated: "b2"},
					},
				},
			},
			contains: []string{"--- a.go", "+a2", "--- b.go", "+b2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatDiffHistories(tt.histories)
			if tt.wantEmpty {
				assert.Equal(t, "", result, "expected empty")
				return
			}
			for _, s := range tt.contains {
				assert.Contains(t, result, s, "contains "+s)
			}
		})
	}
}

func TestProviderGetCompletion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req mercuryapi.Request
		json.Unmarshal(body, &req)

		// Verify prompt structure
		prompt := req.Messages[0].Content
		assert.Contains(t, prompt, RecentlyViewedSnippetsStart, "recently viewed")
		assert.Contains(t, prompt, CurrentFileContentStart, "current file")
		assert.Contains(t, prompt, CodeToEditStart, "code to edit")
		assert.Contains(t, prompt, CursorTag, "cursor")

		resp := mercuryapi.Response{
			ID: "resp-123",
			Choices: []mercuryapi.Choice{
				{Message: mercuryapi.MessageContent{Content: "func updated() {}"}},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	provider := NewProvider(&types.ProviderConfig{
		ProviderURL:       server.URL,
		CompletionTimeout: 30000,
	})

	req := &types.CompletionRequest{
		FilePath:  "test.go",
		Lines:     []string{"func original() {}"},
		CursorRow: 1,
		CursorCol: 5,
	}

	resp, err := provider.GetCompletion(context.Background(), req)
	assert.NoError(t, err, "GetCompletion")
	assert.Equal(t, 1, len(resp.Completions), "completions count")
	assert.Equal(t, 1, resp.Completions[0].StartLine, "start line")
	assert.Equal(t, 1, resp.Completions[0].EndLineInc, "end line")
	assert.Equal(t, []string{"func updated() {}"}, resp.Completions[0].Lines, "lines")
}

func TestProviderGetCompletionEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := mercuryapi.Response{
			ID:      "resp-123",
			Choices: []mercuryapi.Choice{{Message: mercuryapi.MessageContent{Content: "None"}}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	provider := NewProvider(&types.ProviderConfig{
		ProviderURL:       server.URL,
		CompletionTimeout: 30000,
	})

	req := &types.CompletionRequest{
		FilePath:  "test.go",
		Lines:     []string{"code"},
		CursorRow: 1,
		CursorCol: 0,
	}

	resp, err := provider.GetCompletion(context.Background(), req)
	assert.NoError(t, err, "GetCompletion")
	assert.Equal(t, 0, len(resp.Completions), "should be empty")
}

func TestProviderGetCompletionNoOp(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return same content as input (no-op)
		resp := mercuryapi.Response{
			ID:      "resp-123",
			Choices: []mercuryapi.Choice{{Message: mercuryapi.MessageContent{Content: "unchanged"}}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	provider := NewProvider(&types.ProviderConfig{
		ProviderURL:       server.URL,
		CompletionTimeout: 30000,
	})

	req := &types.CompletionRequest{
		FilePath:  "test.go",
		Lines:     []string{"unchanged"},
		CursorRow: 1,
		CursorCol: 0,
	}

	resp, err := provider.GetCompletion(context.Background(), req)
	assert.NoError(t, err, "GetCompletion")
	assert.Equal(t, 0, len(resp.Completions), "should be empty for no-op")
}

func TestProviderEmptyLines(t *testing.T) {
	provider := NewProvider(&types.ProviderConfig{})

	req := &types.CompletionRequest{
		FilePath:  "test.go",
		Lines:     []string{},
		CursorRow: 1,
		CursorCol: 0,
	}

	resp, err := provider.GetCompletion(context.Background(), req)
	assert.NoError(t, err, "GetCompletion")
	assert.Equal(t, 0, len(resp.Completions), "should be empty")
}

func TestExpandRegion(t *testing.T) {
	tests := []struct {
		name      string
		lines     []string
		cursorIdx int
		maxChars  int
		wantStart int
		wantEnd   int
	}{
		{
			name:      "fits all",
			lines:     []string{"a", "b", "c"},
			cursorIdx: 1,
			maxChars:  100,
			wantStart: 0,
			wantEnd:   2,
		},
		{
			name:      "limited by chars",
			lines:     []string{"aaaa", "bbbb", "cccc", "dddd"},
			cursorIdx: 1,
			maxChars:  12, // Can fit ~2 lines (5 chars each with newline)
			wantStart: 0,
			wantEnd:   1,
		},
		{
			name:      "cursor at start",
			lines:     []string{"a", "b", "c"},
			cursorIdx: 0,
			maxChars:  100,
			wantStart: 0,
			wantEnd:   2,
		},
		{
			name:      "cursor at end",
			lines:     []string{"a", "b", "c"},
			cursorIdx: 2,
			maxChars:  100,
			wantStart: 0,
			wantEnd:   2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end := expandRegion(tt.lines, tt.cursorIdx, tt.maxChars)
			assert.Equal(t, tt.wantStart, start, "start")
			assert.Equal(t, tt.wantEnd, end, "end")
		})
	}
}

func TestCursorTagPlacement(t *testing.T) {
	lines := []string{"hello world"}

	// Cursor at position 5 (after "hello")
	prompt := buildPrompt("test.go", lines, 1, 1, 1, 1, 1, 5, nil, nil)

	// Should have cursor after "hello"
	assert.Contains(t, prompt, "hello"+CursorTag+" world", "cursor at col 5")
}

func TestCursorTagAtLineEnd(t *testing.T) {
	lines := []string{"hello"}

	// Cursor at end of line
	prompt := buildPrompt("test.go", lines, 1, 1, 1, 1, 1, 100, nil, nil)

	// Should clamp to end of line
	assert.Contains(t, prompt, "hello"+CursorTag+"\n", "cursor at end")
}

func TestMultilineCompletion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := mercuryapi.Response{
			ID: "resp-123",
			Choices: []mercuryapi.Choice{{
				Message: mercuryapi.MessageContent{Content: "line1\nline2\nline3"},
			}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	provider := NewProvider(&types.ProviderConfig{
		ProviderURL:       server.URL,
		CompletionTimeout: 30000,
	})

	req := &types.CompletionRequest{
		FilePath:  "test.go",
		Lines:     []string{"original"},
		CursorRow: 1,
		CursorCol: 0,
	}

	resp, err := provider.GetCompletion(context.Background(), req)
	assert.NoError(t, err, "GetCompletion")
	assert.Equal(t, 1, len(resp.Completions), "completions count")
	assert.Equal(t, 3, len(resp.Completions[0].Lines), "should have 3 lines")
	assert.Equal(t, "line1", resp.Completions[0].Lines[0], "first line")
	assert.Equal(t, "line2", resp.Completions[0].Lines[1], "second line")
	assert.Equal(t, "line3", resp.Completions[0].Lines[2], "third line")
}
