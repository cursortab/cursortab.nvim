# cursortab.nvim

A Neovim plugin that provides local edit completions and cursor predictions.

> [!NOTE]
>
> **Help improve completions** by contributing anonymous usage data to our
> [open dataset](https://github.com/cursortab/api). Set `contribute_data = true`
> in your config to opt in. No code content or file paths are collected —
> [see the schema](https://github.com/cursortab/api/blob/main/docs/community-data-schema.md).

> [!WARNING]
>
> **This is an early-stage project.** Expect bugs, incomplete features, and
> breaking changes. Make sure to regularly update the plugin.

<p align="center">
    <img src="assets/demo.gif" width="600">
</p>

<!-- mtoc-start -->

* [Requirements](#requirements)
* [Installation](#installation)
  * [Using lazy.nvim](#using-lazynvim)
  * [Using packer.nvim](#using-packernvim)
* [Quick Start](#quick-start)
* [Configuration](#configuration)
  * [Highlight Groups](#highlight-groups)
  * [Providers](#providers)
    * [Benchmarks](#benchmarks)
    * [Inline Provider (Default)](#inline-provider-default)
    * [FIM Provider](#fim-provider)
    * [Sweep Provider](#sweep-provider)
    * [Sweep API Provider](#sweep-api-provider)
    * [Zeta-2 Provider](#zeta-2-provider)
    * [Zeta Provider (legacy)](#zeta-provider-legacy)
    * [Copilot Provider](#copilot-provider)
    * [Mercury API Provider](#mercury-api-provider)
  * [blink.cmp Integration](#blinkcmp-integration)
* [Usage](#usage)
  * [Commands](#commands)
* [Development](#development)
* [FAQ](#faq)
* [Contributing](#contributing)
* [License](#license)

<!-- mtoc-end -->

## Requirements

- Go 1.25.0+ (for building the server component)
- Neovim 0.8+ (for the plugin)

## Installation

### Using [lazy.nvim](https://github.com/folke/lazy.nvim)

```lua
{
  "cursortab/cursortab.nvim",
  -- version = "*",  -- Use latest tagged version for more stability
  lazy = false,      -- The server is already lazy loaded
  build = "cd server && go build",
  config = function()
    require("cursortab").setup()
  end,
}
```

### Using [packer.nvim](https://github.com/wbthomason/packer.nvim)

```lua
use {
  "cursortab/cursortab.nvim",
  -- tag = "*",  -- Use latest tagged version for more stability
  run = "cd server && go build",
  config = function()
    require("cursortab").setup()
  end
}
```

## Quick Start

The fastest way to get started is with the **Mercury API** provider (hosted, no
local GPU needed):

1. Get an API key from [Inception Labs](https://docs.inceptionlabs.ai/)
2. Set the environment variable:

   ```bash
   export MERCURY_AI_TOKEN="your-api-key-here"
   ```

3. Add the provider to your setup:

   ```lua
   require("cursortab").setup({
     provider = {
       type = "mercuryapi",
       api_key_env = "MERCURY_AI_TOKEN",
     },
   })
   ```

If you prefer **local inference**, use the `sweep` provider with
[llama.cpp](https://github.com/ggml-org/llama.cpp):

```bash
llama-server -hf sweepai/sweep-next-edit-1.5b --port 8000
```

```lua
require("cursortab").setup({
  provider = {
    type = "sweep",
    url = "http://localhost:8000",
  },
})
```

See [Providers](#providers) for all available options.

## Configuration

```lua
require("cursortab").setup({
  enabled = true,
  log_level = "info",  -- "trace", "debug", "info", "warn", "error"
  state_dir = vim.fn.stdpath("state") .. "/cursortab",  -- Directory for runtime files (log, socket, pid)
  contribute_data = false,  -- Opt-in: send anonymous metrics to train a better gating model

  keymaps = {
    accept = "<Tab>",           -- Keymap to accept completion, or false to disable
    partial_accept = "<S-Tab>", -- Keymap to partially accept, or false to disable
    trigger = false,            -- Keymap to manually trigger completion, or false to disable
  },

  ui = {
    completions = {
      addition_style = "dimmed",  -- "dimmed" or "highlight"
      fg_opacity = 0.6,           -- opacity for completion overlays (0=invisible, 1=fully visible)
    },
    jump = {
      symbol = "",              -- Symbol shown for jump points
      text = " TAB ",            -- Text displayed after jump symbol
      show_distance = true,      -- Show line distance for off-screen jumps
    },
  },

  behavior = {
    idle_completion_delay = 50,  -- Delay in ms after idle to trigger completion (-1 to disable)
    text_change_debounce = 50,   -- Debounce in ms after text change to trigger completion (-1 to disable)
    max_visible_lines = 12,      -- Max visible lines per completion (0 to disable)
    enabled_modes = { "insert", "normal" },  -- Modes where completions are active
    cursor_prediction = {
      enabled = true,            -- Show jump indicators after completions
      auto_advance = true,       -- When no changes, show cursor jump to last line
      proximity_threshold = 2,   -- Min lines apart to show cursor jump (0 to disable)
    },
    ignore_paths = {             -- Glob patterns for files to skip completions
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
    ignore_gitignored = true,    -- Skip files matched by .gitignore
  },

  provider = {
    type = "inline",                      -- Provider: "inline", "fim", "sweep", "sweepapi", "zeta", "zeta-2", "copilot", or "mercuryapi"
    url = "http://localhost:8000",        -- URL of the provider server
    api_key_env = "",                     -- Env var name for API key (e.g., "OPENAI_API_KEY")
    model = "",                           -- Model name
    temperature = 0.0,                    -- Sampling temperature
    max_tokens = 512,                     -- Max tokens to generate
    top_k = 50,                           -- Top-k sampling
    completion_timeout = 5000,            -- Timeout in ms for completion requests
    max_diff_history_tokens = 512,        -- Max tokens for diff history (0 = no limit)
    completion_path = "/v1/completions",  -- API endpoint path
    fim_tokens = {                        -- FIM tokens (for FIM provider)
      prefix = "<|fim_prefix|>",
      suffix = "<|fim_suffix|>",
      middle = "<|fim_middle|>",
    },
    privacy_mode = true,                  -- Don't send telemetry to provider
  },

  blink = {
    enabled = false,    -- Enable blink source
    ghost_text = true,  -- Show native ghost text alongside blink menu
  },

  debug = {
    immediate_shutdown = false,  -- Shutdown daemon immediately when no clients
  },
})
```

For detailed configuration documentation, see `:help cursortab-config`.

### Highlight Groups

The plugin defines the following highlight groups with `default = true`, so you
can override them in your colorscheme or config:

| Group                   | Default                            | Purpose                        |
| ----------------------- | ---------------------------------- | ------------------------------ |
| `CursorTabDeletion`     | `bg = "#4f2f2f"`                   | Background for deleted text    |
| `CursorTabAddition`     | `bg = "#394f2f"`                   | Background for added text      |
| `CursorTabModification` | `bg = "#282e38"`                   | Background for modified text   |
| `CursorTabCompletion`   | `fg = "#80899c"`                   | Foreground for completion text |
| `CursorTabJumpSymbol`   | `fg = "#373b45"`                   | Jump indicator symbol          |
| `CursorTabJumpText`     | `bg = "#373b45"`, `fg = "#bac1d1"` | Jump indicator text            |

To customize, set the highlight before or after calling `setup()`:

```lua
vim.api.nvim_set_hl(0, "CursorTabAddition", { bg = "#1a3a1a" })
```

### Providers

The plugin supports eight AI provider backends: Inline, FIM, Sweep, Sweep API,
Zeta-2, Zeta (legacy), Copilot, and Mercury API.

| Provider     | Hosted | Multi-line | Multi-edit | Cursor Prediction | Streaming | Model                   |
| ------------ | :----: | :--------: | :--------: | :---------------: | :-------: | ----------------------- |
| `inline`     |        |            |            |                   |           | Any base model          |
| `fim`        |        |     ✓      |            |                   |     ✓     | Any FIM-capable         |
| `sweep`      |        |     ✓      |     ✓      |         ✓         |     ✓     | Sweep Next-Edit family  |
| `sweepapi`   |   ✓    |     ✓      |     ✓      |         ✓         |     ✓     | `sweep-next-edit-7b`    |
| `zeta-2`     |        |     ✓      |     ✓      |         ✓         |     ✓     | `zeta-2` (SeedCoder-8B) |
| `zeta`       |        |     ✓      |     ✓      |         ✓         |     ✓     | `zeta` (Qwen2.5-Coder)  |
| `copilot`    |   ✓    |     ✓      |     ✓      |         ✓         |           | GitHub Copilot          |
| `mercuryapi` |   ✓    |     ✓      |     ✓      |         ✓         |           | `mercury-edit`          |

**Context Per Provider:**

| Context             | inline | fim | sweep | zeta-2 | zeta | sweepapi | copilot | mercuryapi |
| ------------------- | :----: | :-: | :---: | :----: | :--: | :------: | :-----: | :--------: |
| Buffer content      |   ✓    |  ✓  |   ✓   |   ✓    |  ✓   |    ✓     |         |     ✓      |
| Edit history        |        |     |   ✓   |   ✓    |  ✓   |    ✓     |         |     ✓      |
| Previous file state |        |     |   ✓   |        |      |    ✓     |         |            |
| LSP diagnostics     |        |     |       |   ✓    |  ✓   |    ✓     |         |     ✓      |
| Treesitter context  |        |     |   ✓   |   ✓    |  ✓   |    ✓     |         |     ✓      |
| Git diff context    |        |     |   ✓   |   ✓    |  ✓   |    ✓     |         |     ✓      |
| Recent files        |        |     |   ✓   |   ✓    |  ✓   |    ✓     |         |     ✓      |
| User actions        |        |     |   ✓   |        |      |    ✓     |         |            |

#### Benchmarks

Measured on 50 scenarios (25 quality + 25 suppress) using the eval harness.
Sorted by Score (higher = better):

- **Score** — `deltaChrF × gateScore / 100` where
  `gateScore = 2 × showRate × quietRate / (showRate + quietRate)`. Combines edit
  quality with gating behavior into a single metric.
- **deltaChrF** — edit quality when shown (character n-gram F-score on the diff
  region)
- **Show rate** — fraction of quality scenarios where a completion was shown
- **Quiet rate** — fraction of suppress scenarios where the provider correctly
  produced nothing

| Target               | Type       |    Score | deltaChrF | Show rate | Quiet rate | p50 (ms) | p90 (ms) |
| -------------------- | ---------- | -------: | --------: | --------: | ---------: | -------: | -------: |
| mercuryapi           | mercuryapi | **0.58** |  **64.4** |  **100%** |        81% |      565 |      739 |
| zeta-2               | zeta-2     |     0.56 |      61.5 |       88% |    **96%** |      551 |      833 |
| zeta                 | zeta       |     0.55 |      60.9 |       88% |        92% |      413 |      661 |
| qwen3.5-27B          | fim        |     0.23 |      32.2 |       76% |        68% |      131 |      647 |
| sweep-next-edit-7B   | sweep      |     0.22 |      45.2 |       64% |        40% |      237 |      474 |
| sweep-next-edit-1.5B | sweep      |     0.20 |      41.9 |       68% |        36% |      155 |      258 |
| qwen3.5-4B           | fim        |     0.18 |      27.1 |       76% |        60% |     1254 |     1339 |
| qwen3.5-0.8B         | fim        |     0.18 |      31.4 |       84% |        44% |   **49** |      509 |
| qwen3.5-2B           | fim        |     0.17 |      29.4 |       84% |        44% |       89 |      735 |
| copilot              | copilot    |     0.13 |      22.3 |       40% |   **100%** |      351 |      915 |
| sweep-next-edit-0.5B | sweep      |     0.10 |      23.0 |       52% |        40% |      126 |  **201** |
| sweepapi             | sweepapi   |     0.08 |      16.4 |       32% |   **100%** |      156 |      300 |

#### Inline Provider (Default)

<details>
<summary>Details</summary>

Single-line completion using any OpenAI-compatible `/v1/completions` endpoint.

```lua
require("cursortab").setup({
  provider = {
    type = "inline",
    url = "http://localhost:8000",
  },
})
```

```bash
llama-server -hf ggml-org/Qwen2.5-Coder-1.5B-Q8_0-GGUF --port 8000
```

</details>

#### FIM Provider

<details>
<summary>Details</summary>

Fill-in-the-Middle multi-line completion. Compatible with Qwen, DeepSeek-Coder,
and similar FIM-capable models.

```lua
require("cursortab").setup({
  provider = {
    type = "fim",
    url = "http://localhost:8000",
    max_tokens = 256,
  },
})
```

```bash
llama-server -hf ggml-org/Qwen2.5-Coder-1.5B-Q8_0-GGUF --port 8000
```

</details>

#### Sweep Provider

<details>
<summary>Details</summary>

Local next-edit prediction using [Sweep models](https://huggingface.co/sweepai):
`sweep-next-edit-v2-7B`, `sweep-next-edit-1.5B`, `sweep-next-edit-0.5B`.

```lua
require("cursortab").setup({
  provider = {
    type = "sweep",
    url = "http://localhost:8000",
  },
})
```

```bash
llama-server -hf sweepai/sweep-next-edit-1.5b --port 8000
```

</details>

#### Sweep API Provider

<details>
<summary>Details</summary>

Hosted Sweep API — no local model required. Get a token from
[sweep.dev](https://sweep.dev/).

```bash
export SWEEPAPI_TOKEN="your-api-token-here"
```

```lua
require("cursortab").setup({
  provider = {
    type = "sweepapi",
    api_key_env = "SWEEPAPI_TOKEN",
  },
})
```

</details>

#### Zeta-2 Provider

<details>
<summary>Details</summary>

Zed's [Zeta-2](https://huggingface.co/zed-industries/zeta-2) (SeedCoder-8B).
Requires vLLM or compatible inference server.

```lua
require("cursortab").setup({
  provider = {
    type = "zeta-2",
    url = "http://localhost:8000",
    model = "zeta-2",
  },
})
```

```bash
vllm serve zed-industries/zeta-2 --served-model-name zeta-2 --port 8000
```

</details>

#### Zeta Provider (legacy)

<details>
<summary>Details</summary>

Zed's original [Zeta](https://huggingface.co/zed-industries/zeta) model
(Qwen2.5-Coder-7B). Superseded by [Zeta-2](#zeta-2-provider).

```lua
require("cursortab").setup({
  provider = {
    type = "zeta",
    url = "http://localhost:8000",
    model = "zeta",
  },
})
```

```bash
vllm serve zed-industries/zeta --served-model-name zeta --port 8000
```

</details>

#### Copilot Provider

<details>
<summary>Details</summary>

GitHub Copilot via
[copilot-language-server](https://github.com/github/copilot-language-server-release).
Requires a Copilot subscription and `vim.lsp.enable`.

```lua
require("cursortab").setup({
  provider = {
    type = "copilot",
  },
})
```

</details>

#### Mercury API Provider

<details>
<summary>Details</summary>

Hosted [Mercury](https://docs.inceptionlabs.ai/) next-edit model by Inception
Labs.

```bash
export MERCURY_AI_TOKEN="your-api-key-here"
```

```lua
require("cursortab").setup({
  provider = {
    type = "mercuryapi",
    api_key_env = "MERCURY_AI_TOKEN",
  },
})
```

</details>

### blink.cmp Integration

<details>
<summary>Details</summary>

This integration exposes a minimal blink source that only consumes
`append_chars` (end-of-line ghost text). Complex diffs (multi-line edits,
replacements, deletions, cursor prediction UI) still render via the native UI.

```lua
require("cursortab").setup({
  keymaps = {
    accept = false, -- Let blink manage <Tab>
  },
  blink = {
    enabled = true,
    ghost_text = false,  -- Disable native ghost text
  },
})

require("blink.cmp").setup({
  sources = {
    providers = {
      cursortab = {
        module = "cursortab.blink",
        name = "cursortab",
        async = true,
        -- Should match provider.completion_timeout in cursortab config
        timeout_ms = 5000,
        score_offset = 50, -- Higher priority among suggestions
      },
    },
  },
})
```

</details>

## Usage

- **Tab Key**: Navigate to cursor predictions or accept completions
- **Shift-Tab Key**: Partially accept completions (word-by-word for inline,
  line-by-line for multi-line)
- **Esc Key**: Reject current completions
- The plugin automatically shows jump indicators for predicted cursor positions
- Visual indicators appear for additions, deletions, and completions
- Off-screen jump targets show directional arrows with distance information

### Commands

- `:CursortabToggle`: Toggle the plugin on/off
- `:CursortabShowLog`: Show the cursortab log file in a new buffer
- `:CursortabClearLog`: Clear the cursortab log file
- `:CursortabStatus`: Show detailed status information about the plugin and
  daemon
- `:CursortabRestart`: Restart the cursortab daemon process

## Development

```bash
# Build the server
cd server && go build

# Run all tests
cd server && go test ./...
```

For E2E tests, eval harness, and detailed development instructions, see
[CONTRIBUTING.md](CONTRIBUTING.md).

## FAQ

<details>
<summary>Why are completions slow?</summary>

1. Use a smaller or more heavily quantized model (e.g., Q4 instead of Q8)
2. Decrease `provider.max_tokens` to reduce output length (also limits input
   context)

The `copilot` provider is known to be slower than the rest.

</details>

<details>
<summary>Why are completions not working?</summary>

1. Update to the latest version, restart Neovim and restart the daemon with
   `:CursortabRestart`
2. Increase `provider.completion_timeout` (default: 5000ms) to 10000 or more if
   your model is slow
3. Increase `provider.max_tokens` to give the model more surrounding context
   (trade-off: slower completions)

</details>

<details>
<summary>How do I update the plugin?</summary>

Use your Neovim plugin manager to pull the latest changes, then restart Neovim.
The daemon will automatically restart if the configuration has changed. You can
also run `:CursortabRestart` to force a restart.

</details>

<details>
<summary>Why isn't my API key or environment variable being picked up?</summary>

The plugin runs a background daemon that persists after Neovim closes.
Environment variables are only loaded when the daemon starts. If you add or
change an environment variable (e.g., `SWEEPAPI_TOKEN` in your `.zshrc`), run
`:CursortabRestart` to restart the daemon with the new environment variables.

Note: If you change plugin configuration (e.g., switch providers), the daemon
will automatically restart on the next `setup()` call.

</details>

## Contributing

Contributions are welcome! See [CONTRIBUTING.md](CONTRIBUTING.md) for build,
test, and eval instructions.

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file
for details.
