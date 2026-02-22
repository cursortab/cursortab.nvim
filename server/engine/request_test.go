package engine

import (
	"cursortab/assert"
	"cursortab/text"
	"cursortab/types"
	"testing"
)

func TestAcceptCompletion_TriggersPrefetch_ShouldRetrigger(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"hello"}
	buf.row = 1
	buf.col = 5
	prov := newMockProvider()
	clock := newMockClock()
	eng, cancel := createTestEngineWithContext(buf, prov, clock)
	defer cancel()

	eng.state = stateHasCompletion
	eng.completions = []*types.Completion{{
		StartLine:  1,
		EndLineInc: 1,
		Lines:      []string{"hello world"},
	}}
	eng.applyBatch = &mockBatch{}
	eng.stagedCompletion = nil
	eng.cursorTarget = &types.CursorPredictionTarget{
		LineNumber:      5,
		ShouldRetrigger: true,
	}

	eng.doAcceptCompletion(Event{Type: EventAccept})

	assert.Equal(t, prefetchWaitingForCursorPrediction, eng.prefetchState, "prefetch should be waiting for cursor prediction after accept")
}

func TestPrefetchReady_DoesNotInterruptActiveCompletion(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"line 1", "line 2", "line 3", "line 4", "line 5"}
	buf.row = 2
	buf.col = 0
	prov := newMockProvider()
	clock := newMockClock()
	eng, cancel := createTestEngineWithContext(buf, prov, clock)
	defer cancel()

	eng.state = stateHasCompletion
	eng.completions = []*types.Completion{{
		StartLine:  2,
		EndLineInc: 2,
		Lines:      []string{"modified line 2"},
	}}
	eng.applyBatch = &mockBatch{}

	eng.prefetchState = prefetchWaitingForCursorPrediction
	eng.prefetchedCompletions = []*types.Completion{{
		StartLine:  4,
		EndLineInc: 4,
		Lines:      []string{"modified line 4"},
	}}

	initialShowCursorTargetCalls := buf.showCursorTargetLine

	eng.handlePrefetchReady(&types.CompletionResponse{
		Completions: eng.prefetchedCompletions,
	})

	assert.Equal(t, stateHasCompletion, eng.state, "state should remain HasCompletion, not interrupted by cursor prediction")
	assert.Equal(t, initialShowCursorTargetCalls, buf.showCursorTargetLine, "should not show cursor target while completion is active")
	assert.Equal(t, prefetchReady, eng.prefetchState, "prefetch state should be ready")
}

func TestAcceptLastStage_UsesPrefetchForCursorPrediction(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{
		"line 1", "line 2", "line 3", "line 4", "line 5",
		"line 6", "line 7", "line 8", "line 9", "old 10",
		"line 11", "line 12", "line 13", "line 14", "old 15",
		"line 16", "line 17", "line 18", "line 19", "line 20",
		"line 21", "line 22", "line 23", "line 24", "old 25",
	}
	buf.row = 15
	buf.col = 0
	buf.viewportTop = 1
	buf.viewportBottom = 30
	prov := newMockProvider()
	clock := newMockClock()
	eng, cancel := createTestEngineWithContext(buf, prov, clock)
	defer cancel()

	eng.state = stateHasCompletion
	eng.completions = []*types.Completion{{
		StartLine:  15,
		EndLineInc: 15,
		Lines:      []string{"new 15"},
	}}
	eng.applyBatch = &mockBatch{}

	eng.stagedCompletion = &text.StagedCompletion{
		CurrentIdx: 1,
		Stages: []*text.Stage{
			&text.Stage{
				BufferStart: 10,
				BufferEnd:   10,
				Lines:       []string{"new 10"},
				CursorTarget: &types.CursorPredictionTarget{
					LineNumber:      15,
					ShouldRetrigger: false,
				},
			},
			&text.Stage{
				BufferStart: 15,
				BufferEnd:   15,
				Lines:       []string{"new 15"},
				Groups: []*text.Group{{
					Type:       "modification",
					BufferLine: 15,
					Lines:      []string{"new 15"},
				}},
				CursorTarget: &types.CursorPredictionTarget{
					LineNumber:      15,
					ShouldRetrigger: true,
				},
				IsLastStage: true,
			},
		},
	}

	eng.cursorTarget = &types.CursorPredictionTarget{
		LineNumber:      15,
		ShouldRetrigger: true,
	}

	eng.prefetchState = prefetchReady
	eng.prefetchedCompletions = []*types.Completion{{
		StartLine:  25,
		EndLineInc: 25,
		Lines:      []string{"new 25"},
	}}

	eng.doAcceptCompletion(Event{Type: EventAccept})

	assert.Equal(t, stateHasCursorTarget, eng.state, "should be HasCursorTarget showing prediction to line 25")
	assert.Equal(t, 25, buf.showCursorTargetLine, "should show cursor target at line 25")
}

func TestAcceptLastStage_ClearsStalePrefetch_WhenOverlaps(t *testing.T) {
	// Test that prefetch is cleared when it overlaps with the stage just applied.
	// This prevents showing the same content twice after accepting a stage.
	buf := newMockBuffer()
	buf.lines = []string{
		"line 1", "line 2", "line 3", "line 4", "line 5",
		"line 6", "line 7", "line 8", "line 9", "old 10",
		"line 11", "line 12", "line 13", "line 14", "old 15",
	}
	buf.row = 15
	buf.col = 0
	buf.viewportTop = 1
	buf.viewportBottom = 20
	prov := newMockProvider()
	clock := newMockClock()
	eng, cancel := createTestEngineWithContext(buf, prov, clock)
	defer cancel()

	eng.state = stateHasCompletion
	eng.completions = []*types.Completion{{
		StartLine:  15,
		EndLineInc: 15,
		Lines:      []string{"new 15"},
	}}
	eng.applyBatch = &mockBatch{}

	eng.stagedCompletion = &text.StagedCompletion{
		CurrentIdx: 1,
		Stages: []*text.Stage{
			&text.Stage{
				BufferStart: 10,
				BufferEnd:   10,
				Lines:       []string{"new 10"},
				CursorTarget: &types.CursorPredictionTarget{
					LineNumber:      15,
					ShouldRetrigger: false,
				},
			},
			&text.Stage{
				BufferStart: 15,
				BufferEnd:   15,
				Lines:       []string{"new 15"},
				Groups: []*text.Group{{
					Type:       "modification",
					BufferLine: 15,
					Lines:      []string{"new 15"},
				}},
				CursorTarget: &types.CursorPredictionTarget{
					LineNumber:      15,
					ShouldRetrigger: true,
				},
				IsLastStage: true,
			},
		},
	}

	eng.cursorTarget = &types.CursorPredictionTarget{
		LineNumber:      15,
		ShouldRetrigger: true,
	}

	// Prefetch is for line 15 - same as the stage being applied (overlaps)
	eng.prefetchState = prefetchReady
	eng.prefetchedCompletions = []*types.Completion{{
		StartLine:  15,
		EndLineInc: 15,
		Lines:      []string{"new 15"},
	}}

	eng.doAcceptCompletion(Event{Type: EventAccept})

	// Stale prefetch should be cleared because it overlaps with the applied stage.
	// Then a new prefetch is requested (since ShouldRetrigger=true).
	assert.Equal(t, prefetchWaitingForCursorPrediction, eng.prefetchState, "new prefetch should be requested after clearing stale")
	assert.Nil(t, eng.prefetchedCompletions, "stale prefetched completions should be nil")
}

func TestPartialAccept_FinishTriggersPrefetch_ShouldRetrigger(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"hello"}
	buf.row = 1
	buf.col = 5
	prov := newMockProvider()
	clock := newMockClock()
	eng, cancel := createTestEngineWithContext(buf, prov, clock)
	defer cancel()

	eng.state = stateHasCompletion
	eng.completions = []*types.Completion{{
		StartLine:  1,
		EndLineInc: 1,
		Lines:      []string{"hello!"},
	}}
	eng.completionOriginalLines = []string{"hello"}
	eng.currentGroups = []*text.Group{{
		Type:       "modification",
		BufferLine: 1,
		RenderHint: "append_chars",
		ColStart:   5,
		Lines:      []string{"hello!"},
	}}
	eng.cursorTarget = &types.CursorPredictionTarget{
		LineNumber:      5,
		ShouldRetrigger: true,
	}
	eng.stagedCompletion = nil

	initialSyncCalls := buf.syncCalls

	eng.doPartialAcceptCompletion(Event{Type: EventPartialAccept})

	assert.True(t, buf.syncCalls > initialSyncCalls, "buffer should be synced after finish")
	assert.Equal(t, prefetchWaitingForCursorPrediction, eng.prefetchState, "prefetch should be waiting for cursor prediction")
}

func TestPartialAccept_FinishTriggersPrefetch_N1Stage(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"line 1", "line 2", "line 3"}
	buf.row = 1
	prov := newMockProvider()
	clock := newMockClock()
	eng, cancel := createTestEngineWithContext(buf, prov, clock)
	defer cancel()

	stage0Groups := []*text.Group{{
		Type:       "modification",
		BufferLine: 1,
		Lines:      []string{"new line 1"},
	}}

	eng.state = stateHasCompletion
	eng.completions = []*types.Completion{{
		StartLine:  1,
		EndLineInc: 1,
		Lines:      []string{"new line 1"},
	}}
	eng.completionOriginalLines = []string{"line 1"}
	eng.currentGroups = stage0Groups

	eng.cursorTarget = &types.CursorPredictionTarget{
		LineNumber:      2,
		ShouldRetrigger: false,
	}

	eng.stagedCompletion = &text.StagedCompletion{
		CurrentIdx: 0,
		Stages: []*text.Stage{
			&text.Stage{
				BufferStart: 1,
				BufferEnd:   1,
				Lines:       []string{"new line 1"},
				Groups:      stage0Groups,
				CursorTarget: &types.CursorPredictionTarget{
					LineNumber:      2,
					ShouldRetrigger: false,
				},
			},
			&text.Stage{
				BufferStart: 2,
				BufferEnd:   2,
				Lines:       []string{"new line 2"},
				Groups:      []*text.Group{{Type: "modification", BufferLine: 2, Lines: []string{"new line 2"}}},
				CursorTarget: &types.CursorPredictionTarget{
					LineNumber:      3,
					ShouldRetrigger: true,
				},
			},
		},
	}

	eng.doPartialAcceptCompletion(Event{Type: EventPartialAccept})

	assert.NotNil(t, eng.stagedCompletion, "stagedCompletion should not be nil")
	assert.Equal(t, 1, eng.stagedCompletion.CurrentIdx, "should be at stage 1")
	assert.Equal(t, prefetchWaitingForCursorPrediction, eng.prefetchState, "prefetch should be waiting for cursor prediction at n-1 stage")
}

// TestTryShowPrefetchedCompletion_WithChanges tests that tryShowPrefetchedCompletion
// successfully shows a completion when the prefetch has changes.
func TestTryShowPrefetchedCompletion_WithChanges(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"line 1", "old line 2", "line 3"}
	buf.row = 2
	buf.col = 0
	buf.viewportTop = 1
	buf.viewportBottom = 10
	prov := newMockProvider()
	clock := newMockClock()
	eng, cancel := createTestEngineWithContext(buf, prov, clock)
	defer cancel()

	eng.state = stateIdle
	eng.prefetchState = prefetchReady
	eng.prefetchedCompletions = []*types.Completion{{
		StartLine:  1,
		EndLineInc: 3,
		Lines:      []string{"line 1", "new line 2", "line 3"},
	}}

	result := eng.tryShowPrefetchedCompletion()

	assert.True(t, result, "should return true when prefetch has changes")
	assert.Equal(t, stateHasCompletion, eng.state, "should transition to HasCompletion")
	assert.NotNil(t, eng.stagedCompletion, "should have staged completion")
	assert.Equal(t, prefetchNone, eng.prefetchState, "prefetch state should be cleared")
}

// TestTryShowPrefetchedCompletion_NoChanges tests that tryShowPrefetchedCompletion
// returns false when the prefetch has no changes.
func TestTryShowPrefetchedCompletion_NoChanges(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"line 1", "line 2", "line 3"}
	buf.row = 1
	buf.col = 0
	buf.viewportTop = 1
	buf.viewportBottom = 10
	prov := newMockProvider()
	clock := newMockClock()
	eng, cancel := createTestEngineWithContext(buf, prov, clock)
	defer cancel()

	eng.state = stateIdle
	eng.prefetchState = prefetchReady
	eng.prefetchedCompletions = []*types.Completion{{
		StartLine:  1,
		EndLineInc: 3,
		Lines:      []string{"line 1", "line 2", "line 3"},
	}}

	result := eng.tryShowPrefetchedCompletion()

	assert.False(t, result, "should return false when no changes")
	assert.Equal(t, prefetchNone, eng.prefetchState, "prefetch state should be cleared")
}

// TestHandlePrefetchCursorPrediction_CloseDistance tests that when the cursor is close
// to the first changed line, the prefetch is shown directly.
func TestHandlePrefetchCursorPrediction_CloseDistance(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"line 1", "line 2", "old line 3"}
	buf.row = 2
	buf.col = 0
	buf.viewportTop = 1
	buf.viewportBottom = 10
	prov := newMockProvider()
	clock := newMockClock()
	eng, cancel := createTestEngineWithContext(buf, prov, clock)
	defer cancel()

	eng.state = stateHasCursorTarget
	eng.prefetchState = prefetchWaitingForCursorPrediction
	eng.prefetchedCompletions = []*types.Completion{{
		StartLine:  1,
		EndLineInc: 3,
		Lines:      []string{"line 1", "line 2", "new line 3"},
	}}

	eng.handlePrefetchReady(&types.CompletionResponse{
		Completions: eng.prefetchedCompletions,
	})

	assert.Equal(t, stateHasCompletion, eng.state, "should show completion when cursor is close")
	assert.NotNil(t, eng.stagedCompletion, "should have staged completion")
	assert.Equal(t, prefetchNone, eng.prefetchState, "prefetch should be consumed")
}

// TestHandlePrefetchCursorPrediction_FarDistance tests that when the cursor
// is far from the first changed line, a cursor target is shown instead.
func TestHandlePrefetchCursorPrediction_FarDistance(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{
		"line 1", "line 2", "line 3", "line 4", "line 5",
		"line 6", "line 7", "line 8", "line 9", "old line 10",
	}
	buf.row = 2
	buf.col = 0
	buf.viewportTop = 1
	buf.viewportBottom = 20
	prov := newMockProvider()
	clock := newMockClock()
	eng, cancel := createTestEngineWithContext(buf, prov, clock)
	defer cancel()

	eng.state = stateHasCursorTarget
	eng.prefetchState = prefetchWaitingForCursorPrediction
	eng.prefetchedCompletions = []*types.Completion{{
		StartLine:  1,
		EndLineInc: 10,
		Lines: []string{
			"line 1", "line 2", "line 3", "line 4", "line 5",
			"line 6", "line 7", "line 8", "line 9", "new line 10",
		},
	}}

	eng.handlePrefetchReady(&types.CompletionResponse{
		Completions: eng.prefetchedCompletions,
	})

	assert.Equal(t, stateHasCursorTarget, eng.state, "should show cursor target when far")
	assert.NotNil(t, eng.cursorTarget, "should have cursor target")
	assert.Equal(t, int32(10), eng.cursorTarget.LineNumber, "cursor target should point to changed line")
	assert.Equal(t, prefetchReady, eng.prefetchState, "prefetch should be ready for later use")
}

// TestAcceptLastStage_UsesPrefetchWithAdditionalChanges tests that when accepting the last
// stage of a completion, a ready prefetch with additional changes beyond the current stage
// is used to show the next completion.
func TestAcceptLastStage_UsesPrefetchWithAdditionalChanges(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{
		"line 1",
		"line 2",
		"old line 3",
		"line 4",
		"old line 5",
	}
	buf.row = 3
	buf.col = 0
	buf.viewportTop = 1
	buf.viewportBottom = 10
	prov := newMockProvider()
	clock := newMockClock()
	eng, cancel := createTestEngineWithContext(buf, prov, clock)
	defer cancel()

	// Setup staged completion at last stage (line 3)
	eng.stagedCompletion = &text.StagedCompletion{
		CurrentIdx: 0,
		Stages: []*text.Stage{
			&text.Stage{
				BufferStart: 3,
				BufferEnd:   3,
				Lines:       []string{"new line 3"},
				Groups: []*text.Group{{
					Type:       "modification",
					BufferLine: 3,
					Lines:      []string{"new line 3"},
					OldLines:   []string{"old line 3"},
				}},
				CursorTarget: &types.CursorPredictionTarget{
					LineNumber:      3,
					ShouldRetrigger: true,
				},
				IsLastStage: true,
			},
		},
	}

	eng.state = stateHasCompletion
	eng.completions = []*types.Completion{{
		StartLine:  3,
		EndLineInc: 3,
		Lines:      []string{"new line 3"},
	}}
	eng.applyBatch = &mockBatch{}
	eng.cursorTarget = &types.CursorPredictionTarget{
		LineNumber:      3,
		ShouldRetrigger: true,
	}

	// Setup prefetch that extends beyond the current stage
	eng.prefetchState = prefetchReady
	eng.prefetchedCompletions = []*types.Completion{{
		StartLine:  1,
		EndLineInc: 5,
		Lines: []string{
			"line 1",
			"line 2",
			"new line 3",
			"line 4",
			"new line 5",
		},
	}}

	eng.doAcceptCompletion(Event{Type: EventAccept})

	// Simulate buffer update
	buf.lines[2] = "new line 3"

	assert.Equal(t, stateHasCompletion, eng.state, "should show prefetched completion")
	assert.NotNil(t, eng.stagedCompletion, "should have new staged completion from prefetch")
}

// TestTryShowPrefetchedCompletion_StaleEndLineInc tests that when a prefetch completion
// has a stale EndLineInc (computed against a buffer before stage accepts added lines),
// the extra lines are not shown as phantom additions if they already exist in the buffer.
func TestTryShowPrefetchedCompletion_StaleEndLineInc(t *testing.T) {
	buf := newMockBuffer()
	// Buffer after accepting stages 1-4: all 15 lines are present
	buf.lines = []string{
		"import numpy as np",
		"",
		"def bubble_sort(arr):",
		"    n = len(arr)",
		"    for i in range(n):",
		"        for j in range(0, n-i-1):",
		"            if arr[j] > arr[j+1]:",
		"                arr[j], arr[j+1] = arr[j+1], arr[j]",
		"    return arr",
		"",
		"if __name__ == \"__main__\":",
		"    arr = np.random.randint(0, 100, 10)",
		"    print(\"Original array:\", arr)",
		"    sorted_arr = bubble_sort(arr)",
		"    print(\"Sorted array:\", sorted_arr)",
	}
	buf.row = 15
	buf.col = 0
	buf.viewportTop = 1
	buf.viewportBottom = 20
	prov := newMockProvider()
	clock := newMockClock()
	eng, cancel := createTestEngineWithContext(buf, prov, clock)
	defer cancel()

	eng.state = stateIdle

	// Prefetch was computed against a 11-line buffer (before stage 4 added 4 lines).
	// EndLineInc=11 is stale - the buffer now has 15 lines.
	// The completion's Lines match the current buffer exactly.
	eng.prefetchState = prefetchReady
	eng.prefetchedCompletions = []*types.Completion{{
		StartLine:  1,
		EndLineInc: 11,
		Lines: []string{
			"import numpy as np",
			"",
			"def bubble_sort(arr):",
			"    n = len(arr)",
			"    for i in range(n):",
			"        for j in range(0, n-i-1):",
			"            if arr[j] > arr[j+1]:",
			"                arr[j], arr[j+1] = arr[j+1], arr[j]",
			"    return arr",
			"",
			"if __name__ == \"__main__\":",
			"    arr = np.random.randint(0, 100, 10)",
			"    print(\"Original array:\", arr)",
			"    sorted_arr = bubble_sort(arr)",
			"    print(\"Sorted array:\", sorted_arr)",
		},
	}}

	result := eng.tryShowPrefetchedCompletion()

	// Should return false because all content already exists in the buffer.
	// Without the EndLineInc fix, this would return true and show lines 12-15
	// as phantom additions even though they're already present.
	assert.False(t, result, "should return false when prefetch content already in buffer")
}

// TestAcceptLastStage_WaitsForInflightPrefetch tests that when accepting the last stage
// and prefetch is still in-flight, the engine waits for it instead of going idle.
func TestAcceptLastStage_WaitsForInflightPrefetch(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"line 1", "old line 2", "line 3"}
	buf.row = 2
	buf.col = 0
	buf.viewportTop = 1
	buf.viewportBottom = 10
	prov := newMockProvider()
	clock := newMockClock()
	eng, cancel := createTestEngineWithContext(buf, prov, clock)
	defer cancel()

	// Setup staged completion at last stage
	eng.stagedCompletion = &text.StagedCompletion{
		CurrentIdx: 0,
		Stages: []*text.Stage{
			&text.Stage{
				BufferStart: 2,
				BufferEnd:   2,
				Lines:       []string{"new line 2"},
				Groups: []*text.Group{{
					Type:       "modification",
					BufferLine: 2,
					Lines:      []string{"new line 2"},
					OldLines:   []string{"old line 2"},
				}},
				CursorTarget: &types.CursorPredictionTarget{
					LineNumber:      2,
					ShouldRetrigger: true,
				},
				IsLastStage: true,
			},
		},
	}

	eng.state = stateHasCompletion
	eng.completions = []*types.Completion{{
		StartLine:  2,
		EndLineInc: 2,
		Lines:      []string{"new line 2"},
	}}
	eng.applyBatch = &mockBatch{}
	eng.cursorTarget = &types.CursorPredictionTarget{
		LineNumber:      2,
		ShouldRetrigger: true,
	}

	// Prefetch is in-flight (not ready yet)
	eng.prefetchState = prefetchInFlight

	eng.doAcceptCompletion(Event{Type: EventAccept})

	// Should wait for prefetch instead of triggering a new request
	assert.Equal(t, prefetchWaitingForTab, eng.prefetchState, "should be waiting for prefetch")
	assert.Equal(t, stateIdle, eng.state, "should clear UI while waiting")
}
