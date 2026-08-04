package zeta2

import (
	"fmt"
	"strings"
	"testing"

	"cursortab/assert"
	"cursortab/client/openai"
	sourcectx "cursortab/ctx"
	"cursortab/engine"
	"cursortab/provider"
	"cursortab/types"
)

func newZeta21TestProvider() *Provider21 {
	return NewProvider21(&types.ProviderConfig{
		ProviderModel:     "zeta-2.1",
		ProviderMaxTokens: 2048,
	})
}

func TestZeta21Build_UsesNumberedRegionProtocol(t *testing.T) {
	p := newZeta21TestProvider()
	ctx := stateForLines("main.go", []string{
		"package main",
		"",
		"func main() {",
		"\tprintln(\"hello\")",
		"}",
	}, 4, 10)

	req, err := p.Build(ctx)
	assert.Equal(t, nil, err, "build should succeed")
	assert.Equal(t, []string{zeta21EOSMarker}, req.Stop, "uses the model EOS stop token")
	assert.True(t, strings.Contains(req.Prompt, "<|marker_1|>\n"), "contains opening numbered boundary")
	assert.True(t, strings.Contains(req.Prompt, "<|marker_2|>\n"), "contains closing numbered boundary")
	assert.True(t, strings.Contains(req.Prompt, "println(\""+cursorMarker+"hello\")"), "places the cursor inside the editable excerpt")
	assert.False(t, strings.Contains(req.Prompt, currentMarker), "does not use Zeta-2 CURRENT scaffolding")
	assert.False(t, strings.Contains(req.Prompt, separator), "does not use Zeta-2 separator scaffolding")
	assert.True(t, strings.HasSuffix(req.Prompt, fimMiddle), "ends with fim-middle")
}

func TestZeta21Build_V0318BoundariesSplitLongEditableExcerpt(t *testing.T) {
	p := newZeta21TestProvider()
	lines := make([]string, 40)
	for i := range lines {
		lines[i] = "line " + strings.Repeat("x", i%3)
	}
	ctx := stateForLines("main.go", lines, 20, 0)

	req, err := p.Build(ctx)
	assert.Equal(t, nil, err, "build should succeed")
	boundaries := zeta21MarkerBoundaryLines(ctx)

	assert.Equal(t, []int{4, 20, 35}, boundaries, "hard cap creates deterministic line boundaries")
	assert.True(t, strings.Contains(req.Prompt, "<|marker_3|>"), "renders every computed boundary")
	assert.False(t, strings.Contains(req.Prompt, "<|marker_4|>"), "does not render an extra boundary")
}

func TestZeta21Build_PrefersBlankLineBoundary(t *testing.T) {
	lines := make([]string, 31)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i)
	}
	lines[7] = ""
	ctx := stateForLines("main.go", lines, 16, 0)

	boundaries := zeta21MarkerBoundaryLines(ctx)

	assert.GreaterOrEqual(t, len(boundaries), 3, "blank-line excerpt has multiple boundaries")
	if len(boundaries) < 3 {
		return
	}
	assert.Equal(t, 0, boundaries[0], "editable excerpt starts at the first line")
	assert.Equal(t, 8, boundaries[1], "new block starts after the blank line")
	assert.Equal(t, 31, boundaries[len(boundaries)-1], "last boundary ends the editable excerpt")
}

func TestZeta21Build_PreservesUTF8CursorByteOffset(t *testing.T) {
	p := newZeta21TestProvider()
	ctx := stateForLines("main.go", []string{"яcursor"}, 1, 2)

	req, err := p.Build(ctx)
	assert.Equal(t, nil, err, "build should succeed")
	assert.True(t, strings.Contains(req.Prompt, "я"+cursorMarker+"cursor"), "cursor byte offset remains on a UTF-8 boundary")
}

func TestZeta21Build_EmptyBufferHasInsertionBoundaries(t *testing.T) {
	p := newZeta21TestProvider()
	ctx := stateForLines("main.go", nil, 1, 0)

	req, err := p.Build(ctx)
	assert.Equal(t, nil, err, "build should succeed")
	assert.True(t, strings.Contains(req.Prompt, "<|marker_1|>\n"+cursorMarker+"\n<|marker_2|>"), "empty buffer exposes a zero-width editable span")
}

func TestZeta21Build_IncludesOnlyDiagnosticsIntersectingEditableExcerpt(t *testing.T) {
	p := newZeta21TestProvider()
	lines := make([]string, 60)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i+1)
	}
	diagnostics := &types.Diagnostics{Items: []*types.Diagnostic{
		{Severity: types.SeverityError, Message: "before excerpt", Range: &types.CursorRange{StartLine: 14, EndLine: 14}},
		{Severity: types.SeverityError, Message: "overlaps excerpt start", Range: &types.CursorRange{StartLine: 14, EndLine: 15}},
		{Severity: types.SeverityError, Message: "inside excerpt", Range: &types.CursorRange{StartLine: 30}},
		{Severity: types.SeverityError, Message: "overlaps excerpt end", Range: &types.CursorRange{StartLine: 45, EndLine: 46}},
		{Severity: types.SeverityError, Message: "after excerpt", Range: &types.CursorRange{StartLine: 46, EndLine: 46}},
		{Severity: types.SeverityError, Message: "without location"},
	}}
	ctx := stateForLines("main.go", lines, 30, 0, sourcectx.Materials{
		sourcectx.Diagnostics{Data: diagnostics},
	})

	req, err := p.Build(ctx)
	assert.Equal(t, nil, err, "build should succeed")
	assert.False(t, strings.Contains(req.Prompt, "before excerpt"), "diagnostic before editable excerpt is omitted")
	assert.True(t, strings.Contains(req.Prompt, "overlaps excerpt start"), "diagnostic overlapping excerpt start is included")
	assert.True(t, strings.Contains(req.Prompt, "inside excerpt"), "diagnostic inside editable excerpt is included")
	assert.True(t, strings.Contains(req.Prompt, "overlaps excerpt end"), "diagnostic overlapping excerpt end is included")
	assert.False(t, strings.Contains(req.Prompt, "after excerpt"), "diagnostic after editable excerpt is omitted")
	assert.False(t, strings.Contains(req.Prompt, "without location"), "diagnostic without a range is omitted")
}

func TestZeta21Build_IncludesAllDiagnosticsInEditableExcerpt(t *testing.T) {
	p := newZeta21TestProvider()
	lines := make([]string, 40)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i+1)
	}
	diagnostics := &types.Diagnostics{}
	for i := range 20 {
		diagnostics.Items = append(diagnostics.Items, &types.Diagnostic{
			Severity: types.SeverityWarning,
			Message:  fmt.Sprintf("diagnostic-%03d-end", i),
			Source:   "test",
			Range:    &types.CursorRange{StartLine: 20},
		})
	}
	ctx := stateForLines("main.go", lines, 20, 0, sourcectx.Materials{
		sourcectx.Diagnostics{Data: diagnostics},
	})

	req, err := p.Build(ctx)
	assert.Equal(t, nil, err, "build should succeed")
	assert.Equal(t, 20, strings.Count(req.Prompt, "(source: test)"), "all diagnostics in the editable excerpt are included")
	assert.True(t, strings.Contains(req.Prompt, "diagnostic-019-end"), "last matching diagnostic is included")
}

func TestZeta21Build_MapsDiagnosticFilterThroughTrimmedWindow(t *testing.T) {
	p := newZeta21TestProvider()
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i+1)
	}
	diagnostics := &types.Diagnostics{Items: []*types.Diagnostic{
		{Severity: types.SeverityError, Message: "before trimmed excerpt", Range: &types.CursorRange{StartLine: 34}},
		{Severity: types.SeverityError, Message: "at trimmed excerpt start", Range: &types.CursorRange{StartLine: 35}},
		{Severity: types.SeverityError, Message: "at trimmed excerpt end", Range: &types.CursorRange{StartLine: 65}},
		{Severity: types.SeverityError, Message: "after trimmed excerpt", Range: &types.CursorRange{StartLine: 66}},
	}}
	ctx := stateForLines("main.go", lines, 50, 0, sourcectx.Materials{
		sourcectx.Diagnostics{Data: diagnostics},
	})
	ctx.Window = provider.RequestWindow{
		Lines:      lines[20:80],
		Start:      20,
		CursorLine: 29,
	}

	req, err := p.Build(ctx)
	assert.Equal(t, nil, err, "build should succeed")
	assert.False(t, strings.Contains(req.Prompt, "before trimmed excerpt"), "diagnostic before document-relative excerpt is omitted")
	assert.True(t, strings.Contains(req.Prompt, "at trimmed excerpt start"), "document-relative excerpt start is included")
	assert.True(t, strings.Contains(req.Prompt, "at trimmed excerpt end"), "document-relative excerpt end is included")
	assert.False(t, strings.Contains(req.Prompt, "after trimmed excerpt"), "diagnostic after document-relative excerpt is omitted")
}

func TestZeta21Parse_MapsNumberedSpanToDocumentLines(t *testing.T) {
	p := newZeta21TestProvider()
	lines := make([]string, 40)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i)
	}
	lines[20] = "old value"
	ctx := stateForLines("main.go", lines, 20, 0)
	replacement := append([]string(nil), lines[20:35]...)
	replacement[0] = "new value"
	result := &openai.CompletionResult{
		Text: "<|marker_2|>\n" + strings.Join(replacement, "\n") + "\n<|marker_3|>" + zeta21EOSMarker,
	}

	resp, err := p.Parse(ctx, result)
	assert.Equal(t, nil, err, "parse should succeed")
	assert.True(t, resp.Completion != nil, "has a completion")
	if resp.Completion == nil {
		return
	}
	assert.Equal(t, 21, resp.Completion.StartLine, "starts at marker_2 boundary")
	assert.Equal(t, 35, resp.Completion.EndLineInc, "ends before marker_3 boundary")
	assert.Equal(t, replacement, resp.Completion.Lines, "uses only text inside the marker span")
}

func TestZeta21Parse_RejectsUnsafeMarkerSpans(t *testing.T) {
	p := newZeta21TestProvider()
	ctx := stateForLines("main.go", []string{"a", "b", "c"}, 2, 0)

	for _, output := range []string{
		"changed",
		"<|marker_1|>\nchanged",
		"<|marker_2|>\nchanged\n<|marker_1|>",
		"<|marker_1|>changed<|marker_1|>",
		"<|marker_1|>\na\n<|marker_2|>\nb\n<|marker_3|>",
		"<|marker_9|>\nchanged\n<|marker_10|>",
	} {
		resp, err := p.Parse(ctx, &openai.CompletionResult{Text: output})
		assert.Equal(t, nil, err, "parse should not return an error")
		assert.True(t, resp.Completion == nil, "malformed marker span is rejected")
	}
}

func TestZeta21Parse_RecognizesRepeatedMarkerAsNoEdits(t *testing.T) {
	p := newZeta21TestProvider()
	ctx := stateForLines("main.go", []string{"first", "second", "third"}, 2, 0)
	output := "<|marker_1|><|marker_1|>"

	span, ok := decodeZeta21MarkerSpan(ctx, output)
	assert.True(t, ok, "repeated marker is a valid protocol response")
	assert.True(t, span.noEdits, "repeated marker represents NO_EDITS")

	resp, err := p.Parse(ctx, &openai.CompletionResult{Text: output})
	assert.Equal(t, nil, err, "parse should succeed")
	assert.True(t, resp.Completion == nil, "NO_EDITS has no completion")
	assert.True(t, resp.CursorTarget == nil, "NO_EDITS has no cursor target")
}

func TestZeta21Parse_AppliesTrimmedWindowOffset(t *testing.T) {
	p := newZeta21TestProvider()
	lines := make([]string, 60)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i)
	}
	input := testInput("main.go", lines, 30, 0)
	ctx := stateForInput(input, nil)
	ctx.Window = provider.RequestWindow{
		Lines:      lines[10:50],
		Start:      10,
		CursorLine: 19,
	}
	replacement := append([]string(nil), lines[30:45]...)
	replacement[0] = "changed"

	resp, err := p.Parse(ctx, &openai.CompletionResult{
		Text: "<|marker_2|>\n" + strings.Join(replacement, "\n") + "\n<|marker_3|>",
	})
	assert.Equal(t, nil, err, "parse should succeed")
	assert.True(t, resp.Completion != nil, "has a completion")
	if resp.Completion == nil {
		return
	}
	assert.Equal(t, 31, resp.Completion.StartLine, "start includes the trimmed window offset")
	assert.Equal(t, 45, resp.Completion.EndLineInc, "end includes the trimmed window offset")
}

func TestZeta21Parse_PreservesDeletionOnlyEdit(t *testing.T) {
	p := newZeta21TestProvider()
	ctx := stateForLines("main.go", []string{"obsolete()"}, 1, 0)

	resp, err := p.Parse(ctx, &openai.CompletionResult{Text: "<|marker_1|>\n<|marker_2|>"})
	assert.Equal(t, nil, err, "parse should succeed")
	assert.True(t, resp.Completion != nil, "deletion is a completion")
	if resp.Completion == nil {
		return
	}
	assert.Equal(t, 1, resp.Completion.StartLine, "deletion starts at first boundary")
	assert.Equal(t, 1, resp.Completion.EndLineInc, "deletion ends at second boundary")
	assert.Equal(t, []string{}, resp.Completion.Lines, "replacement is empty")
}

func TestZeta21Parse_PreservesInsertionOnlyEdit(t *testing.T) {
	p := newZeta21TestProvider()
	ctx := stateForLines("main.go", nil, 1, 0)

	resp, err := p.Parse(ctx, &openai.CompletionResult{Text: "<|marker_1|>\ninserted()\n<|marker_2|>"})
	assert.Equal(t, nil, err, "parse should succeed")
	assert.True(t, resp.Completion != nil, "insertion is a completion")
	if resp.Completion == nil {
		return
	}
	assert.Equal(t, 1, resp.Completion.StartLine, "insertion starts at the first line")
	assert.Equal(t, 0, resp.Completion.EndLineInc, "zero-width span replaces no existing line")
	assert.Equal(t, []string{"inserted()"}, resp.Completion.Lines, "inserted content is preserved")
}

func TestZeta21Parse_MapsCursorMarkerInsideSelectedSpan(t *testing.T) {
	p := newZeta21TestProvider()
	ctx := stateForLines("main.go", []string{"first", "old", "last"}, 2, 0)

	resp, err := p.Parse(ctx, &openai.CompletionResult{
		Text: "<|marker_1|>\nfirst\nnew" + cursorMarker + "\nlast\n<|marker_2|>",
	})
	assert.Equal(t, nil, err, "parse should succeed")
	assert.True(t, resp.Completion != nil, "has a completion")
	assert.True(t, resp.CursorTarget != nil, "has a cursor target")
	if resp.Completion == nil || resp.CursorTarget == nil {
		return
	}
	assert.Equal(t, []string{"first", "new", "last"}, resp.Completion.Lines, "cursor marker is removed from code")
	assert.Equal(t, int32(2), resp.CursorTarget.LineNumber, "cursor target maps to the output line")
}

func TestZeta21Parse_RejectsTruncatedGeneration(t *testing.T) {
	p := newZeta21TestProvider()
	ctx := stateForLines("main.go", []string{"old"}, 1, 0)

	resp, err := p.Parse(ctx, &openai.CompletionResult{
		Text:         "<|marker_1|>\nnew\n<|marker_2|>",
		FinishReason: "length",
	})
	assert.Equal(t, nil, err, "parse should not return an error")
	assert.True(t, resp.Completion == nil, "truncated generation is rejected")
}

func TestZeta21Complete_ImplementsBatchFlow(t *testing.T) {
	var _ provider.CompletionFlow[*openai.CompletionRequest, *openai.CompletionResult] = newZeta21TestProvider()
}

func TestZeta21Complete_DoesNotImplementStreaming(t *testing.T) {
	_, streams := any(newZeta21TestProvider()).(engine.StreamingProvider)
	assert.False(t, streams, "Zeta 2.1 uses only the batch completion flow")
}
