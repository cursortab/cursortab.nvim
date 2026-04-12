package e2e

import (
	"encoding/json"
	"fmt"
	"html"
	"strings"
)

// --- Types ---

type GroupInfo struct {
	Type       string
	StartLine  int
	EndLine    int
	BufferLine int
	RenderHint string
	ColStart   int
	ColEnd     int
	Lines      []string
}

type StageInfo struct {
	StartLine  int
	Groups     []GroupInfo
	CursorLine int // relative to StartLine, from diff result
	CursorCol  int
}

// LineHighlight describes how to render a single line in the preview.
// CSS classes map to config.lua highlight groups:
//
//	"del"   → CursorTabDeletion     bg #4f2f2f
//	"add"   → CursorTabAddition     bg #394f2f
//	"mod"   → CursorTabModification bg #282e38
//	"ghost" → CursorTabCompletion   fg #80899c (for append_chars ghost text)
type LineHighlight struct {
	Class      string
	RenderHint string
	ColStart   int
	ColEnd     int
}

// PreviewLine is a single line in the editor preview.
type PreviewLine struct {
	Text      string
	HL        LineHighlight
	SideText  string // non-empty for side-by-side modification (shown to the right)
	SideHL    LineHighlight
	CursorCol int // 0-indexed column of block cursor; -1 means no cursor on this line
}

const TabSize = 4

// --- Parsing ---

func JSONInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

func JSONStr(v any) string {
	s, _ := v.(string)
	return s
}

func ParseStages(data []map[string]any) []StageInfo {
	var stages []StageInfo
	for _, s := range data {
		si := StageInfo{
			StartLine:  JSONInt(s["startLine"]),
			CursorLine: JSONInt(s["cursor_line"]),
			CursorCol:  JSONInt(s["cursor_col"]),
		}

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
			si.Groups = append(si.Groups, GroupInfo{
				Type:       JSONStr(g["type"]),
				StartLine:  JSONInt(g["start_line"]),
				EndLine:    JSONInt(g["end_line"]),
				BufferLine: JSONInt(g["buffer_line"]),
				RenderHint: JSONStr(g["render_hint"]),
				ColStart:   JSONInt(g["col_start"]),
				ColEnd:     JSONInt(g["col_end"]),
				Lines:      lines,
			})
		}
		stages = append(stages, si)
	}
	return stages
}

// StageFinalCursor returns the absolute (1-indexed row, 0-indexed col) cursor
// position in the new text after applying all stages. If cursor_line and
// cursor_col are both -1, the old position is preserved.
func StageFinalCursor(stages []StageInfo, oldRow, oldCol int) (row, col int) {
	if len(stages) == 0 {
		return oldRow, oldCol
	}
	last := stages[len(stages)-1]
	if last.CursorLine == -1 && last.CursorCol == -1 {
		return oldRow, oldCol
	}
	return last.StartLine + last.CursorLine - 1, last.CursorCol
}

// --- Tab expansion ---

func expandTabsWithMap(text string) (string, []int) {
	runes := []rune(text)
	mapping := make([]int, len(runes)+1)
	var out strings.Builder
	visual := 0
	for i, ch := range runes {
		mapping[i] = visual
		if ch == '\t' {
			spaces := TabSize - (visual % TabSize)
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

func mapCol(col int, mapping []int) int {
	if col < 0 {
		return -1
	}
	if col >= len(mapping) {
		return mapping[len(mapping)-1]
	}
	return mapping[col]
}

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

// --- Line rendering ---

func RenderLine(b *strings.Builder, lineNum int, text string, hl LineHighlight, cursorCol int) {
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

func RenderPreviewLine(b *strings.Builder, lineNum int, pl PreviewLine) {
	if pl.SideText != "" {
		RenderLine(b, lineNum, pl.Text, pl.HL, pl.CursorCol)
		fmt.Fprintf(b, "<span class=\"line mod side\"><span class=\"ln\">→</span>%s</span>",
			html.EscapeString(pl.SideText))
		return
	}
	RenderLine(b, lineNum, pl.Text, pl.HL, pl.CursorCol)
}

func RenderTextPane(b *strings.Builder, label string, lines []string, cursorRow, cursorCol int, extraClass ...string) {
	cls := "pane"
	if len(extraClass) > 0 && extraClass[0] != "" {
		cls += " " + extraClass[0]
	}
	fmt.Fprintf(b, "<div class=\"%s\">\n", cls)
	fmt.Fprintf(b, "<h3>%s</h3><pre>", html.EscapeString(label))
	for i, line := range lines {
		if i == len(lines)-1 && line == "" {
			break
		}
		col := -1
		if i+1 == cursorRow {
			col = cursorCol
		}
		RenderLine(b, i+1, line, LineHighlight{}, col)
	}
	b.WriteString("</pre></div>\n")
}

// --- Preview building ---

// BuildPreview renders what the editor buffer looks like with completions overlaid.
// cursorRow is 1-indexed, cursorCol is 0-indexed; pass 0/−1 to omit the cursor.
func BuildPreview(oldText, newText string, stages []StageInfo, cursorRow, cursorCol int) []PreviewLine {
	oldLines := strings.Split(oldText, "\n")
	newLines := strings.Split(newText, "\n")

	type lineAction struct {
		kind       string
		newContent string
		hl         LineHighlight
		sideText   string
		sideHL     LineHighlight
	}
	actions := map[int]lineAction{}
	additionsBefore := map[int][]PreviewLine{}
	modGroupsAfter := map[int][]PreviewLine{}
	var additionsAfterEnd []PreviewLine

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
					pl := PreviewLine{Text: addContent, HL: LineHighlight{Class: "add"}}
					if g.BufferLine > len(oldLines) {
						additionsAfterEnd = append(additionsAfterEnd, pl)
					} else {
						additionsBefore[g.BufferLine] = append(additionsBefore[g.BufferLine], pl)
					}
				case "modification":
					oldBufLine := g.BufferLine + relLine - g.StartLine
					lineIdx := relLine - g.StartLine
					lineContent := newContent
					if lineIdx >= 0 && lineIdx < len(g.Lines) {
						lineContent = g.Lines[lineIdx]
					}
					if isSingle && g.RenderHint == "append_chars" {
						actions[oldBufLine] = lineAction{
							kind:       "append_chars",
							newContent: lineContent,
							hl:         LineHighlight{RenderHint: "append_chars", ColStart: g.ColStart},
						}
					} else if isSingle && g.RenderHint == "replace_chars" {
						actions[oldBufLine] = lineAction{
							kind:       "replace_chars",
							newContent: lineContent,
							hl:         LineHighlight{RenderHint: "replace_chars", ColStart: g.ColStart, ColEnd: g.ColEnd},
						}
					} else if isSingle && g.RenderHint == "delete_chars" {
						actions[oldBufLine] = lineAction{
							kind: "delete_chars",
							hl:   LineHighlight{RenderHint: "delete_chars", ColStart: g.ColStart, ColEnd: g.ColEnd},
						}
					} else if isSingle {
						actions[oldBufLine] = lineAction{
							kind:     "mod",
							sideText: lineContent,
							sideHL:   LineHighlight{Class: "mod"},
						}
					} else {
						actions[oldBufLine] = lineAction{kind: "del"}
						if relLine == g.EndLine {
							for _, newLine := range g.Lines {
								modGroupsAfter[oldBufLine] = append(modGroupsAfter[oldBufLine],
									PreviewLine{Text: newLine, HL: LineHighlight{Class: "mod"}})
							}
						}
					}
				case "deletion":
					delBufLine := g.BufferLine + relLine - g.StartLine
					actions[delBufLine] = lineAction{kind: "del"}
				}
			}
		}
	}

	var preview []PreviewLine
	bufToPreview := map[int]int{}
	for i, line := range oldLines {
		bufLine := i + 1

		if added, ok := additionsBefore[bufLine]; ok {
			preview = append(preview, added...)
		}

		bufToPreview[bufLine] = len(preview)
		if action, ok := actions[bufLine]; ok {
			switch action.kind {
			case "append_chars":
				preview = append(preview, PreviewLine{Text: action.newContent, HL: action.hl})
			case "replace_chars":
				preview = append(preview, PreviewLine{Text: action.newContent, HL: action.hl})
			case "delete_chars":
				preview = append(preview, PreviewLine{Text: line, HL: action.hl})
			case "del":
				preview = append(preview, PreviewLine{Text: line, HL: LineHighlight{Class: "del"}})
			case "mod":
				preview = append(preview, PreviewLine{
					Text: line, HL: LineHighlight{Class: "del"},
					SideText: action.sideText, SideHL: action.sideHL,
				})
			}
		} else {
			preview = append(preview, PreviewLine{Text: line, HL: LineHighlight{}})
		}

		if modLines, ok := modGroupsAfter[bufLine]; ok {
			preview = append(preview, modLines...)
		}
	}

	preview = append(preview, additionsAfterEnd...)

	for i := range preview {
		preview[i].CursorCol = -1
	}
	if idx, ok := bufToPreview[cursorRow]; ok && idx < len(preview) {
		preview[idx].CursorCol = cursorCol
	}

	return preview
}

// --- Composite rendering ---

func RenderPipelineCol(b *strings.Builder, label string, oldText, newText string, stages []StageInfo, oldCursorRow, oldCursorCol int, expectedStages []StageInfo) {
	newCursorRow, newCursorCol := StageFinalCursor(stages, oldCursorRow, oldCursorCol)

	b.WriteString("<div class=\"pipeline-col\">\n")
	fmt.Fprintf(b, "<div class=\"pipeline-label\">%s</div>\n", html.EscapeString(label))

	RenderTextPane(b, "Old", strings.Split(oldText, "\n"), oldCursorRow, oldCursorCol)

	preview := BuildPreview(oldText, newText, stages, oldCursorRow, oldCursorCol)
	b.WriteString("<div class=\"pane\">\n<h3>Preview</h3><pre>")
	for i, pl := range preview {
		RenderPreviewLine(b, i+1, pl)
	}
	b.WriteString("</pre></div>\n")

	if expectedStages != nil {
		expectedPreview := BuildPreview(oldText, newText, expectedStages, oldCursorRow, oldCursorCol)
		b.WriteString("<div class=\"pane pane-expected\">\n<h3>Expected Preview</h3><pre>")
		for i, pl := range expectedPreview {
			RenderPreviewLine(b, i+1, pl)
		}
		b.WriteString("</pre></div>\n")
	}

	RenderTextPane(b, "New", strings.Split(newText, "\n"), newCursorRow, newCursorCol)

	b.WriteString("</div>\n")
}

func RenderJSONSection(b *strings.Builder, data any, open bool) {
	jsonBytes, _ := json.MarshalIndent(data, "", "  ")

	openAttr := ""
	if open {
		openAttr = " open"
	}
	fmt.Fprintf(b, "<div class=\"json-section\"><details class=\"json-details\"%s><summary>JSON</summary>\n", openAttr)
	fmt.Fprintf(b, "<code class=\"shiki-json\">%s</code>\n", html.EscapeString(string(jsonBytes)))
	b.WriteString("</details></div>\n")
}

// BaseCSS returns the shared CSS styles for e2e reports.
const BaseCSS = `body { font-family: sans-serif; background: #010409; color: #e6edf3; margin: 20px; }
h1 { font-size: 16px; margin-bottom: 16px; display: flex; align-items: baseline; color: #f0f6fc; }
.stats { font-size: 13px; font-weight: 400; margin-left: auto; display: flex; gap: 8px; }
.fixture { border: 1px solid #30363d; margin-bottom: 24px; overflow: hidden; background: #0d1117; }
.hdr { background: #161b22; padding: 12px 12px; display: flex; flex-wrap: wrap; gap: 6px 10px; align-items: center; font-size: 13px; cursor: pointer; user-select: none; }
.hdr-statuses { display: flex; flex-wrap: wrap; gap: 4px 8px; width: 100%; margin: 0; }
.hdr h2 { font-size: 13px; font-weight: 600; color: #f0f6fc; margin: 0; }
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
.cols-3 { display: grid; grid-template-columns: 1fr 1fr 1fr; }
.pipelines { display: grid; grid-template-columns: 1fr 1fr; border-top: 1px solid #30363d; }
.pipeline-col { background: #0d1117; display: flex; flex-direction: column; }
.pipeline-col + .pipeline-col { border-left: 1px solid #30363d; }
.pipeline-label { background: #161b22; padding: 4px 10px; font-size: 12px; font-weight: 600; color: #e6edf3; }
.pane { padding: 8px; overflow-x: auto; overflow-y: visible; border-top: 1px solid #30363d; }
.pane + .pane { border-left: 1px solid #30363d; }
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
.pane-expected { border-top: 1px dashed #30363d; }
.pane-expected h3 { color: #d29922; }
.apply-section { border-top: 1px solid #30363d; background: #0d1117; }
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
.wrong-line { background: #5c1818; color: #ffa198; }
.step-label { background: #161b22; padding: 6px 10px; font-size: 12px; font-weight: 600; color: #e6edf3; border-top: 1px solid #30363d; display: flex; gap: 8px; align-items: center; }
.group-tag { display: inline-block; padding: 1px 6px; font-size: 11px; border-radius: 3px; }
.group-tag.addition { background: #0f5323; color: #7ee787; }
.group-tag.modification { background: #1a2332; color: #a5d6ff; }
.group-tag.deletion { background: #67060c; color: #ffa198; }`

// ReportHeader writes the common HTML header (DOCTYPE through opening body tag)
// with the shared CSS and an optional title.
func ReportHeader(b *strings.Builder, title string) {
	b.WriteString(`<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
`)
	fmt.Fprintf(b, "<title>%s</title>\n", html.EscapeString(title))
	b.WriteString(`<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=JetBrains+Mono:wght@400;700&display=swap" rel="stylesheet">
<style>
`)
	b.WriteString(BaseCSS)
	b.WriteString("\n</style>\n</head>\n<body>\n")
}

// ReportFooter writes the closing body/html tags with filter JS.
func ReportFooter(b *strings.Builder) {
	b.WriteString(filterJS)
	b.WriteString("\n</body></html>")
}

// ReportStats writes the H1 title with pass/fail counts and filter buttons.
func ReportStats(b *strings.Builder, title string, total, pass, fail int, extraCounts ...struct {
	Label, Class string
	N            int
}) {
	fmt.Fprintf(b, `<h1>%s <span class="stats"><span class="meta">%d fixtures</span>`, html.EscapeString(title), total)
	fmt.Fprintf(b, `<span class="pass">%d pass</span>`, pass)
	if fail > 0 {
		fmt.Fprintf(b, `<span class="fail">%d fail</span>`, fail)
	}
	for _, ec := range extraCounts {
		if ec.N > 0 {
			fmt.Fprintf(b, `<span class="%s">%d %s</span>`, ec.Class, ec.N, html.EscapeString(ec.Label))
		}
	}
	b.WriteString("</span></h1>\n")

	b.WriteString("<div class=\"filters\">\n")
	fmt.Fprintf(b, "<button class=\"filter-btn active\" data-filter=\"all\">All (%d)</button>\n", total)
	fmt.Fprintf(b, "<button class=\"filter-btn\" data-filter=\"passed\">Passed (%d)</button>\n", pass)
	fmt.Fprintf(b, "<button class=\"filter-btn\" data-filter=\"failed\">Failed (%d)</button>\n", fail)
	for _, ec := range extraCounts {
		fmt.Fprintf(b, "<button class=\"filter-btn\" data-filter=\"%s\">%s (%d)</button>\n",
			html.EscapeString(strings.ToLower(ec.Label)), html.EscapeString(ec.Label), ec.N)
	}
	b.WriteString("</div>\n")
}

// FilterJS returns shared filter button JS for e2e reports.
const filterJS = `<script>
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
</script>`
