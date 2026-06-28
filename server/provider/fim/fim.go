// Package fim implements fill-in-the-middle completion.
package fim

import (
	"context"
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

const providerName = "fim"

type Provider struct {
	provider.Base
	provider.OpenAI
}

var _ engine.Provider = (*Provider)(nil)
var _ provider.CompletionFlow[*openai.CompletionRequest, *openai.CompletionResult] = (*Provider)(nil)

func NewProvider(config *types.ProviderConfig) *Provider {
	materials := sourcectx.Materials{sourcectx.Treesitter{}}
	if config.FIMTokens != nil && config.FIMTokens.FileSep != "" {
		materials = append(materials,
			sourcectx.Diagnostics{}, sourcectx.GitDiff{},
			sourcectx.RecentFiles{}, sourcectx.EditHistory{},
		)
	}

	return &Provider{
		Base:   provider.NewBase(engine.CompletionFIM, materials, provider.SyntheticPrefetchDisabled),
		OpenAI: provider.NewOpenAI(providerName, config),
	}
}

func (p *Provider) Complete(ctx context.Context, input sourcectx.CompletionInput) (*types.CompletionResponse, error) {
	return provider.StartBatch(ctx, input, p.ProviderConfig(), p)
}

func (p *Provider) Build(ctx *provider.RequestState) (*openai.CompletionRequest, error) {
	// Build prefix and suffix content (common to both modes)
	var prefixContent strings.Builder
	var suffixContent strings.Builder

	if len(ctx.Window.Lines) > 0 {
		for i := range ctx.Window.CursorLine {
			prefixContent.WriteString(ctx.Window.Lines[i])
			prefixContent.WriteString("\n")
		}

		if ctx.Window.CursorLine < len(ctx.Window.Lines) {
			currentLine := ctx.Window.Lines[ctx.Window.CursorLine]
			cursorCol := min(ctx.Input.Current.Cursor.Col, len(currentLine))
			prefixContent.WriteString(currentLine[:cursorCol])
			suffixContent.WriteString(currentLine[cursorCol:])
		}

		for i := ctx.Window.CursorLine + 1; i < len(ctx.Window.Lines); i++ {
			suffixContent.WriteString("\n")
			suffixContent.WriteString(ctx.Window.Lines[i])
		}
	}

	tokens := p.ProviderConfig().FIMTokens

	// Prompt+suffix mode (OpenAI completions API style): fim_tokens not configured
	if tokens == nil {
		req := p.Request(prefixContent.String(), nil)
		req.Suffix = suffixContent.String()
		p.LogRequest(req, ctx.Window.MaxLines)
		return req, nil
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

	req := p.Request(prompt.String(), stop)
	p.LogRequest(req, ctx.Window.MaxLines)
	return req, nil
}

// buildRepoContext prepends cross-file context using repo-level FIM tokens.
func buildRepoContext(b *strings.Builder, p *Provider, ctx *provider.RequestState) {
	input := ctx.Input
	current := input.Current
	fileSep := p.ProviderConfig().FIMTokens.FileSep
	repoName := p.ProviderConfig().FIMTokens.RepoName

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

func (p *Provider) Parse(ctx *provider.RequestState, result *openai.CompletionResult) (*types.CompletionResponse, error) {
	text := result.Text
	if resp, done := provider.RejectEmptyText(providerName, text); done {
		return resp, nil
	}
	if stripped, resp, done := provider.StripRepetitionText(text); done {
		return resp, nil
	} else {
		text = stripped
	}
	if trimmed, resp, done := dropLastLineIfTruncatedText(text, result.FinishReason, result.StoppedEarly); done {
		return resp, nil
	} else {
		text = trimmed
	}
	if resp, done := rejectLeadingNewlineWithSuffixText(ctx, text); done {
		return resp, nil
	}

	completionText := text
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
	return provider.BuildCompletion(ctx, current.Cursor.Row, current.Cursor.Row, resultLines), nil
}

func dropLastLineIfTruncatedText(text, finishReason string, stoppedEarly bool) (string, *types.CompletionResponse, bool) {
	if finishReason != "length" && !stoppedEarly {
		return text, nil, false
	}

	lines := strings.Split(text, "\n")
	originalLineCount := len(lines)
	if len(lines) <= 1 {
		logger.Info("fim: rejected, truncated single line")
		return text, provider.EmptyResponse(), true
	}

	lines = lines[:len(lines)-1]
	text = strings.Join(lines, "\n")
	if strings.TrimSpace(text) == "" {
		logger.Info("fim: rejected, empty after dropping truncated line")
		return text, provider.EmptyResponse(), true
	}

	logger.Info("%s: truncated, dropped last line (%d -> %d lines)",
		"fim", originalLineCount, len(lines))
	return text, nil, false
}

func rejectLeadingNewlineWithSuffixText(ctx *provider.RequestState, text string) (*types.CompletionResponse, bool) {
	current := ctx.Input.Current
	if current.Cursor.Row < 1 || current.Cursor.Row > len(current.File.Lines) {
		return nil, false
	}

	currentLine := current.File.Lines[current.Cursor.Row-1]
	cursorCol := min(current.Cursor.Col, len(currentLine))
	atEOL := cursorCol >= len(strings.TrimRight(currentLine, " \t"))
	if !atEOL || !strings.HasPrefix(text, "\n") {
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

	return provider.EmptyResponse(), true
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
