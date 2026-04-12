package harness

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"cursortab/engine"
	"cursortab/eval/cassette"
	"cursortab/eval/clock"
	"cursortab/types"
)

// Mode selects how the runner handles missing cassettes and reports results.
type Mode int

const (
	// ModeReplay runs scenarios against cassette-backed providers. Fails if a
	// required cassette is missing.
	ModeReplay Mode = iota
	// ModeRecord hits the wrapped (real) transport, records all HTTP
	// interactions, and writes them back as cassettes.
	ModeRecord
)

// Config controls a runner invocation.
type Config struct {
	// TargetFilter restricts execution to targets whose name is in this list.
	// When empty, every target declared by the scenario is run. The filter
	// never *adds* targets — it only narrows the scenario's declared set.
	TargetFilter []string
	// Mode controls replay vs record.
	Mode Mode
	// Transport is the real http.RoundTripper used in Record mode. Ignored in
	// Replay mode. Defaults to http.DefaultTransport.
	Transport http.RoundTripper
	// BaseConfig provides API keys and editor metadata applied to every
	// target. Target-level overrides (Model, URL) take precedence.
	BaseConfig *types.ProviderConfig
	// StrictModelVersion fails replay when a cassette's meta model_version
	// doesn't match the target's configured model.
	StrictModelVersion bool
}

// StepOutcome records what happened for one step of a scenario run.
type StepOutcome struct {
	Step              Step
	Shown             bool
	Suppressed        bool
	SuppressReason    string
	StageCount        int
	StagedLines       []string
	ProviderLatencyMs int64
	Err               error
}

// TargetOutcome is the full result of running one scenario against one
// target.
type TargetOutcome struct {
	Target         Target
	Steps          []StepOutcome
	FinalBuffer    []string
	Cassette       *cassette.Cassette // cassette used (replay) or captured (record)
	TotalLatencyMs int64
	RequestCount   int
	Error          error
}

// ScenarioOutcome bundles per-target results for a single scenario.
type ScenarioOutcome struct {
	Scenario *Scenario
	Targets  []*TargetOutcome
}

// Run executes one scenario against every declared target (optionally
// filtered) and returns an outcome per target.
func Run(sc *Scenario, cfg Config) *ScenarioOutcome {
	targets := resolveTargets(sc, cfg.TargetFilter)

	outcome := &ScenarioOutcome{
		Scenario: sc,
		Targets:  make([]*TargetOutcome, len(targets)),
	}
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func(i int, t Target) {
			defer wg.Done()
			outcome.Targets[i] = runTarget(sc, t, cfg)
		}(i, t)
	}
	wg.Wait()
	return outcome
}

// resolveTargets returns the set of scenario targets that survive the
// filter. Filter is an intersection, not a replacement: a target in filter
// but not declared by the scenario is silently dropped.
func resolveTargets(sc *Scenario, filter []string) []Target {
	if len(filter) == 0 {
		return append([]Target{}, sc.Targets...)
	}
	allow := make(map[string]bool, len(filter))
	for _, f := range filter {
		allow[f] = true
	}
	out := make([]Target, 0, len(sc.Targets))
	for _, t := range sc.Targets {
		if allow[t.Name] {
			out = append(out, t)
		}
	}
	return out
}

func runTarget(sc *Scenario, t Target, cfg Config) *TargetOutcome {
	to := &TargetOutcome{Target: t}

	// Build the transport and/or LSP client, per target type. HTTP targets
	// use a cassette replayer transport; copilot goes through an LSP
	// cassette client that doesn't touch the HTTP layer at all.
	var transport http.RoundTripper
	var replayer *cassette.Replayer
	var copilotLSP *cassetteCopilotLSP
	var recorder *cassette.Recorder

	switch cfg.Mode {
	case ModeReplay:
		cs, ok := sc.Cassettes[t.Name]
		if !ok {
			// No cassette — use an empty one. This is fine for gating-only
			// scenarios that suppress before the provider is called. If the
			// engine does try to call the provider, the replayer will return
			// a "cassette exhausted" error on the first request.
			cs = cassette.New(t.Type, "")
		}
		if cfg.StrictModelVersion && t.Model != "" && cs.Meta.ModelVersion != "" && cs.Meta.ModelVersion != t.Model {
			to.Error = fmt.Errorf("cassette model_version %q != target model %q", cs.Meta.ModelVersion, t.Model)
			return to
		}
		to.Cassette = cs
		// Only HTTP targets get the replayer transport. Copilot targets
		// use the cassette directly via the LSP client built inside
		// BuildProviderForTarget — they never touch this transport.
		if t.Type == "copilot" {
			copilotLSP = newCassetteCopilotLSP(cs)
		} else {
			replayer = cassette.NewReplayer(cs)
			transport = replayer
		}
	case ModeRecord:
		if t.Type == "copilot" {
			to.Error = fmt.Errorf("copilot cannot be recorded from the standalone harness (no live Neovim LSP); populate cassette/%s.ndjson by hand or via a Neovim-side dumper", t.Name)
			return to
		}
		inner := cfg.Transport
		if inner == nil {
			inner = http.DefaultTransport
		}
		recorder = cassette.NewRecorder(inner)
		recorder.RecordHeaders = true
		transport = recorder
	}

	prov, err := BuildProviderForTarget(t, cfg.BaseConfig, transport, to.Cassette, copilotLSP)
	if err != nil {
		to.Error = err
		return to
	}

	row, col := sc.Buffer.Row, sc.Buffer.Col
	if t.Type == "fim" && sc.FIMRow > 0 {
		row, col = sc.FIMRow, sc.FIMCol
	}
	buf := NewEvalBuffer(sc.FilePath, sc.Buffer.Lines, row, col)
	buf.SetViewport(sc.Buffer.ViewportTop, sc.Buffer.ViewportBottom)
	if sc.Buffer.Modified != nil {
		buf.SetModified(*sc.Buffer.Modified)
	}
	if sc.Buffer.SkipHistory != nil {
		buf.SetSkipHistory(*sc.Buffer.SkipHistory)
	}
	if len(sc.History) > 0 {
		entries := make([]*types.DiffEntry, 0, len(sc.History))
		for _, h := range sc.History {
			entries = append(entries, &types.DiffEntry{
				Original:    h.Original,
				Updated:     h.Updated,
				Source:      types.DiffSourceManual,
				TimestampNs: time.Now().UnixNano(),
			})
		}
		buf.diffHistories = entries
	}

	fc := clock.New(time.Time{})
	eng, err := engine.NewEngine(prov, buf, engine.EngineConfig{
		ProviderName:        t.Name,
		CompletionTimeout:   30 * time.Second,
		IdleCompletionDelay: -1,
		TextChangeDebounce:  -1,
		CursorPrediction: engine.CursorPredictionConfig{
			Enabled: false,
		},
		CompleteInInsert:       true,
		CompleteInNormal:       true,
		EditCompletionProvider: t.Type != "fim" && t.Type != "inline",
		DisableProviderMetrics: true,
	}, fc, nil, nil)
	if err != nil {
		to.Error = fmt.Errorf("engine init: %w", err)
		return to
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng.Start(ctx)
	defer eng.Stop()

	for i, step := range sc.Steps {
		// Snapshot replayer/copilot duration before the step so we can
		// attribute recorded latency to this specific step. Wall-clock
		// latency is always ~0 on replay, so we derive step latency from
		// the cassette interactions the step consumed.
		beforeTotal := int64(0)
		if replayer != nil {
			beforeTotal = replayer.TotalDurationMs()
		}
		if copilotLSP != nil {
			beforeTotal = copilotLSP.TotalDurationMs()
		}
		outcome := runStep(eng, fc, step)
		outcome.Step = step
		if replayer != nil && step.Action == ActionRequestCompletion {
			outcome.ProviderLatencyMs = replayer.TotalDurationMs() - beforeTotal
		}
		if copilotLSP != nil && step.Action == ActionRequestCompletion {
			outcome.ProviderLatencyMs = copilotLSP.TotalDurationMs() - beforeTotal
		}
		to.Steps = append(to.Steps, outcome)
		if outcome.Err != nil {
			to.Error = fmt.Errorf("step %d (%s): %w", i, step.Action, outcome.Err)
			break
		}
	}

	to.FinalBuffer = buf.Snapshot()

	if recorder != nil {
		to.Cassette = recorder.Cassette(t.Type, t.Model)
		to.RequestCount = len(to.Cassette.Interactions)
		for _, it := range to.Cassette.Interactions {
			to.TotalLatencyMs += it.DurationMs
		}
	}
	if replayer != nil {
		to.RequestCount = replayer.Used()
		to.TotalLatencyMs = replayer.TotalDurationMs()
	}
	if copilotLSP != nil {
		to.RequestCount = copilotLSP.Used()
		to.TotalLatencyMs = copilotLSP.TotalDurationMs()
	}
	return to
}

func runStep(eng *engine.Engine, fc *clock.FakeClock, step Step) StepOutcome {
	var out StepOutcome
	switch step.Action {
	case ActionRequestCompletion:
		reqCtx, reqCancel := context.WithTimeout(context.Background(), 30*time.Second)
		res, err := eng.EvalRequestCompletion(reqCtx, step.Manual)
		reqCancel()
		if err != nil {
			out.Err = err
			return out
		}
		out.Shown = res.Shown
		out.Suppressed = res.Suppressed
		out.SuppressReason = res.SuppressReason
		out.StageCount = res.StageCount
		out.StagedLines = res.StagedLines
		out.ProviderLatencyMs = res.ProviderLatency.Milliseconds()
	case ActionAccept:
		eng.EvalAccept()
	case ActionReject:
		eng.EvalReject()
	case ActionWait:
		fc.Advance(step.Wait)
	}

	return out
}
