-- Event handling and autocommands for cursortab.nvim

local buffer = require("cursortab.buffer")
local config = require("cursortab.config")
local daemon = require("cursortab.daemon")
local ui = require("cursortab.ui")

---@class EventsModule
local events = {}

-- Check if a mode is enabled in the config
---@param mode string "insert" or "normal"
---@return boolean
local function is_mode_enabled(mode)
	local modes = config.get().behavior.enabled_modes
	for _, m in ipairs(modes) do
		if m == mode then
			return true
		end
	end
	return false
end

-- Track if autocommands have been set up to prevent duplicate registrations
local autocommands_setup_done = false

-- Track currently bound keys so we can clean them up on re-setup
---@type {accept: string|nil, partial_accept: string|nil, trigger: string|nil}
local current_keymaps = { accept = nil, partial_accept = nil, trigger = nil }

local esc_handler_ns = nil

-- Skip exactly one TextChanged after accepting a completion
---@type boolean
local skip_next_text_changed = false

-- State for cursor movement suppression during completion application
---@type boolean
local skip_next_cursor_moved = false

-- Flag to suppress reject events while waiting for completion after cursor target accept
---@type boolean
local awaiting_completion_after_jump = false

-- Track if text changed in current event loop tick (to dedupe with CursorMovedI)
---@type boolean
local text_changed_this_tick = false

-- Accept key handler
---@return string
local function on_accept()
	if ui.has_cursor_prediction() or ui.has_completion() then
		-- Suppress the immediate text change and cursor movement caused by applying the completion
		skip_next_text_changed = true
		skip_next_cursor_moved = true
		-- When accepting cursor prediction, suppress rejects until we receive the completion
		-- Server runs normal! commands which trigger multiple events
		if ui.has_cursor_prediction() then
			awaiting_completion_after_jump = true
		end
		daemon.send_event("accept")
		return ""
	else
		return "\t"
	end
end

-- Escape key handler
local function on_escape()
	daemon.send_event_immediate("esc")
end

-- Partial accept handler (Shift-Tab by default)
---@return string
local function on_partial_accept()
	if ui.has_completion() then
		-- Suppress the immediate text change and cursor movement caused by partial accept
		skip_next_text_changed = true
		skip_next_cursor_moved = true
		daemon.send_event("partial_accept")
		return ""
	else
		-- Pass through configured key
		local cfg = config.get()
		return vim.api.nvim_replace_termcodes(cfg.keymaps.partial_accept, true, true, true)
	end
end

-- Manual trigger handler
local function on_trigger()
	daemon.send_event_immediate("trigger_completion")
end

-- Update a single keymap slot: clear old binding if changed, set new one
local function update_keymap(name, new_key, handler, opts)
	if current_keymaps[name] and current_keymaps[name] ~= new_key then
		pcall(vim.keymap.del, "i", current_keymaps[name])
		pcall(vim.keymap.del, "n", current_keymaps[name])
		current_keymaps[name] = nil
	end
	if new_key then
		vim.keymap.set("i", new_key, handler, opts)
		vim.keymap.set("n", new_key, handler, opts)
		current_keymaps[name] = new_key
	end
end

-- Set up keymaps (can be called multiple times when config changes)
local function setup_keymaps()
	local cfg = config.get()
	local expr_opts = { noremap = true, silent = true, expr = true }
	local plain_opts = { noremap = true, silent = true }

	update_keymap("accept", cfg.keymaps.accept, on_accept, expr_opts)
	update_keymap("partial_accept", cfg.keymaps.partial_accept, on_partial_accept, expr_opts)
	update_keymap("trigger", cfg.keymaps.trigger, on_trigger, plain_opts)

	if esc_handler_ns then
		-- Clear previous handler
		vim.on_key(nil, esc_handler_ns)
	end

	local ESC = vim.keycode("<Esc>")
	esc_handler_ns = vim.on_key(function(_, typed)
		if typed == ESC then
			on_escape()
		end
	end)
end

-- Set up autocommands (only once)
local function setup_autocommands()
	if autocommands_setup_done then
		return
	end
	autocommands_setup_done = true

	-- Track buffer/window focus changes to update cached state
	vim.api.nvim_create_autocmd({ "BufEnter", "WinEnter" }, {
		callback = vim.schedule_wrap(function()
			buffer.update_state()
		end),
	})

	-- Text change events
	vim.api.nvim_create_autocmd({ "TextChanged", "TextChangedI" }, {
		callback = function(args)
			-- Skip if buffer should be ignored
			if buffer.should_skip() then
				return
			end

			-- Skip exactly one text change immediately following a completion accept
			if skip_next_text_changed then
				skip_next_text_changed = false
				return
			end

			-- Mark that text changed this tick (to dedupe with CursorMovedI)
			text_changed_this_tick = true
			vim.schedule(function()
				text_changed_this_tick = false
			end)

			-- Handle cursor prediction (always clear - no partial match logic)
			if ui.has_cursor_prediction() then
				ui.ensure_close_all()
			elseif ui.has_completion() then
				-- For completions, check if typing matches the prediction
				-- If it matches, update ghost text locally to avoid visual glitch
				-- If it doesn't match, clear immediately to avoid showing stale ghost text
				local current_line = vim.api.nvim_get_current_line()
				local cursor_line = vim.fn.line(".")
				if ui.typing_matches_completion(cursor_line, current_line) then
					-- Update extmark position/content locally for smooth visual
					ui.update_ghost_text_for_typing(cursor_line, current_line)
				else
					ui.ensure_close_all()
				end
			end

			if args.event == "TextChangedI" and not is_mode_enabled("insert") then
				return
			end
			if args.event == "TextChanged" and not is_mode_enabled("normal") then
				return
			end

			daemon.send_event("text_changed")
		end,
	})

	-- Shared cursor movement handler (UI only, does not send event)
	---@return boolean suppressed true if the event was suppressed (skip sending)
	local function handle_cursor_moved(is_insert)
		if is_insert and text_changed_this_tick then
			return true
		end
		if skip_next_cursor_moved then
			skip_next_cursor_moved = false
			return true
		end
		if awaiting_completion_after_jump then
			return true
		end
		if ui.has_cursor_prediction() or ui.has_completion() then
			ui.ensure_close_all()
		end
		return false
	end

	-- Cursor movement events (normal mode)
	vim.api.nvim_create_autocmd({ "CursorMoved" }, {
		callback = function()
			if handle_cursor_moved(false) then
				return
			end
			if not is_mode_enabled("normal") then
				return
			end
			daemon.send_event("cursor_moved")
		end,
	})

	-- Cursor movement events (insert mode - e.g., arrow keys)
	vim.api.nvim_create_autocmd({ "CursorMovedI" }, {
		callback = function()
			if handle_cursor_moved(true) then
				return
			end
			if not is_mode_enabled("insert") then
				return
			end
			daemon.send_event("cursor_moved")
		end,
	})

	-- Insert mode events
	vim.api.nvim_create_autocmd({ "InsertEnter" }, {
		callback = function()
			daemon.send_event("insert_enter")
		end,
	})

	vim.api.nvim_create_autocmd({ "InsertLeave" }, {
		callback = function()
			-- Skip if buffer should be ignored
			if buffer.should_skip() then
				return
			end

			if ui.has_cursor_prediction() or ui.has_completion() then
				ui.ensure_close_all()
			end
			-- Send immediately without debounce - critical for committing user edits
			daemon.send_event_immediate("insert_leave")
		end,
	})

	-- Set up autocommand to close completions/predictions on certain events
	vim.api.nvim_create_autocmd({ "ModeChanged", "CmdlineEnter", "CmdwinEnter", "BufEnter" }, {
		callback = function(args)
			-- Don't close when transitioning from normal to insert mode
			if args.event == "ModeChanged" and args.match and args.match:match("^n:i") then
				return
			end

			-- Skip all events while awaiting completion after cursor target jump
			if awaiting_completion_after_jump then
				return
			end

			if ui.has_cursor_prediction() or ui.has_completion() then
				ui.ensure_close_all()
			end

			daemon.send_reject()
		end,
	})
end

-- Set up all autocommands and keymaps
function events.setup()
	buffer.setup()
	setup_autocommands()
	setup_keymaps()
end

-- Clear all completions (exposed for manual use)
function events.clear_all_completions()
	ui.close_all()
	daemon.send_reject()
end

-- Clear the awaiting completion flag (called when completion is received)
function events.clear_awaiting_completion()
	awaiting_completion_after_jump = false
end

---Accept current completion/prediction if available.
---@return boolean accepted
function events.accept()
	return on_accept() == ""
end

return events
