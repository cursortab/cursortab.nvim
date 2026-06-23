// Package fim implements a fill-in-the-middle completion provider.
//
// Two modes are supported:
//
// 1. Tokenized FIM (FIMTokens is non-nil). The provider concatenates content
// and delimiter tokens into a single `prompt` string:
//
//	<|fim_prefix|>...lines before cursor...
//	...text before cursor on current line...<|fim_suffix|>...text after cursor on current line...
//	...lines after cursor...<|fim_middle|>
//
// When repo-level tokens (repo_name, file_sep) are configured, cross-file
// context is prepended before the FIM tokens:
//
//	<|repo_name|>workspace
//	<|file_sep|>path/to/other.go
//	...recent file contents...
//	<|file_sep|>context/diagnostics
//	...LSP diagnostics...
//	<|file_sep|>context/treesitter
//	...scope context...
//	<|file_sep|>context/staged_diff
//	...git diff...
//	<|file_sep|>path/to/current.go
//	<|fim_prefix|>...prefix...<|fim_suffix|>...suffix...<|fim_middle|>
//
// 2. Prompt+suffix (FIMTokens is nil). The provider sends the text before the
// cursor as `prompt` and the text after as `suffix` on the OpenAI completions
// API (e.g. DeepSeek). No cross-file context is added in this mode.
//
// Lines are trimmed to a window around the cursor before the prompt is rendered.
package fim

import (
	"fmt"
	"path/filepath"
	"strings"

	"cursortab/client/openai"
	sourcectx "cursortab/ctx"
	"cursortab/engine"
	"cursortab/logger"
	"cursortab/provider"
	"cursortab/types"
)

// NewProvider creates a new fill-in-the-middle completion provider
func NewProvider(config *types.ProviderConfig) *provider.Provider {
	p := &provider.Provider{
		Name:         "fim",
		Config:       config,
		Client:       openai.NewClient(config.ProviderURL, config.CompletionPath, config.APIKey),
		Completion:   engine.CompletionFIM,
		Materials:    sourcectx.Materials{sourcectx.Treesitter{}},
		BuildRequest: buildRequest,
		ParseResult:  parseResult,
	}

	if config.FIMTokens != nil && config.FIMTokens.FileSep != "" {
		p.Materials = append(p.Materials,
			sourcectx.Diagnostics{}, sourcectx.GitDiff{},
			sourcectx.RecentFiles{}, sourcectx.EditHistory{},
		)
	}

	return p
}

func buildRequest(p *provider.Provider, ctx *provider.RequestState) provider.PreparedRequest {
	// Build prefix and suffix content (common to both modes)
	var prefixContent strings.Builder
	var suffixContent strings.Builder

	if len(ctx.TrimmedLines) > 0 {
		for i := range ctx.CursorLine {
			prefixContent.WriteString(ctx.TrimmedLines[i])
			prefixContent.WriteString("\n")
		}

		if ctx.CursorLine < len(ctx.TrimmedLines) {
			currentLine := ctx.TrimmedLines[ctx.CursorLine]
			cursorCol := min(ctx.Input.Current.Cursor.Col, len(currentLine))
			prefixContent.WriteString(currentLine[:cursorCol])
			suffixContent.WriteString(currentLine[cursorCol:])
		}

		for i := ctx.CursorLine + 1; i < len(ctx.TrimmedLines); i++ {
			suffixContent.WriteString("\n")
			suffixContent.WriteString(ctx.TrimmedLines[i])
		}
	}

	tokens := p.Config.FIMTokens

	// Prompt+suffix mode (OpenAI completions API style): fim_tokens not configured
	if tokens == nil {
		return provider.PreparedRequest{Completion: &openai.CompletionRequest{
			Model:       p.Config.ProviderModel,
			Prompt:      prefixContent.String(),
			Suffix:      suffixContent.String(),
			Temperature: p.Config.ProviderTemperature,
			MaxTokens:   p.Config.ProviderMaxTokens,
			TopK:        p.Config.ProviderTopK,
			N:           1,
			Echo:        false,
		}}
	}

	// Tokenized FIM mode: concatenate tokens into a single prompt
	var prompt strings.Builder

	// Repo-level cross-file context (when repo_name and file_sep are configured)
	if tokens.RepoName != "" && tokens.FileSep != "" {
		buildRepoContext(&prompt, p, ctx)
	}

	prompt.WriteString(tokens.Prefix)
	prompt.WriteString(prefixContent.String())
	prompt.WriteString(tokens.Suffix)
	prompt.WriteString(suffixContent.String())
	prompt.WriteString(tokens.Middle)

	stop := []string{tokens.Prefix, tokens.Suffix, tokens.Middle}
	if tokens.FileSep != "" {
		stop = append(stop, tokens.FileSep)
	}

	return provider.PreparedRequest{Completion: &openai.CompletionRequest{
		Model:       p.Config.ProviderModel,
		Prompt:      prompt.String(),
		Temperature: p.Config.ProviderTemperature,
		MaxTokens:   p.Config.ProviderMaxTokens,
		TopK:        p.Config.ProviderTopK,
		Stop:        stop,
		N:           1,
		Echo:        false,
	}}
}

// buildRepoContext prepends cross-file context using repo-level FIM tokens.
func buildRepoContext(b *strings.Builder, p *provider.Provider, ctx *provider.RequestState) {
	input := ctx.Input
	current := input.Current
	fileSep := p.Config.FIMTokens.FileSep
	repoName := p.Config.FIMTokens.RepoName

	// Repo name header
	workspace := filepath.Base(current.WorkspacePath)
	if workspace == "" || workspace == "." {
		workspace = "repo"
	}
	b.WriteString(repoName)
	b.WriteString(workspace)
	b.WriteString("\n")

	// Recent files
	if recent, ok := sourcectx.Find[sourcectx.RecentFiles](input.Materials); ok {
		for _, snap := range recent.Files {
			b.WriteString(fileSep)
			b.WriteString(snap.FilePath)
			b.WriteString("\n")
			b.WriteString(strings.Join(snap.Lines, "\n"))
			b.WriteString("\n")
		}
	}

	// Diagnostics
	if diagnostics, ok := sourcectx.Find[sourcectx.Diagnostics](input.Materials); ok {
		if diagText := provider.FormatDiagnosticsText(diagnostics.Data); diagText != "" {
			b.WriteString(fileSep)
			b.WriteString("context/diagnostics\n")
			b.WriteString(diagText)
		}
	}

	// Treesitter context
	if treesitter, ok := sourcectx.Find[sourcectx.Treesitter](input.Materials); ok && treesitter.Data != nil {
		ts := treesitter.Data
		hasContent := ts.EnclosingSignature != "" || len(ts.Siblings) > 0 || len(ts.Imports) > 0
		if hasContent {
			b.WriteString(fileSep)
			b.WriteString("context/treesitter\n")
			if ts.EnclosingSignature != "" {
				fmt.Fprintf(b, "Enclosing scope: %s\n", ts.EnclosingSignature)
			}
			for _, s := range ts.Siblings {
				fmt.Fprintf(b, "Sibling: %s\n", s.Signature)
			}
			for _, imp := range ts.Imports {
				fmt.Fprintf(b, "Import: %s\n", imp)
			}
		}
	}

	// Diff history
	if editHistory, ok := sourcectx.Find[sourcectx.EditHistory](input.Materials); ok {
		if diffSection := provider.FormatDiffHistory(editHistory.Files, provider.DiffHistoryOptions{
			HeaderTemplate: fileSep + "%s.diff\n",
		}); diffSection != "" {
			b.WriteString(diffSection)
		}
	}

	// Git diff (staged changes)
	if gitDiff, ok := sourcectx.Find[sourcectx.GitDiff](input.Materials); ok && gitDiff.Data != nil && gitDiff.Data.Diff != "" {
		b.WriteString(fileSep)
		b.WriteString("context/staged_diff\n")
		b.WriteString(gitDiff.Data.Diff)
		b.WriteString("\n")
	}

	// Current file header
	b.WriteString(fileSep)
	b.WriteString(current.File.Path)
	b.WriteString("\n")
}

func parseResult(p *provider.Provider, ctx *provider.RequestState, result *openai.StreamResult) *types.CompletionResponse {
	if resp, done := provider.RejectEmptyResult(p, result); done {
		return resp
	}
	if resp, done := provider.StripRepetitionResult(p, result); done {
		return resp
	}
	if resp, done := dropLastLineIfTruncatedResult(p, result); done {
		return resp
	}
	if resp, done := rejectLeadingNewlineWithSuffixResult(p, ctx, result); done {
		return resp
	}

	completionText := result.Text
	current := ctx.Input.Current

	currentLine := ""
	if current.Cursor.Row >= 1 && current.Cursor.Row <= len(current.File.Lines) {
		currentLine = current.File.Lines[current.Cursor.Row-1]
	}
	cursorCol := min(current.Cursor.Col, len(currentLine))

	// Build the suffix text (everything after cursor in the file) so we can
	// detect when the model just regenerates it.
	afterCursor := currentLine[cursorCol:]
	var suffixBuilder strings.Builder
	suffixBuilder.WriteString(afterCursor)
	for i := current.Cursor.Row; i < len(current.File.Lines); i++ {
		suffixBuilder.WriteString("\n")
		suffixBuilder.WriteString(current.File.Lines[i])
	}
	suffix := suffixBuilder.String()

	// Strip suffix overlap: if the completion ends with text that matches
	// the beginning of the suffix, trim it. FIM models commonly regenerate
	// the suffix verbatim when there's nothing to insert.
	completionText = stripSuffixOverlap(completionText, suffix)
	completionLines := strings.Split(completionText, "\n")

	beforeCursor := currentLine[:cursorCol]

	resultLines := make([]string, len(completionLines))
	resultLines[0] = beforeCursor + completionLines[0]

	for i := 1; i < len(completionLines); i++ {
		resultLines[i] = completionLines[i]
	}

	// Append afterCursor (suffix text like ")") to the appropriate line.
	// When the first completion line has content (model continues the cursor line),
	// the suffix belongs on the first line (e.g., "len(arr)").
	// When it's empty (model starts with \n), the suffix belongs on the last line
	// (e.g., multi-line bracket fill).
	if completionLines[0] != "" {
		resultLines[0] += afterCursor
	} else {
		resultLines[len(resultLines)-1] += afterCursor
	}

	// FIM inserts content at cursor position - always replace only the current line
	return p.BuildCompletion(ctx, current.Cursor.Row, current.Cursor.Row, resultLines)
}

func dropLastLineIfTruncatedResult(p *provider.Provider, result *openai.StreamResult) (*types.CompletionResponse, bool) {
	if result.FinishReason != "length" && !result.StoppedEarly {
		return nil, false
	}

	lines := strings.Split(result.Text, "\n")
	originalLineCount := len(lines)
	if len(lines) <= 1 {
		logger.Info("%s: rejected, truncated single line", p.Name)
		return p.EmptyResponse(), true
	}

	lines = lines[:len(lines)-1]
	result.Text = strings.Join(lines, "\n")
	if strings.TrimSpace(result.Text) == "" {
		logger.Info("%s: rejected, empty after dropping truncated line", p.Name)
		return p.EmptyResponse(), true
	}

	logger.Info("%s: truncated, dropped last line (%d -> %d lines)",
		p.Name, originalLineCount, len(lines))
	return nil, false
}

func rejectLeadingNewlineWithSuffixResult(p *provider.Provider, ctx *provider.RequestState, result *openai.StreamResult) (*types.CompletionResponse, bool) {
	current := ctx.Input.Current
	if current.Cursor.Row < 1 || current.Cursor.Row > len(current.File.Lines) {
		return nil, false
	}

	currentLine := current.File.Lines[current.Cursor.Row-1]
	cursorCol := min(current.Cursor.Col, len(currentLine))
	atEOL := cursorCol >= len(strings.TrimRight(currentLine, " \t"))
	if !atEOL || !strings.HasPrefix(result.Text, "\n") {
		return nil, false
	}

	afterCursor := currentLine[cursorCol:]
	var suffixBuilder strings.Builder
	suffixBuilder.WriteString(afterCursor)
	for i := current.Cursor.Row; i < len(current.File.Lines); i++ {
		suffixBuilder.WriteString("\n")
		suffixBuilder.WriteString(current.File.Lines[i])
	}
	if strings.TrimSpace(suffixBuilder.String()) == "" {
		return nil, false
	}

	return p.EmptyResponse(), true
}

// stripSuffixOverlap removes the longest suffix of completion that matches a
// prefix of the file suffix. This catches the common FIM no-op pattern where
// the model regenerates text that already exists after the cursor.
func stripSuffixOverlap(completion, suffix string) string {
	if completion == "" || suffix == "" {
		return completion
	}
	// Find the longest k such that completion[len-k:] == suffix[:k].
	maxK := min(len(completion), len(suffix))
	best := 0
	for k := 1; k <= maxK; k++ {
		if completion[len(completion)-k:] == suffix[:k] {
			best = k
		}
	}
	if best > 0 {
		return completion[:len(completion)-best]
	}
	return completion
}
