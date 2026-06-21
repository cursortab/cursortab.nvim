package provider

import (
	"cursortab/client/openai"
	"cursortab/logger"
	"cursortab/text"
	"cursortab/types"
	"errors"
	"fmt"
	"strings"
)

const (
	// AnchorSimilarityThreshold is the minimum similarity score required
	// to accept a line as an anchor match.
	AnchorSimilarityThreshold = 0.7

	// MinLinesForAnchorValidation is the minimum number of old lines required
	// before anchor position validation is applied.
	MinLinesForAnchorValidation = 10

	// AnchorSearchBefore is the number of lines to search before the expected
	// position when looking for anchor matches.
	AnchorSearchBefore = 2

	// AnchorSearchAfter is the number of lines to search after the expected
	// position when looking for anchor matches.
	AnchorSearchAfter = 5
)

// ErrSkipCompletion is a sentinel error for providers that skip completion
// without treating it as a failure.
var ErrSkipCompletion = errors.New("skip completion")

// --- Diff History Processors ---

// DiffHistoryOptions defines how diff history should be formatted
type DiffHistoryOptions struct {
	HeaderTemplate string // e.g. "User edited %q:\n"
	Prefix         string // e.g. "```diff\n"
	Suffix         string // e.g. "\n```"
	Separator      string // e.g. "\n\n"
}

// FormatDiffHistory returns a processor that formats diff history according to the specified options
func FormatDiffHistory(opts DiffHistoryOptions) DiffHistoryBuilder {
	return func(history []*types.FileDiffHistory) string {
		if len(history) == 0 {
			return ""
		}

		var builder strings.Builder
		firstEdit := true

		for _, fileHistory := range history {
			if len(fileHistory.DiffHistory) == 0 {
				continue
			}

			for _, diffEntry := range fileHistory.DiffHistory {
				unifiedDiff := DiffEntryToUnifiedDiff(diffEntry)
				if unifiedDiff == "" {
					continue
				}

				if !firstEdit && opts.Separator != "" {
					builder.WriteString(opts.Separator)
				}
				firstEdit = false

				if opts.HeaderTemplate != "" {
					fmt.Fprintf(&builder, opts.HeaderTemplate, fileHistory.FileName)
				}
				builder.WriteString(opts.Prefix)
				builder.WriteString(unifiedDiff)
				builder.WriteString(opts.Suffix)
			}
		}

		return builder.String()
	}
}

// FormatDiffHistoryOriginalUpdated returns a processor that formats diff history
// using simple original/updated sections instead of unified diff format.
func FormatDiffHistoryOriginalUpdated(headerTemplate string) DiffHistoryBuilder {
	return func(history []*types.FileDiffHistory) string {
		if len(history) == 0 {
			return ""
		}

		var builder strings.Builder

		for _, fileHistory := range history {
			if len(fileHistory.DiffHistory) == 0 {
				continue
			}

			for _, diffEntry := range fileHistory.DiffHistory {
				if diffEntry.Original == diffEntry.Updated {
					continue
				}

				if headerTemplate != "" {
					fmt.Fprintf(&builder, headerTemplate, fileHistory.FileName)
				}
				builder.WriteString("original:\n")
				builder.WriteString(diffEntry.Original)
				builder.WriteString("\nupdated:\n")
				builder.WriteString(diffEntry.Updated)
				builder.WriteString("\n")
			}
		}

		return builder.String()
	}
}

// DiffEntryToUnifiedDiff converts a DiffEntry to a unified diff format.
func DiffEntryToUnifiedDiff(entry *types.DiffEntry) string {
	if entry.Original == entry.Updated {
		return ""
	}

	originalLines := strings.Split(entry.Original, "\n")
	updatedLines := strings.Split(entry.Updated, "\n")

	var diffBuilder strings.Builder

	fmt.Fprintf(&diffBuilder, "@@ -%d,%d +%d,%d @@\n",
		1, len(originalLines), 1, len(updatedLines))

	for _, line := range originalLines {
		diffBuilder.WriteString("-")
		diffBuilder.WriteString(line)
		diffBuilder.WriteString("\n")
	}

	for _, line := range updatedLines {
		diffBuilder.WriteString("+")
		diffBuilder.WriteString(line)
		diffBuilder.WriteString("\n")
	}

	return strings.TrimSuffix(diffBuilder.String(), "\n")
}

func RejectEmptyBatch(p *Provider, result *openai.StreamResult) (*types.CompletionResponse, bool) {
	if strings.TrimSpace(result.Text) == "" {
		logger.Debug("%s: rejected, empty or whitespace-only", p.Name)
		return p.EmptyResponse(), true
	}
	return nil, false
}

func RejectTruncatedBatch(p *Provider, result *openai.StreamResult) (*types.CompletionResponse, bool) {
	if result.FinishReason == "length" {
		logger.Info("%s: rejected, truncated (finish_reason=length)", p.Name)
		return p.EmptyResponse(), true
	}
	return nil, false
}

func DropLastLineIfTruncatedBatch(p *Provider, ctx *BatchContext, result *openai.StreamResult) (*types.CompletionResponse, bool) {
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

	ctx.EndLineInc = ctx.WindowStart + len(lines)
	logger.Info("%s: truncated, dropped last line (%d -> %d lines)",
		p.Name, originalLineCount, len(lines))
	return nil, false
}

func StripRepetitionBatch(p *Provider, result *openai.StreamResult) (*types.CompletionResponse, bool) {
	lines := strings.Split(result.Text, "\n")
	cutIdx := -1
	for i := 2; i < len(lines); i++ {
		if lines[i] == lines[i-1] && lines[i] == lines[i-2] && strings.TrimSpace(lines[i]) != "" {
			cutIdx = i - 2
			break
		}
	}
	if cutIdx < 0 {
		return nil, false
	}
	if cutIdx == 0 {
		return p.EmptyResponse(), true
	}
	result.Text = strings.Join(lines[:cutIdx], "\n")
	return nil, false
}

func RejectLeadingNewlineWithSuffixBatch(p *Provider, ctx *BatchContext, result *openai.StreamResult) (*types.CompletionResponse, bool) {
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

func AnchorTruncationBatch(p *Provider, ctx *BatchContext, result *openai.StreamResult, threshold float64) (*types.CompletionResponse, bool) {
	if result.FinishReason != "length" && !result.StoppedEarly {
		return nil, false
	}

	finishReason := result.FinishReason
	if result.StoppedEarly {
		finishReason = "length"
	}

	newLines := strings.Split(result.Text, "\n")
	originalLineCount := len(newLines)
	oldLines := ctx.Input.Current.File.Lines[ctx.WindowStart:ctx.WindowEnd]

	processedLines, endLineInc, shouldReject := handleTruncatedCompletionWithAnchor(
		newLines, oldLines, finishReason, ctx.WindowStart, ctx.WindowEnd,
	)
	if shouldReject {
		logger.Debug("%s: rejected, truncation handling failed", p.Name)
		return p.EmptyResponse(), true
	}

	if len(oldLines) > MinLinesForAnchorValidation {
		minAllowedLines := int(float64(len(oldLines)) * threshold)
		if len(processedLines) < minAllowedLines {
			logger.Debug("%s: rejected, too few lines (%d < %d min)",
				p.Name, len(processedLines), minAllowedLines)
			return p.EmptyResponse(), true
		}
	}

	result.Text = strings.Join(processedLines, "\n")
	ctx.EndLineInc = endLineInc

	logger.Info("%s: truncated, replacing lines %d-%d (%d -> %d lines)",
		p.Name, ctx.WindowStart+1, endLineInc, originalLineCount, len(processedLines))
	return nil, false
}

// checkAnchorPosition validates that a first line anchors within acceptable range.
// Returns (anchorIdx, maxAllowed, shouldReject).
func checkAnchorPosition(firstLine string, oldLines []string, maxRatio float64) (int, int, bool) {
	if len(oldLines) <= MinLinesForAnchorValidation {
		return -1, 0, false
	}
	anchorIdx := findAnchorLineFullSearch(firstLine, oldLines)
	maxAllowed := int(float64(len(oldLines)) * maxRatio)
	return anchorIdx, maxAllowed, anchorIdx > maxAllowed
}

func ValidateAnchorPositionBatch(p *Provider, ctx *BatchContext, result *openai.StreamResult, maxAnchorRatio float64) (*types.CompletionResponse, bool) {
	newLines := strings.Split(result.Text, "\n")
	if len(newLines) == 0 {
		return nil, false
	}
	oldLines := ctx.Input.Current.File.Lines[ctx.WindowStart:ctx.WindowEnd]
	anchorIdx, maxAllowed, reject := checkAnchorPosition(newLines[0], oldLines, maxAnchorRatio)
	if reject {
		logger.Debug("%s: rejected, first line anchors at %d (max allowed %d)",
			p.Name, anchorIdx, maxAllowed)
		return p.EmptyResponse(), true
	}
	return nil, false
}

// ValidateFirstLineAnchor returns a validator that checks the first streamed line anchors correctly.
// This is the streaming equivalent of ValidateAnchorPosition.
func ValidateFirstLineAnchor(maxAnchorRatio float64) Validator {
	return func(p *Provider, ctx *StreamState, firstLine string) error {
		oldLines := ctx.Input.Current.File.Lines[ctx.WindowStart:ctx.WindowEnd]
		_, _, reject := checkAnchorPosition(firstLine, oldLines, maxAnchorRatio)
		if reject {
			return errors.New("first line anchor position too far from start")
		}
		return nil
	}
}

// --- Helper functions ---

// findAnchorLine searches for the best matching line in oldLines for the given needle.
// Searches in a window around expectedPos to handle structural changes (adds/removes).
// Returns the index in oldLines or -1 if no good match found.
func findAnchorLine(needle string, oldLines []string, expectedPos int) int {
	if len(oldLines) == 0 {
		return -1
	}

	bestIdx := -1
	bestSimilarity := AnchorSimilarityThreshold

	searchStart := max(0, expectedPos-AnchorSearchBefore)
	searchEnd := min(len(oldLines), expectedPos+AnchorSearchAfter)

	for i := searchStart; i < searchEnd; i++ {
		similarity := text.LineSimilarity(needle, oldLines[i])
		if similarity > bestSimilarity {
			bestSimilarity = similarity
			bestIdx = i
		}
	}

	return bestIdx
}

// findAnchorLineFullSearch searches the entire oldLines array for the best matching line.
// Used for validation to detect if model output is misaligned with expected window.
// Returns the index in oldLines or -1 if no good match found.
func findAnchorLineFullSearch(needle string, oldLines []string) int {
	if len(oldLines) == 0 {
		return -1
	}

	bestIdx := -1
	bestSimilarity := AnchorSimilarityThreshold

	for i, line := range oldLines {
		similarity := text.LineSimilarity(needle, line)
		if similarity > bestSimilarity {
			bestSimilarity = similarity
			bestIdx = i
		}
	}

	return bestIdx
}

// handleTruncatedCompletionWithAnchor processes completion lines when the model hits max_tokens,
// using anchor matching to find the correct replacement range.
func handleTruncatedCompletionWithAnchor(
	newLines []string,
	oldLines []string,
	finishReason string,
	windowStart, windowEnd int,
) ([]string, int, bool) {
	endLineInc := windowEnd

	if finishReason == "length" && len(newLines) > 0 {
		newLines = newLines[:len(newLines)-1]

		if len(newLines) == 0 {
			return nil, 0, true
		}

		lastModelLine := newLines[len(newLines)-1]
		expectedPos := len(newLines) - 1
		anchorIdx := findAnchorLine(lastModelLine, oldLines, expectedPos)

		if anchorIdx != -1 {
			endLineInc = windowStart + anchorIdx + 1
		} else {
			endLineInc = windowStart + len(newLines)
		}
	}

	return newLines, endLineInc, false
}

// FormatDiagnosticsText renders diagnostics as plain text lines:
//
//	line 10: [ERROR] undefined: foo (source: gopls)
//	[WARNING] unused variable bar (source: gopls)
//
// Used by zeta, zeta2, and mercuryapi providers. Returns empty string
// if there are no diagnostics.
func FormatDiagnosticsText(diag *types.Diagnostics) string {
	if diag == nil || len(diag.Items) == 0 {
		return ""
	}

	var b strings.Builder
	for _, d := range diag.Items {
		if d.Range != nil {
			fmt.Fprintf(&b, "line %d: ", d.Range.StartLine)
		}
		fmt.Fprintf(&b, "[%s] %s", d.Severity, d.Message)
		if d.Source != "" {
			fmt.Fprintf(&b, " (source: %s)", d.Source)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// IsNoOpReplacement checks if replacing oldLines with newLines would result in no change.
func IsNoOpReplacement(newLines, oldLines []string) bool {
	newText := strings.TrimRight(strings.Join(newLines, "\n"), " \t\n\r")
	oldText := strings.TrimRight(strings.Join(oldLines, "\n"), " \t\n\r")
	return newText == oldText
}
