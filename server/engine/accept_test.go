package engine

import (
	"context"
	"cursortab/assert"
	"cursortab/text"
	"cursortab/types"
	"errors"
	"testing"
)

func TestReject(t *testing.T) {
	buf := newMockBuffer()
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	eng.state = stateHasCompletion
	eng.completions = []*types.Completion{{StartLine: 1, EndLineInc: 1, Lines: []string{"test"}}}
	eng.cursorTarget = &types.CursorPredictionTarget{LineNumber: 5}

	eng.rejectAndRemember()

	assert.Equal(t, stateIdle, eng.state, "state after reject")
	assert.Nil(t, eng.completions, "completions after reject")
	assert.Nil(t, eng.cursorTarget, "cursorTarget after reject")
	assert.Greater(t, buf.clearUICalls, 0, "ClearUI should have been called")
}

// TestReject_DropsLeftoverStreamFromAcceptDuringStreaming verifies that reject
// cancels any in-flight stream from a prior accept-during-streaming. Without
// this, the leftover stream eventually completes and handleStreamCompleteSimple
// sees acceptedDuringStreaming=true, which calls handleStreamCompleteAfterAccept
// and resurrects a completion or cursor target the user just rejected.
func TestReject_DropsLeftoverStreamFromAcceptDuringStreaming(t *testing.T) {
	buf := newMockBuffer()
	prov := newMockProvider()
	clock := newMockClock()
	eng, cancel := createTestEngineWithContext(buf, prov, clock)
	defer cancel()

	streamCtx, streamCancel := context.WithCancel(context.Background())
	eng.streamingState = &StreamingState{}
	eng.streamingCancel = streamCancel
	eng.acceptedDuringStreaming = true
	eng.state = stateHasCompletion

	eng.reject()

	assert.Nil(t, eng.streamingState, "streamingState should be cleared")
	assert.Nil(t, eng.streamingCancel, "streamingCancel should be cleared")
	assert.False(t, eng.acceptedDuringStreaming, "acceptedDuringStreaming flag should be cleared")

	select {
	case <-streamCtx.Done():
	default:
		t.Errorf("stream context should be cancelled")
	}
}

func TestAcceptCompletion_BatchExecuteError_ResetsToIdle(t *testing.T) {
	buf := newMockBuffer()
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	eng.state = stateHasCompletion
	eng.completions = []*types.Completion{{StartLine: 1, EndLineInc: 1, Lines: []string{"x"}}}
	eng.applyBatch = &mockBatch{err: errors.New("execute failed")}

	eng.acceptCompletion()

	assert.Equal(t, stateIdle, eng.state, "state should reset to idle after batch error")
	assert.Nil(t, eng.completions, "completions should be cleared after batch error")
	assert.Nil(t, eng.applyBatch, "applyBatch should be cleared after batch error")
}

func TestPartialAccept_AppendChars_SingleWord(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"func"}
	buf.row = 1
	buf.col = 4
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	eng.state = stateHasCompletion
	eng.completions = []*types.Completion{{
		StartLine:  1,
		EndLineInc: 1,
		Lines:      []string{"function foo()"},
	}}
	eng.completionOriginalLines = []string{"func"}
	eng.currentGroups = []*text.Group{{
		Type:       "modification",
		BufferLine: 1,
		RenderHint: "append_chars",
		ColStart:   4,
		Lines:      []string{"function foo()"},
	}}

	eng.partialAcceptCompletion()

	assert.Equal(t, "tion ", buf.lastInsertedText, "inserted text")
	assert.Equal(t, stateHasCompletion, eng.state, "state after partial accept")
}

func TestPartialAccept_AppendChars_Punctuation(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"foo"}
	buf.row = 1
	buf.col = 3
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	eng.state = stateHasCompletion
	eng.completions = []*types.Completion{{
		StartLine:  1,
		EndLineInc: 1,
		Lines:      []string{"foo.bar.baz"},
	}}
	eng.completionOriginalLines = []string{"foo"}
	eng.currentGroups = []*text.Group{{
		Type:       "modification",
		BufferLine: 1,
		RenderHint: "append_chars",
		ColStart:   3,
		Lines:      []string{"foo.bar.baz"},
	}}

	eng.partialAcceptCompletion()

	assert.Equal(t, ".", buf.lastInsertedText, "inserted text at punctuation")
	assert.Equal(t, stateHasCompletion, eng.state, "state after partial accept")
}

func TestPartialAccept_AppendChars_NoRemaining(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"hello"}
	buf.row = 1
	buf.col = 5
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

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

	eng.partialAcceptCompletion()

	assert.Equal(t, "!", buf.lastInsertedText, "inserted text")
	assert.Equal(t, stateIdle, eng.state, "state when nothing remaining")
}

func TestPartialAccept_MultiLine_FirstLine(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"line 1", "line 2", "line 3"}
	buf.row = 1
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	eng.state = stateHasCompletion
	eng.completions = []*types.Completion{{
		StartLine:  1,
		EndLineInc: 3,
		Lines:      []string{"new line 1", "new line 2", "new line 3"},
	}}
	eng.completionOriginalLines = []string{"line 1", "line 2", "line 3"}
	eng.currentGroups = []*text.Group{{
		Type:       "modification",
		BufferLine: 1,
		Lines:      []string{"new line 1", "new line 2", "new line 3"},
	}}

	eng.partialAcceptCompletion()

	assert.Equal(t, 1, buf.lastReplacedLine, "replaced line number")
	assert.Equal(t, "new line 1", buf.lastReplacedContent, "replaced content")
	assert.Equal(t, stateHasCompletion, eng.state, "state after partial line accept")
	assert.Equal(t, 2, len(eng.completions[0].Lines), "remaining lines")
	assert.Equal(t, 2, eng.completions[0].StartLine, "updated start line")
	assert.Equal(t, 3, eng.completions[0].EndLineInc, "end line unchanged for equal line count")
}

func TestPartialAccept_MultiLine_LastLine(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"old line"}
	buf.row = 1
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	eng.state = stateHasCompletion
	eng.completions = []*types.Completion{{
		StartLine:  1,
		EndLineInc: 1,
		Lines:      []string{"new line"},
	}}
	eng.completionOriginalLines = []string{"old line"}
	eng.currentGroups = []*text.Group{{
		Type:       "modification",
		BufferLine: 1,
		Lines:      []string{"new line"},
	}}

	eng.partialAcceptCompletion()

	assert.Equal(t, "new line", buf.lastReplacedContent, "replaced content")
	assert.Equal(t, stateIdle, eng.state, "state after accepting last line")
}

func TestPartialAccept_WithUserTyping(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"functi"}
	buf.row = 1
	buf.col = 6
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	eng.state = stateHasCompletion
	eng.completions = []*types.Completion{{
		StartLine:  1,
		EndLineInc: 1,
		Lines:      []string{"function foo()"},
	}}
	eng.completionOriginalLines = []string{"func"}
	eng.currentGroups = []*text.Group{{
		Type:       "modification",
		BufferLine: 1,
		RenderHint: "append_chars",
		ColStart:   4,
		Lines:      []string{"function foo()"},
	}}

	eng.partialAcceptCompletion()

	assert.Equal(t, "on ", buf.lastInsertedText, "inserted text after user typing")
}

func TestPartialAccept_NoCompletions(t *testing.T) {
	buf := newMockBuffer()
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	eng.state = stateHasCompletion
	eng.completions = nil

	eng.partialAcceptCompletion()

	assert.Equal(t, stateHasCompletion, eng.state, "state unchanged when no completions")
}

func TestPartialAccept_NoGroups(t *testing.T) {
	buf := newMockBuffer()
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	eng.state = stateHasCompletion
	eng.completions = []*types.Completion{{
		StartLine:  1,
		EndLineInc: 1,
		Lines:      []string{"test"},
	}}
	eng.currentGroups = nil

	eng.partialAcceptCompletion()

	assert.Equal(t, stateHasCompletion, eng.state, "state unchanged when no groups")
}

func TestPartialAccept_AdditionGroup(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"func main() {", "}"}
	buf.row = 1
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	eng.state = stateHasCompletion
	eng.completions = []*types.Completion{{
		StartLine:  1,
		EndLineInc: 2,
		Lines:      []string{"func main() {", "    fmt.Println(\"hello\")", "}"},
	}}
	eng.completionOriginalLines = []string{"func main() {", "}"}
	eng.currentGroups = []*text.Group{{
		Type:       "addition",
		BufferLine: 2,
		StartLine:  2,
		EndLine:    2,
		Lines:      []string{"    fmt.Println(\"hello\")"},
	}}

	eng.partialAcceptCompletion()

	assert.Equal(t, 1, buf.lastReplacedLine, "replaced line number")
	assert.Equal(t, "func main() {", buf.lastReplacedContent, "replaced content")
	assert.Equal(t, stateHasCompletion, eng.state, "state after first partial")
	assert.Equal(t, 2, len(eng.completions[0].Lines), "remaining lines")
	assert.Equal(t, 2, eng.completions[0].StartLine, "updated start line")
	assert.Equal(t, 2, eng.completions[0].EndLineInc, "end line preserved from original")
}

// TestPartialAccept_AppendCharsWithAddition tests that when a multi-line stage
// has an append_chars line followed by addition lines, completing the append_chars
// line transitions to the addition lines (not skipping them).
func TestPartialAccept_AppendCharsWithAddition(t *testing.T) {
	buf := newMockBuffer()
	// Buffer: line 3 is "def bubble_sort(arr):" (already complete after partial accepts)
	buf.lines = []string{"import numpy as np", "", "def bubble_sort(arr):"}
	buf.row = 3
	buf.col = 21 // At end of line
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	// Completion for stage 1: line 3 (append_chars) + line 4 (addition)
	eng.state = stateHasCompletion
	eng.completions = []*types.Completion{{
		StartLine:  3,
		EndLineInc: 3, // Only replacing line 3, but adding line 4
		Lines:      []string{"def bubble_sort(arr):", "    n = len(arr)"},
	}}
	eng.completionOriginalLines = []string{"def bubble_sort(arr):"}

	// Groups: first is append_chars (complete), second is addition
	eng.currentGroups = []*text.Group{
		{
			Type:       "modification",
			BufferLine: 3,
			StartLine:  1,
			EndLine:    1,
			Lines:      []string{"def bubble_sort(arr):"},
			OldLines:   []string{"def bubble_sort(arr):"}, // Same as current - already complete
			RenderHint: "append_chars",
			ColStart:   21, // Already at end
			ColEnd:     21,
		},
		{
			Type:       "addition",
			BufferLine: 4,
			StartLine:  2,
			EndLine:    2,
			Lines:      []string{"    n = len(arr)"},
		},
	}

	// When append_chars line is already complete, partial accept should
	// transition to the next line (the addition), NOT finalize the stage
	eng.partialAcceptCompletion()

	// After partial accept, the completion should now point to the addition line
	assert.Equal(t, stateHasCompletion, eng.state, "should still be in HasCompletion")
	assert.Equal(t, 1, len(eng.completions[0].Lines), "should have 1 remaining line")
	assert.Equal(t, "    n = len(arr)", eng.completions[0].Lines[0], "remaining line content")
	assert.Equal(t, 4, eng.completions[0].StartLine, "startLine should be 4")
}

// TestPartialAccept_StagedCompletion_UsesCurrentGroups tests that during partial
// accept with a staged completion, we use currentGroups (updated by rerenderPartial)
// not the stale stage groups. This prevents skipping addition lines when the
// append_chars group in the stage is stale but currentGroups has been updated.
func TestPartialAccept_StagedCompletion_UsesCurrentGroups(t *testing.T) {
	buf := newMockBuffer()
	// Buffer state after completing append_chars on line 3
	buf.lines = []string{"import numpy as np", "", "def bubble_sort(arr):"}
	buf.row = 3
	buf.col = 21
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	// Staged completion exists with OLD groups (before rerenderPartial updated them)
	eng.stagedCompletion = &text.StagedCompletion{
		Stages: []*text.Stage{
			&text.Stage{
				BufferStart: 3,
				BufferEnd:   3,
				Lines:       []string{"def bubble_sort(arr):", "    n = len(arr)"},
				// These groups are STALE - first group is append_chars for line 3
				Groups: []*text.Group{
					{
						Type:       "modification",
						BufferLine: 3,
						StartLine:  1,
						EndLine:    1,
						Lines:      []string{"def bubble_sort(arr):"},
						OldLines:   []string{"def bubb"},
						RenderHint: "append_chars",
						ColStart:   8,
						ColEnd:     21,
					},
					{
						Type:       "addition",
						BufferLine: 4,
						StartLine:  2,
						EndLine:    2,
						Lines:      []string{"    n = len(arr)"},
					},
				},
			},
			// Next stage
			&text.Stage{
				BufferStart: 5,
				BufferEnd:   5,
				Lines:       []string{"    for i in range(n):"},
				Groups: []*text.Group{{
					Type:       "addition",
					BufferLine: 5,
					Lines:      []string{"    for i in range(n):"},
				}},
			},
		},
		CurrentIdx: 0,
	}

	// Current state: append_chars is complete, now showing addition for line 4
	eng.state = stateHasCompletion
	eng.completions = []*types.Completion{{
		StartLine:  4,
		EndLineInc: 4,
		Lines:      []string{"    n = len(arr)"},
	}}
	eng.completionOriginalLines = []string{} // No original lines since this is an addition

	// currentGroups has been updated by rerenderPartial - just the addition group
	eng.currentGroups = []*text.Group{{
		Type:       "addition",
		BufferLine: 4,
		StartLine:  1,
		EndLine:    1,
		Lines:      []string{"    n = len(arr)"},
	}}

	// This is the key: partial accept should use currentGroups (addition),
	// NOT the staged completion's groups (which have stale append_chars first)
	eng.partialAcceptCompletion()

	// Verify the addition line was inserted
	assert.Equal(t, 4, len(buf.lines), "buffer should have 4 lines after insert")
	assert.Equal(t, "    n = len(arr)", buf.lines[3], "line 4 should be the addition")
}

func TestPartialAccept_FinishSyncsBuffer_NonStaged(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"test"}
	buf.row = 1
	buf.col = 4
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	eng.state = stateHasCompletion
	eng.completions = []*types.Completion{{
		StartLine:  1,
		EndLineInc: 1,
		Lines:      []string{"test!"},
	}}
	eng.completionOriginalLines = []string{"test"}
	eng.currentGroups = []*text.Group{{
		Type:       "modification",
		BufferLine: 1,
		RenderHint: "append_chars",
		ColStart:   4,
		Lines:      []string{"test!"},
	}}
	eng.stagedCompletion = nil
	eng.cursorTarget = nil

	initialSyncCalls := buf.syncCalls

	eng.partialAcceptCompletion()

	assert.True(t, buf.syncCalls > initialSyncCalls, "buffer should be synced after finish")
	assert.Equal(t, stateIdle, eng.state, "should be idle after finish")
}

// TestPartialAccept_MultiLineCompletion_CursorTargetConsistency tests that cursor targets
// remain consistent when using partial accept vs full accept on the same multi-line completion.
func TestPartialAccept_MultiLineCompletion_CursorTargetConsistency(t *testing.T) {
	t.Run("full_accept_preserves_cursor_target", func(t *testing.T) {
		buf := newMockBuffer()
		buf.lines = []string{"old line 1", "old line 2", "old line 3", "old line 4"}
		buf.row = 1
		prov := newMockProvider()
		clock := newMockClock()
		eng, cancel := createTestEngineWithContext(buf, prov, clock)
		defer cancel()

		eng.state = stateHasCompletion
		eng.completions = []*types.Completion{{
			StartLine:  1,
			EndLineInc: 4,
			Lines:      []string{"new line 1", "new line 2", "new line 3", "new line 4"},
		}}
		eng.completionOriginalLines = buf.lines
		eng.currentGroups = []*text.Group{
			{Type: "modification", BufferLine: 1, StartLine: 1, EndLine: 1, Lines: []string{"new line 1"}, OldLines: []string{"old line 1"}},
			{Type: "modification", BufferLine: 2, StartLine: 2, EndLine: 2, Lines: []string{"new line 2"}, OldLines: []string{"old line 2"}},
			{Type: "modification", BufferLine: 3, StartLine: 3, EndLine: 3, Lines: []string{"new line 3"}, OldLines: []string{"old line 3"}},
			{Type: "modification", BufferLine: 4, StartLine: 4, EndLine: 4, Lines: []string{"new line 4"}, OldLines: []string{"old line 4"}},
		}

		expectedCursorTarget := int32(8)
		eng.cursorTarget = &types.CursorPredictionTarget{
			RelativePath:    "test.go",
			LineNumber:      expectedCursorTarget,
			ShouldRetrigger: true,
		}
		eng.applyBatch = &mockBatch{}
		eng.stagedCompletion = nil

		eng.acceptCompletion()

		assert.Equal(t, int(expectedCursorTarget), buf.showCursorTargetLine, "cursor target should be preserved after full accept")
	})

	t.Run("partial_accept_4_lines_one_by_one_same_target", func(t *testing.T) {
		buf := newMockBuffer()
		buf.lines = []string{"old line 1", "old line 2", "old line 3", "old line 4"}
		buf.row = 1
		prov := newMockProvider()
		clock := newMockClock()
		eng, cancel := createTestEngineWithContext(buf, prov, clock)
		defer cancel()

		eng.state = stateHasCompletion
		eng.completions = []*types.Completion{{
			StartLine:  1,
			EndLineInc: 4,
			Lines:      []string{"new line 1", "new line 2", "new line 3", "new line 4"},
		}}
		eng.completionOriginalLines = buf.lines
		eng.currentGroups = []*text.Group{
			{Type: "modification", BufferLine: 1, StartLine: 1, EndLine: 1, Lines: []string{"new line 1"}, OldLines: []string{"old line 1"}},
			{Type: "modification", BufferLine: 2, StartLine: 2, EndLine: 2, Lines: []string{"new line 2"}, OldLines: []string{"old line 2"}},
			{Type: "modification", BufferLine: 3, StartLine: 3, EndLine: 3, Lines: []string{"new line 3"}, OldLines: []string{"old line 3"}},
			{Type: "modification", BufferLine: 4, StartLine: 4, EndLine: 4, Lines: []string{"new line 4"}, OldLines: []string{"old line 4"}},
		}

		expectedCursorTarget := int32(8)
		eng.cursorTarget = &types.CursorPredictionTarget{
			RelativePath:    "test.go",
			LineNumber:      expectedCursorTarget,
			ShouldRetrigger: true,
		}
		eng.stagedCompletion = nil

		eng.partialAcceptCompletion()
		assert.Equal(t, stateHasCompletion, eng.state, "should stay in HasCompletion after partial accept")
		assert.Equal(t, 3, len(eng.completions[0].Lines), "remaining lines")
		assert.Equal(t, 2, eng.completions[0].StartLine, "start line increments")

		eng.partialAcceptCompletion()
		assert.Equal(t, 2, len(eng.completions[0].Lines), "remaining lines")
		assert.Equal(t, 3, eng.completions[0].StartLine, "start line increments")

		eng.partialAcceptCompletion()
		assert.Equal(t, 1, len(eng.completions[0].Lines), "remaining lines")
		assert.Equal(t, 4, eng.completions[0].StartLine, "start line increments")

		eng.partialAcceptCompletion()

		assert.Equal(t, int(expectedCursorTarget), buf.showCursorTargetLine, "cursor target should be preserved through partial accepts")
	})

	t.Run("partial_accept_cursor_target_consistency_through_all_accepts", func(t *testing.T) {
		buf := newMockBuffer()
		buf.lines = []string{"x", "y", "z", "w"}
		buf.row = 1
		prov := newMockProvider()
		clock := newMockClock()
		eng, cancel := createTestEngineWithContext(buf, prov, clock)
		defer cancel()

		cursorTarget := int32(12)
		eng.state = stateHasCompletion
		eng.completions = []*types.Completion{{
			StartLine:  1,
			EndLineInc: 4,
			Lines:      []string{"X", "Y", "Z", "W"},
		}}
		eng.completionOriginalLines = buf.lines
		eng.currentGroups = []*text.Group{
			{Type: "modification", BufferLine: 1, StartLine: 1, EndLine: 1, Lines: []string{"X"}, OldLines: []string{"x"}},
			{Type: "modification", BufferLine: 2, StartLine: 2, EndLine: 2, Lines: []string{"Y"}, OldLines: []string{"y"}},
			{Type: "modification", BufferLine: 3, StartLine: 3, EndLine: 3, Lines: []string{"Z"}, OldLines: []string{"z"}},
			{Type: "modification", BufferLine: 4, StartLine: 4, EndLine: 4, Lines: []string{"W"}, OldLines: []string{"w"}},
		}
		eng.cursorTarget = &types.CursorPredictionTarget{
			RelativePath:    "test.go",
			LineNumber:      cursorTarget,
			ShouldRetrigger: false,
		}
		eng.stagedCompletion = nil

		for i := 0; i < 3; i++ {
			eng.partialAcceptCompletion()
			if i < 2 {
				assert.Equal(t, cursorTarget, eng.cursorTarget.LineNumber, "cursor target should be unchanged")
			}
		}

		eng.partialAcceptCompletion()

		assert.Equal(t, int(cursorTarget), buf.showCursorTargetLine, "final cursor target should be original value")
	})

	t.Run("partial_accept_with_staged_completion", func(t *testing.T) {
		buf := newMockBuffer()
		buf.lines = []string{"a", "b", "c", "d", "e", "f"}
		buf.row = 1
		prov := newMockProvider()
		clock := newMockClock()
		eng, cancel := createTestEngineWithContext(buf, prov, clock)
		defer cancel()

		stage1 := &text.Stage{
			BufferStart: 1,
			BufferEnd:   2,
			Lines:       []string{"A", "B"},
			Groups:      []*text.Group{{Type: "modification", BufferLine: 1}},
			CursorLine:  1,
			CursorCol:   0,
			IsLastStage: false,
			CursorTarget: &types.CursorPredictionTarget{
				LineNumber:      3,
				ShouldRetrigger: false,
			},
		}

		stage2 := &text.Stage{
			BufferStart: 3,
			BufferEnd:   4,
			Lines:       []string{"C", "D"},
			Groups:      []*text.Group{{Type: "modification", BufferLine: 3}},
			CursorLine:  1,
			CursorCol:   0,
			IsLastStage: true,
			CursorTarget: &types.CursorPredictionTarget{
				LineNumber:      5,
				ShouldRetrigger: true,
			},
		}

		eng.state = stateHasCompletion
		eng.completions = []*types.Completion{{
			StartLine:  1,
			EndLineInc: 2,
			Lines:      []string{"A", "B"},
		}}
		eng.completionOriginalLines = []string{"a", "b"}
		eng.currentGroups = []*text.Group{
			{Type: "modification", BufferLine: 1, StartLine: 1, EndLine: 1, Lines: []string{"A"}, OldLines: []string{"a"}},
			{Type: "modification", BufferLine: 2, StartLine: 2, EndLine: 2, Lines: []string{"B"}, OldLines: []string{"b"}},
		}
		eng.stagedCompletion = &text.StagedCompletion{
			Stages:     []*text.Stage{stage1, stage2},
			CurrentIdx: 0,
		}
		eng.applyBatch = &mockBatch{}
		eng.cursorTarget = stage1.CursorTarget

		eng.partialAcceptCompletion()

		assert.Equal(t, int32(3), eng.cursorTarget.LineNumber, "cursor target should be preserved from stage 1")
	})
}

// TestAdvanceStagedCompletion_AdditionGroupsSpanningMultipleOldLines verifies
// that cumulative offset is computed correctly when a stage has only addition
// groups but spans multiple old lines (BufferStart != BufferEnd). The stage
// replaces old lines, so oldLineCount must reflect the replaced range.
func TestAdvanceStagedCompletion_AdditionGroupsSpanningMultipleOldLines(t *testing.T) {
	buf := newMockBuffer()
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	// Stage 1: replaces old lines 6-7 with 5 new lines, all groups are additions
	stage1 := &text.Stage{
		BufferStart: 6,
		BufferEnd:   7,
		Lines:       []string{"", "", "def min_max(data):", "    return min(data)", ""},
		Groups: []*text.Group{
			{Type: "addition", StartLine: 1, EndLine: 2, BufferLine: 6},
			{Type: "addition", StartLine: 5, EndLine: 5, BufferLine: 8},
		},
		CursorTarget: &types.CursorPredictionTarget{LineNumber: 13, RelativePath: "test.py"},
	}
	// Stage 2: further down, should be offset by stage 1's line count change
	stage2 := &text.Stage{
		BufferStart: 10,
		BufferEnd:   10,
		Lines:       []string{"", ""},
		Groups: []*text.Group{
			{Type: "addition", StartLine: 1, EndLine: 2, BufferLine: 10},
		},
		CursorTarget: &types.CursorPredictionTarget{LineNumber: 15, RelativePath: "test.py"},
	}

	eng.stagedCompletion = &text.StagedCompletion{
		Stages:     []*text.Stage{stage1, stage2},
		CurrentIdx: 0,
	}

	eng.advanceStagedCompletion()

	// Stage 1 replaced 2 old lines with 5 new lines → offset = 5 - 2 = 3
	// CumulativeOffset is applied to remaining stages then reset to 0
	assert.Equal(t, 0, eng.stagedCompletion.CumulativeOffset, "offset reset after applying")
	// Stage 2 should be shifted by 3 (from 10 to 13)
	assert.Equal(t, 13, stage2.BufferStart, "stage 2 BufferStart shifted by 3")
	assert.Equal(t, 13, stage2.BufferEnd, "stage 2 BufferEnd shifted by 3")
}

func TestShowOrNavigateToNextStage_CapturesRejectedCompletionCandidate(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{
		"line 1", "line 2", "line 3", "line 4", "line 5",
		"line 6", "line 7", "line 8", "line 9", "old line 10",
	}
	buf.row = 1
	buf.viewportTop = 1
	buf.viewportBottom = 5
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

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

	eng.showOrNavigateToNextStage()

	assert.Equal(t, stateHasCursorTarget, eng.state, "far next stage should show cursor target")
	assert.Equal(t, 10, buf.showCursorTargetLine, "cursor target line for next stage")
	assert.NotNil(t, eng.currentRejectedCompletion, "next-stage cursor target should capture rejection candidate")
}

// TestPartialAccept_StagedOffset_PureAddition tests that after partially accepting
// through a pure addition stage, the cumulative offset for subsequent stages is
// computed correctly. When all groups are additions and BufferStart==BufferEnd,
// the stage is a pure insertion (oldLineCount=0). Partial accept mutates the
// shared Group structs, which must not corrupt the isPureInsertion check.
func TestPartialAccept_StagedOffset_PureAddition(t *testing.T) {
	buf := newMockBuffer()
	// Buffer has 5 lines. Stage 1 is at line 3 (pure addition of 3 lines).
	// Stage 2 is at line 4. After stage 1, stage 2 should shift by +3.
	buf.lines = []string{"line1", "line2", "line3", "line4", "line5"}
	buf.row = 3
	prov := newMockProvider()
	clock := newMockClock()
	eng, cancel := createTestEngineWithContext(buf, prov, clock)
	defer cancel()

	// Stage 1: pure addition at line 3 — insert 3 new lines, replace 0 old lines
	stage1 := &text.Stage{
		BufferStart: 3,
		BufferEnd:   3,
		Lines:       []string{"    a = 1", "    b = 2", "    c = 3"},
		Groups: []*text.Group{
			{Type: "addition", BufferLine: 3, StartLine: 1, EndLine: 3,
				Lines: []string{"    a = 1", "    b = 2", "    c = 3"}},
		},
		CursorTarget: &types.CursorPredictionTarget{LineNumber: 6, ShouldRetrigger: false},
	}

	// Stage 2: at line 4
	stage2 := &text.Stage{
		BufferStart: 4,
		BufferEnd:   4,
		Lines:       []string{"extra"},
		Groups: []*text.Group{
			{Type: "addition", BufferLine: 4, StartLine: 1, EndLine: 1,
				Lines: []string{"extra"}},
		},
	}

	eng.stagedCompletion = &text.StagedCompletion{
		Stages:     []*text.Stage{stage1, stage2},
		CurrentIdx: 0,
	}

	// Show stage 1
	eng.state = stateHasCompletion
	eng.completions = []*types.Completion{{
		StartLine:  3,
		EndLineInc: 3,
		Lines:      []string{"    a = 1", "    b = 2", "    c = 3"},
	}}
	eng.completionOriginalLines = []string{"line3"}
	// Use CopyGroups like showCurrentStage does, to avoid mutating stage groups
	eng.currentGroups = text.CopyGroups(stage1.Groups)

	// Partial accept all 3 addition lines
	eng.partialAcceptCompletion()
	assert.Equal(t, stateHasCompletion, eng.state, "should still have completion after 1st")
	eng.partialAcceptCompletion()
	assert.Equal(t, stateHasCompletion, eng.state, "should still have completion after 2nd")
	eng.partialAcceptCompletion()
	// This finalizes stage 1 and advances to stage 2

	// Stage 1 was a pure insertion: 0 old lines → 3 new lines → offset = +3
	// Stage 2 original BufferStart=4 should shift to 4+3=7
	assert.Equal(t, 7, stage2.BufferStart, "stage 2 BufferStart should be offset by +3")
	assert.Equal(t, 7, stage2.BufferEnd, "stage 2 BufferEnd should be offset by +3")
}
