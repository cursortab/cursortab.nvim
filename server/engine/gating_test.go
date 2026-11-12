package engine

import (
	"testing"

	"cursortab/assert"
)

// A single deleted character is a typo fix and starts no request; a streak
// of deletions is a rewrite and does.
func TestDeletionGating(t *testing.T) {
	prov := newMockProvider()
	h := newHarness(t, newMockBuffer("abcdef"), prov)
	h.buf.col = 6
	h.send("text_changed")

	var requested []bool
	for _, line := range []string{"abcde", "abcd", "abc"} {
		calls := prov.calls()
		h.buf.lines = []string{line}
		h.buf.col = len(line)
		h.send("text_changed")
		requested = append(requested, prov.calls() > calls)
	}
	assert.Equal(t, []bool{false, false, true}, requested, "requests after each deletion")

	calls := prov.calls()
	h.buf.lines = []string{"abcx"}
	h.buf.col = 4
	h.send("text_changed")
	assert.Equal(t, calls+1, prov.calls(), "typing requests again")
}

// An inline provider cannot complete before code on the cursor line; edit
// and FIM providers can.
func TestMidLineGating(t *testing.T) {
	tests := []struct {
		name      string
		kind      CompletionKind
		line      string
		col       int
		requested bool
	}{
		{"edit provider mid-line", CompletionEdit, "func process(items []string) {", 14, true},
		{"FIM provider mid-line", CompletionFIM, "for _, item := range items {", 21, true},
		{"inline provider at the end of the line", CompletionInline, "result = ", 9, true},
		{"inline provider before code", CompletionInline, "for _, item := range items {", 21, false},
		{"inline provider before a closing paren", CompletionInline, "result = append(result, )", 23, true},
		{"inline provider before a paren and semicolon", CompletionInline, "doSomething();", 12, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prov := newMockProvider()
			prov.kind = tt.kind
			buf := newMockBuffer(tt.line)
			buf.col = tt.col
			h := newHarness(t, buf, prov)

			h.send("text_changed")

			assert.Equal(t, tt.requested, prov.calls() == 1, "request started")
		})
	}
}
