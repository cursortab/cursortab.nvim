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
// Lines are trimmed to a window around the cursor via the TrimContent preprocessor.
package fim

import (
	"fmt"
	"path/filepath"
	"strings"

	"cursortab/client/openai"
	"cursortab/provider"
	"cursortab/types"
)

// NewProvider creates a new fill-in-the-middle completion provider
func NewProvider(config *types.ProviderConfig) *provider.Provider {
	p := &provider.Provider{
		Name:                          "fim",
		Config:                        config,
		Client:                        openai.NewClient(config.ProviderURL, config.CompletionPath, config.APIKey),
		StreamingType:                 provider.StreamingNone,
		CompleteWithTextRightOfCursor: true,
		Preprocessors: []provider.Preprocessor{
			provider.TrimContent(),
			setStreamContext(),
		},
		PromptBuilder: buildPrompt,
		Postprocessors: []provider.Postprocessor{
			provider.RejectEmpty(),
			provider.StripRepetition(),
			provider.DropLastLineIfTruncated(),
			provider.RejectLeadingNewlineWithSuffix(),
			parseCompletion,
		},
	}

	if config.FIMTokens != nil && config.FIMTokens.FileSep != "" {
		p.DiffBuilder = provider.FormatDiffHistory(provider.DiffHistoryOptions{
			HeaderTemplate: config.FIMTokens.FileSep + "%s.diff\n",
			Prefix:         "",
			Suffix:         "",
			Separator:      "",
		})
	}

	return p
}

// setStreamContext configures streaming diff context for FIM.
// The streamed lines are raw middle-fill text; this tells the engine how to
// transform them into full replacement lines and what old lines to diff against.
func setStreamContext() provider.Preprocessor {
	return func(p *provider.Provider, ctx *provider.Context) error {
		if len(ctx.TrimmedLines) == 0 || ctx.CursorLine >= len(ctx.TrimmedLines) {
			return nil
		}

		currentLine := ctx.TrimmedLines[ctx.CursorLine]
		cursorCol := min(ctx.Request.CursorCol, len(currentLine))

		ctx.StreamOldLines = ctx.TrimmedLines[ctx.CursorLine:]
		ctx.StreamBaseOff = ctx.WindowStart + ctx.CursorLine
		ctx.FirstLinePfx = currentLine[:cursorCol]
		ctx.LastLineSfx = currentLine[cursorCol:]
		return nil
	}
}

func buildPrompt(p *provider.Provider, ctx *provider.Context) *openai.CompletionRequest {
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
			cursorCol := min(ctx.Request.CursorCol, len(currentLine))
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
		return &openai.CompletionRequest{
			Model:       p.Config.ProviderModel,
			Prompt:      prefixContent.String(),
			Suffix:      suffixContent.String(),
			Temperature: p.Config.ProviderTemperature,
			MaxTokens:   p.Config.ProviderMaxTokens,
			TopK:        p.Config.ProviderTopK,
			N:           1,
			Echo:        false,
		}
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

	return &openai.CompletionRequest{
		Model:       p.Config.ProviderModel,
		Prompt:      prompt.String(),
		Temperature: p.Config.ProviderTemperature,
		MaxTokens:   p.Config.ProviderMaxTokens,
		TopK:        p.Config.ProviderTopK,
		Stop:        stop,
		N:           1,
		Echo:        false,
	}
}

// buildRepoContext prepends cross-file context using repo-level FIM tokens.
func buildRepoContext(b *strings.Builder, p *provider.Provider, ctx *provider.Context) {
	req := ctx.Request
	fileSep := p.Config.FIMTokens.FileSep
	repoName := p.Config.FIMTokens.RepoName

	// Repo name header
	workspace := filepath.Base(req.WorkspacePath)
	if workspace == "" || workspace == "." {
		workspace = "repo"
	}
	b.WriteString(repoName)
	b.WriteString(workspace)
	b.WriteString("\n")

	// Recent files
	for _, snap := range req.RecentBufferSnapshots {
		b.WriteString(fileSep)
		b.WriteString(snap.FilePath)
		b.WriteString("\n")
		b.WriteString(strings.Join(snap.Lines, "\n"))
		b.WriteString("\n")
	}

	// Diagnostics
	if diagText := provider.FormatDiagnosticsText(req.GetDiagnostics()); diagText != "" {
		b.WriteString(fileSep)
		b.WriteString("context/diagnostics\n")
		b.WriteString(diagText)
	}

	// Treesitter context
	if ts := req.GetTreesitter(); ts != nil {
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
	if p.DiffBuilder != nil {
		if diffSection := p.DiffBuilder(req.FileDiffHistories); diffSection != "" {
			b.WriteString(diffSection)
		}
	}

	// Git diff (staged changes)
	if gd := req.GetGitDiff(); gd != nil && gd.Diff != "" {
		b.WriteString(fileSep)
		b.WriteString("context/staged_diff\n")
		b.WriteString(gd.Diff)
		b.WriteString("\n")
	}

	// Current file header
	b.WriteString(fileSep)
	b.WriteString(req.FilePath)
	b.WriteString("\n")
}

func parseCompletion(p *provider.Provider, ctx *provider.Context) (*types.CompletionResponse, bool) {
	completionText := ctx.Result.Text
	req := ctx.Request

	currentLine := ""
	if req.CursorRow >= 1 && req.CursorRow <= len(req.Lines) {
		currentLine = req.Lines[req.CursorRow-1]
	}
	cursorCol := min(req.CursorCol, len(currentLine))

	// Build the suffix text (everything after cursor in the file) so we can
	// detect when the model just regenerates it.
	afterCursor := currentLine[cursorCol:]
	var suffixBuilder strings.Builder
	suffixBuilder.WriteString(afterCursor)
	for i := req.CursorRow; i < len(req.Lines); i++ {
		suffixBuilder.WriteString("\n")
		suffixBuilder.WriteString(req.Lines[i])
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
	return p.BuildCompletion(ctx, req.CursorRow, req.CursorRow, resultLines)
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
