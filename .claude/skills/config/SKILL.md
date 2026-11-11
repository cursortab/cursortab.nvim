---
name: config
description:
  Guidelines for adding, removing, or updating configuration options in
  cursortab.nvim. Use when modifying config fields, enum values, or validation
  logic.
---

## Design Principle

**Lua owns all default values.** Go receives complete configs with all defaults
already applied. Go structs should not have default values or use pointer types
for optional fields - all fields are required and must be provided by Lua.

Go receives two configs, both built in `lua/cursortab/daemon.lua`:

- **Daemon config** (`daemon_config()`): process-wide settings (`state_dir`,
  `log_level`, `debug.immediate_shutdown`). Sent via the `CURSORTAB_CONFIG`
  environment variable when the daemon starts. Go struct: `DaemonConfig` in
  `server/main.go`.
- **Session config** (`session_config()`): per-editor settings (`behavior`,
  `provider`, `contribute_data`). Sent with the `cursortab_init` RPC notification
  when each Neovim connects, so each instance keeps its own settings. Go
  structs: `SessionConfig`, `BehaviorConfig`, `ProviderConfig` in
  `server/session.go`.

**Lua-only vs Go fields:** Some config sections are handled entirely in Lua and
never sent to Go: `enabled`, `keymaps`, `ui`, `blink`. `SessionConfig` also has
auto-populated fields (`ns_id`, `editor_version`, `editor_os`) and the resolved
`provider.api_key` (read from `api_key_env`) that Lua sets internally. These are
not user-configurable and should not be added to `default_config`.

## Files to Update

When modifying config options, update these locations:

### 1. Lua Side

**`lua/cursortab/config.lua`**

- Type annotation in `---@class` block (e.g., `---@field new_option type`)
- Default value in `default_config` table (required - Go expects all values)
- Validation (if enum-like, add to `valid_*` table and update error in
  `validate_config`)
- Unknown keys are automatically rejected by `validate_config_keys()` - no
  update needed there unless changing the validation logic itself
- If adding/modifying default values for highlight groups, update
  `config.setup_highlights()` in config.lua

### 2. Go Side

**`lua/cursortab/daemon.lua`** (only for fields consumed by Go)

- Add the field to `daemon_config()` or `session_config()`

**`server/session.go`** (session fields) or **`server/main.go`** (daemon fields)

- Struct field with JSON tag in the appropriate config struct (`SessionConfig`,
  `BehaviorConfig`, `ProviderConfig`, or `DaemonConfig`)
- No default values or optional fields - Lua provides the complete config
- Validation in `SessionConfig.Validate()` (table of range checks) or
  `DaemonConfig.Validate()` (enum checks use the `validateEnum()` helper)
- Session fields consumed by the engine or providers must also be mapped in
  `newSessionEngine()`

**`server/logger/logger.go`** (for log levels only)

- `LogLevel` constants (`LogLevelTrace`, `LogLevelDebug`, etc.)
- `String()` method switch case
- `ParseLogLevel()` function switch case

### 3. Documentation

**`README.md`**

- Configuration example in the setup block
- Add comment showing valid values for enum options

**`doc/cursortab.txt`**

- Vim help file with same configuration example
- Keep in sync with README.md

## Checklist

For enum-like options (e.g., log_level, provider.type):

- [ ] Add to Lua `valid_*` table in config.lua (e.g., `valid_log_levels`,
      `valid_provider_types`)
- [ ] Update Lua error message in `validate_config()` with new valid values
- [ ] Add to the Go validation: `validateEnum()` in `DaemonConfig.Validate()`
      (main.go), or the `newSessionEngine()` provider switch in session.go for
      `provider.type`
- [ ] Update README.md example/comments
- [ ] Update doc/cursortab.txt example/comments

For simple options:

- [ ] Add `---@field` type annotation in the appropriate `---@class` block in
      config.lua
- [ ] Add default value in `default_config` table in config.lua
- [ ] Add the field to `session_config()` or `daemon_config()` in daemon.lua -
      skip for Lua-only fields
- [ ] Add struct field with JSON tag in session.go or main.go - skip for
      Lua-only fields
- [ ] Add validation in `SessionConfig.Validate()` or `DaemonConfig.Validate()`
      if needed (numeric ranges, path validation, etc.)
- [ ] If `ui.jump.*`, update `config.setup_highlights()` in config.lua
- [ ] Update README.md example
- [ ] Update doc/cursortab.txt example

For removing or renaming options:

- [ ] Add entry to `deprecated_mappings` table in config.lua (maps old flat key
      to new nested path, or `nil` if removed entirely)
- [ ] For nested field renames, add entry to `nested_field_renames` table in
      config.lua
- [ ] Remove old field from `default_config`, `---@class` blocks, and Go structs
- [ ] Update validation logic in both Lua and Go
- [ ] Update README.md and doc/cursortab.txt

## Example: Adding a new enum value

When adding "trace" to log_level:

```lua
-- config.lua
local valid_log_levels = { trace = true, debug = true, info = true, warn = true, error = true }

-- In validate_config():
-- error: "Must be one of: trace, debug, info, warn, error"
```

```go
// main.go - inside DaemonConfig.Validate(), using validateEnum helper
return validateEnum(c.LogLevel, "log_level", []string{"trace", "debug", "info", "warn", "error"})
```

```go
// logger/logger.go - add constant, update String() and ParseLogLevel()
const LogLevelTrace LogLevel = iota
```

```markdown
<!-- README.md and doc/cursortab.txt -->

log_level = "info", -- "trace", "debug", "info", "warn", "error"
```
