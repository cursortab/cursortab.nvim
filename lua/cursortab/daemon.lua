-- Daemon management and RPC communication for cursortab.nvim

local config = require("cursortab.config")
local buffer = require("cursortab.buffer")

local daemon = {}

-- Module state
---@type integer|nil
local chan = nil
-- State of this editor's session, reset on each connect: the PID of the daemon
-- once the session runs, or the error of a failed session init.
---@type integer|nil
local daemon_pid = nil
---@type string|nil
local session_error = nil
local ns_id = vim.api.nvim_create_namespace("cursortab")
local is_enabled = true

local is_windows = vim.fn.has("win32") == 1 or vim.fn.has("win64") == 1

local function get_binary_path()
	local plugin_dir = vim.fn.fnamemodify(debug.getinfo(1, "S").source:sub(2), ":h:h:h")
	return plugin_dir .. "/server/cursortab" .. (is_windows and ".exe" or "")
end

---@type string|nil
local binary_version = nil

-- Get the installed Go binary version, if available
---@return string|nil
function daemon.get_binary_version()
	if binary_version then
		return binary_version
	end
	local binary_path = get_binary_path()
	if vim.fn.executable(binary_path) == 0 then
		return nil
	end
	local output = vim.fn.system({ binary_path, "--version" })
	if vim.v.shell_error ~= 0 then
		return nil
	end
	binary_version = vim.trim(output)
	return binary_version
end

-- Settings of the daemon, used when this editor starts one. A daemon that
-- already runs keeps its own settings until it restarts.
---@param cfg CursortabConfig
local function daemon_config(cfg)
	return vim.json.encode({
		state_dir = cfg.state_dir,
		log_level = cfg.log_level,
		immediate_shutdown = cfg.debug.immediate_shutdown,
	})
end

local warned_missing_api_key = false

-- Settings of this editor's session, sent when it connects.
---@param cfg CursortabConfig
local function session_config(cfg)
	local v = vim.version()
	local api_key = ""
	if (cfg.provider.api_key_env or "") ~= "" then
		api_key = vim.env[cfg.provider.api_key_env] or ""
		if api_key == "" and not warned_missing_api_key then
			warned_missing_api_key = true
			vim.notify(
				"cursortab: api_key_env is set to " .. cfg.provider.api_key_env .. " but the variable is not defined",
				vim.log.levels.WARN
			)
		end
	end
	return vim.json.encode({
		ns_id = ns_id,
		editor_version = string.format("%d.%d.%d", v.major, v.minor, v.patch),
		editor_os = vim.uv.os_uname().sysname, ---@diagnostic disable-line: undefined-field
		contribute_data = cfg.contribute_data,
		behavior = {
			idle_completion_delay = cfg.behavior.idle_completion_delay,
			text_change_debounce = cfg.behavior.text_change_debounce,
			max_visible_lines = cfg.behavior.max_visible_lines,
			disabled_in = cfg.behavior.disabled_in,
			complete_in_insert = vim.tbl_contains(cfg.behavior.enabled_modes, "insert"),
			complete_in_normal = vim.tbl_contains(cfg.behavior.enabled_modes, "normal"),
			cursor_prediction = {
				enabled = cfg.behavior.cursor_prediction.enabled,
				auto_advance = cfg.behavior.cursor_prediction.auto_advance,
				proximity_threshold = cfg.behavior.cursor_prediction.proximity_threshold,
			},
		},
		provider = {
			type = cfg.provider.type,
			url = cfg.provider.url,
			api_key = api_key,
			model = cfg.provider.model,
			temperature = cfg.provider.temperature,
			context_size = cfg.provider.context_size,
			max_tokens = cfg.provider.max_tokens,
			top_k = cfg.provider.top_k,
			top_p = cfg.provider.top_p,
			completion_timeout = cfg.provider.completion_timeout,
			max_diff_history_tokens = cfg.provider.max_diff_history_tokens,
			completion_path = cfg.provider.completion_path,
			fim_tokens = cfg.provider.fim_tokens,
			privacy_mode = cfg.provider.privacy_mode,
		},
	})
end

-- Connections that end within this time count as failed reconnects.
local stable_connection_ms = 5000
local max_quick_failures = 3
local connected_at = 0
local quick_failures = 0

-- Set while a restart waits for the daemon to stop.
local restarting = false

-- Called once when the next session starts, dropped when it fails.
---@type fun()|nil
local on_started = nil

---@type fun()|nil
local on_disconnect = nil

local start

-- Reconnect after the daemon went away without this editor asking for it, for
-- example after a restart from another editor or a crash.
---@param stderr string Output of the relay that ended
local function reconnect(stderr)
	if vim.uv.now() - connected_at < stable_connection_ms then
		quick_failures = quick_failures + 1
	else
		quick_failures = 0
	end
	if quick_failures >= max_quick_failures then
		local detail = stderr ~= "" and (": " .. stderr) or ""
		vim.notify(
			"cursortab: lost the connection to the daemon" .. detail .. "\nRun :CursortabRestart",
			vim.log.levels.ERROR
		)
		return
	end
	vim.defer_fn(function()
		if not chan then
			start()
		end
	end, 200 * (quick_failures + 1))
end

-- Start the relay, which connects to the daemon and starts one when none
-- runs, and begin this editor's session. Nothing here waits for the daemon;
-- the daemon reports session errors with vim.notify.
---@return boolean
start = function()
	local binary_path = get_binary_path()
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

	local cfg = config.get()
	vim.fn.mkdir(cfg.state_dir, "p")
	local env = vim.fn.environ()
	env.CURSORTAB_CONFIG = daemon_config(cfg)

	local stderr = {}
	local relay
	relay = vim.fn.jobstart({ binary_path }, {
		rpc = true,
		env = env,
		on_stderr = function(_, data)
			vim.list_extend(stderr, data)
		end,
		on_exit = function()
			if chan ~= relay then
				return
			end
			chan = nil
			if on_disconnect then
				on_disconnect()
			end
			if vim.v.exiting ~= vim.NIL then
				return
			end
			if restarting then
				restarting = false
				quick_failures = 0
				start()
				return
			end
			reconnect(vim.trim(table.concat(stderr, "\n")))
		end,
	})
	if relay <= 0 then
		vim.notify("cursortab: could not start the daemon relay", vim.log.levels.ERROR)
		return false
	end
	chan = relay
	daemon_pid = nil
	session_error = nil
	connected_at = vim.uv.now()
	vim.rpcnotify(relay, "cursortab_init", session_config(cfg))
	daemon.send_event("buf_enter")
	return true
end

-- Public API

---@param event_name string
function daemon.send_event(event_name)
	if buffer.should_skip() or not is_enabled then
		return
	end

	-- Drop event if no valid channel (daemon starts on setup/restart, not here)
	if not chan or chan <= 0 then
		return
	end

	-- A failed notify means the relay is ending; its on_exit reconnects.
	pcall(vim.fn.rpcnotify, chan, "cursortab_event", event_name, buffer.position())
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
		pcall(vim.fn.rpcnotify, chan, "cursortab_event", "esc", buffer.position())
	end
end

-- Get channel status
function daemon.get_channel_status()
	return {
		connected = chan ~= nil,
		channel_id = chan,
		daemon_pid = daemon_pid,
		session_error = session_error,
	}
end

-- RPC callback: the session of this editor runs in the daemon with this PID.
---@param pid integer
function daemon.on_session_started(pid)
	daemon_pid = pid
	local done = on_started
	on_started = nil
	-- Return the RPC response first: the daemon waits for it.
	if done then
		vim.schedule(done)
	end
end

-- RPC callback: the daemon could not start the session of this editor. The
-- connection stays open so that it does not reconnect and fail again.
---@param message string
function daemon.on_session_error(message)
	session_error = message
	on_started = nil
	vim.notify("cursortab: " .. message, vim.log.levels.ERROR)
end

-- Called after the connection to the daemon ends.
---@param handler fun()
function daemon.set_disconnect_handler(handler)
	on_disconnect = handler
end

-- Connect to the daemon, starting it when it is not running.
function daemon.start()
	if not chan then
		start()
	end
end

-- Stop the daemon of every editor and connect to a new one, then call done
-- once the new session runs. Other editors reconnect on their own. A failed
-- start reports its error.
---@param done fun()
function daemon.restart(done)
	quick_failures = 0
	on_started = done
	if not chan then
		start()
		return
	end
	local relay = chan
	local pid = daemon_pid
	restarting = true
	pcall(vim.fn.rpcnotify, relay, "cursortab_shutdown")
	-- A daemon that does not stop is killed.
	vim.defer_fn(function()
		if chan ~= relay then
			return
		end
		if pid then
			vim.uv.kill(pid, "sigkill")
		end
		vim.fn.jobstop(relay)
	end, 5000)
end

return daemon
