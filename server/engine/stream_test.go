package engine

import (
	"cursortab/assert"
	"cursortab/text"
	"cursortab/types"
	"testing"
)

func TestLineStreamingKeepPartial_FullyTypedDoesNotCacheRejection(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"hello world"} // User typed the full completion
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	eng.state = stateStreamingCompletion
	eng.streamingState = &StreamingState{}
	eng.completions = []*types.Completion{{
		StartLine:  1,
		EndLineInc: 1,
		Lines:      []string{"hello world"},
	}}
	eng.completionOriginalLines = []string{"hello "}
	eng.currentRejectedCompletion = &rejectedCompletion{
		filePath:   buf.Path(),
		startLine:  1,
		endLineInc: 1,
		beforeLine: "",
		afterLine:  "",
		oldLines:   []string{"hello"},
		lines:      []string{"hello world"},
	}
	eng.streamLinesChan = make(chan string)

	eng.doRejectStreamingAndDebounce()

	assert.Equal(t, stateIdle, eng.state, "state after fully typing line-streamed completion")
	assert.Nil(t, eng.rejectedCompletions[buf.Path()], "fully typed streamed completion should not populate rejection cache")
}

func TestLineStreamingReject_NoKeepPartial(t *testing.T) {
	buf := newMockBuffer()
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	eng.state = stateStreamingCompletion
	eng.streamingState = &StreamingState{}
	eng.streamLinesChan = make(chan string)

	// Trigger text change during streaming
	eng.doRejectStreamingAndDebounce()

	// Should transition to Idle (line streaming doesn't keep partial)
	assert.Equal(t, stateIdle, eng.state, "state after rejecting line streaming")
}

func TestRenderStreamedStage_SuppressedBeforeRender(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"hello"}
	buf.row = 1
	buf.col = 5
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	eng.currentRejectedCompletion = &rejectedCompletion{
		filePath:   buf.Path(),
		startLine:  1,
		endLineInc: 1,
		beforeLine: "",
		afterLine:  "",
		oldLines:   []string{"hello"},
		lines:      []string{"hello world"},
	}
	eng.rememberRejectedCompletion()

	eng.state = stateStreamingCompletion
	eng.streamingState = &StreamingState{}
	eng.streamLinesChan = make(chan string)

	stage := &text.Stage{
		BufferStart: 1,
		BufferEnd:   1,
		Lines:       []string{"hello world"},
		Groups: []*text.Group{{
			Type:       "modification",
			StartLine:  1,
			EndLine:    1,
			BufferLine: 1,
			Lines:      []string{"hello world"},
			OldLines:   []string{"hello"},
			RenderHint: "append_chars",
			ColStart:   5,
			ColEnd:     11,
		}},
	}

	eng.renderStreamedStage(stage)

	assert.Equal(t, 0, buf.prepareCompletionCalls, "suppressed streamed stage should not render")
	assert.Greater(t, buf.clearUICalls, 0, "suppressed streamed stage should clear UI through reject")
	assert.Equal(t, stateIdle, eng.state, "state after suppressing streamed stage")
	assert.Nil(t, eng.streamingState, "streaming state after suppression")
	assert.Nil(t, eng.streamLinesChan, "stream channel after suppression")
}

// TestStreamingAccept_FinalizedStageMismatch tests that when a stage rendered during
// streaming differs from the finalized stage[0] (which Finalize() recomputes from scratch),
// accepting the rendered stage and advancing to the next stage uses correct line offsets.
//
// Reproduces the bug from cursortab.1.log where:
//  1. Streaming rendered a 4-line stage (modification + 3 additions)
//  2. Finalize() produced a 12-line stage[0] (modification + 11 additions)
//  3. After accepting the 4-line rendered stage, advanceStagedCompletion used
//     the 12-line finalized stage for offset calculation, producing wrong offsets
//  4. The next stage's BufferStart was not adjusted, showing duplicate content
func TestStreamingAccept_FinalizedStageMismatch(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{
		"import numpy as np",
		"",
		"def bubb",
	}
	buf.row = 3
	buf.col = 8
	buf.viewportTop = 1
	buf.viewportBottom = 20
	prov := newMockProvider()
	clock := newMockClock()
	eng, cancel := createTestEngineWithContext(buf, prov, clock)
	defer cancel()

	// Simulate the state after handleStreamCompleteSimple with firstStageRendered=true.
	// The rendered stage (from incremental builder) had 4 lines.
	// But Finalize() produced a stage[0] with 8 lines (different boundary).
	eng.state = stateHasCompletion
	eng.completions = []*types.Completion{{
		StartLine:  3,
		EndLineInc: 3,
		Lines: []string{
			"def bubble_sort(arr):",
			"    n = len(arr)",
			"    for i in range(n):",
			"        for j in range(0, n - i - 1):",
		},
	}}
	eng.completionOriginalLines = []string{"def bubb"}
	eng.currentGroups = []*text.Group{
		{
			Type:       "modification",
			StartLine:  1,
			EndLine:    1,
			BufferLine: 3,
			Lines:      []string{"def bubble_sort(arr):"},
			OldLines:   []string{"def bubb"},
			RenderHint: "append_chars",
			ColStart:   8,
			ColEnd:     21,
		},
		{
			Type:       "addition",
			StartLine:  2,
			EndLine:    4,
			BufferLine: 4,
			Lines: []string{
				"    n = len(arr)",
				"    for i in range(n):",
				"        for j in range(0, n - i - 1):",
			},
		},
	}
	eng.applyBatch = &mockBatch{}

	// The stagedCompletion from Finalize() has stage[0] with MORE lines than rendered.
	// This is the mismatch that causes the bug.
	eng.stagedCompletion = &text.StagedCompletion{
		CurrentIdx: 0,
		Stages: []*text.Stage{
			{
				BufferStart: 3,
				BufferEnd:   3,
				Lines: []string{
					"def bubble_sort(arr):",
					"    n = len(arr)",
					"    for i in range(n):",
					"        for j in range(0, n - i - 1):",
					"            if arr[j] > arr[j + 1]:",
					"                arr[j], arr[j + 1] = arr[j + 1], arr[j]",
					"    return arr",
					"",
				},
				Groups: []*text.Group{
					{Type: "modification", BufferLine: 3},
					{Type: "addition", BufferLine: 4},
				},
				CursorTarget: &types.CursorPredictionTarget{
					LineNumber:      4, // Points to stage[1].BufferStart (addition beyond old text)
					ShouldRetrigger: false,
				},
			},
			{
				BufferStart: 4,
				BufferEnd:   3, // Pure addition (End < Start)
				Lines: []string{
					"if __name__ == \"__main__\":",
					"    arr = np.random.randint(0, 100, 10)",
					"    print(\"Sorted array:\", sorted_arr)",
				},
				Groups: []*text.Group{
					{Type: "addition", BufferLine: 4},
				},
				CursorTarget: &types.CursorPredictionTarget{
					LineNumber:      6,
					ShouldRetrigger: true,
				},
				IsLastStage: true,
			},
		},
	}

	eng.cursorTarget = eng.stagedCompletion.Stages[0].CursorTarget

	// Simulate accepting the rendered 4-line stage
	eng.acceptCompletion()

	// After accepting the 4-line rendered stage (replacing 1 line with 4),
	// CumulativeOffset should be 4 - 1 = 3.
	// The next stage's BufferStart should be adjusted by +3 (from 4 to 7).
	//
	// BUG: advanceStagedCompletion uses the finalized stage[0]'s Lines (8 lines)
	// to compute offset = 8 - 1 = 7, giving wrong BufferStart = 4 + 7 = 11.
	// With the fix, it should use the rendered stage's actual line count (4).

	if eng.stagedCompletion != nil && eng.stagedCompletion.CurrentIdx < len(eng.stagedCompletion.Stages) {
		nextStage := eng.stagedCompletion.Stages[eng.stagedCompletion.CurrentIdx]
		// After accepting 4 lines replacing 1, offset = 3, so BufferStart should be 4 + 3 = 7
		assert.Equal(t, 7, nextStage.BufferStart, "next stage BufferStart should be adjusted by actual rendered line count offset (4-1=3)")
	} else {
		// Even if stages are exhausted, the cursor target should not point to line 4
		// (which now has content from the first accept)
		if eng.cursorTarget != nil {
			assert.True(t, int(eng.cursorTarget.LineNumber) > 6,
				"cursor target should be beyond the applied content (line 6)")
		}
	}
}
