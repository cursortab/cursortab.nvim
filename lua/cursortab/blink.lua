---@diagnostic disable: undefined-doc-name
local config = require("cursortab.config")
local daemon = require("cursortab.daemon")
local ui = require("cursortab.ui")

local M = {}

local empty_response = { items = {}, is_incomplete_forward = false, is_incomplete_backward = false }

if vim.tbl_isempty(vim.api.nvim_get_hl(0, { name = "BlinkCmpItemKindCursortab" })) then
	vim.api.nvim_set_hl(0, "BlinkCmpItemKindCursortab", { link = "BlinkCmpItemKind" })
end

---Create a new blink source instance.
---@return table
function M.new()
	local obj = setmetatable({}, { __index = M })
	return obj
end

---Check whether the source is enabled.
---@return boolean
function M:enabled()
	local cfg = config.get()
	return cfg.blink and cfg.blink.enabled and daemon.is_enabled()
end

---Return completions based on current append_chars state.
---@param _ blink.cmp.Context
---@param callback fun(response: blink.cmp.CompletionResponse | nil)
function M:get_completions(_, callback)
	local cfg = config.get()
	if not (cfg.blink and cfg.blink.enabled) or not daemon.is_enabled() then
		callback(empty_response)
		return
	end

	local append_chars = ui.get_append_chars()
	if not append_chars or append_chars.text == "" then
		callback(empty_response)
		return
	end

	local item = {
		label = append_chars.text,
		insertText = append_chars.text,
		kind = require("blink.cmp.types").CompletionItemKind.Text,
		kind_name = "Cursortab",
		kind_hl = "BlinkCmpItemKindCursortab",
	}

	callback({
		items = { item },
		is_incomplete_forward = false,
		is_incomplete_backward = false,
	})
end

return M
