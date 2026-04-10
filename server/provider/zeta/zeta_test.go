package zeta

import (
	"cursortab/assert"
	"cursortab/client/openai"
	"cursortab/provider"
	"cursortab/types"
	"strings"
	"testing"
)

func TestBuildUserExcerpt_EmptyFile(t *testing.T) {
	req := &types.CompletionRequest{
		FilePath: "main.go",
		Lines:    []string{},
	}
	ctx := &provider.Context{
		Request: req,
	}

	result := buildUserExcerpt(req, ctx)

	assert.True(t, strings.Contains(result, "```main.go"), "should have file path")
	assert.True(t, strings.Contains(result, "<|start_of_file|>"), "should have start marker")
	assert.True(t, strings.Contains(result, "<|editable_region_start|>"), "should have editable start")
	assert.True(t, strings.Contains(result, "<|user_cursor_is_here|>"), "should have cursor marker")
	assert.True(t, strings.Contains(result, "<|editable_region_end|>"), "should have editable end")
}

func TestBuildUserExcerpt_WithContent(t *testing.T) {
	req := &types.CompletionRequest{
		FilePath:  "main.go",
		Lines:     []string{"func main() {", "  println()", "}"},
		CursorRow: 2,
		CursorCol: 2,
	}
	ctx := &provider.Context{
		Request:     req,
		WindowStart: 0,
		WindowEnd:   3,
	}

	result := buildUserExcerpt(req, ctx)

	assert.True(t, strings.Contains(result, "func main() {"), "should have first line")
	assert.True(t, strings.Contains(result, "<|user_cursor_is_here|>"), "should have cursor marker")
	assert.True(t, strings.Contains(result, "  <|user_cursor_is_here|>println()"), "cursor at correct position")
}

func TestBuildUserExcerpt_CursorAtEndOfLine(t *testing.T) {
	req := &types.CompletionRequest{
		FilePath:  "main.go",
		Lines:     []string{"hello"},
		CursorRow: 1,
		CursorCol: 100, // Beyond line length
	}
	ctx := &provider.Context{
		Request:     req,
		WindowStart: 0,
		WindowEnd:   1,
	}

	result := buildUserExcerpt(req, ctx)

	assert.True(t, strings.Contains(result, "hello<|user_cursor_is_here|>"), "cursor at line end")
}

func TestFormatDiagnosticsForPrompt_Empty(t *testing.T) {
	req := &types.CompletionRequest{}
	result := formatDiagnosticsForPrompt(req)
	assert.Equal(t, "", result, "empty for no diagnostics")
}

func TestFormatDiagnosticsForPrompt_WithErrors(t *testing.T) {
	req := &types.CompletionRequest{
		AdditionalContext: &types.ContextResult{
			Diagnostics: &types.LinterErrors{
				RelativeWorkspacePath: "src/main.go",
				Errors: []*types.LinterError{
					{
						Severity: "error",
						Message:  "undefined: foo",
						Source:   "gopls",
						Range: &types.CursorRange{
							StartLine: 10,
						},
					},
				},
			},
		},
	}

	result := formatDiagnosticsForPrompt(req)

	assert.True(t, strings.Contains(result, "src/main.go"), "should have file path")
	assert.True(t, strings.Contains(result, "line 10"), "should have line number")
	assert.True(t, strings.Contains(result, "[error]"), "should have severity")
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

	ctx := &provider.Context{
		Request: &types.CompletionRequest{
			Lines: []string{"line 1", "line 2"},
		},
		Result: &openai.StreamResult{
			Text: "<|editable_region_start|>\nmodified line 1\nmodified line 2\n<|editable_region_end|>",
		},
		WindowStart: 0,
		WindowEnd:   2,
	}

	resp, ok := parseCompletion(p, ctx)

	assert.True(t, ok, "should succeed")
	assert.NotNil(t, resp, "should have response")
}

func TestParseCompletion_NoEditableMarker(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
	}
	p := NewProvider(config)

	ctx := &provider.Context{
		Request: &types.CompletionRequest{
			Lines:     []string{"line 1"},
			CursorRow: 1,
			CursorCol: 6,
		},
		Result: &openai.StreamResult{
			Text: " completion",
		},
		WindowStart: 0,
		WindowEnd:   1,
	}

	resp, ok := parseCompletion(p, ctx)

	assert.True(t, ok, "should succeed")
	assert.NotNil(t, resp, "should have response")
}

func TestParseCompletion_StripsMarkers(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
	}
	p := NewProvider(config)

	ctx := &provider.Context{
		Request: &types.CompletionRequest{
			Lines:     []string{"original"},
			CursorRow: 1,
			CursorCol: 0,
		},
		Result: &openai.StreamResult{
			Text: "<|editable_region_start|>\nmodified<|user_cursor_is_here|> text\n<|editable_region_end|>",
		},
		WindowStart: 0,
		WindowEnd:   1,
	}

	resp, ok := parseCompletion(p, ctx)

	assert.True(t, ok, "should succeed")
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

	ctx := &provider.Context{
		Request: &types.CompletionRequest{
			Lines: []string{"line 1", "line 2"},
		},
		Result: &openai.StreamResult{
			Text: "<|editable_region_start|>\nline 1\nline 2\n<|editable_region_end|>",
		},
		WindowStart: 0,
		WindowEnd:   2,
	}

	resp, ok := parseCompletion(p, ctx)

	assert.True(t, ok, "should succeed")
	assert.Nil(t, resp.Completions, "no completions for identical content")
}

func TestParseSimpleCompletion(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
	}
	p := NewProvider(config)

	ctx := &provider.Context{
		Request: &types.CompletionRequest{
			Lines:     []string{"hello"},
			CursorRow: 1,
			CursorCol: 5,
		},
		Result: &openai.StreamResult{
			Text: " world",
		},
	}

	resp, ok := parseSimpleCompletion(p, ctx)

	assert.True(t, ok, "should succeed")
	assert.NotNil(t, resp, "should have response")
	assert.True(t, len(resp.Completions) > 0, "should have completions")
	assert.Equal(t, "hello world", resp.Completions[0].Lines[0], "completion merged")
}

func TestParseSimpleCompletion_MultiLine(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
	}
	p := NewProvider(config)

	ctx := &provider.Context{
		Request: &types.CompletionRequest{
			Lines:     []string{"start"},
			CursorRow: 1,
			CursorCol: 5,
		},
		Result: &openai.StreamResult{
			Text: " middle\nend",
		},
	}

	resp, ok := parseSimpleCompletion(p, ctx)

	assert.True(t, ok, "should succeed")
	assert.Len(t, 1, resp.Completions, "completions count")
	assert.Equal(t, 2, len(resp.Completions[0].Lines), "should have 2 lines")
}
