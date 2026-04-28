package main

import (
	"fmt"
	"os"
	"sort"
	"text/tabwriter"

	"cursortab/eval/metrics"
)

type suppressCount struct{ correct, total int }

// targetStats holds pre-computed aggregate stats for one target.
//
// Score is the headline metric: deltaChrF × gateScore / 100, where
// gateScore is the harmonic mean (F1) of showRate and quietRate. This
// rewards targets that produce high-quality completions (deltaChrF) while
// also showing them at the right time (showRate) and staying quiet when
// appropriate (quietRate).
type targetStats struct {
	name      string
	typ       string
	agg       metrics.Aggregate
	quietRate float64
	score     float64
}

func computeTargetStats(perTarget map[string][]metrics.Score, suppressStats map[string]*suppressCount, targetTypes map[string]string) []targetStats {
	stats := make([]targetStats, 0, len(perTarget))
	for name, scores := range perTarget {
		agg := metrics.Summarize(scores)
		ts := targetStats{
			name: name,
			typ:  targetTypes[name],
			agg:  agg,
		}
		if sc := suppressStats[name]; sc != nil && sc.total > 0 {
			ts.quietRate = float64(sc.correct) / float64(sc.total)
		}
		// score = deltaChrF × gateScore / 100
		// gateScore = harmonic mean of showRate and quietRate (F1).
		gateScore := float64(0)
		if agg.ShowRate+ts.quietRate > 0 {
			gateScore = 2 * agg.ShowRate * ts.quietRate / (agg.ShowRate + ts.quietRate)
		}
		ts.score = agg.MeanDeltaChrF * gateScore / 100
		stats = append(stats, ts)
	}
	sort.Slice(stats, func(i, j int) bool {
		if stats[i].score != stats[j].score {
			return stats[i].score > stats[j].score
		}
		return stats[i].name < stats[j].name
	})
	return stats
}

func renderQualityReport(perTarget map[string][]metrics.Score, perScenario []perScenarioScore, suppressStats map[string]*suppressCount, targetTypes map[string]string, includePerScenario bool) {
	if len(perTarget) == 0 {
		fmt.Println("no quality scores collected (no scenarios with -- expected -- section)")
		return
	}

	stats := computeTargetStats(perTarget, suppressStats, targetTypes)

	if includePerScenario && len(perScenario) > 0 {
		fmt.Println()
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "Scenario\tTarget\tdeltaChrF\tLat (ms)\tShown\tCombined")
		scenarioOrder := distinctScenarios(perScenario)
		for _, sid := range scenarioOrder {
			for _, ts := range stats {
				s, ok := findScore(perScenario, sid, ts.name)
				if !ok {
					continue
				}
				shown := "no"
				if s.Shown {
					shown = "yes"
				}
				fmt.Fprintf(w, "%s\t%s\t%.2f\t%d\t%s\t%.2f\n",
					sid, ts.name, s.DeltaChrF, s.LatencyMs, shown, s.Combined)
			}
		}
		w.Flush()
	}

	fmt.Println()
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "Target\tType\tScore\tdeltaChrF\tShow rate\tQuiet rate\tp50 (ms)\tp90 (ms)")
	for _, ts := range stats {
		quietRate := "-"
		if ts.quietRate > 0 {
			quietRate = fmt.Sprintf("%.0f%%", ts.quietRate*100)
		}
		fmt.Fprintf(w, "%s\t%s\t%.2f\t%.1f\t%.0f%%\t%s\t%d\t%d\n",
			ts.name, ts.typ, ts.score, ts.agg.MeanDeltaChrF,
			ts.agg.ShowRate*100, quietRate,
			ts.agg.MedianLatencyMs, ts.agg.P90LatencyMs)
	}
	w.Flush()

	qualityN := 0
	for _, v := range perTarget {
		qualityN = len(v)
		break
	}
	suppressN := 0
	for _, sc := range suppressStats {
		suppressN = sc.total
		break
	}
	fmt.Printf("\n%d quality scenarios, %d suppress scenarios\n", qualityN, suppressN)
}

// buildBaseline computes the current baseline from quality scores and suppress stats.
func buildBaseline(perTarget map[string][]metrics.Score, suppressStats map[string]*suppressCount, targetTypes map[string]string) Baseline {
	b := make(Baseline, len(perTarget))
	for _, ts := range computeTargetStats(perTarget, suppressStats, targetTypes) {
		b[ts.name] = TargetBaseline{
			Score:     ts.score,
			DeltaChrF: ts.agg.MeanDeltaChrF,
			ShowRate:  ts.agg.ShowRate,
			QuietRate: ts.quietRate,
		}
	}
	return b
}

func distinctScenarios(rows []perScenarioScore) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range rows {
		if !seen[r.scenarioID] {
			seen[r.scenarioID] = true
			out = append(out, r.scenarioID)
		}
	}
	sort.Strings(out)
	return out
}

func findScore(rows []perScenarioScore, scenarioID, target string) (metrics.Score, bool) {
	for _, r := range rows {
		if r.scenarioID == scenarioID && r.targetName == target {
			return r.score, true
		}
	}
	return metrics.Score{}, false
}
