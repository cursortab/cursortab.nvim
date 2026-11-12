package engine

import (
	"slices"
	"testing"

	"cursortab/assert"
)

func TestInertSuffixPattern(t *testing.T) {
	tests := []struct {
		suffix string
		inert  bool
	}{
		// Inert suffixes → should NOT suppress
		{"", true},
		{")", true},
		{"))", true},
		{"}", true},
		{"]", true},
		{`"`, true},
		{"'", true},
		{"`", true},
		{");", true},
		{") {", true},
		{"})", true},
		{"  )", true},
		{")  ", true},
		{",", true},
		{":", true},

		// Active suffixes → should suppress
		{"items {", false},
		{"!= nil {", false},
		{"foo()", false},
		{"hello", false},
		{"x + y", false},
		{".method()", false},
		{"= value", false},
		{"range items {", false},
	}

	for _, tt := range tests {
		got := inertSuffixPattern.MatchString(tt.suffix)
		assert.Equal(t, tt.inert, got, "suffix: "+tt.suffix)
	}
}

func TestRejectedCompletionMatches(t *testing.T) {
	body := []string{
		"  for i := range items {",
		"    if items[i] > 0 {",
		"      result = append(result, items[i])",
		"    }",
		"  }",
		"  return result",
	}
	cached := func(oldLines, lines []string) *rejectedCompletion {
		return &rejectedCompletion{
			filePath: "test.go", startLine: 1, endLineInc: 1,
			beforeLine: "func foo() {", oldLines: oldLines, lines: lines,
		}
	}
	tests := []struct {
		name     string
		cached   *rejectedCompletion
		incoming *rejectedCompletion
		want     bool
	}{
		{
			name:     "a retry one line shorter matches",
			cached:   cached([]string{"  // body"}, append(slices.Clone(body), "  // trailing")),
			incoming: cached([]string{"  // body"}, body),
			want:     true,
		},
		{
			name:     "a much shorter retry does not match",
			cached:   cached([]string{"  // body"}, []string{"line a", "line b", "line c", "line d", "line e"}),
			incoming: cached([]string{"  // body"}, []string{"line a", "line b", "line c"}),
			want:     false,
		},
		{
			name:     "different old lines do not match even with short context",
			cached:   &rejectedCompletion{filePath: "test.go", startLine: 2, endLineInc: 2, beforeLine: "a", afterLine: "c", oldLines: []string{"b"}, lines: []string{"updated"}},
			incoming: &rejectedCompletion{filePath: "test.go", startLine: 2, endLineInc: 2, beforeLine: "a", afterLine: "c", oldLines: []string{"totally different content here"}, lines: []string{"updated"}},
			want:     false,
		},
	}
	for _, tt := range tests {
		matched, _ := tt.cached.matches(tt.incoming)
		assert.Equal(t, tt.want, matched, tt.name)
	}
}
