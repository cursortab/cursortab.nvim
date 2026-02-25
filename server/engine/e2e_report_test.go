package engine

import (
	"cursortab/e2e"
	"cursortab/text"
	"cursortab/types"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/txtar"
)

// --- Report data ---

type stepResult struct {
	Action     string
	Completion *completionData

	// Buffer snapshot before this step
	BufferLines []string
	CursorRow   int
	CursorCol   int

	// Diff construction analysis
	OldLines     []string // old lines extracted from buffer
	OldStart     int      // startLine
	OldEndOrig   int      // original endLineInc
	OldEndActual int      // actual end after max() extension
	NewLines     []string // completion.Lines

	// Engine output
	Stages []e2e.StageInfo
	Shown  bool

	// Assertion results
	Failures []string
	Pass     bool

	// Buffer comparison (when bufferAfterAccept is specified)
	ExpectedBuffer []string
	ActualBuffer   []string
}

type scenarioResult struct {
	Name        string
	Description string
	Steps       []stepResult
	Pass        bool
}

// --- Old lines extraction (mirrors processCompletion logic) ---

func extractOldLines(bufferLines []string, comp *completionData) (oldLines []string, origEnd, actualEnd int) {
	origEnd = comp.EndLineInc
	actualEnd = comp.EndLineInc
	for i := actualEnd + 1; i < comp.StartLine+len(comp.Lines) && i-1 < len(bufferLines); i++ {
		compIdx := i - comp.StartLine
		if compIdx < len(comp.Lines) && bufferLines[i-1] == comp.Lines[compIdx] {
			actualEnd = i
		} else {
			break
		}
	}
	for i := comp.StartLine; i <= actualEnd && i-1 < len(bufferLines); i++ {
		oldLines = append(oldLines, bufferLines[i-1])
	}
	return
}

func stagesFromEngine(stages []*text.Stage) []e2e.StageInfo {
	var result []e2e.StageInfo
	for _, s := range stages {
		si := e2e.StageInfo{
			StartLine:  s.BufferStart,
			CursorLine: s.CursorLine,
			CursorCol:  s.CursorCol,
		}
		for _, g := range s.Groups {
			si.Groups = append(si.Groups, e2e.GroupInfo{
				Type:       g.Type,
				StartLine:  g.StartLine,
				EndLine:    g.EndLine,
				BufferLine: g.BufferLine,
				RenderHint: g.RenderHint,
				ColStart:   g.ColStart,
				ColEnd:     g.ColEnd,
				Lines:      g.Lines,
			})
		}
		result = append(result, si)
	}
	return result
}

// --- Scenario runner (captures results for report) ---

func runEngineScenarioForReport(sc *engineScenario) scenarioResult {
	result := scenarioResult{
		Description: sc.Description,
		Pass:        true,
	}

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

	for _, step := range sc.Steps {
		sr := stepResult{
			Action:      step.Action,
			Completion:  step.Completion,
			BufferLines: append([]string{}, buf.lines...),
			CursorRow:   buf.row,
			CursorCol:   buf.col,
			Pass:        true,
		}

		if step.SetCursor != nil {
			buf.mu.Lock()
			buf.row = step.SetCursor.Row
			buf.col = step.SetCursor.Col
			buf.mu.Unlock()
			sr.CursorRow = step.SetCursor.Row
			sr.CursorCol = step.SetCursor.Col
		}

		switch step.Action {
		case "completion":
			if step.Completion != nil {
				sr.OldLines, sr.OldEndOrig, sr.OldEndActual = extractOldLines(buf.lines, step.Completion)
				sr.OldStart = step.Completion.StartLine
				sr.NewLines = step.Completion.Lines

				comp := &types.Completion{
					StartLine:  step.Completion.StartLine,
					EndLineInc: step.Completion.EndLineInc,
					Lines:      step.Completion.Lines,
				}
				sr.Shown = eng.processCompletion(comp)
			}

		case "prefetch":
			if step.Completion != nil {
				sr.OldLines, sr.OldEndOrig, sr.OldEndActual = extractOldLines(buf.lines, step.Completion)
				sr.OldStart = step.Completion.StartLine
				sr.NewLines = step.Completion.Lines

				eng.prefetchedCompletions = []*types.Completion{{
					StartLine:  step.Completion.StartLine,
					EndLineInc: step.Completion.EndLineInc,
					Lines:      step.Completion.Lines,
				}}
				eng.prefetchState = prefetchReady
				sr.Shown = eng.tryShowPrefetchedCompletion()
			}

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
		}

		// Capture stages
		if eng.stagedCompletion != nil {
			sr.Stages = stagesFromEngine(eng.stagedCompletion.Stages)
		}

		// Check assertions
		if step.Expect != nil {
			if step.Expect.Shown != nil && *step.Expect.Shown != sr.Shown {
				sr.Failures = append(sr.Failures, fmt.Sprintf("shown: got %v, want %v", sr.Shown, *step.Expect.Shown))
			}
			if step.Expect.StageCount != nil {
				actual := 0
				if eng.stagedCompletion != nil {
					actual = len(eng.stagedCompletion.Stages)
				}
				if *step.Expect.StageCount != actual {
					sr.Failures = append(sr.Failures, fmt.Sprintf("stageCount: got %d, want %d", actual, *step.Expect.StageCount))
				}
			}
			if step.Expect.NoGroupsBefore > 0 && eng.stagedCompletion != nil {
				for _, stage := range eng.stagedCompletion.Stages {
					for _, g := range stage.Groups {
						if g.BufferLine < step.Expect.NoGroupsBefore {
							sr.Failures = append(sr.Failures, fmt.Sprintf("group %q at buffer_line %d before %d", g.Type, g.BufferLine, step.Expect.NoGroupsBefore))
						}
					}
				}
			}
			if step.Expect.NoDeletionGroups != nil && *step.Expect.NoDeletionGroups && eng.stagedCompletion != nil {
				for _, stage := range eng.stagedCompletion.Stages {
					for _, g := range stage.Groups {
						if g.Type == "deletion" {
							sr.Failures = append(sr.Failures, fmt.Sprintf("unexpected deletion at buffer_line %d", g.BufferLine))
						}
					}
				}
			}
			if step.Expect.BufferAfterAccept != nil {
				sr.ActualBuffer = applyAllStagesToCopy(buf.lines, eng)
				sr.ExpectedBuffer = step.Expect.BufferAfterAccept
				if len(sr.ActualBuffer) != len(sr.ExpectedBuffer) {
					sr.Failures = append(sr.Failures, fmt.Sprintf("bufferAfterAccept: %d lines, want %d", len(sr.ActualBuffer), len(sr.ExpectedBuffer)))
				} else {
					for j := range sr.ActualBuffer {
						if sr.ActualBuffer[j] != sr.ExpectedBuffer[j] {
							sr.Failures = append(sr.Failures, fmt.Sprintf("line %d: got %q, want %q", j+1, sr.ActualBuffer[j], sr.ExpectedBuffer[j]))
						}
					}
				}
			}
		}

		sr.Pass = len(sr.Failures) == 0
		if !sr.Pass {
			result.Pass = false
		}
		result.Steps = append(result.Steps, sr)
	}

	return result
}

// --- HTML report rendering ---

func renderBufferPane(b *strings.Builder, sr *stepResult) {
	b.WriteString("<div class=\"pane\">\n")
	b.WriteString("<h3>Buffer</h3><pre>")
	for i, line := range sr.BufferLines {
		bufLine := i + 1
		col := -1
		if bufLine == sr.CursorRow {
			col = sr.CursorCol
		}

		var hl e2e.LineHighlight
		if sr.Completion != nil {
			if bufLine >= sr.OldStart && bufLine <= sr.OldEndOrig {
				hl.Class = "mod" // requested range
			} else if bufLine > sr.OldEndOrig && bufLine <= sr.OldEndActual {
				hl.Class = "wrong-line" // extended range (bug)
			}
		}
		e2e.RenderLine(b, bufLine, line, hl, col)
	}
	b.WriteString("</pre></div>\n")
}

func renderDiffPane(b *strings.Builder, sr *stepResult) {
	b.WriteString("<div class=\"pane\">\n")
	b.WriteString("<h3>Old (extracted)</h3><pre>")
	for i, line := range sr.OldLines {
		bufLine := sr.OldStart + i
		var hl e2e.LineHighlight
		if bufLine > sr.OldEndOrig {
			hl.Class = "wrong-line"
		}
		e2e.RenderLine(b, bufLine, line, hl, -1)
	}
	b.WriteString("</pre>\n")

	b.WriteString("<h3>New (completion)</h3><pre>")
	for i, line := range sr.NewLines {
		e2e.RenderLine(b, i+1, line, e2e.LineHighlight{Class: "add"}, -1)
	}
	b.WriteString("</pre></div>\n")
}

func renderAfterAcceptPane(b *strings.Builder, sr *stepResult) {
	if sr.ExpectedBuffer == nil && sr.ActualBuffer == nil {
		return
	}
	b.WriteString("<div class=\"pane\">\n")
	b.WriteString("<h3>After Accept</h3><pre>")

	// Build a set of wrong line indices
	wrongLines := map[int]bool{}
	maxLen := max(len(sr.ActualBuffer), len(sr.ExpectedBuffer))
	for i := 0; i < maxLen; i++ {
		got := ""
		if i < len(sr.ActualBuffer) {
			got = sr.ActualBuffer[i]
		}
		want := ""
		if i < len(sr.ExpectedBuffer) {
			want = sr.ExpectedBuffer[i]
		}
		if got != want {
			wrongLines[i] = true
		}
	}

	for i, line := range sr.ActualBuffer {
		var hl e2e.LineHighlight
		if wrongLines[i] {
			hl.Class = "wrong-line"
		}
		e2e.RenderLine(b, i+1, line, hl, -1)
	}
	if len(sr.ActualBuffer) < len(sr.ExpectedBuffer) {
		for i := len(sr.ActualBuffer); i < len(sr.ExpectedBuffer); i++ {
			e2e.RenderLine(b, i+1, "(missing)", e2e.LineHighlight{Class: "wrong-line"}, -1)
		}
	}
	b.WriteString("</pre></div>\n")
}

func renderStepGroups(b *strings.Builder, stages []e2e.StageInfo) {
	if len(stages) == 0 {
		return
	}
	b.WriteString("<div class=\"step-label\">Groups: ")
	for _, s := range stages {
		for _, g := range s.Groups {
			cls := g.Type
			fmt.Fprintf(b, "<span class=\"group-tag %s\">%s @%d</span> ",
				html.EscapeString(cls), html.EscapeString(g.Type), g.BufferLine)
		}
	}
	b.WriteString("</div>\n")
}

func generateEngineReport(results []scenarioResult, outputPath string) error {
	var b strings.Builder

	b.WriteString(`<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<title>Engine E2E Report</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=JetBrains+Mono:wght@400;700&display=swap" rel="stylesheet">
<style>
`)
	b.WriteString(e2e.BaseCSS)
	b.WriteString("\n</style>\n</head>\n<body>\n")

	var total, passCount, failCount int
	for _, r := range results {
		total++
		if r.Pass {
			passCount++
		} else {
			failCount++
		}
	}
	fmt.Fprintf(&b, `<h1>Engine E2E Report <span class="stats"><span class="meta">%d fixtures</span>`, total)
	fmt.Fprintf(&b, `<span class="pass">%d pass</span>`, passCount)
	if failCount > 0 {
		fmt.Fprintf(&b, `<span class="fail">%d fail</span>`, failCount)
	}
	b.WriteString("</span></h1>\n")

	b.WriteString("<div class=\"filters\">\n")
	fmt.Fprintf(&b, "<button class=\"filter-btn active\" data-filter=\"all\">All (%d)</button>\n", total)
	fmt.Fprintf(&b, "<button class=\"filter-btn\" data-filter=\"passed\">Passed (%d)</button>\n", passCount)
	fmt.Fprintf(&b, "<button class=\"filter-btn\" data-filter=\"failed\">Failed (%d)</button>\n", failCount)
	b.WriteString("</div>\n")

	for _, r := range results {
		status := "passed"
		statusLabel := `<span class="pass">PASS</span>`
		if !r.Pass {
			status = "failed"
			statusLabel = `<span class="fail">FAIL</span>`
		}

		fmt.Fprintf(&b, "<details class=\"fixture\" data-status=\"%s\" open>\n", status)
		fmt.Fprintf(&b, "<summary class=\"hdr\"><h2>%s</h2>%s<span class=\"meta\">%s</span></summary>\n",
			html.EscapeString(r.Name), statusLabel, html.EscapeString(r.Description))

		for i, sr := range r.Steps {
			stepStatus := `<span class="pass">PASS</span>`
			if !sr.Pass {
				stepStatus = `<span class="fail">FAIL</span>`
			}
			fmt.Fprintf(&b, "<div class=\"step-label\">Step %d: %s %s</div>\n",
				i, html.EscapeString(sr.Action), stepStatus)

			if sr.Completion != nil {
				fmt.Fprintf(&b, "<div class=\"step-label\"><span class=\"meta\">startLine=%d endLineInc=%d lines=%d → extractedEnd=%d</span></div>\n",
					sr.OldStart, sr.OldEndOrig, len(sr.NewLines), sr.OldEndActual)

				b.WriteString("<div class=\"cols-3\">\n")
				renderBufferPane(&b, &sr)
				renderDiffPane(&b, &sr)
				renderAfterAcceptPane(&b, &sr)
				b.WriteString("</div>\n")
			}

			renderStepGroups(&b, sr.Stages)

			if len(sr.Failures) > 0 {
				b.WriteString("<div class=\"step-label\"><span class=\"fail\">Failures:</span> ")
				for _, f := range sr.Failures {
					fmt.Fprintf(&b, "<span class=\"meta\">%s</span> ", html.EscapeString(f))
				}
				b.WriteString("</div>\n")
			}

			// JSON details for the scenario step
			if sr.Completion != nil {
				e2e.RenderJSONSection(&b, map[string]any{
					"completion": sr.Completion,
					"oldLines":   sr.OldLines,
					"stages":     sr.Stages,
				}, !sr.Pass)
			}
		}

		b.WriteString("</details>\n")
	}

	b.WriteString(e2e.FilterJS)
	b.WriteString("\n</body></html>")
	return os.WriteFile(outputPath, []byte(b.String()), 0644)
}

// --- Test entry point ---

func TestEngineE2EReport(t *testing.T) {
	e2eDir := filepath.Join("testdata")
	entries, err := os.ReadDir(e2eDir)
	if err != nil {
		t.Fatalf("failed to read e2e directory: %v", err)
	}

	var results []scenarioResult
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".txtar") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".txtar")

		data, err := os.ReadFile(filepath.Join(e2eDir, entry.Name()))
		if err != nil {
			t.Logf("skip %s: %v", name, err)
			continue
		}

		ar := txtar.Parse(data)
		scenario, err := parseTxtarScenario(ar)
		if err != nil {
			t.Logf("skip %s: %v", name, err)
			continue
		}

		r := runEngineScenarioForReport(scenario)
		r.Name = name
		results = append(results, r)
	}

	reportPath := filepath.Join(e2eDir, "report.html")
	if err := generateEngineReport(results, reportPath); err != nil {
		t.Fatalf("failed to generate report: %v", err)
	}
	t.Logf("report: %s", reportPath)
}
