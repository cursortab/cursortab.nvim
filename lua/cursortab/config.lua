-- Configuration management for cursortab.nvim

---@class CursortabUIJumpConfig
---@field symbol string
---@field text string
---@field show_distance boolean

---@class CursortabUICompletionsConfig
---@field addition_style string "dimmed" or "highlight"
---@field fg_opacity number opacity for completion overlays (0=invisible, 1=fully visible)

---@class CursortabUIConfig
---@field completions CursortabUICompletionsConfig
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
---@field disabled_in string[] Tree-sitter scopes where completions are suppressed (e.g., "comment", "string")
---@field ignore_paths string[] Glob patterns for files to skip (gitignore-style)
---@field ignore_filetypes string[] Filetypes to skip completions
---@field ignore_gitignored boolean Skip files matched by .gitignore
---@field enabled_modes string[] Modes where completions are active ("insert", "normal")

---@class CursortabFIMTokensConfig
---@field prefix string FIM prefix token (e.g., "<|fim_prefix|>")
---@field suffix string FIM suffix token (e.g., "<|fim_suffix|>")
---@field middle string FIM middle token (e.g., "<|fim_middle|>")
---@field repo_name string Optional repo-level FIM token (e.g., "<|repo_name|>"); enables cross-file context
---@field file_sep string Optional file separator token (e.g., "<|file_sep|>"); enables cross-file context

---@class CursortabProviderConfig
---@field type string
---@field url string
---@field api_key_env string|nil Environment variable name containing the API key (e.g., "OPENAI_API_KEY")
---@field model string
---@field temperature number
---@field context_size integer Max input context size in tokens (used for input trimming; 0 = use max_tokens)
---@field max_tokens integer Max tokens to generate
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
---@field contribute_data boolean Opt-in: send anonymous completion metrics to the public dataset for model training
---@field provider CursortabProviderConfig
---@field blink CursortabBlinkConfig
---@field debug CursortabDebugConfig

-- Default configuration
---@type CursortabConfig
local default_config = {
	enabled = true,
	log_level = "info",
	state_dir = vim.fn.stdpath("state") .. "/cursortab",
	contribute_data = false, -- Opt-in: send anonymous metrics to train a better gating model

	keymaps = {
		accept = "<Tab>", -- Keymap to accept completion, or false to disable
		partial_accept = "<S-Tab>", -- Keymap to partially accept completion, or false to disable
		trigger = false, -- Keymap to manually trigger completion, or false to disable (default: false)
	},

	ui = {
		completions = {
			addition_style = "dimmed", -- "dimmed" or "highlight"
			fg_opacity = 0.6, -- opacity for completion overlays (0=invisible, 1=fully visible)
		},
		jump = {
			symbol = "",
			text = " TAB ",
			show_distance = true,
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
		disabled_in = {}, -- Tree-sitter scopes where completions are suppressed (e.g., "comment", "string")
		enabled_modes = { "insert", "normal" }, -- Modes where completions are active
		ignore_paths = { -- Glob patterns for files to skip completions
			"*.min.js",
			"*.min.css",
			"*.map",
			"*-lock.json",
			"*.lock",
			"*.sum",
			"*.csv",
			"*.tsv",
			"*.parquet",
			"*.zip",
			"*.tar",
			"*.gz",
			"*.pem",
			"*.key",
			".env",
			".env.*",
			"*.log",
		},
		ignore_filetypes = { "", "terminal" }, -- Filetypes to skip completions
		ignore_gitignored = true, -- Skip files matched by .gitignore
	},

	provider = {
		type = "inline", -- "inline", "fim", "sweep", "zeta-2", "zeta", "copilot", or "mercuryapi"
		url = "http://localhost:8000", -- URL of the provider server
		api_key_env = "", -- Environment variable name for API key (e.g., "OPENAI_API_KEY")
		model = "", -- Model name
		temperature = 0.0, -- Sampling temperature
		context_size = 0, -- Max input context size in tokens (0 = use max_tokens)
		max_tokens = 512, -- Max tokens to generate
		top_k = 50, -- Top-k sampling
		completion_timeout = 5000, -- Timeout in ms for completion requests
		max_diff_history_tokens = 512, -- Max tokens for diff history (0 = no limit)
		completion_path = "/v1/completions", -- API endpoint path
		fim_tokens = { -- FIM tokens (for FIM provider)
			prefix = "<|fim_prefix|>",
			suffix = "<|fim_suffix|>",
			middle = "<|fim_middle|>",
			repo_name = "", -- Optional: "<|repo_name|>" enables repo-level cross-file context
			file_sep = "", -- Optional: "<|file_sep|>" enables repo-level cross-file context
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
local valid_provider_types = { inline = true, fim = true, sweep = true, ["zeta-2"] = true, zeta = true, copilot = true, mercuryapi = true }
local valid_log_levels = { trace = true, debug = true, info = true, warn = true, error = true }
local valid_addition_styles = { dimmed = true, highlight = true }

-- Validate that all keys in user config exist in default config
---@param user_cfg table User configuration
---@param default_cfg table Default configuration
---@param path string Current path for error messages
local function validate_config_keys(user_cfg, default_cfg, path)
	for key, value in pairs(user_cfg) do
		if default_cfg[key] == nil then
			error(string.format("[cursortab.nvim] Unknown config option: %s%s", path, key))
		end
		-- Recursively validate nested tables (skip lists with numeric keys)
		if type(value) == "table" and type(default_cfg[key]) == "table" and next(value) ~= nil and type(next(value)) ~= "number" then
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
				"[cursortab.nvim] Invalid provider.type '%s'. Must be one of: inline, fim, sweep, zeta-2, zeta, copilot, mercuryapi",
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

	-- Validate addition style
	if cfg.ui and cfg.ui.completions and cfg.ui.completions.addition_style then
		if not valid_addition_styles[cfg.ui.completions.addition_style] then
			error(string.format(
				"[cursortab.nvim] Invalid ui.completions.addition_style '%s'. Must be one of: dimmed, highlight",
				cfg.ui.completions.addition_style
			))
		end
	end

	-- Validate fg_opacity
	if cfg.ui and cfg.ui.completions and cfg.ui.completions.fg_opacity then
		local f = cfg.ui.completions.fg_opacity
		if type(f) ~= "number" or f < 0 or f > 1 then
			error("[cursortab.nvim] ui.completions.fg_opacity must be a number between 0 and 1")
		end
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
		if cfg.behavior.enabled_modes ~= nil then
			if type(cfg.behavior.enabled_modes) ~= "table" then
				error("[cursortab.nvim] behavior.enabled_modes must be a list (e.g., { \"insert\", \"normal\" })")
			end
			local valid_modes = { insert = true, normal = true }
			for i, mode in ipairs(cfg.behavior.enabled_modes) do
				if type(mode) ~= "string" or not valid_modes[mode] then
					error(string.format(
						"[cursortab.nvim] behavior.enabled_modes[%d] = %q is invalid. Must be \"insert\" or \"normal\"",
						i,
						tostring(mode)
					))
				end
			end
		end
		if cfg.behavior.ignore_paths ~= nil then
			if type(cfg.behavior.ignore_paths) ~= "table" then
				error("[cursortab.nvim] behavior.ignore_paths must be a list of glob pattern strings")
			end
			for i, pattern in ipairs(cfg.behavior.ignore_paths) do
				if type(pattern) ~= "string" then
					error(string.format("[cursortab.nvim] behavior.ignore_paths[%d] must be a string", i))
				end
			end
		end
		if cfg.behavior.ignore_filetypes ~= nil then
			if type(cfg.behavior.ignore_filetypes) ~= "table" then
				error("[cursortab.nvim] behavior.ignore_filetypes must be a list of filetype strings")
			end
			for i, ft in ipairs(cfg.behavior.ignore_filetypes) do
				if type(ft) ~= "string" then
					error(string.format("[cursortab.nvim] behavior.ignore_filetypes[%d] must be a string", i))
				end
			end
		end
		if cfg.behavior.ignore_gitignored ~= nil and type(cfg.behavior.ignore_gitignored) ~= "boolean" then
			error("[cursortab.nvim] behavior.ignore_gitignored must be a boolean")
		end
	end

	if cfg.provider then
		if cfg.provider.context_size and cfg.provider.context_size < 0 then
			error("[cursortab.nvim] provider.context_size must be >= 0")
		end
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
			local optional_fields = { "repo_name", "file_sep" }
			for _, field in ipairs(optional_fields) do
				local value = cfg.provider.fim_tokens[field]
				if value ~= nil and type(value) ~= "string" then
					error(string.format(
						"[cursortab.nvim] provider.fim_tokens.%s must be a string",
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

	-- Apply per-provider defaults when the user hasn't explicitly set them
	local p = current_config.provider
	local user_p = migrated.provider or {}
	local provider_defaults = {
		inline = { context_size = 1024, max_tokens = 64 },
		fim = { context_size = 1024, max_tokens = 128 },
	}
	local defaults = provider_defaults[p.type]
	if defaults then
		for key, value in pairs(defaults) do
			if user_p[key] == nil then
				p[key] = value
			end
		end
	end

	-- Auto-detect repo-level FIM tokens from model name when not explicitly set
	local user_fim = (user_p.fim_tokens or {})
	if user_fim.repo_name == nil and user_fim.file_sep == nil then
		local model_lower = (p.model or ""):lower()
		-- Qwen family (Qwen2.5-Coder, Qwen3.5) and Zeta (Qwen2.5-Coder based)
		if model_lower:match("qwen") or (p.type == "zeta") then
			p.fim_tokens.repo_name = "<|repo_name|>"
			p.fim_tokens.file_sep = "<|file_sep|>"
		end
	end

	return current_config
end

-- Set up default values for highlight groups
function config.setup_highlights()
	vim.api.nvim_set_hl(0, "CursorTabDeletion", {
		default = true,
		ctermbg = "DarkRed",
		bg = "#4f2f2f",
		bold = false,
	})

	vim.api.nvim_set_hl(0, "CursorTabAddition", {
		default = true,
		ctermbg = "DarkGreen",
		bg = "#394f2f",
		bold = false,
	})

	vim.api.nvim_set_hl(0, "CursorTabModification", {
		default = true,
		ctermbg = "DarkGray",
		bg = "#282e38",
		bold = false,
	})

	vim.api.nvim_set_hl(0, "CursorTabCompletion", {
		default = true,
		ctermfg = "DarkBlue",
		fg = "#80899c",
		bold = false,
	})

	vim.api.nvim_set_hl(0, "CursorTabJumpSymbol", {
		default = true,
		ctermfg = "Cyan",
		fg = "#373b45",
		bold = false,
	})

	vim.api.nvim_set_hl(0, "CursorTabJumpText", {
		default = true,
		ctermbg = "Cyan",
		ctermfg = "Black",
		bg = "#373b45",
		fg = "#bac1d1",
		bold = false,
	})
end

return config
