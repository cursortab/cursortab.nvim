package engine

import (
	"testing"

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
