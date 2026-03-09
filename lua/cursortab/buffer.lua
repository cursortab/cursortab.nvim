-- Buffer state management for cursortab.nvim

local config = require("cursortab.config")

---@class BufferModule
local buffer = {}

---@class BufferState
---@field is_floating_window boolean
---@field is_modifiable boolean
---@field is_readonly boolean
---@field filetype string
---@field should_skip boolean Combined check result
---@field current_buf integer|nil
---@field current_win integer|nil

-- Convert a gitignore-style glob pattern to a Lua pattern.
-- If the pattern contains "/", it matches against the full relative path;
-- otherwise it matches against the filename only.
---@param path string Absolute file path
---@param pattern string Gitignore-style glob pattern
---@return boolean
local function match_ignore_pattern(path, pattern)
	-- Determine what to match against
	local match_target
	if pattern:find("/", 1, true) then
		-- Pattern contains "/" — match against relative path from cwd
		local cwd = vim.fn.getcwd() .. "/"
		if path:sub(1, #cwd) == cwd then
			match_target = path:sub(#cwd + 1)
		else
			match_target = path
		end
	else
		-- No "/" in pattern — match against filename only
		match_target = vim.fn.fnamemodify(path, ":t")
	end

	-- Convert glob to Lua pattern:
	-- First handle "**" (any chars including /), then "*" (any non-/ chars)
	local lua_pattern = pattern:gsub("([%.%+%-%%%[%]%^%$%(%)%?])", "%%%1") -- escape special chars (except *)
	lua_pattern = lua_pattern:gsub("%*%*", "\0") -- placeholder for **
	lua_pattern = lua_pattern:gsub("%*", "[^/]*") -- * -> any non-/ chars
	lua_pattern = lua_pattern:gsub("%z", ".*") -- ** -> any chars
	lua_pattern = "^" .. lua_pattern .. "$"

	return match_target:match(lua_pattern) ~= nil
end

-- Check if a file path matches any ignore pattern.
---@param path string Absolute file path
---@param patterns string[] Glob patterns
---@return boolean
local function matches_ignore_paths(path, patterns)
	for _, pattern in ipairs(patterns) do
		if match_ignore_pattern(path, pattern) then
			return true
		end
	end
	return false
end

-- Check if a filetype should be ignored.
---@param filetype string
---@param ignored_filetypes string[]
---@return boolean
local function is_ignored_filetype(filetype, ignored_filetypes)
	for _, ft in ipairs(ignored_filetypes) do
		if ft == filetype then
			return true
		end
	end
	return false
end

-- Cache for gitignore check results (path -> boolean)
---@type table<string, boolean>
local gitignore_cache = {}

-- Files that should never be treated as gitignored
local gitignore_exceptions = {
	["COMMIT_EDITMSG"] = true,
}

-- Check if a file is ignored by git (cached).
---@param path string Absolute file path
---@return boolean
local function is_gitignored(path)
	local filename = vim.fn.fnamemodify(path, ":t")
	if gitignore_exceptions[filename] then
		return false
	end
	local cached = gitignore_cache[path]
	if cached ~= nil then
		return cached
	end
	vim.fn.system({ "git", "check-ignore", "--quiet", path })
	local ignored = vim.v.shell_error == 0
	gitignore_cache[path] = ignored
	return ignored
end

-- Global buffer state cache to avoid expensive API calls on every cursor movement
---@type BufferState
local buffer_state = {
	is_floating_window = false,
	is_modifiable = false,
	is_readonly = false,
	filetype = "",
	should_skip = true, -- Combined check result
	current_buf = nil,
	current_win = nil,
}

-- Function to update buffer state (called when buffer/window changes)
local function update_buffer_state()
	---@type integer
	local current_buf = vim.api.nvim_get_current_buf()
	---@type integer
	local current_win = vim.api.nvim_get_current_win()

	-- Only update if buffer or window actually changed
	if buffer_state.current_buf == current_buf and buffer_state.current_win == current_win then
		return
	end

	-- Re-check if we're still in the same buffer/window after defer
	if vim.api.nvim_get_current_buf() ~= current_buf or vim.api.nvim_get_current_win() ~= current_win then
		return
	end

	-- Update cached state
	buffer_state.current_buf = current_buf
	buffer_state.current_win = current_win

	-- Check if in floating window
	---@type table
	local win_config = vim.api.nvim_win_get_config(current_win)
	buffer_state.is_floating_window = win_config.relative ~= ""

	-- Check buffer properties
	buffer_state.is_modifiable = vim.api.nvim_get_option_value("modifiable", { buf = current_buf })
	buffer_state.is_readonly = vim.api.nvim_get_option_value("readonly", { buf = current_buf })
	buffer_state.filetype = vim.api.nvim_get_option_value("filetype", { buf = current_buf })

	-- Combined check: should we skip idle completions for this buffer?
	local cfg = config.get()
	local should_skip_filetype = is_ignored_filetype(buffer_state.filetype, cfg.behavior.ignore_filetypes)
	local should_skip_path = false
	local buf_path = vim.api.nvim_buf_get_name(current_buf)
	if buf_path ~= "" then
		if #cfg.behavior.ignore_paths > 0 then
			should_skip_path = matches_ignore_paths(buf_path, cfg.behavior.ignore_paths)
		end
		if not should_skip_path and cfg.behavior.ignore_gitignored then
			should_skip_path = is_gitignored(buf_path)
		end
	end

	buffer_state.should_skip = buffer_state.is_floating_window
		or not buffer_state.is_modifiable
		or buffer_state.is_readonly
		or should_skip_filetype
		or should_skip_path
end

-- Public API

-- Update cached buffer state
function buffer.update_state()
	update_buffer_state()
end

-- Check if current buffer should be skipped
---@return boolean
function buffer.should_skip()
	return buffer_state.should_skip
end

-- Set up autocmds for cache invalidation
function buffer.setup()
	vim.api.nvim_create_autocmd("BufWritePost", {
		group = vim.api.nvim_create_augroup("cursortab_gitignore_cache", { clear = true }),
		pattern = { ".gitignore", ".git/info/exclude" },
		callback = function()
			gitignore_cache = {}
			-- Force re-evaluation on next buffer enter
			buffer_state.current_buf = nil
			buffer_state.current_win = nil
		end,
	})
end

return buffer
