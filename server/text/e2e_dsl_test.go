// DSL format for text E2E expected output.
//
// The "expected" section in txtar fixtures uses this format instead of JSON.
// "none" represents null (no stages). Otherwise:
//
//	stage @<startLine> cursor=<cursorLine>:<cursorCol>
//	  <type> @<bufferLine> <startLine>-<endLine> [<renderHint> <colStart>:<colEnd>]
//	    + "<new line>"
//	    - "<old line>"
//
// Multiple stages are separated by blank lines.
//
// Group types: addition, modification, deletion.
// Render hints: append_chars, replace_chars, delete_chars.
// + lines map to the "lines" field, - lines map to "old_lines".
// Lines are quoted with " and only " and \ are escaped.
//
// Example:
//
//	stage @2 cursor=3:30
//	  modification @2 1-1 replace_chars 17:20
//	    + "    console.log(\"new message\");"
//	    - "    console.log(\"old message\");"
//	  addition @4 3-3
//	    + "    console.log(\"added line\");"
package text

import (
	"cursortab/e2e"
	"encoding/json"
	"fmt"
	"strings"
)

// formatExpected converts the Lua-format stages to the text DSL.
// Returns "none" for nil/empty input.
func formatExpected(stages []map[string]any) string {
	if len(stages) == 0 {
		return "none"
	}

	var b strings.Builder
	for i, stage := range stages {
		if i > 0 {
			b.WriteByte('\n')
		}
		startLine := toInt(stage["startLine"])
		cursorLine := toInt(stage["cursor_line"])
		cursorCol := toInt(stage["cursor_col"])
		fmt.Fprintf(&b, "stage @%d cursor=%d:%d\n", startLine, cursorLine, cursorCol)

		groups, _ := stage["groups"].([]any)
		for _, gAny := range groups {
			g, _ := gAny.(map[string]any)
			if g == nil {
				continue
			}
			typ, _ := g["type"].(string)
			bufLine := toInt(g["buffer_line"])
			startL := toInt(g["start_line"])
			endL := toInt(g["end_line"])

			hint, _ := g["render_hint"].(string)
			if hint != "" {
				colStart := toInt(g["col_start"])
				colEnd := toInt(g["col_end"])
				fmt.Fprintf(&b, "  %s @%d %d-%d %s %d:%d\n", typ, bufLine, startL, endL, hint, colStart, colEnd)
			} else {
				fmt.Fprintf(&b, "  %s @%d %d-%d\n", typ, bufLine, startL, endL)
			}

			lines := toStringSlice(g["lines"])
			for _, l := range lines {
				fmt.Fprintf(&b, "    + %s\n", e2e.QuoteLine(l))
			}

			oldLines := toStringSlice(g["old_lines"])
			for _, l := range oldLines {
				fmt.Fprintf(&b, "    - %s\n", e2e.QuoteLine(l))
			}
		}
	}
	return b.String()
}

// parseExpected parses the text DSL back into Lua-format stages.
// Returns nil for "none" input.
func parseExpected(s string) ([]map[string]any, error) {
	s = strings.TrimSpace(s)
	if s == "none" || s == "" {
		return nil, nil
	}

	var stages []map[string]any
	var currentStage map[string]any
	var currentGroups []any
	var currentGroup map[string]any

	flushGroup := func() {
		if currentGroup != nil {
			currentGroups = append(currentGroups, currentGroup)
			currentGroup = nil
		}
	}
	flushStage := func() {
		flushGroup()
		if currentStage != nil {
			currentStage["groups"] = currentGroups
			stages = append(stages, currentStage)
			currentStage = nil
			currentGroups = nil
		}
	}

	for lineNum, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		if strings.HasPrefix(trimmed, "stage ") {
			flushStage()
			var startLine, cursorLine, cursorCol int
			n, err := fmt.Sscanf(trimmed, "stage @%d cursor=%d:%d", &startLine, &cursorLine, &cursorCol)
			if err != nil || n != 3 {
				return nil, fmt.Errorf("line %d: invalid stage header: %s", lineNum+1, trimmed)
			}
			currentStage = map[string]any{
				"startLine":   startLine,
				"cursor_line": cursorLine,
				"cursor_col":  cursorCol,
			}
			currentGroups = nil
			continue
		}

		if currentStage == nil {
			return nil, fmt.Errorf("line %d: content before stage header: %s", lineNum+1, trimmed)
		}

		// Group header: "  <type> @<buf> <start>-<end> [hint col_s:col_e]"
		if !strings.HasPrefix(trimmed, "+ ") && !strings.HasPrefix(trimmed, "- ") {
			flushGroup()
			currentGroup = parseGroupHeader(trimmed)
			if currentGroup == nil {
				return nil, fmt.Errorf("line %d: invalid group header: %s", lineNum+1, trimmed)
			}
			continue
		}

		if currentGroup == nil {
			return nil, fmt.Errorf("line %d: line content before group header: %s", lineNum+1, trimmed)
		}

		// Content line: + "..." or - "..."
		prefix := trimmed[:2]
		quoted := trimmed[2:]
		val, err := e2e.UnquoteLine(quoted)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNum+1, err)
		}

		if prefix == "+ " {
			existing := toStringSlice(currentGroup["lines"])
			currentGroup["lines"] = append(existing, val)
		} else {
			existing := toStringSlice(currentGroup["old_lines"])
			currentGroup["old_lines"] = append(existing, val)
		}
	}

	flushStage()
	return stages, nil
}

func parseGroupHeader(s string) map[string]any {
	// Try with hint: "<type> @<buf> <start>-<end> <hint> <col_s>:<col_e>"
	parts := strings.Fields(s)
	if len(parts) < 3 {
		return nil
	}

	typ := parts[0]
	if typ != "addition" && typ != "modification" && typ != "deletion" {
		return nil
	}

	var bufLine int
	if !strings.HasPrefix(parts[1], "@") {
		return nil
	}
	fmt.Sscanf(parts[1], "@%d", &bufLine)

	var startL, endL int
	n, _ := fmt.Sscanf(parts[2], "%d-%d", &startL, &endL)
	if n != 2 {
		return nil
	}

	g := map[string]any{
		"type":        typ,
		"buffer_line": bufLine,
		"start_line":  startL,
		"end_line":    endL,
		"lines":       []string(nil),
		"old_lines":   nil,
	}

	if len(parts) >= 5 {
		hint := parts[3]
		var colStart, colEnd int
		fmt.Sscanf(parts[4], "%d:%d", &colStart, &colEnd)
		g["render_hint"] = hint
		g["col_start"] = colStart
		g["col_end"] = colEnd
	}

	return g
}

func toInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	default:
		return 0
	}
}

func toStringSlice(v any) []string {
	if v == nil {
		return nil
	}
	switch s := v.(type) {
	case []string:
		return s
	case []any:
		result := make([]string, len(s))
		for i, item := range s {
			result[i], _ = item.(string)
		}
		return result
	default:
		return nil
	}
}
