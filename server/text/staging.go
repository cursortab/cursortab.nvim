package text

import (
	"cursortab/types"
	"sort"
	"strings"
	"unicode/utf8"
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
}

// stageBuilder holds the scratch state used while assembling a Stage.
// It exists so the runtime Stage struct doesn't have to carry construction-only
// fields. Once finalizeStages turns a builder into a Stage, the builder is
// discarded.
//
// Three distinct coordinate systems live on this struct:
//   - startLine/endLine: each change's MapKey (NewLineNum for non-deletions,
//     OldLineNum for deletions). Used to track which raw changes belong here.
//   - newLineStart/newLineEnd: new-file line range whose content forms Stage.Lines.
//   - bufferStart/bufferEnd: buffer line range (old-file coords + baseLineOffset).
type stageBuilder struct {
	rawChanges   []LineChange
	startLine    int
	endLine      int
	newLineStart int
	newLineEnd   int
	bufferStart  int
	bufferEnd    int
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
	AvailableWidth     int // Window text width for stacked-vs-side-by-side decision
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

	// Step 1: Compute buffer line for each change
	allChanges := make([]indexedChange, 0, len(diff.Changes))
	for _, change := range diff.Changes {
		bufferLine := diff.LineMapping.GetBufferLine(change, p.BaseLineOffset)
		allChanges = append(allChanges, indexedChange{change, bufferLine})
	}
	sort.SliceStable(allChanges, func(i, j int) bool {
		if allChanges[i].bufferLine != allChanges[j].bufferLine {
			return allChanges[i].bufferLine < allChanges[j].bufferLine
		}
		return allChanges[i].change.MapKey() < allChanges[j].change.MapKey()
	})

	// Step 2: Group changes into stage builders (proximity + maxLines + viewport)
	builders := groupChangesIntoStages(allChanges, p.ProximityThreshold, p.MaxLines, p.ViewportBottom, p.BaseLineOffset, diff)

	if len(builders) == 0 {
		return nil
	}

	// Step 3: Sort stages by cursor distance
	sort.SliceStable(builders, func(i, j int) bool {
		distI := distanceFromCursor(builders[i].bufferStart, builders[i].bufferEnd, p.CursorRow)
		distJ := distanceFromCursor(builders[j].bufferStart, builders[j].bufferEnd, p.CursorRow)
		if distI != distJ {
			return distI < distJ
		}
		return builders[i].startLine < builders[j].startLine
	})

	// Step 4: Finalize builders into stages (content, cursor targets, stacked hints)
	allStages := finalizeStages(builders, p.NewLines, p.OldLines, p.FilePath, p.BaseLineOffset, diff, p.CursorRow, p.CursorCol, p.AvailableWidth)

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

// groupChangesIntoStages groups sorted changes into stage builders based on
// buffer-line proximity and stage line limits.
func groupChangesIntoStages(changes []indexedChange, proximityThreshold int, maxLines int, viewportBottom int, baseLineOffset int, diff *DiffResult) []*stageBuilder {
	if len(changes) == 0 {
		return nil
	}

	var builders []*stageBuilder
	var current *stageBuilder
	lastBufferLine := 0

	startNew := func(ic indexedChange) {
		current = &stageBuilder{
			startLine:  ic.change.MapKey(),
			endLine:    ic.change.MapKey(),
			rawChanges: []LineChange{ic.change},
		}
		lastBufferLine = ic.bufferLine
	}

	for _, ic := range changes {
		mapKey := ic.change.MapKey()

		if current == nil {
			startNew(ic)
			continue
		}

		gap := ic.bufferLine - lastBufferLine
		if gap < 0 {
			gap = -gap
		}
		stageLineCount := current.endLine - current.startLine + 1
		exceedsMaxLines := maxLines > 0 && stageLineCount >= maxLines
		// Additions past the viewport create virtual lines that overflow;
		// deletions/modifications overlay existing lines, so they're safe.
		additionPastViewport := viewportBottom > 0 && ic.bufferLine > viewportBottom && ic.change.Type == ChangeAddition

		if gap <= proximityThreshold && !exceedsMaxLines && !additionPastViewport {
			current.rawChanges = append(current.rawChanges, ic.change)
			if mapKey > current.endLine {
				current.endLine = mapKey
			}
			lastBufferLine = ic.bufferLine
		} else {
			computeStageRanges(current, baseLineOffset, diff)
			builders = append(builders, current)
			startNew(ic)
		}
	}

	if current != nil {
		computeStageRanges(current, baseLineOffset, diff)
		builders = append(builders, current)
	}

	return builders
}

// StageNeedsNavigation determines if a stage requires cursor prediction UI.
// Returns true if the stage is outside viewport or far from cursor.
func StageNeedsNavigation(stage *Stage, cursorRow, viewportTop, viewportBottom, distThreshold int) bool {
	// Always require navigation for stages entirely outside the viewport,
	// even when close to the cursor (e.g. additions at the bottom edge).
	if viewportTop > 0 && viewportBottom > 0 {
		entirelyOutside := stage.BufferEnd < viewportTop || stage.BufferStart > viewportBottom
		if entirelyOutside {
			return true
		}
	}

	return distanceFromCursor(stage.BufferStart, stage.BufferEnd, cursorRow) > distThreshold
}

// distanceFromCursor returns the minimum distance from cursorRow to the
// inclusive range [start, end]. Returns 0 if cursorRow is inside the range.
func distanceFromCursor(start, end, cursorRow int) int {
	if cursorRow >= start && cursorRow <= end {
		return 0
	}
	if cursorRow < start {
		return start - cursorRow
	}
	return cursorRow - end
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
func computeStageRanges(b *stageBuilder, baseLineOffset int, diff *DiffResult) {
	minOldNonAdd := -1
	maxOldNonAdd := -1
	minAnchor := -1
	maxAnchor := -1
	hasNonAdditions := false
	hasAdditions := false
	newStart := 0
	newEnd := 0

	for _, change := range b.rawChanges {
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
		for _, change := range b.rawChanges {
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

	// Anchorless additions (OldLineNum=-1): resolve each addition's insertion
	// point by walking forward in NewToOld from its NewLineNum to the next
	// mapped old line. The insertion belongs just before that old line; if
	// no forward match exists, it goes past the end. Run unconditionally so
	// mixed stages (anchorless addition combined with non-additions) extend
	// the buffer range to include the insertion point — otherwise the
	// untouched lines between the insertion point and the rest of the stage
	// get duplicated when nvim_buf_set_lines applies the replacement.
	if diff.LineMapping != nil {
		for _, change := range b.rawChanges {
			if change.Type != ChangeAddition || change.OldLineNum > 0 {
				continue
			}
			if change.NewLineNum <= 0 {
				continue
			}
			pos := -1
			for i := change.NewLineNum - 1; i < len(diff.LineMapping.NewToOld); i++ {
				if diff.LineMapping.NewToOld[i] > 0 {
					pos = diff.LineMapping.NewToOld[i]
					break
				}
			}
			if pos < 0 {
				pos = len(diff.LineMapping.OldToNew) + 1
			}
			if oldStart <= 0 || pos < oldStart {
				oldStart = pos
			}
			if oldEnd > 0 && pos > oldEnd+1 {
				oldEnd = pos - 1
			}
		}
	}
	if oldEnd <= 0 {
		oldEnd = oldStart
	}
	if oldStart <= 0 {
		oldStart = b.startLine
	}
	if oldEnd <= 0 {
		oldEnd = b.endLine
	}

	b.bufferStart = oldStart + baseLineOffset - 1
	b.bufferEnd = oldEnd + baseLineOffset - 1

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
		b.newLineStart = 1
		b.newLineEnd = 0
	} else {
		b.newLineStart = newStart
		b.newLineEnd = newEnd
	}
}

// finalizeStages turns each stage builder into a finalized Stage.
// It extracts content, remaps changes to relative line numbers, computes groups,
// and sets cursor targets based on sort order.
func finalizeStages(builders []*stageBuilder, newLines []string, oldLines []string, filePath string, baseLineOffset int, diff *DiffResult, cursorRow, cursorCol int, availableWidth int) []*Stage {
	stages := make([]*Stage, len(builders))
	for i, b := range builders {
		isLastStage := i == len(builders)-1

		// Convert a single addition on the cursor line to a modification (append_chars).
		// When the cursor sits on a whitespace-only line and the diff produces a
		// pure addition (because the old content matched elsewhere as equal), we
		// want inline ghost text rather than a virtual line.
		if len(b.rawChanges) == 1 {
			change := b.rawChanges[0]
			if change.Type == ChangeAddition {
				bufLine := diff.LineMapping.GetBufferLine(change, baseLineOffset)
				oldIdx := bufLine - baseLineOffset // 0-indexed into oldLines
				if bufLine == cursorRow && oldIdx >= 0 && oldIdx < len(oldLines) {
					oldContent := oldLines[oldIdx]
					if oldContent != "" && strings.TrimSpace(oldContent) == "" {
						changeType, colStart, colEnd := categorizeLineChangeWithColumns(oldContent, change.Content)
						b.rawChanges[0] = LineChange{
							Type:       changeType,
							OldLineNum: oldIdx + 1,
							NewLineNum: change.NewLineNum,
							Content:    change.Content,
							OldContent: oldContent,
							ColStart:   colStart,
							ColEnd:     colEnd,
						}
						computeStageRanges(b, baseLineOffset, diff)
					}
				}
			}
		}

		// Extract the new content using the pre-computed new-line range
		var stageLines []string
		for j := b.newLineStart; j <= b.newLineEnd && j-1 < len(newLines); j++ {
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
		for _, change := range b.rawChanges {
			if change.Type != ChangeDeletion {
				hasNonDeletions = true
				break
			}
		}
		nCount := 0
		if hasNonDeletions {
			nCount = len(stageLines)
		}

		for _, change := range b.rawChanges {
			mapKey := change.MapKey()
			var relativeLine int
			if change.Type == ChangeDeletion {
				rel := mapKey - b.startLine + 1
				if nCount > 0 {
					rel = nCount + rel
				}
				relativeLine = rel
			} else {
				newLineNum := change.NewLineNum
				if newLineNum <= 0 {
					newLineNum = mapKey
				}
				relativeLine = newLineNum - b.newLineStart + 1
			}
			relativeIdx := relativeLine - 1

			if relativeIdx >= 0 && relativeIdx < len(stageOldLines) {
				stageOldLines[relativeIdx] = change.OldContent
			}

			if relativeLine > 0 && (relativeLine <= len(stageLines) || change.Type == ChangeDeletion) {
				relativeToBufferLine[relativeLine] = diff.LineMapping.GetBufferLine(change, baseLineOffset)

				remappedChange := change
				remappedChange.NewLineNum = relativeLine
				remappedChanges[relativeLine] = remappedChange
			}
		}

		// Compute groups, set BufferLine, validate render hints, and compute cursor position
		ctx := &StageContext{
			BufferStart:         b.bufferStart,
			CursorRow:           cursorRow,
			CursorCol:           cursorCol,
			LineNumToBufferLine: relativeToBufferLine,
		}
		groups, targetCursorLine, targetCursorCol := FinalizeStageGroups(remappedChanges, stageLines, ctx)

		// Create cursor target. For the last stage, point to the end of NEW content
		// (additions may extend past the old buffer end); otherwise point to the
		// start of the next stage. Pure deletions have no new content — the line
		// after the deleted range shifts down to bufferStart, but for a deletion
		// at the end of the completion's view there's no surviving line at that
		// position, so clamp to the last new line in the view.
		var cursorTarget *types.CursorPredictionTarget
		if isLastStage {
			targetLine := b.bufferStart + len(stageLines) - 1
			if len(stageLines) == 0 {
				targetLine = min(b.bufferStart, len(newLines)+baseLineOffset-1)
			}
			cursorTarget = &types.CursorPredictionTarget{
				RelativePath:    filePath,
				LineNumber:      int32(targetLine),
				ShouldRetrigger: true,
			}
		} else {
			cursorTarget = &types.CursorPredictionTarget{
				RelativePath:    filePath,
				LineNumber:      int32(builders[i+1].bufferStart),
				ShouldRetrigger: false,
			}
		}

		// Set stacked render hint for multi-line modification groups
		if availableWidth > 0 {
			setStackedHints(groups, availableWidth)
		}

		stages[i] = &Stage{
			BufferStart:  b.bufferStart,
			BufferEnd:    b.bufferEnd,
			Lines:        stageLines,
			Changes:      remappedChanges,
			Groups:       groups,
			CursorLine:   targetCursorLine,
			CursorCol:    targetCursorCol,
			CursorTarget: cursorTarget,
			IsLastStage:  isLastStage,
		}
	}
	return stages
}

// setStackedHints sets RenderHint="stacked" on modification groups when
// side-by-side rendering won't fit the available width.
func setStackedHints(groups []*Group, availableWidth int) {
	for _, g := range groups {
		if g.Type != "modification" || g.RenderHint != "" {
			continue
		}

		maxOldWidth := 0
		for _, line := range g.OldLines {
			if w := utf8.RuneCountInString(line); w > maxOldWidth {
				maxOldWidth = w
			}
		}
		maxNewWidth := 0
		for _, line := range g.Lines {
			if w := utf8.RuneCountInString(line); w > maxNewWidth {
				maxNewWidth = w
			}
		}
		if maxOldWidth+2+maxNewWidth > availableWidth {
			g.RenderHint = "stacked"
		}
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
