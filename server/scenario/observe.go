package scenario

import (
	"fmt"
	"slices"
	"strings"
)

// Observation is what a runner sees after a step: the editor and the engine
// from outside.
type Observation struct {
	Buffer       []string
	Row, Col     int      // Cursor, 1-indexed row and 0-indexed byte column
	Display      []string // Summaries of the displayed groups, nil when none is shown
	CursorTarget int      // Line of the shown cursor target, 0 for none
	State        string   // Engine state
	Requests     int      // Provider requests so far
}

// Shown reports whether a completion is displayed.
func (o Observation) Shown() bool {
	return o.Display != nil
}

// GroupSummary describes a displayed group: its type and first buffer line,
// the number of lines when more than one, and its render hint.
func GroupSummary(groupType string, bufferLine, lineCount int, hint string) string {
	s := fmt.Sprintf("%s %d", groupType, bufferLine)
	if lineCount > 1 {
		s += fmt.Sprintf(" x%d", lineCount)
	}
	if hint != "" {
		s += " " + hint
	}
	return s
}

// Check returns the differences between e and o, empty when o matches.
func (e *Expect) Check(o Observation) []string {
	var diffs []string
	if e.Shown != nil && *e.Shown != o.Shown() {
		diffs = append(diffs, fmt.Sprintf("shown: got %v, want %v", o.Shown(), *e.Shown))
	}
	if e.CursorTarget != nil && *e.CursorTarget != o.CursorTarget {
		diffs = append(diffs, fmt.Sprintf("target: got %d, want %d", o.CursorTarget, *e.CursorTarget))
	}
	if e.State != "" && !strings.EqualFold(e.State, o.State) {
		diffs = append(diffs, fmt.Sprintf("state: got %s, want %s", o.State, e.State))
	}
	if e.Requests != nil && *e.Requests != o.Requests {
		diffs = append(diffs, fmt.Sprintf("requests: got %d, want %d", o.Requests, *e.Requests))
	}
	if e.Cursor != nil && (e.Cursor[0] != o.Row || e.Cursor[1] != o.Col) {
		diffs = append(diffs, fmt.Sprintf("cursor: got %d:%d, want %d:%d", o.Row, o.Col, e.Cursor[0], e.Cursor[1]))
	}
	if e.Buffer != nil && !slices.Equal(e.Buffer, o.Buffer) {
		diffs = append(diffs, fmt.Sprintf("buffer:\n  got:  %q\n  want: %q", o.Buffer, e.Buffer))
	}
	if e.Display != nil && !slices.Equal(e.Display, o.Display) && !(len(e.Display) == 0 && o.Display == nil) {
		diffs = append(diffs, fmt.Sprintf("display:\n  got:  %q\n  want: %q", o.Display, e.Display))
	}
	return diffs
}
