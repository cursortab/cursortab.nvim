package text

import (
	"strings"
	"testing"

	"cursortab/assert"
)

// dpLevenshtein is the textbook dynamic program over runes.
func dpLevenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}

func TestLevenshteinMatchesDynamicProgram(t *testing.T) {
	long := strings.Repeat("ab c(){}", 25) // Longer than one 64-rune block
	tests := []struct{ name, a, b string }{
		{"both empty", "", ""},
		{"one empty", "abc", ""},
		{"identical", "foo(x)", "foo(x)"},
		{"substitution", "foo(x)", "foo(y)"},
		{"insertion and deletion", "return 1", "retrn 10"},
		{"multibyte runes", "café 🚀", "cafe 🚀!"},
		{"unrelated short", "abc", "xyz"},
		{"block boundary", strings.Repeat("a", 64), strings.Repeat("a", 63) + "b"},
		{"long with shared prefix and suffix", long, long[:90] + "é" + long[91:]},
		{"long unrelated", long, strings.Repeat("{)(c ba", 30)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, dpLevenshtein(tt.a, tt.b), levenshtein(tt.a, tt.b), "distance")
		})
	}
}

func TestLevenshteinBoundsHugeLines(t *testing.T) {
	a := strings.Repeat("ab", 50_000)
	b := strings.Repeat("ba", 50_000)
	assert.Equal(t, len(b), levenshtein(a, b), "unrelated huge lines")
	assert.Equal(t, 4, levenshtein(a+"tail", a), "huge lines that share a prefix")
}
