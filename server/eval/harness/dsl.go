package harness

import (
	"fmt"
	"strings"
	"time"
)

// ParseSteps parses the eval step DSL.
//
// Syntax:
//
//	# comment
//	request-completion [manual]
//	wait 50ms
//	accept
//	reject
//
// Each step starts in column 0 with the action name. Indented lines are
// ignored (legacy expect blocks).
func ParseSteps(src string) ([]Step, error) {
	lines := strings.Split(src, "\n")
	var steps []Step
	i := 0
	for i < len(lines) {
		raw := lines[i]
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			i++
			continue
		}
		if isIndented(raw) {
			i++
			continue
		}

		step, next, err := parseStepBlock(lines, i)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		steps = append(steps, step)
		i = next
	}
	return steps, nil
}

func parseStepBlock(lines []string, i int) (Step, int, error) {
	head := strings.TrimSpace(lines[i])
	fields := strings.Fields(head)
	if len(fields) == 0 {
		return Step{}, i + 1, fmt.Errorf("empty step")
	}
	action := StepAction(fields[0])

	step := Step{Action: action}

	switch action {
	case ActionRequestCompletion:
		for _, f := range fields[1:] {
			if f == "manual" {
				step.Manual = true
			}
		}
	case ActionAccept, ActionReject:
		// no args
	case ActionWait:
		if len(fields) < 2 {
			return step, i, fmt.Errorf("wait: missing duration")
		}
		d, err := time.ParseDuration(fields[1])
		if err != nil {
			return step, i, fmt.Errorf("wait: invalid duration %q: %w", fields[1], err)
		}
		step.Wait = d
	default:
		return step, i, fmt.Errorf("unknown action %q", fields[0])
	}
	i++

	// Skip any indented lines (legacy expect blocks).
	for i < len(lines) {
		line := lines[i]
		if line == "" {
			i++
			break
		}
		if !isIndented(line) {
			break
		}
		i++
	}

	return step, i, nil
}

func isIndented(line string) bool {
	return len(line) > 0 && (line[0] == ' ' || line[0] == '\t')
}
