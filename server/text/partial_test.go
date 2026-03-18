package text

import (
	"cursortab/assert"
	"testing"
)

func TestFindNextWordBoundary(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int // number of bytes to accept
	}{
		// Leading whitespace - consumed as a single chunk
		{"space at start", " world", 1},
		{"leading spaces", "    return true", 4}, // all 4 spaces
		{"leading tabs", "\t\treturn", 2},        // both tabs
		{"leading mixed", " \t  code", 4},        // all whitespace
		{"only spaces", "    ", 4},               // all spaces, no non-ws

		// Word then boundary
		{"word then space", "hello world", 6}, // "hello " (5 + 1)
		{"tab", "foo\tbar", 4},                // "foo\t"

		// Punctuation boundaries
		{"period", "foo.bar", 4},         // "foo."
		{"comma", "a, b", 2},             // "a,"
		{"semicolon", "x;y", 2},          // "x;"
		{"colon", "key:value", 4},        // "key:"
		{"exclamation", "wow!really", 4}, // "wow!"
		{"question", "what?next", 5},     // "what?"
		{"open paren", "func()", 5},      // "func("
		{"close paren", ")next", 1},      // ")"
		{"open bracket", "arr[0]", 4},    // "arr["
		{"close bracket", "]end", 1},     // "]"
		{"open brace", "map{}", 4},       // "map{"
		{"close brace", "}done", 1},      // "}"
		{"double quote", `say"hi"`, 4},   // `say"`
		{"single quote", "it's", 3},      // "it'"
		{"backtick", "code`here", 5},     // "code`"
		{"less than", "a<b", 2},          // "a<"
		{"greater than", "a>b", 2},       // "a>"
		{"slash", "path/to", 5},          // "path/"

		// No boundary - accept all
		{"no boundary", "identifier", 10},
		{"single char", "x", 1},
		{"empty", "", 0},
		{"all letters", "abcdef", 6},

		// Multiple boundaries - stop at first
		{"multiple periods", "a.b.c", 2},        // "a."
		{"mixed punctuation", "x,y;z", 2},       // "x,"
		{"space after word", "hello world!", 6}, // "hello "

		// Unicode - should handle correctly
		{"unicode word then space", "日本語 test", 10}, // "日本語 " (9 bytes for chars + 1 for space)
		{"unicode with period", "日本語.test", 10},     // "日本語." (9 + 1)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FindNextWordBoundary(tt.input)
			assert.Equal(t, tt.want, got, "FindNextWordBoundary")
		})
	}
}
