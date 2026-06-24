package engine

import (
	"cursortab/assert"
	"cursortab/text"
	"cursortab/types"
	"testing"
)

func TestStreamingKeepPartial_FullyTypedDoesNotCacheRejection(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"hello world"} // User typed the full completion
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	eng.state = stateStreamingCompletion
	eng.streamingState = &streamingState{}
	showDisplayedCompletionForTest(
		eng,
		&types.Completion{
			StartLine:  1,
			EndLineInc: 1,
			Lines:      []string{"hello world"},
		},
		[]string{"hello "},
		nil,
	)
	eng.display.setRejectionCandidate(&rejectedCompletion{
		filePath:   buf.Path(),
		startLine:  1,
		endLineInc: 1,
		beforeLine: "",
		afterLine:  "",
		oldLines:   []string{"hello"},
		lines:      []string{"hello world"},
	})
	eng.streamLinesChan = make(chan string)

	eng.doRejectStreamingAndDebounce()

	assert.Equal(t, stateIdle, eng.state, "state after fully typing line-streamed completion")
	assert.Nil(t, eng.rejectedCompletions[buf.Path()], "fully typed streamed completion should not populate rejection cache")
}

func TestStreamingReject_NoKeepPartial(t *testing.T) {
	buf := newMockBuffer()
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	eng.state = stateStreamingCompletion
	eng.streamingState = &streamingState{}
	eng.streamLinesChan = make(chan string)

	eng.doRejectStreamingAndDebounce()

	assert.Equal(t, stateIdle, eng.state, "state after rejecting line streaming")
}

func TestStreamCompleteAfterAccept_UsesCursorTargetOnlyResponse(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"line 1", "line 2"}
	buf.row = 1
	buf.col = 0
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	eng.handleStreamCompleteAfterAccept(&types.CompletionResponse{
		CursorTarget: &types.CursorPredictionTarget{
			LineNumber:      10,
			ShouldRetrigger: true,
		},
	}, false)

	assert.Equal(t, stateHasCursorTarget, eng.state, "cursor target should survive accepted stream finish")
	assert.Equal(t, 10, buf.showCursorTargetLine, "cursor target line")
}

func TestStreamCompleteAfterAccept_PreservesManualFlagThroughCachedPrefetch(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{
		"line 1", "line 2", "line 3", "line 4", "line 5",
		"line 6", "line 7", "line 8", "line 9", "old line 10",
	}
	buf.row = 1
	buf.col = 0
	buf.viewportTop = 1
	buf.viewportBottom = 20
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)
	eng.config.CursorPrediction.ProximityThreshold = 3

	eng.handleStreamCompleteAfterAccept(&types.CompletionResponse{
		Completion: &types.Completion{
			StartLine:  10,
			EndLineInc: 10,
			Lines:      []string{"new line 10"},
		},
	}, true)

	assert.Equal(t, stateHasCursorTarget, eng.state, "far stream finish should first show cursor target")
	assert.Equal(t, 10, buf.showCursorTargetLine, "cursor target line")

	eng.acceptCursorTarget()

	assert.NotNil(t, eng.currentSnapshot, "metrics snapshot")
	assert.True(t, eng.currentSnapshot.ManuallyTriggered, "manual flag follows cached stream completion")
}

func TestRenderStreamedStage_SuppressedBeforeRender(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"hello"}
	buf.row = 1
	buf.col = 5
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	eng.display.setRejectionCandidate(&rejectedCompletion{
		filePath:   buf.Path(),
		startLine:  1,
		endLineInc: 1,
		beforeLine: "",
		afterLine:  "",
		oldLines:   []string{"hello"},
		lines:      []string{"hello world"},
	})
	eng.rememberRejectedCompletion()

	eng.state = stateStreamingCompletion
	eng.streamingState = &streamingState{}
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

	// The stream UI already rendered a 4-line stage. Finalize can later produce
	// a wider first stage from the full stream, but accept must advance offsets
	// from the stage that actually reached the buffer.
	eng.state = stateHasCompletion
	showDisplayedCompletionWithBatchForTest(
		eng,
		&types.Completion{
			StartLine:  3,
			EndLineInc: 3,
			Lines: []string{
				"def bubble_sort(arr):",
				"    n = len(arr)",
				"    for i in range(n):",
				"        for j in range(0, n - i - 1):",
			},
		},
		&mockBatch{},
		[]string{"def bubb"},
		[]*text.Group{
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
		},
	)

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

	eng.acceptCompletion()

	if eng.stagedCompletion != nil && eng.stagedCompletion.CurrentIdx < len(eng.stagedCompletion.Stages) {
		nextStage := eng.stagedCompletion.Stages[eng.stagedCompletion.CurrentIdx]
		assert.Equal(t, 7, nextStage.BufferStart, "next stage BufferStart should be adjusted by actual rendered line count offset (4-1=3)")
	} else {
		if eng.cursorTarget != nil {
			assert.True(t, int(eng.cursorTarget.LineNumber) > 6,
				"cursor target should be beyond the applied content (line 6)")
		}
	}
}
