package text

import (
	"cursortab/logger"
	"cursortab/types"
	"strings"
)

// IncrementalDiffBuilder builds diff results incrementally as lines stream in.
// It computes changes line-by-line using similarity matching against old lines.
type IncrementalDiffBuilder struct {
	OldLines    []string           // Original content lines
	NewLines    []string           // Accumulated new content lines
	Changes     map[int]LineChange // Changes keyed by new line number (1-indexed)
	LineMapping *LineMapping       // Coordinate mapping between old and new

	// Tracking state
	oldLineIdx   int          // Current position in old lines (0-indexed)
	usedOldLines map[int]bool // Old line indices that have been matched
}

// NewIncrementalDiffBuilder creates a new incremental diff builder
func NewIncrementalDiffBuilder(oldLines []string) *IncrementalDiffBuilder {
	return &IncrementalDiffBuilder{
		OldLines:     oldLines,
		NewLines:     []string{},
		Changes:      make(map[int]LineChange),
		LineMapping:  &LineMapping{NewToOld: []int{}, OldToNew: make([]int, len(oldLines))},
		oldLineIdx:   0,
		usedOldLines: make(map[int]bool),
	}
}

// AddLine processes a new line and returns the change if any.
// Returns nil if the line matches exactly (no change).
func (b *IncrementalDiffBuilder) AddLine(line string) *LineChange {
	newLineNum := len(b.NewLines) + 1 // 1-indexed
	b.NewLines = append(b.NewLines, line)

	// Find matching old line
	oldLineNum := b.findMatchingOldLine(line, newLineNum)
	b.LineMapping.NewToOld = append(b.LineMapping.NewToOld, oldLineNum)

	if oldLineNum <= 0 {
		// Pure addition - no matching old line
		// Use the current old line position as anchor if available
		anchorOld := -1
		if b.oldLineIdx > 0 && b.oldLineIdx <= len(b.OldLines) {
			anchorOld = b.oldLineIdx
		}

		change := LineChange{
			Type:       ChangeAddition,
			OldLineNum: anchorOld,
			NewLineNum: newLineNum,
			Content:    line,
		}
		b.Changes[newLineNum] = change
		return &change
	}

	// Mark old line as used
	b.usedOldLines[oldLineNum] = true
	if oldLineNum-1 < len(b.LineMapping.OldToNew) {
		b.LineMapping.OldToNew[oldLineNum-1] = newLineNum
	}

	// Check for exact match
	oldContent := b.OldLines[oldLineNum-1]
	if oldContent == line {
		// Advance oldLineIdx past matched lines
		if oldLineNum > b.oldLineIdx {
			b.oldLineIdx = oldLineNum
		}
		return nil // No change
	}

	// Modification - categorize the change
	changeType, colStart, colEnd := categorizeLineChangeWithColumns(oldContent, line)
	change := LineChange{
		Type:       changeType,
		OldLineNum: oldLineNum,
		NewLineNum: newLineNum,
		OldContent: oldContent,
		Content:    line,
		ColStart:   colStart,
		ColEnd:     colEnd,
	}
	b.Changes[newLineNum] = change

	// Advance oldLineIdx past matched lines
	if oldLineNum > b.oldLineIdx {
		b.oldLineIdx = oldLineNum
	}

	return &change
}

// findMatchingOldLine searches for the best matching old line for the given new line.
// Returns the 1-indexed old line number, or 0 if no match found.
//
// The matching priority is:
// 1. Exact match at expected position (fastest path for unchanged lines)
// 2. Empty/whitespace old line with content new line (append_chars scenario)
// 3. Exact match anywhere in search window (handles model reordering)
// 4. Prefix match at expected position (completion/append_chars)
// 5. Similarity match at expected position with priority (fixes mismatch bug)
// 6. Prefix match anywhere in window
// 7. Best similarity match in window
func (b *IncrementalDiffBuilder) findMatchingOldLine(newLine string, _ int) int {
	if len(b.OldLines) == 0 {
		return 0
	}

	expectedPos := b.oldLineIdx
	searchStart := max(0, expectedPos-2)
	searchEnd := min(len(b.OldLines), expectedPos+10)

	// Priority 1: Exact match at expected position
	if expectedPos < len(b.OldLines) && !b.usedOldLines[expectedPos+1] {
		if b.OldLines[expectedPos] == newLine {
			return expectedPos + 1 // 1-indexed
		}

		// Priority 2: Empty/whitespace old line at expected position with content new line.
		// This is a modification (append_chars), not an addition.
		oldLineTrimmed := strings.TrimSpace(b.OldLines[expectedPos])
		newLineTrimmed := strings.TrimSpace(newLine)
		if oldLineTrimmed == "" && newLineTrimmed != "" {
			return expectedPos + 1 // 1-indexed
		}
	}

	// Priority 3: Exact match anywhere in search window
	for i := searchStart; i < searchEnd; i++ {
		if b.usedOldLines[i+1] {
			continue
		}
		if b.OldLines[i] == newLine {
			return i + 1
		}
	}

	// Priority 4: Prefix match at expected position
	if expectedPos < len(b.OldLines) && !b.usedOldLines[expectedPos+1] {
		oldLineTrimmedRight := strings.TrimRight(b.OldLines[expectedPos], " \t")
		if len(oldLineTrimmedRight) > 0 && strings.HasPrefix(newLine, oldLineTrimmedRight) {
			return expectedPos + 1 // 1-indexed
		}
	}

	// Priority 5: Similarity match at expected position with priority.
	// This gives priority to the expected position when similarity is REASONABLY HIGH,
	// even if a line further away might have slightly higher similarity.
	// Uses ExpectedPositionSimilarityThreshold to avoid false matches on barely-similar lines.
	// This fixes the bug where extended if conditions were matched to template strings.
	if expectedPos < len(b.OldLines) && !b.usedOldLines[expectedPos+1] {
		expectedSimilarity := LineSimilarity(newLine, b.OldLines[expectedPos])
		if expectedSimilarity > ExpectedPositionSimilarityThreshold {
			return expectedPos + 1 // 1-indexed
		}
	}

	// Priority 6: Prefix match anywhere in window
	for i := searchStart; i < searchEnd; i++ {
		if b.usedOldLines[i+1] || i == expectedPos {
			continue
		}
		oldLineTrimmed := strings.TrimRight(b.OldLines[i], " \t")
		if len(oldLineTrimmed) > 0 && strings.HasPrefix(newLine, oldLineTrimmed) {
			return i + 1
		}
	}

	// Priority 7: Best similarity match in window (excluding expected position)
	bestIdx := -1
	bestSimilarity := SimilarityThreshold

	for i := searchStart; i < searchEnd; i++ {
		if b.usedOldLines[i+1] || i == expectedPos {
			continue
		}
		similarity := LineSimilarity(newLine, b.OldLines[i])
		if similarity > bestSimilarity {
			bestSimilarity = similarity
			bestIdx = i
		}
	}

	if bestIdx >= 0 {
		return bestIdx + 1
	}

	// No match found - this will be treated as an addition during streaming.
	// The actual change type will be determined at stage finalization using
	// batch diff, which correctly handles equal line counts as modifications.
	return 0
}

// IncrementalStageBuilder builds stages incrementally as lines stream in.
// It finalizes stages when gaps or viewport boundaries are detected.
type IncrementalStageBuilder struct {
	OldLines           []string
	BaseLineOffset     int // Where the diff range starts in the buffer (1-indexed)
	ProximityThreshold int
	MaxVisibleLines    int // Max visible lines per completion (0 to disable)
	ViewportTop        int
	ViewportBottom     int
	CursorRow          int
	CursorCol          int // Current cursor column (0-indexed)
	FilePath           string

	// State
	diffBuilder            *IncrementalDiffBuilder
	currentStage           *Stage
	currentStageInViewport bool
	finalizedStages        []*Stage
	lastChangeBufferLine   int // Track last BUFFER line with a change (not new line number)
}

// NewIncrementalStageBuilder creates a new incremental stage builder
func NewIncrementalStageBuilder(
	oldLines []string,
	baseLineOffset int,
	proximityThreshold int,
	maxVisibleLines int,
	viewportTop, viewportBottom int,
	cursorRow, cursorCol int,
	filePath string,
) *IncrementalStageBuilder {
	return &IncrementalStageBuilder{
		OldLines:             oldLines,
		BaseLineOffset:       baseLineOffset,
		ProximityThreshold:   proximityThreshold,
		MaxVisibleLines:      maxVisibleLines,
		ViewportTop:          viewportTop,
		ViewportBottom:       viewportBottom,
		CursorRow:            cursorRow,
		CursorCol:            cursorCol,
		FilePath:             filePath,
		diffBuilder:          NewIncrementalDiffBuilder(oldLines),
		finalizedStages:      []*Stage{},
		lastChangeBufferLine: 0,
	}
}

// AddLine processes a new line and returns a newly finalized stage if any.
// Returns nil if no stage was finalized on this line.
func (b *IncrementalStageBuilder) AddLine(line string) *Stage {
	change := b.diffBuilder.AddLine(line)
	lineNum := len(b.diffBuilder.NewLines) // 1-indexed

	if change == nil {
		// No change on this line - but check if we should finalize based on
		// buffer line gap (where this line maps in the original file).
		if b.currentStage != nil && b.lastChangeBufferLine > 0 {
			// Compute where this unchanged line maps in the buffer
			currentBufferLine := b.computeCurrentBufferLine(lineNum)
			if currentBufferLine > 0 {
				bufferGap := currentBufferLine - b.lastChangeBufferLine
				if bufferGap > b.ProximityThreshold {
					return b.finalizeCurrentStage()
				}
			}
		}
		return nil
	}

	// We have a change - compute its buffer line
	bufferLine := b.diffBuilder.LineMapping.GetBufferLine(*change, lineNum, b.BaseLineOffset)

	// Determine if this change is in viewport
	isInViewport := b.ViewportTop == 0 && b.ViewportBottom == 0 ||
		(bufferLine >= b.ViewportTop && bufferLine <= b.ViewportBottom)

	// Check if this starts a new stage (buffer line gap or viewport boundary)
	if b.shouldStartNewStage(bufferLine, isInViewport) {
		finalized := b.finalizeCurrentStage()
		b.startNewStage(lineNum, bufferLine, *change, isInViewport)
		return finalized
	}

	// Extend current stage or start first one
	if b.currentStage == nil {
		b.startNewStage(lineNum, bufferLine, *change, isInViewport)
	} else {
		b.extendCurrentStage(lineNum, bufferLine, *change)
	}

	return nil
}

// shouldStartNewStage determines if we need to start a new stage based on
// BUFFER LINE gaps (not new line numbers), MaxVisibleLines limit, and viewport boundaries.
// This ensures stages split when changes map to far-apart positions in the original file.
func (b *IncrementalStageBuilder) shouldStartNewStage(bufferLine int, isInViewport bool) bool {
	if b.currentStage == nil {
		return false // First change will be handled by starting new stage
	}

	// Check MaxVisibleLines limit
	if b.MaxVisibleLines > 0 {
		stageLineCount := b.currentStage.endLine - b.currentStage.startLine + 1
		if stageLineCount >= b.MaxVisibleLines {
			return true
		}
	}

	// Check buffer line gap (not new line gap!)
	if b.lastChangeBufferLine > 0 {
		bufferGap := bufferLine - b.lastChangeBufferLine
		if bufferGap < 0 {
			bufferGap = -bufferGap // Handle out-of-order matches
		}
		if bufferGap > b.ProximityThreshold {
			return true
		}
	}

	// Check viewport boundary crossing
	if b.currentStageInViewport != isInViewport {
		return true
	}

	return false
}

// startNewStage initializes a new stage with the given change
func (b *IncrementalStageBuilder) startNewStage(lineNum int, bufferLine int, change LineChange, isInViewport bool) {
	b.currentStage = &Stage{
		startLine:  lineNum,
		endLine:    lineNum,
		rawChanges: make(map[int]LineChange),
	}
	b.currentStage.rawChanges[lineNum] = change
	b.currentStageInViewport = isInViewport
	b.lastChangeBufferLine = bufferLine

	// Compute initial buffer range
	b.currentStage.BufferStart, b.currentStage.BufferEnd = b.computeStageBufferRange(b.currentStage)
}

// extendCurrentStage adds a change to the current stage
func (b *IncrementalStageBuilder) extendCurrentStage(lineNum int, bufferLine int, change LineChange) {
	b.currentStage.rawChanges[lineNum] = change
	if lineNum > b.currentStage.endLine {
		b.currentStage.endLine = lineNum
	}
	b.lastChangeBufferLine = bufferLine

	// Update buffer range
	b.currentStage.BufferStart, b.currentStage.BufferEnd = b.computeStageBufferRange(b.currentStage)
}

// finalizeCurrentStage finalizes and returns the current stage
func (b *IncrementalStageBuilder) finalizeCurrentStage() *Stage {
	if b.currentStage == nil || len(b.currentStage.rawChanges) == 0 {
		return nil
	}

	stage := b.currentStage

	// Extract NEW content for the stage
	newStartLine, newEndLine := getStageNewLineRange(stage)
	var stageNewLines []string
	for j := newStartLine; j <= newEndLine && j-1 < len(b.diffBuilder.NewLines); j++ {
		if j > 0 {
			stageNewLines = append(stageNewLines, b.diffBuilder.NewLines[j-1])
		}
	}

	// Find OLD line range using rawChanges and LineMapping.
	// For changes with explicit OldLineNum (including additions anchored to a line), use that.
	// For unmatched lines, fall back to the LineMapping or sequential position.
	minOld := -1
	maxOld := -1

	// First, check rawChanges for explicit old line anchors
	for lineNum, change := range stage.rawChanges {
		if change.OldLineNum > 0 && change.OldLineNum <= len(b.OldLines) {
			if minOld == -1 || change.OldLineNum < minOld {
				minOld = change.OldLineNum
			}
			if change.OldLineNum > maxOld {
				maxOld = change.OldLineNum
			}
		} else if change.Type == ChangeAddition && change.OldLineNum == -1 {
			// Pure addition without anchor - use the last old line as anchor
			// This handles additions that extend beyond the original content
			lastOldLine := len(b.OldLines)
			if lastOldLine > 0 {
				if minOld == -1 || lastOldLine < minOld {
					minOld = lastOldLine
				}
				if lastOldLine > maxOld {
					maxOld = lastOldLine
				}
			}
		}
		_ = lineNum // suppress unused warning
	}

	// Fall back to LineMapping if no anchors found in rawChanges
	if minOld == -1 {
		for j := newStartLine; j <= newEndLine; j++ {
			if j <= 0 {
				continue
			}
			var oldLine int
			if j-1 < len(b.diffBuilder.LineMapping.NewToOld) {
				oldLine = b.diffBuilder.LineMapping.NewToOld[j-1]
			}
			if oldLine <= 0 {
				// Unmatched line - use sequential position as fallback
				oldLine = j
			}
			if oldLine > 0 && oldLine <= len(b.OldLines) {
				if minOld == -1 || oldLine < minOld {
					minOld = oldLine
				}
				if oldLine > maxOld {
					maxOld = oldLine
				}
			}
		}
	}

	// Extract OLD content for the computed range
	var stageOldLines []string
	if minOld > 0 && maxOld > 0 {
		// Convert 1-indexed old line numbers to 0-indexed array indices
		startIdx := minOld - 1
		endIdx := maxOld // exclusive (maxOld is 1-indexed, so maxOld = endIdx exclusive)
		if startIdx >= 0 && endIdx <= len(b.OldLines) {
			stageOldLines = b.OldLines[startIdx:endIdx]
		}
	}

	// Compute initial BufferStart from the old line range (before additions adjustment)
	bufferStart := b.BaseLineOffset
	if minOld > 0 {
		bufferStart = minOld + b.BaseLineOffset - 1
	}

	// Build changes using the LineMapping from streaming for line correspondence,
	// then categorize each pair individually. This preserves the ordered prefix
	// matching from incremental diff while getting accurate change types.
	//
	// The key insight: batch ComputeDiff optimizes globally and may match lines
	// out of order, but for code completion we want ordered matching where the
	// first new line matches the first old line (especially for prefix completion).

	// First, build a set of old lines that are already matched (to avoid double-use)
	usedOldLines := make(map[int]bool)
	for j := newStartLine; j <= newEndLine; j++ {
		if j > 0 && j-1 < len(b.diffBuilder.LineMapping.NewToOld) {
			oldLine := b.diffBuilder.LineMapping.NewToOld[j-1]
			if oldLine > 0 {
				usedOldLines[oldLine] = true
			}
		}
	}

	remappedChanges := make(map[int]LineChange)
	for i, newLine := range stageNewLines {
		relativeLine := i + 1 // 1-indexed relative to stage
		absoluteNewLine := newStartLine + i

		// Get the matched old line from streaming's LineMapping
		var oldLine int
		var oldContent string
		if absoluteNewLine > 0 && absoluteNewLine-1 < len(b.diffBuilder.LineMapping.NewToOld) {
			oldLine = b.diffBuilder.LineMapping.NewToOld[absoluteNewLine-1]
		}
		if oldLine <= 0 {
			// No match during streaming - use sequential position as fallback
			// BUT only if that old line isn't already matched to another new line
			fallbackOldLine := absoluteNewLine
			if fallbackOldLine > 0 && fallbackOldLine <= len(b.OldLines) && !usedOldLines[fallbackOldLine] {
				oldLine = fallbackOldLine
			}
		}
		if oldLine > 0 && oldLine <= len(b.OldLines) {
			oldContent = b.OldLines[oldLine-1]
		}

		// Determine change type
		var change LineChange
		if oldLine <= 0 {
			// No matching old line - this is an addition
			change = LineChange{
				Type:       ChangeAddition,
				OldLineNum: -1,
				NewLineNum: relativeLine,
				Content:    newLine,
			}
		} else if oldContent == newLine {
			// Skip if content is identical (matched old line with same content)
			continue
		} else {
			// Modification - categorize the change type
			changeType, colStart, colEnd := categorizeLineChangeWithColumns(oldContent, newLine)
			change = LineChange{
				Type:       changeType,
				OldLineNum: oldLine - minOld + 1, // Relative to stage's old content
				NewLineNum: relativeLine,
				OldContent: oldContent,
				Content:    newLine,
				ColStart:   colStart,
				ColEnd:     colEnd,
			}
		}
		remappedChanges[relativeLine] = change
	}

	// Check if this is a pure additions stage using the REMAPPED changes.
	// This is important because streaming may classify low-similarity modifications
	// as additions, but the fallback matching above correctly identifies them as
	// modifications. We use the final classification to decide on buffer positioning.
	hasPureAdditionsOnly := true
	for _, change := range remappedChanges {
		if change.Type != ChangeAddition {
			hasPureAdditionsOnly = false
			break
		}
	}

	// For pure additions WITH a valid anchor, the buffer range represents
	// where the new content will be INSERTED, not the anchor. Additions are inserted
	// AFTER the anchor line, so we need to add 1 to get the insertion point.
	// This matches the non-streaming getStageBufferRange behavior.
	if hasPureAdditionsOnly && minOld > 0 {
		bufferStart++
	}

	stage.BufferStart = bufferStart
	stage.BufferEnd = max(bufferStart+len(stageOldLines)-1, bufferStart)

	// Build mapping from relative line to buffer line for modifications.
	// Modifications map to their old line position in the buffer.
	relativeToBufferLine := make(map[int]int)
	for relativeLine, change := range remappedChanges {
		if change.Type == ChangeModification || change.Type.IsCharacterLevel() {
			if change.OldLineNum > 0 {
				relativeToBufferLine[relativeLine] = bufferStart + change.OldLineNum - 1
			}
		}
	}

	// Compute groups, set BufferLine, validate render hints, and compute cursor position
	ctx := &StageContext{
		BufferStart:         bufferStart,
		CursorRow:           b.CursorRow,
		CursorCol:           b.CursorCol,
		LineNumToBufferLine: relativeToBufferLine,
	}
	groups, cursorLine, cursorCol := FinalizeStageGroups(remappedChanges, stageNewLines, ctx)

	// Populate stage fields
	stage.Lines = stageNewLines
	stage.Changes = remappedChanges
	stage.Groups = groups
	stage.CursorLine = cursorLine
	stage.CursorCol = cursorCol

	b.finalizedStages = append(b.finalizedStages, stage)
	b.currentStage = nil

	return stage
}

// computeStageBufferRange computes the buffer range for a stage
func (b *IncrementalStageBuilder) computeStageBufferRange(stage *Stage) (int, int) {
	// Create a temporary DiffResult for compatibility with getStageBufferRange
	diffResult := &DiffResult{
		Changes:      b.diffBuilder.Changes,
		LineMapping:  b.diffBuilder.LineMapping,
		OldLineCount: len(b.OldLines),
		NewLineCount: len(b.diffBuilder.NewLines),
	}

	return getStageBufferRange(stage, b.BaseLineOffset, diffResult, nil)
}

// computeCurrentBufferLine computes the buffer line for the current position
// (used for gap detection on unchanged lines)
func (b *IncrementalStageBuilder) computeCurrentBufferLine(lineNum int) int {
	// Use the line mapping to find where this new line maps in the old file
	if b.diffBuilder.LineMapping != nil && lineNum > 0 && lineNum <= len(b.diffBuilder.LineMapping.NewToOld) {
		oldLine := b.diffBuilder.LineMapping.NewToOld[lineNum-1]
		if oldLine > 0 {
			return oldLine + b.BaseLineOffset - 1
		}
	}
	// Fallback: estimate based on position
	return lineNum + b.BaseLineOffset - 1
}

// Finalize completes the build and returns all stages sorted by cursor distance.
// Call this when the stream completes.
func (b *IncrementalStageBuilder) Finalize() *StagingResult {
	defer logger.Trace("IncrementalStageBuilder.Finalize")()

	// Finalize any remaining stage
	if b.currentStage != nil && len(b.currentStage.rawChanges) > 0 {
		b.finalizeCurrentStage()
	}

	if len(b.finalizedStages) == 0 {
		return nil
	}

	// Sort stages by cursor distance
	stages := b.finalizedStages
	for i := 0; i < len(stages)-1; i++ {
		for j := i + 1; j < len(stages); j++ {
			distI := stageDistanceFromCursor(stages[i], b.CursorRow)
			distJ := stageDistanceFromCursor(stages[j], b.CursorRow)
			if distJ < distI || (distJ == distI && stages[j].startLine < stages[i].startLine) {
				stages[i], stages[j] = stages[j], stages[i]
			}
		}
	}

	// Set cursor targets and IsLastStage
	for i, stage := range stages {
		isLastStage := i == len(stages)-1

		if isLastStage {
			// For last stage, cursor target points to end of NEW content,
			// not the old buffer end. This is important when additions extend
			// beyond the original buffer.
			newEndLine := stage.BufferStart + len(stage.Lines) - 1
			stage.CursorTarget = &types.CursorPredictionTarget{
				RelativePath:    b.FilePath,
				LineNumber:      int32(newEndLine),
				ShouldRetrigger: true,
			}
		} else {
			nextStage := stages[i+1]
			stage.CursorTarget = &types.CursorPredictionTarget{
				RelativePath:    b.FilePath,
				LineNumber:      int32(nextStage.BufferStart),
				ShouldRetrigger: false,
			}
		}
		stage.IsLastStage = isLastStage

		// Clear rawChanges
		stage.rawChanges = nil
	}

	// Check if first stage needs navigation UI
	firstNeedsNav := StageNeedsNavigation(
		stages[0], b.CursorRow, b.ViewportTop, b.ViewportBottom, b.ProximityThreshold,
	)

	return &StagingResult{
		Stages:               stages,
		FirstNeedsNavigation: firstNeedsNav,
	}
}

