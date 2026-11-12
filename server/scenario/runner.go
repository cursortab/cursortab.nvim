package scenario

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"cursortab/engine"
	"cursortab/eval/cassette"
	"cursortab/eval/clock"
	hist "cursortab/history"
	"cursortab/provider/sweep"
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
	Step        Step
	Observation Observation
	Failures    []string // expect steps: differences from the observation
	// Shown is true when a request step left a staged completion, displayed
	// or behind a cursor target.
	Shown bool
	// StagedLines is the buffer with every stage applied after a request
	// step, nil without a staged completion.
	StagedLines       []string
	ProviderLatencyMs int64
}

// TargetOutcome is the full result of running one scenario against one
// target, or of a behavior scenario.
type TargetOutcome struct {
	Target         Target
	Steps          []StepOutcome
	FinalBuffer    []string
	Cassette       *cassette.Cassette // cassette used (replay) or captured (record)
	TotalLatencyMs int64
	RequestCount   int
	Error          error
}

// Failures returns the differences of all expect steps, with their lines.
func (to *TargetOutcome) Failures() []string {
	var out []string
	for _, s := range to.Steps {
		for _, f := range s.Failures {
			out = append(out, fmt.Sprintf("steps line %d: %s", s.Step.Line, f))
		}
	}
	return out
}

// ScenarioOutcome bundles per-target results for a single scenario.
type ScenarioOutcome struct {
	Scenario *Scenario
	Targets  []*TargetOutcome
}

// Run executes the scenario: a behavior scenario once, an evaluation
// scenario against every declared target (optionally filtered).
func Run(sc *Scenario, cfg Config) *ScenarioOutcome {
	if sc.Behavior() {
		return &ScenarioOutcome{Scenario: sc, Targets: []*TargetOutcome{runBehavior(sc)}}
	}
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

// Engine settings of behavior scenarios: the plugin defaults, without idle
// completions, so that only the steps start requests.
const (
	textChangeDebounce = 50 * time.Millisecond
	behaviorTimeout    = 5 * time.Second
)

func behaviorConfig(providerName string) engine.Config {
	return engine.Config{
		ProviderName:        providerName,
		CompletionTimeout:   behaviorTimeout,
		IdleCompletionDelay: -1,
		TextChangeDebounce:  textChangeDebounce,
		CursorPrediction: engine.CursorPredictionConfig{
			Enabled:            true,
			AutoAdvance:        true,
			ProximityThreshold: 2,
		},
		MaxDiffTokens:          512,
		MaxVisibleLines:        12,
		CompleteInInsert:       true,
		CompleteInNormal:       true,
		DisableProviderMetrics: true,
	}
}

func runBehavior(sc *Scenario) *TargetOutcome {
	to := &TargetOutcome{Target: Target{Name: sc.Provider, Type: sc.Provider}}
	queue := &responses{}
	var prov engine.Provider = &scriptedProvider{responses: queue}
	if sc.Provider == ProviderSweep {
		p := sweep.NewProvider(&types.ProviderConfig{
			URL:               "http://sweep.test",
			CompletionPath:    "/v1/completions",
			MaxTokens:         512,
			CompletionTimeout: int(behaviorTimeout / time.Millisecond),
		})
		p.SetHTTPTransport(handlerTransport{handler: &sweepModel{responses: queue}})
		prov = p
	}
	s := newSession(sc, prov, behaviorConfig(sc.Provider), sc.Buffer.Row, sc.Buffer.Col)
	defer s.close()
	s.responses = queue
	s.run(sc.Steps, to)
	to.RequestCount = queue.count()
	return to
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
			to.Error = fmt.Errorf("copilot cannot be recorded from the standalone runner (no live Neovim LSP); populate cassette/%s.ndjson by hand or via a Neovim-side dumper", t.Name)
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
	s := newSession(sc, recordedProvider{Provider: prov}, engine.Config{
		ProviderName:        t.Name,
		CompletionTimeout:   30 * time.Second,
		IdleCompletionDelay: -1,
		TextChangeDebounce:  textChangeDebounce,
		CursorPrediction: engine.CursorPredictionConfig{
			Enabled: false,
		},
		CompleteInInsert:       true,
		CompleteInNormal:       true,
		DisableProviderMetrics: true,
	}, row, col)
	defer s.close()
	// Recorded latency attributes provider time to the steps: wall-clock
	// latency is about zero on replay.
	if replayer != nil {
		s.latencyMs = replayer.TotalDurationMs
	}
	if copilotLSP != nil {
		s.latencyMs = copilotLSP.TotalDurationMs
	}
	s.run(sc.Steps, to)

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

// session is one engine running a scenario on a fake buffer and clock.
type session struct {
	eng       *engine.Engine
	buf       *fakeBuffer
	clock     *clock.FakeClock
	state     string
	inInsert  bool
	responses *responses   // Behavior scenarios: answers of respond steps
	latencyMs func() int64 // Evaluation scenarios: recorded provider time so far
}

func newSession(sc *Scenario, prov engine.Provider, config engine.Config, row, col int) *session {
	fc := clock.New(time.Time{})
	var history []*types.DiffEntry
	for _, h := range sc.History {
		history = append(history, &types.DiffEntry{
			Original:    h.Original,
			Updated:     h.Updated,
			Source:      types.DiffSourceManual,
			TimestampNs: fc.Now().UnixNano(),
		})
	}
	store := hist.NewStore(3)
	store.Seed(sc.FilePath, sc.Buffer.Lines, history, fc.Now().UnixNano())

	s := &session{buf: newFakeBuffer(sc.FilePath, sc.Buffer, row, col), clock: fc}
	s.eng = engine.New(engine.Deps{
		Provider: prov,
		Buffer:   s.buf,
		Clock:    fc,
		History:  store,
	}, config)
	go s.eng.Run()
	return s
}

func (s *session) close() {
	s.eng.Close()
}

// run executes steps, starting in insert mode like an editor user who opened
// the buffer.
func (s *session) run(steps []Step, to *TargetOutcome) {
	if _, err := s.events("buf_enter", "insert_enter"); err != nil {
		to.Error = err
		return
	}
	s.inInsert = true
	for _, step := range steps {
		outcome, err := s.step(step)
		to.Steps = append(to.Steps, outcome)
		if err != nil {
			to.Error = fmt.Errorf("steps line %d (%s): %w", step.Line, step.Action, err)
			break
		}
	}
	to.FinalBuffer = s.buf.observe().Buffer
}

func (s *session) step(step Step) (StepOutcome, error) {
	out := StepOutcome{Step: step}
	var err error
	switch step.Action {
	case ActionRequestCompletion:
		var before int64
		if s.latencyMs != nil {
			before = s.latencyMs()
		}
		event := "text_changed"
		if step.Manual {
			event = "trigger_completion"
		}
		var snapshot engine.Snapshot
		snapshot, err = s.events(event)
		out.Shown = snapshot.StagedLines != nil
		out.StagedLines = snapshot.StagedLines
		if s.latencyMs != nil {
			out.ProviderLatencyMs = s.latencyMs() - before
		}
	case ActionRespond:
		if s.responses == nil {
			return out, fmt.Errorf("respond needs a scenario without targets")
		}
		s.responses.push(step.Edit)
	case ActionAccept:
		_, err = s.events("accept")
	case ActionPartialAccept:
		_, err = s.events("partial_accept")
	case ActionReject:
		// Escape leaves insert mode, and the cursor moves onto the previous
		// character, as in Neovim.
		_, err = s.events("esc", "insert_leave", "esc")
		s.inInsert = false
		s.buf.col = max(0, s.buf.col-1)
	case ActionType:
		if !s.inInsert {
			if _, err = s.events("insert_enter"); err != nil {
				break
			}
			s.inInsert = true
		}
		s.buf.insert(step.Text)
		_, err = s.events("text_changed")
	case ActionMove:
		s.buf.row, s.buf.col = step.Row, step.Col
		_, err = s.events("cursor_moved")
	case ActionWait:
		_, err = s.advance(step.Wait)
	case ActionExpect:
		out.Observation = s.observe()
		out.Failures = step.Expect.Check(out.Observation)
	}
	return out, err
}

// events sends editor events and waits until the engine settled.
func (s *session) events(names ...string) (engine.Snapshot, error) {
	if err := s.send(names...); err != nil {
		return engine.Snapshot{}, err
	}
	return s.settle()
}

func (s *session) send(names ...string) error {
	for _, name := range names {
		if engine.EventTypeFromString(name) == "" {
			return fmt.Errorf("unknown editor event %q", name)
		}
		s.eng.HandleEditorEvent(name, s.buf.position())
	}
	return nil
}

// advance moves the clock, which fires the timers due, and waits until the
// engine settled.
func (s *session) advance(d time.Duration) (engine.Snapshot, error) {
	s.clock.Advance(d)
	return s.settle()
}

func (s *session) settle() (engine.Snapshot, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	snapshot, err := s.eng.Settle(ctx)
	if err == nil {
		s.state = snapshot.State
	}
	return snapshot, err
}

func (s *session) observe() Observation {
	o := s.buf.observe()
	o.State = s.state
	if s.responses != nil {
		o.Requests = s.responses.count()
	}
	return o
}
