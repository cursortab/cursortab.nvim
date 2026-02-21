# AGENTS.md

## Build & Test Commands

```bash
# Build the Go server
cd server && go build

# Run all tests
cd server && go test ./...

# Run tests for a specific package
cd server && go test ./text/...

# Run a single test
cd server && go test ./text/... -run TestDiff

# Check for dead code
cd server && deadcode .

# Run E2E pipeline tests (ComputeDiff → CreateStages → ToLuaFormat)
cd server && go test ./text/... -run TestE2E -v

# Record new expected output after changes
cd server && go test ./text/... -run TestE2E -update
```

Each E2E fixture is a directory under `server/text/e2e/` with `old.txt`,
`new.txt`, `params.json`, and `expected.json`. Both batch and incremental
pipelines are verified against the same expected output. An HTML report is
generated at `server/text/e2e/report.html`.

**Important:** Never run `-verify` or `-verify-case`. Verification must always
be done manually by the user.

When done with your changes run formatting with:

```bash
gofmt -w .
```

## Code Style

### No Legacy Code or Backward Compatibility

When refactoring or modifying code, completely remove old implementations. DO
NOT:

- Keep deprecated functions or methods
- Add backward-compatible shims or wrappers
- Leave commented-out old code
- Add comments explaining what changed from the old version
- Rename unused parameters with underscore prefixes

Treat the new code as if it was always the correct implementation.

## Bug Investigation

When working on bugs, follow this process:

1. **Trace logs with code** - If logs are provided, go line by line through the
   code path that produced them
2. **Find the root cause** - Don't stop at symptoms; understand why the bug
   occurs
3. **Write tests first** - Before fixing, write tests that validate your
   hypothesis about the root cause
4. **Fix and verify** - Apply the fix and confirm tests pass

## Testing Guidelines

### Test Behavior, Not Specific Bugs

Tests should verify general behavior, not be overly specific to a particular bug
scenario:

- Use generic code examples similar to existing tests in the codebase
- Test the behavior/contract of the function, not just the bug case
- Make tests readable and representative of real usage

### Use the Assert Package

Always use `server/assert/assert.go` for test assertions:

```go
import "cursortab/assert"

func TestExample(t *testing.T) {
    result := SomeFunction(input)
    assert.Equal(t, expected, result)
}
```

## Architecture Overview

cursortab.nvim is an AI-powered code completion plugin for Neovim with a hybrid
Lua/Go architecture.

### Two-Part System

**Lua Plugin** (`lua/cursortab/`) - Neovim frontend

- Handles UI rendering, keybindings, and event subscriptions
- Communicates with the daemon via Neovim RPC
- Integrates with blink.cmp (`blink.lua`) as a completion source

**Go Daemon** (`server/`) - Backend server

- Runs as a separate process, communicates via Unix socket (`cursortab.sock`)
- Manages completion state machine and AI provider calls

### Communication Flow

```
Neovim Events → Lua Plugin → RPC → Go Daemon → Context Gathering → AI Provider
                    ↑                                    ↓
               UI Rendering ←──────────────── Completion Response
```

### Key Modules

**Engine** (`server/engine/`)

- State machine: `Idle` → `PendingCompletion` → `HasCompletion` →
  `HasCursorTarget` (also `StreamingCompletion` for streaming providers)
- `engine.go`: Core state management, event dispatching, interface definitions
  (`Buffer`, `Provider`), user action tracking, file state persistence, metrics
- `state.go`: State machine transitions and state type definitions
- `event.go`: Event processing logic
- `request.go`: Completion request construction with context gathering, recent
  buffer snapshots, and user actions
- `prefetch.go`: Completion prefetching and cursor prediction
- `timer.go`: Idle timer management
- `clock.go`: `Clock`/`Timer` interfaces for testability

**Context Gathering** (`server/ctx/`)

- Pluggable system that runs context sources in parallel with a 200ms timeout
- `gatherer.go`: `Gatherer` runs all sources concurrently and merges results
  into a `ContextResult`
- `diagnostics.go`: LSP diagnostics from the current buffer
- `treesitter.go`: Scope context (enclosing function, siblings, imports) via
  Neovim's built-in treesitter
- `gitdiff.go`: For `COMMIT_EDITMSG` files, extracts staged changes and
  function/type signatures from git diff

**Buffer** (`server/buffer/`)

- `buffer.go`: `NvimBuffer` - implements `engine.Buffer` for Neovim integration
- `client.go`: Supporting types (`Batch`, `SyncResult`)
- Buffer also provides `TreesitterSymbols()`, `LinterErrors()`, and Copilot LSP
  integration methods

**Text Processing** (`server/text/`)

- `diff.go`: Analyzes text changes, categorizes as
  additions/deletions/modifications
- `staging.go`: Multi-stage completion handling
- `grouping.go`: Visual grouping of completion changes
- `incremental.go`: `IncrementalDiffBuilder` for line-by-line streaming diffs

**Providers** (`server/provider/`)

- `provider.go`: Base `Provider` type, `Client` interface, streaming support
  (`LineStreamProvider`, `TokenStreamProvider`)
- `processors.go`: `Preprocessor`, `PromptBuilder`, `Postprocessor` patterns;
  diff history formatting
- `inline/`: Simple end-of-line completion (token streaming)
- `fim/`: Fill-in-the-middle completion with prefix/suffix context (line
  streaming)
- `sweep/`: SweepAI Next-Edit model for multi-line edits (line streaming)
- `sweepapi/`: SweepAPI hosted provider with metrics, file chunks, retrieval
  chunks, and user action tracking
- `zeta/`: Zed's native model (line streaming)
- `copilot/`: GitHub Copilot LSP integration via
  `textDocument/copilotInlineEdit` NES protocol

**Metrics** (`server/metrics/`)

- Unified metrics interface for telemetry across providers
- `Sender` interface implemented by providers (e.g., SweepAPI)
- Events: `shown`, `accepted`, `rejected`, `ignored`
- Engine owns metrics state, providers implement the sending

**Types** (`server/types/`)

- `types.go`: Core type definitions including `CompletionRequest`,
  `CompletionResponse`, `ContextResult`, `TreesitterContext`, `GitDiffContext`,
  `UserAction`, `RecentBufferSnapshot`, `MetricsInfo`

**Client** (`server/client/`)

- `openai/`: OpenAI-compatible API client with batch and streaming support
- `sweepapi/`: SweepAPI client with file chunks, retrieval chunks, and user
  actions

### Configuration Flow

Lua config is serialized to JSON and passed via `CURSORTAB_CONFIG` environment
variable when spawning the daemon.

### Plugin Commands

- `:CursortabToggle` - Enable/disable
- `:CursortabStatus` - Show daemon status
- `:CursortabShowLog` - View daemon logs
- `:CursortabRestart` - Restart daemon

---

## Data Flow: Provider to Lua

This section documents the complete flow from AI provider response to rendered
completion in Neovim.

### High-Level Flow

```
User Edit / Idle Timer
    ↓
Context Gathering (diagnostics, treesitter, git diff — parallel, 200ms timeout)
    ↓
Build CompletionRequest (context + recent files + user actions)
    ↓
Provider Pipeline → API Call → Provider Response (text)
    ↓
ComputeDiff (analyze line-level changes)
    ↓
DiffResult {Changes, LineMapping}
    ↓
CreateStages (partition by viewport, group by proximity)
    ↓
Stage {Lines, Groups, CursorTarget}
    ↓
PrepareCompletion (format for Lua)
    ↓
RPC: on_completion_ready(luaData)
    ↓
Lua Plugin renders UI
    ↓
User accepts (Tab)
    ↓
ApplyBatch (execute nvim operations)
    ↓
CommitPending (update diff history, version++)
    ↓
Metrics Event (shown/accepted/rejected/ignored → provider backend)
```

### Request Construction

Before calling the provider, the engine builds a `CompletionRequest` with:

1. **Context Gathering** - Runs diagnostics, treesitter, and git diff sources in
   parallel (200ms timeout) via `ctx.Gatherer`
2. **Recent Buffer Snapshots** - First 30 lines of up to 3 recently accessed
   files (from `fileStateStore` with LRU eviction)
3. **User Actions** - Ring buffer of last 16 user edit actions (insert, delete,
   cursor movement) with byte offsets and timestamps

### Provider Pipeline

Providers execute this pipeline in `provider.GetCompletion()`:

1. **Preprocessors** - Trim buffer context to viewport, extract window bounds
2. **PromptBuilder** - Format prompt with trimmed lines, diff history, cursor
   position, additional context (diagnostics, treesitter, git diff)
3. **API Call** - Send to AI model, receive completion text
4. **Postprocessors** - Parse response, validate content, build
   `CompletionResponse`

**provider.Context** carries data through the pipeline:

- `TrimmedLines` - Lines sent to model (subset of buffer)
- `WindowStart` - 0-indexed offset of trimmed window in buffer
- `CursorLine` - 0-indexed cursor position within trimmed lines

### Streaming Types

Providers support different streaming modes:

- **StreamingTypeNone** - Batch: wait for full response
- **StreamingTypeLines** - Line-by-line: sweep, zeta, fim providers
- **StreamingTypeTokens** - Token-by-token: inline provider

For line streaming, `IncrementalStageBuilder` processes lines as they arrive,
computing diffs incrementally and creating stages when gaps are detected.

### Diff Computation

`text.ComputeDiff()` analyzes changes between old and new text:

**DiffResult** contains:

- `Changes` - Map of line number (1-indexed) to `LineChange`
- `LineMapping` - Coordinate mapping between old/new line numbers
- `OldLineCount`, `NewLineCount`

**LineChange** describes a single line change:

- `Type` - Addition, Deletion, Modification, AppendChars, DeleteChars,
  ReplaceChars
- `OldLineNum`, `NewLineNum` - Positions (1-indexed, -1 if not applicable)
- `Content`, `OldContent` - New and old line content
- `ColStart`, `ColEnd` - Character-level positions (0-indexed, for char-level
  types)

**Change Types**:

- `Addition` - New line with no old correspondence
- `Deletion` - Old line removed
- `Modification` - Line changed (complex character changes)
- `AppendChars` - Characters appended at end of line
- `DeleteChars` - Characters deleted from line
- `ReplaceChars` - Characters replaced within line

### Staging

`text.CreateStages()` breaks multi-line completions into progressive stages:

1. **Partition** - Separate changes into in-viewport and out-of-viewport
2. **Group by Proximity** - Changes within `ProximityThreshold` stay together
3. **Sort by Distance** - Prioritize stages closest to cursor
4. **Finalize** - Extract content, compute groups, set cursor targets

**Stage** represents one step in a multi-stage completion:

- `BufferStart`, `BufferEnd` - 1-indexed buffer coordinates
- `Lines` - New content for this stage
- `Changes` - Changes with relative line numbers
- `Groups` - Pre-computed groups for rendering
- `CursorLine`, `CursorCol` - Optimal cursor position after applying
- `CursorTarget` - Jump target to next stage (if navigating)
- `IsLastStage` - Whether this is the final stage

### Grouping

`text.GroupChanges()` organizes changes within a stage for UI rendering:

**Group** represents consecutive same-type changes:

- `Type` - "modification", "addition", "deletion"
- `StartLine`, `EndLine` - 1-indexed, relative to stage
- `BufferLine` - 1-indexed absolute buffer position
- `Lines` - New content
- `OldLines` - Old content (for modifications)
- `RenderHint` - Character-level optimization: "", "append_chars",
  "replace_chars", "delete_chars"
- `ColStart`, `ColEnd` - Character-level positions (0-indexed)

### Lua Format

`diffResultToLuaFormat()` converts for Lua consumption:

```lua
{
  startLine = 5,           -- 1-indexed buffer start
  groups = {
    {
      type = "modification",
      start_line = 1,      -- relative to startLine
      end_line = 2,
      buffer_line = 5,     -- absolute buffer position
      lines = {"new1", "new2"},
      old_lines = {"old1", "old2"},
      render_hint = "append_chars",
      col_start = 10,
      col_end = 15
    }
  },
  cursor_line = 2,         -- relative cursor position
  cursor_col = 8
}
```

### Commit & History

After user accepts, `buffer.CommitPending()`:

1. Extracts granular diffs as `DiffEntry` records
2. Appends to `diffHistories` for provider context
3. Updates `originalLines` checkpoint
4. Increments `version`

**DiffEntry** tracks cumulative edits:

- `Original` - Content before change
- `Updated` - Content after change

Diff history is included in subsequent completion requests to give the AI
context about recent edits.

---

## Terminology Reference

| Term                        | Definition                                                                               |
| --------------------------- | ---------------------------------------------------------------------------------------- |
| **Trimming**                | Selecting a window of buffer context around cursor to reduce prompt size                 |
| **WindowStart**             | 0-indexed offset of trimmed context window in full buffer                                |
| **Staging**                 | Breaking multi-line completions into progressive stages for display                      |
| **Stage**                   | One step in a multi-stage completion with its own lines, groups, and cursor target       |
| **Proximity Threshold**     | Maximum gap (in lines) between changes to keep in same stage                             |
| **Grouping**                | Organizing consecutive same-type changes within a stage for rendering                    |
| **Group**                   | Set of consecutive changes of same type (addition/modification/deletion)                 |
| **Render Hint**             | Character-level optimization indicator (append_chars, replace_chars, delete_chars)       |
| **Buffer Line**             | 1-indexed absolute line number in the Neovim buffer                                      |
| **LineMapping**             | Coordinate mapping between old and new line numbers after diff                           |
| **Change Type**             | Classification: Addition, Deletion, Modification, AppendChars, DeleteChars, ReplaceChars |
| **Diff History**            | Cumulative record of edits passed to provider for context                                |
| **File State**              | Persisted previous/original lines and diffs for file context restoration                 |
| **Cursor Target**           | Position to jump to for next stage or end of completion                                  |
| **Context Gathering**       | Parallel collection of diagnostics, treesitter, and git diff context before requests     |
| **ContextResult**           | Merged output from all context sources (diagnostics, treesitter, git diff)               |
| **Recent Buffer Snapshots** | First N lines of recently accessed files, passed as cross-file context                   |
| **User Actions**            | Ring buffer of recent edits/cursor movements with byte offsets and timestamps            |
| **Metrics Sender**          | Provider-implemented interface for sending telemetry (shown/accepted/rejected/ignored)   |

---

## Coordinate Systems

- **Buffer coordinates**: 1-indexed line numbers as used by Neovim
- **Provider coordinates**: May be offset by `WindowStart` when trimming is
  applied
- **Stage-relative coordinates**: Line numbers relative to `BufferStart` of the
  stage
- **Character positions**: 0-indexed column positions within a line

Always convert at boundaries. The `LineMapping` in `DiffResult` handles old↔new
line number translation.
