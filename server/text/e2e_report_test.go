package text

import (
	"encoding/json"
	"fmt"
	"html"
	"os"
	"strings"
)

type groupInfo struct {
	Type       string
	StartLine  int
	EndLine    int
	BufferLine int
	RenderHint string
	ColStart   int
	ColEnd     int
}

type stageInfo struct {
	StartLine int
	Groups    []groupInfo
}

func parseStages(data []map[string]any) []stageInfo {
	var stages []stageInfo
	for _, s := range data {
		si := stageInfo{StartLine: jsonInt(s["startLine"])}

		// Handle both []any (from JSON) and []map[string]any (from Go directly)
		var groupMaps []map[string]any
		switch g := s["groups"].(type) {
		case []any:
			for _, item := range g {
				if m, ok := item.(map[string]any); ok {
					groupMaps = append(groupMaps, m)
				}
			}
		case []map[string]any:
			groupMaps = g
		}

		for _, g := range groupMaps {
			si.Groups = append(si.Groups, groupInfo{
				Type:       jsonStr(g["type"]),
				StartLine:  jsonInt(g["start_line"]),
				EndLine:    jsonInt(g["end_line"]),
				BufferLine: jsonInt(g["buffer_line"]),
				RenderHint: jsonStr(g["render_hint"]),
				ColStart:   jsonInt(g["col_start"]),
				ColEnd:     jsonInt(g["col_end"]),
			})
		}
		stages = append(stages, si)
	}
	return stages
}

// lineHighlight describes how to render a single line in the preview.
// CSS classes map to config.lua highlight groups:
//
//	"del"   → CursorTabDeletion     bg #4f2f2f
//	"add"   → CursorTabAddition     bg #394f2f
//	"mod"   → CursorTabModification bg #282e38
//	"ghost" → CursorTabCompletion   fg #80899c (for append_chars ghost text)
type lineHighlight struct {
	Class      string
	RenderHint string
	ColStart   int
	ColEnd     int
}

func renderLine(b *strings.Builder, lineNum int, text string, hl lineHighlight) {
	escaped := html.EscapeString(text)

	switch hl.RenderHint {
	case "append_chars":
		// Show text before col_start normally, appended part in ghost color
		cs := min(hl.ColStart, len(text))
		before := html.EscapeString(text[:cs])
		ghost := html.EscapeString(text[cs:])
		fmt.Fprintf(b, "<span class=\"line\"><span class=\"ln\">%d</span>%s<span class=\"ghost\">%s</span></span>",
			lineNum, before, ghost)
		return
	case "replace_chars":
		// Full line, with col range highlighted in addition color
		cs := hl.ColStart
		ce := min(hl.ColEnd, len(text))
		if ce <= cs {
			ce = len(text)
		}
		before := html.EscapeString(text[:cs])
		mid := html.EscapeString(text[cs:ce])
		after := html.EscapeString(text[ce:])
		fmt.Fprintf(b, "<span class=\"line\"><span class=\"ln\">%d</span>%s<span class=\"add-hl\">%s</span>%s</span>",
			lineNum, before, mid, after)
		return
	case "delete_chars":
		// Line with col range highlighted in deletion color
		cs := hl.ColStart
		ce := min(hl.ColEnd, len(text))
		if ce <= cs {
			ce = len(text)
		}
		before := html.EscapeString(text[:cs])
		mid := html.EscapeString(text[cs:ce])
		after := html.EscapeString(text[ce:])
		fmt.Fprintf(b, "<span class=\"line\"><span class=\"ln\">%d</span>%s<span class=\"del-hl\">%s</span>%s</span>",
			lineNum, before, mid, after)
		return
	}

	if hl.Class != "" {
		fmt.Fprintf(b, "<span class=\"line %s\"><span class=\"ln\">%d</span>%s</span>", hl.Class, lineNum, escaped)
		return
	}
	fmt.Fprintf(b, "<span class=\"line\"><span class=\"ln\">%d</span>%s</span>", lineNum, escaped)
}

// previewLine is a single line in the editor preview.
type previewLine struct {
	Text     string
	HL       lineHighlight
	SideText string // non-empty for side-by-side modification (shown to the right)
	SideHL   lineHighlight
}

// buildPreview renders what the editor buffer looks like with completions overlaid.
// It mirrors the rendering logic in lua/cursortab/ui.lua's show_completion.
func buildPreview(oldText, newText string, stages []stageInfo) []previewLine {
	oldLines := strings.Split(oldText, "\n")
	newLines := strings.Split(newText, "\n")

	type lineAction struct {
		kind       string // "del", "mod", "append_chars", "replace_chars", "delete_chars"
		newContent string
		hl         lineHighlight
		sideText   string
		sideHL     lineHighlight
	}
	actions := map[int]lineAction{}
	// additions keyed by the buffer line they appear BEFORE (virt_lines_above)
	additionsBefore := map[int][]previewLine{}
	// additions that go after the last buffer line
	var additionsAfterEnd []previewLine

	for _, s := range stages {
		for _, g := range s.Groups {
			isSingle := g.StartLine == g.EndLine
			for relLine := g.StartLine; relLine <= g.EndLine; relLine++ {
				bufLine := s.StartLine + relLine - 1
				newIdx := bufLine - 1
				newContent := ""
				if newIdx >= 0 && newIdx < len(newLines) {
					newContent = newLines[newIdx]
				}

				switch g.Type {
				case "addition":
					pl := previewLine{Text: newContent, HL: lineHighlight{Class: "add"}}
					// ui.lua: virt_lines_above=true at buffer_line, unless past end of buffer
					if g.BufferLine > len(oldLines) {
						additionsAfterEnd = append(additionsAfterEnd, pl)
					} else {
						additionsBefore[g.BufferLine] = append(additionsBefore[g.BufferLine], pl)
					}
				case "modification":
					oldBufLine := g.BufferLine + relLine - g.StartLine
					if isSingle && g.RenderHint == "append_chars" {
						actions[oldBufLine] = lineAction{
							kind:       "append_chars",
							newContent: newContent,
							hl:         lineHighlight{RenderHint: "append_chars", ColStart: g.ColStart},
						}
					} else if isSingle && g.RenderHint == "replace_chars" {
						actions[oldBufLine] = lineAction{
							kind:       "replace_chars",
							newContent: newContent,
							hl:         lineHighlight{RenderHint: "replace_chars", ColStart: g.ColStart, ColEnd: g.ColEnd},
						}
					} else if isSingle && g.RenderHint == "delete_chars" {
						actions[oldBufLine] = lineAction{
							kind: "delete_chars",
							hl:   lineHighlight{RenderHint: "delete_chars", ColStart: g.ColStart, ColEnd: g.ColEnd},
						}
					} else {
						// Side-by-side: old line with del bg, new content with mod bg to the right
						actions[oldBufLine] = lineAction{
							kind:     "mod",
							sideText: newContent,
							sideHL:   lineHighlight{Class: "mod"},
						}
					}
				case "deletion":
					delBufLine := g.BufferLine + relLine - g.StartLine
					actions[delBufLine] = lineAction{kind: "del"}
				}
			}
		}
	}

	var preview []previewLine
	for i, line := range oldLines {
		bufLine := i + 1

		// Insert additions that go before this line
		if added, ok := additionsBefore[bufLine]; ok {
			preview = append(preview, added...)
		}

		if action, ok := actions[bufLine]; ok {
			switch action.kind {
			case "append_chars":
				preview = append(preview, previewLine{Text: action.newContent, HL: action.hl})
			case "replace_chars":
				preview = append(preview, previewLine{Text: action.newContent, HL: action.hl})
			case "delete_chars":
				preview = append(preview, previewLine{Text: line, HL: action.hl})
			case "del":
				preview = append(preview, previewLine{Text: line, HL: lineHighlight{Class: "del"}})
			case "mod":
				preview = append(preview, previewLine{
					Text: line, HL: lineHighlight{Class: "del"},
					SideText: action.sideText, SideHL: action.sideHL,
				})
			}
		} else {
			preview = append(preview, previewLine{Text: line, HL: lineHighlight{}})
		}
	}

	// Additions past the end of the buffer
	preview = append(preview, additionsAfterEnd...)

	return preview
}

func renderPreviewLine(b *strings.Builder, lineNum int, pl previewLine) {
	if pl.SideText != "" {
		// Side-by-side modification: old (del) + separator + new (mod)
		renderLine(b, lineNum, pl.Text, pl.HL)
		// Render the side text as an inline companion
		fmt.Fprintf(b, "<span class=\"line mod side\"><span class=\"ln\">→</span>%s</span>",
			html.EscapeString(pl.SideText))
		return
	}
	renderLine(b, lineNum, pl.Text, pl.HL)
}

func renderPlainLine(b *strings.Builder, lineNum int, text string) {
	fmt.Fprintf(b, "<span class=\"line\"><span class=\"ln\">%d</span>%s</span>",
		lineNum, html.EscapeString(text))
}

func renderOldNewPane(b *strings.Builder, oldText, newText string) {
	oldLines := strings.Split(oldText, "\n")
	newLines := strings.Split(newText, "\n")

	b.WriteString("<div class=\"old-new-row\"><div class=\"cols-2\">\n")

	b.WriteString("<div class=\"col\"><h3>Old</h3><pre>")
	for i, line := range oldLines {
		renderPlainLine(b, i+1, line)
	}
	b.WriteString("</pre></div>\n")

	b.WriteString("<div class=\"col\"><h3>New</h3><pre>")
	for i, line := range newLines {
		renderPlainLine(b, i+1, line)
	}
	b.WriteString("</pre></div>\n")

	b.WriteString("</div></div>\n")
}

func renderPipelineCol(b *strings.Builder, label string, oldText, newText string, stages []stageInfo) {
	b.WriteString("<div class=\"pipeline-col\">\n")
	fmt.Fprintf(b, "<div class=\"pipeline-label\">%s</div>\n", label)

	preview := buildPreview(oldText, newText, stages)
	b.WriteString("<div class=\"preview-pane\"><h3>Preview</h3><pre>")
	for i, pl := range preview {
		renderPreviewLine(b, i+1, pl)
	}
	b.WriteString("</pre></div>\n")

	b.WriteString("</div>\n")
}

func renderJSONSection(b *strings.Builder, batchData, incData []map[string]any) {
	batchJSON, _ := json.MarshalIndent(batchData, "", "  ")
	incJSON, _ := json.MarshalIndent(incData, "", "  ")

	fmt.Fprintf(b, "<div class=\"json-section\"><details class=\"json-details\" open><summary>JSON</summary>\n")
	b.WriteString("<div class=\"cols-2\">\n")
	fmt.Fprintf(b, "<div class=\"json-col\"><code class=\"shiki-json\">%s</code></div>\n", html.EscapeString(string(batchJSON)))
	fmt.Fprintf(b, "<div class=\"json-col\"><code class=\"shiki-json\">%s</code></div>\n", html.EscapeString(string(incJSON)))
	b.WriteString("</div>\n")
	b.WriteString("</details></div>\n")
}

func generateReport(fixtures []fixtureResult, outputPath string) error {
	var b strings.Builder

	// Colors from config.lua highlight groups:
	// CursorTabDeletion:     bg #4f2f2f
	// CursorTabAddition:     bg #394f2f
	// CursorTabModification: bg #282e38
	// CursorTabCompletion:   fg #80899c
	b.WriteString(`<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<title>E2E Report</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=JetBrains+Mono:wght@400;700&display=swap" rel="stylesheet">
<style>
body { font-family: sans-serif; background: #010409; color: #e6edf3; margin: 20px; }
h1 { font-size: 16px; margin-bottom: 16px; display: flex; align-items: baseline; color: #f0f6fc; }
.stats { font-size: 13px; font-weight: 400; margin-left: auto; display: flex; gap: 8px; }
.fixture { border: 1px solid #30363d; margin-bottom: 24px; overflow: hidden; background: #0d1117; }
.hdr { background: #161b22; padding: 8px 12px; display: flex; gap: 10px; align-items: center; font-size: 13px; cursor: pointer; user-select: none; }
.hdr h2 { font-size: 13px; font-weight: 600; color: #f0f6fc; }
.copy-btn { background: none; border: 1px solid #30363d; color: #7d8590; cursor: pointer; padding: 2px 6px; font-size: 11px; line-height: 1; }
.copy-btn:hover { color: #e6edf3; border-color: #545d68; }
.filters { display: flex; gap: 6px; margin-bottom: 16px; }
.filter-btn { background: #161b22; border: 1px solid #30363d; color: #7d8590; cursor: pointer; padding: 4px 10px; font-size: 12px; }
.filter-btn:hover { color: #e6edf3; border-color: #545d68; }
.filter-btn.active { color: #e6edf3; border-color: #e6edf3; }
.pass { color: #3fb950; }
.fail { color: #f85149; }
.unverified { color: #d29922; }
.meta { color: #7d8590; }
.old-new-row { border-top: 1px solid #30363d; }
.cols-2 { display: grid; grid-template-columns: 1fr 1fr; }
.pipelines { display: grid; grid-template-columns: 1fr 1fr; border-top: 1px solid #30363d; }
.pipeline-col { background: #0d1117; }
.pipeline-col + .pipeline-col { border-left: 1px solid #30363d; }
.pipeline-label { background: #161b22; padding: 4px 10px; font-size: 12px; font-weight: 600; color: #e6edf3; }
.preview-pane { padding: 8px; overflow-x: auto; overflow-y: visible; }
.preview-pane h3 { font-size: 11px; color: #7d8590; margin-bottom: 4px; }
.col { padding: 8px; overflow-x: auto; overflow-y: visible; }
.col + .col { border-left: 1px solid #30363d; }
.col h3 { font-size: 11px; color: #7d8590; margin-bottom: 4px; }
pre { font-family: 'JetBrains Mono', monospace; font-size: 13px; margin: 0; }
.line { display: block; line-height: 1.4; padding: 1px 4px; }
.ln { display: inline-block; width: 24px; text-align: right; color: #545d68; margin-right: 8px; user-select: none; }
.del { background: #67060c; color: #ffa198; }
.add { background: #0f5323; color: #7ee787; }
.mod { background: #1a2332; color: #a5d6ff; }
.ghost { color: #6e7681; }
.del-hl { background: #67060c; color: #ffa198; }
.add-hl { background: #0f5323; color: #7ee787; }
.side { font-style: italic; opacity: 0.85; }
.json-section { border-top: 1px solid #30363d; background: #0d1117; }
.json-col { min-width: 0; padding: 8px; overflow-x: auto; }
.json-col + .json-col { border-left: 1px solid #30363d; }
.json-col h3 { font-size: 11px; color: #7d8590; margin-bottom: 4px; }
.json-details { height: 100%; padding: 4px 8px; }
.json-details summary { font-size: 12px; color: #7d8590; margin-bottom: 4px; cursor: pointer; user-select: none; font-weight: 600; }
.json-details code { display: block; white-space: pre; font-family: 'JetBrains Mono', monospace; font-size: 12px; overflow-x: auto; overflow-y: visible; color: #e6edf3; }
.json-details:not([open]) { display: flex; flex-direction: column; }
.json-details:not([open]) code { display: none; }
.json-details .shiki { background: #010409 !important; margin: 0; padding: 8px; overflow-x: auto; font-size: 12px; }
.json-details .shiki code { line-height: 1.3; }
.json-details .shiki code .line { display: inline; }
</style>
</head>
<body>
`)

	// Compute stats
	var totalFixtures, verified, batchPass, incPass, allPass int
	for _, f := range fixtures {
		totalFixtures++
		if f.Verified {
			verified++
		}
		if f.BatchPass {
			batchPass++
		}
		if f.IncrementalPass {
			incPass++
		}
		if f.BatchPass && f.IncrementalPass && f.Verified {
			allPass++
		}
	}
	fmt.Fprintf(&b, `<h1>E2E Pipeline Report <span class="stats"><span class="meta">%d fixtures</span>`, totalFixtures)
	fmt.Fprintf(&b, `<span class="pass">%d pass</span>`, allPass)
	if totalFixtures-allPass > 0 {
		fmt.Fprintf(&b, `<span class="fail">%d fail</span>`, totalFixtures-allPass)
	}
	if totalFixtures-verified > 0 {
		fmt.Fprintf(&b, `<span class="unverified">%d unverified</span>`, totalFixtures-verified)
	}
	b.WriteString("</span></h1>\n")

	b.WriteString("<div class=\"filters\">\n")
	b.WriteString("<button class=\"filter-btn active\" data-filter=\"all\">All</button>\n")
	b.WriteString("<button class=\"filter-btn\" data-filter=\"passed\">Passed</button>\n")
	b.WriteString("<button class=\"filter-btn\" data-filter=\"failed\">Failed</button>\n")
	b.WriteString("<button class=\"filter-btn\" data-filter=\"unverified\">Unverified</button>\n")
	b.WriteString("</div>\n")

	for _, f := range fixtures {
		batchStages := parseStages(f.BatchActual)
		incStages := parseStages(f.IncrementalActual)

		bStatus := `<span class="pass">batch:pass</span>`
		if !f.BatchPass {
			bStatus = `<span class="fail">batch:FAIL</span>`
		}
		iStatus := `<span class="pass">inc:pass</span>`
		if !f.IncrementalPass {
			iStatus = `<span class="fail">inc:FAIL</span>`
		}
		vStatus := `<span class="pass">verified</span>`
		if !f.Verified {
			vStatus = `<span class="unverified">unverified</span>`
		}

		// Collapse verified+passing fixtures
		allPass := f.BatchPass && f.IncrementalPass && f.Verified
		escapedName := html.EscapeString(f.Name)
		open := ""
		if !allPass {
			open = " open"
		}
		status := "passed"
		if !f.BatchPass || !f.IncrementalPass {
			status = "failed"
		} else if !f.Verified {
			status = "unverified"
		}
		fmt.Fprintf(&b, "<details class=\"fixture\" data-status=\"%s\"%s>\n<summary class=\"hdr\"><h2>%s</h2><button class=\"copy-btn\" data-name=\"%s\" onclick=\"navigator.clipboard.writeText('TestE2E/'+this.dataset.name)\">copy</button> %s %s %s <span class=\"meta\">cursor=(%d,%d) vp=[%d,%d]</span></summary>\n",
			status, open, escapedName, escapedName, vStatus, bStatus, iStatus,
			f.Params.CursorRow, f.Params.CursorCol,
			f.Params.ViewportTop, f.Params.ViewportBottom)

		// Old / New (shared, shown once)
		renderOldNewPane(&b, f.OldText, f.NewText)

		// Batch | Incremental side by side
		b.WriteString("<div class=\"pipelines\">\n")
		renderPipelineCol(&b, "Batch", f.OldText, f.NewText, batchStages)
		renderPipelineCol(&b, "Incremental", f.OldText, f.NewText, incStages)
		b.WriteString("</div>\n")

		renderJSONSection(&b, f.BatchActual, f.IncrementalActual)

		b.WriteString("</details>\n")
	}

	b.WriteString(`<script>
function applyFilter(filter) {
  document.querySelectorAll('.filter-btn').forEach(b => b.classList.remove('active'))
  const btn = document.querySelector('.filter-btn[data-filter="' + filter + '"]')
  if (btn) btn.classList.add('active')
  document.querySelectorAll('.fixture').forEach(el => {
    if (filter === 'all') { el.style.display = ''; return }
    el.style.display = el.dataset.status === filter ? '' : 'none'
  })
}
const initialFilter = new URLSearchParams(location.search).get('filter') || 'all'
applyFilter(initialFilter)
document.querySelectorAll('.filter-btn').forEach(btn => {
  btn.addEventListener('click', () => {
    const filter = btn.dataset.filter
    const url = new URL(location)
    if (filter === 'all') { url.searchParams.delete('filter') } else { url.searchParams.set('filter', filter) }
    history.replaceState(null, '', url)
    applyFilter(filter)
  })
})
</script>
<script type="module">
import { codeToHtml } from 'https://esm.sh/shiki@3.0.0'
document.querySelectorAll('.shiki-json').forEach(async (el) => {
  const code = el.textContent
  el.innerHTML = await codeToHtml(code, { lang: 'json', theme: 'github-dark-default' })
})
</script>
`)
	b.WriteString("</body></html>")
	return os.WriteFile(outputPath, []byte(b.String()), 0644)
}

func jsonInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

func jsonStr(v any) string {
	s, _ := v.(string)
	return s
}
