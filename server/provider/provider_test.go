package provider

import (
	"cursortab/assert"
	"testing"
)

// TestContext_TrimmedContextInterface verifies that Context implements
// the methods needed for TrimmedContext interface in engine.
// This ensures the engine can extract trim info from provider context.
func TestContext_TrimmedContextInterface(t *testing.T) {
	ctx := &Context{
		WindowStart:  20,
		TrimmedLines: []string{"line 1", "line 2", "line 3"},
	}

	// These methods should exist and return correct values
	assert.Equal(t, 20, ctx.GetWindowStart(), "GetWindowStart")

	lines := ctx.GetTrimmedLines()
	assert.Equal(t, 3, len(lines), "GetTrimmedLines length")
	assert.Equal(t, "line 1", lines[0], "GetTrimmedLines[0]")
}

// TestContext_EmptyTrimmedLines verifies behavior when no trimming occurred.
func TestContext_EmptyTrimmedLines(t *testing.T) {
	ctx := &Context{
		WindowStart:  0,
		TrimmedLines: nil,
	}

	assert.Equal(t, 0, ctx.GetWindowStart(), "GetWindowStart")

	lines := ctx.GetTrimmedLines()
	assert.Nil(t, lines, "GetTrimmedLines should be nil")
}

func TestContext_TransformLine_NoMarker(t *testing.T) {
	ctx := &Context{}
	result := ctx.TransformLine("hello world")
	assert.Equal(t, "hello world", result, "line unchanged when no marker configured")
	assert.Equal(t, 1, ctx.LinesReceived, "counter still increments")
	assert.False(t, ctx.CursorMarkerSeen, "marker not seen")
}

func TestContext_TransformLine_StripsMarker(t *testing.T) {
	ctx := &Context{CursorMarker: "<|user_cursor|>"}

	line1 := ctx.TransformLine("    return arr<|user_cursor|>")
	assert.Equal(t, "    return arr", line1, "marker stripped")
	assert.True(t, ctx.CursorMarkerSeen, "marker seen")
	assert.Equal(t, 0, ctx.CursorMarkerLine, "line index captured")
	assert.Equal(t, 14, ctx.CursorMarkerCol, "column captured (byte offset before marker)")
	assert.Equal(t, 1, ctx.LinesReceived, "counter incremented")
}

func TestContext_TransformLine_MarkerMidStream(t *testing.T) {
	ctx := &Context{CursorMarker: "<|user_cursor|>"}

	ctx.TransformLine("def bubble_sort(arr):")
	ctx.TransformLine("    for i in range(len(arr)):")
	ctx.TransformLine("    return arr")
	ctx.TransformLine("")
	result := ctx.TransformLine("    return arr<|user_cursor|>")

	assert.Equal(t, "    return arr", result, "marker stripped on line 5")
	assert.True(t, ctx.CursorMarkerSeen, "marker seen")
	assert.Equal(t, 4, ctx.CursorMarkerLine, "line index = 4 (0-indexed, 5th line)")
	assert.Equal(t, 14, ctx.CursorMarkerCol, "column captured")
	assert.Equal(t, 5, ctx.LinesReceived, "counter shows 5 lines processed")
}

func TestContext_TransformLine_OnlyFirstOccurrenceRecorded(t *testing.T) {
	ctx := &Context{CursorMarker: "<|user_cursor|>"}

	ctx.TransformLine("a<|user_cursor|>b")
	ctx.TransformLine("c<|user_cursor|>d")

	assert.Equal(t, 0, ctx.CursorMarkerLine, "first occurrence wins")
	assert.Equal(t, 1, ctx.CursorMarkerCol, "first occurrence column")
}

func TestContext_TransformLine_StripsAllInstancesOnOneLine(t *testing.T) {
	ctx := &Context{CursorMarker: "<|user_cursor|>"}

	result := ctx.TransformLine("a<|user_cursor|>b<|user_cursor|>c")
	assert.Equal(t, "abc", result, "all instances stripped from the line")
	assert.Equal(t, 1, ctx.CursorMarkerCol, "first occurrence column recorded")
}

func TestContext_TransformLine_SkipLineWhenMarkerOnly(t *testing.T) {
	ctx := &Context{CursorMarker: "<|user_cursor|>"}

	result := ctx.TransformLine("<|user_cursor|>")
	assert.Equal(t, "", result, "line stripped to empty")
	assert.True(t, ctx.ShouldSkipLine(), "marker-only line should be skipped")
	assert.True(t, ctx.CursorMarkerSeen, "marker still captured")
}

func TestContext_TransformLine_SkipLineWithWhitespace(t *testing.T) {
	ctx := &Context{CursorMarker: "<|user_cursor|>"}

	result := ctx.TransformLine("  <|user_cursor|>  ")
	assert.Equal(t, "    ", result, "whitespace preserved in return value")
	assert.True(t, ctx.ShouldSkipLine(), "whitespace-only remnant should be skipped")
}

func TestContext_TransformLine_NoSkipWhenContentRemains(t *testing.T) {
	ctx := &Context{CursorMarker: "<|user_cursor|>"}

	ctx.TransformLine("    return arr<|user_cursor|>")
	assert.False(t, ctx.ShouldSkipLine(), "content remains — do not skip")
}

func TestContext_TransformLine_SkipLineResets(t *testing.T) {
	ctx := &Context{CursorMarker: "<|user_cursor|>"}

	ctx.TransformLine("<|user_cursor|>")
	assert.True(t, ctx.ShouldSkipLine(), "first call: skip")

	ctx.TransformLine("normal line")
	assert.False(t, ctx.ShouldSkipLine(), "second call: no skip")
}
