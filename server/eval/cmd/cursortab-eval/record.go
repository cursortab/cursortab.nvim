package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"cursortab/eval/cassette"
	"cursortab/eval/harness"
	"cursortab/types"
)

func recordCmd(args []string) error {
	fs := flag.NewFlagSet("record", flag.ContinueOnError)
	var (
		dir         = fs.String("scenarios", "eval/scenarios", "directory of .txtar scenario fixtures")
		targetFlag  = fs.String("target", "", "target name to record (required; matches scenarios' target list)")
		scenario    = fs.String("scenario", "", "scenario id filter (empty = all)")
		onlyMissing = fs.Bool("missing", false, "only record scenarios that don't yet have a cassette for this target")
		apiURL      = fs.String("url", "", "provider API url (overrides the target's URL)")
		apiKey      = fs.String("api-key", "", "API key (falls back to CURSORTAB_EVAL_API_KEY)")
		model       = fs.String("model", "", "model id to annotate in the cassette (overrides target's model)")
		dry         = fs.Bool("dry-run", false, "parse scenarios but don't hit the network")
	)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}

	targetName := *targetFlag
	if targetName == "" {
		return fmt.Errorf("--target is required")
	}

	key := *apiKey
	if key == "" {
		key = os.Getenv("CURSORTAB_EVAL_API_KEY")
	}
	if key == "" && !*dry {
		return fmt.Errorf("no API key: pass --api-key or set CURSORTAB_EVAL_API_KEY")
	}

	scenarios, err := loadScenarios(*dir)
	if err != nil {
		return err
	}

	recorded := 0
	skipped := 0
	missingTarget := 0
	for _, sc := range scenarios {
		if *scenario != "" && sc.ID != *scenario {
			continue
		}
		if sc.TargetByName(targetName) == nil {
			missingTarget++
			continue
		}
		if *onlyMissing {
			if _, ok := sc.Cassettes[targetName]; ok {
				skipped++
				continue
			}
		}
		if *dry {
			fmt.Printf("[dry-run] would record %s / %s\n", sc.ID, targetName)
			continue
		}

		target := *sc.TargetByName(targetName)
		if *apiURL != "" {
			target.URL = *apiURL
		}
		if *model != "" {
			target.Model = *model
		}
		saved := sc.Targets
		sc.Targets = upsertTarget(saved, target)

		fmt.Printf("recording %s / %s...\n", sc.ID, targetName)
		outcome := harness.Run(sc, harness.Config{
			TargetFilter: []string{targetName},
			Mode:         harness.ModeRecord,
			Transport:    http.DefaultTransport,
			BaseConfig:   &types.ProviderConfig{APIKey: key},
		})
		sc.Targets = saved

		if len(outcome.Targets) == 0 {
			fmt.Fprintf(os.Stderr, "  skipped: no target outcome\n")
			continue
		}
		to := outcome.Targets[0]
		if to.Error != nil {
			fmt.Fprintf(os.Stderr, "  error: %v\n", to.Error)
			continue
		}
		if to.Cassette == nil || len(to.Cassette.Interactions) == 0 {
			fmt.Fprintf(os.Stderr, "  skipped: no HTTP interactions recorded (did the scenario call request-completion?)\n")
			continue
		}
		if *model != "" {
			to.Cassette.Meta.ModelVersion = *model
		} else if target.Model != "" {
			to.Cassette.Meta.ModelVersion = target.Model
		}
		if err := writeCassette(sc, targetName, to.Cassette); err != nil {
			return fmt.Errorf("write cassette: %w", err)
		}
		fmt.Printf("  captured %d interaction(s), %dms total\n", to.RequestCount, to.TotalLatencyMs)
		recorded++
	}

	fmt.Printf("\nrecord: %d recorded, %d skipped, %d scenarios do not declare target %q\n",
		recorded, skipped, missingTarget, targetName)
	return nil
}

// upsertTarget replaces the target with the same name in the list (by
// value), or appends it if none exists.
func upsertTarget(targets []harness.Target, t harness.Target) []harness.Target {
	out := make([]harness.Target, len(targets))
	copy(out, targets)
	for i := range out {
		if out[i].Name == t.Name {
			out[i] = t
			return out
		}
	}
	return append(out, t)
}

// writeCassette writes a cassette to the sidecar directory next to the
// scenario's .txtar file.
func writeCassette(sc *harness.Scenario, targetName string, cs *cassette.Cassette) error {
	dir := sc.CassetteDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create cassette dir: %w", err)
	}
	path := filepath.Join(dir, targetName+".ndjson")
	return cs.Save(path)
}
