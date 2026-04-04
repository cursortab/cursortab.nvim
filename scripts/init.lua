-- Minimal test config for cursortab.nvim
-- Loads the plugin directly from the repo root

local plugin_dir = vim.fn.fnamemodify(debug.getinfo(1, "S").source:sub(2), ":h:h")
vim.opt.rtp:prepend(plugin_dir)

require("cursortab").setup({
	log_level = "debug",
	provider = {
		type = "sweep",
	},
})
