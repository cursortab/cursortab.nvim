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
	rawChanges   []LineChange // Original changes with absolute line nums
	startLine    int          // First change line (absolute, 1-indexed)
	endLine      int          // Last change line (absolute, 1-indexed)
	newLineStart int          // First new-file line whose content belongs to this stage
	newLineEnd   int          // Last new-file line whose content belongs to this stage
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

	// Step 1: Compute buffer line for each change and partition by viewport
	var inView, outView []indexedChange
	for _, change := range diff.Changes {
		bufferLine := diff.LineMapping.GetBufferLine(change, p.BaseLineOffset)
		isVisible := p.ViewportTop == 0 && p.ViewportBottom == 0 ||
			(bufferLine >= p.ViewportTop && bufferLine <= p.ViewportBottom)
		ic := indexedChange{change, bufferLine}
		if isVisible {
			inView = append(inView, ic)
		} else {
			outView = append(outView, ic)
		}
	}

	sortByBufferLine := func(s []indexedChange) {
		sort.SliceStable(s, func(i, j int) bool {
			if s[i].bufferLine != s[j].bufferLine {
				return s[i].bufferLine < s[j].bufferLine
			}
			return s[i].change.MapKey() < s[j].change.MapKey()
		})
	}
	sortByBufferLine(inView)
	sortByBufferLine(outView)

	// Step 2: Group changes into partial stages
	inViewStages := groupChangesIntoStages(inView, p.ProximityThreshold, p.MaxLines, p.BaseLineOffset, diff)
	outViewStages := groupChangesIntoStages(outView, p.ProximityThreshold, p.MaxLines, p.BaseLineOffset, diff)
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
	finalizeStages(allStages, p.NewLines, p.OldLines, p.FilePath, p.BaseLineOffset, diff, p.CursorRow, p.CursorCol)

	// Step 5: Check if first stage needs navigation UI
	firstNeedsNav := StageNeedsNavigation(
		allStages[0], p.CursorRow, p.ViewportTop, p.ViewportBottom, p.ProximityThreshold,
	)

	return &StagingResult{
		Stages:               allStages,
		FirstNeedsNavigation: firstNeedsNav,
	}
}

// indexedChange pairs a change with its pre-computed buffer line.
type indexedChange struct {
	change     LineChange
	bufferLine int
}

// groupChangesIntoStages groups sorted changes into partial Stage structs based on
// buffer-line proximity and stage line limits.
func groupChangesIntoStages(changes []indexedChange, proximityThreshold int, maxLines int, baseLineOffset int, diff *DiffResult) []*Stage {
	if len(changes) == 0 {
		return nil
	}

	var stages []*Stage
	var currentStage *Stage
	lastBufferLine := 0

	for _, ic := range changes {
		mapKey := ic.change.MapKey()

		if currentStage == nil {
			currentStage = &Stage{
				startLine:  mapKey,
				endLine:    mapKey,
				rawChanges: []LineChange{ic.change},
			}
			lastBufferLine = ic.bufferLine
		} else {
			gap := ic.bufferLine - lastBufferLine
			if gap < 0 {
				gap = -gap
			}
			stageLineCount := currentStage.endLine - currentStage.startLine + 1
			exceedsMaxLines := maxLines > 0 && stageLineCount >= maxLines
			if gap <= proximityThreshold && !exceedsMaxLines {
				currentStage.rawChanges = append(currentStage.rawChanges, ic.change)
				if mapKey > currentStage.endLine {
					currentStage.endLine = mapKey
				}
				lastBufferLine = ic.bufferLine
			} else {
				computeStageRanges(currentStage, baseLineOffset, diff, nil)
				stages = append(stages, currentStage)
				currentStage = &Stage{
					startLine:  mapKey,
					endLine:    mapKey,
					rawChanges: []LineChange{ic.change},
				}
				lastBufferLine = ic.bufferLine
			}
		}
	}

	if currentStage != nil {
		computeStageRanges(currentStage, baseLineOffset, diff, nil)
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

// computeStageRanges sets BufferStart, BufferEnd, newLineStart, and newLineEnd on the
// stage in a single pass. It also optionally populates a bufferLines map (mapKey →
// buffer line) used later for UI group positioning.
//
// Buffer range (old-file space):
//   - Modifications/deletions contribute their OldLineNum directly.
//   - Additions contribute their anchor (OldLineNum of the preceding old line).
//   - Anchorless additions (OldLineNum=-1) are resolved via a forward walk of NewToOld.
//
// New-file range:
//   - Seeded from the NewLineNum of every non-deletion change.
//   - Expanded to include unchanged old lines in [oldStart, oldEnd] that map to new
//     lines via OldToNew (skipped for pure same-anchor-addition stages, which insert
//     without replacing any existing content).
func computeStageRanges(stage *Stage, baseLineOffset int, diff *DiffResult, bufferLines map[int]int) {
	// Populate buffer line mappings for UI group positioning
	if bufferLines != nil {
		for _, change := range stage.rawChanges {
			bufferLines[change.MapKey()] = diff.LineMapping.GetBufferLine(change, baseLineOffset)
		}
	}

	minOldNonAdd := -1
	maxOldNonAdd := -1
	minAnchor := -1
	maxAnchor := -1
	hasNonAdditions := false
	hasAdditions := false
	newStart := 0
	newEnd := 0

	for _, change := range stage.rawChanges {
		// Track new-line range from every non-deletion change
		if change.Type != ChangeDeletion && change.NewLineNum > 0 {
			if newStart == 0 || change.NewLineNum < newStart {
				newStart = change.NewLineNum
			}
			if change.NewLineNum > newEnd {
				newEnd = change.NewLineNum
			}
		}

		if change.Type == ChangeAddition {
			hasAdditions = true
			if change.OldLineNum > 0 {
				if minAnchor == -1 || change.OldLineNum < minAnchor {
					minAnchor = change.OldLineNum
				}
				if change.OldLineNum > maxAnchor {
					maxAnchor = change.OldLineNum
				}
			}
		} else {
			hasNonAdditions = true
			if change.OldLineNum > 0 {
				if minOldNonAdd == -1 || change.OldLineNum < minOldNonAdd {
					minOldNonAdd = change.OldLineNum
				}
				if change.OldLineNum > maxOldNonAdd {
					maxOldNonAdd = change.OldLineNum
				}
			}
		}
	}

	// Compute old-file range (buffer range)
	var oldStart, oldEnd int

	if !hasNonAdditions && hasAdditions {
		// Pure additions: anchor is the old line before insertion
		if minAnchor == maxAnchor {
			// All anchored at the same line → pure insertion point
			oldStart = minAnchor + 1
			oldEnd = minAnchor + 1
		} else {
			// Different anchors → must replace old lines between them
			oldStart = minAnchor + 1
			oldEnd = maxAnchor
		}
	} else if !hasAdditions {
		// No additions: range covers modified/deleted old lines
		oldStart = minOldNonAdd
		oldEnd = maxOldNonAdd
	} else {
		// Mixed: start with non-addition range, extend for addition anchors
		oldStart = minOldNonAdd
		oldEnd = maxOldNonAdd
		for _, change := range stage.rawChanges {
			if change.Type == ChangeAddition && change.OldLineNum > 0 {
				if change.OldLineNum >= oldEnd {
					oldEnd = change.OldLineNum
				}
				if change.OldLineNum+1 < oldStart {
					oldStart = change.OldLineNum + 1
				}
			}
		}
	}

	// For anchorless additions (OldLineNum=-1), derive the insertion
	// point from the LineMapping using the change's NewLineNum.
	if oldStart <= 0 && diff.LineMapping != nil {
		for _, change := range stage.rawChanges {
			if change.NewLineNum > 0 && change.NewLineNum <= len(diff.LineMapping.NewToOld) {
				// Walk forward from NewLineNum to find the next mapped old line
				for i := change.NewLineNum - 1; i < len(diff.LineMapping.NewToOld); i++ {
					if diff.LineMapping.NewToOld[i] > 0 {
						pos := diff.LineMapping.NewToOld[i]
						if oldStart <= 0 || pos < oldStart {
							oldStart = pos
						}
						break
					}
				}
				// If no forward match, insertion is past last old line
				if oldStart <= 0 {
					oldStart = len(diff.LineMapping.OldToNew) + 1
				}
			}
		}
	}
	if oldEnd <= 0 {
		oldEnd = oldStart
	}
	if oldStart <= 0 {
		oldStart = stage.startLine
	}
	if oldEnd <= 0 {
		oldEnd = stage.endLine
	}

	stage.BufferStart = oldStart + baseLineOffset - 1
	stage.BufferEnd = oldEnd + baseLineOffset - 1

	// Expand new-line range to include unchanged old lines that fall within
	// [oldStart, oldEnd] and map to new lines. These must be included in
	// Stage.Lines because SetBufferLines replaces the entire old range.
	// Skip this for pure same-anchor-addition stages — they insert without
	// replacing, so only the added lines are needed.
	isPureSameAnchorInsert := !hasNonAdditions && hasAdditions && minAnchor == maxAnchor
	if !isPureSameAnchorInsert && diff.LineMapping != nil {
		for oldRel := oldStart; oldRel <= oldEnd && oldRel-1 < len(diff.LineMapping.OldToNew); oldRel++ {
			if oldRel < 1 {
				continue
			}
			newLine := diff.LineMapping.OldToNew[oldRel-1]
			if newLine > 0 {
				if newStart == 0 || newLine < newStart {
					newStart = newLine
				}
				if newLine > newEnd {
					newEnd = newLine
				}
			}
		}
	}

	if newStart == 0 {
		// Pure deletion stage — no new content
		stage.newLineStart = 1
		stage.newLineEnd = 0
	} else {
		stage.newLineStart = newStart
		stage.newLineEnd = newEnd
	}
}

// finalizeStages populates the remaining fields of partial stages.
// It extracts content, remaps changes to relative line numbers, computes groups,
// and sets cursor targets based on sort order.
func finalizeStages(stages []*Stage, newLines []string, oldLines []string, filePath string, baseLineOffset int, diff *DiffResult, cursorRow, cursorCol int) {
	for i, stage := range stages {
		isLastStage := i == len(stages)-1

		// Populate buffer line mappings for UI group positioning
		lineNumToBufferLine := make(map[int]int)
		computeStageRanges(stage, baseLineOffset, diff, lineNumToBufferLine)

		// Convert a single addition on the cursor line to a modification (append_chars).
		// When the cursor sits on a whitespace-only line and the diff produces a
		// pure addition (because the old content matched elsewhere as equal), we
		// want inline ghost text rather than a virtual line.
		if len(stage.rawChanges) == 1 {
			change := stage.rawChanges[0]
			mapKey := change.MapKey()
			if change.Type == ChangeAddition {
				bufLine := lineNumToBufferLine[mapKey]
				oldIdx := bufLine - baseLineOffset // 0-indexed into oldLines
				if bufLine == cursorRow && oldIdx >= 0 && oldIdx < len(oldLines) {
					oldContent := oldLines[oldIdx]
					if oldContent != "" && strings.TrimSpace(oldContent) == "" {
						changeType, colStart, colEnd := categorizeLineChangeWithColumns(oldContent, change.Content)
						stage.rawChanges[0] = LineChange{
							Type:       changeType,
							OldLineNum: oldIdx + 1,
							NewLineNum: change.NewLineNum,
							Content:    change.Content,
							OldContent: oldContent,
							ColStart:   colStart,
							ColEnd:     colEnd,
						}
						computeStageRanges(stage, baseLineOffset, diff, lineNumToBufferLine)
					}
				}
			}
		}

		// Extract the new content using the pre-computed new-line range
		var stageLines []string
		for j := stage.newLineStart; j <= stage.newLineEnd && j-1 < len(newLines); j++ {
			if j > 0 {
				stageLines = append(stageLines, newLines[j-1])
			}
		}

		// Extract old content for modifications and create remapped changes
		stageOldLines := make([]string, len(stageLines))
		remappedChanges := make(map[int]LineChange)
		relativeToBufferLine := make(map[int]int)

		// Deletions use old-coordinate space; non-deletions use new-coordinate space.
		// When both are present, shift all deletions beyond the non-deletion range so
		// their relative lines never overlap. Non-deletions occupy [1..nCount] and
		// deletions occupy [nCount+1..], preserving inter-deletion gaps for grouping.
		hasNonDeletions := false
		for _, change := range stage.rawChanges {
			if change.Type != ChangeDeletion {
				hasNonDeletions = true
				break
			}
		}
		nCount := 0
		if hasNonDeletions {
			nCount = len(stageLines)
		}

		for _, change := range stage.rawChanges {
			mapKey := change.MapKey()
			var relativeLine int
			if change.Type == ChangeDeletion {
				rel := mapKey - stage.startLine + 1
				if nCount > 0 {
					rel = nCount + rel
				}
				relativeLine = rel
			} else {
				newLineNum := change.NewLineNum
				if newLineNum <= 0 {
					newLineNum = mapKey
				}
				relativeLine = newLineNum - stage.newLineStart + 1
			}
			relativeIdx := relativeLine - 1

			if relativeIdx >= 0 && relativeIdx < len(stageOldLines) {
				stageOldLines[relativeIdx] = change.OldContent
			}

			if relativeLine > 0 && (relativeLine <= len(stageLines) || change.Type == ChangeDeletion) {
				relativeToBufferLine[relativeLine] = lineNumToBufferLine[mapKey]

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
