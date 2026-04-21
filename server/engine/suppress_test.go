package engine

import (
	"fmt"
	"testing"
	"time"

	"cursortab/assert"
	"cursortab/types"
)

func TestInertSuffixPattern(t *testing.T) {
	tests := []struct {
		suffix string
		inert  bool
	}{
		// Inert suffixes → should NOT suppress
		{"", true},
		{")", true},
		{"))", true},
		{"}", true},
		{"]", true},
		{`"`, true},
		{"'", true},
		{"`", true},
		{");", true},
		{") {", true},
		{"})", true},
		{"  )", true},
		{")  ", true},
		{",", true},
		{":", true},

		// Active suffixes → should suppress
		{"items {", false},
		{"!= nil {", false},
		{"foo()", false},
		{"hello", false},
		{"x + y", false},
		{".method()", false},
		{"= value", false},
		{"range items {", false},
	}

	for _, tt := range tests {
		got := inertSuffixPattern.MatchString(tt.suffix)
		assert.Equal(t, tt.inert, got, "suffix: "+tt.suffix)
	}
}

func TestSuppressForSingleDeletion(t *testing.T) {
	e := &Engine{
		config: EngineConfig{},
	}

	// No actions → no suppress
	e.userActions = nil
	assert.False(t, e.suppressForSingleDeletion(), "no actions")

	// Last action is insertion → no suppress
	e.userActions = []*types.UserAction{
		{ActionType: types.ActionInsertChar},
	}
	assert.False(t, e.suppressForSingleDeletion(), "insertion")

	// Single deletion → suppress
	e.userActions = []*types.UserAction{
		{ActionType: types.ActionInsertChar},
		{ActionType: types.ActionDeleteChar},
	}
	assert.True(t, e.suppressForSingleDeletion(), "single delete")

	// Two deletions → suppress (below threshold of 3)
	e.userActions = []*types.UserAction{
		{ActionType: types.ActionInsertChar},
		{ActionType: types.ActionDeleteChar},
		{ActionType: types.ActionDeleteChar},
	}
	assert.True(t, e.suppressForSingleDeletion(), "two deletes")

	// Three consecutive deletions → allow (rewriting pattern)
	e.userActions = []*types.UserAction{
		{ActionType: types.ActionInsertChar},
		{ActionType: types.ActionDeleteChar},
		{ActionType: types.ActionDeleteChar},
		{ActionType: types.ActionDeleteChar},
	}
	assert.False(t, e.suppressForSingleDeletion(), "three deletes = rewrite")

	// DeleteSelection counts as deletion
	e.userActions = []*types.UserAction{
		{ActionType: types.ActionDeleteSelection},
	}
	assert.True(t, e.suppressForSingleDeletion(), "single delete selection")

	// Mixed deletion types count together
	e.userActions = []*types.UserAction{
		{ActionType: types.ActionDeleteChar},
		{ActionType: types.ActionDeleteSelection},
		{ActionType: types.ActionDeleteChar},
	}
	assert.False(t, e.suppressForSingleDeletion(), "mixed deletes reach threshold")
}

func TestSuppressForMidLine(t *testing.T) {
	// Edit completion provider → never suppress mid-line
	e := &Engine{
		config: EngineConfig{EditCompletionProvider: true},
		buffer: &mockBuffer{
			lines: []string{"func process(items []string) {"},
			row:   1,
			col:   14, // mid-line
		},
	}
	assert.False(t, e.suppressForMidLine(), "edit provider ignores mid-line")

	// Non-edit provider, cursor at end → no suppress
	e = &Engine{
		config: EngineConfig{EditCompletionProvider: false},
		buffer: &mockBuffer{
			lines: []string{"result = "},
			row:   1,
			col:   9,
		},
	}
	assert.False(t, e.suppressForMidLine(), "cursor at end of line")

	// FIM provider → never suppress mid-line
	e = &Engine{
		config: EngineConfig{ProviderName: "fim"},
		buffer: &mockBuffer{
			lines: []string{"for _, item := range items {"},
			row:   1,
			col:   21, // before "items {"
		},
	}
	assert.False(t, e.suppressForMidLine(), "FIM provider ignores mid-line")

	// Inline provider, cursor mid-line with code to right → suppress
	e = &Engine{
		config: EngineConfig{EditCompletionProvider: false},
		buffer: &mockBuffer{
			lines: []string{"for _, item := range items {"},
			row:   1,
			col:   21, // before "items {"
		},
	}
	assert.True(t, e.suppressForMidLine(), "code to right of cursor")

	// Non-edit provider, only closing paren to right → no suppress
	e = &Engine{
		config: EngineConfig{EditCompletionProvider: false},
		buffer: &mockBuffer{
			lines: []string{"result = append(result, )"},
			row:   1,
			col:   23, // before ")"
		},
	}
	assert.False(t, e.suppressForMidLine(), "only closing paren")

	// Non-edit provider, closing bracket + semicolon → no suppress
	e = &Engine{
		config: EngineConfig{EditCompletionProvider: false},
		buffer: &mockBuffer{
			lines: []string{"doSomething();"},
			row:   1,
			col:   12, // before ");"
		},
	}
	assert.False(t, e.suppressForMidLine(), "closing paren + semicolon")
}

func TestRejectedCompletionSuppression_EscRejectsSimilarCompletion(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"hello"}
	buf.row = 1
	buf.col = 5
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	comp := &types.Completion{
		StartLine:  1,
		EndLineInc: 1,
		Lines:      []string{"hello world"},
	}

	outcome := eng.processCompletion(comp)
	assert.Equal(t, completionShown, outcome, "initial completion shown")
	assert.Equal(t, 1, buf.prepareCompletionCalls, "initial render count")

	eng.doReject()

	outcome = eng.processCompletion(&types.Completion{
		StartLine:  1,
		EndLineInc: 1,
		Lines:      []string{"hello world!"},
	})
	assert.Equal(t, completionSuppressed, outcome, "similar rejected completion suppressed")
	assert.Equal(t, 1, buf.prepareCompletionCalls, "suppressed completion should not render")
	assert.Equal(t, stateIdle, eng.state, "state after suppression")
}

func TestRejectedCompletionSuppression_ManualTriggerBypassesCache(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"hello"}
	buf.row = 1
	buf.col = 5
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	comp := &types.Completion{
		StartLine:  1,
		EndLineInc: 1,
		Lines:      []string{"hello world"},
	}

	assert.Equal(t, completionShown, eng.processCompletion(comp), "initial completion shown")
	eng.doReject()

	eng.manuallyTriggered = true
	assert.Equal(t, completionShown, eng.processCompletion(comp), "manual trigger bypasses rejection cache")
	assert.Equal(t, 2, buf.prepareCompletionCalls, "manual trigger should render completion")
}

func TestRejectedCompletionSuppression_ExpiresAfterTTL(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"hello"}
	buf.row = 1
	buf.col = 5
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	comp := &types.Completion{
		StartLine:  1,
		EndLineInc: 1,
		Lines:      []string{"hello world"},
	}

	assert.Equal(t, completionShown, eng.processCompletion(comp), "initial completion shown")
	eng.doReject()
	clock.Advance(rejectedCompletionTTL + time.Second)

	assert.Equal(t, completionShown, eng.processCompletion(comp), "expired rejection should not suppress completion")
	assert.Equal(t, 2, buf.prepareCompletionCalls, "completion should render after ttl")
}

func TestRejectedCompletionSuppression_TypingMismatchCachesRejection(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"hello"}
	buf.row = 1
	buf.col = 5
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	comp := &types.Completion{
		StartLine:  1,
		EndLineInc: 1,
		Lines:      []string{"hello world"},
	}

	assert.Equal(t, completionShown, eng.processCompletion(comp), "initial completion shown")

	buf.lines = []string{"hello x"}
	buf.col = 7
	eng.handleTextChangeImpl()

	buf.lines = []string{"hello"}
	buf.col = 5
	assert.Equal(t, completionSuppressed, eng.processCompletion(comp), "typed-over completion should be cached as rejected")
	assert.Equal(t, 1, buf.prepareCompletionCalls, "typed-over completion should not rerender")
}

func TestRejectedCompletionSuppression_BufferProgressAllowsCompletion(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"import "}
	buf.row = 1
	buf.col = 7
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	comp := &types.Completion{
		StartLine:  1,
		EndLineInc: 1,
		Lines:      []string{"import numpy as np"},
	}

	assert.Equal(t, completionShown, eng.processCompletion(comp), "initial completion shown")
	eng.doReject()

	buf.lines = []string{"import nump"}
	buf.col = len("import nump")
	assert.Equal(t, completionShown, eng.processCompletion(comp), "buffer progress should allow previously rejected completion")
	assert.Equal(t, 2, buf.prepareCompletionCalls, "completion should rerender after buffer changes")
}

func TestRejectedCompletionSuppression_CursorMoveDoesNotCache(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"hello"}
	buf.row = 1
	buf.col = 5
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	comp := &types.Completion{
		StartLine:  1,
		EndLineInc: 1,
		Lines:      []string{"hello world"},
	}

	assert.Equal(t, completionShown, eng.processCompletion(comp), "initial completion shown")
	eng.doResetIdleTimer()

	assert.Equal(t, completionShown, eng.processCompletion(comp), "cursor move should not populate rejection cache")
	assert.Equal(t, 2, buf.prepareCompletionCalls, "completion should rerender after cursor move")
}

func TestRejectedCompletionSuppression_PureInsertionSuppresses(t *testing.T) {
	buf := newMockBuffer()
	// Empty line inside a scope — cursor sitting on a blank line.
	buf.lines = []string{"def foo():", "", "bar = 1"}
	buf.row = 2
	buf.col = 0
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	comp := &types.Completion{
		StartLine:  2,
		EndLineInc: 2,
		Lines:      []string{`    print("hi")`},
	}

	assert.Equal(t, completionShown, eng.processCompletion(comp), "initial completion shown")
	eng.doReject()

	assert.Equal(t, completionSuppressed, eng.processCompletion(comp),
		"same completion into empty line should be suppressed")
}

func TestRejectedCompletionSuppression_AcceptClearsCache(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"hello"}
	buf.row = 1
	buf.col = 5
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	comp := &types.Completion{
		StartLine:  1,
		EndLineInc: 1,
		Lines:      []string{"hello world"},
	}

	assert.Equal(t, completionShown, eng.processCompletion(comp), "initial completion shown")
	eng.doReject()

	// Simulate an accept in the same file (unrelated completion).
	eng.forgetRejectedCompletions(buf.Path())

	assert.Equal(t, completionShown, eng.processCompletion(comp),
		"accept should clear rejection cache so identical completion is shown again")
}

// Multi-stage completions are stored at stage granularity but were previously
// compared against the full incoming completion's coordinates, which never
// matched. After the fix, suppression is checked against the first stage.
func TestRejectedCompletionSuppression_MultiStageMatchesOnFirstStage(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{
		"function a() {",
		"  return 1;",
		"}",
		"",
		"function b() {",
		"  return 2;",
		"}",
	}
	buf.row = 2
	buf.col = 0
	buf.viewportTop = 1
	buf.viewportBottom = 20
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	multiRegion := func() *types.Completion {
		return &types.Completion{
			StartLine:  1,
			EndLineInc: 7,
			Lines: []string{
				"function a() {",
				"  return 10;",
				"}",
				"",
				"function b() {",
				"  return 20;",
				"}",
			},
		}
	}

	assert.Equal(t, completionShown, eng.processCompletion(multiRegion()), "initial multi-stage shown")
	assert.Equal(t, 2, len(eng.stagedCompletion.Stages), "produces two stages")

	eng.doReject()

	assert.Equal(t, completionSuppressed, eng.processCompletion(multiRegion()),
		"identical multi-stage completion suppressed via first-stage match")
}

// A pure-deletion completion (Lines is empty, oldLines carries the text being
// removed) used to be skipped by the cache entirely.
func TestRejectedCompletionSuppression_PureDeletionCached(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"keep this", "drop this", "and keep this"}
	buf.row = 2
	buf.col = 0
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	deletion := func() *types.Completion {
		return &types.Completion{
			StartLine:  2,
			EndLineInc: 2,
			Lines:      []string{},
		}
	}

	assert.Equal(t, completionShown, eng.processCompletion(deletion()), "initial deletion shown")
	eng.doReject()

	assert.Equal(t, completionSuppressed, eng.processCompletion(deletion()),
		"pure-deletion completion is suppressed after rejection")
}

func TestRejectedCompletionSuppression_BlankLineDeletionCached(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"first line", "", "third line"}
	buf.row = 2
	buf.col = 0
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	deleteBlankLine := func() *types.Completion {
		return &types.Completion{
			StartLine:  2,
			EndLineInc: 2,
			Lines:      []string{},
		}
	}

	assert.Equal(t, completionShown, eng.processCompletion(deleteBlankLine()), "initial blank-line deletion shown")
	eng.doReject()

	assert.Equal(t, completionSuppressed, eng.processCompletion(deleteBlankLine()),
		"blank-line deletion should be suppressed after rejection")
}

func TestRejectedCompletionSuppression_NewlineDeletionCached(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"if condition:", "    pass"}
	buf.row = 1
	buf.col = len("if condition:")
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	removeNewline := func() *types.Completion {
		return &types.Completion{
			StartLine:  1,
			EndLineInc: 2,
			Lines:      []string{"if condition:    pass"},
		}
	}

	assert.Equal(t, completionShown, eng.processCompletion(removeNewline()), "initial newline-deletion shown")
	eng.doReject()

	assert.Equal(t, completionSuppressed, eng.processCompletion(removeNewline()),
		"newline deletion should be suppressed after rejection")
}

// Two completions where 49 of 50 lines are identical but the first line is
// totally different used to average above threshold and be wrongly suppressed.
// The min-line-similarity gate prevents that.
func TestRejectedCompletionSuppression_MinLineGateBlocksFalseMatch(t *testing.T) {
	bufLines := make([]string, 50)
	for i := range bufLines {
		bufLines[i] = fmt.Sprintf("line %d", i+1)
	}
	buf := newMockBuffer()
	buf.lines = bufLines
	buf.row = 1
	buf.col = 0
	buf.viewportTop = 1
	buf.viewportBottom = 100
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	makeBig := func(firstLine string) *types.Completion {
		lines := make([]string, 50)
		lines[0] = firstLine
		for i := 1; i < 50; i++ {
			lines[i] = fmt.Sprintf("line %d updated", i+1)
		}
		return &types.Completion{
			StartLine:  1,
			EndLineInc: 50,
			Lines:      lines,
		}
	}

	assert.Equal(t, completionShown, eng.processCompletion(makeBig("import path/to/foo")), "first big completion shown")
	eng.doReject()

	assert.Equal(t, completionShown, eng.processCompletion(makeBig("import totally/different/bar")),
		"different first line should not be drowned by 49 identical trailing lines")
}

func TestRejectedCompletionSuppression_LRUCapPerFile(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"hello"}
	buf.row = 1
	buf.col = 5
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	// Seed more than the cap directly through rememberRejectedCompletion.
	for i := 0; i < rejectedCompletionMaxPerFile+5; i++ {
		eng.currentRejectedCompletion = &rejectedCompletion{
			filePath:  buf.Path(),
			startLine: i + 1,
			lines:     []string{"x"},
		}
		eng.rememberRejectedCompletion()
	}

	entries := eng.rejectedCompletions[buf.Path()]
	assert.Equal(t, rejectedCompletionMaxPerFile, len(entries), "cache capped at max per file")
}
