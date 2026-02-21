package text

import (
	"cursortab/logger"
	"cursortab/types"
	"sort"
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

// matchStrategy is a function that attempts to match a new line to an old line.
// Returns 1-indexed old line number, or 0 if no match.
type matchStrategy func(b *IncrementalDiffBuilder, newLine string, expectedPos, searchStart, searchEnd int) int

var matchStrategies = []matchStrategy{
	// Priority 1: Exact match at expected position
	func(b *IncrementalDiffBuilder, newLine string, expectedPos, _, _ int) int {
		if expectedPos < len(b.OldLines) && !b.usedOldLines[expectedPos+1] && b.OldLines[expectedPos] == newLine {
			return expectedPos + 1
		}
		return 0
	},
	// Priority 2: Empty/whitespace old line at expected position with content new line (append_chars)
	func(b *IncrementalDiffBuilder, newLine string, expectedPos, _, _ int) int {
		if expectedPos < len(b.OldLines) && !b.usedOldLines[expectedPos+1] {
			if strings.TrimSpace(b.OldLines[expectedPos]) == "" && strings.TrimSpace(newLine) != "" {
				return expectedPos + 1
			}
		}
		return 0
	},
	// Priority 3: Exact match anywhere in search window
	func(b *IncrementalDiffBuilder, newLine string, _, searchStart, searchEnd int) int {
		for i := searchStart; i < searchEnd; i++ {
			if !b.usedOldLines[i+1] && b.OldLines[i] == newLine {
				return i + 1
			}
		}
		return 0
	},
	// Priority 4: Prefix match at expected position
	func(b *IncrementalDiffBuilder, newLine string, expectedPos, _, _ int) int {
		if expectedPos < len(b.OldLines) && !b.usedOldLines[expectedPos+1] {
			trimmed := strings.TrimRight(b.OldLines[expectedPos], " \t")
			if len(trimmed) > 0 && strings.HasPrefix(newLine, trimmed) {
				return expectedPos + 1
			}
		}
		return 0
	},
	// Priority 5: Similarity match at expected position with priority
	func(b *IncrementalDiffBuilder, newLine string, expectedPos, _, _ int) int {
		if expectedPos < len(b.OldLines) && !b.usedOldLines[expectedPos+1] {
			if LineSimilarity(newLine, b.OldLines[expectedPos]) > ExpectedPositionSimilarityThreshold {
				return expectedPos + 1
			}
		}
		return 0
	},
	// Priority 6: Prefix match anywhere in window
	func(b *IncrementalDiffBuilder, newLine string, expectedPos, searchStart, searchEnd int) int {
		for i := searchStart; i < searchEnd; i++ {
			if b.usedOldLines[i+1] || i == expectedPos {
				continue
			}
			trimmed := strings.TrimRight(b.OldLines[i], " \t")
			if len(trimmed) > 0 && strings.HasPrefix(newLine, trimmed) {
				return i + 1
			}
		}
		return 0
	},
	// Priority 7: Best similarity match in window
	func(b *IncrementalDiffBuilder, newLine string, expectedPos, searchStart, searchEnd int) int {
		bestIdx := -1
		bestSim := SimilarityThreshold
		for i := searchStart; i < searchEnd; i++ {
			if b.usedOldLines[i+1] || i == expectedPos {
				continue
			}
			sim := LineSimilarity(newLine, b.OldLines[i])
			if sim > bestSim {
				bestSim = sim
				bestIdx = i
			}
		}
		if bestIdx >= 0 {
			return bestIdx + 1
		}
		return 0
	},
}

// findMatchingOldLine searches for the best matching old line for the given new line.
// Returns the 1-indexed old line number, or 0 if no match found.
func (b *IncrementalDiffBuilder) findMatchingOldLine(newLine string, _ int) int {
	if len(b.OldLines) == 0 {
		return 0
	}

	expectedPos := b.oldLineIdx
	searchStart := max(0, expectedPos-2)
	searchEnd := min(len(b.OldLines), expectedPos+10)

	for _, strategy := range matchStrategies {
		if result := strategy(b, newLine, expectedPos, searchStart, searchEnd); result > 0 {
			return result
		}
	}

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

// oldLineRange holds the computed old line range and extracted content for a stage.
type oldLineRange struct {
	minOld        int
	maxOld        int
	stageOldLines []string
}

// extractStageNewLines extracts new content lines for the stage from the builder's NewLines.
func (b *IncrementalStageBuilder) extractStageNewLines(stage *Stage) ([]string, int, int) {
	newStartLine, newEndLine := getStageNewLineRange(stage)
	var stageNewLines []string
	for j := newStartLine; j <= newEndLine && j-1 < len(b.diffBuilder.NewLines); j++ {
		if j > 0 {
			stageNewLines = append(stageNewLines, b.diffBuilder.NewLines[j-1])
		}
	}
	return stageNewLines, newStartLine, newEndLine
}

// computeOldLineRange finds the old line range using rawChanges and LineMapping,
// then extracts old content for that range.
func (b *IncrementalStageBuilder) computeOldLineRange(stage *Stage, newStartLine, newEndLine int) oldLineRange {
	minOld := -1
	maxOld := -1

	// Check rawChanges for explicit old line anchors
	for _, change := range stage.rawChanges {
		if change.OldLineNum > 0 && change.OldLineNum <= len(b.OldLines) {
			if minOld == -1 || change.OldLineNum < minOld {
				minOld = change.OldLineNum
			}
			if change.OldLineNum > maxOld {
				maxOld = change.OldLineNum
			}
		} else if change.Type == ChangeAddition && change.OldLineNum == -1 {
			// Pure addition without anchor - use the last old line as anchor
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
	}

	// Fall back to LineMapping if no anchors found
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

	// Extract old content for the computed range
	var stageOldLines []string
	if minOld > 0 && maxOld > 0 {
		startIdx := minOld - 1
		endIdx := maxOld
		if startIdx >= 0 && endIdx <= len(b.OldLines) {
			stageOldLines = b.OldLines[startIdx:endIdx]
		}
	}

	return oldLineRange{minOld: minOld, maxOld: maxOld, stageOldLines: stageOldLines}
}

// remapChanges builds changes using the LineMapping from streaming for line correspondence,
// categorizing each pair individually. This preserves ordered prefix matching from
// incremental diff while getting accurate change types.
func (b *IncrementalStageBuilder) remapChanges(stageNewLines []string, newStartLine, newEndLine, minOld int) map[int]LineChange {
	// Build a set of old lines that are already matched (to avoid double-use)
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
		relativeLine := i + 1
		absoluteNewLine := newStartLine + i

		var oldLine int
		var oldContent string
		if absoluteNewLine > 0 && absoluteNewLine-1 < len(b.diffBuilder.LineMapping.NewToOld) {
			oldLine = b.diffBuilder.LineMapping.NewToOld[absoluteNewLine-1]
		}
		if oldLine <= 0 {
			fallbackOldLine := absoluteNewLine
			if fallbackOldLine > 0 && fallbackOldLine <= len(b.OldLines) &&
				!usedOldLines[fallbackOldLine] && !b.diffBuilder.usedOldLines[fallbackOldLine] {
				oldLine = fallbackOldLine
			}
		}
		if oldLine > 0 && oldLine <= len(b.OldLines) {
			oldContent = b.OldLines[oldLine-1]
		}

		var change LineChange
		if oldLine <= 0 {
			change = LineChange{
				Type:       ChangeAddition,
				OldLineNum: -1,
				NewLineNum: relativeLine,
				Content:    newLine,
			}
		} else if oldContent == newLine {
			continue
		} else {
			changeType, colStart, colEnd := categorizeLineChangeWithColumns(oldContent, newLine)
			change = LineChange{
				Type:       changeType,
				OldLineNum: oldLine - minOld + 1,
				NewLineNum: relativeLine,
				OldContent: oldContent,
				Content:    newLine,
				ColStart:   colStart,
				ColEnd:     colEnd,
			}
		}
		remappedChanges[relativeLine] = change
	}

	return remappedChanges
}

// finalizeCurrentStage finalizes and returns the current stage
func (b *IncrementalStageBuilder) finalizeCurrentStage() *Stage {
	if b.currentStage == nil || len(b.currentStage.rawChanges) == 0 {
		return nil
	}

	stage := b.currentStage

	stageNewLines, newStartLine, newEndLine := b.extractStageNewLines(stage)
	olr := b.computeOldLineRange(stage, newStartLine, newEndLine)

	bufferStart := b.BaseLineOffset
	if olr.minOld > 0 {
		bufferStart = olr.minOld + b.BaseLineOffset - 1
	}

	remappedChanges := b.remapChanges(stageNewLines, newStartLine, newEndLine, olr.minOld)

	// Check if this is a pure additions stage using the remapped changes.
	// Streaming may classify low-similarity modifications as additions, but
	// fallback matching correctly identifies them as modifications.
	hasPureAdditionsOnly := true
	for _, change := range remappedChanges {
		if change.Type != ChangeAddition {
			hasPureAdditionsOnly = false
			break
		}
	}

	if hasPureAdditionsOnly {
		// For pure additions, compute bufferStart from GetBufferLine which
		// already handles the anchor→insertion point conversion.
		minBuf := -1
		for lineNum, change := range stage.rawChanges {
			bufLine := b.diffBuilder.LineMapping.GetBufferLine(change, lineNum, b.BaseLineOffset)
			if minBuf == -1 || bufLine < minBuf {
				minBuf = bufLine
			}
		}
		if minBuf > 0 {
			bufferStart = minBuf
		}
	} else {
		// Streaming may classify modifications as additions due to low similarity.
		// After fallback matching in remapChanges, we may discover that additions are
		// actually modifications of a different old line than the anchor. Recompute
		// the old line range from the actual matched positions and re-remap.
		correctedMinOld := -1
		correctedMaxOld := -1
		for _, change := range remappedChanges {
			if change.Type == ChangeAddition || change.OldLineNum <= 0 {
				continue
			}
			absOld := change.OldLineNum + olr.minOld - 1
			if correctedMinOld == -1 || absOld < correctedMinOld {
				correctedMinOld = absOld
			}
			if absOld > correctedMaxOld {
				correctedMaxOld = absOld
			}
		}
		if correctedMinOld > 0 && correctedMinOld != olr.minOld {
			olr.minOld = correctedMinOld
			olr.maxOld = max(olr.maxOld, correctedMaxOld)
			startIdx := olr.minOld - 1
			endIdx := olr.maxOld
			if startIdx >= 0 && endIdx <= len(b.OldLines) {
				olr.stageOldLines = b.OldLines[startIdx:endIdx]
			}
			bufferStart = olr.minOld + b.BaseLineOffset - 1
			remappedChanges = b.remapChanges(stageNewLines, newStartLine, newEndLine, olr.minOld)
		}
	}

	stage.BufferStart = bufferStart
	stage.BufferEnd = max(bufferStart+len(olr.stageOldLines)-1, bufferStart)

	// Build mapping from relative line to buffer line.
	relativeToBufferLine := make(map[int]int)
	for relativeLine, change := range remappedChanges {
		if change.Type == ChangeModification || change.Type.IsCharacterLevel() {
			if change.OldLineNum > 0 {
				relativeToBufferLine[relativeLine] = bufferStart + change.OldLineNum - 1
			}
		} else if change.Type == ChangeAddition {
			absoluteNewLine := newStartLine + relativeLine - 1
			if origChange, ok := b.diffBuilder.Changes[absoluteNewLine]; ok {
				relativeToBufferLine[relativeLine] = b.diffBuilder.LineMapping.GetBufferLine(
					origChange, absoluteNewLine, b.BaseLineOffset)
			}
		}
	}

	ctx := &StageContext{
		BufferStart:         bufferStart,
		CursorRow:           b.CursorRow,
		CursorCol:           b.CursorCol,
		LineNumToBufferLine: relativeToBufferLine,
	}
	groups, cursorLine, cursorCol := FinalizeStageGroups(remappedChanges, stageNewLines, ctx)

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
	sort.SliceStable(stages, func(i, j int) bool {
		distI := stageDistanceFromCursor(stages[i], b.CursorRow)
		distJ := stageDistanceFromCursor(stages[j], b.CursorRow)
		if distI != distJ {
			return distI < distJ
		}
		return stages[i].startLine < stages[j].startLine
	})

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
