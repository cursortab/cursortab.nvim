package e2e

import (
	"fmt"
	"strings"
)

// QuoteLine wraps a string in double quotes, escaping only " and \.
// Unlike strconv.Quote, it preserves literal tabs and other characters for readability.
func QuoteLine(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		default:
			b.WriteByte(s[i])
		}
	}
	b.WriteByte('"')
	return b.String()
}

// UnquoteLine strips surrounding double quotes and unescapes \", \\, \n, and \t.
func UnquoteLine(s string) (string, error) {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return "", fmt.Errorf("not a quoted string: %s", s)
	}
	inner := s[1 : len(s)-1]
	var b strings.Builder
	for i := 0; i < len(inner); i++ {
		if inner[i] == '\\' && i+1 < len(inner) {
			switch inner[i+1] {
			case '"':
				b.WriteByte('"')
			case '\\':
				b.WriteByte('\\')
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			default:
				return "", fmt.Errorf("unknown escape \\%c", inner[i+1])
			}
			i++
		} else {
			b.WriteByte(inner[i])
		}
	}
	return b.String(), nil
}
