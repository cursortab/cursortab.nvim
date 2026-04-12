package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
)

// TargetBaseline stores the quality metrics for one target.
type TargetBaseline struct {
	Score     float64 `json:"score"`
	DeltaChrF float64 `json:"deltaChrF"`
	ShowRate  float64 `json:"showRate"`
	QuietRate float64 `json:"quietRate"`
}

// Baseline maps target names to their quality metrics.
type Baseline map[string]TargetBaseline

func loadBaseline(path string) (Baseline, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var b Baseline
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("parse baseline: %w", err)
	}
	return b, nil
}

func saveBaseline(path string, b Baseline) error {
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

type baselineDiff struct {
	target string
	field  string
	old    float64
	new    float64
}

func (d baselineDiff) regressed() bool {
	return d.new < d.old
}

var baselineFields = []string{"score", "deltaChrF", "showRate", "quietRate"}

// compareBaselines returns diffs between old and new. If old is nil (first run),
// returns no diffs.
func compareBaselines(old, cur Baseline) []baselineDiff {
	if old == nil {
		return nil
	}
	var diffs []baselineDiff
	targets := make(map[string]bool)
	for k := range old {
		targets[k] = true
	}
	for k := range cur {
		targets[k] = true
	}
	sorted := make([]string, 0, len(targets))
	for k := range targets {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)

	for _, t := range sorted {
		o, oOK := old[t]
		c, cOK := cur[t]
		for _, field := range baselineFields {
			var ov, cv float64
			if oOK {
				ov = getField(o, field)
			}
			if cOK {
				cv = getField(c, field)
			}
			if !floatEq(ov, cv) {
				diffs = append(diffs, baselineDiff{t, field, ov, cv})
			}
		}
	}
	return diffs
}

func getField(b TargetBaseline, field string) float64 {
	switch field {
	case "score":
		return b.Score
	case "deltaChrF":
		return b.DeltaChrF
	case "showRate":
		return b.ShowRate
	case "quietRate":
		return b.QuietRate
	}
	return 0
}

func floatEq(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func printBaselineDiffs(diffs []baselineDiff) {
	for _, d := range diffs {
		arrow := "↑"
		if d.regressed() {
			arrow = "↓"
		}
		fmt.Fprintf(os.Stderr, "  %s %s: %.4f → %.4f %s\n", d.target, d.field, d.old, d.new, arrow)
	}
}
