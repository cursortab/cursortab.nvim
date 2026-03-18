package engine

import (
	"fmt"
	"testing"
	"time"

	"cursortab/assert"
	"cursortab/gating"
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

	// Non-edit provider, cursor mid-line with code to right → suppress
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

func TestSuppressForGating(t *testing.T) {
	e := &Engine{
		clock: newMockClock(),
		buffer: &mockBuffer{
			lines: []string{"result = "},
			row:   1,
			col:   9,
			path:  "main.go",
		},
	}
	assert.False(t, e.suppressForGating(), "good context should pass")
	assert.True(t, e.filterState.lastShown, "lastShown should be true after pass")
}

func TestSuppressForGating_UpdatesState(t *testing.T) {
	clock := newMockClock()
	e := &Engine{
		clock: clock,
		buffer: &mockBuffer{
			lines: []string{"x = "},
			row:   1,
			col:   4,
			path:  "main.go",
		},
	}

	e.suppressForGating()
	assert.False(t, e.filterState.lastDecisionTime.IsZero(),
		"lastDecisionTime should be set after filter call")
}

func TestSuppressForGating_Momentum(t *testing.T) {
	clock := newMockClock()
	base := &Engine{
		clock: clock,
		buffer: &mockBuffer{
			lines: []string{"x := "},
			row:   1,
			col:   5,
			path:  "main.go",
		},
		filterState: gatingState{
			lastShown:        true,
			lastDecisionTime: clock.Now().Add(-1 * time.Second),
		},
	}

	// With momentum, score should be higher
	scoreWith := gating.Score(gating.Input{
		Lines:         base.buffer.Lines(),
		Row:           base.buffer.Row(),
		Col:           base.buffer.Col(),
		FileExtension: ".go",
		PreviousLabel: true,
		LastDecision:  clock.Now().Add(-1 * time.Second),
		Now:           clock.Now(),
	})
	scoreWithout := gating.Score(gating.Input{
		Lines:         base.buffer.Lines(),
		Row:           base.buffer.Row(),
		Col:           base.buffer.Col(),
		FileExtension: ".go",
		PreviousLabel: false,
		LastDecision:  clock.Now().Add(-1 * time.Second),
		Now:           clock.Now(),
	})

	assert.True(t, scoreWith > scoreWithout,
		fmt.Sprintf("momentum should increase score: with=%.3f, without=%.3f",
			scoreWith, scoreWithout))
}
