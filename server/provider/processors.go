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
	anchorSimilarityThreshold   = 0.7
	minLinesForAnchorValidation = 10
	anchorSearchBefore          = 2
	anchorSearchAfter           = 5
)

type DiffHistoryOptions struct {
	HeaderTemplate string
	Prefix         string
	Suffix         string
	Separator      string
}

func FormatDiffHistory(history []*types.FileDiffHistory, opts DiffHistoryOptions) string {
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

func RejectEmptyResult(p *Provider, result *openai.StreamResult) (*types.CompletionResponse, bool) {
	if strings.TrimSpace(result.Text) == "" {
		logger.Debug("%s: rejected, empty or whitespace-only", p.Name)
		return p.EmptyResponse(), true
	}
	return nil, false
}

func StripRepetitionResult(p *Provider, result *openai.StreamResult) (*types.CompletionResponse, bool) {
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

func AnchorTruncationResult(p *Provider, ctx *RequestState, result *openai.StreamResult, threshold float64) (int, *types.CompletionResponse, bool) {
	if result.FinishReason != "length" && !result.StoppedEarly {
		return 0, nil, false
	}

	finishReason := result.FinishReason
	if result.StoppedEarly {
		finishReason = "length"
	}

	newLines := strings.Split(result.Text, "\n")
	originalLineCount := len(newLines)
	windowEnd := ctx.WindowStart + len(ctx.TrimmedLines)
	oldLines := ctx.Input.Current.File.Lines[ctx.WindowStart:windowEnd]

	processedLines, endLineInc, shouldReject := handleTruncatedCompletionWithAnchor(
		newLines, oldLines, finishReason, ctx.WindowStart, windowEnd,
	)
	if shouldReject {
		logger.Debug("%s: rejected, truncation handling failed", p.Name)
		return 0, p.EmptyResponse(), true
	}

	if len(oldLines) > minLinesForAnchorValidation {
		minAllowedLines := int(float64(len(oldLines)) * threshold)
		if len(processedLines) < minAllowedLines {
			logger.Debug("%s: rejected, too few lines (%d < %d min)",
				p.Name, len(processedLines), minAllowedLines)
			return 0, p.EmptyResponse(), true
		}
	}

	result.Text = strings.Join(processedLines, "\n")

	logger.Info("%s: truncated, replacing lines %d-%d (%d -> %d lines)",
		p.Name, ctx.WindowStart+1, endLineInc, originalLineCount, len(processedLines))
	return endLineInc, nil, false
}

// checkAnchorPosition validates that a first line anchors within acceptable range.
// Returns (anchorIdx, maxAllowed, shouldReject).
func checkAnchorPosition(firstLine string, oldLines []string, maxRatio float64) (int, int, bool) {
	if len(oldLines) <= minLinesForAnchorValidation {
		return -1, 0, false
	}
	anchorIdx := findAnchorLineFullSearch(firstLine, oldLines)
	maxAllowed := int(float64(len(oldLines)) * maxRatio)
	return anchorIdx, maxAllowed, anchorIdx > maxAllowed
}

func ValidateAnchorPositionResult(p *Provider, ctx *RequestState, result *openai.StreamResult, maxAnchorRatio float64) (*types.CompletionResponse, bool) {
	newLines := strings.Split(result.Text, "\n")
	if len(newLines) == 0 {
		return nil, false
	}
	oldLines := ctx.Input.Current.File.Lines[ctx.WindowStart : ctx.WindowStart+len(ctx.TrimmedLines)]
	anchorIdx, maxAllowed, reject := checkAnchorPosition(newLines[0], oldLines, maxAnchorRatio)
	if reject {
		logger.Debug("%s: rejected, first line anchors at %d (max allowed %d)",
			p.Name, anchorIdx, maxAllowed)
		return p.EmptyResponse(), true
	}
	return nil, false
}

// FirstLineAnchorValidator returns a validator that checks the first streamed line anchors correctly.
// This is the streaming equivalent of ValidateAnchorPosition.
func FirstLineAnchorValidator(maxAnchorRatio float64) firstLineValidator {
	return func(p *Provider, ctx *StreamState, firstLine string) error {
		oldLines := ctx.Input.Current.File.Lines[ctx.WindowStart : ctx.WindowStart+len(ctx.TrimmedLines)]
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
	bestSimilarity := anchorSimilarityThreshold

	searchStart := max(0, expectedPos-anchorSearchBefore)
	searchEnd := min(len(oldLines), expectedPos+anchorSearchAfter)

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
	bestSimilarity := anchorSimilarityThreshold

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

func isNoOpReplacement(newLines, oldLines []string) bool {
	newText := strings.TrimRight(strings.Join(newLines, "\n"), " \t\n\r")
	oldText := strings.TrimRight(strings.Join(oldLines, "\n"), " \t\n\r")
	return newText == oldText
}
