package text

import (
	"unicode/utf8"
)

// WordBoundaryChars defines characters that end a word for partial accept.
// Includes spaces, tabs, and common punctuation.
const WordBoundaryChars = " \t.,;:!?()[]{}\"'`<>/"

// FindNextWordBoundary returns the byte index of the next word boundary in text.
// The boundary character itself is included in the returned length.
// If no boundary is found, returns len(text).
//
// When text starts with whitespace (spaces/tabs), all consecutive leading
// whitespace is consumed as a single chunk. This prevents partial accept from
// stepping through indentation one character at a time.
func FindNextWordBoundary(text string) int {
	if len(text) == 0 {
		return 0
	}

	// If text starts with whitespace, consume all leading whitespace at once
	r, _ := utf8.DecodeRuneInString(text)
	if r == ' ' || r == '\t' {
		i := 0
		for i < len(text) {
			r, size := utf8.DecodeRuneInString(text[i:])
			if r != ' ' && r != '\t' {
				break
			}
			i += size
		}
		return i
	}

	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		for _, boundary := range WordBoundaryChars {
			if r == boundary {
				return i + size
			}
		}
		i += size
	}
	return len(text)
}
