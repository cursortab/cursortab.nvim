package engine

import (
	"cursortab/assert"
	"cursortab/text"
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
	Action     string          `json:"action"`
	Completion *completionData `json:"completion,omitempty"`
	SetCursor  *cursorPos      `json:"setCursor,omitempty"`
	Expect     *expectations   `json:"expect,omitempty"`
}

type expectations struct {
	Shown             *bool    `json:"shown,omitempty"`
	StageCount        *int     `json:"stageCount,omitempty"`
	NoGroupsBefore    int      `json:"noGroupsBefore,omitempty"`
	NoDeletionGroups  *bool    `json:"noDeletionGroups,omitempty"`
	BufferAfterAccept []string `json:"bufferAfterAccept,omitempty"`
}

// parseTxtarScenario parses a txtar archive into an engineScenario.
// Header contains description + buffer params. Sections: buffer.txt, steps.
func parseTxtarScenario(ar *txtar.Archive) (*engineScenario, error) {
	sc := &engineScenario{}

	lines := strings.Split(string(ar.Comment), "\n")
	if len(lines) > 0 {
		sc.Description = strings.TrimSpace(lines[0])
	}
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		n, _ := strconv.Atoi(val)
		switch key {
		case "row":
			sc.Buffer.Row = n
		case "col":
			sc.Buffer.Col = n
		case "viewportTop":
			sc.Buffer.ViewportTop = n
		case "viewportBottom":
			sc.Buffer.ViewportBottom = n
		}
	}

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

func e2eStageIsPureInsertion(stage *text.Stage) bool {
	if stage.BufferStart != stage.BufferEnd || len(stage.Groups) == 0 {
		return false
	}
	groupLines := 0
	for _, g := range stage.Groups {
		if g.Type != "addition" {
			return false
		}
		groupLines += g.EndLine - g.StartLine + 1
	}
	return len(stage.Lines) == groupLines
}

func applyStageToLines(lines []string, stage *text.Stage) []string {
	isPure := e2eStageIsPureInsertion(stage)
	start := stage.BufferStart - 1

	if isPure {
		out := make([]string, 0, len(lines)+len(stage.Lines))
		out = append(out, lines[:start]...)
		out = append(out, stage.Lines...)
		out = append(out, lines[start:]...)
		return out
	}

	end := stage.BufferEnd
	out := make([]string, 0, len(lines)-end+start+len(stage.Lines))
	out = append(out, lines[:start]...)
	out = append(out, stage.Lines...)
	if end < len(lines) {
		out = append(out, lines[end:]...)
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}

func e2eAdvanceOffsets(stages []*text.Stage, appliedIdx int) {
	stage := stages[appliedIdx]
	var oldLineCount int
	if e2eStageIsPureInsertion(stage) {
		oldLineCount = 0
	} else {
		oldLineCount = stage.BufferEnd - stage.BufferStart + 1
	}
	offset := len(stage.Lines) - oldLineCount
	if offset != 0 {
		for i := appliedIdx + 1; i < len(stages); i++ {
			if stages[i].BufferStart >= stage.BufferStart {
				stages[i].BufferStart += offset
				stages[i].BufferEnd += offset
				for _, g := range stages[i].Groups {
					g.BufferLine += offset
				}
			}
		}
	}
}

func applyAllStagesToCopy(bufLines []string, eng *Engine) []string {
	if eng.stagedCompletion == nil || len(eng.stagedCompletion.Stages) == 0 {
		return bufLines
	}

	out := append([]string{}, bufLines...)

	// Deep-copy stages so offset adjustments don't affect the engine
	stages := make([]*text.Stage, len(eng.stagedCompletion.Stages))
	for i, s := range eng.stagedCompletion.Stages {
		cp := *s
		cp.Groups = make([]*text.Group, len(s.Groups))
		for j, g := range s.Groups {
			gCopy := *g
			cp.Groups[j] = &gCopy
		}
		stages[i] = &cp
	}

	for i := range stages {
		out = applyStageToLines(out, stages[i])
		e2eAdvanceOffsets(stages, i)
	}
	return out
}

// --- Verification ---

func verifyExpectations(t *testing.T, eng *Engine, buf *mockBuffer, expect *expectations, label string) {
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

	if expect.BufferAfterAccept != nil {
		actual := applyAllStagesToCopy(buf.lines, eng)
		if len(actual) != len(expect.BufferAfterAccept) {
			t.Errorf("%s bufferAfterAccept: line count mismatch: got %d, want %d\n  got:  %v\n  want: %v",
				label, len(actual), len(expect.BufferAfterAccept), actual, expect.BufferAfterAccept)
		} else {
			for i := range actual {
				if actual[i] != expect.BufferAfterAccept[i] {
					t.Errorf("%s bufferAfterAccept: line %d mismatch:\n  got:  %q\n  want: %q",
						label, i+1, actual[i], expect.BufferAfterAccept[i])
				}
			}
		}
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
			result := eng.processCompletion(comp)
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
