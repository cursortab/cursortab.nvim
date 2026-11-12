package engine

import (
	"fmt"
	"testing"
	"time"

	"cursortab/assert"
	"cursortab/types"
)

// showCompletion shows c, requested by hand, on a buffer of lines with the
// cursor at row:col.
func showCompletion(t *testing.T, c *types.Completion, lines []string, row, col int) (*harness, *mockProvider) {
	t.Helper()
	prov := newMockProvider()
	buf := newMockBuffer(lines...)
	buf.row, buf.col = row, col
	h := newHarness(t, buf, prov)
	prov.push(c)
	h.send("trigger_completion")
	_, shown, target := buf.display()
	if !shown && target == 0 {
		t.Fatal("completion not displayed")
	}
	return h, prov
}

// requestAgain sends events, answers the request they start with c, and
// reports whether c was displayed.
func requestAgain(h *harness, prov *mockProvider, c *types.Completion, events ...string) bool {
	h.t.Helper()
	calls := prov.calls()
	renders, _, _ := h.buf.display()
	prov.push(c)
	h.send(events...)
	if prov.calls() == calls {
		h.t.Fatal("no request")
	}
	after, _, target := h.buf.display()
	return after > renders || target > 0
}

func numbered(n int, format string) []string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf(format, i+1)
	}
	return lines
}

// A completion like one the user rejected with Esc is not shown again by a
// request the editor starts, for a while, in the same context.
func TestRejectedCompletionIsSuppressed(t *testing.T) {
	big := func(first string) *types.Completion {
		lines := numbered(50, "line %d updated")
		lines[0] = first
		return &types.Completion{StartLine: 1, EndLineInc: 50, Lines: lines}
	}
	tests := []struct {
		name       string
		lines      []string
		row, col   int
		completion *types.Completion
		again      *types.Completion // Default: completion
		between    func(h *harness)
		event      string // Default: text_changed
		shown      bool
	}{
		{
			name:  "similar completion",
			lines: []string{"hello"}, row: 1, col: 5,
			completion: &types.Completion{StartLine: 1, EndLineInc: 1, Lines: []string{"hello world"}},
			again:      &types.Completion{StartLine: 1, EndLineInc: 1, Lines: []string{"hello world!"}},
		},
		{
			name:  "manual request",
			lines: []string{"hello"}, row: 1, col: 5,
			completion: &types.Completion{StartLine: 1, EndLineInc: 1, Lines: []string{"hello world"}},
			event:      "trigger_completion",
			shown:      true,
		},
		{
			name:  "after the rejection expired",
			lines: []string{"hello"}, row: 1, col: 5,
			completion: &types.Completion{StartLine: 1, EndLineInc: 1, Lines: []string{"hello world"}},
			between:    func(h *harness) { h.clock.Advance(rejectedCompletionTTL + time.Second) },
			shown:      true,
		},
		{
			name:  "after the user typed on",
			lines: []string{"import "}, row: 1, col: 7,
			completion: &types.Completion{StartLine: 1, EndLineInc: 1, Lines: []string{"import numpy as np"}},
			between:    func(h *harness) { h.buf.lines, h.buf.col = []string{"import nump"}, 11 },
			shown:      true,
		},
		{
			name:  "insertion into an empty line",
			lines: []string{"def foo():", "", "bar = 1"}, row: 2, col: 0,
			completion: &types.Completion{StartLine: 2, EndLineInc: 2, Lines: []string{`    print("hi")`}},
		},
		{
			name:  "multi-stage completion",
			lines: []string{"function a() {", "  return 1;", "}", "", "function b() {", "  return 2;", "}"}, row: 2, col: 0,
			completion: &types.Completion{StartLine: 1, EndLineInc: 7, Lines: []string{"function a() {", "  return 10;", "}", "", "function b() {", "  return 20;", "}"}},
		},
		{
			name:  "line deletion",
			lines: []string{"keep this", "drop this", "and keep this"}, row: 2, col: 0,
			completion: &types.Completion{StartLine: 2, EndLineInc: 2, Lines: []string{}},
		},
		{
			name:  "blank line deletion",
			lines: []string{"first line", "", "third line"}, row: 2, col: 0,
			completion: &types.Completion{StartLine: 2, EndLineInc: 2, Lines: []string{}},
		},
		{
			name:  "line join",
			lines: []string{"if condition:", "    pass"}, row: 1, col: 13,
			completion: &types.Completion{StartLine: 1, EndLineInc: 2, Lines: []string{"if condition:    pass"}},
		},
		{
			name:  "one different line among many equal ones",
			lines: numbered(50, "line %d"), row: 1, col: 0,
			completion: big("import path/to/foo"),
			again:      big("import totally/different/bar"),
			shown:      true,
		},
		{
			name:  "punctuation added to a short context line",
			lines: []string{"fn foo() {", "  let x = 1;", "}"}, row: 2, col: 0,
			completion: &types.Completion{StartLine: 2, EndLineInc: 2, Lines: []string{"  let x = 42;"}},
			between:    func(h *harness) { h.buf.lines = []string{"fn foo() {", "  let x = 1;", "};"} },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, prov := showCompletion(t, tt.completion, tt.lines, tt.row, tt.col)
			h.send("esc")
			if tt.between != nil {
				tt.between(h)
			}
			again, event := tt.again, tt.event
			if again == nil {
				again = tt.completion
			}
			if event == "" {
				event = "text_changed"
			}
			assert.Equal(t, tt.shown, requestAgain(h, prov, again, event), "shown again")
		})
	}
}

// Typing that diverges from a shown completion rejects it like Esc.
func TestTypingAgainstCompletionRejectsIt(t *testing.T) {
	line := "    total = compute_total(items, tax_rate)"
	completion := &types.Completion{StartLine: 1, EndLineInc: 1, Lines: []string{line + ".round(2)"}}
	h, prov := showCompletion(t, completion, []string{line}, 1, len(line))

	h.buf.lines, h.buf.col = []string{line + ","}, len(line)+1
	assert.False(t, requestAgain(h, prov, completion, "text_changed"), "same completion after diverging typing")
}

// Typing while a cursor target is shown rejects its completion.
func TestTypingAgainstCursorTargetRejectsIt(t *testing.T) {
	completion := &types.Completion{StartLine: 10, EndLineInc: 10, Lines: []string{"line 10 modified"}}
	h, prov := showCompletion(t, completion, numbered(20, "line %d"), 1, 0)
	assert.Equal(t, 10, h.buf.target, "cursor target shown")

	h.buf.lines[0], h.buf.col = "line 1x", 7
	assert.False(t, requestAgain(h, prov, completion, "text_changed"), "same completion after typing")
}

// Accepting a completion, or jumping to a cursor target, is progress: the
// rejections of the file no longer apply.
func TestAcceptForgetsRejections(t *testing.T) {
	t.Run("completion", func(t *testing.T) {
		rejected := &types.Completion{StartLine: 1, EndLineInc: 1, Lines: []string{"hello world"}}
		h, prov := showCompletion(t, rejected, []string{"hello", "", "other"}, 1, 5)
		h.send("esc")
		prov.push(&types.Completion{StartLine: 3, EndLineInc: 3, Lines: []string{"other thing"}})
		h.buf.row = 3
		h.send("trigger_completion")
		h.send("accept")

		h.buf.row, h.buf.col = 1, 5
		assert.True(t, requestAgain(h, prov, rejected, "text_changed"), "rejected completion shown again")
	})
	t.Run("cursor target", func(t *testing.T) {
		rejected := &types.Completion{StartLine: 10, EndLineInc: 10, Lines: []string{"line 10 modified"}}
		h, prov := showCompletion(t, rejected, numbered(20, "line %d"), 1, 0)
		h.send("esc")
		prov.push(&types.Completion{StartLine: 15, EndLineInc: 15, Lines: []string{"line 15 modified"}})
		h.send("trigger_completion")
		assert.Equal(t, 15, h.buf.target, "second cursor target shown")
		h.send("accept")
		h.send("esc")

		assert.True(t, requestAgain(h, prov, rejected, "text_changed"), "rejected completion shown again")
	})
}
