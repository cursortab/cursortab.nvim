package text

// calculateCursorFromGroups computes cursor position from pre-computed groups.
// Uses the last non-deletion group's end line, positioned at end of that line.
// Returns (-1, -1) if no suitable group is found.
func calculateCursorFromGroups(groups []*Group, lines []string) (int, int) {
	targetLine := -1
	for _, g := range groups {
		if g.Type != "deletion" && g.EndLine > targetLine {
			targetLine = g.EndLine
		}
	}
	if targetLine <= 0 || targetLine > len(lines) {
		return -1, -1
	}
	return targetLine, len(lines[targetLine-1])
}

// ToLuaFormat converts a Stage to the map format consumed by the Lua plugin.
// This is the single source of truth for the Lua rendering contract.
func ToLuaFormat(stage *Stage, startLine int) map[string]any {
	cursorLine, cursorCol := stage.CursorLine, stage.CursorCol
	if cursorLine <= 0 {
		cursorLine, cursorCol = calculateCursorFromGroups(stage.Groups, stage.Lines)
	}

	var luaGroups []map[string]any
	for _, g := range stage.Groups {
		luaGroup := map[string]any{
			"type":        g.Type,
			"start_line":  g.StartLine,
			"end_line":    g.EndLine,
			"buffer_line": g.BufferLine,
			"lines":       g.Lines,
			"old_lines":   g.OldLines,
		}

		if g.RenderHint != "" {
			luaGroup["render_hint"] = g.RenderHint
			luaGroup["col_start"] = g.ColStart
			luaGroup["col_end"] = g.ColEnd
		}

		luaGroups = append(luaGroups, luaGroup)
	}

	return map[string]any{
		"startLine":   startLine,
		"groups":      luaGroups,
		"cursor_line": cursorLine,
		"cursor_col":  cursorCol,
	}
}
