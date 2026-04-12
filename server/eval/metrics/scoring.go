package metrics

import (
	"sort"
)

// Score bundles the metrics for one (scenario, provider) result.
type Score struct {
	DeltaChrF float64 // [0, 100] — primary quality signal
	LatencyMs int64
	Shown     bool
	Combined  float64 // deltaChrF × shown — expected quality per keystroke
}

// Compute evaluates all metrics for one result.
func Compute(before, candidate, reference []string, latencyMs int64, shown bool) Score {
	s := Score{
		DeltaChrF: DeltaChrF(before, candidate, reference),
		LatencyMs: latencyMs,
		Shown:     shown,
	}
	if shown {
		s.Combined = s.DeltaChrF
	}
	return s
}

// Aggregate combines multiple scores for summary reporting.
type Aggregate struct {
	N               int
	MeanDeltaChrF   float64
	MedianLatencyMs int64
	P90LatencyMs    int64
	ShowRate        float64
	MeanCombined    float64
}

// Summarize computes aggregate stats from a slice of scores.
func Summarize(scores []Score) Aggregate {
	n := len(scores)
	if n == 0 {
		return Aggregate{}
	}
	var sumChrF, sumCombined float64
	var shownCount int
	lats := make([]int64, 0, n)
	for _, s := range scores {
		sumChrF += s.DeltaChrF
		sumCombined += s.Combined
		if s.Shown {
			shownCount++
		}
		lats = append(lats, s.LatencyMs)
	}
	sort.Slice(lats, func(i, j int) bool { return lats[i] < lats[j] })
	p50 := lats[n/2]
	p90Idx := (n * 90) / 100
	if p90Idx >= n {
		p90Idx = n - 1
	}
	return Aggregate{
		N:               n,
		MeanDeltaChrF:   sumChrF / float64(n),
		MedianLatencyMs: p50,
		P90LatencyMs:    lats[p90Idx],
		ShowRate:        float64(shownCount) / float64(n),
		MeanCombined:    sumCombined / float64(n),
	}
}
