---
name: config
description: Guidelines for adding, removing, or updating configuration options in cursortab.nvim. Use when modifying config fields, enum values, or validation logic.
---

## Design Principle

**Lua owns all default values.** The Go daemon receives the complete config via the `CURSORTAB_CONFIG` environment variable with all defaults already applied. Go structs should not have default values or use pointer types for optional fields - all fields are required and must be provided by Lua.

**Lua-only vs Go fields:** Some config sections are handled entirely in Lua and never sent to Go: `enabled`, `keymaps`, `ui`, `blink`. Conversely, Go's `Config` struct has auto-populated fields (`ns_id`, `editor_version`, `editor_os`) that Lua sets internally — these are not user-configurable and should not be added to `default_config`.

## Files to Update

When modifying config options, update these locations:

### 1. Lua Side

**`lua/cursortab/config.lua`**
- Type annotation in `---@class` block (e.g., `---@field new_option type`)
- Default value in `default_config` table (required - Go expects all values)
- Validation (if enum-like, add to `valid_*` table and update error in `validate_config`)
- Unknown keys are automatically rejected by `validate_config_keys()` — no update needed there unless changing the validation logic itself
- If adding/modifying default values for highlight groups, update `config.setup_highlights()` in config.lua

### 2. Go Side

**`server/main.go`** (only for fields consumed by the daemon — skip for Lua-only fields)
- Struct field with JSON tag in the appropriate config struct (`Config`, `ProviderConfig`, `BehaviorConfig`, etc.)
- No default values or optional fields - Lua provides the complete config
- Validation in `Config.Validate()` method — enum checks use the `validateEnum()` helper with `[]string` slices, numeric ranges use direct comparisons

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

- [ ] Add to Lua `valid_*` table in config.lua (e.g., `valid_log_levels`, `valid_provider_types`)
- [ ] Update Lua error message in `validate_config()` with new valid values
- [ ] Add to Go `validateEnum()` call in `Config.Validate()` in main.go
- [ ] Update README.md example/comments
- [ ] Update doc/cursortab.txt example/comments

For simple options:

- [ ] Add `---@field` type annotation in the appropriate `---@class` block in config.lua
- [ ] Add default value in `default_config` table in config.lua
- [ ] Add struct field with JSON tag in main.go (in `Config` or nested struct) — skip for Lua-only fields
- [ ] Add validation in `Config.Validate()` if needed (numeric ranges, path validation, etc.)
- [ ] If `ui.jump.*`, update `config.setup_highlights()` in config.lua
- [ ] Update README.md example
- [ ] Update doc/cursortab.txt example

For removing or renaming options:

- [ ] Add entry to `deprecated_mappings` table in config.lua (maps old flat key to new nested path, or `nil` if removed entirely)
- [ ] For nested field renames, add entry to `nested_field_renames` table in config.lua
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
// main.go - inside Config.Validate(), using validateEnum helper
if err := validateEnum(c.LogLevel, "log_level", []string{"trace", "debug", "info", "warn", "error"}); err != nil {
    return err
}
```

```go
// logger/logger.go - add constant, update String() and ParseLogLevel()
const LogLevelTrace LogLevel = iota
```

```markdown
<!-- README.md and doc/cursortab.txt -->
log_level = "info",  -- "trace", "debug", "info", "warn", "error"
```
