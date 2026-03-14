package utils

import "cursortab/types"

// Abs returns the absolute value of an integer
func Abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// SnapToSyntaxBoundaries expands a line region to align with syntax node boundaries
// when the expansion fits within the character budget. Ranges are ordered innermost
// to outermost. Returns 0-indexed start and end (inclusive).
func SnapToSyntaxBoundaries(lines []string, start, end int, maxChars int, syntaxRanges []*types.LineRange) (int, int) {
	if len(syntaxRanges) == 0 {
		return start, end
	}

	currentChars := countCharsInRange(lines, start, end)

	for _, sr := range syntaxRanges {
		// Convert 1-indexed to 0-indexed
		srStart := sr.StartLine - 1
		srEnd := sr.EndLine - 1

		// Clamp to valid range
		if srStart < 0 {
			srStart = 0
		}
		if srEnd >= len(lines) {
			srEnd = len(lines) - 1
		}

		// Skip ranges that don't extend beyond the current region
		if srStart >= start && srEnd <= end {
			continue
		}

		// Calculate cost of expanding to this boundary
		extraChars := 0
		if srStart < start {
			extraChars += countCharsInRange(lines, srStart, start-1)
		}
		if srEnd > end {
			extraChars += countCharsInRange(lines, end+1, srEnd)
		}

		if currentChars+extraChars <= maxChars {
			if srStart < start {
				start = srStart
			}
			if srEnd > end {
				end = srEnd
			}
			currentChars += extraChars
		} else {
			// Budget exceeded; stop trying larger boundaries
			break
		}
	}

	return start, end
}

func countCharsInRange(lines []string, start, end int) int {
	chars := 0
	for i := start; i <= end && i < len(lines); i++ {
		chars += len(lines[i]) + 1
	}
	return chars
}

// Token estimation constants
const (
	AvgCharsPerToken = 2 // Conservative estimate for mixed content (code + JSON)
)

// EstimateCharsFromTokens estimates the number of characters for a given token count
func EstimateCharsFromTokens(tokens int) int {
	return tokens * AvgCharsPerToken
}

// TrimContentAroundCursor trims the content to fit within maxTokens while preserving
// context around the cursor position. Returns the trimmed lines, adjusted cursor position,
// trim offset, and whether trimming occurred.
// An optional syntaxRanges parameter (1-indexed, innermost to outermost) causes the
// window boundaries to snap to AST node boundaries when they fit within budget.
func TrimContentAroundCursor(lines []string, cursorRow, cursorCol, maxTokens int, syntaxRanges []*types.LineRange) ([]string, int, int, int, bool) {
	// Handle empty file
	if len(lines) == 0 {
		return lines, 0, cursorCol, 0, false
	}

	// Clamp cursor to valid range
	if cursorRow < 0 {
		cursorRow = 0
	}
	if cursorRow >= len(lines) {
		cursorRow = len(lines) - 1
	}

	if maxTokens <= 0 {
		return lines, cursorRow, cursorCol, 0, false
	}

	maxChars := EstimateCharsFromTokens(maxTokens)

	// Calculate total content size
	totalChars := 0
	for _, line := range lines {
		totalChars += len(line) + 1 // +1 for newline
	}

	// If content is already within limits, return as-is
	if totalChars <= maxChars {
		return lines, cursorRow, cursorCol, 0, false
	}

	// Balanced approach: allocate half budget before cursor, half after
	// This ensures we see context both above AND below the cursor
	cursorLineChars := len(lines[cursorRow]) + 1
	remainingBudget := maxChars - cursorLineChars
	halfBudget := remainingBudget / 2

	// Expand BEFORE cursor (up to half budget)
	startLine := cursorRow
	charsBefore := 0
	for startLine > 0 && charsBefore < halfBudget {
		newChars := len(lines[startLine-1]) + 1
		if charsBefore+newChars <= halfBudget {
			startLine--
			charsBefore += newChars
		} else {
			break
		}
	}

	// Expand AFTER cursor (up to half budget + any unused from before)
	unusedBefore := halfBudget - charsBefore
	budgetAfter := halfBudget + unusedBefore
	endLine := cursorRow
	charsAfter := 0
	for endLine < len(lines)-1 && charsAfter < budgetAfter {
		newChars := len(lines[endLine+1]) + 1
		if charsAfter+newChars <= budgetAfter {
			endLine++
			charsAfter += newChars
		} else {
			break
		}
	}

	// If we have unused budget after expanding down, try expanding up more
	unusedAfter := budgetAfter - charsAfter
	if unusedAfter > 0 {
		for startLine > 0 {
			newChars := len(lines[startLine-1]) + 1
			if charsBefore+newChars <= halfBudget+unusedAfter {
				startLine--
				charsBefore += newChars
			} else {
				break
			}
		}
	}

	// Snap to syntax boundaries if available
	startLine, endLine = SnapToSyntaxBoundaries(lines, startLine, endLine, maxChars, syntaxRanges)

	// Extract the trimmed lines
	trimmedLines := make([]string, endLine-startLine+1)
	copy(trimmedLines, lines[startLine:endLine+1])

	// Adjust cursor position
	newCursorRow := cursorRow - startLine

	// Return trim offset (how many lines were removed from the start)
	trimOffset := startLine

	return trimmedLines, newCursorRow, cursorCol, trimOffset, true
}

// DiffEntry interface for token limiting - matches types.DiffEntry
type DiffEntry interface {
	GetOriginal() string
	GetUpdated() string
}

// TrimDiffEntries trims diff entries to fit within maxTokens.
// Keeps the most recent entries and removes older ones if over limit.
func TrimDiffEntries[T DiffEntry](diffs []T, maxTokens int) []T {
	if len(diffs) == 0 || maxTokens <= 0 {
		return diffs
	}

	maxChars := EstimateCharsFromTokens(maxTokens)

	// Iterate from newest (end) to oldest (start), keeping entries within limit
	totalChars := 0
	cutoffIndex := 0

	for i := len(diffs) - 1; i >= 0; i-- {
		entryChars := len(diffs[i].GetOriginal()) + len(diffs[i].GetUpdated())
		if totalChars+entryChars > maxChars && i < len(diffs)-1 {
			cutoffIndex = i + 1
			break
		}
		totalChars += entryChars
	}

	if cutoffIndex > 0 {
		return diffs[cutoffIndex:]
	}
	return diffs
}
