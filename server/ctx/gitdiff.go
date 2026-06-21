package ctx

import (
	"bufio"
	"context"
	"os/exec"
	"strings"

	"cursortab/logger"
)

// runGit executes a git command and returns its stdout, or "" on error.
func runGit(ctx context.Context, dir string, args ...string) string {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		logger.Debug("gitdiff: git %s failed: %v", args[0], err)
		return ""
	}
	return string(out)
}

// extractChangedSymbols parses a unified diff (-U0) and extracts function/type
// signatures from added/removed declaration lines in git diff format.
func extractChangedSymbols(diff string, maxSymbols int) []string {
	if diff == "" {
		return nil
	}

	seen := make(map[string]struct{})
	var symbols []string

	scanner := bufio.NewScanner(strings.NewReader(diff))

	for scanner.Scan() {
		line := scanner.Text()

		// Skip metadata lines
		if strings.HasPrefix(line, "diff --git ") ||
			strings.HasPrefix(line, "---") ||
			strings.HasPrefix(line, "+++") ||
			strings.HasPrefix(line, "index ") ||
			strings.HasPrefix(line, "@@") {
			continue
		}

		// Extract added declaration lines
		if strings.HasPrefix(line, "+") {
			content := strings.TrimSpace(line[1:])
			if isDeclarationLine(content) {
				sym := "+" + content
				if _, ok := seen[sym]; !ok && len(symbols) < maxSymbols {
					seen[sym] = struct{}{}
					symbols = append(symbols, sym)
				}
			}
			continue
		}

		// Extract removed declaration lines
		if strings.HasPrefix(line, "-") {
			content := strings.TrimSpace(line[1:])
			if isDeclarationLine(content) {
				sym := "-" + content
				if _, ok := seen[sym]; !ok && len(symbols) < maxSymbols {
					seen[sym] = struct{}{}
					symbols = append(symbols, sym)
				}
			}
		}
	}

	return symbols
}

// isDeclarationLine checks if a line looks like a function/type/class declaration
// across common languages (Go, Python, Rust, JS/TS, C/C++, Java).
func isDeclarationLine(line string) bool {
	prefixes := []string{
		"func ", "func(", // Go
		"def ",                     // Python
		"class ",                   // Python, JS/TS, Java, C++
		"type ",                    // Go, TS
		"struct ",                  // Go, Rust, C/C++
		"fn ",                      // Rust
		"impl ",                    // Rust
		"trait ",                   // Rust
		"enum ",                    // Rust, Java, TS
		"interface ",               // Go, TS, Java
		"export function ",         // JS/TS
		"export default function ", // JS/TS
		"export const ",            // JS/TS
		"export class ",            // JS/TS
		"async function ",          // JS/TS
		"export async function ",   // JS/TS
		"public ",                  // Java, C#
		"private ",                 // Java, C#
		"protected ",               // Java, C#
		"static ",                  // Java, C/C++
	}

	for _, prefix := range prefixes {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}

	return false
}
