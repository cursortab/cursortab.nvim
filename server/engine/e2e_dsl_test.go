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
				expect, newI, err := e2e.ParseExpect(lines, i)
				if err != nil {
					return nil, fmt.Errorf("line %d: %w", i+1, err)
				}
				steps[len(steps)-1].Expect = expect
				i = newI
			}
			continue
		}

		if strings.HasPrefix(line, "completion ") || strings.HasPrefix(line, "prefetch ") || strings.HasPrefix(line, "stash-prefetch ") {
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
		expect, newI, err := e2e.ParseExpect(lines, i)
		if err != nil {
			return step, i, err
		}
		step.Expect = expect
		i = newI
	}

	return step, i, nil
}
