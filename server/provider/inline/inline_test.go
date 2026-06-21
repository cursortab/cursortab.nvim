package inline

import (
	"cursortab/assert"
	"cursortab/client/openai"
	sourcectx "cursortab/ctx"
	"cursortab/provider"
	"cursortab/types"
	"testing"
)

func batchContextFromContext(ctx *provider.Context) *provider.BatchContext {
	input := ctx.Input
	if ctx.Request != nil {
		input.Trigger = ctx.Request.Source
		input.Current.Workspace.Path = ctx.Request.WorkspacePath
		input.Current.Workspace.ID = ctx.Request.WorkspaceID
		input.Current.File.Path = ctx.Request.FilePath
		input.Current.File.Lines = ctx.Request.Lines
		input.Current.File.Version = ctx.Request.Version
		input.Current.Cursor.Row = ctx.Request.CursorRow
		input.Current.Cursor.Col = ctx.Request.CursorCol
		input.Current.View.ViewportHeight = ctx.Request.ViewportHeight
		input.Current.View.MaxVisibleLines = ctx.Request.MaxVisibleLines
	}
	return &provider.BatchContext{
		Input:        input,
		TrimmedLines: ctx.TrimmedLines,
		WindowStart:  ctx.WindowStart,
		WindowEnd:    ctx.WindowEnd,
		CursorLine:   ctx.CursorLine,
		MaxLines:     ctx.MaxLines,
		EndLineInc:   ctx.EndLineInc,
		Prefill:      ctx.Prefill,
	}
}

func buildPromptForTest(p *provider.Provider, ctx *provider.Context) *openai.CompletionRequest {
	return buildBatch(p, batchContextFromContext(ctx))
}

func parseCompletion(p *provider.Provider, ctx *provider.Context) (*types.CompletionResponse, bool) {
	return parseBatch(p, batchContextFromContext(ctx), ctx.Result)
}

func TestBuildPrompt_EmptyLines(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel:       "test-model",
		ProviderTemperature: 0.5,
		ProviderMaxTokens:   50,
	}
	p := NewProvider(config)

	ctx := &provider.Context{
		Input:        sourcectx.CompletionInput{},
		Request:      &types.CompletionRequest{},
		TrimmedLines: []string{},
		CursorLine:   0,
	}

	req := buildPromptForTest(p, ctx)

	assert.Equal(t, "", req.Prompt, "prompt should be empty")
	assert.Equal(t, "test-model", req.Model, "model")
	assert.Equal(t, 0.5, req.Temperature, "temperature")
	assert.Equal(t, 50, req.MaxTokens, "max tokens")
}

func TestBuildPrompt_SingleLine(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
	}
	p := NewProvider(config)

	ctx := &provider.Context{
		Input: sourcectx.CompletionInput{
			Current: sourcectx.CurrentSnapshot{
				Cursor: sourcectx.CursorPosition{Col: 5},
			},
		},
		TrimmedLines: []string{"hello world"},
		CursorLine:   0,
	}

	req := buildPromptForTest(p, ctx)

	assert.Equal(t, "hello", req.Prompt, "prompt should include text before cursor")
}

func TestBuildPrompt_MultiLine(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
	}
	p := NewProvider(config)

	ctx := &provider.Context{
		Input: sourcectx.CompletionInput{
			Current: sourcectx.CurrentSnapshot{
				Cursor: sourcectx.CursorPosition{Col: 4},
			},
		},
		TrimmedLines: []string{"line 1", "line 2", "line 3"},
		CursorLine:   2,
	}

	req := buildPromptForTest(p, ctx)

	expected := "line 1\nline 2\nline"
	assert.Equal(t, expected, req.Prompt, "prompt should include lines before and partial current line")
}

func TestBuildPrompt_CursorBeyondLineLength(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
	}
	p := NewProvider(config)

	ctx := &provider.Context{
		Input: sourcectx.CompletionInput{
			Current: sourcectx.CurrentSnapshot{
				Cursor: sourcectx.CursorPosition{Col: 100}, // Beyond line length
			},
		},
		TrimmedLines: []string{"short"},
		CursorLine:   0,
	}

	req := buildPromptForTest(p, ctx)

	assert.Equal(t, "short", req.Prompt, "prompt should include entire line when cursor is beyond")
}

func TestParseCompletion(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
	}
	p := NewProvider(config)

	ctx := &provider.Context{
		Request: &types.CompletionRequest{
			Lines:     []string{"func main() {"},
			CursorRow: 1,
			CursorCol: 13,
		},
		Result: &openai.StreamResult{
			Text: " fmt.Println()",
		},
	}

	resp, ok := parseCompletion(p, ctx)

	assert.True(t, ok, "should succeed")
	assert.NotNil(t, resp, "response should not be nil")
	assert.Equal(t, 1, len(resp.Completions), "should have 1 completion")
	assert.Equal(t, "func main() { fmt.Println()", resp.Completions[0].Lines[0], "completion merged with line")
}

func TestParseCompletion_CursorClamped(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
	}
	p := NewProvider(config)

	ctx := &provider.Context{
		Request: &types.CompletionRequest{
			Lines:     []string{"abc"},
			CursorRow: 1,
			CursorCol: 100, // Beyond line length
		},
		Result: &openai.StreamResult{
			Text: "def",
		},
	}

	resp, ok := parseCompletion(p, ctx)

	assert.True(t, ok, "should succeed")
	assert.Equal(t, "abcdef", resp.Completions[0].Lines[0], "cursor clamped to line end")
}

// TestBuildPrompt_StripsCursorLineTrailingWhitespace tests that when the cursor is at the
// end of a whitespace-only line (e.g., after pressing Enter with auto-indent), the trailing
// whitespace is stripped from the prompt. This prevents the model from seeing trailing
// whitespace and generating an empty completion (the model stops immediately when the
// prompt ends with indentation, as it considers the line already "complete").
func TestBuildPrompt_StripsCursorLineTrailingWhitespace(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
	}
	p := NewProvider(config)

	ctx := &provider.Context{
		Input: sourcectx.CompletionInput{
			Current: sourcectx.CurrentSnapshot{
				Cursor: sourcectx.CursorPosition{Col: 4},
			},
		},
		TrimmedLines: []string{
			"def bubble_sort(arr):",
			"    ", // auto-indent: cursor at col 4, all whitespace
		},
		CursorLine: 1,
	}

	req := buildPromptForTest(p, ctx)

	// Prompt should NOT end with "    " (trailing whitespace)
	// It should end with the line before cursor (no whitespace-only suffix)
	expected := "def bubble_sort(arr):\n"
	assert.Equal(t, expected, req.Prompt, "prompt should not include trailing whitespace on cursor line")
}

// TestParseCompletion_WhitespaceOnlyCursorLine tests that when the cursor is on a
// whitespace-only line and the model returns a completion with leading indentation
// (because the prompt had the whitespace stripped), parseCompletion does not double-indent.
// The model returns "    n = len(arr)" and beforeCursor should be "" (stripped from "    "),
// so newLine = "" + "    n = len(arr)" = "    n = len(arr)".
func TestParseCompletion_WhitespaceOnlyCursorLine(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
	}
	p := NewProvider(config)

	ctx := &provider.Context{
		Request: &types.CompletionRequest{
			Lines:     []string{"def bubble_sort(arr):", "    "},
			CursorRow: 2,
			CursorCol: 4, // End of "    " (auto-indent)
		},
		Result: &openai.StreamResult{
			Text: "    n = len(arr)", // Model includes indentation since prompt had it stripped
		},
	}

	resp, ok := parseCompletion(p, ctx)

	assert.True(t, ok, "should succeed")
	assert.NotNil(t, resp, "response should not be nil")
	assert.Equal(t, 1, len(resp.Completions), "should have 1 completion")
	// Should be "    n = len(arr)" — not "        n = len(arr)" (double-indented)
	assert.Equal(t, "    n = len(arr)", resp.Completions[0].Lines[0], "should not double-indent")
}
