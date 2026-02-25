// DSL format for engine E2E step definitions.
//
// The "steps" section in txtar fixtures uses this format instead of JSON.
// Each step is an action optionally followed by expectations.
// Steps are separated by blank lines.
//
//	completion <startLine>-<endLineInc> [cursor=<row>:<col>]
//	  | "<completion line>"
//	expect [shown|!shown] [noDeletionGroups] [stageCount=<n>] [noGroupsBefore=<n>]
//	  buffer:
//	    | "<expected buffer line>"
//
//	accept
//
//	prefetch <startLine>-<endLineInc> [cursor=<row>:<col>]
//	  | "<completion line>"
//	expect ...
//
// | lines are quoted with " and only " and \ are escaped.
// cursor= on an action line sets the cursor before that step (setCursor).
//
// Example:
//
//	completion 2-2
//	  | "  return a + b;"
//	expect shown noDeletionGroups
//	  buffer:
//	    | "function add(a, b) {"
//	    | "  return a + b;"
//	    | "}"
//
//	accept
package engine

import (
	"cursortab/e2e"
	"fmt"
	"strings"
)

// FormatSteps converts scenario steps to the engine DSL format.
func FormatSteps(steps []scenarioStep) string {
	var b strings.Builder
	for i, step := range steps {
		if i > 0 {
			b.WriteByte('\n')
		}

		switch step.Action {
		case "completion", "prefetch":
			fmt.Fprintf(&b, "%s %d-%d", step.Action, step.Completion.StartLine, step.Completion.EndLineInc)
			if step.SetCursor != nil {
				fmt.Fprintf(&b, " cursor=%d:%d", step.SetCursor.Row, step.SetCursor.Col)
			}
			b.WriteByte('\n')
			for _, line := range step.Completion.Lines {
				fmt.Fprintf(&b, "  | %s\n", e2e.QuoteLine(line))
			}

		case "accept":
			b.WriteString("accept\n")
		}

		if step.Expect != nil {
			formatExpectations(&b, step.Expect)
		}
	}
	return b.String()
}

func formatExpectations(b *strings.Builder, e *expectations) {
	b.WriteString("expect")
	if e.Shown != nil {
		if *e.Shown {
			b.WriteString(" shown")
		} else {
			b.WriteString(" !shown")
		}
	}
	if e.NoDeletionGroups != nil && *e.NoDeletionGroups {
		b.WriteString(" noDeletionGroups")
	}
	if e.StageCount != nil {
		fmt.Fprintf(b, " stageCount=%d", *e.StageCount)
	}
	if e.NoGroupsBefore > 0 {
		fmt.Fprintf(b, " noGroupsBefore=%d", e.NoGroupsBefore)
	}
	b.WriteByte('\n')

	if e.BufferAfterAccept != nil {
		b.WriteString("  buffer:\n")
		for _, line := range e.BufferAfterAccept {
			fmt.Fprintf(b, "    | %s\n", e2e.QuoteLine(line))
		}
	}
}

// ParseSteps parses the engine DSL format back into scenario steps.
func ParseSteps(s string) ([]scenarioStep, error) {
	lines := strings.Split(s, "\n")
	var steps []scenarioStep
	i := 0

	for i < len(lines) {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			i++
			continue
		}

		if line == "accept" {
			steps = append(steps, scenarioStep{Action: "accept"})
			i++
			// Check for expect after accept
			if i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "expect") {
				expect, newI, err := parseExpect(lines, i)
				if err != nil {
					return nil, fmt.Errorf("line %d: %w", i+1, err)
				}
				steps[len(steps)-1].Expect = expect
				i = newI
			}
			continue
		}

		if strings.HasPrefix(line, "completion ") || strings.HasPrefix(line, "prefetch ") {
			step, newI, err := parseActionStep(lines, i)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", i+1, err)
			}
			steps = append(steps, step)
			i = newI
			continue
		}

		return nil, fmt.Errorf("line %d: unexpected: %s", i+1, line)
	}

	return steps, nil
}

func parseActionStep(lines []string, i int) (scenarioStep, int, error) {
	line := strings.TrimSpace(lines[i])
	parts := strings.Fields(line)

	step := scenarioStep{Action: parts[0]}

	// Parse range: <start>-<endInc>
	if len(parts) < 2 {
		return step, i, fmt.Errorf("missing range in %s", line)
	}
	comp := &completionData{}
	n, err := fmt.Sscanf(parts[1], "%d-%d", &comp.StartLine, &comp.EndLineInc)
	if err != nil || n != 2 {
		return step, i, fmt.Errorf("invalid range %q in %s", parts[1], line)
	}
	step.Completion = comp

	// Parse optional cursor=<row>:<col>
	for _, p := range parts[2:] {
		if strings.HasPrefix(p, "cursor=") {
			cur := &cursorPos{}
			fmt.Sscanf(p, "cursor=%d:%d", &cur.Row, &cur.Col)
			step.SetCursor = cur
		}
	}

	i++

	// Parse completion lines: "  | <quoted>"
	for i < len(lines) {
		trimmed := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(trimmed, "| ") {
			break
		}
		quoted := trimmed[2:]
		val, err := e2e.UnquoteLine(quoted)
		if err != nil {
			return step, i, fmt.Errorf("line %d: %w", i+1, err)
		}
		comp.Lines = append(comp.Lines, val)
		i++
	}

	// Parse optional expect
	if i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "expect") {
		expect, newI, err := parseExpect(lines, i)
		if err != nil {
			return step, i, err
		}
		step.Expect = expect
		i = newI
	}

	return step, i, nil
}

func parseExpect(lines []string, i int) (*expectations, int, error) {
	line := strings.TrimSpace(lines[i])
	if !strings.HasPrefix(line, "expect") {
		return nil, i, fmt.Errorf("expected 'expect', got: %s", line)
	}

	e := &expectations{}
	parts := strings.Fields(line)
	for _, p := range parts[1:] {
		switch p {
		case "shown":
			t := true
			e.Shown = &t
		case "!shown":
			f := false
			e.Shown = &f
		case "noDeletionGroups":
			t := true
			e.NoDeletionGroups = &t
		default:
			if strings.HasPrefix(p, "stageCount=") {
				var n int
				fmt.Sscanf(p, "stageCount=%d", &n)
				e.StageCount = &n
			} else if strings.HasPrefix(p, "noGroupsBefore=") {
				fmt.Sscanf(p, "noGroupsBefore=%d", &e.NoGroupsBefore)
			}
		}
	}

	i++

	// Parse buffer: section
	if i < len(lines) && strings.TrimSpace(lines[i]) == "buffer:" {
		i++
		for i < len(lines) {
			trimmed := strings.TrimSpace(lines[i])
			if !strings.HasPrefix(trimmed, "| ") {
				break
			}
			quoted := trimmed[2:]
			val, err := e2e.UnquoteLine(quoted)
			if err != nil {
				return nil, i, fmt.Errorf("line %d: %w", i+1, err)
			}
			e.BufferAfterAccept = append(e.BufferAfterAccept, val)
			i++
		}
	}

	return e, i, nil
}
