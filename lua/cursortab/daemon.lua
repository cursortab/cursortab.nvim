-- Daemon management and RPC communication for cursortab.nvim

local config = require("cursortab.config")
local buffer = require("cursortab.buffer")

local daemon = {}

-- Module state
---@type integer|nil
local chan = nil
local ns_id = vim.api.nvim_create_namespace("cursortab")
local event_debounce_timer = nil
local is_enabled = true

-- Check if process with given PID is running
local function is_process_running(pid)
	vim.fn.system("kill -0 " .. pid .. " 2>/dev/null")
	return vim.v.shell_error == 0
end

-- Read daemon PID from file and check if it's running
---@param pid_path string
---@return integer|nil pid, boolean running
local function read_daemon_pid(pid_path)
	if vim.fn.filereadable(pid_path) == 0 then
		return nil, false
	end
	local pid_content = vim.fn.readfile(pid_path)
	if #pid_content == 0 then
		return nil, false
	end
	local pid = tonumber(pid_content[1])
	if not pid then
		return nil, false
	end
	return pid, is_process_running(pid)
end

-- Start the daemon process
local function start_daemon()
	local cfg = config.get()
	local state_dir = cfg.state_dir

	-- Ensure state directory exists
	vim.fn.mkdir(state_dir, "p")

	local plugin_dir = vim.fn.fnamemodify(debug.getinfo(1, "S").source:sub(2), ":h:h:h")
	local binary_name = "cursortab"
	if vim.fn.has("win32") == 1 or vim.fn.has("win64") == 1 then
		binary_name = binary_name .. ".exe"
	end
	local binary_path = plugin_dir .. "/server/" .. binary_name
	local socket_path = state_dir .. "/cursortab.sock"
	local pid_path = state_dir .. "/cursortab.pid"

	-- Check if binary exists
	if vim.fn.executable(binary_path) == 0 then
		vim.notify(
			"cursortab binary not found at: "
				.. binary_path
				.. "\n"
				.. "Please ensure the Go server was built during installation.\n"
				.. "If using lazy.nvim, make sure the build step is configured:\n"
				.. 'build = "cd server && go build"',
			vim.log.levels.ERROR
		)
		return false
	end

	-- Create JSON configuration (matches Go Config struct)
	-- Note: UI config is Lua-only (for highlights), not sent to Go daemon
	local v = vim.version()
	local json_config = vim.json.encode({
		ns_id = ns_id,
		log_level = cfg.log_level,
		state_dir = state_dir,
		editor_version = string.format("%d.%d.%d", v.major, v.minor, v.patch),
		editor_os = vim.uv.os_uname().sysname, ---@diagnostic disable-line: undefined-field
		behavior = {
			idle_completion_delay = cfg.behavior.idle_completion_delay,
			text_change_debounce = cfg.behavior.text_change_debounce,
			max_visible_lines = cfg.behavior.max_visible_lines,
			cursor_prediction = {
				enabled = cfg.behavior.cursor_prediction.enabled,
				auto_advance = cfg.behavior.cursor_prediction.auto_advance,
				proximity_threshold = cfg.behavior.cursor_prediction.proximity_threshold,
			},
		},
		provider = {
			type = cfg.provider.type,
			url = cfg.provider.url,
			api_key_env = cfg.provider.api_key_env,
			model = cfg.provider.model,
			temperature = cfg.provider.temperature,
			max_tokens = cfg.provider.max_tokens,
			top_k = cfg.provider.top_k,
			completion_timeout = cfg.provider.completion_timeout,
			max_diff_history_tokens = cfg.provider.max_diff_history_tokens,
			completion_path = cfg.provider.completion_path,
			fim_tokens = cfg.provider.fim_tokens,
			privacy_mode = cfg.provider.privacy_mode,
		},
		debug = {
			immediate_shutdown = cfg.debug.immediate_shutdown,
		},
	})

	local env = vim.fn.environ()
	env.CURSORTAB_CONFIG = json_config

	-- Check if we need to start the daemon
	local need_daemon_start = false
	local config_path = state_dir .. "/cursortab.config.json"

	if vim.fn.filereadable(socket_path) == 0 then
		-- No socket, need to start daemon
		need_daemon_start = true
	else
		-- Socket exists, check if daemon is actually running
		local _, daemon_running = read_daemon_pid(pid_path)

		if not daemon_running then
			-- Stale socket, clean up and start fresh
			vim.fn.delete(socket_path)
			if vim.fn.filereadable(pid_path) == 1 then
				vim.fn.delete(pid_path)
			end
			need_daemon_start = true
		elseif vim.fn.filereadable(config_path) == 1 then
			-- Daemon is running, check if config has changed
			local stored = table.concat(vim.fn.readfile(config_path), "\n")
			if stored ~= json_config then
				daemon.stop_daemon()
				need_daemon_start = true
			end
		end
	end

	if need_daemon_start then
		vim.fn.jobstart({ binary_path, "--daemon" }, {
			env = env,
			detach = true,
		})

		-- Write config so future connections can detect changes
		vim.fn.writefile({ json_config }, config_path)

		-- Wait for socket (max 1 second)
		for _ = 1, 10 do
			vim.wait(100)
			if vim.fn.filereadable(socket_path) == 1 then
				break
			end
		end
	end

	-- Connect to daemon
	chan = vim.fn.jobstart({ binary_path }, {
		rpc = true,
		env = env,
	})

	return chan > 0
end

-- Send RPC event to daemon
---@param event_name string
local function send_rpc_event(event_name)
	if buffer.should_skip() or not is_enabled then
		return
	end

	-- Drop event if no valid channel (daemon starts on setup/restart, not here)
	if not chan or chan <= 0 then
		return
	end

	local success = pcall(function()
		vim.fn.rpcnotify(chan, "cursortab_event", event_name)
	end)

	if not success then
		chan = nil
	end
end

-- Public API

-- Send event with debouncing
---@param event_name string
function daemon.send_event(event_name)
	if event_debounce_timer then
		vim.fn.timer_stop(event_debounce_timer)
	end
	local event_debounce_ms = 10 -- hardcoded internal value
	event_debounce_timer = vim.fn.timer_start(event_debounce_ms, function()
		send_rpc_event(event_name)
	end)
end

-- Get the namespace ID
function daemon.get_namespace_id()
	return ns_id
end

-- Enable/disable daemon functionality
---@param enabled boolean
function daemon.set_enabled(enabled)
	is_enabled = enabled
end

function daemon.is_enabled()
	return is_enabled
end

-- Send reject event directly (for clearing completions)
function daemon.send_reject()
	if chan and chan > 0 then
		pcall(function()
			vim.fn.rpcnotify(chan, "cursortab_event", "esc")
		end)
	end
end

-- Send event immediately without debouncing (for critical events like insert_leave)
---@param event_name string
function daemon.send_event_immediate(event_name)
	send_rpc_event(event_name)
end

-- Check daemon process status
function daemon.check_daemon_status()
	local cfg = config.get()
	local state_dir = cfg.state_dir
	local socket_path = state_dir .. "/cursortab.sock"
	local pid_path = state_dir .. "/cursortab.pid"

	local status = {
		socket_exists = vim.fn.filereadable(socket_path) == 1,
		pid_file_exists = vim.fn.filereadable(pid_path) == 1,
		daemon_running = false,
		pid = nil,
	}

	-- Check if PID file exists and process is running
	local pid, running = read_daemon_pid(pid_path)
	if pid then
		status.pid = pid
		status.daemon_running = running
	end

	return status
end

-- Get channel status
function daemon.get_channel_status()
	return {
		connected = chan and chan > 0,
		channel_id = chan,
	}
end

-- Clean up stale socket and pid files
local function cleanup_stale_files()
	local cfg = config.get()
	local state_dir = cfg.state_dir
	local socket_path = state_dir .. "/cursortab.sock"
	local pid_path = state_dir .. "/cursortab.pid"
	local config_path = state_dir .. "/cursortab.config.json"

	-- Remove socket file if it exists
	if vim.fn.filereadable(socket_path) == 1 then
		vim.fn.delete(socket_path)
	end

	-- Remove pid file if it exists
	if vim.fn.filereadable(pid_path) == 1 then
		vim.fn.delete(pid_path)
	end

	-- Remove config file if it exists
	if vim.fn.filereadable(config_path) == 1 then
		vim.fn.delete(config_path)
	end
end

-- Stop daemon process
function daemon.stop_daemon()
	local cfg = config.get()
	local state_dir = cfg.state_dir
	local pid_path = state_dir .. "/cursortab.pid"
	local socket_path = state_dir .. "/cursortab.sock"

	-- Reset channel regardless of outcome
	chan = nil

	-- If no PID file, just clean up any stale socket
	if vim.fn.filereadable(pid_path) == 0 then
		if vim.fn.filereadable(socket_path) == 1 then
			vim.fn.delete(socket_path)
			return true, "Cleaned up stale socket (no PID file)"
		end
		return true, "Daemon not running (no PID file)"
	end

	local pid, running = read_daemon_pid(pid_path)
	if not pid then
		cleanup_stale_files()
		return true, "Cleaned up stale files (invalid PID)"
	end

	if not running then
		cleanup_stale_files()
		return true, "Cleaned up stale files (process not running)"
	end

	-- Send TERM signal to daemon
	vim.fn.system("kill " .. pid .. " 2>/dev/null")
	local kill_sent = vim.v.shell_error == 0

	if not kill_sent then
		cleanup_stale_files()
		return true, "Cleaned up stale files (could not signal process)"
	end

	-- Wait for socket to be removed (daemon cleanup)
	for _ = 1, 50 do
		vim.wait(100)
		if vim.fn.filereadable(socket_path) == 0 then
			return true, "Daemon stopped successfully"
		end
	end

	-- Process didn't terminate gracefully, send SIGKILL
	if is_process_running(pid) then
		vim.fn.system("kill -9 " .. pid .. " 2>/dev/null")
		-- Brief wait for forced termination
		vim.wait(100)
	end

	cleanup_stale_files()
	return true, "Daemon stopped (forced kill after timeout)"
end

-- Force start daemon (for use after stop_daemon)
function daemon.force_start()
	return start_daemon()
end

return daemon
