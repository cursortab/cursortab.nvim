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
	Lines      []string
}

type stageInfo struct {
	StartLine  int
	Groups     []groupInfo
	CursorLine int // relative to StartLine, from diff result
	CursorCol  int
}

func parseStages(data []map[string]any) []stageInfo {
	var stages []stageInfo
	for _, s := range data {
		si := stageInfo{
			StartLine:  jsonInt(s["startLine"]),
			CursorLine: jsonInt(s["cursor_line"]),
			CursorCol:  jsonInt(s["cursor_col"]),
		}

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
			var lines []string
			if rawLines, ok := g["lines"]; ok {
				switch v := rawLines.(type) {
				case []any:
					for _, item := range v {
						if s, ok := item.(string); ok {
							lines = append(lines, s)
						}
					}
				case []string:
					lines = v
				}
			}
			si.Groups = append(si.Groups, groupInfo{
				Type:       jsonStr(g["type"]),
				StartLine:  jsonInt(g["start_line"]),
				EndLine:    jsonInt(g["end_line"]),
				BufferLine: jsonInt(g["buffer_line"]),
				RenderHint: jsonStr(g["render_hint"]),
				ColStart:   jsonInt(g["col_start"]),
				ColEnd:     jsonInt(g["col_end"]),
				Lines:      lines,
			})
		}
		stages = append(stages, si)
	}
	return stages
}

// stageFinalCursor returns the absolute (1-indexed row, 0-indexed col) cursor
// position in the new text after applying all stages, using the last stage's
// cursor_line/cursor_col from the diff result.
func stageFinalCursor(stages []stageInfo) (row, col int) {
	if len(stages) == 0 {
		return 0, -1
	}
	last := stages[len(stages)-1]
	return last.StartLine + last.CursorLine - 1, last.CursorCol
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

const reportTabSize = 4

// expandTabsWithMap expands tab characters to spaces and returns a mapping
// from original rune index to expanded rune index, so cursor/col positions
// can be remapped after expansion.
func expandTabsWithMap(text string) (string, []int) {
	runes := []rune(text)
	mapping := make([]int, len(runes)+1)
	var out strings.Builder
	visual := 0
	for i, ch := range runes {
		mapping[i] = visual
		if ch == '\t' {
			spaces := reportTabSize - (visual % reportTabSize)
			out.WriteString(strings.Repeat(" ", spaces))
			visual += spaces
		} else {
			out.WriteRune(ch)
			visual++
		}
	}
	mapping[len(runes)] = visual
	return out.String(), mapping
}

// mapCol maps an original rune index through the tab-expansion mapping.
// Returns -1 unchanged (no cursor).
func mapCol(col int, mapping []int) int {
	if col < 0 {
		return -1
	}
	if col >= len(mapping) {
		return mapping[len(mapping)-1]
	}
	return mapping[col]
}

// injectCursor wraps the character (or appends a space) at col (0-indexed,
// already tab-expanded) in a cursor span. text must already be tab-expanded.
func injectCursor(text string, col int) string {
	runes := []rune(text)
	if col < 0 {
		col = 0
	}
	if col >= len(runes) {
		return html.EscapeString(string(runes)) + "<span class=\"cursor\"> </span>"
	}
	before := html.EscapeString(string(runes[:col]))
	ch := html.EscapeString(string(runes[col : col+1]))
	after := html.EscapeString(string(runes[col+1:]))
	return before + "<span class=\"cursor\">" + ch + "</span>" + after
}

func renderLine(b *strings.Builder, lineNum int, text string, hl lineHighlight, cursorCol int) {
	// Expand tabs so cursor and highlight spans have correct visual widths.
	expanded, mapping := expandTabsWithMap(text)
	mappedCursor := mapCol(cursorCol, mapping)

	escaped := html.EscapeString(expanded)
	if mappedCursor >= 0 {
		escaped = injectCursor(expanded, mappedCursor)
	}

	switch hl.RenderHint {
	case "append_chars":
		runes := []rune(expanded)
		cs := mapCol(hl.ColStart, mapping)
		cs = min(cs, len(runes))
		before := html.EscapeString(string(runes[:cs]))
		ghost := html.EscapeString(string(runes[cs:]))
		if mappedCursor >= 0 && mappedCursor < cs {
			before = injectCursor(string(runes[:cs]), mappedCursor)
		} else if mappedCursor >= cs {
			ghost = injectCursor(string(runes[cs:]), mappedCursor-cs)
		}
		fmt.Fprintf(b, "<span class=\"line\"><span class=\"ln\">%d</span>%s<span class=\"ghost\">%s</span></span>",
			lineNum, before, ghost)
		return
	case "replace_chars":
		runes := []rune(expanded)
		cs := mapCol(hl.ColStart, mapping)
		ce := mapCol(hl.ColEnd, mapping)
		ce = min(ce, len(runes))
		if ce <= cs {
			ce = len(runes)
		}
		before := html.EscapeString(string(runes[:cs]))
		mid := html.EscapeString(string(runes[cs:ce]))
		after := html.EscapeString(string(runes[ce:]))
		if mappedCursor >= 0 && mappedCursor < cs {
			before = injectCursor(string(runes[:cs]), mappedCursor)
		} else if mappedCursor >= cs && mappedCursor < ce {
			mid = injectCursor(string(runes[cs:ce]), mappedCursor-cs)
		} else if mappedCursor >= ce {
			after = injectCursor(string(runes[ce:]), mappedCursor-ce)
		}
		fmt.Fprintf(b, "<span class=\"line\"><span class=\"ln\">%d</span>%s<span class=\"add-hl\">%s</span>%s</span>",
			lineNum, before, mid, after)
		return
	case "delete_chars":
		runes := []rune(expanded)
		cs := mapCol(hl.ColStart, mapping)
		ce := mapCol(hl.ColEnd, mapping)
		ce = min(ce, len(runes))
		if ce <= cs {
			ce = len(runes)
		}
		before := html.EscapeString(string(runes[:cs]))
		mid := html.EscapeString(string(runes[cs:ce]))
		after := html.EscapeString(string(runes[ce:]))
		if mappedCursor >= 0 && mappedCursor < cs {
			before = injectCursor(string(runes[:cs]), mappedCursor)
		} else if mappedCursor >= cs && mappedCursor < ce {
			mid = injectCursor(string(runes[cs:ce]), mappedCursor-cs)
		} else if mappedCursor >= ce {
			after = injectCursor(string(runes[ce:]), mappedCursor-ce)
		}
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
	Text      string
	HL        lineHighlight
	SideText  string // non-empty for side-by-side modification (shown to the right)
	SideHL    lineHighlight
	CursorCol int // 0-indexed column of block cursor; -1 means no cursor on this line
}

// buildPreview renders what the editor buffer looks like with completions overlaid.
// It mirrors the rendering logic in lua/cursortab/ui.lua's show_completion.
// cursorRow is 1-indexed, cursorCol is 0-indexed; pass 0/−1 to omit the cursor.
func buildPreview(oldText, newText string, stages []stageInfo, cursorRow, cursorCol int) []previewLine {
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
					lineIdx := relLine - g.StartLine
					addContent := newContent
					if lineIdx >= 0 && lineIdx < len(g.Lines) {
						addContent = g.Lines[lineIdx]
					}
					pl := previewLine{Text: addContent, HL: lineHighlight{Class: "add"}}
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
	bufToPreview := map[int]int{} // maps buffer line (1-indexed) to preview index (0-indexed)
	for i, line := range oldLines {
		bufLine := i + 1

		// Insert additions that go before this line
		if added, ok := additionsBefore[bufLine]; ok {
			preview = append(preview, added...)
		}

		bufToPreview[bufLine] = len(preview)
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

	// Default all lines to no cursor, then mark the cursor line.
	for i := range preview {
		preview[i].CursorCol = -1
	}
	// Map cursor from buffer coordinates to preview coordinates.
	if idx, ok := bufToPreview[cursorRow]; ok && idx < len(preview) {
		preview[idx].CursorCol = cursorCol
	}

	return preview
}

func renderPreviewLine(b *strings.Builder, lineNum int, pl previewLine) {
	if pl.SideText != "" {
		// Side-by-side modification: old (del) + separator + new (mod)
		renderLine(b, lineNum, pl.Text, pl.HL, pl.CursorCol)
		// Render the side text as an inline companion
		fmt.Fprintf(b, "<span class=\"line mod side\"><span class=\"ln\">→</span>%s</span>",
			html.EscapeString(pl.SideText))
		return
	}
	renderLine(b, lineNum, pl.Text, pl.HL, pl.CursorCol)
}

func renderTextPane(b *strings.Builder, label string, lines []string, cursorRow, cursorCol int) {
	b.WriteString("<div class=\"pane\">\n")
	fmt.Fprintf(b, "<h3>%s</h3><pre>", html.EscapeString(label))
	for i, line := range lines {
		col := -1
		if i+1 == cursorRow {
			col = cursorCol
		}
		renderLine(b, i+1, line, lineHighlight{}, col)
	}
	b.WriteString("</pre></div>\n")
}

func renderPipelineCol(b *strings.Builder, label string, oldText, newText string, stages []stageInfo, oldCursorRow, oldCursorCol int) {
	newCursorRow, newCursorCol := stageFinalCursor(stages)

	b.WriteString("<div class=\"pipeline-col\">\n")
	fmt.Fprintf(b, "<div class=\"pipeline-label\">%s</div>\n", html.EscapeString(label))

	renderTextPane(b, "Old", strings.Split(oldText, "\n"), oldCursorRow, oldCursorCol)

	preview := buildPreview(oldText, newText, stages, oldCursorRow, oldCursorCol)
	b.WriteString("<div class=\"pane\">\n<h3>Preview</h3><pre>")
	for i, pl := range preview {
		renderPreviewLine(b, i+1, pl)
	}
	b.WriteString("</pre></div>\n")

	renderTextPane(b, "New", strings.Split(newText, "\n"), newCursorRow, newCursorCol)

	b.WriteString("</div>\n")
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
.cols-2 { display: grid; grid-template-columns: 1fr 1fr; }
.pipelines { display: grid; grid-template-columns: 1fr 1fr; border-top: 1px solid #30363d; }
.pipeline-col { background: #0d1117; display: flex; flex-direction: column; }
.pipeline-col + .pipeline-col { border-left: 1px solid #30363d; }
.pipeline-label { background: #161b22; padding: 4px 10px; font-size: 12px; font-weight: 600; color: #e6edf3; }
.pane { padding: 8px; overflow-x: auto; overflow-y: visible; border-top: 1px solid #30363d; }
.pane h3 { font-size: 11px; color: #7d8590; margin: 0 0 4px; }
pre { font-family: 'JetBrains Mono', monospace; font-size: 13px; margin: 0; }
.line { display: block; line-height: 1.4; padding: 1px 4px; }
.ln { display: inline-block; width: 24px; text-align: right; color: #545d68; margin-right: 8px; user-select: none; }
.del { background: #67060c; color: #ffa198; }
.add { background: #0f5323; color: #7ee787; }
.mod { background: #1a2332; color: #a5d6ff; }
.ghost { color: #6e7681; }
.cursor { background: #c9d1d9; color: #0d1117; }
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
.json-details .shiki { background: #010409 !important; margin: 0; padding: 8px; overflow-x: auto; overflow-y: hidden; font-size: 12px; }
.json-details .shiki code { line-height: 1.3; overflow-y: hidden; }
.json-details .shiki code .line { display: inline; }
</style>
</head>
<body>
`)

	// Compute stats
	var totalFixtures, allPass, statusFailed, statusUnverified int
	for _, f := range fixtures {
		totalFixtures++
		if !f.BatchPass || !f.IncrementalPass {
			statusFailed++
		} else if !f.Verified {
			statusUnverified++
		} else {
			allPass++
		}
	}
	fmt.Fprintf(&b, `<h1>E2E Pipeline Report <span class="stats"><span class="meta">%d fixtures</span>`, totalFixtures)
	fmt.Fprintf(&b, `<span class="pass">%d pass</span>`, allPass)
	if statusFailed > 0 {
		fmt.Fprintf(&b, `<span class="fail">%d fail</span>`, statusFailed)
	}
	if statusUnverified > 0 {
		fmt.Fprintf(&b, `<span class="unverified">%d unverified</span>`, statusUnverified)
	}
	b.WriteString("</span></h1>\n")

	b.WriteString("<div class=\"filters\">\n")
	fmt.Fprintf(&b, "<button class=\"filter-btn active\" data-filter=\"all\">All (%d)</button>\n", totalFixtures)
	fmt.Fprintf(&b, "<button class=\"filter-btn\" data-filter=\"passed\">Passed (%d)</button>\n", allPass)
	fmt.Fprintf(&b, "<button class=\"filter-btn\" data-filter=\"failed\">Failed (%d)</button>\n", statusFailed)
	fmt.Fprintf(&b, "<button class=\"filter-btn\" data-filter=\"unverified\">Unverified (%d)</button>\n", statusUnverified)
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

		allPass := f.BatchPass && f.IncrementalPass && f.Verified
		escapedName := html.EscapeString(f.Name)
		status := "passed"
		if !f.BatchPass || !f.IncrementalPass {
			status = "failed"
		} else if !f.Verified {
			status = "unverified"
		}
		fmt.Fprintf(&b, "<details class=\"fixture\" data-status=\"%s\" open>\n<summary class=\"hdr\"><h2>%s</h2><button class=\"copy-btn\" data-name=\"%s\" onclick=\"navigator.clipboard.writeText(this.dataset.name)\">copy</button> %s %s %s <span class=\"meta\">cursor=(%d,%d) vp=[%d,%d]</span></summary>\n",
			status, escapedName, escapedName, vStatus, bStatus, iStatus,
			f.Params.CursorRow, f.Params.CursorCol,
			f.Params.ViewportTop, f.Params.ViewportBottom)

		b.WriteString("<div class=\"pipelines\">\n")
		renderPipelineCol(&b, "Batch", f.OldText, f.NewText, batchStages, f.Params.CursorRow, f.Params.CursorCol)
		renderPipelineCol(&b, "Incremental", f.OldText, f.NewText, incStages, f.Params.CursorRow, f.Params.CursorCol)
		b.WriteString("</div>\n")

		renderJSONSection(&b, f.BatchActual, f.IncrementalActual, !allPass)

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
