local source = debug.getinfo(1, "S").source:sub(2)
local plugin_dir = vim.fn.fnamemodify(source, ":h:h")
vim.opt.rtp:prepend(plugin_dir)

vim.o.columns = 120
vim.o.lines = 20
vim.o.wrap = false
vim.o.number = false
vim.o.relativenumber = false
vim.o.signcolumn = "no"
vim.o.foldcolumn = "0"
vim.o.laststatus = 0
vim.o.cmdheight = 1

local function assert_equal(expected, actual, label)
	if expected ~= actual then
		error(string.format("%s: expected %s, got %s", label, vim.inspect(expected), vim.inspect(actual)))
	end
end

local config = require("cursortab.config")
config.setup({
	enabled = false,
	blink = { ghost_text = true },
	ui = { completions = { addition_style = "dimmed" } },
})
config.setup_highlights()

local ui = require("cursortab.ui")
local old_line = '- Never use the em dash "—". Use plain dash "-" inst'
local new_line = old_line .. "ead."

vim.api.nvim_buf_set_lines(0, 0, -1, false, { old_line })
vim.api.nvim_win_set_cursor(0, { 1, #old_line })
vim.cmd("redraw")

ui.show_completion({
	groups = {
		{
			type = "modification",
			start_line = 1,
			end_line = 1,
			buffer_line = 1,
			lines = { new_line },
			old_lines = { old_line },
			render_hint = "append_chars",
			col_start = #old_line,
			col_end = #new_line,
		},
	},
	startLine = 1,
	cursor_line = 1,
	cursor_col = #new_line,
})

local overlay_config
for _, win in ipairs(vim.api.nvim_tabpage_list_wins(0)) do
	local win_config = vim.api.nvim_win_get_config(win)
	if win_config.relative ~= "" then
		overlay_config = win_config
		break
	end
end

if not overlay_config then
	error("completion overlay was not created")
end

assert_equal(vim.fn.strdisplaywidth(old_line), overlay_config.col, "overlay column")
print("ui positioning test passed")
vim.cmd("qa!")
