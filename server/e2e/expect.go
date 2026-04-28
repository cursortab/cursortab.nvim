package e2e

import (
	"fmt"
	"strings"
	"time"
)

// Expectations is the union of all assertion flags used by the engine E2E
// and eval harnesses. Each harness checks the subset it cares about.
type Expectations struct {
	Shown            *bool
	Suppressed       *bool
	StageCount       *int
	NoGroupsBefore   int
	NoDeletionGroups *bool
	MaxLatency       time.Duration
	BufferLines      []string // bufferAfterAccept (engine) or stagedLinesAfter (eval)
	CursorTargetLine *int     // engine: ShowCursorTarget called with this line (-1 = expect not called)
	State            string   // engine: state name e.g. "idle", "hasCompletion", "hasCursorTarget"
	PrefetchState    string   // engine: "none", "inFlight", "ready", "waitingForTab", "waitingForCursorPrediction"
}

// ParseExpect parses an expect line and optional buffer: block starting at
// lines[i]. Returns the parsed expectations and the next line index.
//
// Format:
//
//	expect [shown|!shown] [suppressed|!suppressed] [noDeletionGroups] [stageCount=N] [noGroupsBefore=N] [latencyMax=DUR]
//	  buffer:
//	    | "line 1"
//	    | "line 2"
func ParseExpect(lines []string, i int) (*Expectations, int, error) {
	line := strings.TrimSpace(lines[i])
	if !strings.HasPrefix(line, "expect") {
		return nil, i, fmt.Errorf("expected 'expect', got %q", line)
	}

	e := &Expectations{}
	for _, f := range strings.Fields(line)[1:] {
		switch {
		case f == "shown":
			t := true
			e.Shown = &t
		case f == "!shown":
			f := false
			e.Shown = &f
		case f == "suppressed":
			t := true
			e.Suppressed = &t
		case f == "!suppressed":
			f := false
			e.Suppressed = &f
		case f == "noDeletionGroups":
			t := true
			e.NoDeletionGroups = &t
		case strings.HasPrefix(f, "stageCount="):
			var n int
			_, err := fmt.Sscanf(f, "stageCount=%d", &n)
			if err != nil {
				return nil, i, fmt.Errorf("bad stageCount: %w", err)
			}
			e.StageCount = &n
		case strings.HasPrefix(f, "noGroupsBefore="):
			fmt.Sscanf(f, "noGroupsBefore=%d", &e.NoGroupsBefore)
		case strings.HasPrefix(f, "cursorTargetLine="):
			var n int
			_, err := fmt.Sscanf(f, "cursorTargetLine=%d", &n)
			if err != nil {
				return nil, i, fmt.Errorf("bad cursorTargetLine: %w", err)
			}
			e.CursorTargetLine = &n
		case strings.HasPrefix(f, "state="):
			e.State = strings.TrimPrefix(f, "state=")
		case strings.HasPrefix(f, "prefetchState="):
			e.PrefetchState = strings.TrimPrefix(f, "prefetchState=")
		case strings.HasPrefix(f, "latencyMax="):
			v := strings.TrimPrefix(f, "latencyMax=")
			d, err := time.ParseDuration(v)
			if err != nil {
				return nil, i, fmt.Errorf("bad latencyMax: %w", err)
			}
			e.MaxLatency = d
		default:
			return nil, i, fmt.Errorf("unknown expect flag %q", f)
		}
	}

	i++

	// Optional buffer: block.
	if i < len(lines) && strings.TrimSpace(lines[i]) == "buffer:" {
		i++
		for i < len(lines) {
			tl := strings.TrimSpace(lines[i])
			if !strings.HasPrefix(tl, "| ") && tl != "|" {
				break
			}
			body := strings.TrimPrefix(tl, "|")
			body = strings.TrimPrefix(body, " ")
			val, err := UnquoteLine(body)
			if err != nil {
				return nil, i, fmt.Errorf("line %d: %w", i+1, err)
			}
			e.BufferLines = append(e.BufferLines, val)
			i++
		}
	}

	return e, i, nil
}
