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
			editStart, editEnd, contextStart, contextEnd := computeRegions(tt.lines, tt.cursorRow, nil)
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
		nil,
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

	prompt := buildPrompt("main.go", lines, 1, 1, 1, 1, 1, 0, nil, snapshots, nil, nil, nil)

	assert.Contains(t, prompt, RecentlyViewedSnippetStart, "snippet start")
	assert.Contains(t, prompt, "code_snippet_file_path: helper.go", "snippet path")
	assert.Contains(t, prompt, "func helper() {}", "snippet content")
}

func TestBuildPromptWithDiagnostics(t *testing.T) {
	lines := []string{"code"}
	diag := &types.Diagnostics{
		FilePath: "main.go",
		Items: []*types.Diagnostic{
			{
				Message:  "undefined: processItems",
				Source:   "gopls",
				Severity: types.SeverityError,
				Range:    &types.CursorRange{StartLine: 12},
			},
			{
				Message:  "unused variable 'x'",
				Source:   "gopls",
				Severity: types.SeverityWarning,
			},
		},
	}

	prompt := buildPrompt("main.go", lines, 1, 1, 1, 1, 1, 0, nil, nil, diag, nil, nil)

	assert.Contains(t, prompt, "code_snippet_file_path: diagnostics", "diagnostics snippet path")
	assert.Contains(t, prompt, "line 12: [ERROR] undefined: processItems (source: gopls)", "error with line")
	assert.Contains(t, prompt, "[WARNING] unused variable 'x' (source: gopls)", "warning without line")
}

func TestBuildPromptWithTreesitter(t *testing.T) {
	lines := []string{"code"}
	ts := &types.TreesitterContext{
		EnclosingSignature: "func process(items []string) []string {",
		Siblings: []*types.TreesitterSymbol{
			{Name: "main", Signature: "func main() {", Line: 15},
		},
		Imports: []string{"import \"fmt\""},
	}

	prompt := buildPrompt("main.go", lines, 1, 1, 1, 1, 1, 0, nil, nil, nil, ts, nil)

	assert.Contains(t, prompt, "code_snippet_file_path: treesitter_context", "treesitter snippet path")
	assert.Contains(t, prompt, "Enclosing scope: func process(items []string) []string {", "enclosing scope")
	assert.Contains(t, prompt, "Sibling: line 15: func main() {", "sibling")
	assert.Contains(t, prompt, "Import: import \"fmt\"", "import")
}

func TestBuildPromptWithGitDiff(t *testing.T) {
	lines := []string{"code"}
	gd := &types.GitDiffContext{
		Diff: "+func newHelper() {}\n-func oldHelper() {}\n",
	}

	prompt := buildPrompt("main.go", lines, 1, 1, 1, 1, 1, 0, nil, nil, nil, nil, gd)

	assert.Contains(t, prompt, "code_snippet_file_path: staged_git_diff", "git diff snippet path")
	assert.Contains(t, prompt, "+func newHelper() {}", "diff content")
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

	prompt := buildPrompt("main.go", lines, 1, 1, 1, 1, 1, 0, histories, nil, nil, nil, nil)

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
			contains: []string{"--- test.go", "+++ test.go", "@@ -1,1 +1,1 @@", "-before", "+after"},
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
			contains: []string{"--- a.go", "@@ -1,1 +1,1 @@", "+a2", "--- b.go", "+b2"},
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
		assert.Equal(t, mercuryapi.Model, req.Model, "model")

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

func TestProviderGetCompletionUsesConfiguredModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req mercuryapi.Request
		json.Unmarshal(body, &req)
		assert.Equal(t, "custom-model", req.Model, "model")

		resp := mercuryapi.Response{
			ID:      "resp-123",
			Choices: []mercuryapi.Choice{{Message: mercuryapi.MessageContent{Content: "None"}}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	provider := NewProvider(&types.ProviderConfig{
		ProviderURL:       server.URL,
		ProviderModel:     "custom-model",
		CompletionTimeout: 30000,
	})

	req := &types.CompletionRequest{
		FilePath:  "test.go",
		Lines:     []string{"func original() {}"},
		CursorRow: 1,
		CursorCol: 5,
	}

	_, err := provider.GetCompletion(context.Background(), req)
	assert.NoError(t, err, "GetCompletion")
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

func TestComputeRegionsWithSyntaxRanges(t *testing.T) {
	// 13-line Go file, cursor at line 6 (inside for-loop)
	lines := []string{
		"package main",                       // 1
		"",                                   // 2
		"func process() {",                   // 3
		"    x := 1",                         // 4
		"    for i := range items {",         // 5
		"        result = append(result, i)", // 6  <- cursor
		"    }",                              // 7
		"    return result",                  // 8
		"}",                                  // 9
		"",                                   // 10
		"func main() {",                      // 11
		"    process()",                      // 12
		"}",                                  // 13
	}

	// Syntax ranges: for-loop (5-7), func process (3-9)
	syntaxRanges := []*types.LineRange{
		{StartLine: 5, EndLine: 7},
		{StartLine: 3, EndLine: 9},
	}

	editStart, editEnd, _, _ := computeRegions(lines, 6, syntaxRanges)

	// With syntax ranges, the editable region should snap to function boundaries
	// rather than cutting mid-block
	assert.True(t, editStart <= 3, "should include func start")
	assert.True(t, editEnd >= 9, "should include func end")
}

func TestCursorTagPlacement(t *testing.T) {
	lines := []string{"hello world"}

	// Cursor at position 5 (after "hello")
	prompt := buildPrompt("test.go", lines, 1, 1, 1, 1, 1, 5, nil, nil, nil, nil, nil)

	// Should have cursor after "hello"
	assert.Contains(t, prompt, "hello"+CursorTag+" world", "cursor at col 5")
}

func TestCursorTagAtLineEnd(t *testing.T) {
	lines := []string{"hello"}

	// Cursor at end of line
	prompt := buildPrompt("test.go", lines, 1, 1, 1, 1, 1, 100, nil, nil, nil, nil, nil)

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
