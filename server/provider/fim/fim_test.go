package fim

import (
	"cursortab/assert"
	"cursortab/client/openai"
	sourcectx "cursortab/ctx"
	"cursortab/provider"
	"cursortab/types"
	"strings"
	"testing"
)

func batchContextFromContext(ctx *provider.StreamState) *provider.BatchContext {
	return &provider.BatchContext{
		Input:        ctx.Input,
		TrimmedLines: ctx.TrimmedLines,
		WindowStart:  ctx.WindowStart,
		WindowEnd:    ctx.WindowEnd,
		CursorLine:   ctx.CursorLine,
		MaxLines:     ctx.MaxLines,
		EndLineInc:   ctx.EndLineInc,
		Prefill:      ctx.Prefill,
	}
}

func inputFromRequest(req *types.CompletionRequest) sourcectx.CompletionInput {
	return sourcectx.CompletionInput{
		Trigger: req.Source,
		Current: sourcectx.CurrentSnapshot{
			Workspace: sourcectx.WorkspaceRef{
				Path: req.WorkspacePath,
				ID:   req.WorkspaceID,
			},
			File: sourcectx.FileSnapshot{
				Path:    req.FilePath,
				Lines:   req.Lines,
				Version: req.Version,
			},
			Cursor: sourcectx.CursorPosition{
				Row: req.CursorRow,
				Col: req.CursorCol,
			},
			View: sourcectx.ViewConstraints{
				ViewportHeight:  req.ViewportHeight,
				MaxVisibleLines: req.MaxVisibleLines,
			},
		},
	}
}

func buildPromptForTest(p *provider.Provider, ctx *provider.StreamState) *openai.CompletionRequest {
	return buildBatch(p, batchContextFromContext(ctx))
}

func parseCompletion(p *provider.Provider, ctx *provider.StreamState) (*types.CompletionResponse, bool) {
	return parseBatch(p, batchContextFromContext(ctx), ctx.Result)
}

func TestFIMTokensFromConfig(t *testing.T) {
	config := &types.ProviderConfig{
		FIMTokens: &types.FIMTokenConfig{
			Prefix: "<PRE>",
			Suffix: "<SUF>",
			Middle: "<MID>",
		},
	}

	assert.Equal(t, "<PRE>", config.FIMTokens.Prefix, "prefix token")
	assert.Equal(t, "<SUF>", config.FIMTokens.Suffix, "suffix token")
	assert.Equal(t, "<MID>", config.FIMTokens.Middle, "middle token")
}

func TestBuildPrompt_EmptyLines(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
		FIMTokens: &types.FIMTokenConfig{
			Prefix: "<PRE>",
			Suffix: "<SUF>",
			Middle: "<MID>",
		},
	}
	p := NewProvider(config)

	ctx := &provider.StreamState{
		Input:        sourcectx.CompletionInput{},
		TrimmedLines: []string{},
		CursorLine:   0,
	}

	req := buildPromptForTest(p, ctx)

	assert.Equal(t, "<PRE><SUF><MID>", req.Prompt, "empty prompt should have FIM tokens only")
}

func TestBuildPrompt_SingleLineMiddle(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
		FIMTokens: &types.FIMTokenConfig{
			Prefix: "<PRE>",
			Suffix: "<SUF>",
			Middle: "<MID>",
		},
	}
	p := NewProvider(config)

	ctx := &provider.StreamState{
		Input: sourcectx.CompletionInput{
			Current: sourcectx.CurrentSnapshot{
				Cursor: sourcectx.CursorPosition{Col: 5},
			},
		},
		TrimmedLines: []string{"hello world"},
		CursorLine:   0,
	}

	req := buildPromptForTest(p, ctx)

	assert.True(t, strings.HasPrefix(req.Prompt, "<PRE>hello"), "prefix should have content before cursor")
	assert.True(t, strings.Contains(req.Prompt, "<SUF> world"), "suffix should have content after cursor")
	assert.True(t, strings.HasSuffix(req.Prompt, "<MID>"), "should end with middle token")
}

func TestBuildPrompt_MultiLine(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
		FIMTokens: &types.FIMTokenConfig{
			Prefix: "<PRE>",
			Suffix: "<SUF>",
			Middle: "<MID>",
		},
	}
	p := NewProvider(config)

	ctx := &provider.StreamState{
		Input: sourcectx.CompletionInput{
			Current: sourcectx.CurrentSnapshot{
				Cursor: sourcectx.CursorPosition{Col: 4},
			},
		},
		TrimmedLines: []string{"line 1", "line 2", "line 3"},
		CursorLine:   1,
	}

	req := buildPromptForTest(p, ctx)

	// Should have line 1 before cursor, partial line 2 before cursor
	// And rest of line 2 + line 3 after cursor
	assert.True(t, strings.Contains(req.Prompt, "line 1\n"), "should include line before cursor")
	assert.True(t, strings.Contains(req.Prompt, "<PRE>line 1\nline"), "prefix with lines before")
	assert.True(t, strings.Contains(req.Prompt, "<SUF> 2\nline 3"), "suffix with lines after")
}

func TestBuildPrompt_CursorBeyondLine(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
		FIMTokens: &types.FIMTokenConfig{
			Prefix: "<PRE>",
			Suffix: "<SUF>",
			Middle: "<MID>",
		},
	}
	p := NewProvider(config)

	ctx := &provider.StreamState{
		Input: sourcectx.CompletionInput{
			Current: sourcectx.CurrentSnapshot{
				Cursor: sourcectx.CursorPosition{Col: 100}, // Beyond line length
			},
		},
		TrimmedLines: []string{"short"},
		CursorLine:   0,
	}

	req := buildPromptForTest(p, ctx)

	assert.True(t, strings.Contains(req.Prompt, "<PRE>short<SUF><MID>"), "should handle cursor beyond line")
}

func TestParseCompletion_SingleLine(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
	}
	p := NewProvider(config)

	ctx := &provider.StreamState{
		Input: inputFromRequest(&types.CompletionRequest{
			Lines:     []string{"hello world"},
			CursorRow: 1,
			CursorCol: 5,
		}),
		Result: &openai.StreamResult{
			Text: " there",
		},
	}

	resp, ok := parseCompletion(p, ctx)

	assert.True(t, ok, "should succeed")
	assert.NotNil(t, resp, "response should not be nil")
	assert.Len(t, 1, resp.Completions, "completions count")
	// "hello" + " there" + " world"
	assert.Equal(t, "hello there world", resp.Completions[0].Lines[0], "completion inserted at cursor")
}

func TestParseCompletion_MultiLineCompletion(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
	}
	p := NewProvider(config)

	ctx := &provider.StreamState{
		Input: inputFromRequest(&types.CompletionRequest{
			Lines:     []string{"func main() {"},
			CursorRow: 1,
			CursorCol: 13,
		}),
		Result: &openai.StreamResult{
			Text: "\n  fmt.Println()\n",
		},
	}

	resp, ok := parseCompletion(p, ctx)

	assert.True(t, ok, "should succeed")
	assert.Len(t, 1, resp.Completions, "completions count")
	assert.Equal(t, 3, len(resp.Completions[0].Lines), "should have 3 lines")
	assert.Equal(t, "func main() {", resp.Completions[0].Lines[0], "first line")
	assert.Equal(t, "  fmt.Println()", resp.Completions[0].Lines[1], "middle line")
}

func TestBuildPrompt_RepoContext(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
		FIMTokens: &types.FIMTokenConfig{
			Prefix:   "<PRE>",
			Suffix:   "<SUF>",
			Middle:   "<MID>",
			RepoName: "<|repo_name|>",
			FileSep:  "<|file_sep|>",
		},
	}
	p := NewProvider(config)

	ctx := &provider.StreamState{
		Input: sourcectx.CompletionInput{
			Current: sourcectx.CurrentSnapshot{
				Workspace: sourcectx.WorkspaceRef{Path: "/home/user/myproject"},
				File:      sourcectx.FileSnapshot{Path: "main.go"},
				Cursor:    sourcectx.CursorPosition{Col: 5},
			},
			Context: sourcectx.CollectedContext{
				sourcectx.RecentFiles{Files: []*types.RecentBufferSnapshot{
					{FilePath: "utils.go", Lines: []string{"package main", "", "func helper() {}"}},
				}},
				sourcectx.Diagnostics{Data: &types.Diagnostics{
					Items: []*types.Diagnostic{
						{Message: "undefined: foo", Severity: types.SeverityError, Source: "gopls", Range: &types.CursorRange{StartLine: 10}},
					},
				}},
				sourcectx.Treesitter{Data: &types.TreesitterContext{
					EnclosingSignature: "func main()",
					Siblings:           []*types.TreesitterSymbol{{Signature: "func helper()", Line: 5}},
					Imports:            []string{"import \"fmt\""},
				}},
			},
		},
		TrimmedLines: []string{"hello world"},
		CursorLine:   0,
	}

	req := buildPromptForTest(p, ctx)

	assert.True(t, strings.Contains(req.Prompt, "<|repo_name|>myproject\n"), "should have repo name")
	assert.True(t, strings.Contains(req.Prompt, "<|file_sep|>utils.go\n"), "should have recent file")
	assert.True(t, strings.Contains(req.Prompt, "package main"), "should have recent file content")
	assert.True(t, strings.Contains(req.Prompt, "<|file_sep|>context/diagnostics\n"), "should have diagnostics section")
	assert.True(t, strings.Contains(req.Prompt, "undefined: foo"), "should have diagnostic message")
	assert.True(t, strings.Contains(req.Prompt, "<|file_sep|>context/treesitter\n"), "should have treesitter section")
	assert.True(t, strings.Contains(req.Prompt, "Enclosing scope: func main()"), "should have enclosing scope")
	assert.True(t, strings.Contains(req.Prompt, "<|file_sep|>main.go\n"), "should have current file header")
	assert.True(t, strings.Contains(req.Prompt, "<PRE>hello<SUF> world<MID>"), "should have FIM tokens at end")
}

func TestBuildPrompt_NoRepoContextWithoutTokens(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
		FIMTokens: &types.FIMTokenConfig{
			Prefix: "<PRE>",
			Suffix: "<SUF>",
			Middle: "<MID>",
		},
	}
	p := NewProvider(config)

	ctx := &provider.StreamState{
		Input: sourcectx.CompletionInput{
			Current: sourcectx.CurrentSnapshot{
				Workspace: sourcectx.WorkspaceRef{Path: "/home/user/myproject"},
				File:      sourcectx.FileSnapshot{Path: "main.go"},
				Cursor:    sourcectx.CursorPosition{Col: 5},
			},
		},
		TrimmedLines: []string{"hello world"},
		CursorLine:   0,
	}

	req := buildPromptForTest(p, ctx)

	assert.False(t, strings.Contains(req.Prompt, "repo_name"), "should NOT have repo context")
	assert.False(t, strings.Contains(req.Prompt, "file_sep"), "should NOT have file_sep")
	assert.Equal(t, "<PRE>hello<SUF> world<MID>", req.Prompt, "should be plain FIM prompt")
}

func TestBuildPrompt_RepoContextStopTokens(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
		FIMTokens: &types.FIMTokenConfig{
			Prefix:   "<PRE>",
			Suffix:   "<SUF>",
			Middle:   "<MID>",
			RepoName: "<|repo_name|>",
			FileSep:  "<|file_sep|>",
		},
	}
	p := NewProvider(config)

	ctx := &provider.StreamState{
		Input: sourcectx.CompletionInput{
			Current: sourcectx.CurrentSnapshot{
				File:   sourcectx.FileSnapshot{Path: "main.go"},
				Cursor: sourcectx.CursorPosition{Col: 5},
			},
		},
		TrimmedLines: []string{"hello world"},
		CursorLine:   0,
	}

	req := buildPromptForTest(p, ctx)

	assert.True(t, containsStr(req.Stop, "<|file_sep|>"), "stop tokens should include file_sep")
	assert.True(t, containsStr(req.Stop, "<PRE>"), "stop tokens should include prefix")
}

func containsStr(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

func TestBuildPromptPromptSuffix_EmptyLines(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
		FIMTokens:     nil,
	}
	p := NewProvider(config)

	ctx := &provider.StreamState{
		Input:        sourcectx.CompletionInput{},
		TrimmedLines: []string{},
		CursorLine:   0,
	}

	req := buildPromptForTest(p, ctx)

	assert.Equal(t, "", req.Prompt, "empty prompt should be empty")
	assert.Equal(t, "", req.Suffix, "empty suffix should be empty")
	assert.Equal(t, 0, len(req.Stop), "stop should be empty in prompt+suffix mode")
}

func TestBuildPromptPromptSuffix_SingleLine(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
		FIMTokens:     nil,
	}
	p := NewProvider(config)

	ctx := &provider.StreamState{
		Input: sourcectx.CompletionInput{
			Current: sourcectx.CurrentSnapshot{
				Cursor: sourcectx.CursorPosition{Col: 5},
			},
		},
		TrimmedLines: []string{"hello world"},
		CursorLine:   0,
	}

	req := buildPromptForTest(p, ctx)

	assert.Equal(t, "hello", req.Prompt, "prompt should have text before cursor")
	assert.Equal(t, " world", req.Suffix, "suffix should have text after cursor")
	assert.Equal(t, 0, len(req.Stop), "stop should be empty in prompt+suffix mode")
}

func TestBuildPromptPromptSuffix_MultiLine(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
		FIMTokens:     nil,
	}
	p := NewProvider(config)

	ctx := &provider.StreamState{
		Input: sourcectx.CompletionInput{
			Current: sourcectx.CurrentSnapshot{
				Cursor: sourcectx.CursorPosition{Col: 4},
			},
		},
		TrimmedLines: []string{"line 1", "line 2", "line 3"},
		CursorLine:   1,
	}

	req := buildPromptForTest(p, ctx)

	assert.Equal(t, "line 1\nline", req.Prompt, "prompt should have lines before cursor")
	assert.Equal(t, " 2\nline 3", req.Suffix, "suffix should have lines after cursor")
	assert.Equal(t, 0, len(req.Stop), "stop should be empty in prompt+suffix mode")
}

func TestBuildPromptPromptSuffix_CursorBeyondLine(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
		FIMTokens:     nil,
	}
	p := NewProvider(config)

	ctx := &provider.StreamState{
		Input: sourcectx.CompletionInput{
			Current: sourcectx.CurrentSnapshot{
				Cursor: sourcectx.CursorPosition{Col: 100},
			},
		},
		TrimmedLines: []string{"short"},
		CursorLine:   0,
	}

	req := buildPromptForTest(p, ctx)

	assert.Equal(t, "short", req.Prompt, "prompt should have full line")
	assert.Equal(t, "", req.Suffix, "suffix should be empty when cursor beyond line")
}

func TestParseCompletion_SingleLineWithAfterCursor(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
	}
	p := NewProvider(config)

	ctx := &provider.StreamState{
		Input: inputFromRequest(&types.CompletionRequest{
			Lines:     []string{"func()"},
			CursorRow: 1,
			CursorCol: 4, // After "func"
		}),
		Result: &openai.StreamResult{
			Text: "tion",
		},
	}

	resp, ok := parseCompletion(p, ctx)

	assert.True(t, ok, "should succeed")
	assert.NotNil(t, resp, "response should not be nil")
	// "func" + "tion" + "()"
	assert.Equal(t, "function()", resp.Completions[0].Lines[0], "completion inserted at cursor with suffix")
}
