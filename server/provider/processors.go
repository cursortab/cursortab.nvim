package provider

import (
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

func RejectEmptyText(providerName, text string) (*types.CompletionResponse, bool) {
	if strings.TrimSpace(text) == "" {
		logger.Debug("%s: rejected, empty or whitespace-only", providerName)
		return EmptyResponse(), true
	}
	return nil, false
}

func StripRepetitionText(text string) (string, *types.CompletionResponse, bool) {
	lines := strings.Split(text, "\n")
	cutIdx := -1
	for i := 2; i < len(lines); i++ {
		if lines[i] == lines[i-1] && lines[i] == lines[i-2] && strings.TrimSpace(lines[i]) != "" {
			cutIdx = i - 2
			break
		}
	}
	if cutIdx < 0 {
		return text, nil, false
	}
	if cutIdx == 0 {
		return text, EmptyResponse(), true
	}
	return strings.Join(lines[:cutIdx], "\n"), nil, false
}

func AnchorTruncationText(providerName string, ctx *RequestState, text, finishReason string, stoppedEarly bool, threshold float64) (string, int, *types.CompletionResponse, bool) {
	if finishReason != "length" && !stoppedEarly {
		return text, 0, nil, false
	}

	if stoppedEarly {
		finishReason = "length"
	}

	newLines := strings.Split(text, "\n")
	originalLineCount := len(newLines)
	windowEnd := ctx.Window.Start + len(ctx.Window.Lines)
	oldLines := ctx.Input.Current.File.Lines[ctx.Window.Start:windowEnd]

	processedLines, endLineInc, shouldReject := handleTruncatedCompletionWithAnchor(
		newLines, oldLines, finishReason, ctx.Window.Start, windowEnd,
	)
	if shouldReject {
		logger.Debug("%s: rejected, truncation handling failed", providerName)
		return text, 0, EmptyResponse(), true
	}

	if len(oldLines) > minLinesForAnchorValidation {
		minAllowedLines := int(float64(len(oldLines)) * threshold)
		if len(processedLines) < minAllowedLines {
			logger.Debug("%s: rejected, too few lines (%d < %d min)",
				providerName, len(processedLines), minAllowedLines)
			return text, 0, EmptyResponse(), true
		}
	}

	logger.Info("%s: truncated, replacing lines %d-%d (%d -> %d lines)",
		providerName, ctx.Window.Start+1, endLineInc, originalLineCount, len(processedLines))
	return strings.Join(processedLines, "\n"), endLineInc, nil, false
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

func ValidateAnchorPositionText(providerName string, ctx *RequestState, text string, maxAnchorRatio float64) (*types.CompletionResponse, bool) {
	newLines := strings.Split(text, "\n")
	if len(newLines) == 0 {
		return nil, false
	}
	oldLines := ctx.Input.Current.File.Lines[ctx.Window.Start : ctx.Window.Start+len(ctx.Window.Lines)]
	anchorIdx, maxAllowed, reject := checkAnchorPosition(newLines[0], oldLines, maxAnchorRatio)
	if reject {
		logger.Debug("%s: rejected, first line anchors at %d (max allowed %d)",
			providerName, anchorIdx, maxAllowed)
		return EmptyResponse(), true
	}
	return nil, false
}

func FirstLineAnchorChecker(maxAnchorRatio float64) func(*RequestState, string) error {
	return func(ctx *RequestState, firstLine string) error {
		oldLines := ctx.Input.Current.File.Lines[ctx.Window.Start : ctx.Window.Start+len(ctx.Window.Lines)]
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
