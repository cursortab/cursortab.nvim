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
			provider.DropLastLineIfTruncated(),
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

	completionLines := strings.Split(completionText, "\n")

	beforeCursor := currentLine[:cursorCol]
	afterCursor := currentLine[cursorCol:]

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
