package text

import (
	"cursortab/e2e"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"strings"
)

func allMaxLinesPass(f fixtureResult) bool {
	for _, mlr := range f.MaxLinesResults {
		if !mlr.ApplyPass || !mlr.PartialAcceptPass {
			return false
		}
	}
	return true
}

func renderJSONSection(b *strings.Builder, batchData, incData []map[string]any, open bool) {
	batchJSON, _ := json.MarshalIndent(batchData, "", "  ")
	incJSON, _ := json.MarshalIndent(incData, "", "  ")

	openAttr := ""
	if open {
		openAttr = " open"
	}
	fmt.Fprintf(b, "<div class=\"json-section\"><details class=\"json-details\"%s><summary>JSON</summary>\n", openAttr)
	b.WriteString("<div class=\"cols-2\">\n")
	fmt.Fprintf(b, "<div class=\"json-col\"><code class=\"shiki-json\">%s</code></div>\n", html.EscapeString(string(batchJSON)))
	fmt.Fprintf(b, "<div class=\"json-col\"><code class=\"shiki-json\">%s</code></div>\n", html.EscapeString(string(incJSON)))
	b.WriteString("</div>\n")
	b.WriteString("</details></div>\n")
}

func generateReport(fixtures []fixtureResult, outputPath string) error {
	var b strings.Builder

	var totalFixtures, passCount, failCount, unverifiedCount int
	for _, f := range fixtures {
		totalFixtures++
		testPass := f.BatchPass && f.IncrementalPass && allMaxLinesPass(f)
		if !testPass {
			failCount++
		} else if !f.Verified {
			unverifiedCount++
		} else {
			passCount++
		}
	}

	e2e.ReportHeader(&b, "E2E Report")
	e2e.ReportStats(&b, "E2E Pipeline Report", totalFixtures, passCount, failCount,
		struct {
			Label, Class string
			N            int
		}{Label: "Unverified", Class: "unverified", N: unverifiedCount})

	for _, f := range fixtures {
		batchStages := e2e.ParseStages(f.BatchActual)
		incStages := e2e.ParseStages(f.IncrementalActual)

		bStatus := `<span class="pass">batch:pass</span>`
		if !f.BatchPass {
			bStatus = `<span class="fail">batch:FAIL</span>`
		}
		iStatus := `<span class="pass">inc:pass</span>`
		if !f.IncrementalPass {
			iStatus = `<span class="fail">inc:FAIL</span>`
		}
		var applyStatuses string
		for _, mlr := range f.MaxLinesResults {
			label := "default"
			if mlr.MaxLines > 0 {
				label = fmt.Sprintf("ml%d", mlr.MaxLines)
			}
			if mlr.ApplyPass {
				applyStatuses += fmt.Sprintf(` <span class="pass">apply(%s):pass</span>`, label)
			} else {
				applyStatuses += fmt.Sprintf(` <span class="fail">apply(%s):FAIL</span>`, label)
			}
			if mlr.PartialAcceptPass {
				applyStatuses += fmt.Sprintf(` <span class="pass">partial(%s):pass</span>`, label)
			} else {
				applyStatuses += fmt.Sprintf(` <span class="fail">partial(%s):FAIL</span>`, label)
			}
		}

		mlPass := allMaxLinesPass(f)
		allPass := f.BatchPass && f.IncrementalPass && mlPass
		escapedName := html.EscapeString(f.Name)
		status := "passed"
		if !allPass {
			status = "failed"
		} else if !f.Verified {
			status = "unverified"
		}
		verifiedBadge := `<span class="fail">unverified</span>`
		if f.Verified {
			verifiedBadge = `<span class="pass">verified</span>`
		}
		openAttr := " open"
		if allPass && f.Verified {
			openAttr = ""
		}
		fmt.Fprintf(&b, "<details class=\"fixture\" data-status=\"%s\"%s>\n<summary class=\"hdr\"><h2>%s</h2><button class=\"copy-btn\" data-name=\"%s\" onclick=\"navigator.clipboard.writeText(this.dataset.name)\">copy</button><span class=\"meta\">cursor=(%d,%d) vp=[%d,%d]</span><span class=\"hdr-statuses\">%s %s %s%s</span></summary>\n",
			status, openAttr, escapedName, escapedName,
			f.Params.CursorRow, f.Params.CursorCol,
			f.Params.ViewportTop, f.Params.ViewportBottom,
			verifiedBadge, bStatus, iStatus, applyStatuses)

		var expectedStages []e2e.StageInfo
		if !f.BatchPass || !f.IncrementalPass {
			expectedStages = e2e.ParseStages(f.Expected)
		}
		b.WriteString("<div class=\"pipelines\">\n")
		var batchExpected, incExpected []e2e.StageInfo
		if expectedStages != nil {
			if !f.BatchPass {
				batchExpected = expectedStages
			}
			if !f.IncrementalPass {
				incExpected = expectedStages
			}
		}
		e2e.RenderPipelineCol(&b, "Batch", f.OldText, f.NewText, batchStages, f.Params.CursorRow, f.Params.CursorCol, batchExpected)
		e2e.RenderPipelineCol(&b, "Incremental", f.OldText, f.NewText, incStages, f.Params.CursorRow, f.Params.CursorCol, incExpected)
		b.WriteString("</div>\n")

		for _, mlr := range f.MaxLinesResults {
			label := "default"
			if mlr.MaxLines > 0 {
				label = fmt.Sprintf("maxLines=%d", mlr.MaxLines)
			}
			if !mlr.ApplyPass && len(mlr.ApplyLines) > 0 {
				b.WriteString("<div class=\"apply-section\">\n")
				b.WriteString("<div class=\"cols-2\">\n")
				e2e.RenderTextPane(&b, fmt.Sprintf("Applied %s (got)", label), mlr.ApplyLines, 0, -1)
				e2e.RenderTextPane(&b, "Expected (new.txt)", strings.Split(f.NewText, "\n"), 0, -1, "pane-expected")
				b.WriteString("</div>\n")
				b.WriteString("</div>\n")
			}
			if !mlr.PartialAcceptPass && len(mlr.PartialAcceptLines) > 0 {
				b.WriteString("<div class=\"apply-section\">\n")
				b.WriteString("<div class=\"cols-2\">\n")
				e2e.RenderTextPane(&b, fmt.Sprintf("Partial Accept %s (got)", label), mlr.PartialAcceptLines, 0, -1)
				e2e.RenderTextPane(&b, "Expected (new.txt)", strings.Split(f.NewText, "\n"), 0, -1, "pane-expected")
				b.WriteString("</div>\n")
				b.WriteString("</div>\n")
			}
		}

		renderJSONSection(&b, f.BatchActual, f.IncrementalActual, !allPass)

		b.WriteString("</details>\n")
	}

	e2e.ReportFooter(&b)
	return os.WriteFile(outputPath, []byte(b.String()), 0644)
}
