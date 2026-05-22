package harness

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"cursortab/e2e"
	"cursortab/eval/cassette"

	"golang.org/x/tools/txtar"
)

// Scenario is a single evaluation fixture loaded from a .txtar file.
//
// A scenario runs against one or more Targets. A Target is the tuple
// (provider_type, model, url) — the thing being evaluated. Target names are
// arbitrary; the same provider type can appear under multiple names with
// different models/URLs, letting you A/B test model versions side-by-side.
//
// Targets are defined in harness.DefaultTargets(); scenarios just list
// which targets they use via the `targets:` header.
//
// Cassettes are stored as sidecar files next to the .txtar:
//
//	eval/scenarios/
//	  12-go-positional-to-options.txtar
//	  12-go-positional-to-options/
//	    mercuryapi.ndjson
//	    copilot.ndjson
//
// Fixture layout:
//
//	<free-text description line>
//	id: my-scenario
//	language: go
//	file: store.go
//	row: 2
//	col: 16
//	viewportTop: 1
//	viewportBottom: 20
//	targets: sweep-next-edit-1.5B, sweep-next-edit-7B, mercuryapi, zeta
//
//	-- buffer.txt --
//	...starting buffer contents...
//	-- history --           (optional: recent diff history)
//	<|file|> main.go
//	<|old|>
//	old line
//	<|new|>
//	new line
//	-- steps --
//	request-completion
//	  expect shown stageCount=1
//	-- expected --
//	...the ideal final buffer state used for quality scoring...
type Scenario struct {
	Path            string
	ID              string
	Description     string
	Language        string
	FilePath        string
	Targets         []Target
	Buffer          BufferState
	FIMRow          int // cursor override for FIM providers (0 = use Buffer.Row)
	FIMCol          int
	History         []DiffEntryState
	Steps           []Step
	Expected        []string // ideal final buffer lines (for quality scoring)
	Cassettes       map[string]*cassette.Cassette
	CursorPositions [][2]int // extra (row,col) pairs; LoadScenario expands these into separate scenarios
}

// Target is one evaluation target: a provider type configured with a
// specific model and URL. Targets have arbitrary names so the same provider
// type can appear multiple times (e.g. sweep-v1 vs sweep-v2).
type Target struct {
	Name  string // "sweep-v1", "mercuryapi", etc.
	Type  string // "mercuryapi", "zeta", "zeta-2"
	Model string // model version id
	URL   string // provider endpoint; empty = default
}

// TargetNames returns just the names for display/filtering.
func (s *Scenario) TargetNames() []string {
	out := make([]string, 0, len(s.Targets))
	for _, t := range s.Targets {
		out = append(out, t.Name)
	}
	return out
}

// TargetByName returns the target with the given name, or nil.
func (s *Scenario) TargetByName(name string) *Target {
	for i := range s.Targets {
		if s.Targets[i].Name == name {
			return &s.Targets[i]
		}
	}
	return nil
}

// CassetteDir returns the sidecar directory path where cassettes are stored
// for this scenario. It's the .txtar path with the extension stripped.
func (s *Scenario) CassetteDir() string {
	return strings.TrimSuffix(s.Path, ".txtar")
}

// BufferState seeds the EvalBuffer at the start of a scenario.
type BufferState struct {
	Lines          []string
	Row            int
	Col            int
	ViewportTop    int
	ViewportBottom int
	Modified       *bool // nil = default (true)
	SkipHistory    *bool // nil = default (false)
}

// DiffEntryState is a recent-edit history entry (seeded into diff history
// before the scenario runs).
type DiffEntryState struct {
	FileName string
	Original string
	Updated  string
}

// Step is one action in the scenario's step list.
type Step struct {
	Action  StepAction
	Wait    time.Duration // for ActionWait
	Manual  bool          // for ActionRequestCompletion: bypass gating
	Comment string
}

// StepAction enumerates what a step does.
type StepAction string

const (
	ActionRequestCompletion StepAction = "request-completion"
	ActionAccept            StepAction = "accept"
	ActionReject            StepAction = "reject"
	ActionWait              StepAction = "wait"
)

// LoadScenario parses a .txtar file into one or more Scenarios. When the
// header contains `cursor-positions:`, the file is expanded into one scenario
// per position (each sharing the same buffer, steps, and cassettes). The
// base row/col from the header is always included as the first position.
func LoadScenario(path string, targets map[string]Target) ([]*Scenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read scenario: %w", err)
	}
	sc, err := ParseScenario(data, targets)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	sc.Path = path
	if sc.ID == "" {
		sc.ID = strings.TrimSuffix(filepath.Base(path), ".txtar")
	}
	if err := sc.loadSidecarCassettes(); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}

	if len(sc.CursorPositions) == 0 {
		return []*Scenario{sc}, nil
	}

	// Expand: the base row/col is the first position, then the extras.
	positions := append([][2]int{{sc.Buffer.Row, sc.Buffer.Col}}, sc.CursorPositions...)
	out := make([]*Scenario, 0, len(positions))
	for _, pos := range positions {
		clone := *sc
		clone.ID = fmt.Sprintf("%s@L%d", sc.ID, pos[0])
		clone.Buffer.Row = pos[0]
		clone.Buffer.Col = pos[1]
		out = append(out, &clone)
	}
	return out, nil
}

// loadSidecarCassettes loads cassettes from the companion directory next to
// the .txtar file (e.g. 02-js-greet-return/*.ndjson).
func (sc *Scenario) loadSidecarCassettes() error {
	dir := sc.CassetteDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no cassettes directory — fine
		}
		return fmt.Errorf("read cassette dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ndjson") {
			continue
		}
		targetName := strings.TrimSuffix(e.Name(), ".ndjson")
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return fmt.Errorf("cassette %s: %w", targetName, err)
		}
		cs, err := cassette.Parse(bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("cassette %s: %w", targetName, err)
		}
		sc.Cassettes[targetName] = cs
	}
	return nil
}

// ParseScenario parses a raw .txtar blob. Target names referenced in the
// `targets:` header are resolved against the provided target map. If targets
// is nil, bare names default their type to the name itself.
func ParseScenario(data []byte, targets map[string]Target) (*Scenario, error) {
	ar := txtar.Parse(data)
	sc := &Scenario{
		Cassettes: make(map[string]*cassette.Cassette),
		Buffer: BufferState{
			ViewportTop:    1,
			ViewportBottom: 1000,
		},
	}

	hdr := e2e.ParseHeader(ar.Comment)

	sc.Description = hdr[""]
	if v, ok := hdr["description"]; ok {
		sc.Description = v
	}
	sc.ID = hdr["id"]
	sc.Language = hdr["language"]
	sc.FilePath = hdr["file"]
	sc.Buffer.Row, _ = strconv.Atoi(hdr["row"])
	sc.Buffer.Col, _ = strconv.Atoi(hdr["col"])
	sc.FIMRow, _ = strconv.Atoi(hdr["fim-row"])
	sc.FIMCol, _ = strconv.Atoi(hdr["fim-col"])
	if v, ok := hdr["viewportTop"]; ok {
		n, _ := strconv.Atoi(v)
		sc.Buffer.ViewportTop = n
	}
	if v, ok := hdr["viewportBottom"]; ok {
		n, _ := strconv.Atoi(v)
		sc.Buffer.ViewportBottom = n
	}
	if v, ok := hdr["modified"]; ok {
		b := v == "true"
		sc.Buffer.Modified = &b
	}
	if v, ok := hdr["skipHistory"]; ok {
		b := v == "true"
		sc.Buffer.SkipHistory = &b
	}
	if v, ok := hdr["cursor-positions"]; ok {
		positions, err := parseCursorPositions(v)
		if err != nil {
			return nil, fmt.Errorf("cursor-positions: %w", err)
		}
		sc.CursorPositions = positions
	}

	var targetNames []string
	if v, ok := hdr["targets"]; ok {
		for _, p := range strings.Split(v, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				targetNames = append(targetNames, p)
			}
		}
	}

	// Resolve targets. When the scenario lists explicit targets, use those.
	// When omitted, run against every target in the shared map.
	if len(targetNames) == 0 {
		for _, t := range targets {
			sc.Targets = append(sc.Targets, t)
		}
	} else {
		for _, name := range targetNames {
			if t, ok := targets[name]; ok {
				sc.Targets = append(sc.Targets, t)
			} else {
				sc.Targets = append(sc.Targets, Target{Name: name, Type: name})
			}
		}
	}

	for _, f := range ar.Files {
		name := f.Name
		content := strings.TrimSuffix(string(f.Data), "\n")

		switch {
		case name == "buffer.txt":
			if content == "" {
				sc.Buffer.Lines = nil
			} else {
				sc.Buffer.Lines = strings.Split(content, "\n")
			}
		case name == "expected" || name == "expected.txt":
			if content == "" {
				sc.Expected = nil
			} else {
				sc.Expected = strings.Split(content, "\n")
			}
		case name == "history":
			hist, err := parseHistory(content)
			if err != nil {
				return nil, fmt.Errorf("history: %w", err)
			}
			sc.History = hist
		case name == "steps":
			steps, err := ParseSteps(content)
			if err != nil {
				return nil, fmt.Errorf("steps: %w", err)
			}
			sc.Steps = steps
		case strings.HasPrefix(name, "cassette/"):
			// Support inline cassettes for backward compatibility and tests.
			targetName := strings.TrimSuffix(strings.TrimPrefix(name, "cassette/"), ".ndjson")
			cs, err := cassette.Parse(bytes.NewReader(f.Data))
			if err != nil {
				return nil, fmt.Errorf("cassette %s: %w", targetName, err)
			}
			sc.Cassettes[targetName] = cs
		}
	}

	if sc.Buffer.Row == 0 {
		sc.Buffer.Row = 1
	}
	if sc.Buffer.ViewportBottom <= 0 {
		sc.Buffer.ViewportBottom = len(sc.Buffer.Lines) + 20
	}
	if sc.FilePath == "" {
		sc.FilePath = "eval." + extForLanguage(sc.Language)
	}
	if len(sc.Targets) == 0 {
		for name := range sc.Cassettes {
			sc.Targets = append(sc.Targets, Target{Name: name, Type: name})
		}
	}
	return sc, nil
}

// extForLanguage maps a language id to a plausible file extension.
func extForLanguage(lang string) string {
	switch lang {
	case "go":
		return "go"
	case "python":
		return "py"
	case "typescript", "ts":
		return "ts"
	case "javascript", "js":
		return "js"
	case "rust":
		return "rs"
	default:
		return "txt"
	}
}

// parseCursorPositions parses "row:col, row:col, ..." into position pairs.
func parseCursorPositions(s string) ([][2]int, error) {
	var out [][2]int
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		parts := strings.SplitN(p, ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid position %q (want row:col)", p)
		}
		row, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil {
			return nil, fmt.Errorf("invalid row in %q: %w", p, err)
		}
		col, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil {
			return nil, fmt.Errorf("invalid col in %q: %w", p, err)
		}
		out = append(out, [2]int{row, col})
	}
	return out, nil
}

// parseHistory parses a simple diff-history section.
// Each entry is delimited by <|file|> then <|old|>/<|new|> blocks.
func parseHistory(s string) ([]DiffEntryState, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var out []DiffEntryState
	lines := strings.Split(s, "\n")
	i := 0
	for i < len(lines) {
		if !strings.HasPrefix(lines[i], "<|file|>") {
			i++
			continue
		}
		entry := DiffEntryState{
			FileName: strings.TrimSpace(strings.TrimPrefix(lines[i], "<|file|>")),
		}
		i++
		var mode string
		var oldBuf, newBuf []string
		for i < len(lines) && !strings.HasPrefix(lines[i], "<|file|>") {
			line := lines[i]
			switch line {
			case "<|old|>":
				mode = "old"
			case "<|new|>":
				mode = "new"
			default:
				switch mode {
				case "old":
					oldBuf = append(oldBuf, line)
				case "new":
					newBuf = append(newBuf, line)
				}
			}
			i++
		}
		entry.Original = strings.Join(oldBuf, "\n")
		entry.Updated = strings.Join(newBuf, "\n")
		out = append(out, entry)
	}
	return out, nil
}
