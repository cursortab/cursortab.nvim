# Contributing

Contributions are welcome! Please open an issue or a pull request.

## Build

```bash
cd server && go build
```

## Test

Run all tests:

```bash
cd server && go test ./...
```

Run a specific package:

```bash
cd server && go test ./text/...
```

## E2E Pipeline Tests

The E2E tests verify the full pipeline: ComputeDiff, CreateStages, and
ToLuaFormat.

```bash
# Run E2E tests
cd server && go test ./text/... -run TestE2E -v

# Record new expected output after changes
cd server && go test ./text/... -run TestE2E -update
```

Updated fixtures are marked as **unverified**. After reviewing the generated
`expected.json` files and the HTML report, mark a specific fixture as verified:

```bash
cd server && go test ./text/... -run TestE2E -verify <name>
```

Verification state is tracked in `server/text/e2e/verified.json` (a SHA256
manifest). In the HTML report, verified passing tests are collapsed while
unverified or failing tests are shown expanded.

Each fixture is a directory under `server/text/e2e/` with `old.txt`, `new.txt`,
`params.json`, and `expected.json`. Both batch and incremental pipelines are
verified against the same expected output. An HTML report is generated at
`server/text/e2e/report.html`.

To add a new fixture manually, create a directory with the four files and run
with `-update` to generate the initial `expected.json`, then review and
`-verify-case=<name>` to mark it verified.

## Eval Harness

Runs scenarios through the real engine with recorded API responses
(cassettes), scores output quality (deltaChrF), and tracks gating
behavior (show rate, quiet rate). Regressions are caught by comparing
against a committed baseline JSON file.

A **target** is the unit of evaluation: a `(provider_type, model, url)`
tuple with an arbitrary name. The same provider type can appear under
multiple names with different models (e.g. `sweep-v1` vs `sweep-v2`),
each with its own cassette. Scenarios list which targets they use via
the `targets:` header.

### Workflow

```
# 1. Write a .txtar scenario (buffer, steps, expected, target list)
vim eval/scenarios/my-scenario.txtar

# 2. Record real provider responses once — one target at a time
# Hosted providers (API key only)
CURSORTAB_EVAL_API_KEY=$MERCURY_KEY  just eval-record mercuryapi

# Local providers (URL to your inference server)
CURSORTAB_EVAL_URL=https://my-server  CURSORTAB_EVAL_API_KEY=$KEY  just eval-record sweep-v1

# 3. Review the committed cassettes in the diff, then commit everything
git add eval/scenarios/my-scenario.txtar
git commit -m 'eval: add my-scenario'

# 4. CI / local runs replay offline — no network, no API cost
just eval
```

### Fixture format

Each scenario is a [txtar](https://pkg.go.dev/golang.org/x/tools/txtar)
file. Header lines carry metadata; named sections hold buffer content,
steps, expected output, and cassettes.

```
Description line goes first, free-form.
id: my-scenario
language: go
file: store.go
row: 6
col: 17
viewportTop: 1
viewportBottom: 20
modified: true          # default true; set false for no-edits gating tests

# Preferred: declare each target explicitly.
targets: sweep-v1, sweep-v2, mercury
target.sweep-v1: type=sweepapi model=next-edit-v1 url=https://a.sweep.dev/backend/next_edit_autocomplete
target.sweep-v2: type=sweepapi model=next-edit-v2 url=https://b.sweep.dev/backend/next_edit_autocomplete
target.mercury:  type=mercuryapi model=mercury-edit-20251201

# Backward compat: a bare providers: list synthesizes one target per entry.
# Use this when you don't need custom names. (Keys: target.name == provider type.)
# providers: sweepapi, mercuryapi, zeta

-- buffer.txt --
package store
...

-- steps --
request-completion manual
accept

-- expected --
package store
... ideal final state used for quality scoring ...

-- cassette/sweep-v1.ndjson --
{"kind":"meta","schema_version":1,"provider":"sweepapi","model_version":"next-edit-v1","recorded_at":"..."}
{"kind":"request","interaction":0,"method":"POST","url":"...","body_b64":"..."}
{"kind":"response","interaction":0,"status":200,"body_b64":"...","duration_ms":201}

-- cassette/sweep-v2.ndjson --
...

-- cassette/mercury.ndjson --
...
```

Each target's cassette lives at `cassette/<target-name>.ndjson`. Two
targets of the same provider type get two independent cassettes.

### Step DSL

| Verb | Syntax | Effect |
|------|--------|--------|
| `request-completion` | `request-completion [manual]` | Runs gating + provider + staging. Add `manual` to bypass suppression layers (matches production manual-trigger). |
| `accept` | `accept` | Accepts the current staged completion. |
| `reject` | `reject` | Rejects and clears the current completion. |
| `wait` | `wait 50ms` | Advances the fake clock by a duration. |

### Quality metrics

The run command computes, per `(scenario, target)`:

- **deltaChrF** — character n-gram F-score (Popović 2015) on the diff
  region only. Range `[0, 100]`, higher is better.
- **Latency p50 / p90** — read from the cassette (`duration_ms` per
  interaction). Stable across re-runs of the same cassette.

The aggregate report computes, per target:

- **Show rate** — fraction of quality scenarios where the provider
  produced a completion. "When there's something to say, does it
  show up?"
- **Quiet rate** — fraction of suppress scenarios (no `-- expected --`
  section) where the provider correctly produced nothing. "When
  there's nothing to say, does it stay quiet?"
- **Score** — the headline metric combining quality and gating behavior:
  ```
  gateScore = 2 × showRate × quietRate / (showRate + quietRate)
  score     = deltaChrF × gateScore / 100
  ```
  A target needs both good completions (high deltaChrF) and good gating
  (high show rate + high quiet rate) to rank high. If it writes great
  code but fires on every keystroke, the score drops. If it's correctly
  quiet but produces poor output, the score also drops.

### Baseline regression guard

The `--baseline` flag writes per-target quality metrics (score,
deltaChrF, showRate, quietRate) to a JSON file after each run. The file
is committed to the repo and acts as a ratchet:

- **Default mode** (`just eval`): compares current metrics against the
  baseline, overwrites the file with current values, then exits 0 if
  metrics held or improved, exit 1 if any metric regressed.
- **Check mode** (`just eval-check`): compares without updating the
  file. Exits 1 if any metric differs from the baseline. Designed for
  CI where the committed baseline must match exactly.

Workflow after making engine changes:

```
just eval              # runs eval, updates baseline.json, exits 1 if regression
git diff               # review the baseline.json diff
git add eval/baseline.json
git commit             # commit the new baseline alongside your changes
```

CI runs `just eval-check` — if the committed baseline doesn't match
the replayed results, the build fails.

### Strict cassette versioning

Cassettes are pinned to a `model_version` string in the meta line. On
replay, `--strict-model=true` (default) fails loudly if the runtime
asks for a different model. This makes the "record once, replay
forever" contract real: model upgrades become deliberate, visible
events in git history.

Override for local iteration with `--strict-model=false`.

### Recording

Missing cassettes never auto-record during `run` — it fails with a
clear message pointing at `record`. This prevents silent API spend.

Copilot cassettes can't be recorded via `cursortab-eval record`
(no Neovim attached). Use `just eval-record-copilot <socket>` with a
running nvim session instead.

### Adding a new provider type

1. Implement `SetHTTPTransport(rt http.RoundTripper)` on the provider's
   HTTP client.
2. Add a case in `harness.BuildProviderForTarget`.
3. Add the type to `harness.KnownProviderTypes`.

### CLI reference

```
# Run (replay, offline)
just eval                                        # all scenarios, all targets, update baseline
just eval-check                                  # CI mode: fail if baseline differs
just eval --targets mercury,sweep-v1             # only these target names
just eval --per-scenario=false                   # aggregate table only

# Record (hits real APIs)
just eval-record mercury                          # record every scenario that declares 'mercury'
just eval-record sweep-v1 --scenario go-error-wrapping
just eval-rerecord sweep-v2 multi-model-sweep     # overwrite one pair
```

The target filter is an **intersection**, not a replacement: passing
`--targets sweep-v1` only runs scenarios that already declare `sweep-v1`
in their target list. Recording against a target the scenario doesn't
declare is silently skipped — `just eval-record` reports the count so
you know how many scenarios were untouched.

### File layout

```
server/eval/
├── clock/            deterministic FakeClock
├── cassette/         NDJSON format + record/replay RoundTrippers
├── harness/          engine harness, scenario parser, runner
├── metrics/          chrF, diff edit distance, combined scoring
├── scenarios/        .txtar fixtures (the evaluation dataset)
└── cmd/cursortab-eval/
    ├── main.go
    ├── baseline.go
    ├── record.go
    └── run.go
```

### Engine integration

The harness uses `engine.EvalRequestCompletion(ctx, manual)` — a
synchronous entry point that runs the full gating + provider + staging
pipeline deterministically. Time is controlled by `eval/clock.FakeClock`
which only advances on explicit `Advance()` calls, making runs
reproducible across cassette replays.
