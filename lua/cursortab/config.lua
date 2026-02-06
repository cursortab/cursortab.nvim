-- Configuration management for cursortab.nvim

---@class CursortabUIColorsConfig
---@field deletion string
---@field addition string
---@field modification string
---@field completion string

---@class CursortabUIJumpConfig
---@field symbol string
---@field text string
---@field show_distance boolean
---@field bg_color string
---@field fg_color string

---@class CursortabUIConfig
---@field colors CursortabUIColorsConfig
---@field jump CursortabUIJumpConfig

---@class CursortabCursorPredictionConfig
---@field enabled boolean
---@field auto_advance boolean
---@field proximity_threshold integer

---@class CursortabBehaviorConfig
---@field idle_completion_delay integer
---@field text_change_debounce integer
---@field max_visible_lines integer Max visible lines per completion (0 to disable)
---@field cursor_prediction CursortabCursorPredictionConfig

---@class CursortabFIMTokensConfig
---@field prefix string FIM prefix token (e.g., "<|fim_prefix|>")
---@field suffix string FIM suffix token (e.g., "<|fim_suffix|>")
---@field middle string FIM middle token (e.g., "<|fim_middle|>")

---@class CursortabProviderConfig
---@field type string
---@field url string
---@field api_key_env string|nil Environment variable name containing the API key (e.g., "OPENAI_API_KEY")
---@field model string
---@field temperature number
---@field max_tokens integer Max tokens to generate (also used to derive input context size)
---@field top_k integer
---@field completion_timeout integer
---@field max_diff_history_tokens integer
---@field completion_path string API endpoint path (e.g., "/v1/completions")
---@field fim_tokens CursortabFIMTokensConfig|nil FIM tokens configuration (optional)
---@field privacy_mode boolean Enable privacy mode (don't send telemetry to provider)

---@class CursortabDebugConfig
---@field immediate_shutdown boolean

---@class CursortabKeymapsConfig
---@field accept string|false Accept keymap (e.g., "<Tab>"), or false to disable
---@field partial_accept string|false Partial accept keymap (e.g., "<S-Tab>"), or false to disable
---@field trigger string|false Trigger completion keymap (e.g., "<C-Space>"), or false to disable

---@class CursortabBlinkConfig
---@field enabled boolean
---@field ghost_text boolean

---@class CursortabConfig
---@field enabled boolean
---@field log_level string
---@field state_dir string Directory for runtime files (log, socket, pid)
---@field keymaps CursortabKeymapsConfig
---@field ui CursortabUIConfig
---@field behavior CursortabBehaviorConfig
---@field provider CursortabProviderConfig
---@field blink CursortabBlinkConfig
---@field debug CursortabDebugConfig

-- Default configuration
---@type CursortabConfig
local default_config = {
	enabled = true,
	log_level = "info",
	state_dir = vim.fn.stdpath("state") .. "/cursortab",

	keymaps = {
		accept = "<Tab>", -- Keymap to accept completion, or false to disable
		partial_accept = "<S-Tab>", -- Keymap to partially accept completion, or false to disable
		trigger = false, -- Keymap to manually trigger completion, or false to disable (default: false)
	},

	ui = {
		colors = {
			deletion = "#4f2f2f",
			addition = "#394f2f",
			modification = "#282e38",
			completion = "#80899c",
		},
		jump = {
			symbol = "",
			text = " TAB ",
			show_distance = true,
			bg_color = "#373b45",
			fg_color = "#bac1d1",
		},
	},

	behavior = {
		idle_completion_delay = 50, -- Delay in ms after being idle in normal mode to trigger completion (-1 to disable)
		text_change_debounce = 50, -- Debounce in ms after text changed to trigger completion
		max_visible_lines = 12, -- Max visible lines per completion (0 to disable)
		cursor_prediction = {
			enabled = true, -- Show jump indicators after completions
			auto_advance = true, -- When completion has no changes, show cursor jump to last line
			proximity_threshold = 2, -- Min lines apart to show cursor jump between completions (0 to disable)
		},
	},

	provider = {
		type = "inline", -- "inline", "fim", "sweep", "sweepapi", "zeta", "copilot", or "mercuryapi"
		url = "http://localhost:8000", -- URL of the provider server
		api_key_env = "", -- Environment variable name for API key (e.g., "OPENAI_API_KEY")
		model = "", -- Model name
		temperature = 0.0, -- Sampling temperature
		max_tokens = 512, -- Max tokens to generate
		top_k = 50, -- Top-k sampling
		completion_timeout = 5000, -- Timeout in ms for completion requests
		max_diff_history_tokens = 512, -- Max tokens for diff history (0 = no limit)
		completion_path = "/v1/completions", -- API endpoint path
		fim_tokens = { -- FIM tokens (for FIM provider)
			prefix = "<|fim_prefix|>",
			suffix = "<|fim_suffix|>",
			middle = "<|fim_middle|>",
		},
		privacy_mode = true, -- Don't send telemetry to provider
	},

	blink = {
		enabled = false,
		ghost_text = true,
	},

	debug = {
		immediate_shutdown = false, -- Shutdown daemon immediately when no clients are connected
	},
}

-- Deprecated field mappings (old flat field -> new nested path)
-- A nil value means the option was removed entirely
-- Example: old_field = { "new", "nested", "path" }
-- Example: removed_field = nil
local deprecated_mappings = {}

-- Nested field renames (old nested field -> new field name within same parent)
-- Format: { path = { "path", "to", "parent" }, old = "old_field", new = "new_field" }
-- Example: { path = { "behavior", "cursor_prediction" }, old = "dist_threshold", new = "proximity_threshold" }
local nested_field_renames = {}

-- Migrate deprecated flat config to new nested structure
---@param user_config table
---@return table
local function migrate_deprecated_config(user_config)
	local migrated = vim.deepcopy(user_config)
	local deprecated_keys = {}
	local removed_keys = {}

	for old_key, new_path in pairs(deprecated_mappings) do
		if migrated[old_key] ~= nil then
			-- Skip if key exists in new format (e.g., provider = { type = ... } is new, provider = "inline" is old)
			-- When new_path[1] == old_key, the new format uses a table at that key
			if new_path and new_path[1] == old_key and type(migrated[old_key]) == "table" then
				goto continue
			end

			if new_path == nil then
				-- Option was removed entirely
				table.insert(removed_keys, old_key)
			else
				-- Option was moved to new location
				table.insert(deprecated_keys, old_key)
				-- Navigate to the nested location and set the value
				local target = migrated
				for i = 1, #new_path - 1 do
					local key = new_path[i]
					-- Create table if nil or if it's not a table (e.g., old "provider" string)
					if target[key] == nil or type(target[key]) ~= "table" then
						target[key] = {}
					end
					target = target[key]
				end
				target[new_path[#new_path]] = migrated[old_key]
			end
			migrated[old_key] = nil

			::continue::
		end
	end

	if #deprecated_keys > 0 then
		vim.schedule(function()
			vim.notify(
				"[cursortab.nvim] Deprecated config keys detected: "
					.. table.concat(deprecated_keys, ", ")
					.. "\nPlease migrate to the new nested structure. See :help cursortab-config",
				vim.log.levels.WARN
			)
		end)
	end

	if #removed_keys > 0 then
		vim.schedule(function()
			vim.notify(
				"[cursortab.nvim] Removed config keys detected: "
					.. table.concat(removed_keys, ", ")
					.. "\nThese options no longer have any effect.",
				vim.log.levels.WARN
			)
		end)
	end

	-- Handle nested field renames
	local renamed_fields = {}
	for _, rename in ipairs(nested_field_renames) do
		-- Navigate to the parent table
		local parent = migrated
		local found = true
		for _, key in ipairs(rename.path) do
			if parent[key] == nil or type(parent[key]) ~= "table" then
				found = false
				break
			end
			parent = parent[key]
		end

		-- If parent exists and has the old field, rename it
		if found and parent[rename.old] ~= nil then
			parent[rename.new] = parent[rename.old]
			parent[rename.old] = nil
			table.insert(renamed_fields, rename.old .. " -> " .. rename.new)
		end
	end

	if #renamed_fields > 0 then
		vim.schedule(function()
			vim.notify(
				"[cursortab.nvim] Renamed config fields detected: "
					.. table.concat(renamed_fields, ", ")
					.. "\nPlease update your config. See :help cursortab-config",
				vim.log.levels.WARN
			)
		end)
	end

	return migrated
end

-- Valid values for enum-like config options
local valid_provider_types = { inline = true, fim = true, sweep = true, sweepapi = true, zeta = true, copilot = true, mercuryapi = true }
local valid_log_levels = { trace = true, debug = true, info = true, warn = true, error = true }

-- Validate that all keys in user config exist in default config
---@param user_cfg table User configuration
---@param default_cfg table Default configuration
---@param path string Current path for error messages
local function validate_config_keys(user_cfg, default_cfg, path)
	for key, value in pairs(user_cfg) do
		if default_cfg[key] == nil then
			error(string.format("[cursortab.nvim] Unknown config option: %s%s", path, key))
		end
		-- Recursively validate nested tables
		if type(value) == "table" and type(default_cfg[key]) == "table" then
			validate_config_keys(value, default_cfg[key], path .. key .. ".")
		end
	end
end

-- Validate configuration values
---@param cfg table
local function validate_config(cfg)
	-- First, validate that all keys are recognized
	validate_config_keys(cfg, default_config, "")
	-- Validate keymaps.accept (must be string or false)
	if cfg.keymaps and cfg.keymaps.accept ~= nil then
		local accept = cfg.keymaps.accept
		if accept ~= false and type(accept) ~= "string" then
			error("[cursortab.nvim] keymaps.accept must be a string (keymap) or false to disable")
		end
		if type(accept) == "string" and accept == "" then
			error("[cursortab.nvim] keymaps.accept cannot be an empty string (use false to disable)")
		end
	end

	-- Validate provider type
	if cfg.provider and cfg.provider.type then
		if not valid_provider_types[cfg.provider.type] then
			error(string.format(
				"[cursortab.nvim] Invalid provider.type '%s'. Must be one of: inline, fim, sweep, sweepapi, zeta, copilot, mercuryapi",
				cfg.provider.type
			))
		end
	end

	-- Validate log level
	if cfg.log_level and not valid_log_levels[cfg.log_level] then
		error(string.format(
			"[cursortab.nvim] Invalid log_level '%s'. Must be one of: trace, debug, info, warn, error",
			cfg.log_level
		))
	end

	-- Validate numeric ranges
	if cfg.behavior then
		if cfg.behavior.idle_completion_delay and cfg.behavior.idle_completion_delay < -1 then
			error("[cursortab.nvim] behavior.idle_completion_delay must be >= -1")
		end
		if cfg.behavior.text_change_debounce and cfg.behavior.text_change_debounce < -1 then
			error("[cursortab.nvim] behavior.text_change_debounce must be >= -1 (-1 to disable)")
		end
		if cfg.behavior.max_visible_lines and cfg.behavior.max_visible_lines < 0 then
			error("[cursortab.nvim] behavior.max_visible_lines must be >= 0 (0 to disable)")
		end
	end

	if cfg.provider then
		if cfg.provider.max_tokens and cfg.provider.max_tokens < 0 then
			error("[cursortab.nvim] provider.max_tokens must be >= 0")
		end
		if cfg.provider.completion_timeout and cfg.provider.completion_timeout < 0 then
			error("[cursortab.nvim] provider.completion_timeout must be >= 0")
		end
		if cfg.provider.max_diff_history_tokens and cfg.provider.max_diff_history_tokens < 0 then
			error("[cursortab.nvim] provider.max_diff_history_tokens must be >= 0")
		end
		if cfg.provider.completion_path and not cfg.provider.completion_path:match("^/") then
			error("[cursortab.nvim] provider.completion_path must start with '/'")
		end
		if cfg.provider.fim_tokens ~= nil then
			if type(cfg.provider.fim_tokens) ~= "table" then
				error("[cursortab.nvim] provider.fim_tokens must be a table with prefix, suffix, and middle fields")
			end
			local required_fields = { "prefix", "suffix", "middle" }
			for _, field in ipairs(required_fields) do
				local value = cfg.provider.fim_tokens[field]
				if value == nil or type(value) ~= "string" or value == "" then
					error(string.format(
						"[cursortab.nvim] provider.fim_tokens.%s is required and must be a non-empty string",
						field
					))
				end
			end
		end
	end
end

---@class ConfigModule
local config = {}
---@type CursortabConfig
local current_config = vim.deepcopy(default_config)

-- Get current configuration
---@return CursortabConfig
function config.get()
	return current_config
end

-- Set up configuration with user overrides
---@param user_config table|nil User configuration overrides
---@return CursortabConfig
function config.setup(user_config)
	local migrated = migrate_deprecated_config(user_config or {})
	validate_config(migrated)
	current_config = vim.tbl_deep_extend("force", vim.deepcopy(default_config), migrated)
	return current_config
end

-- Set up highlight groups based on current configuration
function config.setup_highlights()
	---@type CursortabConfig
	local cfg = current_config

	vim.api.nvim_set_hl(0, "cursortabhl_deletion", {
		ctermbg = "DarkRed",
		bg = cfg.ui.colors.deletion,
		bold = false,
	})

	vim.api.nvim_set_hl(0, "cursortabhl_addition", {
		ctermbg = "DarkGreen",
		bg = cfg.ui.colors.addition,
		bold = false,
	})

	vim.api.nvim_set_hl(0, "cursortabhl_modification", {
		ctermbg = "DarkGray",
		bg = cfg.ui.colors.modification,
		bold = false,
	})

	vim.api.nvim_set_hl(0, "cursortabhl_completion", {
		ctermfg = "DarkBlue",
		fg = cfg.ui.colors.completion,
		bold = false,
	})

	vim.api.nvim_set_hl(0, "cursortabhl_jump_symbol", {
		ctermfg = "Cyan",
		fg = cfg.ui.jump.bg_color,
		bold = false,
	})

	vim.api.nvim_set_hl(0, "cursortabhl_jump_text", {
		ctermbg = "Cyan",
		ctermfg = "Black",
		bg = cfg.ui.jump.bg_color,
		fg = cfg.ui.jump.fg_color,
		bold = false,
	})
end

return config
