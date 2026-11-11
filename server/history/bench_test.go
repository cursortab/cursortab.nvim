package history

import (
	"fmt"
	"slices"
	"testing"
)

func fileLines(n int) []string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf("\tvalue%d := compute(alpha, beta, %d) // trailing comment", i, i)
	}
	return lines
}

// BenchmarkCommitUserEdits measures recording one typed character in a file
// of growing size.
func BenchmarkCommitUserEdits(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 100_000, 500_000} {
		b.Run(fmt.Sprintf("lines=%d", n), func(b *testing.B) {
			base := fileLines(n)
			edited := slices.Clone(base)
			edited[n/2] += "x"
			s := NewStore(5)
			for b.Loop() {
				s.Seed("f.go", base, nil, 0)
				s.CommitUserEdits("f.go", edited, 1)
			}
		})
	}
}

// BenchmarkCommitUserEditsScattered measures many edits spread over a file,
// such as after a search-and-replace.
func BenchmarkCommitUserEditsScattered(b *testing.B) {
	for _, n := range []int{10_000, 100_000} {
		b.Run(fmt.Sprintf("lines=%d", n), func(b *testing.B) {
			base := fileLines(n)
			edited := slices.Clone(base)
			for i := 0; i < n; i += 10 {
				edited[i] += "x"
			}
			s := NewStore(5)
			for b.Loop() {
				s.Seed("f.go", base, nil, 0)
				s.CommitUserEdits("f.go", edited, 1)
			}
		})
	}
}
