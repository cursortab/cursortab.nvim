package scenario

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"cursortab/e2e"
)

// Step is one action of a scenario.
type Step struct {
	Action Action
	Line   int // Line of the step in the steps section, for messages

	Manual bool          // request-completion: bypass the request gates
	Wait   time.Duration // wait: time to advance
	Text   string        // type: text inserted at the cursor
	Row    int           // move: 1-indexed row
	Col    int           // move: 0-indexed byte column
	Edit   *Edit         // respond: the edit the provider proposes
	Expect *Expect       // expect: what must be observed
}

// Action is what a step does.
type Action string

const (
	ActionRequestCompletion Action = "request-completion"
	ActionRespond           Action = "respond"
	ActionAccept            Action = "accept"
	ActionPartialAccept     Action = "partial-accept"
	ActionReject            Action = "reject"
	ActionType              Action = "type"
	ActionMove              Action = "move"
	ActionWait              Action = "wait"
	ActionExpect            Action = "expect"
)

// Edit replaces the lines StartLine..EndLineInc (1-indexed, inclusive) with
// Lines. EndLineInc is StartLine-1 for an insertion before StartLine.
type Edit struct {
	StartLine  int
	EndLineInc int
	Lines      []string
}

// ParseSteps parses the steps section of a scenario.
//
// Syntax, one step per line in column 0:
//
//	# comment
//	request-completion [manual]
//	respond <start>-<end>
//	  | "line"
//	accept
//	partial-accept
//	reject
//	type "text"
//	move <row>:<col>
//	wait <duration>
//	expect [shown|hidden] [target=<line>|target=none] [state=<name>] [requests=<n>] [cursor=<row>:<col>]
//	  buffer:
//	    | "line"
//	  display:
//	    | <group summary>
//
// Lines start with "| " followed by a Go-quoted string, except display
// summaries, which are plain text.
//
// Each step ends when the engine settles: it handled every event, a pending
// text change debounce fired as if the user paused, and no request is in
// flight.
func ParseSteps(src string) ([]Step, error) {
	lines := strings.Split(src, "\n")
	var steps []Step
	for i := 0; i < len(lines); {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			i++
			continue
		}
		if isIndented(lines[i]) {
			return nil, fmt.Errorf("line %d: indented line outside a step: %q", i+1, trimmed)
		}
		step, next, err := parseStep(lines, i)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		step.Line = i + 1
		steps = append(steps, step)
		i = next
	}
	return steps, nil
}

func parseStep(lines []string, i int) (Step, int, error) {
	head := strings.TrimSpace(lines[i])
	name, args, _ := strings.Cut(head, " ")
	args = strings.TrimSpace(args)
	step := Step{Action: Action(name)}
	i++

	switch step.Action {
	case ActionRequestCompletion:
		switch args {
		case "":
		case "manual":
			step.Manual = true
		default:
			return step, i, fmt.Errorf("request-completion: unknown argument %q", args)
		}
	case ActionAccept, ActionPartialAccept, ActionReject:
		if args != "" {
			return step, i, fmt.Errorf("%s takes no arguments", name)
		}
	case ActionWait:
		d, err := time.ParseDuration(args)
		if err != nil {
			return step, i, fmt.Errorf("wait: %w", err)
		}
		step.Wait = d
	case ActionType:
		text, err := strconv.Unquote(args)
		if err != nil {
			return step, i, fmt.Errorf("type: want a quoted string, got %s", args)
		}
		step.Text = text
	case ActionMove:
		row, col, err := parsePosition(args)
		if err != nil {
			return step, i, fmt.Errorf("move: %w", err)
		}
		step.Row, step.Col = row, col
	case ActionRespond:
		edit := &Edit{}
		if _, err := fmt.Sscanf(args, "%d-%d", &edit.StartLine, &edit.EndLineInc); err != nil {
			return step, i, fmt.Errorf("respond: want <start>-<end>, got %q", args)
		}
		var err error
		edit.Lines, i, err = parseQuotedLines(lines, i)
		if err != nil {
			return step, i, fmt.Errorf("respond: %w", err)
		}
		step.Edit = edit
	case ActionExpect:
		expect, next, err := parseExpect(args, lines, i)
		if err != nil {
			return step, i, fmt.Errorf("expect: %w", err)
		}
		step.Expect = expect
		i = next
	default:
		return step, i, fmt.Errorf("unknown action %q", name)
	}
	return step, i, nil
}

// Expect lists what must be observed after the previous steps. Nil and empty
// fields are not checked.
type Expect struct {
	Shown        *bool    // shown or hidden: a completion is displayed
	CursorTarget *int     // target=<line>: the cursor target shown, 0 for none
	State        string   // state=<name>: the engine state
	Requests     *int     // requests=<n>: provider requests so far
	Cursor       *[2]int  // cursor=<row>:<col>
	Buffer       []string // buffer: the buffer lines
	Display      []string // display: summaries of the displayed groups
}

func parseExpect(args string, lines []string, i int) (*Expect, int, error) {
	e := &Expect{}
	for _, f := range strings.Fields(args) {
		key, value, _ := strings.Cut(f, "=")
		switch key {
		case "shown", "hidden":
			shown := key == "shown"
			e.Shown = &shown
		case "target":
			line := 0
			if value != "none" {
				n, err := strconv.Atoi(value)
				if err != nil {
					return nil, i, fmt.Errorf("target: %w", err)
				}
				line = n
			}
			e.CursorTarget = &line
		case "state":
			e.State = value
		case "requests":
			n, err := strconv.Atoi(value)
			if err != nil {
				return nil, i, fmt.Errorf("requests: %w", err)
			}
			e.Requests = &n
		case "cursor":
			row, col, err := parsePosition(value)
			if err != nil {
				return nil, i, fmt.Errorf("cursor: %w", err)
			}
			e.Cursor = &[2]int{row, col}
		default:
			return nil, i, fmt.Errorf("unknown flag %q", f)
		}
	}

	for i < len(lines) && isIndented(lines[i]) {
		var err error
		switch section := strings.TrimSpace(lines[i]); section {
		case "buffer:":
			e.Buffer, i, err = parseQuotedLines(lines, i+1)
			if e.Buffer == nil {
				e.Buffer = []string{}
			}
		case "display:":
			e.Display, i = parsePlainLines(lines, i+1)
			if e.Display == nil {
				e.Display = []string{}
			}
		default:
			return nil, i, fmt.Errorf("unknown section %q", section)
		}
		if err != nil {
			return nil, i, err
		}
	}
	return e, i, nil
}

// parseQuotedLines reads the "| <quoted>" lines starting at lines[i].
func parseQuotedLines(lines []string, i int) ([]string, int, error) {
	var out []string
	for ; i < len(lines); i++ {
		body, ok := pipeLine(lines[i])
		if !ok {
			break
		}
		value, err := e2e.UnquoteLine(body)
		if err != nil {
			return nil, i, fmt.Errorf("line %d: %w", i+1, err)
		}
		out = append(out, value)
	}
	return out, i, nil
}

// parsePlainLines reads the "| <text>" lines starting at lines[i].
func parsePlainLines(lines []string, i int) ([]string, int) {
	var out []string
	for ; i < len(lines); i++ {
		body, ok := pipeLine(lines[i])
		if !ok {
			break
		}
		out = append(out, body)
	}
	return out, i
}

func pipeLine(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed != "|" && !strings.HasPrefix(trimmed, "| ") {
		return "", false
	}
	return strings.TrimPrefix(strings.TrimPrefix(trimmed, "|"), " "), true
}

func parsePosition(s string) (row, col int, err error) {
	if _, err := fmt.Sscanf(s, "%d:%d", &row, &col); err != nil {
		return 0, 0, fmt.Errorf("want <row>:<col>, got %q", s)
	}
	return row, col, nil
}

func isIndented(line string) bool {
	return len(line) > 0 && (line[0] == ' ' || line[0] == '\t')
}
