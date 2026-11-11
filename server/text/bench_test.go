package text

import (
	"fmt"
	"strings"
	"testing"
)

// codeLine returns a realistic line of Go-like code of roughly width bytes.
func codeLine(i, width int) string {
	s := fmt.Sprintf("\tresult%d := compute(alpha%d, beta%d, gamma) // note %d ", i, i%7, i%13, i)
	for len(s) < width {
		s += "x"
	}
	return s[:width]
}

func codeLines(n, width int) []string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = codeLine(i, width)
	}
	return lines
}

// BenchmarkComputeDiffRewrite measures a provider window that the model
// rewrote with a different line count, which takes the pairwise similarity
// path in handleModificationsWithMapping.
func BenchmarkComputeDiffRewrite(b *testing.B) {
	for _, tc := range []struct{ del, ins, width int }{
		{20, 30, 40},
		{50, 75, 40},
		{100, 150, 40},
		{200, 300, 40},
		{50, 75, 200},
		{100, 150, 200},
	} {
		b.Run(fmt.Sprintf("del=%d/ins=%d/width=%d", tc.del, tc.ins, tc.width), func(b *testing.B) {
			old := codeLines(tc.del, tc.width)
			upd := make([]string, tc.ins)
			for i := range upd {
				// Every line differs a little, so all pairs are scored.
				upd[i] = strings.Replace(codeLine(i*tc.del/tc.ins, tc.width), "compute", "evaluate", 1)
			}
			oldText, newText := JoinLines(old), JoinLines(upd)
			b.ResetTimer()
			for b.Loop() {
				ComputeDiff(oldText, newText)
			}
		})
	}
}

// BenchmarkComputeDiffLongLine measures a single-line edit in a very long
// line, as in minified files.
func BenchmarkComputeDiffLongLine(b *testing.B) {
	for _, size := range []int{10_000, 100_000, 1_000_000} {
		b.Run(fmt.Sprintf("bytes=%d", size), func(b *testing.B) {
			old := strings.Repeat("a=1;b=2;", size/8)
			upd := old[:size/2] + "c=3;" + old[size/2:]
			// One extra line on each side forces the unequal-count path.
			oldText := JoinLines([]string{old})
			newText := JoinLines([]string{upd, "x"})
			for b.Loop() {
				ComputeDiff(oldText, newText)
			}
		})
	}
}
