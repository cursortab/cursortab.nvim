-- Main entry point for cursortab.nvim
-- Import all modules
local config = require("cursortab.config")
local daemon = require("cursortab.daemon")
local events = require("cursortab.events")
local ui = require("cursortab.ui")

---@class CursortabModule
local M = {}

-- RPC callback functions (called from Go daemon)
-- These must remain globally accessible for the RPC interface

---RPC callback: called when completion is rejected
function M.on_reject()
	-- Clear awaiting flag in case we were waiting for a completion
	events.clear_awaiting_completion()
	ui.close_all()
end

---Accept current completion/prediction if available.
---@return boolean accepted
function M.accept()
	return events.accept()
end

---Check if cursortab is mid-completion (for other plugins to suppress their menus).
---@return boolean
function M.is_completing()
	return events.is_completing()
end

---RPC callback: called when completion is ready
---@param diff_result DiffResult Completion diff result from Go daemon
function M.on_completion_ready(diff_result)
	-- Clear the awaiting flag now that we've received the completion
	events.clear_awaiting_completion()
	ui.show_completion(diff_result)
end

---RPC callback: called when cursor prediction is ready
---@param line_num integer Predicted line number (1-indexed)
function M.on_cursor_prediction_ready(line_num)
	ui.show_cursor_prediction(line_num)
end

-- Public API functions for users

---Toggle cursortab functionality on/off
function M.toggle()
	local enabled = not daemon.is_enabled()
	daemon.set_enabled(enabled)

	if enabled then
		vim.notify("Cursortab enabled", vim.log.levels.INFO)
	else
		vim.notify("Cursortab disabled", vim.log.levels.INFO)
		-- Clear all completions and predictions when disabling
		events.clear_all_completions()
	end
end

---Show cursortab log file in a floating window
function M.show_log()
	local cfg = config.get()
	local log_path = cfg.state_dir .. "/cursortab.log"

	-- Check if log file exists
	if vim.fn.filereadable(log_path) == 0 then
		vim.notify("Log file not found: " .. log_path, vim.log.levels.WARN)
		return
	end

	-- Read the log file content
	local lines = vim.fn.readfile(log_path)

	-- Create scratch window using UI module
	ui.create_scratch_window("Cursortab Log", lines, {
		filetype = "log",
		move_to_end = true,
		size_mode = "fullscreen",
	})

	vim.notify("Showing cursortab log", vim.log.levels.INFO)
end

---Clear cursortab log file
function M.clear_log()
	local cfg = config.get()
	local log_path = cfg.state_dir .. "/cursortab.log"

	-- Check if log file exists
	if vim.fn.filereadable(log_path) == 0 then
		vim.notify("Log file not found: " .. log_path, vim.log.levels.WARN)
		return
	end

	-- Clear the log file by writing empty content
	vim.fn.writefile({}, log_path)

	vim.notify("Cursortab log cleared", vim.log.levels.INFO)
end

---Show cursortab status via checkhealth
function M.status()
	vim.cmd("checkhealth cursortab")
end

---Restart cursortab daemon
function M.restart()
	vim.notify("Restarting cursortab daemon...", vim.log.levels.INFO)

	-- Clear any existing completions first
	events.clear_all_completions()

	-- Stop existing daemon (this now handles all cleanup reliably)
	local _, stop_message = daemon.stop_daemon()
	vim.notify(stop_message, vim.log.levels.INFO)

	-- Small delay to ensure cleanup is complete
	vim.defer_fn(function()
		-- Explicitly start the daemon
		local start_success = daemon.force_start()

		if start_success then
			vim.notify("Cursortab daemon restarted successfully", vim.log.levels.INFO)
		else
			vim.notify("Failed to start cursortab daemon", vim.log.levels.ERROR)
		end
	end, 200)
end

---Setup cursortab with user configuration
---@param user_config table|nil User configuration overrides
function M.setup(user_config)
	-- Setup configuration
	local cfg = config.setup(user_config)
	daemon.set_enabled(cfg.enabled)

	-- Create user commands
	vim.api.nvim_create_user_command("CursortabToggle", function()
		M.toggle()
	end, { desc = "Toggle Cursortab functionality" })

	vim.api.nvim_create_user_command("CursortabShowLog", function()
		M.show_log()
	end, { desc = "Show cursortab log file in a scratch window" })

	vim.api.nvim_create_user_command("CursortabClearLog", function()
		M.clear_log()
	end, { desc = "Clear cursortab log file" })

	vim.api.nvim_create_user_command("CursortabStatus", function()
		M.status()
	end, { desc = "Show cursortab status information" })

	vim.api.nvim_create_user_command("CursortabRestart", function()
		M.restart()
	end, { desc = "Restart cursortab daemon" })

	-- Setup highlight groups
	config.setup_highlights()

	-- Set up highlight namespace
	vim.api.nvim_set_hl_ns(daemon.get_namespace_id())

	-- Setup events and autocommands
	events.setup()

	-- Patch completion plugins after all plugins have loaded
	vim.schedule(function()
		M._patch_completion_plugins()
	end)

	-- Start the daemon (non-blocking)
	vim.defer_fn(function()
		daemon.force_start()
	end, 0)
end

-- Wrap a completion plugin's enabled function to return false while cursortab is completing
---@param original any
---@return function
local function wrap_enabled(original)
	return function()
		if events.is_completing() then
			return false
		end
		if type(original) == "function" then
			return original()
		end
		if original == nil then
			return true
		end
		return original
	end
end

-- Patch nvim-cmp to suppress during cursortab completion
function M._patch_completion_plugins()
	local ok_cmp, cmp = pcall(require, "cmp")
	if ok_cmp and cmp.get_config and vim.is_callable(cmp.setup) then
		local cmp_config = cmp.get_config()
		cmp.setup({ enabled = wrap_enabled(cmp_config.enabled) })
	end
end

return M
