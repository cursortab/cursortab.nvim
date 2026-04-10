package text

import (
	"cursortab/logger"
)

// IncrementalDiffBuilder builds diff results incrementally as lines stream in.
// It uses exact matching only during streaming for stage boundary detection.
// Accurate change classification is deferred to ComputeDiff at stage finalization.
type IncrementalDiffBuilder struct {
	OldLines    []string     // Original content lines
	NewLines    []string     // Accumulated new content lines
	Changes     []LineChange // Accumulated changes
	LineMapping *LineMapping // Coordinate mapping between old and new

	// Tracking state
	oldLineIdx   int          // Current position in old lines (0-indexed)
	usedOldLines map[int]bool // Old line indices that have been matched
}

// NewIncrementalDiffBuilder creates a new incremental diff builder
func NewIncrementalDiffBuilder(oldLines []string) *IncrementalDiffBuilder {
	return &IncrementalDiffBuilder{
		OldLines:     oldLines,
		NewLines:     []string{},
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
		b.Changes = append(b.Changes, change)
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

// IncrementalStageBuilder accumulates streamed lines and detects when the first
// stage boundary occurs (a gap of unchanged lines after changes, or enough
// change lines to hit MaxVisibleLines). Stage content is always produced by the
// batch pipeline (ComputeDiff + CreateStages), so false-positive line matches
// only affect gap detection timing, never stage correctness.
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
	AvailableWidth     int

	// State
	diffBuilder          *IncrementalDiffBuilder
	hasChanges           bool // Whether any change has been seen
	changeCount          int  // Number of change lines seen so far
	lastChangeBufferLine int  // Buffer line of the last change (for gap detection)
	firstStageSent       bool // After first stage, only accumulate
	deferredBuild        bool // MaxVisibleLines hit; waiting for next match to build
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
	availableWidth int,
) *IncrementalStageBuilder {
	return &IncrementalStageBuilder{
		OldLines:           oldLines,
		BaseLineOffset:     baseLineOffset,
		ProximityThreshold: proximityThreshold,
		MaxVisibleLines:    maxVisibleLines,
		ViewportTop:        viewportTop,
		ViewportBottom:     viewportBottom,
		CursorRow:          cursorRow,
		CursorCol:          cursorCol,
		FilePath:           filePath,
		AvailableWidth:     availableWidth,
		diffBuilder:        NewIncrementalDiffBuilder(oldLines),
	}
}

// AddLine processes a new line and returns a stage if the first stage boundary
// is detected. After the first stage, returns nil (just accumulates for Finalize).
func (b *IncrementalStageBuilder) AddLine(line string) *Stage {
	change := b.diffBuilder.AddLine(line)
	lineNum := len(b.diffBuilder.NewLines) // 1-indexed

	if b.firstStageSent {
		return nil
	}

	if change != nil {
		b.hasChanges = true
		b.changeCount++
		bufferLine := b.diffBuilder.LineMapping.GetBufferLine(*change, b.BaseLineOffset)
		b.lastChangeBufferLine = bufferLine

		// MaxVisibleLines limit: defer stage building until the next matched
		// line so oldLineIdx advances past the change region. Building now
		// would exclude unmatched old lines (e.g., a partially-typed cursor
		// line) from the diff, causing modifications to appear as additions.
		if b.MaxVisibleLines > 0 && b.changeCount >= b.MaxVisibleLines {
			b.deferredBuild = true
		}
		// Viewport limit: additions past viewport bottom create virtual lines
		// that overflow; defer to split them into a later stage.
		if b.ViewportBottom > 0 && bufferLine > b.ViewportBottom && change.Type == ChangeAddition {
			b.deferredBuild = true
		}
		return nil
	}

	// Unchanged line (exact match found) — oldLineIdx has advanced.

	// Deferred build: maxVisibleLines was reached during changes, but we
	// waited for a match so oldLineIdx now includes the change region.
	if b.deferredBuild {
		b.firstStageSent = true
		return b.buildFirstStage()
	}

	// Check for gap after changes
	if b.hasChanges && b.lastChangeBufferLine > 0 {
		currentBufferLine := b.computeCurrentBufferLine(lineNum)
		if currentBufferLine > 0 {
			gap := currentBufferLine - b.lastChangeBufferLine
			if gap > b.ProximityThreshold {
				b.firstStageSent = true
				return b.buildFirstStage()
			}
		}
	}

	return nil
}

// buildFirstStage runs the batch pipeline on accumulated content to produce
// a correct first stage. Old lines are scoped to the streaming progress to
// avoid treating not-yet-streamed content as deletions.
func (b *IncrementalStageBuilder) buildFirstStage() *Stage {
	// Scope old lines to what streaming has covered. oldLineIdx tracks the
	// last matched old line — everything beyond hasn't been seen yet and
	// would appear as spurious deletions.
	endOld := b.diffBuilder.oldLineIdx
	if endOld > len(b.OldLines) {
		endOld = len(b.OldLines)
	}
	partialOldLines := b.OldLines[:endOld]

	oldText := JoinLines(partialOldLines)
	newText := JoinLines(b.diffBuilder.NewLines)
	diff := ComputeDiff(oldText, newText)

	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          b.CursorRow,
		CursorCol:          b.CursorCol,
		ViewportTop:        b.ViewportTop,
		ViewportBottom:     b.ViewportBottom,
		BaseLineOffset:     b.BaseLineOffset,
		ProximityThreshold: b.ProximityThreshold,
		MaxLines:           b.MaxVisibleLines,
		AvailableWidth:     b.AvailableWidth,
		NewLines:           b.diffBuilder.NewLines,
		OldLines:           partialOldLines,
		FilePath:           b.FilePath,
	})

	if result != nil && len(result.Stages) > 0 {
		return result.Stages[0]
	}
	return nil
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

	newLines := b.diffBuilder.NewLines
	if len(newLines) == 0 {
		return nil
	}

	oldText := JoinLines(b.OldLines)
	newText := JoinLines(newLines)

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
		AvailableWidth:     b.AvailableWidth,
		NewLines:           newLines,
		OldLines:           b.OldLines,
		FilePath:           b.FilePath,
	})
}
