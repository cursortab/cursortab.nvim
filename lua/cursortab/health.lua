local config = require("cursortab.config")
local daemon = require("cursortab.daemon")

local M = {}

function M.check()
	local cfg = config.get()
	local daemon_status = daemon.check_daemon_status()
	local channel_status = daemon.get_channel_status()

	-- Identity
	vim.health.start("Identity")
	local nv = vim.version()
	vim.health.info(
		"neovim: "
			.. string.format("%d.%d.%d", nv.major, nv.minor, nv.patch)
			.. " ("
			.. vim.uv.os_uname().sysname ---@diagnostic disable-line: undefined-field
			.. ")"
	)

	local device_id_path = cfg.state_dir .. "/device_id"
	if vim.fn.filereadable(device_id_path) == 1 then
		local did = table.concat(vim.fn.readfile(device_id_path), "")
		vim.health.info("device_id: " .. vim.trim(did))
	else
		vim.health.info("device_id: not yet created")
	end

	-- Daemon
	vim.health.start("Daemon")
	if not daemon.is_enabled() then
		vim.health.warn("Plugin is disabled")
	elseif daemon_status.daemon_running and channel_status.connected then
		vim.health.ok("Running (pid: " .. daemon_status.pid .. ", channel: " .. channel_status.channel_id .. ")")
	elseif daemon_status.daemon_running then
		vim.health.warn("Process running (pid: " .. daemon_status.pid .. ") but not connected")
	else
		vim.health.error("Not running", { "Run :CursortabRestart to start the daemon" })
	end

	-- Provider
	vim.health.start("Provider")
	vim.health.info("type: " .. cfg.provider.type)
	vim.health.info("model: " .. (cfg.provider.model ~= "" and cfg.provider.model or "-"))
	vim.health.info("url: " .. cfg.provider.url)
	vim.health.info("api_key_env: " .. (cfg.provider.api_key_env ~= "" and cfg.provider.api_key_env or "-"))
	vim.health.info("timeout: " .. cfg.provider.completion_timeout .. "ms")
	vim.health.info("max_tokens: " .. cfg.provider.max_tokens)
	vim.health.info("temperature: " .. cfg.provider.temperature)
	vim.health.info("top_k: " .. cfg.provider.top_k)
	vim.health.info("max_diff_history_tokens: " .. cfg.provider.max_diff_history_tokens)
	vim.health.info("completion_path: " .. cfg.provider.completion_path)
	vim.health.info("privacy_mode: " .. (cfg.provider.privacy_mode and "yes" or "no"))

	if cfg.provider.api_key_env ~= "" then
		local key = vim.fn.getenv(cfg.provider.api_key_env)
		if key == vim.NIL or key == "" then
			vim.health.error(cfg.provider.api_key_env .. " is not set", {
				"Export " .. cfg.provider.api_key_env .. " in your shell config",
				"Run :CursortabRestart after setting it",
			})
		else
			vim.health.ok(cfg.provider.api_key_env .. " is set")
		end
	end

	if cfg.provider.type == "windsurf" then
		vim.health.start("Windsurf")
		local ok, codeium = pcall(require, 'codeium')
		if not ok then
			vim.health.error("codeium plugin not installed", { "Install windsurf.nvim (codeium) plugin" })
		elseif not codeium.s then
			vim.health.error("codeium server not initialized", { "Run :Codeium Auth to authenticate" })
		elseif not codeium.s.healthy then
			vim.health.warn("codeium server not healthy")
		else
			vim.health.ok("codeium server running (port: " .. codeium.s.port .. ")")
		end
	end

	-- Behavior
	vim.health.start("Behavior")
	vim.health.info("idle_delay: " .. cfg.behavior.idle_completion_delay .. "ms")
	vim.health.info("debounce: " .. cfg.behavior.text_change_debounce .. "ms")
	vim.health.info("max_visible_lines: " .. cfg.behavior.max_visible_lines)
	vim.health.info("cursor_prediction: " .. (cfg.behavior.cursor_prediction.enabled and "yes" or "no"))
	vim.health.info("auto_advance: " .. (cfg.behavior.cursor_prediction.auto_advance and "yes" or "no"))
	vim.health.info("proximity_threshold: " .. cfg.behavior.cursor_prediction.proximity_threshold)
	vim.health.info("enabled_modes: " .. table.concat(cfg.behavior.enabled_modes, ", "))
	vim.health.info("ignore_paths: " .. #cfg.behavior.ignore_paths .. " patterns")
	vim.health.info("ignore_filetypes: " .. #cfg.behavior.ignore_filetypes .. " filetypes")
	vim.health.info("ignore_gitignored: " .. (cfg.behavior.ignore_gitignored and "yes" or "no"))

	-- Keymaps
	vim.health.start("Keymaps")
	vim.health.info("accept: " .. (cfg.keymaps.accept or "disabled"))
	vim.health.info("partial_accept: " .. (cfg.keymaps.partial_accept or "disabled"))
	vim.health.info("trigger: " .. (cfg.keymaps.trigger or "disabled"))

	-- Blink
	vim.health.start("Blink")
	vim.health.info("enabled: " .. (cfg.blink.enabled and "yes" or "no"))
	vim.health.info("ghost_text: " .. (cfg.blink.ghost_text and "yes" or "no"))

	-- UI
	vim.health.start("UI")
	vim.health.info("addition_style: " .. cfg.ui.completions.addition_style)
	vim.health.info("fg_opacity: " .. cfg.ui.completions.fg_opacity)
	vim.health.info("jump_symbol: " .. cfg.ui.jump.symbol)
	vim.health.info("jump_text: " .. cfg.ui.jump.text)
	vim.health.info("jump_show_distance: " .. (cfg.ui.jump.show_distance and "yes" or "no"))

	-- FIM Tokens
	vim.health.start("FIM Tokens")
	vim.health.info("prefix: " .. cfg.provider.fim_tokens.prefix)
	vim.health.info("suffix: " .. cfg.provider.fim_tokens.suffix)
	vim.health.info("middle: " .. cfg.provider.fim_tokens.middle)

	-- Debug
	vim.health.start("Debug")
	vim.health.info("immediate_shutdown: " .. (cfg.debug.immediate_shutdown and "yes" or "no"))

	-- Paths
	vim.health.start("Paths")
	vim.health.info("enabled: " .. (cfg.enabled and "yes" or "no"))
	vim.health.info("contribute_data: " .. (cfg.contribute_data and "yes" or "no"))
	vim.health.info("state_dir: " .. cfg.state_dir)
	vim.health.info("log_level: " .. cfg.log_level)
end

return M
