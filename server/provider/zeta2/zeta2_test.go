package zeta2

import (
	"strings"
	"testing"

	"cursortab/assert"
	"cursortab/client/openai"
	sourcectx "cursortab/ctx"
	"cursortab/provider"
	"cursortab/text"
	"cursortab/types"
)

func newTestProvider() *provider.Provider {
	return NewProvider(&types.ProviderConfig{
		ProviderModel:     "zeta2",
		ProviderMaxTokens: 2048,
	})
}

func testInput(filePath string, lines []string, cursorRow int, cursorCol int, contexts ...sourcectx.Materials) sourcectx.CompletionInput {
	var materials sourcectx.Materials
	for _, context := range contexts {
		materials = append(materials, context...)
	}
	return sourcectx.CompletionInput{
		Current: sourcectx.CurrentSnapshot{
			File: sourcectx.FileSnapshot{
				Path:  filePath,
				Lines: lines,
			},
			Cursor: sourcectx.CursorPosition{
				Row: cursorRow,
				Col: cursorCol,
			},
		},
		Materials: materials,
	}
}

func TestAssemblePrompt_EmptyBuffer(t *testing.T) {
	p := newTestProvider()
	ctx := &provider.RequestState{Input: testInput("main.go", []string{}, 0, 0), TrimmedLines: []string{}}

	prompt, _, _ := assemblePrompt(p, ctx)

	assert.True(t, strings.HasPrefix(prompt, fimSuffix), "starts with fim-suffix")
	assert.True(t, strings.Contains(prompt, fimPrefix), "contains fim-prefix")
	assert.True(t, strings.Contains(prompt, fimMiddle), "contains fim-middle")
	assert.True(t, strings.HasSuffix(prompt, fimMiddle), "ends with fim-middle")
	assert.True(t, strings.Contains(prompt, fileMarker+"main.go"), "contains filename marker")
	assert.True(t, strings.Contains(prompt, currentMarker), "contains CURRENT marker")
	assert.True(t, strings.Contains(prompt, separator), "contains separator")
	assert.True(t, strings.Contains(prompt, cursorMarker), "contains cursor marker")
}

func TestAssemblePrompt_StructuralOrder(t *testing.T) {
	p := newTestProvider()
	lines := []string{
		"package main",
		"",
		"func main() {",
		"\tprintln(\"hello\")",
		"}",
	}
	ctx := &provider.RequestState{
		Input:        testInput("main.go", lines, 4, 17),
		TrimmedLines: lines,
		WindowStart:  0,
		CursorLine:   3,
	}

	prompt, _, _ := assemblePrompt(p, ctx)

	// SPM order: suffix, then prefix, then middle.
	suffixIdx := strings.Index(prompt, fimSuffix)
	prefixIdx := strings.Index(prompt, fimPrefix)
	middleIdx := strings.Index(prompt, fimMiddle)
	assert.True(t, suffixIdx >= 0 && prefixIdx > suffixIdx && middleIdx > prefixIdx,
		"tokens in SPM order")

	// Cursor marker lands between CURRENT and separator.
	currentIdx := strings.Index(prompt, currentMarker)
	sepIdx := strings.Index(prompt, separator)
	cursorIdx := strings.Index(prompt, cursorMarker)
	assert.True(t, currentIdx >= 0 && cursorIdx > currentIdx && cursorIdx < sepIdx,
		"cursor marker inside CURRENT block")

	// Filename header uses <filename> marker.
	assert.True(t, strings.Contains(prompt, fileMarker+"main.go\n"), "filename marker + path")
}

func TestAssemblePrompt_CursorPositionInLine(t *testing.T) {
	p := newTestProvider()
	lines := []string{"hello world"}
	ctx := &provider.RequestState{
		Input:        testInput("a.txt", lines, 1, 5),
		TrimmedLines: lines,
		CursorLine:   0,
	}

	prompt, _, _ := assemblePrompt(p, ctx)
	assert.True(t, strings.Contains(prompt, "hello"+cursorMarker+" world"),
		"cursor marker inserted at column")
}

func TestAssemblePrompt_SuffixContainsPostEditableLines(t *testing.T) {
	p := newTestProvider()
	// 40 lines — editable defaults to ±15 around cursor at line 10, so there
	// should be content after the editable region that lands in the suffix section.
	lines := make([]string, 40)
	for i := range lines {
		lines[i] = "line" + string(rune('A'+(i%26)))
	}
	ctx := &provider.RequestState{
		Input:        testInput("big.go", lines, 11, 0),
		TrimmedLines: lines,
		CursorLine:   10,
	}

	start, end := computeEditableRange(lines, 10, 0, nil)
	assert.Equal(t, 0, start, "start clamps toward 0 then expands, got editable start")
	_ = end
	assert.True(t, end < len(lines), "editable end leaves room for suffix content")

	prompt, _, _ := assemblePrompt(p, ctx)
	afterEditable := lines[end]
	suffixSection := prompt[strings.Index(prompt, fimSuffix)+len(fimSuffix) : strings.Index(prompt, fimPrefix)]
	assert.True(t, strings.Contains(suffixSection, afterEditable),
		"lines after editable region appear in suffix section")
}

func TestComputeEditableRange_Center(t *testing.T) {
	trimmed := make([]string, 100)
	start, end := computeEditableRange(trimmed, 50, 0, nil)
	assert.Equal(t, 50-editableLinesBefore, start, "centers before cursor")
	assert.Equal(t, 50+1+editableLinesAfter, end, "centers after cursor")
}

func TestComputeEditableRange_NearStart(t *testing.T) {
	trimmed := make([]string, 100)
	start, end := computeEditableRange(trimmed, 2, 0, nil)
	assert.Equal(t, 0, start, "clamps to 0")
	assert.Equal(t, 2+1+editableLinesAfter, end, "after unchanged")
}

func TestComputeEditableRange_NearEnd(t *testing.T) {
	trimmed := make([]string, 20)
	start, end := computeEditableRange(trimmed, 19, 0, nil)
	assert.Equal(t, 19-editableLinesBefore, start, "before unchanged")
	assert.Equal(t, 20, end, "clamps to len")
}

func TestComputeEditableRange_Empty(t *testing.T) {
	start, end := computeEditableRange(nil, 0, 0, nil)
	assert.Equal(t, 0, start, "empty start")
	assert.Equal(t, 0, end, "empty end")
}

// TestComputeEditableRange_SnapsToSyntaxBoundaries verifies that when
// treesitter syntax ranges are supplied, the editable region expands outward
// to AST node boundaries rather than being cut mid-function.
func TestComputeEditableRange_SnapsToSyntaxBoundaries(t *testing.T) {
	// 50-line buffer, cursor at line 25. Default range: [10, 41).
	trimmed := make([]string, 50)
	for i := range trimmed {
		trimmed[i] = "x"
	}
	// Treesitter reports a function spanning lines 5-45 (1-indexed).
	// That extends past both ends of the default ±15 window, so snap
	// should expand outward to cover the whole function.
	ranges := []*types.LineRange{
		{StartLine: 5, EndLine: 45},
	}

	start, end := computeEditableRange(trimmed, 25, 0, ranges)

	// Snapped region is 0-indexed inclusive end → [4, 45) exclusive.
	assert.Equal(t, 4, start, "snaps start outward to function boundary")
	assert.Equal(t, 45, end, "snaps end outward to function boundary")
}

// TestComputeEditableRange_SnapRespectsWindowStart verifies that absolute
// syntax ranges are correctly translated into trimmed-window coordinates.
func TestComputeEditableRange_SnapRespectsWindowStart(t *testing.T) {
	// Trimmed window is lines 100-150 of the full buffer (windowStart=100).
	trimmed := make([]string, 50)
	for i := range trimmed {
		trimmed[i] = "x"
	}
	// In full buffer coords, a node spans lines 105-145 (1-indexed).
	// That translates to trimmed-window coords as [4, 44] (0-indexed
	// inclusive) which is within the ±15 window at cursor 25.
	ranges := []*types.LineRange{
		{StartLine: 105, EndLine: 145},
	}

	start, end := computeEditableRange(trimmed, 25, 100, ranges)

	assert.Equal(t, 4, start, "absolute range translated and snapped")
	assert.Equal(t, 45, end, "absolute range translated and snapped (exclusive)")
}

func TestFormatEditableWithCursor_Basic(t *testing.T) {
	result := formatEditableWithCursor([]string{"abc", "def"}, 1, 1)
	assert.Equal(t, "abc\nd"+cursorMarker+"ef", result, "cursor in second line")
}

func TestFormatEditableWithCursor_BeyondLineLength(t *testing.T) {
	result := formatEditableWithCursor([]string{"abc"}, 0, 999)
	assert.Equal(t, "abc"+cursorMarker, result, "cursor at EOL")
}

func TestFormatEditableWithCursor_EmptyLines(t *testing.T) {
	result := formatEditableWithCursor(nil, 0, 0)
	assert.Equal(t, cursorMarker, result, "just the marker when empty")
}

func TestBuildEditHistory_Empty(t *testing.T) {
	assert.Equal(t, "", buildEditHistory(nil), "nil history")
	assert.Equal(t, "", buildEditHistory([]*types.FileDiffHistory{}), "empty slice")
}

func TestBuildEditHistory_UnifiedDiffFormat(t *testing.T) {
	history := []*types.FileDiffHistory{
		{
			FileName: "src/main.go",
			DiffHistory: []*types.DiffEntry{
				{
					Original:    "old line",
					Updated:     "new line",
					Source:      types.DiffSourceManual,
					TimestampNs: 1000,
				},
			},
		},
	}
	result := buildEditHistory(history)
	assert.True(t, strings.Contains(result, "--- a/src/main.go\n"), "has --- header")
	assert.True(t, strings.Contains(result, "+++ b/src/main.go\n"), "has +++ header")
	assert.True(t, strings.Contains(result, "-old line"), "has deletion")
	assert.True(t, strings.Contains(result, "+new line"), "has addition")
}

func TestBuildEditHistory_PredictedAnnotation(t *testing.T) {
	history := []*types.FileDiffHistory{
		{
			FileName: "a.go",
			DiffHistory: []*types.DiffEntry{
				{
					Original:    "foo",
					Updated:     "bar",
					Source:      types.DiffSourcePredicted,
					TimestampNs: 1,
				},
			},
		},
	}
	result := buildEditHistory(history)
	assert.True(t, strings.Contains(result, "// User accepted prediction:\n"),
		"predicted annotation present")
}

func TestBuildEditHistory_NewestLastOrder(t *testing.T) {
	history := []*types.FileDiffHistory{
		{
			FileName: "a.go",
			DiffHistory: []*types.DiffEntry{
				{Original: "v1", Updated: "v2", TimestampNs: 100},
				{Original: "v2", Updated: "v3", TimestampNs: 200},
				{Original: "v3", Updated: "v4", TimestampNs: 300},
			},
		},
	}
	result := buildEditHistory(history)
	idx100 := strings.Index(result, "-v1")
	idx200 := strings.Index(result, "-v2")
	idx300 := strings.Index(result, "-v3")
	assert.True(t, idx100 < idx200 && idx200 < idx300,
		"oldest-first in prompt output (newest-last)")
}

func TestBuildEditHistory_CappedToMaxEvents(t *testing.T) {
	entries := make([]*types.DiffEntry, 10)
	for i := range entries {
		entries[i] = &types.DiffEntry{
			Original:    "old" + string(rune('0'+i)),
			Updated:     "new" + string(rune('0'+i)),
			TimestampNs: int64(i + 1),
		}
	}
	history := []*types.FileDiffHistory{{FileName: "a.go", DiffHistory: entries}}
	result := buildEditHistory(history)

	// Only the 6 most-recent events should appear. Oldest events (old0..old3) are dropped.
	assert.False(t, strings.Contains(result, "-old0"), "oldest dropped")
	assert.False(t, strings.Contains(result, "-old3"), "oldest dropped")
	assert.True(t, strings.Contains(result, "-old9"), "newest kept")
	assert.True(t, strings.Contains(result, "-old4"), "6th newest kept")
}

func TestBuildEditHistory_UnixPathNormalization(t *testing.T) {
	history := []*types.FileDiffHistory{
		{
			FileName: "src\\win\\path.go",
			DiffHistory: []*types.DiffEntry{
				{Original: "a", Updated: "b", TimestampNs: 1},
			},
		},
	}
	result := buildEditHistory(history)
	assert.True(t, strings.Contains(result, "a/src/win/path.go"), "backslashes normalized")
}

func TestParseCompletion_StripsEndMarker(t *testing.T) {
	p := newTestProvider()
	lines := []string{"line1", "line2", "line3"}
	ctx := &provider.RequestState{
		Input:        testInput("", lines, 0, 0),
		TrimmedLines: lines,
		WindowStart:  0,
		CursorLine:   1,
	}
	resp := p.ParseResult(p, ctx, &openai.StreamResult{Text: "new1\nnew2\nnew3" + endMarker})
	assert.True(t, len(resp.Completions) > 0, "has completion")
	for _, line := range resp.Completions[0].Lines {
		assert.False(t, strings.Contains(line, endMarker), "no end marker in output")
	}
}

func TestParseCompletion_StripsCursorMarker(t *testing.T) {
	p := newTestProvider()
	lines := []string{"line1", "line2"}
	ctx := &provider.RequestState{
		Input:        testInput("", lines, 0, 0),
		TrimmedLines: lines,
		WindowStart:  0,
		CursorLine:   0,
	}
	resp := p.ParseResult(p, ctx, &openai.StreamResult{Text: "pre" + cursorMarker + "post\nother"})
	for _, line := range resp.Completions[0].Lines {
		assert.False(t, strings.Contains(line, cursorMarker), "cursor marker stripped")
	}
}

func TestSplitLines_PreservesBoundaryBlankLines(t *testing.T) {
	lines := text.SplitLines("\nfirst\nsecond\n\n")
	assert.Equal(t, 4, len(lines), "preserves legitimate leading/trailing blank lines")
	assert.Equal(t, "", lines[0], "leading blank line preserved")
	assert.Equal(t, "first", lines[1], "first content line preserved")
	assert.Equal(t, "second", lines[2], "second content line preserved")
	assert.Equal(t, "", lines[3], "trailing blank line preserved")
}

func TestParseCompletion_PreservesBoundaryBlankLines(t *testing.T) {
	p := newTestProvider()
	lines := []string{"a", "b", "c"}
	ctx := &provider.RequestState{
		Input:        testInput("", lines, 2, 0),
		TrimmedLines: lines,
		WindowStart:  0,
		CursorLine:   1,
	}

	resp := p.ParseResult(p, ctx, &openai.StreamResult{Text: "\na\nb\n\n" + endMarker})
	assert.True(t, len(resp.Completions) > 0, "has completion")
	assert.Equal(t, []string{"", "a", "b", ""}, resp.Completions[0].Lines, "blank lines preserved at both boundaries")
}

func TestParseCompletion_NoEditsSentinel(t *testing.T) {
	p := newTestProvider()
	lines := []string{"line1"}
	ctx := &provider.RequestState{
		Input:        testInput("", lines, 0, 0),
		TrimmedLines: lines,
		WindowStart:  0,
		CursorLine:   0,
	}
	resp := p.ParseResult(p, ctx, &openai.StreamResult{Text: "NO_EDITS\n" + endMarker})
	assert.Equal(t, 0, len(resp.Completions), "no completions on NO_EDITS")
}

func TestParseCompletion_EmptyAfterStripping(t *testing.T) {
	p := newTestProvider()
	lines := []string{"line1"}
	ctx := &provider.RequestState{
		Input:        testInput("", lines, 0, 0),
		TrimmedLines: lines,
		WindowStart:  0,
		CursorLine:   0,
	}
	resp := p.ParseResult(p, ctx, &openai.StreamResult{Text: endMarker})
	assert.Equal(t, 0, len(resp.Completions), "no completions for empty output")
}

func TestParseCompletion_ReplacesEditableRegion(t *testing.T) {
	p := newTestProvider()
	// 5 lines, cursor at line 2 → editable spans full buffer (cursor ±15 clamps).
	lines := []string{"a", "b", "c", "d", "e"}
	ctx := &provider.RequestState{
		Input:        testInput("", lines, 3, 0),
		TrimmedLines: lines,
		WindowStart:  0,
		CursorLine:   2,
	}
	resp := p.ParseResult(p, ctx, &openai.StreamResult{Text: "a\nb\nNEW\nd\ne\n" + endMarker})
	assert.True(t, len(resp.Completions) > 0, "has completion")
	completion := resp.Completions[0]
	assert.Equal(t, 1, completion.StartLine, "replaces from line 1")
	assert.Equal(t, 5, completion.EndLineInc, "to line 5 inclusive")
	assert.Equal(t, 5, len(completion.Lines), "5 new lines")
	assert.Equal(t, "NEW", completion.Lines[2], "replacement preserved")
}

func TestAssemblePrompt_WithEditHistory(t *testing.T) {
	p := newTestProvider()
	lines := []string{"x", "y", "z"}
	editHistory := []*types.FileDiffHistory{
		{
			FileName: "main.go",
			DiffHistory: []*types.DiffEntry{
				{Original: "old", Updated: "new", TimestampNs: 1},
			},
		},
	}
	ctx := &provider.RequestState{
		Input:        testInput("main.go", lines, 2, 1, sourcectx.Materials{sourcectx.EditHistory{Files: editHistory}}),
		TrimmedLines: lines,
		CursorLine:   1,
	}

	prompt, _, _ := assemblePrompt(p, ctx)
	assert.True(t, strings.Contains(prompt, fileMarker+"edit_history\n"),
		"edit_history section present")
	assert.True(t, strings.Contains(prompt, "--- a/main.go\n"), "unified diff header")

	// edit_history must come before the cursor file section.
	historyIdx := strings.Index(prompt, fileMarker+"edit_history\n")
	cursorFileIdx := strings.Index(prompt, fileMarker+"main.go\n")
	assert.True(t, historyIdx >= 0 && cursorFileIdx > historyIdx,
		"edit_history precedes cursor file section")
}

// --- Pseudo-file context sections ---

func TestWriteRecentFilesPseudoFiles_Empty(t *testing.T) {
	var b strings.Builder
	writeRecentFilesPseudoFiles(&b, nil)
	assert.Equal(t, "", b.String(), "nil writes nothing")
}

func TestWriteRecentFilesPseudoFiles_SkipsEmptyLines(t *testing.T) {
	var b strings.Builder
	writeRecentFilesPseudoFiles(&b, []*types.RecentBufferSnapshot{
		{FilePath: "empty.go", Lines: []string{}},
		{FilePath: "real.go", Lines: []string{"package real"}},
	})
	assert.False(t, strings.Contains(b.String(), "empty.go"), "empty snapshot skipped")
	assert.True(t, strings.Contains(b.String(), "real.go"), "non-empty snapshot included")
}

func TestWriteRecentFilesPseudoFiles_BlockFormat(t *testing.T) {
	var b strings.Builder
	writeRecentFilesPseudoFiles(&b, []*types.RecentBufferSnapshot{
		{FilePath: "a.go", Lines: []string{"package a", "func A() {}"}},
		{FilePath: "b.go", Lines: []string{"package b"}},
	})
	out := b.String()
	assert.True(t, strings.Contains(out, fileMarker+"a.go\npackage a\nfunc A() {}\n"),
		"first file block formatted correctly")
	assert.True(t, strings.Contains(out, fileMarker+"b.go\npackage b\n"),
		"second file block formatted correctly")

	idxA := strings.Index(out, fileMarker+"a.go")
	idxB := strings.Index(out, fileMarker+"b.go")
	assert.True(t, idxA >= 0 && idxB > idxA, "files in insertion order")
}

func TestWriteDiagnosticsPseudoFile_Empty(t *testing.T) {
	var b strings.Builder
	writeDiagnosticsPseudoFile(&b, nil)
	assert.Equal(t, "", b.String(), "nil writes nothing")

	writeDiagnosticsPseudoFile(&b, &types.Diagnostics{Items: nil})
	assert.Equal(t, "", b.String(), "empty errors writes nothing")
}

func TestWriteDiagnosticsPseudoFile_Format(t *testing.T) {
	var b strings.Builder
	writeDiagnosticsPseudoFile(&b, &types.Diagnostics{
		Items: []*types.Diagnostic{
			{
				Severity: types.SeverityError,
				Message:  "undefined: foo",
				Source:   "gopls",
				Range:    &types.CursorRange{StartLine: 10},
			},
			{
				Severity: types.SeverityWarning,
				Message:  "unused variable bar",
				Source:   "gopls",
			},
		},
	})
	out := b.String()
	assert.True(t, strings.HasPrefix(out, fileMarker+"diagnostics\n"), "starts with diagnostics header")
	assert.True(t, strings.Contains(out, "line 10: [ERROR] undefined: foo (source: gopls)"), "first diag")
	assert.True(t, strings.Contains(out, "[WARNING] unused variable bar (source: gopls)"), "second diag")
}

func TestWriteTreesitterPseudoFile_Empty(t *testing.T) {
	var b strings.Builder
	writeTreesitterPseudoFile(&b, nil)
	assert.Equal(t, "", b.String(), "nil writes nothing")

	writeTreesitterPseudoFile(&b, &types.TreesitterContext{})
	assert.Equal(t, "", b.String(), "empty context writes nothing")
}

func TestWriteTreesitterPseudoFile_Format(t *testing.T) {
	var b strings.Builder
	writeTreesitterPseudoFile(&b, &types.TreesitterContext{
		EnclosingSignature: "func handleRequest(w http.ResponseWriter, r *http.Request)",
		Siblings: []*types.TreesitterSymbol{
			{Line: 5, Signature: "func otherFunc()"},
		},
		Imports: []string{"import \"net/http\""},
	})
	out := b.String()
	assert.True(t, strings.HasPrefix(out, fileMarker+"context/treesitter\n"),
		"starts with treesitter header")
	assert.True(t, strings.Contains(out, "Enclosing scope: func handleRequest"), "enclosing rendered")
	assert.True(t, strings.Contains(out, "Sibling: line 5: func otherFunc()"), "sibling with line")
	assert.True(t, strings.Contains(out, "Import: import \"net/http\""), "imports rendered")
}

func TestWriteGitDiffPseudoFile_Empty(t *testing.T) {
	var b strings.Builder
	writeGitDiffPseudoFile(&b, nil)
	assert.Equal(t, "", b.String(), "nil writes nothing")

	writeGitDiffPseudoFile(&b, &types.GitDiffContext{})
	assert.Equal(t, "", b.String(), "empty diff writes nothing")
}

func TestWriteGitDiffPseudoFile_Format(t *testing.T) {
	var b strings.Builder
	writeGitDiffPseudoFile(&b, &types.GitDiffContext{
		Diff: "diff --git a/x.go b/x.go\n+new\n-old",
	})
	out := b.String()
	assert.True(t, strings.HasPrefix(out, fileMarker+"context/staged_diff\n"),
		"starts with staged_diff header")
	assert.True(t, strings.Contains(out, "+new"), "diff body included")
}

// --- Cursor-marker streaming strip & CursorTarget capture ---

func TestBuildCursorTarget_BasicOffsetTranslation(t *testing.T) {
	ctx := &provider.RequestState{
		Input:       testInput("src/main.py", nil, 0, 0),
		WindowStart: 10, // trimmed window starts at buffer line 11 (1-indexed)
	}
	newLines := []string{"alpha", "beta", "gamma", "delta", "epsilon"}

	target := buildCursorTarget(ctx, 2, 3, newLines)

	// bufferRow = WindowStart(10) + editableStart(2) + lineIdx(3) + 1 = 16
	assert.Equal(t, int32(16), target.LineNumber, "translated buffer row")
	assert.Equal(t, "src/main.py", target.RelativePath, "relative path preserved")
	assert.Equal(t, "delta", target.ExpectedContent, "expected content = stripped line at marker index")
	assert.True(t, target.ShouldRetrigger, "auto-chain prefetch enabled")
}

func TestBuildCursorTarget_ClampsBeyondNewLines(t *testing.T) {
	ctx := &provider.RequestState{
		Input:       testInput("f.go", nil, 0, 0),
		WindowStart: 0,
	}
	newLines := []string{"a", "b", "c"}

	target := buildCursorTarget(ctx, 0, 99, newLines)
	assert.Equal(t, int32(3), target.LineNumber, "clamped to last new line")
	assert.Equal(t, "c", target.ExpectedContent, "expected = last line")
}

func TestBuildCursorTarget_NoNewLines(t *testing.T) {
	ctx := &provider.RequestState{
		Input: testInput("f.go", nil, 0, 0),
	}
	assert.Nil(t, buildCursorTarget(ctx, 0, 0, nil), "nil target when no lines")
}

func TestParseCompletion_PopulatesCursorTargetWhenMarkerSeen(t *testing.T) {
	p := newTestProvider()

	// 5-line buffer, cursor on line 3. editable region = full buffer (cursor ±15 clamped).
	lines := []string{"a", "b", "c", "d", "e"}
	ctx := &provider.RequestState{
		Input:        testInput("main.go", lines, 3, 0),
		TrimmedLines: lines,
		WindowStart:  0,
		CursorLine:   2,
	}

	resp := parseCompletionWithCursorMarker(p, ctx, &openai.StreamResult{Text: "a\nb\nNEW\nd\ne\n" + endMarker}, true, 2)
	assert.NotNil(t, resp.CursorTarget, "cursor target populated")
	assert.Equal(t, int32(3), resp.CursorTarget.LineNumber, "buffer row = 0 + 0 + 2 + 1 = 3")
	assert.True(t, resp.CursorTarget.ShouldRetrigger, "retrigger enabled")
	assert.Equal(t, "NEW", resp.CursorTarget.ExpectedContent, "expected content = new line 3")
}

func TestParseCompletion_NoCursorTargetWhenMarkerAbsent(t *testing.T) {
	p := newTestProvider()
	lines := []string{"a", "b", "c"}
	ctx := &provider.RequestState{
		Input:        testInput("main.go", lines, 2, 0),
		TrimmedLines: lines,
		WindowStart:  0,
		CursorLine:   1,
	}

	resp := p.ParseResult(p, ctx, &openai.StreamResult{Text: "a\nNEW\nc\n" + endMarker})
	assert.Nil(t, resp.CursorTarget, "no cursor target when marker absent")
}

func TestAssemblePrompt_ContextOrderingAndCoexistence(t *testing.T) {
	p := newTestProvider()
	lines := []string{"package main", "", "func main() {}"}
	recentFiles := []*types.RecentBufferSnapshot{
		{FilePath: "helper.go", Lines: []string{"package main", "func help() {}"}},
	}
	diagnostics := &types.Diagnostics{
		Items: []*types.Diagnostic{
			{Severity: types.SeverityError, Message: "oops", Source: "gopls", Range: &types.CursorRange{StartLine: 3}},
		},
	}
	treesitter := &types.TreesitterContext{
		EnclosingSignature: "func main()",
		Imports:            []string{"import \"fmt\""},
	}
	gitDiff := &types.GitDiffContext{Diff: "some diff"}
	editHistory := []*types.FileDiffHistory{
		{
			FileName: "main.go",
			DiffHistory: []*types.DiffEntry{
				{Original: "old", Updated: "new", TimestampNs: 1},
			},
		},
	}
	ctx := &provider.RequestState{
		Input: testInput("main.go", lines, 3, 13, sourcectx.Materials{
			sourcectx.RecentFiles{Files: recentFiles},
			sourcectx.Diagnostics{Data: diagnostics},
			sourcectx.Treesitter{Data: treesitter},
			sourcectx.GitDiff{Data: gitDiff},
			sourcectx.EditHistory{Files: editHistory},
		}),
		TrimmedLines: lines,
		CursorLine:   2,
	}

	prompt, _, _ := assemblePrompt(p, ctx)

	// Every context section present.
	assert.True(t, strings.Contains(prompt, fileMarker+"helper.go\n"), "recent file present")
	assert.True(t, strings.Contains(prompt, fileMarker+"diagnostics\n"), "diagnostics present")
	assert.True(t, strings.Contains(prompt, fileMarker+"context/treesitter\n"), "treesitter present")
	assert.True(t, strings.Contains(prompt, fileMarker+"context/staged_diff\n"), "git diff present")
	assert.True(t, strings.Contains(prompt, fileMarker+"edit_history\n"), "edit history present")
	assert.True(t, strings.Contains(prompt, fileMarker+"main.go\n"), "cursor file present")

	// Ordering inside the fim-prefix block:
	//   recent_files -> diagnostics -> treesitter -> staged_diff -> edit_history -> cursor file
	idxRecent := strings.Index(prompt, fileMarker+"helper.go")
	idxDiag := strings.Index(prompt, fileMarker+"diagnostics")
	idxTS := strings.Index(prompt, fileMarker+"context/treesitter")
	idxGit := strings.Index(prompt, fileMarker+"context/staged_diff")
	idxHist := strings.Index(prompt, fileMarker+"edit_history")
	idxCursor := strings.LastIndex(prompt, fileMarker+"main.go") // last one = cursor file section
	assert.True(t, idxRecent < idxDiag, "recent files before diagnostics")
	assert.True(t, idxDiag < idxTS, "diagnostics before treesitter")
	assert.True(t, idxTS < idxGit, "treesitter before git diff")
	assert.True(t, idxGit < idxHist, "git diff before edit history")
	assert.True(t, idxHist < idxCursor, "edit history before cursor file")

	// All context blocks live inside the fim-prefix region, not the fim-suffix region.
	prefixIdx := strings.Index(prompt, fimPrefix)
	suffixIdx := strings.Index(prompt, fimSuffix)
	assert.True(t, suffixIdx < prefixIdx, "suffix token comes before prefix (SPM order)")
	assert.True(t, idxRecent > prefixIdx, "pseudo-files live inside fim-prefix region")
}

func TestStripCursorMarker(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{
			name: "inline marker stripped",
			text: "    return arr<|user_cursor|>\nother line\n",
			want: "    return arr\nother line\n",
		},
		{
			name: "marker only line dropped",
			text: "  }\n<|user_cursor|>\n",
			want: "  }\n",
		},
		{
			name: "marker only line with whitespace dropped",
			text: "  }\n  <|user_cursor|>  \n",
			want: "  }\n",
		},
		{
			name: "no marker",
			text: "hello\nworld\n",
			want: "hello\nworld\n",
		},
		{
			name: "marker at end of text",
			text: "  }\n<|user_cursor|>",
			want: "  }",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, stripCursorMarker(tt.text, cursorMarker), "stripped text")
		})
	}
}

func TestParseCompletion_MarkerOnlyLineDropped(t *testing.T) {
	p := newTestProvider()
	lines := []string{"func foo() {", "    return 1", "}"}
	ctx := &provider.RequestState{
		Input:        testInput("", lines, 2, 0),
		TrimmedLines: lines,
		WindowStart:  0,
		CursorLine:   1,
	}
	resp := p.ParseResult(p, ctx, &openai.StreamResult{
		Text: "func foo() {\n    return 2\n}\n" + cursorMarker + "\n" + endMarker,
	})
	assert.True(t, len(resp.Completions) > 0, "has completions")
	// The completion should have exactly 3 lines (matching old line count),
	// not 4 with trailing empty from the stripped marker line.
	assert.Equal(t, 3, len(resp.Completions[0].Lines), "marker-only line excluded from completion")
}
