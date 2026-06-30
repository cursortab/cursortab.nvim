package e2e

import (
	"strings"
	"testing"

	"cursortab/assert"
)

func TestRenderLineMapsByteColumnsToUTF8Characters(t *testing.T) {
	var b strings.Builder

	RenderLine(&b, 1, "Hello 🚀 world", LineHighlight{
		RenderHint: "replace_chars",
		ColStart:   len("Hello "),
		ColEnd:     len("Hello 🚀"),
	}, -1)

	assert.True(t, strings.Contains(b.String(), `Hello <span class="add-hl">🚀</span> world`), "highlighted emoji")
}
