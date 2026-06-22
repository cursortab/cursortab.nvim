package zeta

import (
	"cursortab/assert"
	"cursortab/client/openai"
	sourcectx "cursortab/ctx"
	"cursortab/provider"
	"cursortab/types"
	"strings"
	"testing"
)

func completionInput(lines []string, cursorRow int, cursorCol int) sourcectx.CompletionInput {
	return sourcectx.CompletionInput{
		Current: sourcectx.CurrentSnapshot{
			File: sourcectx.FileSnapshot{
				Lines: lines,
			},
			Cursor: sourcectx.CursorPosition{
				Row: cursorRow,
				Col: cursorCol,
			},
		},
	}
}

func TestBuildUserExcerpt_EmptyFile(t *testing.T) {
	current := sourcectx.CurrentSnapshot{
		File: sourcectx.FileSnapshot{
			Path:  "main.go",
			Lines: []string{},
		},
	}
	ctx := &provider.RequestState{
		Input: sourcectx.CompletionInput{Current: current},
	}

	result := buildUserExcerpt(current, ctx)

	assert.True(t, strings.Contains(result, "```main.go"), "should have file path")
	assert.True(t, strings.Contains(result, "<|start_of_file|>"), "should have start marker")
	assert.True(t, strings.Contains(result, "<|editable_region_start|>"), "should have editable start")
	assert.True(t, strings.Contains(result, "<|user_cursor_is_here|>"), "should have cursor marker")
	assert.True(t, strings.Contains(result, "<|editable_region_end|>"), "should have editable end")
}

func TestBuildUserExcerpt_WithContent(t *testing.T) {
	current := sourcectx.CurrentSnapshot{
		File: sourcectx.FileSnapshot{
			Path:  "main.go",
			Lines: []string{"func main() {", "  println()", "}"},
		},
		Cursor: sourcectx.CursorPosition{Row: 2, Col: 2},
	}
	ctx := &provider.RequestState{
		Input:        sourcectx.CompletionInput{Current: current},
		TrimmedLines: current.File.Lines,
		WindowStart:  0,
	}

	result := buildUserExcerpt(current, ctx)

	assert.True(t, strings.Contains(result, "func main() {"), "should have first line")
	assert.True(t, strings.Contains(result, "<|user_cursor_is_here|>"), "should have cursor marker")
	assert.True(t, strings.Contains(result, "  <|user_cursor_is_here|>println()"), "cursor at correct position")
}

func TestBuildUserExcerpt_CursorAtEndOfLine(t *testing.T) {
	current := sourcectx.CurrentSnapshot{
		File: sourcectx.FileSnapshot{
			Path:  "main.go",
			Lines: []string{"hello"},
		},
		Cursor: sourcectx.CursorPosition{Row: 1, Col: 100}, // Beyond line length
	}
	ctx := &provider.RequestState{
		Input:        sourcectx.CompletionInput{Current: current},
		TrimmedLines: current.File.Lines,
		WindowStart:  0,
	}

	result := buildUserExcerpt(current, ctx)

	assert.True(t, strings.Contains(result, "hello<|user_cursor_is_here|>"), "cursor at line end")
}

func TestFormatDiagnosticsForPrompt_Empty(t *testing.T) {
	result := formatDiagnosticsForPrompt(nil)
	assert.Equal(t, "", result, "empty for no diagnostics")
}

func TestFormatDiagnosticsForPrompt_WithErrors(t *testing.T) {
	diagnostics := &types.Diagnostics{
		FilePath: "src/main.go",
		Items: []*types.Diagnostic{
			{
				Severity: types.SeverityError,
				Message:  "undefined: foo",
				Source:   "gopls",
				Range: &types.CursorRange{
					StartLine: 10,
				},
			},
		},
	}

	result := formatDiagnosticsForPrompt(diagnostics)

	assert.True(t, strings.Contains(result, "src/main.go"), "should have file path")
	assert.True(t, strings.Contains(result, "line 10"), "should have line number")
	assert.True(t, strings.Contains(result, "[ERROR]"), "should have severity")
	assert.True(t, strings.Contains(result, "undefined: foo"), "should have message")
	assert.True(t, strings.Contains(result, "(source: gopls)"), "should have source")
}

func TestBuildInstructionPrompt(t *testing.T) {
	result := buildInstructionPrompt("user edits", "diagnostics", "treesitter ctx", "git diff", "recent files", "user excerpt")

	assert.True(t, strings.Contains(result, "### Instruction:"), "should have instruction")
	assert.True(t, strings.Contains(result, "### User Edits:"), "should have edits section")
	assert.True(t, strings.Contains(result, "user edits"), "should have edits content")
	assert.True(t, strings.Contains(result, "### Diagnostics:"), "should have diagnostics section")
	assert.True(t, strings.Contains(result, "### Code Context:"), "should have code context section")
	assert.True(t, strings.Contains(result, "### Staged Changes:"), "should have staged changes section")
	assert.True(t, strings.Contains(result, "### Recent Files:"), "should have recent files section")
	assert.True(t, strings.Contains(result, "### User Excerpt:"), "should have excerpt section")
	assert.True(t, strings.Contains(result, "### Response:"), "should have response marker")
}

func TestBuildInstructionPrompt_NoOptionalSections(t *testing.T) {
	result := buildInstructionPrompt("user edits", "", "", "", "", "user excerpt")

	assert.False(t, strings.Contains(result, "### Diagnostics:"), "should not have diagnostics section")
	assert.False(t, strings.Contains(result, "### Code Context:"), "should not have code context section")
	assert.False(t, strings.Contains(result, "### Staged Changes:"), "should not have staged changes section")
	assert.False(t, strings.Contains(result, "### Recent Files:"), "should not have recent files section")
}

func TestParseCompletion_WithEditableRegion(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
	}
	p := NewProvider(config)

	ctx := &provider.RequestState{
		Input:        completionInput([]string{"line 1", "line 2"}, 0, 0),
		TrimmedLines: []string{"line 1", "line 2"},
		WindowStart:  0,
	}

	resp := p.ParseResult(p, ctx, &openai.StreamResult{
		Text: "<|editable_region_start|>\nmodified line 1\nmodified line 2\n<|editable_region_end|>",
	})
	assert.NotNil(t, resp, "should have response")
}

func TestParseCompletion_NoEditableMarker(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
	}
	p := NewProvider(config)

	ctx := &provider.RequestState{
		Input:        completionInput([]string{"line 1"}, 1, 6),
		TrimmedLines: []string{"line 1"},
		WindowStart:  0,
	}

	resp := p.ParseResult(p, ctx, &openai.StreamResult{
		Text: " completion",
	})
	assert.NotNil(t, resp, "should have response")
}

func TestParseCompletion_StripsMarkers(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
	}
	p := NewProvider(config)

	ctx := &provider.RequestState{
		Input:        completionInput([]string{"original"}, 1, 0),
		TrimmedLines: []string{"original"},
		WindowStart:  0,
	}

	resp := p.ParseResult(p, ctx, &openai.StreamResult{
		Text: "<|editable_region_start|>\nmodified<|user_cursor_is_here|> text\n<|editable_region_end|>",
	})
	// The cursor marker should be stripped
	if len(resp.Completions) > 0 {
		assert.False(t, strings.Contains(resp.Completions[0].Lines[0], "<|user_cursor_is_here|>"), "cursor marker stripped")
	}
}

func TestParseCompletion_IdenticalContent(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
	}
	p := NewProvider(config)

	ctx := &provider.RequestState{
		Input:        completionInput([]string{"line 1", "line 2"}, 0, 0),
		TrimmedLines: []string{"line 1", "line 2"},
		WindowStart:  0,
	}

	resp := p.ParseResult(p, ctx, &openai.StreamResult{
		Text: "<|editable_region_start|>\nline 1\nline 2\n<|editable_region_end|>",
	})
	assert.Nil(t, resp.Completions, "no completions for identical content")
}

func TestParseSimpleCompletion(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
	}
	p := NewProvider(config)

	ctx := &provider.RequestState{
		Input: completionInput([]string{"hello"}, 1, 5),
	}

	resp := parseSimpleCompletion(p, ctx, &openai.StreamResult{Text: " world"}, 0)
	assert.NotNil(t, resp, "should have response")
	assert.True(t, len(resp.Completions) > 0, "should have completions")
	assert.Equal(t, "hello world", resp.Completions[0].Lines[0], "completion merged")
}

func TestParseSimpleCompletion_MultiLine(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
	}
	p := NewProvider(config)

	ctx := &provider.RequestState{
		Input: completionInput([]string{"start"}, 1, 5),
	}

	resp := parseSimpleCompletion(p, ctx, &openai.StreamResult{Text: " middle\nend"}, 0)
	assert.Len(t, 1, resp.Completions, "completions count")
	assert.Equal(t, 2, len(resp.Completions[0].Lines), "should have 2 lines")
}
