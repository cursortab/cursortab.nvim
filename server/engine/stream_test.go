package engine

import (
	"cursortab/assert"
	"cursortab/text"
	"cursortab/types"
	"strings"
	"testing"
)

// fakeStreamContext is a minimal StreamContext for testing handleStreamLine
// wiring. It records raw inputs and delegates marker stripping to the same
// algorithm as provider.Context (can't embed it due to import cycle).
type fakeStreamContext struct {
	marker        string
	received      []string
	cursorSeen    bool
	cursorLine    int
	cursorCol     int
	linesReceived int
	skipLine      bool
}

func (f *fakeStreamContext) GetStreamOldLines() []string           { return nil }
func (f *fakeStreamContext) GetStreamBaseOffset() int              { return 0 }
func (f *fakeStreamContext) TransformFirstLine(line string) string { return line }
func (f *fakeStreamContext) TransformLastLine(line string) string  { return line }
func (f *fakeStreamContext) ShouldSkipLine() bool                  { return f.skipLine }

func (f *fakeStreamContext) TransformLine(line string) string {
	f.skipLine = false
	f.received = append(f.received, line)
	defer func() { f.linesReceived++ }()
	if f.marker != "" {
		if !f.cursorSeen {
			if idx := strings.Index(line, f.marker); idx >= 0 {
				f.cursorSeen = true
				f.cursorLine = f.linesReceived
				f.cursorCol = idx
			}
		}
		stripped := strings.ReplaceAll(line, f.marker, "")
		if strings.TrimSpace(stripped) == "" && strings.Contains(line, f.marker) {
			f.skipLine = true
		}
		return stripped
	}
	return line
}

// TestHandleStreamLine_CallsTransformLineBeforeAccumulation verifies that
// handleStreamLine runs the StreamContext.TransformLine hook before the line
// hits AccumulatedText or the stage builder. This is the fix for zeta2's
// <|user_cursor|> marker leaking into rendered stages.
func TestHandleStreamLine_CallsTransformLineBeforeAccumulation(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"a", "b", "c", "d", "e", "f"}
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	fakeCtx := &fakeStreamContext{marker: "<|user_cursor|>"}

	eng.state = stateStreamingCompletion
	eng.streamingState = &StreamingState{
		StageBuilder: text.NewIncrementalStageBuilder(
			buf.lines,
			1,  // baseLineOffset
			3,  // proximityThreshold
			50, // maxVisibleLines
			1, 50,
			1, 0,
			"test.go",
			100,
		),
		ProviderContext: fakeCtx,
		Request:         &types.CompletionRequest{FilePath: "test.go", Lines: buf.lines},
	}

	// Stream a sequence of lines, one containing the marker.
	eng.handleStreamLine("a")
	eng.handleStreamLine("b")
	eng.handleStreamLine("    return arr<|user_cursor|>")
	eng.handleStreamLine("d")

	// TransformLine must have been called on every line in order.
	assert.Equal(t, 4, len(fakeCtx.received), "TransformLine called on every line")
	assert.Equal(t, "    return arr<|user_cursor|>", fakeCtx.received[2], "raw line reaches hook")

	// Accumulated text must NOT contain the marker.
	acc := eng.streamingState.AccumulatedText.String()
	assert.False(t, strings.Contains(acc, "<|user_cursor|>"),
		"accumulated text is clean — marker stripped before accumulation")
	assert.True(t, strings.Contains(acc, "    return arr"), "stripped content preserved in accumulator")

	// Marker position captured correctly.
	assert.True(t, fakeCtx.cursorSeen, "marker recorded")
	assert.Equal(t, 2, fakeCtx.cursorLine, "line index = 2 (3rd line, 0-indexed)")
	assert.Equal(t, 14, fakeCtx.cursorCol, "column = byte offset before marker")
}

// TestHandleStreamLine_SkipsMarkerOnlyLine verifies that when the model emits
// <|user_cursor|> on its own line, the resulting empty line is dropped from
// accumulation and the stage builder. This prevents a phantom trailing stage.
func TestHandleStreamLine_SkipsMarkerOnlyLine(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"a", "b", "c", "d", "e", "f"}
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	fakeCtx := &fakeStreamContext{marker: "<|user_cursor|>"}

	eng.state = stateStreamingCompletion
	eng.streamingState = &StreamingState{
		StageBuilder: text.NewIncrementalStageBuilder(
			buf.lines, 1, 3, 50, 1, 50, 1, 0, "test.go", 100,
		),
		ProviderContext: fakeCtx,
		Request:         &types.CompletionRequest{FilePath: "test.go", Lines: buf.lines},
	}

	eng.handleStreamLine("a")
	eng.handleStreamLine("b")
	eng.handleStreamLine("<|user_cursor|>") // marker-only line

	// TransformLine was called 3 times (including the marker-only line).
	assert.Equal(t, 3, len(fakeCtx.received), "TransformLine called on every line including marker-only")

	// Accumulated text must NOT contain the marker-only empty line.
	// Only "a\nb\n" should be accumulated (2 lines × "line\n"), not "a\nb\n\n" (3 lines).
	acc := eng.streamingState.AccumulatedText.String()
	assert.Equal(t, "a\nb\n", acc, "marker-only line excluded from accumulation")

	// Marker position still captured.
	assert.True(t, fakeCtx.cursorSeen, "marker recorded even though line skipped")
}

// TestHandleStreamLine_TransformLineIsNoopWithoutMarker verifies that a
// StreamContext with an empty marker pattern is a harmless no-op — all other
// providers remain untouched.
func TestHandleStreamLine_TransformLineIsNoopWithoutMarker(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"x", "y", "z"}
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	fakeCtx := &fakeStreamContext{}

	eng.state = stateStreamingCompletion
	eng.streamingState = &StreamingState{
		StageBuilder: text.NewIncrementalStageBuilder(
			buf.lines, 1, 3, 50, 1, 50, 1, 0, "test.go", 100,
		),
		ProviderContext: fakeCtx,
		Request:         &types.CompletionRequest{FilePath: "test.go", Lines: buf.lines},
	}

	eng.handleStreamLine("some line with <|user_cursor|> in it")

	// With no marker configured, the line passes through unchanged.
	assert.Equal(t, 1, len(fakeCtx.received), "hook still called")
	acc := eng.streamingState.AccumulatedText.String()
	assert.True(t, strings.Contains(acc, "<|user_cursor|>"),
		"empty-marker hook does not strip anything")
}

func TestTokenStreamingKeepPartial_TypingMatchesPartial(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"hello wo"} // User typed "wo" which matches partial stream
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	// Simulate token streaming state with partial result
	eng.state = stateStreamingCompletion
	eng.tokenStreamingState = &TokenStreamingState{
		AccumulatedText: "world",
		LinePrefix:      "hello ",
		LineNum:         1,
	}
	// This would have been set by handleTokenChunk
	eng.completions = []*types.Completion{{
		StartLine:  1,
		EndLineInc: 1,
		Lines:      []string{"hello world"},
	}}
	eng.completionOriginalLines = []string{"hello "}
	eng.tokenStreamChan = make(chan string) // Non-nil to indicate active stream

	// Trigger text change during streaming
	eng.doRejectStreamingAndDebounce()

	// Should transition to HasCompletion state since typing matches
	assert.Equal(t, stateHasCompletion, eng.state, "state after matching typing during streaming")

	// Completions should be preserved
	assert.Greater(t, len(eng.completions), 0, "completions count")

	// Token streaming state should be cleared
	assert.Nil(t, eng.tokenStreamingState, "tokenStreamingState after cancellation")
}

func TestTokenStreamingKeepPartial_TypingDoesNotMatch(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"hello xyz"} // User typed something different
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	// Simulate token streaming state with partial result
	eng.state = stateStreamingCompletion
	eng.tokenStreamingState = &TokenStreamingState{
		AccumulatedText: "world",
		LinePrefix:      "hello ",
		LineNum:         1,
	}
	eng.completions = []*types.Completion{{
		StartLine:  1,
		EndLineInc: 1,
		Lines:      []string{"hello world"},
	}}
	eng.completionOriginalLines = []string{"hello "}
	eng.tokenStreamChan = make(chan string)

	// Trigger text change during streaming
	eng.doRejectStreamingAndDebounce()

	// Should transition to Idle state since typing doesn't match
	assert.Equal(t, stateIdle, eng.state, "state after mismatching typing during streaming")

	// Completions should be cleared
	assert.Nil(t, eng.completions, "completions after mismatch")

	// ClearUI should have been called
	assert.Greater(t, buf.clearUICalls, 0, "ClearUI should have been called")
}

func TestTokenStreamingKeepPartial_FullyTyped(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"hello world"} // User typed the full completion
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	// Simulate token streaming state with partial result
	eng.state = stateStreamingCompletion
	eng.tokenStreamingState = &TokenStreamingState{
		AccumulatedText: "world",
		LinePrefix:      "hello ",
		LineNum:         1,
	}
	eng.completions = []*types.Completion{{
		StartLine:  1,
		EndLineInc: 1,
		Lines:      []string{"hello world"},
	}}
	eng.completionOriginalLines = []string{"hello "}
	eng.tokenStreamChan = make(chan string)

	// Trigger text change during streaming
	eng.doRejectStreamingAndDebounce()

	// Should transition to Idle since fully typed
	assert.Equal(t, stateIdle, eng.state, "state after fully typing completion during streaming")
}

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

	// Simulate line streaming state (NOT token streaming)
	eng.state = stateStreamingCompletion
	eng.streamingState = &StreamingState{} // Line streaming state
	eng.tokenStreamingState = nil          // No token streaming
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

func TestCancelTokenStreamingKeepPartial(t *testing.T) {
	buf := newMockBuffer()
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	// Set up token streaming state
	eng.tokenStreamChan = make(chan string)
	eng.tokenStreamingState = &TokenStreamingState{
		AccumulatedText: "test",
		LineNum:         1,
	}
	eng.completions = []*types.Completion{{
		StartLine:  1,
		EndLineInc: 1,
		Lines:      []string{"test line"},
	}}
	eng.completionOriginalLines = []string{""}

	// Cancel keeping partial
	eng.cancelTokenStreamingKeepPartial()

	// Token stream channel should be nil
	assert.Nil(t, eng.tokenStreamChan, "tokenStreamChan after cancel")

	// Token streaming state should be nil
	assert.Nil(t, eng.tokenStreamingState, "tokenStreamingState after cancel")

	// But completions should be preserved
	assert.NotNil(t, eng.completions, "completions after cancel")

	// And completionOriginalLines should be preserved
	assert.NotNil(t, eng.completionOriginalLines, "completionOriginalLines after cancel")
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
