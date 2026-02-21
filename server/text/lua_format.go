package text

// ToLuaFormat converts a Stage to the map format consumed by the Lua plugin.
// This is the single source of truth for the Lua rendering contract.
func ToLuaFormat(stage *Stage, startLine int) map[string]any {
	cursorLine, cursorCol := CalculateCursorPosition(stage.Changes, stage.Lines)

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
