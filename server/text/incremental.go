package text

import (
	"cursortab/logger"
)

// IncrementalDiffBuilder builds diff results incrementally as lines stream in.
// It uses exact matching only during streaming for stage boundary detection.
// Accurate change classification is deferred to ComputeDiff at stage finalization.
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

// AddLine processes a new line and returns a change if the line doesn't exactly
// match an old line. Returns nil for exact matches. During streaming, non-matches
// are reported as additions; accurate classification happens at stage finalization.
func (b *IncrementalDiffBuilder) AddLine(line string) *LineChange {
	newLineNum := len(b.NewLines) + 1 // 1-indexed
	b.NewLines = append(b.NewLines, line)

	// Find exact matching old line
	oldLineNum := b.findMatchingOldLine(line)
	b.LineMapping.NewToOld = append(b.LineMapping.NewToOld, oldLineNum)

	if oldLineNum <= 0 {
		// No exact match — report as addition for stage boundary detection
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

	// Exact match — mark old line as used and advance
	b.usedOldLines[oldLineNum] = true
	if oldLineNum-1 < len(b.LineMapping.OldToNew) {
		b.LineMapping.OldToNew[oldLineNum-1] = newLineNum
	}
	if oldLineNum > b.oldLineIdx {
		b.oldLineIdx = oldLineNum
	}
	return nil
}

// findMatchingOldLine searches for an exact matching old line.
// Returns the 1-indexed old line number, or 0 if no match found.
func (b *IncrementalDiffBuilder) findMatchingOldLine(newLine string) int {
	if len(b.OldLines) == 0 {
		return 0
	}

	expectedPos := b.oldLineIdx
	searchStart := max(0, expectedPos-2)
	searchEnd := min(len(b.OldLines), expectedPos+10)

	// Priority 1: Exact match at expected position
	if expectedPos < len(b.OldLines) && !b.usedOldLines[expectedPos+1] && b.OldLines[expectedPos] == newLine {
		return expectedPos + 1
	}

	// Priority 2: Exact match anywhere in search window
	for i := searchStart; i < searchEnd; i++ {
		if !b.usedOldLines[i+1] && b.OldLines[i] == newLine {
			return i + 1
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
func (b *IncrementalStageBuilder) shouldStartNewStage(bufferLine int, isInViewport bool) bool {
	if b.currentStage == nil {
		return false
	}

	// Check MaxVisibleLines limit
	if b.MaxVisibleLines > 0 {
		stageLineCount := b.currentStage.endLine - b.currentStage.startLine + 1
		if stageLineCount >= b.MaxVisibleLines {
			return true
		}
	}

	// Check buffer line gap
	if b.lastChangeBufferLine > 0 {
		bufferGap := bufferLine - b.lastChangeBufferLine
		if bufferGap < 0 {
			bufferGap = -bufferGap
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

	b.currentStage.BufferStart, b.currentStage.BufferEnd = b.computeStageBufferRange(b.currentStage)
}

// extendCurrentStage adds a change to the current stage
func (b *IncrementalStageBuilder) extendCurrentStage(lineNum int, bufferLine int, change LineChange) {
	b.currentStage.rawChanges[lineNum] = change
	if lineNum > b.currentStage.endLine {
		b.currentStage.endLine = lineNum
	}
	b.lastChangeBufferLine = bufferLine

	b.currentStage.BufferStart, b.currentStage.BufferEnd = b.computeStageBufferRange(b.currentStage)
}

// findOldLineRange finds the old line range for a stage by using exact-match
// anchors from NewToOld. Walks backward from startNewLine and forward from
// endNewLine to find the nearest anchors, then returns the range between them.
// Returns (minOld, maxOld) as 1-indexed old line numbers.
func (b *IncrementalStageBuilder) findOldLineRange(startNewLine, endNewLine int) (int, int) {
	mapping := b.diffBuilder.LineMapping.NewToOld

	// Find anchor before: walk backward from startNewLine-1
	anchorBefore := -1
	for i := startNewLine - 2; i >= 0; i-- {
		if i < len(mapping) && mapping[i] > 0 {
			anchorBefore = mapping[i]
			break
		}
	}

	// Find anchor after: walk forward from endNewLine+1
	anchorAfter := -1
	for i := endNewLine; i < len(mapping); i++ {
		if mapping[i] > 0 {
			anchorAfter = mapping[i]
			break
		}
	}

	// Compute old range between anchors (exclusive of anchors themselves)
	minOld := -1
	maxOld := -1

	if anchorBefore > 0 {
		minOld = anchorBefore + 1
	} else {
		minOld = 1
	}

	if anchorAfter > 0 {
		maxOld = anchorAfter - 1
	} else {
		maxOld = len(b.OldLines)
	}

	// Clamp
	if minOld < 1 {
		minOld = 1
	}
	if maxOld > len(b.OldLines) {
		maxOld = len(b.OldLines)
	}

	// If range is empty (anchors are adjacent), this is a pure addition
	if minOld > maxOld {
		return -1, -1
	}

	return minOld, maxOld
}

// finalizeCurrentStage finalizes the current stage using ComputeDiff for
// accurate change classification.
func (b *IncrementalStageBuilder) finalizeCurrentStage() *Stage {
	if b.currentStage == nil || len(b.currentStage.rawChanges) == 0 {
		return nil
	}

	stage := b.currentStage

	// Get new line range from stage
	newStartLine := stage.startLine
	newEndLine := stage.endLine

	// Extract new lines for this stage
	var stageNewLines []string
	for j := newStartLine; j <= newEndLine && j-1 < len(b.diffBuilder.NewLines); j++ {
		if j > 0 {
			stageNewLines = append(stageNewLines, b.diffBuilder.NewLines[j-1])
		}
	}

	// Find old line range using anchors
	minOld, maxOld := b.findOldLineRange(newStartLine, newEndLine)

	if minOld > 0 && maxOld > 0 {
		// We have old lines to diff against
		stageOldLines := b.OldLines[minOld-1 : maxOld]

		// Run ComputeDiff for accurate change classification
		diff := ComputeDiff(JoinLines(stageOldLines), JoinLines(stageNewLines))

		bufferStart := minOld + b.BaseLineOffset - 1
		bufferEnd := maxOld + b.BaseLineOffset - 1

		// Build buffer line mappings using diff
		lineNumToBufferLine := make(map[int]int)
		getStageBufferRange(&Stage{rawChanges: diff.Changes}, b.BaseLineOffset+minOld-1, diff, lineNumToBufferLine)

		// Remap changes to relative line numbers
		remappedChanges := make(map[int]LineChange)
		relativeToBufferLine := make(map[int]int)

		newStart, _ := getStageNewLineRange(&Stage{rawChanges: diff.Changes})

		for lineNum, change := range diff.Changes {
			newLineNum := lineNum
			if change.NewLineNum > 0 {
				newLineNum = change.NewLineNum
			}
			relativeLine := newLineNum - newStart + 1

			if relativeLine > 0 && (relativeLine <= len(stageNewLines) || change.Type == ChangeDeletion) {
				relativeToBufferLine[relativeLine] = lineNumToBufferLine[lineNum]
				remapped := change
				remapped.NewLineNum = relativeLine
				remappedChanges[relativeLine] = remapped
			}
		}

		ctx := &StageContext{
			BufferStart:         bufferStart,
			CursorRow:           b.CursorRow,
			CursorCol:           b.CursorCol,
			LineNumToBufferLine: relativeToBufferLine,
		}
		groups, cursorLine, cursorCol := FinalizeStageGroups(remappedChanges, stageNewLines, ctx)

		stage.BufferStart = bufferStart
		stage.BufferEnd = bufferEnd
		stage.Lines = stageNewLines
		stage.Changes = remappedChanges
		stage.Groups = groups
		stage.CursorLine = cursorLine
		stage.CursorCol = cursorCol
	} else {
		// Pure addition — no old lines to diff against
		// Compute buffer start from the anchor (line before insertion)
		bufferStart := b.BaseLineOffset + len(b.OldLines)

		// Find the anchor from the streaming changes
		for lineNum, change := range stage.rawChanges {
			bufLine := b.diffBuilder.LineMapping.GetBufferLine(change, lineNum, b.BaseLineOffset)
			if bufferStart == b.BaseLineOffset+len(b.OldLines) || bufLine < bufferStart {
				bufferStart = bufLine
			}
		}

		// Build addition changes with relative line numbers
		remappedChanges := make(map[int]LineChange)
		relativeToBufferLine := make(map[int]int)
		for i, line := range stageNewLines {
			relativeLine := i + 1
			absoluteNewLine := newStartLine + i
			remappedChanges[relativeLine] = LineChange{
				Type:       ChangeAddition,
				OldLineNum: -1,
				NewLineNum: relativeLine,
				Content:    line,
			}
			if origChange, ok := b.diffBuilder.Changes[absoluteNewLine]; ok {
				relativeToBufferLine[relativeLine] = b.diffBuilder.LineMapping.GetBufferLine(
					origChange, absoluteNewLine, b.BaseLineOffset)
			}
		}

		ctx := &StageContext{
			BufferStart:         bufferStart,
			CursorRow:           b.CursorRow,
			CursorCol:           b.CursorCol,
			LineNumToBufferLine: relativeToBufferLine,
		}
		groups, cursorLine, cursorCol := FinalizeStageGroups(remappedChanges, stageNewLines, ctx)

		stage.BufferStart = bufferStart
		stage.BufferEnd = bufferStart
		stage.Lines = stageNewLines
		stage.Changes = remappedChanges
		stage.Groups = groups
		stage.CursorLine = cursorLine
		stage.CursorCol = cursorCol
	}

	b.finalizedStages = append(b.finalizedStages, stage)
	b.currentStage = nil

	return stage
}

// computeStageBufferRange computes the buffer range for a stage
func (b *IncrementalStageBuilder) computeStageBufferRange(stage *Stage) (int, int) {
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
	if b.diffBuilder.LineMapping != nil && lineNum > 0 && lineNum <= len(b.diffBuilder.LineMapping.NewToOld) {
		oldLine := b.diffBuilder.LineMapping.NewToOld[lineNum-1]
		if oldLine > 0 {
			return oldLine + b.BaseLineOffset - 1
		}
	}
	return lineNum + b.BaseLineOffset - 1
}

// Finalize completes the build and returns all stages using the batch pipeline.
// This runs ComputeDiff + CreateStages on the full old/new text, guaranteeing
// identical results to the batch code path.
func (b *IncrementalStageBuilder) Finalize() *StagingResult {
	defer logger.Trace("IncrementalStageBuilder.Finalize")()

	oldText := JoinLines(b.OldLines)
	newText := JoinLines(b.diffBuilder.NewLines)

	diff := ComputeDiff(oldText, newText)

	return CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          b.CursorRow,
		CursorCol:          b.CursorCol,
		ViewportTop:        b.ViewportTop,
		ViewportBottom:     b.ViewportBottom,
		BaseLineOffset:     b.BaseLineOffset,
		ProximityThreshold: b.ProximityThreshold,
		MaxLines:           b.MaxVisibleLines,
		NewLines:           b.diffBuilder.NewLines,
		OldLines:           b.OldLines,
		FilePath:           b.FilePath,
	})
}
