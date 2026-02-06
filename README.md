# cursortab.nvim

A Neovim plugin that provides local edit completions and cursor predictions.
Currently supports custom models and models form Zeta (Zed) and SweepAI.

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
* [Configuration](#configuration)
  * [Providers](#providers)
    * [Inline Provider (Default)](#inline-provider-default)
    * [FIM Provider](#fim-provider)
    * [Sweep Provider](#sweep-provider)
    * [Sweep API Provider](#sweep-api-provider)
    * [Zeta Provider](#zeta-provider)
    * [Copilot Provider](#copilot-provider)
    * [Mercury API Provider](#mercury-api-provider)
  * [blink.cmp Integration](#blinkcmp-integration)
* [Usage](#usage)
  * [Commands](#commands)
* [Development](#development)
  * [Build](#build)
  * [Test](#test)
* [FAQ](#faq)
* [Contributing](#contributing)
* [License](#license)

<!-- mtoc-end -->

## Requirements

- Go 1.24.2+ (for building the server component)
- Neovim 0.8+ (for the plugin)

## Installation

### Using [lazy.nvim](https://github.com/folke/lazy.nvim)

```lua
{
  "leonardcser/cursortab.nvim",
  -- version = "*",  -- Use latest tagged version for more stability
  build = "cd server && go build",
  config = function()
    require("cursortab").setup()
  end,
}
```

### Using [packer.nvim](https://github.com/wbthomason/packer.nvim)

```lua
use {
  "leonardcser/cursortab.nvim",
  -- tag = "*",  -- Use latest tagged version for more stability
  run = "cd server && go build",
  config = function()
    require("cursortab").setup()
  end
}
```

## Configuration

```lua
require("cursortab").setup({
  enabled = true,
  log_level = "info",  -- "trace", "debug", "info", "warn", "error"
  state_dir = vim.fn.stdpath("state") .. "/cursortab",  -- Directory for runtime files (log, socket, pid)

  keymaps = {
    accept = "<Tab>",           -- Keymap to accept completion, or false to disable
    partial_accept = "<S-Tab>", -- Keymap to partially accept, or false to disable
    trigger = false,            -- Keymap to manually trigger completion, or false to disable
  },

  ui = {
    colors = {
      deletion = "#4f2f2f",      -- Background color for deletions
      addition = "#394f2f",      -- Background color for additions
      modification = "#282e38",  -- Background color for modifications
      completion = "#80899c",    -- Foreground color for completions
    },
    jump = {
      symbol = "",              -- Symbol shown for jump points
      text = " TAB ",            -- Text displayed after jump symbol
      show_distance = true,      -- Show line distance for off-screen jumps
      bg_color = "#373b45",      -- Jump text background color
      fg_color = "#bac1d1",      -- Jump text foreground color
    },
  },

  behavior = {
    idle_completion_delay = 50,  -- Delay in ms after idle to trigger completion (-1 to disable)
    text_change_debounce = 50,   -- Debounce in ms after text change to trigger completion (-1 to disable)
    max_visible_lines = 12,      -- Max visible lines per completion (0 to disable)
    cursor_prediction = {
      enabled = true,            -- Show jump indicators after completions
      auto_advance = true,       -- When no changes, show cursor jump to last line
      proximity_threshold = 2,   -- Min lines apart to show cursor jump (0 to disable)
    },
  },

  provider = {
    type = "inline",                      -- Provider: "inline", "fim", "sweep", "sweepapi", "zeta", "copilot", or "mercuryapi"
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

### Providers

The plugin supports seven AI provider backends: Inline, FIM, Sweep, Sweep API,
Zeta, Copilot, and Mercury API.

| Provider     | Hosted | Multi-line | Multi-edit | Cursor Prediction | Streaming | Model                  |
| ------------ | :----: | :--------: | :--------: | :---------------: | :-------: | ---------------------- |
| `inline`     |        |            |            |                   |     ✓     | Any base model         |
| `fim`        |        |     ✓      |            |                   |     ✓     | Any FIM-capable        |
| `sweep`      |        |     ✓      |     ✓      |         ✓         |     ✓     | `sweep-next-edit-1.5b` |
| `sweepapi`   |   ✓    |     ✓      |     ✓      |         ✓         |     ✓     | `sweep-next-edit-7b`   |
| `zeta`       |        |     ✓      |     ✓      |         ✓         |     ✓     | `zeta`                 |
| `copilot`    |   ✓    |     ✓      |     ✓      |         ✓         |           | GitHub Copilot         |
| `mercuryapi` |   ✓    |     ✓      |     ✓      |         ✓         |           | `mercury-coder`        |

**Context Per Provider:**

| Context             | inline | fim | sweep | zeta | sweepapi | copilot | mercuryapi |
| ------------------- | :----: | :-: | :---: | :--: | :------: | :-----: | :--------: |
| Buffer content      |   ✓    |  ✓  |   ✓   |  ✓   |    ✓     |         |     ✓      |
| Edit history        |        |     |   ✓   |  ✓   |    ✓     |         |     ✓      |
| Previous file state |        |     |   ✓   |      |    ✓     |         |            |
| LSP diagnostics     |        |     |       |  ✓   |    ✓     |         |            |
| Treesitter context  |        |     |   ✓   |  ✓   |    ✓     |         |            |
| Git diff context    |        |     |   ✓   |  ✓   |    ✓     |         |            |
| Recent files        |        |     |       |      |    ✓     |         |     ✓      |
| User actions        |        |     |       |      |    ✓     |         |            |

#### Inline Provider (Default)

<details>
<summary>Details</summary>

End-of-line completion using OpenAI-compatible API endpoints. Works with any
OpenAI-compatible `/v1/completions` endpoint.

**Requirements:**

- An OpenAI-compatible completions endpoint

**Example Configuration:**

```lua
require("cursortab").setup({
  provider = {
    type = "inline",
    url = "http://localhost:8000",
  },
})
```

**Example Setup:**

```bash
# Using llama.cpp
llama-server -hf ggml-org/Qwen2.5-Coder-1.5B-Q8_0-GGUF --port 8000
```

</details>

#### FIM Provider

<details>
<summary>Details</summary>

Fill-in-the-Middle completion using standard FIM tokens. Uses both prefix
(before cursor) and suffix (after cursor) context. Compatible with Qwen,
DeepSeek-Coder, and similar models. Works with any OpenAI-compatible
`/v1/completions` endpoint.

**Requirements:**

- An OpenAI-compatible completions endpoint with a FIM-capable model

**Example Configuration:**

```lua
require("cursortab").setup({
  provider = {
    type = "fim",
    url = "http://localhost:8000",
  },
})
```

**Example Setup:**

```bash
# Using llama.cpp with Qwen2.5-Coder 1.5B
llama-server -hf ggml-org/Qwen2.5-Coder-1.5B-Q8_0-GGUF --port 8000

# Or with Qwen 2.5 Coder 14B + 0.5B draft for speculative decoding
llama-server \
    -hf ggml-org/Qwen2.5-Coder-14B-Q8_0-GGUF:q8_0 \
    -hfd ggml-org/Qwen2.5-Coder-0.5B-Q8_0-GGUF:q8_0 \
    --port 8012 \
    -b 1024 \
    -ub 1024 \
    --cache-reuse 256
```

</details>

#### Sweep Provider

<details>
<summary>Details</summary>

Sweep Next-Edit 1.5B model for fast, accurate next-edit predictions. Sends full
file for small files, trimmed around cursor for large files.

**Requirements:**

- vLLM or compatible inference server
- Sweep Next-Edit model downloaded from
  [Hugging Face](https://huggingface.co/sweepai/sweep-next-edit-1.5b)

**Example Configuration:**

```lua
require("cursortab").setup({
  provider = {
    type = "sweep",
    url = "http://localhost:8000",
  },
})
```

**Example Setup:**

```bash
# Using llama.cpp
llama-server -hf sweepai/sweep-next-edit-1.5b-GGUF --port 8000

# Or with a local GGUF file
llama-server -m sweep-next-edit-1.5b.q8_0.v2.gguf --port 8000
```

</details>

#### Sweep API Provider

<details>
<summary>Details</summary>

Sweep's hosted API for Next-Edit predictions. No local model setup required.

**Requirements:**

- Create an account at [sweep.dev](https://sweep.dev/) and get your API token
- Set the `SWEEPAPI_TOKEN` environment variable with your token

**Example Configuration:**

```bash
# In your shell config (.bashrc, .zshrc, etc.)
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

#### Zeta Provider

<details>
<summary>Details</summary>

Zed's Zeta model - a Qwen2.5-Coder-7B fine-tuned for edit prediction using DPO
and SFT.

**Requirements:**

- vLLM or compatible inference server
- Zeta model downloaded from
  [Hugging Face](https://huggingface.co/zed-industries/zeta)

**Example Configuration:**

```lua
require("cursortab").setup({
  provider = {
    type = "zeta",
    url = "http://localhost:8000",
    model = "zeta",
  },
})
```

**Example Setup:**

```bash
# Using vLLM
vllm serve zed-industries/zeta --served-model-name zeta --port 8000

# See the HuggingFace page for optimized deployment options
```

</details>

#### Copilot Provider

<details>
<summary>Details</summary>

GitHub Copilot completions using the official
[copilot-language-server](https://github.com/github/copilot-language-server-release)
LSP server, enabled with `vim.lsp.enable`. Can be installed in multiple ways:

1. Install using `npm` or your OS's package manager
2. Install with
   [mason-lspconfig.nvim](https://github.com/mason-org/mason-lspconfig.nvim)
3. [copilot.lua](https://github.com/zbirenbaum/copilot.lua) and
   [copilot.vim](https://github.com/github/copilot.vim) both bundle the LSP
   server in their plugin
4. Sign in to Copilot: `:LspCopilotSignIn`

**Requirements:**

- GitHub Copilot subscription
- `copilot-language-server` installed and enabled via `vim.lsp.enable`

**Example Configuration:**

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

[Mercury](https://docs.inceptionlabs.ai/) is Inception Labs' hosted Next-Edit
prediction model.

**Requirements:**

- Mercury API key (set via `MERCURY_AI_TOKEN` environment variable)

**Example Configuration:**

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

### Build

To build the server component:

```bash
cd server && go build
```

### Test

To run tests:

```bash
cd server && go test ./...
```

## FAQ

<details>
<summary>Which provider should I use?</summary>

See the [provider feature comparison table](#providers) for capabilities. For
the best experience:

- If you have a consumer GPU and want to run locally, use the `sweep` provider
  with the `sweep-next-edit-1.5b` model for fast local inference
- Otherwise, use the `sweepapi` for the best quality with the hosted
  `sweep-next-edit-7b` model

</details>

<details>
<summary>Why are completions slow?</summary>

1. Use a smaller or more heavily quantized model (e.g., Q4 instead of Q8)
2. Decrease `provider.max_tokens` to reduce output length (also limits input
   context)

The `github` provider is known to be slower than the rest.

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

Contributions are welcome! Please open an issue or a pull request.

Feel free to open issues for bugs :)

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file
for details.
