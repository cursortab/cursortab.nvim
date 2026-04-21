package engine

import (
	"cursortab/assert"
	"cursortab/e2e"
	"cursortab/types"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/tools/txtar"
)

// --- Fixture schema ---

type engineScenario struct {
	Description string
	Buffer      bufferState
	Steps       []scenarioStep
}

type bufferState struct {
	Lines          []string
	Row            int
	Col            int
	ViewportTop    int
	ViewportBottom int
}

type completionData struct {
	StartLine  int      `json:"startLine"`
	EndLineInc int      `json:"endLineInc"`
	Lines      []string `json:"lines"`
}

type cursorPos struct {
	Row int `json:"row"`
	Col int `json:"col"`
}

type scenarioStep struct {
	Action     string            `json:"action"`
	Completion *completionData   `json:"completion,omitempty"`
	SetCursor  *cursorPos        `json:"setCursor,omitempty"`
	Expect     *e2e.Expectations `json:"expect,omitempty"`
}

// parseTxtarScenario parses a txtar archive into an engineScenario.
func parseTxtarScenario(ar *txtar.Archive) (*engineScenario, error) {
	sc := &engineScenario{}
	hdr := e2e.ParseHeader(ar.Comment)

	sc.Description = hdr[""]
	sc.Buffer.Row, _ = strconv.Atoi(hdr["row"])
	sc.Buffer.Col, _ = strconv.Atoi(hdr["col"])
	sc.Buffer.ViewportTop, _ = strconv.Atoi(hdr["viewportTop"])
	sc.Buffer.ViewportBottom, _ = strconv.Atoi(hdr["viewportBottom"])

	for _, f := range ar.Files {
		switch f.Name {
		case "buffer.txt":
			content := strings.TrimSuffix(string(f.Data), "\n")
			sc.Buffer.Lines = strings.Split(content, "\n")
		case "steps":
			stepsDSL := strings.TrimSuffix(string(f.Data), "\n")
			var err error
			sc.Steps, err = ParseSteps(stepsDSL)
			if err != nil {
				return nil, fmt.Errorf("parse steps: %w", err)
			}
		}
	}

	return sc, nil
}

// --- Helpers ---

// applyAllStagesToCopy applies every staged completion stage to a copy of
// bufLines. Helper for test assertions that want the post-accept buffer.
func applyAllStagesToCopy(bufLines []string, eng *Engine) []string {
	if eng.stagedCompletion == nil {
		return bufLines
	}
	return applyAllStages(bufLines, eng.stagedCompletion.Stages)
}

// --- Verification ---

func verifyExpectations(t *testing.T, eng *Engine, buf *mockBuffer, expect *e2e.Expectations, label string) {
	t.Helper()
	if expect == nil {
		return
	}

	if expect.StageCount != nil {
		actual := 0
		if eng.stagedCompletion != nil {
			actual = len(eng.stagedCompletion.Stages)
		}
		assert.Equal(t, *expect.StageCount, actual, label+" stageCount")
	}

	if expect.NoGroupsBefore > 0 && eng.stagedCompletion != nil {
		for _, stage := range eng.stagedCompletion.Stages {
			for _, g := range stage.Groups {
				if g.BufferLine < expect.NoGroupsBefore {
					t.Errorf("%s: group %q at buffer_line %d is before %d",
						label, g.Type, g.BufferLine, expect.NoGroupsBefore)
				}
			}
		}
	}

	if expect.NoDeletionGroups != nil && *expect.NoDeletionGroups && eng.stagedCompletion != nil {
		for _, stage := range eng.stagedCompletion.Stages {
			for _, g := range stage.Groups {
				if g.Type == "deletion" {
					t.Errorf("%s: unexpected deletion group at buffer_line %d", label, g.BufferLine)
				}
			}
		}
	}

	if expect.BufferLines != nil {
		actual := applyAllStagesToCopy(buf.lines, eng)
		if len(actual) != len(expect.BufferLines) {
			t.Errorf("%s bufferAfterAccept: line count mismatch: got %d, want %d\n  got:  %v\n  want: %v",
				label, len(actual), len(expect.BufferLines), actual, expect.BufferLines)
		} else {
			for i := range actual {
				if actual[i] != expect.BufferLines[i] {
					t.Errorf("%s bufferAfterAccept: line %d mismatch:\n  got:  %q\n  want: %q",
						label, i+1, actual[i], expect.BufferLines[i])
				}
			}
		}
	}

	if expect.CursorTargetLine != nil {
		assert.Equal(t, *expect.CursorTargetLine, buf.showCursorTargetLine, label+" cursorTargetLine")
	}

	if expect.State != "" {
		actual := strings.ToLower(eng.state.String())
		want := strings.ToLower(expect.State)
		if actual != want {
			t.Errorf("%s state: got %q, want %q", label, actual, want)
		}
	}

	if expect.PrefetchState != "" {
		actual := prefetchStateName(eng.prefetchState)
		if !strings.EqualFold(actual, expect.PrefetchState) {
			t.Errorf("%s prefetchState: got %q, want %q", label, actual, expect.PrefetchState)
		}
	}
}

func prefetchStateName(s prefetchState) string {
	switch s {
	case prefetchNone:
		return "none"
	case prefetchInFlight:
		return "inFlight"
	case prefetchWaitingForTab:
		return "waitingForTab"
	case prefetchWaitingForCursorPrediction:
		return "waitingForCursorPrediction"
	case prefetchReady:
		return "ready"
	default:
		return "unknown"
	}
}

// --- Test runner ---

func runEngineScenario(t *testing.T, sc *engineScenario) {
	t.Helper()

	buf := newMockBuffer()
	buf.lines = append([]string{}, sc.Buffer.Lines...)
	buf.row = sc.Buffer.Row
	buf.col = sc.Buffer.Col
	buf.viewportTop = sc.Buffer.ViewportTop
	buf.viewportBottom = sc.Buffer.ViewportBottom

	prov := newMockProvider()
	clock := newMockClock()
	eng, cancel := createTestEngineWithContext(buf, prov, clock)
	defer cancel()

	for i, step := range sc.Steps {
		label := fmt.Sprintf("step[%d] %s", i, step.Action)

		if step.SetCursor != nil {
			buf.mu.Lock()
			buf.row = step.SetCursor.Row
			buf.col = step.SetCursor.Col
			buf.mu.Unlock()
		}

		switch step.Action {
		case "completion":
			if step.Completion == nil {
				t.Fatalf("%s: missing completion field", label)
			}
			comp := &types.Completion{
				StartLine:  step.Completion.StartLine,
				EndLineInc: step.Completion.EndLineInc,
				Lines:      step.Completion.Lines,
			}
			result := eng.processCompletion(comp) == completionShown
			if step.Expect != nil && step.Expect.Shown != nil {
				assert.Equal(t, *step.Expect.Shown, result, label+" shown")
			}
			verifyExpectations(t, eng, buf, step.Expect, label)

		case "prefetch":
			if step.Completion == nil {
				t.Fatalf("%s: missing completion field", label)
			}
			eng.prefetchedCompletions = []*types.Completion{{
				StartLine:  step.Completion.StartLine,
				EndLineInc: step.Completion.EndLineInc,
				Lines:      step.Completion.Lines,
			}}
			eng.prefetchState = prefetchReady
			result := eng.tryShowPrefetchedCompletion()
			if step.Expect != nil && step.Expect.Shown != nil {
				assert.Equal(t, *step.Expect.Shown, result, label+" shown")
			}
			verifyExpectations(t, eng, buf, step.Expect, label)

		case "stash-prefetch":
			if step.Completion == nil {
				t.Fatalf("%s: missing completion field", label)
			}
			eng.prefetchedCompletions = []*types.Completion{{
				StartLine:  step.Completion.StartLine,
				EndLineInc: step.Completion.EndLineInc,
				Lines:      step.Completion.Lines,
			}}
			eng.prefetchState = prefetchReady
			verifyExpectations(t, eng, buf, step.Expect, label)

		case "accept":
			if eng.stagedCompletion != nil && eng.stagedCompletion.CurrentIdx < len(eng.stagedCompletion.Stages) {
				stage := eng.getStage(eng.stagedCompletion.CurrentIdx)
				if stage != nil {
					buf.mu.Lock()
					buf.lines = applyStageToLines(buf.lines, stage)
					buf.mu.Unlock()
				}
			}
			eng.acceptCompletion()
			verifyExpectations(t, eng, buf, step.Expect, label)

		default:
			t.Fatalf("%s: unknown action %q", label, step.Action)
		}
	}
}

func TestEngineE2E(t *testing.T) {
	e2eDir := filepath.Join("testdata")
	entries, err := os.ReadDir(e2eDir)
	if err != nil {
		t.Fatalf("failed to read e2e directory: %v", err)
	}

	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".txtar") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".txtar")

		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(e2eDir, entry.Name()))
			assert.NoError(t, err, "read fixture")

			ar := txtar.Parse(data)
			scenario, err := parseTxtarScenario(ar)
			assert.NoError(t, err, "parse fixture")

			runEngineScenario(t, scenario)
		})
	}
}
