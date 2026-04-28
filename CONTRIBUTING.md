# Contributing

Contributions are welcome! Please open an issue or a pull request.

## Prerequisites

- Go 1.25.0+
- [just](https://github.com/casey/just) command runner

## Build & Test

```bash
just build             # build the server
just test              # all tests (unit, E2E pipeline, eval baseline check)
just test TestName     # run specific test(s)
just fmt               # format Go code
just lint              # check for dead code
```

## E2E Pipeline Tests

Pipeline tests verify ComputeDiff, CreateStages, and ToLuaFormat. They run as
part of `just test`. Fixtures are `.txtar` files in `server/text/testdata/`,
each containing `old.txt`, `new.txt`, and an `expected` section.

| Command | Who runs it |
| --- | --- |
| `just test-e2e` (run + generate HTML report) | agent or human |
| `just update-e2e [name]` (regenerate expected output) | agent or human |
| Review `server/text/testdata/report.html` | **human only** |
| `just verify-e2e [name]` (mark as reviewed) | human; agents only when explicitly asked |

Updated fixtures are marked as **unverified** and will fail `just test` until
the human runs `just verify-e2e <name>`. Verification state is tracked in
`server/text/testdata/verified.json` (a SHA256 manifest). In the HTML report,
verified passing tests are collapsed while unverified or failing tests are
shown expanded.

To add a new fixture, create a `.txtar` file in `server/text/testdata/` with
`old.txt`, `new.txt` sections, then run `just update-e2e <name>` to generate
the initial expected output. Stop there — the human reviews the report and
runs `just verify-e2e <name>`.

## Eval Harness

The eval harness measures completion quality and gating behavior across
providers. It replays recorded API responses (cassettes) offline — no network or
API cost. Results appear in the [benchmarks table](README.md#benchmarks).

### Quick Start

```bash
just eval                        # replay, update baseline, fail on regression
just eval-check                  # CI: fail if baseline differs (no update)
just eval --targets mercury      # filter by target
just eval --per-scenario=false   # aggregate table only
```

### Scenarios

Each scenario is a [txtar](https://pkg.go.dev/golang.org/x/tools/txtar) file in
`server/eval/scenarios/`. Targets are defined globally in
`server/eval/harness/targets.go`; scenarios reference them by name.

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
`viewportTop`, `viewportBottom`, `modified` (default true), `skipHistory`,
`targets` (comma-separated), `cursor-positions` (expands one scenario into
multiple at different positions).

Omit `-- expected --` to create a suppress scenario (tests that the provider
correctly produces nothing).

### Step DSL

| Verb                 | Syntax                        | Effect                                                          |
| -------------------- | ----------------------------- | --------------------------------------------------------------- |
| `request-completion` | `request-completion [manual]` | Run gating + provider + staging. `manual` bypasses suppression. |
| `accept`             | `accept`                      | Accept the staged completion.                                   |
| `reject`             | `reject`                      | Reject and clear.                                               |
| `wait`               | `wait <duration>`             | Advance the fake clock (e.g. `50ms`, `1s`).                     |

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
```

### Adding a New Provider Type

1. Implement `SetHTTPTransport(rt http.RoundTripper)` on the provider's HTTP
   client.
2. Add a case in `BuildProviderForTarget` (`server/eval/harness/factory.go`).
