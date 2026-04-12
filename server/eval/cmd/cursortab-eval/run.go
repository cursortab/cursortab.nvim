package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"cursortab/eval/harness"
	"cursortab/eval/metrics"
)

// perScenarioScore holds a row of the per-scenario report table.
type perScenarioScore struct {
	scenarioID string
	targetName string
	score      metrics.Score
}

func runCmd(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	var (
		dir          = fs.String("scenarios", "eval/scenarios", "directory of .txtar scenario fixtures")
		filterFlag   = fs.String("targets", "", "comma-separated target names (filter; only runs targets the scenario already declares)")
		strictModel  = fs.Bool("strict-model", true, "fail if cassette model_version doesn't match target model")
		showPerScen  = fs.Bool("per-scenario", true, "include per-scenario breakdown in the quality report")
		baselineFile = fs.String("baseline", "", "path to baseline JSON file; writes current metrics after each run")
		checkOnly    = fs.Bool("check", false, "compare against baseline without updating it (exit 1 if different)")
	)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}

	scenarios, err := loadScenarios(*dir)
	if err != nil {
		return err
	}
	if len(scenarios) == 0 {
		return fmt.Errorf("no scenarios found in %s", *dir)
	}

	filter := splitCSV(*filterFlag)

	cfg := harness.Config{
		TargetFilter:       filter,
		Mode:               harness.ModeReplay,
		StrictModelVersion: *strictModel,
	}

	// Run all scenarios in parallel with a bounded worker pool.
	outcomes := make([]*harness.ScenarioOutcome, len(scenarios))
	sem := make(chan struct{}, runtime.NumCPU())
	var wg sync.WaitGroup
	for i, sc := range scenarios {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, sc *harness.Scenario) {
			defer wg.Done()
			defer func() { <-sem }()
			outcomes[i] = harness.Run(sc, cfg)
		}(i, sc)
	}
	wg.Wait()

	// Collect results sequentially (deterministic output order).
	perTarget := make(map[string][]metrics.Score)
	perScenario := []perScenarioScore{}
	suppressStats := make(map[string]*suppressCount)
	targetTypes := make(map[string]string)

	for i, sc := range scenarios {
		isSuppress := len(sc.Expected) == 0
		outcome := outcomes[i]
		for _, to := range outcome.Targets {
			targetTypes[to.Target.Name] = to.Target.Type
			if to.Error != nil {
				fmt.Fprintf(os.Stderr, "[error] %s / %s: %v\n", sc.ID, to.Target.Name, to.Error)
				continue
			}
			if isSuppress {
				sc := suppressStats[to.Target.Name]
				if sc == nil {
					sc = &suppressCount{}
					suppressStats[to.Target.Name] = sc
				}
				sc.total++
				anyShown := false
				for _, step := range to.Steps {
					if step.Shown {
						anyShown = true
						break
					}
				}
				if !anyShown {
					sc.correct++
				}
			}
			if len(sc.Expected) > 0 {
				var latencyMs int64
				var shown bool
				produced := to.FinalBuffer
				producedSet := false
				for _, step := range to.Steps {
					if step.ProviderLatencyMs > latencyMs {
						latencyMs = step.ProviderLatencyMs
					}
					if !producedSet && len(step.StagedLines) > 0 {
						produced = step.StagedLines
						producedSet = true
					}
					if step.Shown {
						shown = true
					}
				}
				score := metrics.Compute(sc.Buffer.Lines, produced, sc.Expected, latencyMs, shown)
				perTarget[to.Target.Name] = append(perTarget[to.Target.Name], score)
				perScenario = append(perScenario, perScenarioScore{
					scenarioID: sc.ID,
					targetName: to.Target.Name,
					score:      score,
				})
			}
		}
	}

	renderQualityReport(perTarget, perScenario, suppressStats, targetTypes, *showPerScen)

	if *baselineFile != "" {
		cur := buildBaseline(perTarget, suppressStats, targetTypes)
		old, err := loadBaseline(*baselineFile)
		if err != nil {
			return fmt.Errorf("load baseline: %w", err)
		}
		diffs := compareBaselines(old, cur)
		hasRegression := false
		for _, d := range diffs {
			if d.regressed() {
				hasRegression = true
				break
			}
		}
		if len(diffs) > 0 {
			fmt.Fprintf(os.Stderr, "\nbaseline diff:\n")
			printBaselineDiffs(diffs)
		}
		if !*checkOnly {
			if err := saveBaseline(*baselineFile, cur); err != nil {
				return fmt.Errorf("save baseline: %w", err)
			}
		}
		if *checkOnly && len(diffs) > 0 {
			return fmt.Errorf("baseline mismatch (run without --check to update)")
		}
		if hasRegression {
			return fmt.Errorf("baseline regression detected")
		}
	}
	return nil
}

func loadScenarios(dir string) ([]*harness.Scenario, error) {
	targets := harness.DefaultTargets()

	var out []*harness.Scenario
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".txtar") {
			return nil
		}
		scenarios, err := harness.LoadScenario(path, targets)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, scenarios...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
