# Telemetry

cursortab.nvim does **not** collect any data by default.

If you opt in, anonymous completion metrics are sent to a telemetry endpoint to help train a better gating model, the system that decides when to show or suppress a completion.

## Opting in

```lua
require("cursortab").setup({
  behavior = {
    enable_community_data = true,
  },
})
```

## Opting out

Set `enable_community_data = false` (the default) or remove the setting. No data is collected or sent.

## What is collected

Structural features about the cursor context when a completion is shown, plus the outcome (accepted, rejected, or ignored).

| Field | Description | Example |
|---|---|---|
| `outcome` | What the user did | `"rejected"` |
| `file_ext` | File extension only | `".go"` |
| `cursor_col` | Column position | `15` |
| `prefix_length` | Characters before cursor on line | `15` |
| `trimmed_prefix_length` | Same, minus trailing whitespace | `13` |
| `doc_length` | Document size in bytes | `8400` |
| `cursor_offset` | Byte offset of cursor in file | `1203` |
| `relative_position` | Cursor position as fraction of file | `0.14` |
| `after_cursor_ws` | Only whitespace after cursor? | `true` |
| `last_char` | Character before cursor | `")"` |
| `last_nonws_char` | Last non-whitespace char before cursor | `")"` |
| `prev_filter_shown` | Was previous completion shown? | `true` |
| `completion_lines` | Lines in the completion | `3` |
| `completion_source` | How it was triggered | `"typing"` |
| `display_duration_ms` | How long it was visible | `1200` |

Sample event:

```json
{
  "outcome": "rejected",
  "file_ext": ".go",
  "cursor_col": 15,
  "prefix_length": 15,
  "after_cursor_ws": false,
  "last_char": ")",
  "display_duration_ms": 1200
}
```

## What is NOT collected

- No code content, only structural features like line length and character categories
- No file paths, only the extension
- No project or workspace identifiers
- No user identity, events are anonymous with no persistent ID
- No IP addresses logged by the endpoint

## Why

The data is used to train an open gating model that ships as the default filter in future versions. The dataset, model weights, and training code are all published openly.
