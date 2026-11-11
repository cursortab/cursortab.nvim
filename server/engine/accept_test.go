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
	showDisplayedCompletionForTest(
		eng,
		&types.Completion{StartLine: 1, EndLineInc: 1, Lines: []string{"test"}},
		nil,
		nil,
	)
	eng.cursorTarget = &types.CursorPredictionTarget{LineNumber: 5}

	eng.rejectAndRemember()

	assert.Equal(t, stateIdle, eng.state, "state after reject")
	assert.Nil(t, eng.display.current(), "completions after reject")
	assert.Nil(t, eng.cursorTarget, "cursorTarget after reject")
	assert.Greater(t, buf.clearUICalls, 0, "ClearUI should have been called")
}

// TestReject_DropsLeftoverStreamFromAcceptDuringStreaming verifies that reject
// cancels any in-flight stream from a prior accept-during-streaming. Without
// this, the leftover stream eventually completes and handleStreamComplete
// sees stream.acceptedDuring=true, which calls handleStreamCompleteAfterAccept
// and resurrects a completion or cursor target the user just rejected.
func TestReject_DropsLeftoverStreamFromAcceptDuringStreaming(t *testing.T) {
	buf := newMockBuffer()
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)
	defer eng.Close()

	streamCtx, streamCancel := context.WithCancel(context.Background())
	defer streamCancel()
	eng.stream = &activeStream{stream: newMockCompletionStream(streamCancel)}
	eng.stream.acceptedDuring = true
	eng.state = stateHasCompletion

	eng.reject()

	assert.Nil(t, eng.stream, "streamingState should be cleared")
	assert.Nil(t, eng.stream, "leftover stream should be cleared")

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
	showDisplayedCompletionWithBatchForTest(
		eng,
		&types.Completion{StartLine: 1, EndLineInc: 1, Lines: []string{"x"}},
		&mockBatch{err: errors.New("execute failed")},
		nil,
		nil,
	)

	eng.acceptCompletion()

	assert.Equal(t, stateIdle, eng.state, "state should reset to idle after batch error")
	assert.Nil(t, eng.display.current(), "completions should be cleared after batch error")
	assert.Nil(t, eng.display.batchToApply(), "display batch should be cleared after batch error")
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
	showDisplayedCompletionForTest(
		eng,
		&types.Completion{
			StartLine:  1,
			EndLineInc: 1,
			Lines:      []string{"function foo()"},
		},
		[]string{"func"},
		[]*text.Group{{
			Type:       text.GroupModification,
			BufferLine: 1,
			RenderHint: text.HintAppendChars,
			ColStart:   4,
			Lines:      []string{"function foo()"},
		}},
	)

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
	showDisplayedCompletionForTest(
		eng,
		&types.Completion{
			StartLine:  1,
			EndLineInc: 1,
			Lines:      []string{"foo.bar.baz"},
		},
		[]string{"foo"},
		[]*text.Group{{
			Type:       text.GroupModification,
			BufferLine: 1,
			RenderHint: text.HintAppendChars,
			ColStart:   3,
			Lines:      []string{"foo.bar.baz"},
		}},
	)

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
	showDisplayedCompletionForTest(
		eng,
		&types.Completion{
			StartLine:  1,
			EndLineInc: 1,
			Lines:      []string{"hello!"},
		},
		[]string{"hello"},
		[]*text.Group{{
			Type:       text.GroupModification,
			BufferLine: 1,
			RenderHint: text.HintAppendChars,
			ColStart:   5,
			Lines:      []string{"hello!"},
		}},
	)

	eng.partialAcceptCompletion()

	assert.Equal(t, []string{"hello!"}, buf.lines, "buffer after accepting the last word")
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
	showDisplayedCompletionForTest(
		eng,
		&types.Completion{
			StartLine:  1,
			EndLineInc: 3,
			Lines:      []string{"new line 1", "new line 2", "new line 3"},
		},
		[]string{"line 1", "line 2", "line 3"},
		[]*text.Group{{
			Type:       text.GroupModification,
			BufferLine: 1,
			Lines:      []string{"new line 1", "new line 2", "new line 3"},
		}},
	)

	eng.partialAcceptCompletion()

	assert.Equal(t, []string{"new line 1", "line 2", "line 3"}, buf.lines, "buffer after one line")
	assert.Equal(t, stateHasCompletion, eng.state, "state after partial line accept")
	assert.Equal(t, &types.Completion{StartLine: 2, EndLineInc: 3, Lines: []string{"new line 2", "new line 3"}}, eng.display.current(), "remaining completion")
}

func TestPartialAccept_MultiLine_LastLine(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"old line"}
	buf.row = 1
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	eng.state = stateHasCompletion
	showDisplayedCompletionForTest(
		eng,
		&types.Completion{
			StartLine:  1,
			EndLineInc: 1,
			Lines:      []string{"new line"},
		},
		[]string{"old line"},
		[]*text.Group{{
			Type:       text.GroupModification,
			BufferLine: 1,
			Lines:      []string{"new line"},
		}},
	)

	eng.partialAcceptCompletion()

	assert.Equal(t, []string{"new line"}, buf.lines, "buffer after accepting the last line")
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
	showDisplayedCompletionForTest(
		eng,
		&types.Completion{
			StartLine:  1,
			EndLineInc: 1,
			Lines:      []string{"function foo()"},
		},
		[]string{"func"},
		[]*text.Group{{
			Type:       text.GroupModification,
			BufferLine: 1,
			RenderHint: text.HintAppendChars,
			ColStart:   4,
			Lines:      []string{"function foo()"},
		}},
	)

	eng.partialAcceptCompletion()

	assert.Equal(t, "on ", buf.lastInsertedText, "inserted text after user typing")
}

func TestPartialAccept_NoCompletions(t *testing.T) {
	buf := newMockBuffer()
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	eng.state = stateHasCompletion
	showDisplayedCompletionForTest(
		eng,
		nil,
		nil,
		nil,
	)

	eng.partialAcceptCompletion()

	assert.Equal(t, stateHasCompletion, eng.state, "state unchanged when no completions")
}

func TestPartialAccept_NoGroups(t *testing.T) {
	buf := newMockBuffer()
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	eng.state = stateHasCompletion
	showDisplayedCompletionForTest(
		eng,
		&types.Completion{
			StartLine:  1,
			EndLineInc: 1,
			Lines:      []string{"test"},
		},
		nil,
		nil,
	)

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
	showDisplayedCompletionForTest(
		eng,
		&types.Completion{
			StartLine:  1,
			EndLineInc: 2,
			Lines:      []string{"func main() {", "    fmt.Println(\"hello\")", "}"},
		},
		[]string{"func main() {", "}"},
		[]*text.Group{{
			Type:       text.GroupAddition,
			BufferLine: 2,
			StartLine:  2,
			EndLine:    2,
			Lines:      []string{"    fmt.Println(\"hello\")"},
		}},
	)

	eng.partialAcceptCompletion()

	assert.Equal(t, []string{"func main() {", "    fmt.Println(\"hello\")", "}"}, buf.lines, "the addition is the first change")
	assert.Equal(t, stateIdle, eng.state, "accepting the only change accepts the completion")
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
	showDisplayedCompletionForTest(
		eng,
		&types.Completion{
			StartLine:  3,
			EndLineInc: 3, // Only replacing line 3, but adding line 4
			Lines:      []string{"def bubble_sort(arr):", "    n = len(arr)"},
		},
		[]string{"def bubble_sort(arr):"},
		[]*text.Group{
			{
				Type:       text.GroupModification,
				BufferLine: 3,
				StartLine:  1,
				EndLine:    1,
				Lines:      []string{"def bubble_sort(arr):"},
				OldLines:   []string{"def bubble_sort(arr):"},
				RenderHint: text.HintAppendChars,
				ColStart:   21,
				ColEnd:     21,
			},
			{
				Type:       text.GroupAddition,
				BufferLine: 4,
				StartLine:  2,
				EndLine:    2,
				Lines:      []string{"    n = len(arr)"},
			},
		},
	)

	// When append_chars line is already complete, partial accept should
	// transition to the next line (the addition), NOT finalize the stage
	eng.partialAcceptCompletion()

	// After partial accept, the completion should now point to the addition line
	assert.Equal(t, stateHasCompletion, eng.state, "should still be in HasCompletion")
	assert.Equal(t, 1, len(eng.display.current().Lines), "should have 1 remaining line")
	assert.Equal(t, "    n = len(arr)", eng.display.current().Lines[0], "remaining line content")
	assert.Equal(t, 4, eng.display.current().StartLine, "startLine should be 4")
}

// TestPartialAccept_StagedCompletion_UsesCurrentGroups tests that during partial
// accept with a staged completion, we use current display groups (updated by rerenderPartial)
// not the stale stage groups. This prevents skipping addition lines when the
// append_chars group in the stage is stale but current display groups has been updated.
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
						Type:       text.GroupModification,
						BufferLine: 3,
						StartLine:  1,
						EndLine:    1,
						Lines:      []string{"def bubble_sort(arr):"},
						OldLines:   []string{"def bubb"},
						RenderHint: text.HintAppendChars,
						ColStart:   8,
						ColEnd:     21,
					},
					{
						Type:       text.GroupAddition,
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
					Type:       text.GroupAddition,
					BufferLine: 5,
					Lines:      []string{"    for i in range(n):"},
				}},
			},
		},
		CurrentIdx: 0,
	}

	// Current state: append_chars is complete, now showing addition for line 4
	eng.state = stateHasCompletion
	showDisplayedCompletionForTest(
		eng,
		&types.Completion{
			StartLine:  4,
			EndLineInc: 3,
			Lines:      []string{"    n = len(arr)"},
		},
		nil,
		[]*text.Group{{
			Type:       text.GroupAddition,
			BufferLine: 4,
			StartLine:  1,
			EndLine:    1,
			Lines:      []string{"    n = len(arr)"},
		}},
	)

	// This is the key: partial accept should use current display groups (addition),
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
	showDisplayedCompletionForTest(
		eng,
		&types.Completion{
			StartLine:  1,
			EndLineInc: 1,
			Lines:      []string{"test!"},
		},
		[]string{"test"},
		[]*text.Group{{
			Type:       text.GroupModification,
			BufferLine: 1,
			RenderHint: text.HintAppendChars,
			ColStart:   4,
			Lines:      []string{"test!"},
		}},
	)
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
		eng := createTestEngine(buf, prov, clock)
		defer eng.Close()

		eng.state = stateHasCompletion
		showDisplayedCompletionWithBatchForTest(
			eng,
			&types.Completion{
				StartLine:  1,
				EndLineInc: 4,
				Lines:      []string{"new line 1", "new line 2", "new line 3", "new line 4"},
			},
			&mockBatch{},
			buf.lines,
			[]*text.Group{
				{Type: text.GroupModification, BufferLine: 1, StartLine: 1, EndLine: 1, Lines: []string{"new line 1"}, OldLines: []string{"old line 1"}},
				{Type: text.GroupModification, BufferLine: 2, StartLine: 2, EndLine: 2, Lines: []string{"new line 2"}, OldLines: []string{"old line 2"}},
				{Type: text.GroupModification, BufferLine: 3, StartLine: 3, EndLine: 3, Lines: []string{"new line 3"}, OldLines: []string{"old line 3"}},
				{Type: text.GroupModification, BufferLine: 4, StartLine: 4, EndLine: 4, Lines: []string{"new line 4"}, OldLines: []string{"old line 4"}},
			},
		)

		expectedCursorTarget := int32(8)
		eng.cursorTarget = &types.CursorPredictionTarget{
			LineNumber:      expectedCursorTarget,
			ShouldRetrigger: true,
		}
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
		eng := createTestEngine(buf, prov, clock)
		defer eng.Close()

		eng.state = stateHasCompletion
		showDisplayedCompletionForTest(
			eng,
			&types.Completion{
				StartLine:  1,
				EndLineInc: 4,
				Lines:      []string{"new line 1", "new line 2", "new line 3", "new line 4"},
			},
			buf.lines,
			[]*text.Group{
				{Type: text.GroupModification, BufferLine: 1, StartLine: 1, EndLine: 1, Lines: []string{"new line 1"}, OldLines: []string{"old line 1"}},
				{Type: text.GroupModification, BufferLine: 2, StartLine: 2, EndLine: 2, Lines: []string{"new line 2"}, OldLines: []string{"old line 2"}},
				{Type: text.GroupModification, BufferLine: 3, StartLine: 3, EndLine: 3, Lines: []string{"new line 3"}, OldLines: []string{"old line 3"}},
				{Type: text.GroupModification, BufferLine: 4, StartLine: 4, EndLine: 4, Lines: []string{"new line 4"}, OldLines: []string{"old line 4"}},
			},
		)

		expectedCursorTarget := int32(8)
		eng.cursorTarget = &types.CursorPredictionTarget{
			LineNumber:      expectedCursorTarget,
			ShouldRetrigger: true,
		}
		eng.stagedCompletion = nil

		eng.partialAcceptCompletion()
		assert.Equal(t, stateHasCompletion, eng.state, "should stay in HasCompletion after partial accept")
		assert.Equal(t, 3, len(eng.display.current().Lines), "remaining lines")
		assert.Equal(t, 2, eng.display.current().StartLine, "start line increments")

		eng.partialAcceptCompletion()
		assert.Equal(t, 2, len(eng.display.current().Lines), "remaining lines")
		assert.Equal(t, 3, eng.display.current().StartLine, "start line increments")

		eng.partialAcceptCompletion()
		assert.Equal(t, 1, len(eng.display.current().Lines), "remaining lines")
		assert.Equal(t, 4, eng.display.current().StartLine, "start line increments")

		eng.partialAcceptCompletion()

		assert.Equal(t, int(expectedCursorTarget), buf.showCursorTargetLine, "cursor target should be preserved through partial accepts")
	})

	t.Run("partial_accept_cursor_target_consistency_through_all_accepts", func(t *testing.T) {
		buf := newMockBuffer()
		buf.lines = []string{"x", "y", "z", "w"}
		buf.row = 1
		prov := newMockProvider()
		clock := newMockClock()
		eng := createTestEngine(buf, prov, clock)
		defer eng.Close()

		cursorTarget := int32(12)
		eng.state = stateHasCompletion
		showDisplayedCompletionForTest(
			eng,
			&types.Completion{
				StartLine:  1,
				EndLineInc: 4,
				Lines:      []string{"X", "Y", "Z", "W"},
			},
			buf.lines,
			[]*text.Group{
				{Type: text.GroupModification, BufferLine: 1, StartLine: 1, EndLine: 1, Lines: []string{"X"}, OldLines: []string{"x"}},
				{Type: text.GroupModification, BufferLine: 2, StartLine: 2, EndLine: 2, Lines: []string{"Y"}, OldLines: []string{"y"}},
				{Type: text.GroupModification, BufferLine: 3, StartLine: 3, EndLine: 3, Lines: []string{"Z"}, OldLines: []string{"z"}},
				{Type: text.GroupModification, BufferLine: 4, StartLine: 4, EndLine: 4, Lines: []string{"W"}, OldLines: []string{"w"}},
			},
		)
		eng.cursorTarget = &types.CursorPredictionTarget{
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
		eng := createTestEngine(buf, prov, clock)
		defer eng.Close()

		stage1 := &text.Stage{
			BufferStart: 1,
			BufferEnd:   2,
			Lines:       []string{"A", "B"},
			Groups:      []*text.Group{{Type: text.GroupModification, BufferLine: 1}},
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
			Groups:      []*text.Group{{Type: text.GroupModification, BufferLine: 3}},
			CursorLine:  1,
			CursorCol:   0,
			IsLastStage: true,
			CursorTarget: &types.CursorPredictionTarget{
				LineNumber:      5,
				ShouldRetrigger: true,
			},
		}

		eng.state = stateHasCompletion
		showDisplayedCompletionWithBatchForTest(
			eng,
			&types.Completion{
				StartLine:  1,
				EndLineInc: 2,
				Lines:      []string{"A", "B"},
			},
			&mockBatch{},
			[]string{"a", "b"},
			[]*text.Group{
				{Type: text.GroupModification, BufferLine: 1, StartLine: 1, EndLine: 1, Lines: []string{"A"}, OldLines: []string{"a"}},
				{Type: text.GroupModification, BufferLine: 2, StartLine: 2, EndLine: 2, Lines: []string{"B"}, OldLines: []string{"b"}},
			},
		)
		eng.stagedCompletion = &text.StagedCompletion{
			Stages:     []*text.Stage{stage1, stage2},
			CurrentIdx: 0,
		}
		eng.cursorTarget = stage1.CursorTarget

		eng.partialAcceptCompletion()

		assert.Equal(t, int32(3), eng.cursorTarget.LineNumber, "cursor target should be preserved from stage 1")
	})
}

// TestAdvanceStagedCompletion_AdditionGroupsSpanningMultipleOldLines verifies
// that later stages shift by the lines a stage added when it has only addition
// groups but replaces several old lines.
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
			{Type: text.GroupAddition, StartLine: 1, EndLine: 2, BufferLine: 6},
			{Type: text.GroupAddition, StartLine: 5, EndLine: 5, BufferLine: 8},
		},
		CursorTarget: &types.CursorPredictionTarget{LineNumber: 13},
	}
	// Stage 2: further down, should be offset by stage 1's line count change
	stage2 := &text.Stage{
		BufferStart: 10,
		BufferEnd:   10,
		Lines:       []string{"", ""},
		Groups: []*text.Group{
			{Type: text.GroupAddition, StartLine: 1, EndLine: 2, BufferLine: 10},
		},
		CursorTarget: &types.CursorPredictionTarget{LineNumber: 15},
	}

	eng.stagedCompletion = &text.StagedCompletion{
		Stages:     []*text.Stage{stage1, stage2},
		CurrentIdx: 0,
	}

	eng.advanceStagedCompletion()

	// Stage 1 replaced 2 old lines with 5 new lines → offset = 5 - 2 = 3
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
				Type:       text.GroupModification,
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
	assert.NotNil(t, eng.display.rejectionCandidate(), "next-stage cursor target should capture rejection candidate")
}

// TestPartialAccept_StagedOffset_PureAddition tests that after partially
// accepting through a pure insertion stage, later stages shift by the inserted
// lines. Partial accept mutates the shared Group structs, which must not change
// the stage's insertion range.
func TestPartialAccept_StagedOffset_PureAddition(t *testing.T) {
	buf := newMockBuffer()
	// Buffer has 5 lines. Stage 1 is at line 3 (pure addition of 3 lines).
	// Stage 2 is at line 4. After stage 1, stage 2 should shift by +3.
	buf.lines = []string{"line1", "line2", "line3", "line4", "line5"}
	buf.row = 3
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)
	defer eng.Close()

	// Stage 1: pure addition at line 3 — insert 3 new lines, replace 0 old lines
	stage1 := &text.Stage{
		BufferStart: 3,
		BufferEnd:   2,
		Lines:       []string{"    a = 1", "    b = 2", "    c = 3"},
		Groups: []*text.Group{
			{Type: text.GroupAddition, BufferLine: 3, StartLine: 1, EndLine: 3,
				Lines: []string{"    a = 1", "    b = 2", "    c = 3"}},
		},
		CursorTarget: &types.CursorPredictionTarget{LineNumber: 6, ShouldRetrigger: false},
	}

	// Stage 2: insertion at line 4
	stage2 := &text.Stage{
		BufferStart: 4,
		BufferEnd:   3,
		Lines:       []string{"extra"},
		Groups: []*text.Group{
			{Type: text.GroupAddition, BufferLine: 4, StartLine: 1, EndLine: 1,
				Lines: []string{"extra"}},
		},
	}

	eng.stagedCompletion = &text.StagedCompletion{
		Stages:     []*text.Stage{stage1, stage2},
		CurrentIdx: 0,
	}

	// Show stage 1
	eng.state = stateHasCompletion
	showDisplayedCompletionForTest(
		eng,
		&types.Completion{
			StartLine:  3,
			EndLineInc: 2,
			Lines:      []string{"    a = 1", "    b = 2", "    c = 3"},
		},
		nil,
		text.CopyGroups(stage1.Groups),
	)

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
	assert.Equal(t, 6, stage2.BufferEnd, "stage 2 BufferEnd should be offset by +3")
}

func TestAcceptCompletionRecordsPredictedEdit(t *testing.T) {
	buf := newMockBuffer()
	buf.path = "a.go"
	buf.lines = []string{"a", "b", "c"}
	buf.row = 2
	eng := createTestEngine(buf, newMockProvider(), newMockClock())
	defer eng.Close()
	buf.switchOnSync = true
	eng.handleEvent(Event{Type: EventBufEnter})

	completion := &types.Completion{StartLine: 2, EndLineInc: 2, Lines: []string{"B"}}
	assert.Equal(t, completionShown, eng.processCompletion(&types.CompletionResponse{Completion: completion}, false), "completion shown")
	eng.acceptCompletion()

	assert.Equal(t, []string{"a", "B", "c"}, buf.lines, "edit applied")
	diffs := eng.history.Diffs("a.go")
	assert.Equal(t, 1, len(diffs), "one recorded edit")
	assert.Equal(t, "b", diffs[0].Original, "replaced line")
	assert.Equal(t, "B", diffs[0].Updated, "accepted line")
	assert.Equal(t, types.DiffSourcePredicted, diffs[0].Source, "recorded as predicted")
}

func TestAcceptRecordsTypingBeforeThePrediction(t *testing.T) {
	buf := newMockBuffer()
	buf.path = "a.go"
	buf.lines = []string{"hello"}
	buf.col = 5
	eng := createTestEngine(buf, newMockProvider(), newMockClock())
	defer eng.Close()
	buf.switchOnSync = true
	eng.handleEvent(Event{Type: EventBufEnter})

	buf.lines = []string{"hello!"} // Typed in insert mode, no InsertLeave yet
	completion := &types.Completion{StartLine: 1, EndLineInc: 1, Lines: []string{"hello! big world"}}
	assert.Equal(t, completionShown, eng.processCompletion(&types.CompletionResponse{Completion: completion}, false), "completion shown")
	eng.acceptCompletion()

	diffs := eng.history.Diffs("a.go")
	assert.Equal(t, 2, len(diffs), "typing and prediction recorded separately")
	assert.Equal(t, types.DiffSourceManual, diffs[0].Source, "typing first")
	assert.Equal(t, "hello", diffs[0].Original, "typing original")
	assert.Equal(t, "hello!", diffs[0].Updated, "typing result")
	assert.Equal(t, types.DiffSourcePredicted, diffs[1].Source, "prediction second")
	assert.Equal(t, "hello!", diffs[1].Original, "prediction applies to typed content")
	assert.Equal(t, "hello! big world", diffs[1].Updated, "prediction result")
}

func TestPartialAcceptRecordsPrediction(t *testing.T) {
	buf := newMockBuffer()
	buf.path = "a.go"
	buf.lines = []string{"func"}
	buf.col = 4
	eng := createTestEngine(buf, newMockProvider(), newMockClock())
	defer eng.Close()
	buf.switchOnSync = true
	eng.handleEvent(Event{Type: EventBufEnter})

	completion := &types.Completion{StartLine: 1, EndLineInc: 1, Lines: []string{"function foo()"}}
	assert.Equal(t, completionShown, eng.processCompletion(&types.CompletionResponse{Completion: completion}, false), "completion shown")
	for i := 0; i < 5 && eng.display.hasCompletion(); i++ {
		eng.partialAcceptCompletion()
	}

	assert.Equal(t, []string{"function foo()"}, buf.lines, "completion fully accepted")
	diffs := eng.history.Diffs("a.go")
	assert.Equal(t, 1, len(diffs), "one recorded edit")
	assert.Equal(t, types.DiffSourcePredicted, diffs[0].Source, "partial accepts are predicted edits")
	assert.Equal(t, "func", diffs[0].Original, "original line")
	assert.Equal(t, "function foo()", diffs[0].Updated, "accepted line")
}

func TestPartialAcceptThenAcceptShiftsLaterStagesByWholeStage(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"line1", "line2", "line3", "line4", "line5"}
	buf.row = 3
	eng := createTestEngine(buf, newMockProvider(), newMockClock())
	defer eng.Close()

	added := []string{"    a = 1", "    b = 2", "    c = 3"}
	stage1 := &text.Stage{
		BufferStart: 3, BufferEnd: 2, Lines: added,
		Groups: []*text.Group{{Type: text.GroupAddition, BufferLine: 3, StartLine: 1, EndLine: 3, Lines: added}},
	}
	stage2 := &text.Stage{
		BufferStart: 5, BufferEnd: 5, Lines: []string{"LINE5"},
		Groups: []*text.Group{{Type: text.GroupModification, BufferLine: 5, StartLine: 1, EndLine: 1, Lines: []string{"LINE5"}, OldLines: []string{"line5"}}},
	}
	eng.stagedCompletion = &text.StagedCompletion{Stages: []*text.Stage{stage1, stage2}}
	eng.state = stateHasCompletion
	eng.showCurrentStage()

	eng.partialAcceptCompletion()
	assert.Equal(t, stateHasCompletion, eng.state, "rest of stage 1 still shown")
	eng.acceptCompletion()

	assert.Equal(t, []string{"line1", "line2", "    a = 1", "    b = 2", "    c = 3", "line3", "line4", "line5"}, buf.lines, "stage 1 fully applied")
	assert.Equal(t, 8, stage2.BufferStart, "stage 2 shifted by all three inserted lines")
}
