package engine

import (
	"cursortab/assert"
	"cursortab/text"
	"cursortab/types"
	"fmt"
	"testing"
)

func TestCheckTypingMatchesPrediction_NoCompletions(t *testing.T) {
	buf := newMockBuffer()
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	matches, hasRemaining := eng.checkTypingMatchesPrediction()
	assert.False(t, matches, "matches when no completions")
	assert.False(t, hasRemaining, "hasRemaining when no completions")
}

func TestCheckTypingMatchesPrediction_MatchesPrefix(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"hello wo"}
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	eng.completions = []*types.Completion{{
		StartLine:  1,
		EndLineInc: 1,
		Lines:      []string{"hello world"},
	}}
	eng.completionOriginalLines = []string{"hello "}

	matches, hasRemaining := eng.checkTypingMatchesPrediction()
	assert.True(t, matches, "match when buffer is prefix of target")
	assert.True(t, hasRemaining, "hasRemaining when buffer hasn't fully matched target")
}

func TestCheckTypingMatchesPrediction_FullyTyped(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"hello world"}
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	eng.completions = []*types.Completion{{
		StartLine:  1,
		EndLineInc: 1,
		Lines:      []string{"hello world"},
	}}
	eng.completionOriginalLines = []string{"hello "}

	matches, hasRemaining := eng.checkTypingMatchesPrediction()
	assert.True(t, matches, "match when buffer matches target")
	assert.False(t, hasRemaining, "hasRemaining when buffer fully matches target")
}

func TestCheckTypingMatchesPrediction_NoMatch(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"hello universe"}
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	eng.completions = []*types.Completion{{
		StartLine:  1,
		EndLineInc: 1,
		Lines:      []string{"hello world"},
	}}
	eng.completionOriginalLines = []string{"hello "}

	matches, _ := eng.checkTypingMatchesPrediction()
	assert.False(t, matches, "match when buffer diverges from target")
}

func TestCheckTypingMatchesPrediction_MultiLine(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"line 1", "line 2 co"}
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	eng.completions = []*types.Completion{{
		StartLine:  1,
		EndLineInc: 2,
		Lines:      []string{"line 1", "line 2 complete"},
	}}
	eng.completionOriginalLines = []string{"line 1", "line 2 "}

	matches, hasRemaining := eng.checkTypingMatchesPrediction()
	assert.True(t, matches, "match for multi-line partial completion")
	assert.True(t, hasRemaining, "hasRemaining for multi-line partial completion")
}

func TestCheckTypingMatchesPrediction_DeletionNotSupported(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"line 1"}
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	eng.completions = []*types.Completion{{
		StartLine:  1,
		EndLineInc: 2,
		Lines:      []string{"combined line"},
	}}
	eng.completionOriginalLines = []string{"line 1", "line 2"}

	matches, _ := eng.checkTypingMatchesPrediction()
	assert.False(t, matches, "match when completion deletes lines")
}

func TestHandleCursorTarget_Disabled(t *testing.T) {
	buf := newMockBuffer()
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)
	eng.config.CursorPrediction.Enabled = false

	eng.cursorTarget = &types.CursorPredictionTarget{LineNumber: 10}
	eng.state = stateHasCursorTarget

	eng.handleCursorTarget()

	assert.Equal(t, stateIdle, eng.state, "state when cursor prediction disabled")
	assert.Nil(t, eng.cursorTarget, "cursorTarget when cursor prediction disabled")
}

func TestHandleCursorTarget_CloseEnough(t *testing.T) {
	buf := newMockBuffer()
	buf.row = 8
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)
	eng.config.CursorPrediction.Enabled = true
	eng.config.CursorPrediction.ProximityThreshold = 3

	eng.cursorTarget = &types.CursorPredictionTarget{LineNumber: 10}

	eng.handleCursorTarget()

	assert.Equal(t, stateIdle, eng.state, "state when close enough")
}

func TestHandleCursorTarget_FarAway(t *testing.T) {
	buf := newMockBuffer()
	buf.row = 1
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)
	eng.config.CursorPrediction.Enabled = true
	eng.config.CursorPrediction.ProximityThreshold = 3

	eng.cursorTarget = &types.CursorPredictionTarget{LineNumber: 10}

	eng.handleCursorTarget()

	assert.Equal(t, stateHasCursorTarget, eng.state, "state when far away")
	assert.Equal(t, 10, buf.showCursorTargetLine, "showCursorTargetLine")
}

func TestHandleCursorTarget_StageNeedsNavigationCapturesRejectedCompletionCandidate(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{
		"line 1", "line 2", "line 3", "line 4", "line 5",
		"line 6", "line 7", "line 8", "line 9", "old line 10",
	}
	buf.row = 10
	buf.viewportTop = 1
	buf.viewportBottom = 5
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)
	eng.config.CursorPrediction.Enabled = true
	eng.config.CursorPrediction.ProximityThreshold = 3

	eng.cursorTarget = &types.CursorPredictionTarget{LineNumber: 10}
	eng.stagedCompletion = &text.StagedCompletion{
		CurrentIdx: 0,
		Stages: []*text.Stage{{
			BufferStart: 10,
			BufferEnd:   10,
			Lines:       []string{"new line 10"},
			Groups: []*text.Group{{
				Type:       "modification",
				BufferLine: 10,
				StartLine:  1,
				EndLine:    1,
				Lines:      []string{"new line 10"},
				OldLines:   []string{"old line 10"},
			}},
		}},
	}

	eng.handleCursorTarget()

	assert.Equal(t, stateHasCursorTarget, eng.state, "state when next stage still needs navigation")
	assert.Equal(t, 10, buf.showCursorTargetLine, "cursor target line for next stage")
	assert.NotNil(t, eng.currentRejectedCompletion, "stage cursor target should capture rejection candidate")
}

// TestProcessCompletion_TailTrimModelOverrun tests that when the model generates
// content beyond the editable range, trailing lines that match post-editable
// buffer content are trimmed, preventing duplicated code.
func TestProcessCompletion_TailTrimModelOverrun(t *testing.T) {
	buf := newMockBuffer()
	// Editable range is lines 1-5. Lines 6-10 are post-editable.
	buf.lines = []string{
		"  if (!article) {",      // 1 - editable
		"    return false;",      // 2 - editable
		"  }",                    // 3 - editable
		"",                       // 4 - editable
		"  if (tags === null) {", // 5 - editable
		"    tags = tag;",        // 6 - post-editable
		"  }",                    // 7 - post-editable
		"",                       // 8 - post-editable
		"  inc('added');",        // 9 - post-editable
		"",                       // 10 - post-editable
	}
	buf.row = 3
	buf.col = 0
	buf.viewportTop = 1
	buf.viewportBottom = 20
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	// Model generates 10 lines for a 5-line editable range.
	// Lines 6-10 duplicate (with modification) post-editable buffer content.
	// Lines 8-10 match exactly; lines 6-7 are modified.
	comp := &types.Completion{
		StartLine:  1,
		EndLineInc: 5,
		Lines: []string{
			"  if (!article) {", // 1 - same
			"    return false;", // 2 - same
			"  }",               // 3 - same
			"",                  // 4 - same
			"  tags.push(tag);", // 5 - modified (within editable)
			"  tags.push(tag);", // 6 - model overrun (different from buffer)
			"  }",               // 7 - model overrun (matches buffer)
			"",                  // 8 - matches buffer
			"  inc('added');",   // 9 - matches buffer
			"",                  // 10 - matches buffer
		},
	}

	eng.processCompletion(comp)

	// After tail-trim, lines 7-10 should be trimmed (they match buffer[7:10]).
	// The completion should only produce changes within/near the editable range,
	// not duplicate post-editable content.
	if eng.stagedCompletion != nil {
		for _, stage := range eng.stagedCompletion.Stages {
			for _, g := range stage.Groups {
				// No group should reference content from deep in post-editable range
				if g.Type == "addition" {
					for _, line := range g.Lines {
						assert.True(t, line != "  inc('added');",
							"inc('added') should not appear as addition — it already exists in buffer")
					}
				}
			}
		}
	}
}

// TestProcessCompletion_NoSpuriousAdditions tests that processCompletion does not show
// already-existing buffer lines as additions when a FIM completion's Lines span beyond EndLineInc.
//
// This reproduces the FIM provider bug where, after accepting a first streaming stage
// (which inserts lines below the cursor), a follow-up completion uses EndLineInc=CursorRow
// to extract originalLines. Because those newly-accepted lines are not included in
// originalLines, ComputeDiff treats them as additions, duplicating content already in the buffer.
func TestProcessCompletion_NoSpuriousAdditions(t *testing.T) {
	buf := newMockBuffer()
	// Buffer after first FIM accept: 6 lines total
	buf.lines = []string{
		"import numpy as np",
		"",
		"def bubble_sort(arr):",
		"    n = len(arr)",
		"    for i in range(n):",
		"        for j in range(0, n - i - 1):",
	}
	buf.row = 5 // cursor at line 5 (moved after first accept)
	buf.col = 23
	buf.viewportTop = 1
	buf.viewportBottom = 20
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	// Completion from FIM: StartLine=EndLineInc=3 (original cursor row), but Lines
	// spans lines 3-9 — including lines 4-6 that were already accepted into the buffer.
	comp := &types.Completion{
		StartLine:  3,
		EndLineInc: 3,
		Lines: []string{
			"def bubble_sort(arr):",
			"    n = len(arr)",
			"    for i in range(n):",
			"        for j in range(0, n - i - 1):",
			"            if arr[j] > arr[j + 1]:",
			"                arr[j], arr[j + 1] = arr[j + 1], arr[j]",
			"    return arr",
		},
	}

	result := eng.processCompletion(comp) == completionShown
	assert.True(t, result, "processCompletion should show remaining changes")

	if eng.stagedCompletion != nil && len(eng.stagedCompletion.Stages) > 0 {
		for _, stage := range eng.stagedCompletion.Stages {
			for _, g := range stage.Groups {
				assert.True(t, g.BufferLine > 6,
					fmt.Sprintf("should not show already-accepted buffer lines 1-6 as changes, got %q at buffer_line %d", g.Type, g.BufferLine))
			}
		}
	}
}
