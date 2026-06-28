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

func stateForLines(lines []string, cursorRow int, cursorCol int, config *types.ProviderConfig) *provider.RequestState {
	return stateForInput(completionInput(lines, cursorRow, cursorCol))
}

func stateForInput(input sourcectx.CompletionInput) *provider.RequestState {
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

func parseCompletion(p *Provider, ctx *provider.RequestState, result *openai.CompletionResult) *types.CompletionResponse {
	resp, err := p.Parse(ctx, result)
	if err != nil {
		panic(err)
	}
	return resp
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

	ctx := stateForLines(nil, 1, 0, config)

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

	ctx := stateForLines([]string{"hello world"}, 1, 5, config)

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

	ctx := stateForLines([]string{"line 1", "line 2", "line 3"}, 2, 4, config)

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

	ctx := stateForLines([]string{"short"}, 1, 100, config)

	req := buildPromptForTest(p, ctx)

	assert.True(t, strings.Contains(req.Prompt, "<PRE>short<SUF><MID>"), "should handle cursor beyond line")
}

func TestParseCompletion_SingleLine(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
	}
	p := NewProvider(config)

	ctx := stateForLines([]string{"hello world"}, 1, 5, config)

	resp := parseCompletion(p, ctx, &openai.CompletionResult{Text: " there"})
	assert.NotNil(t, resp, "response should not be nil")
	assert.NotNil(t, resp.Completion, "completions count")
	// "hello" + " there" + " world"
	assert.Equal(t, "hello there world", resp.Completion.Lines[0], "completion inserted at cursor")
}

func TestParseCompletion_MultiLineCompletion(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
	}
	p := NewProvider(config)

	ctx := stateForLines([]string{"func main() {"}, 1, 13, config)

	resp := parseCompletion(p, ctx, &openai.CompletionResult{Text: "\n  fmt.Println()\n"})
	assert.NotNil(t, resp.Completion, "completions count")
	assert.Equal(t, 3, len(resp.Completion.Lines), "should have 3 lines")
	assert.Equal(t, "func main() {", resp.Completion.Lines[0], "first line")
	assert.Equal(t, "  fmt.Println()", resp.Completion.Lines[1], "middle line")
}

func TestParseCompletion_DropsTruncatedLastLine(t *testing.T) {
	p := NewProvider(&types.ProviderConfig{ProviderModel: "test-model"})
	ctx := stateForLines([]string{"hello world"}, 1, 5, &types.ProviderConfig{ProviderModel: "test-model"})

	resp := parseCompletion(p, ctx, &openai.CompletionResult{
		Text:         " there\nincomplete",
		FinishReason: "length",
	})
	assert.NotNil(t, resp, "response should not be nil")
	assert.NotNil(t, resp.Completion, "completions count")
	assert.Equal(t, []string{"hello there world"}, resp.Completion.Lines, "completion lines")
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

	input := completionInput([]string{"hello world"}, 1, 5)
	input.Current.WorkspacePath = "/home/user/myproject"
	input.Current.File.Path = "main.go"
	input.Materials = sourcectx.Materials{
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
	}
	ctx := stateForInput(input)

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

	input := completionInput([]string{"hello world"}, 1, 5)
	input.Current.WorkspacePath = "/home/user/myproject"
	input.Current.File.Path = "main.go"
	ctx := stateForInput(input)

	req := buildPromptForTest(p, ctx)

	assert.False(t, strings.Contains(req.Prompt, "repo_name"), "should NOT have repo context")
	assert.False(t, strings.Contains(req.Prompt, "file_sep"), "should NOT have file_sep")
	assert.Equal(t, "<PRE>hello<SUF> world<MID>", req.Prompt, "should be plain FIM prompt")
}

func TestBuildPrompt_RepoContextStops(t *testing.T) {
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

	input := completionInput([]string{"hello world"}, 1, 5)
	input.Current.File.Path = "main.go"
	ctx := stateForInput(input)

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

	ctx := stateForLines(nil, 1, 0, config)

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

	ctx := stateForLines([]string{"hello world"}, 1, 5, config)

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

	ctx := stateForLines([]string{"line 1", "line 2", "line 3"}, 2, 4, config)

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

	ctx := stateForLines([]string{"short"}, 1, 100, config)

	req := buildPromptForTest(p, ctx)

	assert.Equal(t, "short", req.Prompt, "prompt should have full line")
	assert.Equal(t, "", req.Suffix, "suffix should be empty when cursor beyond line")
}

func TestParseCompletion_SingleLineWithAfterCursor(t *testing.T) {
	config := &types.ProviderConfig{
		ProviderModel: "test-model",
	}
	p := NewProvider(config)

	ctx := stateForLines([]string{"func()"}, 1, 4, config)

	resp := parseCompletion(p, ctx, &openai.CompletionResult{Text: "tion"})
	assert.NotNil(t, resp, "response should not be nil")
	// "func" + "tion" + "()"
	assert.Equal(t, "function()", resp.Completion.Lines[0], "completion inserted at cursor with suffix")
}
