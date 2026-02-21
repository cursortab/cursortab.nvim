package text

import (
	"cursortab/types"
	"sort"
	"strings"
)

// Stage represents a single stage of changes to apply
type Stage struct {
	BufferStart  int                           // 1-indexed buffer coordinate
	BufferEnd    int                           // 1-indexed, inclusive
	Lines        []string                      // New content for this stage
	Changes      map[int]LineChange            // Changes keyed by line num relative to stage
	Groups       []*Group                      // Pre-computed groups for rendering
	CursorLine   int                           // Cursor position (1-indexed, relative to stage)
	CursorCol    int                           // Cursor column (0-indexed)
	CursorTarget *types.CursorPredictionTarget // Navigation target
	IsLastStage  bool

	// Unexported fields for construction (not serialized)
	rawChanges map[int]LineChange // Original changes with absolute line nums
	startLine  int                // First change line (absolute, 1-indexed)
	endLine    int                // Last change line (absolute, 1-indexed)
}

// StagingResult contains the result of CreateStages
type StagingResult struct {
	Stages               []*Stage
	FirstNeedsNavigation bool
}

// StagedCompletion holds the queue of pending stages
type StagedCompletion struct {
	Stages           []*Stage
	CurrentIdx       int
	SourcePath       string
	CumulativeOffset int // Tracks line count drift after each stage accept (for unequal line counts)
}

// StagingParams holds all parameters for CreateStages
type StagingParams struct {
	Diff               *DiffResult
	CursorRow          int // 1-indexed buffer coordinate
	CursorCol          int // 0-indexed
	ViewportTop        int // 1-indexed buffer coordinate
	ViewportBottom     int // 1-indexed buffer coordinate
	BaseLineOffset     int // Where the diff range starts in the buffer (1-indexed)
	ProximityThreshold int // Max gap between changes to be in same stage
	MaxLines           int // Max lines per stage (0 to disable)
	FilePath           string
	NewLines           []string // New content lines for extracting stage content
	OldLines           []string // Old content lines for extracting old content in groups
}

// CreateStages is the main entry point for creating stages from a diff result.
// Always returns stages (at least 1 stage for non-empty changes).
func CreateStages(p *StagingParams) *StagingResult {
	diff := p.Diff
	if len(diff.Changes) == 0 {
		return nil
	}

	// Step 1: Partition changes by viewport visibility
	var inViewChanges, outViewChanges []int
	for lineNum, change := range diff.Changes {
		bufferLine := diff.LineMapping.GetBufferLine(change, lineNum, p.BaseLineOffset)

		isVisible := p.ViewportTop == 0 && p.ViewportBottom == 0 ||
			(bufferLine >= p.ViewportTop && bufferLine <= p.ViewportBottom)

		if isVisible {
			inViewChanges = append(inViewChanges, lineNum)
		} else {
			outViewChanges = append(outViewChanges, lineNum)
		}
	}

	sort.Ints(inViewChanges)
	sort.Ints(outViewChanges)

	// Step 2: Group changes into partial stages
	inViewStages := groupChangesIntoStages(diff, inViewChanges, p.ProximityThreshold, p.MaxLines, p.BaseLineOffset)
	outViewStages := groupChangesIntoStages(diff, outViewChanges, p.ProximityThreshold, p.MaxLines, p.BaseLineOffset)
	allStages := append(inViewStages, outViewStages...)

	if len(allStages) == 0 {
		return nil
	}

	// Step 3: Sort stages by cursor distance
	sort.SliceStable(allStages, func(i, j int) bool {
		distI := stageDistanceFromCursor(allStages[i], p.CursorRow)
		distJ := stageDistanceFromCursor(allStages[j], p.CursorRow)
		if distI != distJ {
			return distI < distJ
		}
		return allStages[i].startLine < allStages[j].startLine
	})

	// Step 4: Finalize stages (content, cursor targets)
	finalizeStages(allStages, p.NewLines, p.FilePath, p.BaseLineOffset, diff, p.CursorRow, p.CursorCol)

	// Step 5: Check if first stage needs navigation UI
	firstNeedsNav := StageNeedsNavigation(
		allStages[0], p.CursorRow, p.ViewportTop, p.ViewportBottom, p.ProximityThreshold,
	)

	return &StagingResult{
		Stages:               allStages,
		FirstNeedsNavigation: firstNeedsNav,
	}
}

// groupChangesIntoStages groups sorted line numbers into partial Stage structs based on proximity
// and stage line limits. The returned stages have rawChanges, startLine, endLine, BufferStart, and BufferEnd
// populated. Other fields are left as zero values to be filled by finalizeStages.
func groupChangesIntoStages(diff *DiffResult, lineNumbers []int, proximityThreshold int, maxLines int, baseLineOffset int) []*Stage {
	if len(lineNumbers) == 0 {
		return nil
	}

	var stages []*Stage
	var currentStage *Stage

	for _, lineNum := range lineNumbers {
		change := diff.Changes[lineNum]
		endLine := lineNum

		if currentStage == nil {
			currentStage = &Stage{
				startLine:  lineNum,
				endLine:    endLine,
				rawChanges: make(map[int]LineChange),
			}
			currentStage.rawChanges[lineNum] = change
		} else {
			gap := lineNum - currentStage.endLine
			// Check both proximity threshold and stage line limit
			stageLineCount := currentStage.endLine - currentStage.startLine + 1
			exceedsMaxLines := maxLines > 0 && stageLineCount >= maxLines
			if gap <= proximityThreshold && !exceedsMaxLines {
				currentStage.rawChanges[lineNum] = change
				if endLine > currentStage.endLine {
					currentStage.endLine = endLine
				}
			} else {
				// Compute buffer range before appending
				currentStage.BufferStart, currentStage.BufferEnd = getStageBufferRange(currentStage, baseLineOffset, diff, nil)
				stages = append(stages, currentStage)
				currentStage = &Stage{
					startLine:  lineNum,
					endLine:    endLine,
					rawChanges: make(map[int]LineChange),
				}
				currentStage.rawChanges[lineNum] = change
			}
		}
	}

	if currentStage != nil {
		currentStage.BufferStart, currentStage.BufferEnd = getStageBufferRange(currentStage, baseLineOffset, diff, nil)
		stages = append(stages, currentStage)
	}

	return stages
}

// StageNeedsNavigation determines if a stage requires cursor prediction UI.
// Returns true if the stage is outside viewport or far from cursor.
func StageNeedsNavigation(stage *Stage, cursorRow, viewportTop, viewportBottom, distThreshold int) bool {
	// Check distance first - if within threshold, no navigation needed.
	// This handles cases like additions at end of file where BufferStart may be
	// beyond the viewport but the stage is still close to the cursor.
	distance := stageDistanceFromCursor(stage, cursorRow)
	if distance <= distThreshold {
		return false
	}

	// Check viewport bounds for stages that are far from cursor
	if viewportTop > 0 && viewportBottom > 0 {
		entirelyOutside := stage.BufferEnd < viewportTop || stage.BufferStart > viewportBottom
		if entirelyOutside {
			return true
		}
	}

	return true // distance > distThreshold
}

// stageDistanceFromCursor calculates the minimum distance from cursor to a stage.
func stageDistanceFromCursor(stage *Stage, cursorRow int) int {
	if cursorRow >= stage.BufferStart && cursorRow <= stage.BufferEnd {
		return 0
	}
	if cursorRow < stage.BufferStart {
		return stage.BufferStart - cursorRow
	}
	return cursorRow - stage.BufferEnd
}

// getStageBufferRange determines the buffer line range for a stage using coordinate mapping.
// If bufferLines is non-nil, it will be populated with lineNum -> bufferLine mappings.
func getStageBufferRange(stage *Stage, baseLineOffset int, diff *DiffResult, bufferLines map[int]int) (int, int) {
	minOldLine := -1
	maxOldLine := -1
	minOldLineFromNonAdditions := -1
	hasAdditions := false
	hasNonAdditions := false
	maxNewLineNum := 0

	for lineNum, change := range stage.rawChanges {
		bufferLine := diff.LineMapping.GetBufferLine(change, lineNum, baseLineOffset)
		if bufferLines != nil {
			bufferLines[lineNum] = bufferLine
		}

		isAddition := change.Type == ChangeAddition

		if isAddition {
			hasAdditions = true
			if change.NewLineNum > maxNewLineNum {
				maxNewLineNum = change.NewLineNum
			}
			hasValidAnchor := change.OldLineNum > 0 ||
				(diff.LineMapping != nil && change.NewLineNum > 0)
			if hasValidAnchor {
				if minOldLine == -1 || bufferLine < minOldLine {
					minOldLine = bufferLine
				}
			}
		} else {
			hasNonAdditions = true
			if minOldLineFromNonAdditions == -1 || bufferLine < minOldLineFromNonAdditions {
				minOldLineFromNonAdditions = bufferLine
			}
			if minOldLine == -1 || bufferLine < minOldLine {
				minOldLine = bufferLine
			}
			if bufferLine > maxOldLine {
				maxOldLine = bufferLine
			}
		}
	}

	if hasAdditions && hasNonAdditions && minOldLineFromNonAdditions > 0 {
		minOldLine = minOldLineFromNonAdditions
	}

	if hasAdditions && diff.OldLineCount > 0 {
		lastOldLineInRange := baseLineOffset + diff.OldLineCount - 1
		if maxNewLineNum > diff.OldLineCount && maxOldLine < lastOldLineInRange {
			maxOldLine = lastOldLineInRange
		}
	}

	if minOldLine == -1 {
		minOldLine = stage.startLine + baseLineOffset - 1
	}
	if maxOldLine == -1 {
		if hasAdditions && !hasNonAdditions {
			maxOldLine = minOldLine
		} else {
			maxOldLine = stage.endLine + baseLineOffset - 1
		}
	}

	return minOldLine, maxOldLine
}

// getStageNewLineRange determines the new line range for content extraction.
func getStageNewLineRange(stage *Stage) (int, int) {
	minNewLine := -1
	maxNewLine := -1

	for _, change := range stage.rawChanges {
		if change.NewLineNum > 0 {
			if minNewLine == -1 || change.NewLineNum < minNewLine {
				minNewLine = change.NewLineNum
			}
			if change.NewLineNum > maxNewLine {
				maxNewLine = change.NewLineNum
			}
		}
	}

	if minNewLine == -1 {
		minNewLine = stage.startLine
	}
	if maxNewLine == -1 {
		maxNewLine = stage.endLine
	}

	return minNewLine, maxNewLine
}

// finalizeStages populates the remaining fields of partial stages.
// It extracts content, remaps changes to relative line numbers, computes groups,
// and sets cursor targets based on sort order.
func finalizeStages(stages []*Stage, newLines []string, filePath string, baseLineOffset int, diff *DiffResult, cursorRow, cursorCol int) {
	for i, stage := range stages {
		isLastStage := i == len(stages)-1

		// Get buffer line mappings for this stage
		lineNumToBufferLine := make(map[int]int)
		getStageBufferRange(stage, baseLineOffset, diff, lineNumToBufferLine)

		// Get new line range for content extraction
		newStartLine, newEndLine := getStageNewLineRange(stage)

		// Extract the new content using new coordinates
		var stageLines []string
		for j := newStartLine; j <= newEndLine && j-1 < len(newLines); j++ {
			if j > 0 {
				stageLines = append(stageLines, newLines[j-1])
			}
		}

		// Extract old content for modifications and create remapped changes
		stageOldLines := make([]string, len(stageLines))
		remappedChanges := make(map[int]LineChange)
		relativeToBufferLine := make(map[int]int)
		for lineNum, change := range stage.rawChanges {
			newLineNum := lineNum
			if change.NewLineNum > 0 {
				newLineNum = change.NewLineNum
			}
			relativeLine := newLineNum - newStartLine + 1
			relativeIdx := newLineNum - newStartLine

			if relativeIdx >= 0 && relativeIdx < len(stageOldLines) {
				stageOldLines[relativeIdx] = change.OldContent
			}

			if relativeLine > 0 && (relativeLine <= len(stageLines) || change.Type == ChangeDeletion) {
				// Use pre-computed buffer line from getStageBufferRange
				relativeToBufferLine[relativeLine] = lineNumToBufferLine[lineNum]

				remappedChange := change
				remappedChange.NewLineNum = relativeLine
				remappedChanges[relativeLine] = remappedChange
			}
		}

		// Compute groups, set BufferLine, validate render hints, and compute cursor position
		ctx := &StageContext{
			BufferStart:         stage.BufferStart,
			CursorRow:           cursorRow,
			CursorCol:           cursorCol,
			LineNumToBufferLine: relativeToBufferLine,
		}
		groups, targetCursorLine, targetCursorCol := FinalizeStageGroups(remappedChanges, stageLines, ctx)

		// Create cursor target
		var cursorTarget *types.CursorPredictionTarget
		if isLastStage {
			// For last stage, cursor target points to end of NEW content,
			// not the old buffer end. This is important when additions extend
			// beyond the original buffer.
			newEndLine := stage.BufferStart + len(stageLines) - 1
			cursorTarget = &types.CursorPredictionTarget{
				RelativePath:    filePath,
				LineNumber:      int32(newEndLine),
				ShouldRetrigger: true,
			}
		} else {
			nextStage := stages[i+1]
			cursorTarget = &types.CursorPredictionTarget{
				RelativePath:    filePath,
				LineNumber:      int32(nextStage.BufferStart),
				ShouldRetrigger: false,
			}
		}

		// Populate the stage's exported fields
		stage.Lines = stageLines
		stage.Changes = remappedChanges
		stage.Groups = groups
		stage.CursorLine = targetCursorLine
		stage.CursorCol = targetCursorCol
		stage.CursorTarget = cursorTarget
		stage.IsLastStage = isLastStage

		// Clear rawChanges (no longer needed)
		stage.rawChanges = nil
	}
}

// JoinLines joins a slice of strings with newlines.
// Each line gets a trailing \n, which is the standard line terminator format
// that diffmatchpatch expects. This ensures proper line counting:
// - ["a", "b"] → "a\nb\n" (2 lines)
// - ["a", ""] → "a\n\n" (2 lines, second is empty)
func JoinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	var b strings.Builder
	for _, line := range lines {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}
