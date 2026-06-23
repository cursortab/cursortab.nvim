package inline

import (
	"cursortab/assert"
	"cursortab/client/openai"
	sourcectx "cursortab/ctx"
	"cursortab/provider"
	"cursortab/types"
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

func buildPromptForTest(p *provider.Provider, ctx *provider.RequestState) *openai.CompletionRequest {
	return buildRequest(p, ctx)
}

func parseCompletion(p *provider.Provider, ctx *provider.RequestState, result *openai.StreamResult) *types.CompletionResponse {
	return parseResult(p, ctx, result)
}

func TestBuildPrompt_EmptyLines(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel:       "test-model",
		ProviderTemperature: 0.5,
		ProviderMaxTokens:   50,
	}
	p := NewProvider(config)

	ctx := &provider.RequestState{
		Input:        sourcectx.CompletionInput{},
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

	ctx := &provider.RequestState{
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

	ctx := &provider.RequestState{
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

	ctx := &provider.RequestState{
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

	ctx := &provider.RequestState{
		Input: completionInput([]string{"func main() {"}, 1, 13),
	}

	resp := parseCompletion(p, ctx, &openai.StreamResult{Text: " fmt.Println()"})
	assert.NotNil(t, resp, "response should not be nil")
	assert.NotNil(t, resp.Completion, "should have 1 completion")
	assert.Equal(t, "func main() { fmt.Println()", resp.Completion.Lines[0], "completion merged with line")
}

func TestParseCompletion_CursorClamped(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
	}
	p := NewProvider(config)

	ctx := &provider.RequestState{
		Input: completionInput([]string{"abc"}, 1, 100),
	}

	resp := parseCompletion(p, ctx, &openai.StreamResult{Text: "def"})
	assert.Equal(t, "abcdef", resp.Completion.Lines[0], "cursor clamped to line end")
}

func TestParseCompletion_RejectsTruncatedResult(t *testing.T) {
	p := NewProvider(&types.ProviderConfig{ProviderModel: "test-model"})
	ctx := &provider.RequestState{
		Input: completionInput([]string{"abc"}, 1, 3),
	}

	resp := parseCompletion(p, ctx, &openai.StreamResult{
		Text:         "def",
		FinishReason: "length",
	})
	assert.Nil(t, resp.Completion, "truncated result should not produce completion")
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

	ctx := &provider.RequestState{
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

	ctx := &provider.RequestState{
		Input: completionInput([]string{"def bubble_sort(arr):", "    "}, 2, 4),
	}

	resp := parseCompletion(p, ctx, &openai.StreamResult{
		Text: "    n = len(arr)", // Model includes indentation since prompt had it stripped
	})
	assert.NotNil(t, resp, "response should not be nil")
	assert.NotNil(t, resp.Completion, "should have 1 completion")
	// Should be "    n = len(arr)" — not "        n = len(arr)" (double-indented)
	assert.Equal(t, "    n = len(arr)", resp.Completion.Lines[0], "should not double-indent")
}
