package sweep

import (
	"cursortab/assert"
	"cursortab/client/openai"
	sourcectx "cursortab/ctx"
	"cursortab/provider"
	"cursortab/types"
	"strings"
	"testing"
)

func completionInput(filePath string, lines []string, cursorRow int, cursorCol int, materials ...sourcectx.Materials) sourcectx.CompletionInput {
	var collected sourcectx.Materials
	for _, material := range materials {
		collected = append(collected, material...)
	}
	return sourcectx.CompletionInput{
		Current: sourcectx.CurrentSnapshot{
			File: sourcectx.FileSnapshot{
				Path:  filePath,
				Lines: lines,
			},
			Cursor: sourcectx.CursorPosition{
				Row: cursorRow,
				Col: cursorCol,
			},
		},
		Materials: collected,
	}
}

func stateForInput(input sourcectx.CompletionInput, config *types.ProviderConfig) *provider.RequestState {
	return &provider.RequestState{
		Input:  input,
		Window: provider.RequestWindow{Lines: input.Current.File.Lines, CursorLine: input.Current.Cursor.Row - 1},
	}
}

func buildPromptForTest(p *Provider, ctx *provider.RequestState) *openai.CompletionRequest {
	req, err := p.Build(ctx)
	if err != nil {
		panic(err)
	}
	return req
}

func parseCompletionForTest(p *Provider, ctx *provider.RequestState, result *openai.CompletionResult) *types.CompletionResponse {
	resp, err := p.Parse(ctx, result)
	if err != nil {
		panic(err)
	}
	return resp
}

func TestBuildPrompt_EmptyLines(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
	}
	p := NewProvider(config)

	ctx := stateForInput(completionInput("main.go", nil, 1, 0), config)

	req := buildPromptForTest(p, ctx)

	assert.True(t, strings.Contains(req.Prompt, "<|file_sep|>original/main.go"), "should have original marker")
	assert.True(t, strings.Contains(req.Prompt, "<|file_sep|>current/main.go"), "should have current marker")
	assert.True(t, strings.Contains(req.Prompt, "<|file_sep|>updated/main.go"), "should have updated marker")
}

func TestBuildPrompt_WithContent(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
	}
	p := NewProvider(config)

	ctx := stateForInput(completionInput("main.go", []string{"line 1", "line 2"}, 1, 0), config)

	req := buildPromptForTest(p, ctx)

	assert.True(t, strings.Contains(req.Prompt, "line 1\nline 2"), "should contain file content")
}

func TestBuildPrompt_WithDiffHistory(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
	}
	p := NewProvider(config)

	ctx := stateForInput(completionInput("main.go", []string{"line 1"}, 1, 0, sourcectx.Materials{
		sourcectx.EditHistory{Files: []*types.FileDiffHistory{
			{
				FileName: "other.go",
				DiffHistory: []*types.DiffEntry{
					{Original: "old code", Updated: "new code"},
				},
			},
		}},
	}), config)

	req := buildPromptForTest(p, ctx)

	assert.True(t, strings.Contains(req.Prompt, "other.go.diff"), "should have diff section")
	assert.True(t, strings.Contains(req.Prompt, "original:\nold code"), "should have original in diff")
	assert.True(t, strings.Contains(req.Prompt, "updated:\nnew code"), "should have updated in diff")
}

func TestParseCompletion_NoChange(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
	}
	p := NewProvider(config)

	ctx := stateForInput(completionInput("", []string{"line 1", "line 2"}, 1, 0), config)

	resp := parseCompletionForTest(p, ctx, &openai.CompletionResult{
		Text: "line 1\nline 2",
	})
	assert.Nil(t, resp.Completion, "no completions when text is same")
}

func TestParseCompletion_WithChange(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
	}
	p := NewProvider(config)

	ctx := stateForInput(completionInput("", []string{"line 1", "line 2"}, 1, 0), config)

	resp := parseCompletionForTest(p, ctx, &openai.CompletionResult{
		Text: "line 1\nmodified line 2",
	})
	assert.NotNil(t, resp, "should have response")
	assert.True(t, resp.Completion != nil, "should have completions")
}

func TestParseCompletion_StripsStopMarkers(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
	}
	p := NewProvider(config)

	ctx := stateForInput(completionInput("", []string{"line 1"}, 1, 0), config)

	resp := parseCompletionForTest(p, ctx, &openai.CompletionResult{
		Text: "modified line 1<|file_sep|>",
	})
	assert.NotNil(t, resp, "should have response")
}
