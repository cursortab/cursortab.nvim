package gating

import (
	"cursortab/assert"
	"fmt"
	"testing"
	"time"
)

var now = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

func TestScore_EndOfLineAfterOpenParen(t *testing.T) {
	score := Score(Input{
		Lines:         []string{"func main("},
		Row:           1,
		Col:           10,
		FileExtension: ".go",
		Now:           now,
	})
	assert.True(t, score > Threshold,
		fmt.Sprintf("end of line after '(' should pass filter, got %.3f", score))
}

func TestScore_MidLineAfterCloseParen(t *testing.T) {
	score := Score(Input{
		Lines:         []string{"result = process(items) + transform(data)"},
		Row:           1,
		Col:           23,
		FileExtension: ".go",
		Now:           now,
	})
	assert.True(t, score < 0.5,
		fmt.Sprintf("mid-line after ')' should score low, got %.3f", score))
}

func TestScore_EmptyLine(t *testing.T) {
	score := Score(Input{
		Lines:         []string{"func main() {", "", "}"},
		Row:           2,
		Col:           0,
		FileExtension: ".go",
		Now:           now,
	})
	assert.True(t, score > Threshold,
		fmt.Sprintf("empty line should pass filter, got %.3f", score))
}

func TestScore_Momentum(t *testing.T) {
	base := Input{
		Lines:         []string{"x := "},
		Row:           1,
		Col:           5,
		FileExtension: ".go",
		LastDecision:  now.Add(-1 * time.Second),
		Now:           now,
	}

	base.PreviousLabel = true
	withMomentum := Score(base)

	base.PreviousLabel = false
	withoutMomentum := Score(base)

	assert.True(t, withMomentum > withoutMomentum,
		fmt.Sprintf("momentum should increase score: with=%.3f, without=%.3f",
			withMomentum, withoutMomentum))
}

func TestScore_OpeningDelimiters(t *testing.T) {
	cases := []struct {
		line string
		col  int
	}{
		{"fn(", 3},
		{"if x {", 6},
		{" ", 1},
	}

	for _, tc := range cases {
		score := Score(Input{
			Lines:         []string{tc.line},
			Row:           1,
			Col:           tc.col,
			FileExtension: ".go",
			Now:           now,
		})
		ch := tc.line[tc.col-1]
		assert.True(t, score > Threshold,
			fmt.Sprintf("after %q should pass filter, got %.3f", string(ch), score))
	}
}

func TestScore_ClosingDelimiterMidLine(t *testing.T) {
	score := Score(Input{
		Lines:         []string{"fn(x) + fn(y)"},
		Row:           1,
		Col:           5,
		FileExtension: ".go",
		Now:           now,
	})
	assert.True(t, score < 0.5,
		fmt.Sprintf("after ')' mid-line should score low, got %.3f", score))
}

func TestScore_LanguageEffect(t *testing.T) {
	base := Input{
		Lines: []string{"x = "},
		Row:   1,
		Col:   4,
		Now:   now,
	}

	base.FileExtension = ".go"
	goScore := Score(base)

	base.FileExtension = ".md"
	mdScore := Score(base)

	assert.True(t, goScore > mdScore,
		fmt.Sprintf("Go should score higher than Markdown: go=%.3f, md=%.3f", goScore, mdScore))
}

func TestScore_TimeSinceDecision(t *testing.T) {
	base := Input{
		Lines:         []string{"x = "},
		Row:           1,
		Col:           4,
		FileExtension: ".go",
		PreviousLabel: true,
		Now:           now,
	}

	base.LastDecision = now.Add(-100 * time.Millisecond)
	recentScore := Score(base)

	base.LastDecision = now.Add(-60 * time.Second)
	staleScore := Score(base)

	assert.True(t, recentScore > staleScore,
		fmt.Sprintf("recent decision should score higher: recent=%.3f, stale=%.3f",
			recentScore, staleScore))
}

func TestShouldSuppress(t *testing.T) {
	score := Score(Input{
		Lines:         []string{"result = "},
		Row:           1,
		Col:           9,
		FileExtension: ".go",
		Now:           now,
	})
	assert.False(t, ShouldSuppress(score), "good context should pass")
}

func TestCharIndex(t *testing.T) {
	assert.Equal(t, 1, charIndex(' '), "space")
	assert.Equal(t, 95, charIndex('~'), "tilde")
	assert.Equal(t, 0, charIndex('\t'), "tab is non-printable")
	assert.Equal(t, 0, charIndex(0), "null byte")
	assert.Equal(t, 34, charIndex('A'), "A")
	assert.Equal(t, 66, charIndex('a'), "a")
}

func TestSigmoid(t *testing.T) {
	assert.Equal(t, 0.5, sigmoid(0), "sigmoid(0)")
	assert.True(t, sigmoid(10) > 0.999, "sigmoid(10) near 1")
	assert.True(t, sigmoid(-10) < 0.001, "sigmoid(-10) near 0")
}

func TestLastNonWSChar(t *testing.T) {
	ch, ok := LastNonWSChar("x = ", 4)
	assert.True(t, ok, "should find non-ws char")
	assert.Equal(t, byte('='), ch, "last non-ws char")

	_, ok = LastNonWSChar("  ", 2)
	assert.False(t, ok, "no non-ws char in whitespace")

	ch, ok = LastNonWSChar("func(", 5)
	assert.True(t, ok, "should find char")
	assert.Equal(t, byte('('), ch, "last non-ws char is (")
}

func TestAfterCursorIsWhitespace(t *testing.T) {
	assert.True(t, AfterCursorIsWhitespace([]string{"hello"}, 1, 5), "cursor at end")
	assert.True(t, AfterCursorIsWhitespace([]string{"hello   "}, 1, 5), "only spaces after")
	assert.False(t, AfterCursorIsWhitespace([]string{"hello world"}, 1, 5), "code after cursor")
}

func TestDocumentByteLength(t *testing.T) {
	assert.Equal(t, 12, DocumentByteLength([]string{"hello", "world"}), "two 5-char lines")
	assert.Equal(t, 1, DocumentByteLength([]string{""}), "single empty line")
}

func TestWeightsArrayLength(t *testing.T) {
	assert.Equal(t, 221, len(weights), "weight array should have 221 elements")
}
