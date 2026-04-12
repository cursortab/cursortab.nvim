// Package metrics computes quality scores for eval completions.
//
// chrF (character n-gram F-score, Popović 2015) is the primary quality
// metric, used by Zed's Zeta evaluation. We compute it on the diff region
// only — the lines the completion actually touches — which matches their
// "deltaChrF" approach: copy fidelity shouldn't inflate the score.
package metrics

import "strings"

// DefaultNOrder is the max character n-gram order (chrF/chrF++ use 6).
const DefaultNOrder = 6

// DefaultBeta weights recall vs precision (β=2 favors recall, which is the
// Popović 2015 recommendation for MT evaluation).
const DefaultBeta = 2.0

// ChrF computes the character n-gram F-score between candidate and reference.
// Returns a value in [0, 100] (higher is better, matching sacrebleu chrF).
func ChrF(candidate, reference string) float64 {
	return ChrFWith(candidate, reference, DefaultNOrder, DefaultBeta)
}

// ChrFWith exposes the n-gram order and beta parameters.
func ChrFWith(candidate, reference string, nOrder int, beta float64) float64 {
	if nOrder <= 0 {
		nOrder = DefaultNOrder
	}
	if beta <= 0 {
		beta = DefaultBeta
	}

	// Sum F-scores across n-gram orders, then average. Matches sacrebleu /
	// Popović 2015: F_β = (1+β²) * P * R / (β²*P + R) per order.
	// We skip empty orders from both sides by averaging only the orders that
	// actually have any n-grams in either side.
	var sumF float64
	var count int
	for n := 1; n <= nOrder; n++ {
		candNg := charNGrams(candidate, n)
		refNg := charNGrams(reference, n)
		if len(candNg) == 0 && len(refNg) == 0 {
			continue
		}
		count++
		p, r := pr(candNg, refNg)
		f := fBeta(p, r, beta)
		sumF += f
	}
	if count == 0 {
		// Both candidate and reference produced zero n-grams across every
		// order — most commonly "both are empty strings". We return 100 on
		// purpose: in edit-prediction, an empty reference means "no edit
		// expected" and an empty candidate means "no edit produced" — a
		// trivially correct no-op. Returning 0 or NaN here would make a
		// valid "correctly predicted nothing" case look like a failure.
		return 100.0
	}
	return 100.0 * sumF / float64(count)
}

// DeltaChrF computes chrF restricted to the lines that differ between
// before and the given after. Both before and the two afters must share a
// common prefix/suffix; we extract only the changed middle and score that.
//
// This is the metric that matters for edit prediction: it ignores any
// unchanged copy-through text the provider might emit.
func DeltaChrF(before, candidateAfter, referenceAfter []string) float64 {
	candDelta := extractDelta(before, candidateAfter)
	refDelta := extractDelta(before, referenceAfter)
	return ChrF(candDelta, refDelta)
}

// extractDelta returns the substring representing what changed from before
// to after (common prefix + common suffix stripped), joined with \n.
func extractDelta(before, after []string) string {
	// Find common prefix.
	prefix := 0
	for prefix < len(before) && prefix < len(after) && before[prefix] == after[prefix] {
		prefix++
	}
	// Find common suffix.
	suffix := 0
	for suffix < len(before)-prefix && suffix < len(after)-prefix &&
		before[len(before)-1-suffix] == after[len(after)-1-suffix] {
		suffix++
	}
	delta := after[prefix : len(after)-suffix]
	return strings.Join(delta, "\n")
}

func charNGrams(s string, n int) map[string]int {
	if s == "" || n <= 0 {
		return nil
	}
	// Use runes so unicode behaves.
	runes := []rune(s)
	if len(runes) < n {
		return nil
	}
	out := make(map[string]int, len(runes))
	for i := 0; i+n <= len(runes); i++ {
		out[string(runes[i:i+n])]++
	}
	return out
}

func pr(cand, ref map[string]int) (float64, float64) {
	if len(cand) == 0 && len(ref) == 0 {
		return 1, 1
	}
	// Number of matches is sum of min(cand[g], ref[g]).
	var match int
	for g, c := range cand {
		if r := ref[g]; r > 0 {
			match += min(c, r)
		}
	}
	var candTotal, refTotal int
	for _, c := range cand {
		candTotal += c
	}
	for _, r := range ref {
		refTotal += r
	}
	var p, r float64
	if candTotal > 0 {
		p = float64(match) / float64(candTotal)
	}
	if refTotal > 0 {
		r = float64(match) / float64(refTotal)
	}
	return p, r
}

func fBeta(p, r, beta float64) float64 {
	if p == 0 && r == 0 {
		return 0
	}
	b2 := beta * beta
	return (1 + b2) * p * r / (b2*p + r)
}
