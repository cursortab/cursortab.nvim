package engine

import (
	"cursortab/assert"
	"cursortab/text"
	"cursortab/types"
	"testing"
)

func TestStateNotifierAcceptedStream(t *testing.T) {
	eng := createTestEngine(newMockBuffer(), newMockProvider(), newMockClock())
	var status types.EngineStatus
	setStateNotifier(eng, func(value types.EngineStatus) { status = value })
	eng.state = stateStreamingCompletion
	eng.stream = &activeStream{stream: newMockCompletionStream(nil)}
	eng.currentCancel = func() {}
	showDisplayedCompletionWithBatchForTest(eng, &types.Completion{
		StartLine: 1, EndLineInc: 1, Lines: []string{"completed line 1"},
	}, &mockBatch{}, nil, nil)

	eng.handleEvent(Event{Type: EventAccept})
	assert.Equal(t, types.EngineStatus{State: "Idle", Pending: true}, status, "stream continues after acceptance")
	eng.handleStreamEvent("", false)
	assert.Equal(t, types.EngineStatus{State: "Idle"}, status, "stream finishes without a state transition")
}

func TestStreamingKeepPartial_FullyTypedDoesNotCacheRejection(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"hello world"} // User typed the full completion
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	eng.state = stateStreamingCompletion
	eng.stream = &activeStream{stream: newMockCompletionStream(nil)}
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
	eng.stream = &activeStream{stream: newMockCompletionStream(nil)}

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
	eng.stream = &activeStream{stream: newMockCompletionStream(nil)}

	stage := &text.Stage{
		BufferStart: 1,
		BufferEnd:   1,
		Lines:       []string{"hello world"},
		Groups: []*text.Group{{
			Type:       text.GroupModification,
			StartLine:  1,
			EndLine:    1,
			BufferLine: 1,
			Lines:      []string{"hello world"},
			OldLines:   []string{"hello"},
			RenderHint: text.HintAppendChars,
			ColStart:   5,
			ColEnd:     11,
		}},
	}

	eng.renderStreamedStage(stage)

	assert.Equal(t, 0, buf.prepareCompletionCalls, "suppressed streamed stage should not render")
	assert.Greater(t, buf.clearUICalls, 0, "suppressed streamed stage should clear UI through reject")
	assert.Equal(t, stateIdle, eng.state, "state after suppressing streamed stage")
	assert.Nil(t, eng.stream, "streaming state after suppression")
}

func TestStreamComplete_NoPreviewLines_AdoptsFinishResult(t *testing.T) {
	eng := createTestEngine(newMockBuffer(), newMockProvider(), newMockClock())
	stream := &mockCompletionStream{response: completionResponse(&types.Completion{
		StartLine: 1, EndLineInc: 1, Lines: []string{"updated"},
	})}

	eng.startCompletionStream(stream, false)
	eng.handleStreamComplete()

	assert.Equal(t, stateHasCompletion, eng.state, "finish result is staged")
	assert.NotNil(t, eng.stagedCompletion, "staged completion")
	if eng.stagedCompletion != nil {
		assert.Equal(t, []string{"updated"}, eng.stagedCompletion.Stages[0].Lines, "completion lines")
	}
	assert.Nil(t, eng.stream, "streaming state after finish")
}

func TestStreamComplete_NoPreviewLines_EmptyResultStaysIdle(t *testing.T) {
	eng := createTestEngine(newMockBuffer(), newMockProvider(), newMockClock())

	eng.startCompletionStream(&mockCompletionStream{response: &types.CompletionResponse{}}, false)
	eng.handleStreamComplete()

	assert.Equal(t, stateIdle, eng.state, "stream that delivers nothing keeps the engine idle")
	assert.Nil(t, eng.stream, "streaming state after finish")
}
