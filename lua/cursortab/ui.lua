-- UI management for completion and cursor prediction visualization

local config = require("cursortab.config")
local daemon = require("cursortab.daemon")

---@class UIModule
local ui = {}

-- Dimmed highlight namespaces for overlay windows
---@type table<string, integer>
local dimmed_ns_cache = {}

vim.api.nvim_create_autocmd("ColorScheme", {
	callback = function()
		dimmed_ns_cache = {}
	end,
})

--- Blend a color toward the background by a factor (0=original, 1=fully bg)
---@param fg integer
---@param bg integer
---@param factor number
---@return string
local function blend_color(fg, bg, factor)
	local fr, fg_g, fb = math.floor(fg / 65536), math.floor(fg / 256) % 256, fg % 256
	local br, bg_g, bb = math.floor(bg / 65536), math.floor(bg / 256) % 256, bg % 256
	local r = math.floor(fr + (br - fr) * factor)
	local g = math.floor(fg_g + (bg_g - fg_g) * factor)
	local b = math.floor(fb + (bb - fb) * factor)
	return string.format("#%02x%02x%02x", r, g, b)
end

--- Build a namespace with all highlight groups dimmed toward Normal bg
---@param factor number blend factor (0=original, 1=fully bg)
---@return integer namespace id
local function get_dimmed_ns(factor)
	local key = tostring(factor)
	if dimmed_ns_cache[key] then
		return dimmed_ns_cache[key]
	end

	local ns = vim.api.nvim_create_namespace("cursortab_dimmed")
	local normal_hl = vim.api.nvim_get_hl(0, { name = "Normal", link = false })
	local bg = normal_hl.bg or 0x1e1e2e

	-- Get all highlight group names and dim fg colors
	local all_hls = vim.api.nvim_get_hl(0, {})
	for name, def in pairs(all_hls) do
		if def.link then
			local resolved = vim.api.nvim_get_hl(0, { name = name, link = false })
			if resolved.fg then
				vim.api.nvim_set_hl(ns, name, {
					fg = blend_color(resolved.fg, bg, factor),
					bg = resolved.bg and blend_color(resolved.bg, bg, factor) or nil,
					bold = resolved.bold,
					italic = resolved.italic,
					underline = resolved.underline,
				})
			end
		elseif def.fg then
			vim.api.nvim_set_hl(ns, name, {
				fg = blend_color(def.fg, bg, factor),
				bg = def.bg and blend_color(def.bg, bg, factor) or nil,
				bold = def.bold,
				italic = def.italic,
				underline = def.underline,
			})
		end
	end

	local normal_bg_str = normal_hl.bg and string.format("#%06x", normal_hl.bg) or "NONE"
	vim.api.nvim_set_hl(ns, "Normal", {
		fg = normal_hl.fg and blend_color(normal_hl.fg, bg, factor) or nil,
		bg = normal_bg_str,
	})
	vim.api.nvim_set_hl(ns, "NormalFloat", {
		fg = normal_hl.fg and blend_color(normal_hl.fg, bg, factor) or nil,
		bg = normal_bg_str,
	})

	dimmed_ns_cache[key] = ns
	return ns
end

-- UI state
---@type boolean
local has_completion = false
---@type boolean
local has_cursor_prediction = false

-- Expected line state for partial typing optimization (append_chars only)
---@type string|nil
local expected_line = nil -- Target line content
---@type integer|nil
local expected_line_num = nil -- Which line (1-indexed)
---@type integer|nil
local original_len = nil -- Original content length before ghost text
---@type integer|nil
local append_chars_extmark_id = nil -- Extmark ID for the append_chars ghost text
---@type integer|nil
local append_chars_buf = nil -- Buffer where the extmark was created

---@class AppendCharsState
---@field text string
---@field line integer 1-indexed buffer line
---@field col_start integer
---@type AppendCharsState|nil
local append_chars_state = nil

-- State for cursor prediction jump text
---@type integer|nil
local jump_text_extmark_id = nil
---@type integer|nil
local jump_text_buf = nil
---@type integer|nil
local absolute_jump_win = nil
---@type integer|nil
local absolute_jump_buf = nil

---@class ExtmarkInfo
---@field buf integer
---@field extmark_id integer

---@class WindowInfo
---@field win_id integer
---@field buf_id integer

-- State for completion diff visualization
---@type ExtmarkInfo[]
local completion_extmarks = {} -- Array of {buf, extmark_id} for cleanup
---@type WindowInfo[]
local completion_windows = {} -- Array of {win_id, buf_id} for overlay window cleanup

---@class Group
---@field type string "modification" | "addition" | "deletion"
---@field start_line integer 1-indexed, relative to diff content
---@field end_line integer 1-indexed, inclusive
---@field buffer_line integer 1-indexed absolute buffer position for rendering
---@field lines string[] New content
---@field old_lines string[] Old content (modifications only)
---@field render_hint string|nil "append_chars" | "replace_chars" | "delete_chars" | nil
---@field col_start integer|nil For character-level hints
---@field col_end integer|nil For character-level hints

---@class DiffResult
---@field groups Group[] Array of groups for rendering
---@field startLine integer Start line of the buffer range (1-indexed, used for apply operation)
---@field cursor_line integer Cursor position (1-indexed, relative to content)
---@field cursor_col integer Cursor column (0-indexed)

-- Helper function to close cursor prediction jump text
local function ensure_close_cursor_prediction()
	-- Clear jump text extmark
	if jump_text_extmark_id and jump_text_buf and vim.api.nvim_buf_is_valid(jump_text_buf) then
		vim.api.nvim_buf_del_extmark(jump_text_buf, daemon.get_namespace_id(), jump_text_extmark_id)
		jump_text_extmark_id = nil
		jump_text_buf = nil
	end

	-- Close absolute positioning window if it exists
	if absolute_jump_win and vim.api.nvim_win_is_valid(absolute_jump_win) then
		vim.api.nvim_win_close(absolute_jump_win, true)
		absolute_jump_win = nil
	end

	-- Clean up absolute jump buffer
	if absolute_jump_buf and vim.api.nvim_buf_is_valid(absolute_jump_buf) then
		vim.api.nvim_buf_delete(absolute_jump_buf, { force = true })
		absolute_jump_buf = nil
	end
end

-- Function to close completion diff highlighting
local function ensure_close_completion()
	-- Clear all completion extmarks
	for _, extmark_info in ipairs(completion_extmarks) do
		if extmark_info.buf and vim.api.nvim_buf_is_valid(extmark_info.buf) then
			pcall(function()
				vim.api.nvim_buf_del_extmark(extmark_info.buf, daemon.get_namespace_id(), extmark_info.extmark_id)
			end)
		end
	end

	-- Close all overlay windows
	for _, window_info in ipairs(completion_windows) do
		if window_info.win_id and vim.api.nvim_win_is_valid(window_info.win_id) then
			pcall(function()
				vim.api.nvim_win_close(window_info.win_id, true)
			end)
		end
		if window_info.buf_id and vim.api.nvim_buf_is_valid(window_info.buf_id) then
			pcall(function()
				vim.api.nvim_buf_delete(window_info.buf_id, { force = true })
			end)
		end
	end

	-- Reset state
	completion_extmarks = {}
	completion_windows = {}
end

-- Trim a string by a given number of display columns from the left
---@param text string
---@param display_cols integer
---@return string trimmed_text, integer bytes_trimmed, integer chars_trimmed
local function trim_left_display_cols(text, display_cols)
	if not text or text == "" or display_cols <= 0 then
		return text, 0, 0
	end

	local total_chars = vim.fn.strchars(text)
	local trimmed_chars = 0
	local accumulated_width = 0

	-- Incrementally consume characters until we've trimmed the requested display width
	while trimmed_chars < total_chars and accumulated_width < display_cols do
		local ch = vim.fn.strcharpart(text, trimmed_chars, 1)
		local ch_width = vim.fn.strdisplaywidth(ch)
		accumulated_width = accumulated_width + ch_width
		trimmed_chars = trimmed_chars + 1
	end

	-- Compute bytes trimmed corresponding to the number of characters trimmed
	local bytes_trimmed = vim.str_byteindex(text, "utf-8", trimmed_chars)
	local trimmed_text = vim.fn.strcharpart(text, trimmed_chars)

	return trimmed_text, bytes_trimmed, trimmed_chars
end

-- Create transparent overlay window with syntax highlighting
---@param parent_win integer
---@param buffer_line integer
---@param col integer
---@param content string|string[]
---@param syntax_ft string|nil
---@param bg_highlight string|nil
---@param min_width integer|nil
---@param cursorline_buffer_line integer|nil 0-indexed buffer line for cursorline matching, nil to skip
---@param ns_id integer|nil namespace id for extmarks
---@param row_offset integer|nil visual row offset from buffer_line (for virtual line placements)
---@return integer, integer, integer # overlay_win, overlay_buf, bytes_trimmed_first_line
local function create_overlay_window(
	parent_win,
	buffer_line,
	col,
	content,
	syntax_ft,
	bg_highlight,
	min_width,
	cursorline_buffer_line,
	ns_id,
	row_offset
)
	-- Create buffer for overlay content
	local overlay_buf = vim.api.nvim_create_buf(false, true)

	-- Set buffer content
	---@type string[]
	local content_lines = type(content) == "table" and content or { content }

	-- Query parent window state once: topline, leftcol, textoff, winrow, cursor, cursorline
	---@type {topline: integer, leftcol: integer, textoff: integer, winrow: integer}
	local wininfo = vim.fn.getwininfo(parent_win)[1]
	local leftcol = wininfo.leftcol or 0
	local first_visible_line = wininfo.topline
	local textoff = wininfo.textoff or 0
	local winrow = wininfo.winrow or 1
	local cursor_line = vim.api.nvim_win_get_cursor(parent_win)[1] - 1
	local cursorline_enabled = vim.api.nvim_get_option_value("cursorline", { win = parent_win })

	-- Compute how many display columns of the overlay content are scrolled off to the left
	local trim_cols = math.max(0, leftcol - col)

	-- If needed, trim the left side of each line by trim_cols display columns
	local bytes_trimmed_first_line = 0
	if trim_cols > 0 then
		for i, line_content in ipairs(content_lines) do
			local trimmed, bytes_trimmed = trim_left_display_cols(line_content or "", trim_cols)
			content_lines[i] = trimmed
			if i == 1 then
				bytes_trimmed_first_line = bytes_trimmed
			end
		end
	end

	vim.api.nvim_buf_set_lines(overlay_buf, 0, -1, false, content_lines)

	-- Set filetype for syntax highlighting if provided
	if syntax_ft and syntax_ft ~= "" then
		vim.api.nvim_set_option_value("filetype", syntax_ft, { buf = overlay_buf })
	end

	-- Make buffer non-modifiable
	vim.api.nvim_set_option_value("modifiable", false, { buf = overlay_buf })

	-- Calculate window dimensions
	local max_width = 0
	for _, line_content in ipairs(content_lines) do
		max_width = math.max(max_width, vim.fn.strdisplaywidth(line_content))
	end

	-- Use minimum width if specified (useful for covering original content)
	if min_width and min_width > max_width then
		-- Account for scrolled-off columns when enforcing a minimum overlay width
		local adjusted_min_width = math.max(0, min_width - trim_cols)
		max_width = math.max(max_width, adjusted_min_width)
	end

	-- Use screenpos so wrap, folds, and virtual lines from other plugins
	-- (e.g. render-markdown.nvim) don't shift the overlay off the target line.
	-- buffer_line is the actual buffer line; row_offset adjusts for virtual lines
	-- that this overlay covers (e.g. stacked modifications or additions).
	row_offset = row_offset or 0
	local sp = vim.fn.screenpos(parent_win, buffer_line + 1, 1)
	local window_relative_line
	if sp and sp.row and sp.row > 0 then
		window_relative_line = sp.row - winrow + row_offset
	else
		window_relative_line = buffer_line - (first_visible_line - 1) + row_offset
	end

	-- Create floating window
	local overlay_win = vim.api.nvim_open_win(overlay_buf, false, {
		relative = "win",
		win = parent_win,
		row = window_relative_line,
		-- Position horizontally relative to the visible text start (leftcol)
		col = textoff + math.max(0, col - leftcol),
		width = max_width,
		height = #content_lines,
		style = "minimal",
		border = "none",
		zindex = 1,
		focusable = false,
		-- Prevent Neovim from auto-adjusting window position when it doesn't fit
		fixed = true,
	})

	local overlay_start_line = buffer_line + row_offset
	local overlay_has_cursor_line = cursor_line >= overlay_start_line and cursor_line < overlay_start_line + #content_lines

	-- Inherit conceal settings from parent window so overlay text renders identically.
	-- On the cursor line, Neovim reveals concealed text (unless the mode is in 'concealcursor'),
	-- but the overlay has no cursor so it always conceals. When the overlay covers the cursor line
	-- and concealed text would be revealed, disable concealment to match.
	local parent_conceallevel = vim.api.nvim_get_option_value("conceallevel", { win = parent_win })
	vim.api.nvim_set_option_value("conceallevel", parent_conceallevel, { win = overlay_win })
	if parent_conceallevel > 0 then
		local parent_concealcursor = vim.api.nvim_get_option_value("concealcursor", { win = parent_win })
		vim.api.nvim_set_option_value("concealcursor", parent_concealcursor, { win = overlay_win })
		if overlay_has_cursor_line then
			local mode = vim.api.nvim_get_mode().mode:sub(1, 1)
			if not parent_concealcursor:find(mode, 1, true) then
				vim.api.nvim_set_option_value("conceallevel", 0, { win = overlay_win })
			end
		end
	end

	-- Resolve namespace id for extmarks
	ns_id = ns_id or daemon.get_namespace_id()

	-- Set background highlighting to match main window
	if bg_highlight and bg_highlight ~= "" then
		local has_content = false
		for _, line in ipairs(content_lines) do
			if line:match("%S") then
				has_content = true
				break
			end
		end
		if
			bg_highlight == "CursorTabAddition"
			and config.get().ui.completions.addition_style == "dimmed"
			and has_content
		then
			vim.api.nvim_win_set_hl_ns(overlay_win, get_dimmed_ns(1 - config.get().ui.completions.fg_opacity))
			if cursorline_buffer_line then
				if cursorline_enabled then
					local cursor_offset = cursor_line - cursorline_buffer_line
					if cursor_offset >= 0 and cursor_offset < #content_lines then
						local line_text = content_lines[cursor_offset + 1] or ""
						vim.api.nvim_buf_set_extmark(overlay_buf, ns_id, cursor_offset, 0, {
							end_col = #line_text,
							hl_group = "CursorLine",
							hl_eol = true,
						})
					end
				end
			end
		else
			vim.api.nvim_set_option_value("winhighlight", "Normal:" .. bg_highlight, { win = overlay_win })
		end
	else
		-- Always ensure no transparency
		vim.api.nvim_set_option_value("winblend", 0, { win = overlay_win })

		-- Use CursorLine highlight if overlay is on cursor line and cursorline is active
		if cursorline_enabled and buffer_line == cursor_line then
			vim.api.nvim_set_option_value("winhighlight", "Normal:CursorLine", { win = overlay_win })
		else
			vim.api.nvim_set_option_value("winhighlight", "Normal:Normal", { win = overlay_win })
		end
	end

	return overlay_win, overlay_buf, bytes_trimmed_first_line
end

-- Helper to clear expected line state
local function clear_expected_line_state()
	expected_line = nil
	expected_line_num = nil
	original_len = nil
	append_chars_extmark_id = nil
	append_chars_buf = nil
	append_chars_state = nil
end

-- Highlight a line with deletion background
---@param nvim_line integer 0-indexed line number
---@param current_buf integer
---@param ns_id integer
local function highlight_deletion(nvim_line, current_buf, ns_id)
	local line_content = vim.api.nvim_buf_get_lines(current_buf, nvim_line, nvim_line + 1, false)[1] or ""

	if line_content ~= "" then
		local extmark_id = vim.api.nvim_buf_set_extmark(current_buf, ns_id, nvim_line, 0, {
			end_col = #line_content,
			hl_group = "CursorTabDeletion",
			hl_mode = "combine",
		})
		table.insert(completion_extmarks, { buf = current_buf, extmark_id = extmark_id })
	else
		local extmark_id = vim.api.nvim_buf_set_extmark(current_buf, ns_id, nvim_line, 0, {
			line_hl_group = "CursorTabDeletion",
		})
		table.insert(completion_extmarks, { buf = current_buf, extmark_id = extmark_id })
	end
end

-- Render append_chars: show only the appended part as ghost text
---@param group Group
---@param nvim_line integer 0-indexed line number
---@param current_win integer
---@param current_buf integer
---@param syntax_ft string
---@param ns_id integer
---@param is_first_append boolean
---@param virt_line_offset integer Number of virtual lines added above this point
---@return boolean was_first_append True if this was stored as the first append_chars
local function render_append_chars(
	group,
	nvim_line,
	current_win,
	current_buf,
	syntax_ft,
	ns_id,
	is_first_append,
	virt_line_offset
)
	local content = group.lines[1] or ""
	local col_start = group.col_start or 0
	local appended_text = string.sub(content, col_start + 1)
	local render_ghost_text = config.get().blink.ghost_text

	-- Store expected line state for partial typing optimization (only first append_chars)
	if is_first_append then
		expected_line = content
		expected_line_num = group.buffer_line
		original_len = col_start
		if appended_text and appended_text ~= "" then
			append_chars_state = {
				text = appended_text,
				line = group.buffer_line,
				col_start = col_start,
			}
		else
			append_chars_state = nil
		end
	end

	if not render_ghost_text then
		return is_first_append
	end

	if appended_text and appended_text ~= "" then
		if config.get().ui.completions.addition_style == "dimmed" then
			-- col_start is a byte offset; convert to display column for overlay positioning
			local display_col = vim.fn.strdisplaywidth(string.sub(content, 1, col_start))
			local overlay_win, overlay_buf, _ = create_overlay_window(
				current_win,
				nvim_line,
				display_col,
				{ appended_text },
				syntax_ft,
				"CursorTabAddition",
				nil,
				nvim_line,
				ns_id,
				0
			)
			table.insert(completion_windows, { win_id = overlay_win, buf_id = overlay_buf })

			if is_first_append then
				append_chars_extmark_id = nil
				append_chars_buf = current_buf
			end
		else
			local line_content = vim.api.nvim_buf_get_lines(current_buf, nvim_line, nvim_line + 1, false)[1] or ""
			local line_length = #line_content
			local virt_col = math.min(col_start, line_length)

			local extmark_id = vim.api.nvim_buf_set_extmark(current_buf, ns_id, nvim_line, virt_col, {
				virt_text = { { appended_text, "CursorTabCompletion" } },
				virt_text_pos = "overlay",
				hl_mode = "combine",
			})
			table.insert(completion_extmarks, { buf = current_buf, extmark_id = extmark_id })

			if is_first_append then
				append_chars_extmark_id = extmark_id
				append_chars_buf = current_buf
			end
		end
	end

	return is_first_append
end

-- Render delete_chars: highlight the column range to be deleted
---@param group Group
---@param nvim_line integer 0-indexed line number
---@param current_buf integer
---@param ns_id integer
local function render_delete_chars(group, nvim_line, current_buf, ns_id)
	local line_content = vim.api.nvim_buf_get_lines(current_buf, nvim_line, nvim_line + 1, false)[1] or ""
	local line_length = #line_content

	local col_start = math.max(0, math.min(group.col_start or 0, line_length))
	local col_end = math.max(col_start, math.min(group.col_end or 0, line_length))

	if col_end > col_start then
		local extmark_id = vim.api.nvim_buf_set_extmark(current_buf, ns_id, nvim_line, col_start, {
			end_col = col_end,
			hl_group = "CursorTabDeletion",
			hl_mode = "combine",
		})
		table.insert(completion_extmarks, { buf = current_buf, extmark_id = extmark_id })
	end
end

-- Render replace_chars: overlay entire line with highlight on changed portion
---@param group Group
---@param nvim_line integer 0-indexed line number
---@param current_win integer
---@param syntax_ft string
---@param ns_id integer
local function render_replace_chars(group, nvim_line, virt_line_offset, current_win, syntax_ft, ns_id)
	local content = group.lines[1] or ""
	local old_content = (group.old_lines and group.old_lines[1]) or ""
	local original_line_width = vim.fn.strdisplaywidth(old_content)

	if content ~= "" then
		local overlay_win, overlay_buf, bytes_trimmed = create_overlay_window(
			current_win,
			nvim_line,
			0,
			content,
			syntax_ft,
			nil,
			original_line_width,
			nil,
			ns_id,
			0
		)
		table.insert(completion_windows, { win_id = overlay_win, buf_id = overlay_buf })

		-- Highlight the changed portion
		local ov_line = vim.api.nvim_buf_get_lines(overlay_buf, 0, 1, false)[1] or ""
		local ov_len = #ov_line
		local start_col = math.max(0, (group.col_start or 0) - (bytes_trimmed or 0))
		local end_col = math.max(start_col, math.min(ov_len, (group.col_end or start_col) - (bytes_trimmed or 0)))
		if end_col > start_col then
			vim.api.nvim_buf_set_extmark(overlay_buf, ns_id, 0, start_col, {
				end_col = end_col,
				hl_group = "CursorTabAddition",
			})
		end
	end
end

-- Render modification group: highlight old lines as deletion, show new content side-by-side or stacked
---@param group Group
---@param nvim_line integer 0-indexed line number of the first line in the group
---@param virt_line_offset integer Number of virtual lines added above this point
---@param current_win integer
---@param current_buf integer
---@param syntax_ft string
---@param ns_id integer
---@return integer virt_lines_added Number of virtual lines added by stacked fallback
local function render_modification(group, nvim_line, virt_line_offset, current_win, current_buf, syntax_ft, ns_id)
	local line_count = group.end_line - group.start_line + 1
	local win_width = vim.api.nvim_win_get_width(current_win)
	local use_stacked = group.render_hint == "stacked"

	-- Compute max display width across all old lines for side-by-side overlay positioning
	local max_old_width = 0
	local overlay_col = 0
	if not use_stacked then
		for i = 1, line_count do
			local line_content = vim.api.nvim_buf_get_lines(current_buf, nvim_line + i - 1, nvim_line + i, false)[1]
				or ""
			local w = vim.fn.strdisplaywidth(line_content)
			if w > max_old_width then
				max_old_width = w
			end
		end
		overlay_col = max_old_width + 2
	end

	-- Highlight all old lines with deletion background
	for i = 1, line_count do
		highlight_deletion(nvim_line + i - 1, current_buf, ns_id)
	end

	if use_stacked then
		local last_nvim_line = nvim_line + line_count - 1
		local virt_lines_array = {}
		for _ = 1, line_count do
			table.insert(virt_lines_array, { { "", "Normal" } })
		end
		local virtual_extmark_id = vim.api.nvim_buf_set_extmark(current_buf, ns_id, last_nvim_line, 0, {
			virt_lines = virt_lines_array,
			virt_lines_above = false,
		})
		table.insert(completion_extmarks, { buf = current_buf, extmark_id = virtual_extmark_id })

		for i = 1, line_count do
			local new_line = group.lines[i]
			if new_line and new_line ~= "" then
				local overlay_win, overlay_buf, _ = create_overlay_window(
					current_win,
					last_nvim_line,
					0,
					new_line,
					syntax_ft,
					"CursorTabModification",
					win_width,
					nil,
					ns_id,
					i
				)
				table.insert(completion_windows, { win_id = overlay_win, buf_id = overlay_buf })
			end
		end
		return line_count
	else
		-- Side-by-side overlays
		for i = 1, line_count do
			local new_line = group.lines[i]
			if new_line and new_line ~= "" then
				local overlay_win, overlay_buf, _ = create_overlay_window(
					current_win,
					nvim_line + i - 1,
					overlay_col,
					new_line,
					syntax_ft,
					"CursorTabModification",
					nil,
					nil,
					ns_id,
					0
				)
				table.insert(completion_windows, { win_id = overlay_win, buf_id = overlay_buf })
			end
		end
		return 0
	end
end

-- Render addition group: virtual lines + overlay window
---@param group Group
---@param nvim_line integer 0-indexed line number
---@param virt_line_offset integer Number of virtual lines added above this point
---@param current_win integer
---@param current_buf integer
---@param syntax_ft string
---@param ns_id integer
local function render_addition(group, nvim_line, virt_line_offset, current_win, current_buf, syntax_ft, ns_id)
	local buf_line_count = vim.api.nvim_buf_line_count(current_buf)
	local line_count = group.end_line - group.start_line + 1

	-- Create empty virtual lines as placeholders
	local virt_lines_array = {}
	for _ = 1, line_count do
		table.insert(virt_lines_array, { { "", "Normal" } })
	end

	local virtual_extmark_id
	local overlay_buffer_line
	local overlay_row_offset
	if nvim_line >= buf_line_count then
		local last_existing_line = buf_line_count - 1
		virtual_extmark_id = vim.api.nvim_buf_set_extmark(current_buf, ns_id, last_existing_line, 0, {
			virt_lines = virt_lines_array,
			virt_lines_above = false,
		})
		overlay_buffer_line = last_existing_line
		overlay_row_offset = 1
	else
		virtual_extmark_id = vim.api.nvim_buf_set_extmark(current_buf, ns_id, nvim_line, 0, {
			virt_lines = virt_lines_array,
			virt_lines_above = true,
		})
		overlay_buffer_line = nvim_line
		overlay_row_offset = -line_count
	end
	table.insert(completion_extmarks, { buf = current_buf, extmark_id = virtual_extmark_id })

	-- Create overlay window for syntax-highlighted content
	if #group.lines > 0 then
		local win_width = vim.api.nvim_win_get_width(current_win)
		-- Ensure empty lines have at least a space so the overlay renders
		local display_lines = {}
		for _, line in ipairs(group.lines) do
			table.insert(display_lines, line ~= "" and line or " ")
		end
		local overlay_win, overlay_buf, _ = create_overlay_window(
			current_win,
			overlay_buffer_line,
			0,
			display_lines,
			syntax_ft,
			"CursorTabAddition",
			win_width,
			nil,
			ns_id,
			overlay_row_offset
		)
		table.insert(completion_windows, { win_id = overlay_win, buf_id = overlay_buf })
	end
end

-- Function to show completion diff highlighting (called from Go)
---@param diff_result DiffResult Completion diff result from Go daemon
local function show_completion(diff_result)
	clear_expected_line_state()

	local current_buf = vim.api.nvim_get_current_buf()
	local current_win = vim.api.nvim_get_current_win()

	-- Don't show in floating windows
	local win_config = vim.api.nvim_win_get_config(current_win)
	if win_config.relative ~= "" then
		return
	end

	-- Cache values used across all groups
	local syntax_ft = vim.api.nvim_get_option_value("filetype", { buf = current_buf })
	local ns_id = daemon.get_namespace_id()

	-- Pre-scroll viewport if stacked modifications would overflow the bottom
	local win_height = vim.api.nvim_win_get_height(current_win)
	local topline = vim.fn.getwininfo(current_win)[1].topline
	local max_row = 0
	local extra_virt = 0
	for _, group in ipairs(diff_result.groups or {}) do
		local nvim_line = group.buffer_line - 1
		local line_count = group.end_line - group.start_line + 1
		if group.render_hint == "stacked" then
			local last_row = (nvim_line + 2 * line_count - 1 + extra_virt) - (topline - 1)
			if last_row > max_row then
				max_row = last_row
			end
			extra_virt = extra_virt + line_count
		end
	end
	if max_row >= win_height then
		local scroll = max_row - win_height + 1
		vim.fn.winrestview({ topline = topline + scroll })
	end

	local found_first_append = false
	local virt_line_offset = 0 -- Track cumulative virtual lines for overlay positioning

	-- Process each group in order (groups are already sorted by start_line from Go)
	for _, group in ipairs(diff_result.groups or {}) do
		local is_single_line = group.start_line == group.end_line

		-- Use buffer_line directly (1-indexed absolute buffer position computed by Go)
		local nvim_line = group.buffer_line - 1 -- 0-indexed for nvim API

		-- Handle character-level render hints (single-line only)
		-- "stacked" is a layout hint for modifications, not a character-level hint
		local is_char_hint = group.render_hint == "append_chars"
			or group.render_hint == "replace_chars"
			or group.render_hint == "delete_chars"
		if is_single_line and is_char_hint then
			if group.render_hint == "append_chars" then
				local is_first = not found_first_append
				render_append_chars(
					group,
					nvim_line,
					current_win,
					current_buf,
					syntax_ft,
					ns_id,
					is_first,
					virt_line_offset
				)
				if is_first then
					found_first_append = true
				end
			elseif group.render_hint == "replace_chars" then
				render_replace_chars(group, nvim_line, virt_line_offset, current_win, syntax_ft, ns_id)
			elseif group.render_hint == "delete_chars" then
				render_delete_chars(group, nvim_line, current_buf, ns_id)
			end
		elseif group.type == "modification" then
			virt_line_offset = virt_line_offset
				+ render_modification(group, nvim_line, virt_line_offset, current_win, current_buf, syntax_ft, ns_id)
		elseif group.type == "addition" then
			local line_count = group.end_line - group.start_line + 1
			render_addition(group, nvim_line, virt_line_offset, current_win, current_buf, syntax_ft, ns_id)
			-- Update offset for subsequent overlays
			virt_line_offset = virt_line_offset + line_count
		elseif group.type == "deletion" then
			-- Deletions are always rendered per-line within the group
			for i = 1, (group.end_line - group.start_line + 1) do
				highlight_deletion(nvim_line + i - 1, current_buf, ns_id)
			end
		end
	end
end

-- Function to show cursor prediction jump text (called from Go)
---@param line_num integer Predicted line number (1-indexed)
local function show_cursor_prediction(line_num)
	-- Get current buffer and window info
	---@type integer
	local current_buf = vim.api.nvim_get_current_buf()
	---@type integer
	local current_win = vim.api.nvim_get_current_win()
	---@type table
	local win_config = vim.api.nvim_win_get_config(current_win)

	-- Don't show preview in floating windows
	if win_config.relative ~= "" then
		return
	end

	-- Go now uses 1-indexed line numbers, same as Neovim
	---@type integer
	local nvim_line_num = line_num

	-- Check if the predicted line is visible in the current viewport
	---@type integer
	local first_visible_line = vim.fn.line("w0")
	---@type integer
	local last_visible_line = vim.fn.line("w$")
	---@type integer
	local total_lines = vim.api.nvim_buf_line_count(current_buf)
	---@type integer
	local current_line = vim.fn.line(".")

	-- Ensure the line number is valid
	if nvim_line_num < 1 or nvim_line_num > total_lines then
		return
	end

	---@type CursortabConfig
	local cfg = config.get()

	if nvim_line_num >= first_visible_line and nvim_line_num <= last_visible_line then
		-- Line is visible
		---@type string
		local line_content = vim.api.nvim_buf_get_lines(current_buf, line_num - 1, line_num, false)[1] or ""
		---@type integer
		local line_length = #line_content

		jump_text_extmark_id =
			vim.api.nvim_buf_set_extmark(current_buf, daemon.get_namespace_id(), line_num - 1, line_length, {
				virt_text = {
					{ " " .. cfg.ui.jump.symbol, "CursorTabJumpSymbol" },
					{ cfg.ui.jump.text, "CursorTabJumpText" },
				},
				virt_text_pos = "overlay",
				hl_mode = "combine",
			})
		jump_text_buf = current_buf
	else
		-- Line is not visible - show directional arrow with distance
		---@type integer
		local win_width = vim.api.nvim_win_get_width(current_win)
		---@type integer
		local win_height = vim.api.nvim_win_get_height(current_win)

		-- Determine direction and calculate distance
		---@type boolean
		local is_below = nvim_line_num > last_visible_line
		---@type integer
		local distance = math.abs(nvim_line_num - current_line)

		-- Build the display text
		---@type string
		local display_text = cfg.ui.jump.text
		if cfg.ui.jump.show_distance then
			display_text = display_text .. "(" .. distance .. " lines) "
		end

		-- Create a scratch buffer for the arrow indicator
		absolute_jump_buf = vim.api.nvim_create_buf(false, true)
		vim.api.nvim_buf_set_lines(absolute_jump_buf, 0, -1, false, { display_text })
		vim.api.nvim_set_option_value("modifiable", false, { buf = absolute_jump_buf })

		-- Calculate position - center horizontally, top or bottom vertically
		---@type integer
		local text_width = vim.fn.strdisplaywidth(display_text)
		---@type integer
		local col = math.max(0, math.floor((win_width - text_width) / 2))
		---@type integer
		local row = is_below and (win_height - 2) or 1 -- Bottom or top with some padding

		-- Create floating window for absolute positioning
		absolute_jump_win = vim.api.nvim_open_win(absolute_jump_buf, false, {
			relative = "win",
			win = current_win,
			row = row,
			col = col,
			width = text_width,
			height = 1,
			style = "minimal",
			border = "none",
			zindex = 1,
			focusable = false,
		})

		-- Set window background to match jump text highlight
		vim.api.nvim_set_option_value("winhighlight", "Normal:CursorTabJumpText", { win = absolute_jump_win })
	end
end

-- Public API

-- Helper function to close all UI (matches original ensure_close_all)
function ui.ensure_close_all()
	ensure_close_cursor_prediction()
	ensure_close_completion()
	clear_expected_line_state()
end

-- Show completion diff highlighting
---@param diff_result DiffResult Completion diff result from Go daemon
function ui.show_completion(diff_result)
	has_completion = true
	ui.ensure_close_all()
	local ok = pcall(show_completion, diff_result)
	if not ok then
		ui.ensure_close_all()
		has_completion = false
	end
end

-- Show cursor prediction jump text
---@param line_num integer Predicted line number (1-indexed)
function ui.show_cursor_prediction(line_num)
	has_cursor_prediction = true
	ui.ensure_close_all()
	local ok = pcall(show_cursor_prediction, line_num)
	if not ok then
		ui.ensure_close_all()
		has_cursor_prediction = false
	end
end

-- Close all UI elements and reset state (for on_reject)
function ui.close_all()
	ui.ensure_close_all()
	has_completion = false
	has_cursor_prediction = false
end

-- Check if completion is visible
---@return boolean
function ui.has_completion()
	return has_completion
end

-- Check if cursor prediction is visible
---@return boolean
function ui.has_cursor_prediction()
	return has_cursor_prediction
end

---Get append_chars state for external consumers (e.g., blink source).
---@return AppendCharsState|nil
function ui.get_append_chars()
	if not append_chars_state then
		return nil
	end
	return vim.deepcopy(append_chars_state)
end

-- Check if typed content matches the expected completion (for partial typing optimization)
-- Returns true if current line is a valid progression toward the expected completion
---@param line_num integer Current cursor line (1-indexed)
---@param current_content string Current line content
---@return boolean
function ui.typing_matches_completion(line_num, current_content)
	if not expected_line or not expected_line_num or not original_len then
		return false
	end
	if line_num ~= expected_line_num then
		return false
	end
	-- Current must be: longer than original AND a prefix of target
	local current_len = #current_content
	if current_len <= original_len then
		return false
	end
	return expected_line:sub(1, current_len) == current_content
end

-- Update the ghost text extmark after user typed matching content
-- This avoids visual glitch where old extmark shifts before daemon re-renders
---@param line_num integer Current cursor line (1-indexed)
---@param current_content string Current line content
function ui.update_ghost_text_for_typing(line_num, current_content)
	if not expected_line or not append_chars_extmark_id or not append_chars_buf then
		return
	end
	if not vim.api.nvim_buf_is_valid(append_chars_buf) then
		return
	end

	-- Calculate remaining ghost text
	local current_len = #current_content
	local remaining_ghost = expected_line:sub(current_len + 1)

	-- Delete old extmark
	pcall(vim.api.nvim_buf_del_extmark, append_chars_buf, daemon.get_namespace_id(), append_chars_extmark_id)

	-- If there's remaining ghost text, create new extmark at end of current line
	if remaining_ghost and remaining_ghost ~= "" then
		local nvim_line = line_num - 1 -- Convert to 0-indexed
		local new_extmark_id =
			vim.api.nvim_buf_set_extmark(append_chars_buf, daemon.get_namespace_id(), nvim_line, current_len, {
				virt_text = { { remaining_ghost, "CursorTabCompletion" } },
				virt_text_pos = "overlay",
				hl_mode = "combine",
			})
		append_chars_extmark_id = new_extmark_id

		-- Also update in completion_extmarks array for proper cleanup
		for i, info in ipairs(completion_extmarks) do
			if info.buf == append_chars_buf and info.extmark_id ~= new_extmark_id then
				-- Replace old entry with new one
				completion_extmarks[i] = { buf = append_chars_buf, extmark_id = new_extmark_id }
				break
			end
		end
	else
		append_chars_extmark_id = nil
	end
end

---Create a scratch buffer window with content (replaces floating window)
---@param title string Window title
---@param content string[] Array of content lines
---@param opts table|nil Optional window configuration overrides
---@return integer win_id, integer buf_id
function ui.create_scratch_window(title, content, opts)
	opts = opts or {}

	-- Store the current window to return to later
	local previous_win = vim.api.nvim_get_current_win()

	-- Create buffer for content
	local buf = vim.api.nvim_create_buf(false, true)
	-- Set buffer name with error handling
	local safe_name = "[" .. (title or "unnamed") .. "]"
	pcall(vim.api.nvim_buf_set_name, buf, safe_name)
	vim.api.nvim_buf_set_lines(buf, 0, -1, false, content)

	-- Set buffer options for scratch buffer behavior
	vim.api.nvim_set_option_value("modifiable", false, { buf = buf })
	vim.api.nvim_set_option_value("readonly", true, { buf = buf })
	vim.api.nvim_set_option_value("filetype", opts.filetype or "markdown", { buf = buf })
	vim.api.nvim_set_option_value("buftype", "nofile", { buf = buf })
	vim.api.nvim_set_option_value("bufhidden", "wipe", { buf = buf })
	vim.api.nvim_set_option_value("swapfile", false, { buf = buf })

	-- Determine window sizing based on options
	local win_height = vim.api.nvim_get_option_value("lines", {})
	local split_height

	if opts.size_mode == "fullscreen" then
		-- Use fullscreen - create a new tab
		vim.cmd("tabnew")
		local win = vim.api.nvim_get_current_win()
		vim.api.nvim_win_set_buf(win, buf)
	elseif opts.size_mode == "fit_content" then
		-- Fit content plus one extra line for padding
		split_height = math.min(#content + 1, math.floor(win_height * 0.8)) -- Cap at 80% of screen
		vim.cmd("split")
		local win = vim.api.nvim_get_current_win()
		vim.api.nvim_win_set_height(win, split_height)
		vim.api.nvim_win_set_buf(win, buf)
	else
		-- Default: use 1/3 of screen height
		split_height = math.floor(win_height * 0.3)
		vim.cmd("split")
		local win = vim.api.nvim_get_current_win()
		vim.api.nvim_win_set_height(win, split_height)
		vim.api.nvim_win_set_buf(win, buf)
	end

	local win = vim.api.nvim_get_current_win()

	-- Move to end of content if specified
	if opts.move_to_end and #content > 0 then
		vim.api.nvim_win_set_cursor(win, { #content, 0 })
	end

	-- Also set up autocmd to clean up when buffer is wiped
	vim.api.nvim_create_autocmd("BufWipeout", {
		buffer = buf,
		callback = function()
			-- Return to the previous window if it's still valid
			if vim.api.nvim_win_is_valid(previous_win) then
				vim.api.nvim_set_current_win(previous_win)
			end
		end,
	})

	return win, buf
end

return ui
