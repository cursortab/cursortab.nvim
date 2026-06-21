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

func TestBuildPrompt_EmptyLines(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
	}
	p := NewProvider(config)

	ctx := &provider.BatchContext{
		Input: sourcectx.CompletionInput{
			Current: sourcectx.CurrentSnapshot{
				File: sourcectx.FileSnapshot{Path: "main.go", Lines: []string{}},
			},
		},
		TrimmedLines: []string{},
	}

	req := p.BuildBatch(p, ctx)

	assert.True(t, strings.Contains(req.Prompt, "<|file_sep|>original/main.go"), "should have original marker")
	assert.True(t, strings.Contains(req.Prompt, "<|file_sep|>current/main.go"), "should have current marker")
	assert.True(t, strings.Contains(req.Prompt, "<|file_sep|>updated/main.go"), "should have updated marker")
}

func TestBuildPrompt_WithContent(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
	}
	p := NewProvider(config)

	ctx := &provider.BatchContext{
		Input: sourcectx.CompletionInput{
			Current: sourcectx.CurrentSnapshot{
				File: sourcectx.FileSnapshot{Path: "main.go", Lines: []string{"line 1", "line 2"}},
			},
		},
		TrimmedLines: []string{"line 1", "line 2"},
		WindowStart:  0,
		WindowEnd:    2,
	}

	req := p.BuildBatch(p, ctx)

	assert.True(t, strings.Contains(req.Prompt, "line 1\nline 2"), "should contain file content")
}

func TestBuildPrompt_WithDiffHistory(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
	}
	p := NewProvider(config)

	ctx := &provider.BatchContext{
		Input: sourcectx.CompletionInput{
			Current: sourcectx.CurrentSnapshot{
				File: sourcectx.FileSnapshot{Path: "main.go", Lines: []string{"line 1"}},
			},
			Context: sourcectx.CollectedContext{
				sourcectx.EditHistory{Files: []*types.FileDiffHistory{
					{
						FileName: "other.go",
						DiffHistory: []*types.DiffEntry{
							{Original: "old code", Updated: "new code"},
						},
					},
				}},
			},
		},
		TrimmedLines: []string{"line 1"},
		WindowStart:  0,
		WindowEnd:    1,
	}

	req := p.BuildBatch(p, ctx)

	assert.True(t, strings.Contains(req.Prompt, "other.go.diff"), "should have diff section")
	assert.True(t, strings.Contains(req.Prompt, "original:\nold code"), "should have original in diff")
	assert.True(t, strings.Contains(req.Prompt, "updated:\nnew code"), "should have updated in diff")
}

func TestParseCompletion_NoChange(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
	}
	p := NewProvider(config)

	ctx := &provider.BatchContext{
		Input: sourcectx.CompletionInput{
			Current: sourcectx.CurrentSnapshot{
				File: sourcectx.FileSnapshot{Lines: []string{"line 1", "line 2"}},
			},
		},
		WindowStart: 0,
		WindowEnd:   2,
	}

	resp, ok := p.ParseBatch(p, ctx, &openai.StreamResult{
		Text: "line 1\nline 2",
	})

	assert.True(t, ok, "should succeed")
	assert.Nil(t, resp.Completions, "no completions when text is same")
}

func TestParseCompletion_WithChange(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
	}
	p := NewProvider(config)

	ctx := &provider.BatchContext{
		Input: sourcectx.CompletionInput{
			Current: sourcectx.CurrentSnapshot{
				File: sourcectx.FileSnapshot{Lines: []string{"line 1", "line 2"}},
			},
		},
		WindowStart: 0,
		WindowEnd:   2,
	}

	resp, ok := p.ParseBatch(p, ctx, &openai.StreamResult{
		Text: "line 1\nmodified line 2",
	})

	assert.True(t, ok, "should succeed")
	assert.NotNil(t, resp, "should have response")
	assert.True(t, len(resp.Completions) > 0, "should have completions")
}

func TestParseCompletion_StripsStopTokens(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
	}
	p := NewProvider(config)

	ctx := &provider.BatchContext{
		Input: sourcectx.CompletionInput{
			Current: sourcectx.CurrentSnapshot{
				File: sourcectx.FileSnapshot{Lines: []string{"line 1"}},
			},
		},
		WindowStart: 0,
		WindowEnd:   1,
	}

	resp, ok := p.ParseBatch(p, ctx, &openai.StreamResult{
		Text: "modified line 1<|file_sep|>",
	})

	assert.True(t, ok, "should succeed")
	assert.NotNil(t, resp, "should have response")
}

func TestParseCompletion_InvalidWindow(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
	}
	p := NewProvider(config)

	ctx := &provider.BatchContext{
		Input: sourcectx.CompletionInput{
			Current: sourcectx.CurrentSnapshot{
				File: sourcectx.FileSnapshot{Lines: []string{"line 1"}},
			},
		},
		WindowStart: 5, // Invalid
		WindowEnd:   2,
	}

	resp, ok := parseBatchCompletion(p, ctx, &openai.StreamResult{
		Text: "modified",
	})

	assert.True(t, ok, "should succeed but return empty")
	assert.Nil(t, resp.Completions, "should have no completions for invalid window")
}
