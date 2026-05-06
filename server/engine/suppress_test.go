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

// A trailing punctuation char on a 1-2 char context line ("}" -> "};")
// drops the Levenshtein ratio to 0.5, which would trip the strict 0.9 gate
// even though the structural context is essentially unchanged.
func TestRejectedCompletionSuppression_ShortContextLineLenient(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{
		"fn foo() {",
		"  let x = 1;",
		"}",
	}
	buf.row = 2
	buf.col = 0
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	comp := &types.Completion{
		StartLine:  2,
		EndLineInc: 2,
		Lines:      []string{"  let x = 42;"},
	}

	assert.Equal(t, completionShown, eng.processCompletion(comp), "initial completion shown")
	eng.doReject()

	// Trailing punctuation tweak on the after-line (closing brace).
	buf.lines[2] = "};"

	assert.Equal(t, completionSuppressed, eng.processCompletion(comp),
		"trailing punctuation in short after-line context should not break suppression")
}

// A short context line with a meaningfully different character (not just an
// extension) is still tolerated by the relaxed gate, but the content gates
// (oldLines / lines) carry the real signal so unrelated edits won't suppress.
func TestRejectedCompletionSuppression_ShortContextLineNoSpuriousMatch(t *testing.T) {
	cached := &rejectedCompletion{
		filePath:   "test.go",
		startLine:  2,
		endLineInc: 2,
		beforeLine: "a",
		afterLine:  "c",
		oldLines:   []string{"b"},
		lines:      []string{"updated"},
	}
	// Same shape but a completely different oldLines value — the lines /
	// oldLines gate must still reject it even though context is short.
	incoming := &rejectedCompletion{
		filePath:   "test.go",
		startLine:  2,
		endLineInc: 2,
		beforeLine: "a",
		afterLine:  "c",
		oldLines:   []string{"totally different content here"},
		lines:      []string{"updated"},
	}

	matched, _ := cached.matches(incoming)
	assert.False(t, matched, "different oldLines should not match even with short context")
}

// A rejected completion that comes back one line shorter (e.g. trailing
// blank/comment dropped on retry) should still be suppressed when the
// overlapping prefix is identical.
func TestRejectedCompletionSuppression_TruncatedCompletionSuppressed(t *testing.T) {
	cached := &rejectedCompletion{
		filePath:   "test.go",
		startLine:  1,
		endLineInc: 1,
		beforeLine: "func foo() {",
		afterLine:  "",
		oldLines:   []string{"  // body"},
		lines: []string{
			"  for i := range items {",
			"    if items[i] > 0 {",
			"      result = append(result, items[i])",
			"    }",
			"  }",
			"  return result",
			"  // trailing",
		},
	}
	incoming := &rejectedCompletion{
		filePath:   "test.go",
		startLine:  1,
		endLineInc: 1,
		beforeLine: "func foo() {",
		afterLine:  "",
		oldLines:   []string{"  // body"},
		lines: []string{
			"  for i := range items {",
			"    if items[i] > 0 {",
			"      result = append(result, items[i])",
			"    }",
			"  }",
			"  return result",
		},
	}

	matched, _ := cached.matches(incoming)
	assert.True(t, matched, "shorter retry of rejected completion should still match cached")
}

// Length differences are still penalized through the average — a 5-vs-3
// completion has avg = 3/5 = 0.6, well below the 0.85 threshold.
func TestRejectedCompletionSuppression_LargeLengthDiffAllowed(t *testing.T) {
	cached := &rejectedCompletion{
		filePath:   "test.go",
		startLine:  1,
		endLineInc: 1,
		beforeLine: "func foo() {",
		afterLine:  "",
		oldLines:   []string{"  // body"},
		lines:      []string{"line a", "line b", "line c", "line d", "line e"},
	}
	incoming := &rejectedCompletion{
		filePath:   "test.go",
		startLine:  1,
		endLineInc: 1,
		beforeLine: "func foo() {",
		afterLine:  "",
		oldLines:   []string{"  // body"},
		lines:      []string{"line a", "line b", "line c"},
	}

	matched, _ := cached.matches(incoming)
	assert.False(t, matched, "large length difference should fail the avg gate")
}

// Typing while a cursor target is shown is the equivalent of pressing Esc on
// a regular completion: the user signaled they don't want this prediction.
// The candidate captured at the cursor target should be cached so the same
// prediction doesn't immediately re-pop.
func TestRejectedCompletionSuppression_CursorTargetTypingCachesRejection(t *testing.T) {
	buf := newMockBuffer()
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i+1)
	}
	buf.lines = lines
	buf.row = 1
	buf.col = 0
	buf.viewportTop = 1
	buf.viewportBottom = 30
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	comp := &types.Completion{
		StartLine:  10,
		EndLineInc: 10,
		Lines:      []string{"line 10 modified"},
	}

	assert.Equal(t, completionShown, eng.processCompletion(comp), "initial cursor target shown")
	assert.Equal(t, stateHasCursorTarget, eng.state, "should be in cursor target state")
	assert.NotNil(t, eng.currentRejectedCompletion, "candidate captured for cursor target")

	// EventTextChanged from stateHasCursorTarget is dispatched as
	// doRejectAndDebounce by the state machine.
	eng.doRejectAndDebounce()

	assert.Equal(t, completionSuppressed, eng.processCompletion(comp),
		"typing during cursor target should cache the rejection")
}

// Tab on a cursor target is forward progress — like accepting a regular
// completion, the file's rejection cache should be invalidated since line
// numbers shift and surrounding context is now stale.
func TestRejectedCompletionSuppression_AcceptCursorTargetClearsCache(t *testing.T) {
	buf := newMockBuffer()
	buf.lines = []string{"hello"}
	buf.row = 1
	buf.col = 0
	prov := newMockProvider()
	clock := newMockClock()
	eng := createTestEngine(buf, prov, clock)

	eng.rejectedCompletions[buf.Path()] = []*rejectedCompletion{{
		filePath:  buf.Path(),
		startLine: 1,
		lines:     []string{"hello world"},
		expiresAt: clock.Now().Add(time.Minute),
	}}

	eng.state = stateHasCursorTarget
	eng.cursorTarget = &types.CursorPredictionTarget{
		RelativePath: buf.Path(),
		LineNumber:   1,
	}

	eng.acceptCursorTarget()

	assert.Nil(t, eng.rejectedCompletions[buf.Path()],
		"Tab on cursor target should clear the file's rejection cache")
}
