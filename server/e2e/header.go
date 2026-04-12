package e2e

import "strings"

// ParseHeader extracts key-value pairs from a txtar archive comment.
// Lines are split on the first ":" — the key is trimmed, the value is trimmed.
// Lines without a colon are stored under the empty-string key (first one wins).
func ParseHeader(comment []byte) map[string]string {
	m := make(map[string]string)
	for i, line := range strings.Split(string(comment), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			if _, exists := m[""]; !exists && i == 0 {
				m[""] = line
			}
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if _, exists := m[key]; !exists {
			m[key] = val
		}
	}
	return m
}
