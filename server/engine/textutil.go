package engine

import "strings"

// extToLanguage maps file extensions to language identifiers for metrics.
var extToLanguage = map[string]string{
	".js":   "javascript",
	".mjs":  "javascript",
	".cjs":  "javascript",
	".jsx":  "javascriptreact",
	".ts":   "typescript",
	".tsx":  "typescriptreact",
	".py":   "python",
	".vue":  "vue",
	".php":  "php",
	".dart": "dart",
	".go":   "go",
	".css":  "css",
	".cpp":  "cpp",
	".cc":   "cpp",
	".cxx":  "cpp",
	".h":    "cpp",
	".hpp":  "cpp",
	".html": "html",
	".htm":  "html",
	".scss": "scss",
	".md":   "markdown",
	".cs":   "csharp",
	".java": "java",
	".json": "json",
	".rs":   "rust",
	".rb":   "ruby",
	".c":    "c",
}

// currentLine returns the line at the given 1-based row with col clamped.
func currentLine(lines []string, row, col int) (string, int) {
	if row < 1 || row > len(lines) {
		return "", 0
	}
	line := lines[row-1]
	if col > len(line) {
		col = len(line)
	}
	return line, col
}

// documentByteLength returns total bytes including newlines.
func documentByteLength(lines []string) int {
	total := 0
	for _, line := range lines {
		total += len(line) + 1
	}
	return total
}

// byteOffset returns the byte offset of cursor position in the document.
func byteOffset(lines []string, row, col int) int {
	offset := 0
	for i := 0; i < row-1 && i < len(lines); i++ {
		offset += len(lines[i]) + 1
	}
	if row >= 1 && row <= len(lines) {
		c := min(col, len(lines[row-1]))
		offset += c
	}
	return offset
}

// lastNonWSChar returns the last non-whitespace character before col.
func lastNonWSChar(line string, col int) (byte, bool) {
	for i := col - 1; i >= 0; i-- {
		if line[i] != ' ' && line[i] != '\t' {
			return line[i], true
		}
	}
	return 0, false
}

// afterCursorIsWhitespace returns true if all text after the cursor is whitespace.
func afterCursorIsWhitespace(lines []string, row, col int) bool {
	if row < 1 || row > len(lines) {
		return true
	}
	line := lines[row-1]
	if col >= len(line) {
		return true
	}
	return strings.TrimSpace(line[col:]) == ""
}
