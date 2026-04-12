// Package fim implements a fill-in-the-middle completion provider.
//
// Prompt format (sent as a single text prompt to /v1/completions):
//
//	<|fim_prefix|>...lines before cursor...
//	...text before cursor on current line...<|fim_suffix|>...text after cursor on current line...
//	...lines after cursor...<|fim_middle|>
//
// The FIM token names are configurable via FIMTokenConfig.
// The model fills in text between the prefix and suffix.
// Lines are trimmed to a window around the cursor via the TrimContent preprocessor.
package fim

import (
	"strings"

	"cursortab/client/openai"
	"cursortab/provider"
	"cursortab/types"
)

// NewProvider creates a new fill-in-the-middle completion provider
func NewProvider(config *types.ProviderConfig) *provider.Provider {
	return &provider.Provider{
		Name:          "fim",
		Config:        config,
		Client:        openai.NewClient(config.ProviderURL, config.CompletionPath, config.APIKey),
		StreamingType: provider.StreamingNone,
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
	prefixToken := p.Config.FIMTokens.Prefix
	suffixToken := p.Config.FIMTokens.Suffix
	middleToken := p.Config.FIMTokens.Middle
	var prompt string

	if len(ctx.TrimmedLines) == 0 {
		prompt = prefixToken + suffixToken + middleToken
	} else {
		var prefixBuilder strings.Builder
		var suffixBuilder strings.Builder

		for i := range ctx.CursorLine {
			prefixBuilder.WriteString(ctx.TrimmedLines[i])
			prefixBuilder.WriteString("\n")
		}

		if ctx.CursorLine < len(ctx.TrimmedLines) {
			currentLine := ctx.TrimmedLines[ctx.CursorLine]
			cursorCol := min(ctx.Request.CursorCol, len(currentLine))
			prefixBuilder.WriteString(currentLine[:cursorCol])
			suffixBuilder.WriteString(currentLine[cursorCol:])
		}

		for i := ctx.CursorLine + 1; i < len(ctx.TrimmedLines); i++ {
			suffixBuilder.WriteString("\n")
			suffixBuilder.WriteString(ctx.TrimmedLines[i])
		}

		prompt = prefixToken + prefixBuilder.String() + suffixToken + suffixBuilder.String() + middleToken
	}

	return &openai.CompletionRequest{
		Model:       p.Config.ProviderModel,
		Prompt:      prompt,
		Temperature: p.Config.ProviderTemperature,
		MaxTokens:   p.Config.ProviderMaxTokens,
		TopK:        p.Config.ProviderTopK,
		Stop:        []string{prefixToken, suffixToken, middleToken},
		N:           1,
		Echo:        false,
	}
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
