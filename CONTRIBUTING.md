# Contributing

Contributions are welcome! Please open an issue or a pull request.

## Prerequisites

- Go 1.25.0+
- [just](https://github.com/casey/just) command runner
- Neovim 0.11+ for the Neovim scenario tests (skipped when `nvim` is missing)

## Build & Test

```bash
just build             # build the server
just test              # all tests (unit, scenarios, E2E pipeline, eval baseline check)
just test TestName     # run specific test(s)
just fmt               # format Go code
just lint              # check for dead code
```

## E2E Pipeline Tests

Pipeline tests verify ComputeDiff, CreateStages, and ToLuaFormat. They run as
part of `just test`. Fixtures are `.txtar` files in `server/text/testdata/`,
each containing `old.txt`, `new.txt`, and an `expected` section.

| Command                                               | Who runs it                              |
| ----------------------------------------------------- | ---------------------------------------- |
| `just test-e2e` (run + generate HTML report)          | agent or human                           |
| `just update-e2e [name]` (regenerate expected output) | agent or human                           |
| Review `server/text/testdata/report.html`             | **human only**                           |
| `just verify-e2e [name]` (mark as reviewed)           | human; agents only when explicitly asked |

Updated fixtures are marked as **unverified** and will fail `just test` until
the human runs `just verify-e2e <name>`. Verification state is tracked in
`server/text/testdata/verified.json` (a SHA256 manifest). In the HTML report,
verified passing tests are collapsed while unverified or failing tests are shown
expanded.

To add a new fixture, create a `.txtar` file in `server/text/testdata/` with
`old.txt`, `new.txt` sections, then run `just update-e2e <name>` to generate the
initial expected output. Stop there — the human reviews the report and runs
`just verify-e2e <name>`.

## Behavior Scenarios

Behavior scenarios check what a user sees: the steps a user takes and what the
buffer, the display, and the engine show after them. They are `.txtar` files in
`server/scenario/testdata/` and run as part of `just test`, twice:

- `TestScenarios` runs the engine in process, with a fake buffer and clock.
- `TestScenariosInNeovim` runs them in a headless Neovim with the plugin, a
  freshly built daemon, and a fake model server. It is skipped when `nvim` is
  not installed or with `go test -short`.

The `respond` steps give the answers of the provider. By default a fake sweep
model serves them to the real sweep provider, so the same file runs in both
runners. `provider: scripted` hands them to the engine as completions instead,
for answers that sweep cannot give (such as a completion with a stale end line);
those run only in process.

```
Typing the start of a shown append keeps the prediction without a new request.
row: 2
col: 2
-- buffer.txt --
function add(a, b) {

}
-- steps --
respond 2-2
  | "  return a + b;"
request-completion manual
expect shown requests=1
  display:
    | modification 2 append_chars
type "ret"
expect shown requests=1 cursor=2:5
accept
expect hidden cursor=2:15
  buffer:
    | "function add(a, b) {"
    | "  return a + b;"
    | "}"
```

Each step ends when the engine settles: it handled every event, a pending text
change debounce fires as if the user paused, and no request is in flight. In
Neovim, the test sends `cursortab_sync` to the daemon after the keys of a step,
which answers once the engine settled.

To write the expectations of a new scenario, start with bare `expect` lines and
fill them from what both runners observe.

## Eval Harness

The eval harness measures completion quality and gating behavior across
providers. It replays recorded API responses (cassettes) offline — no network or
API cost. Results appear in the [benchmarks table](README.md#benchmarks).

### Quick Start

```bash
just eval                        # replay, update baseline, fail on regression
just eval-check                  # CI: fail if baseline differs (no update)
just eval --targets mercuryapi   # filter by target
just eval --per-scenario=false   # aggregate table only
```

### Scenarios

Each scenario is a [txtar](https://pkg.go.dev/golang.org/x/tools/txtar) file in
`server/eval/scenarios/`, in the same format as behavior scenarios. Targets are
defined globally in `server/scenario/targets.go`; scenarios reference them by
name, and their recorded cassettes answer the requests.

```
Description of the scenario.
id: my-scenario
language: go
file: store.go
row: 6
col: 17
targets: mercuryapi, zeta-2

-- buffer.txt --
package store
...

-- steps --
request-completion manual
accept

-- expected --
package store
... ideal final buffer ...
```

Metadata fields: `id`, `language`, `file`, `row`, `col`, `fim-row`, `fim-col`,
`viewportTop`, `viewportBottom`, `modified` (default true), `targets`
(comma-separated), `cursor-positions` (expands one scenario into multiple at
different positions).

Omit `-- expected --` to create a suppress scenario (tests that the provider
correctly produces nothing).

### Step DSL

Behavior and eval scenarios share the steps:

| Step                 | Syntax                                    | Effect                                                        |
| -------------------- | ----------------------------------------- | ------------------------------------------------------------- |
| `request-completion` | `request-completion [manual]`             | Request a completion. `manual` bypasses the request gates.    |
| `respond`            | `respond <start>-<end>` + `  \| "line"`   | Behavior only: the next provider answer replaces those lines. |
| `accept`             | `accept`                                  | Accept the completion or jump to the cursor target (Tab).     |
| `partial-accept`     | `partial-accept`                          | Accept the next word or line (S-Tab).                         |
| `reject`             | `reject`                                  | Press Esc: leave insert mode and clear.                       |
| `type`               | `type "text"`                             | Type at the cursor, entering insert mode when needed.         |
| `move`               | `move <row>:<col>`                        | Move the cursor.                                              |
| `wait`               | `wait <duration>`                         | Let time pass (e.g. `50ms`, `1s`).                            |
| `expect`             | `expect [flags]` + `buffer:` / `display:` | Check what is observed now.                                   |

`expect` flags: `shown` or `hidden` (a completion is displayed), `target=<line>`
or `target=none` (cursor target), `state=<name>` (engine state, e.g.
`HasCompletion`), `requests=<n>` (provider requests so far),
`cursor=<row>:<col>`. An indented `buffer:` section lists the expected buffer
lines as quoted strings; `display:` lists the displayed groups as
`<type> <line> [x<lines>] [<hint>]`.

### Scoring

- **deltaChrF** — edit quality (character n-gram F-score on the diff region,
  0–100)
- **Show rate** — fraction of quality scenarios where a completion was shown
- **Quiet rate** — fraction of suppress scenarios correctly producing nothing
- **Score** — `deltaChrF × gateScore / 100` where
  `gateScore = 2 × showRate × quietRate / (showRate + quietRate)`

### Baseline

`eval/baseline.json` is committed and acts as a regression ratchet:

```bash
just eval                      # updates baseline, exits 1 on regression
git diff eval/baseline.json    # review changes
git add eval/baseline.json     # commit alongside your code changes
```

CI runs `just eval-check` — if the committed baseline doesn't match replayed
results, the build fails.

### Recording

Cassettes are never auto-recorded. Missing cassettes fail with a message
pointing to the record command. Cassettes pin a `model_version`; replay fails if
the model doesn't match (override with `--strict-model=false` for local
iteration).

```bash
# Record missing cassettes for a target (hits real APIs)
CURSORTAB_EVAL_API_KEY=$KEY just eval-record <target>

# Single scenario
just eval-record <target> --scenario <id>

# Overwrite existing cassette
just eval-rerecord <target> <scenario>

# Copilot (requires a running nvim with Copilot attached)
just eval-record-copilot /tmp/nvim.sock --missing

# Windsurf (requires a running nvim with Windsurf authenticated)
just eval-record-windsurf /tmp/nvim.sock --missing
```

### Adding a New Provider Type

1. Implement `SetHTTPTransport(rt http.RoundTripper)` on the provider's HTTP
   client.
2. Add a case in `BuildProviderForTarget` (`server/scenario/factory.go`).
