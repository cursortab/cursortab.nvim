package provider

import (
	"cursortab/assert"
	"testing"
)

func TestStreamStateTransformLine(t *testing.T) {
	const marker = "<|user_cursor|>"

	tests := []struct {
		name         string
		marker       string
		lines        []string
		wantLines    []string
		wantSeen     bool
		wantLine     int
		wantReceived int
		wantSkip     bool
	}{
		{
			name:         "no marker configured",
			lines:        []string{"hello world"},
			wantLines:    []string{"hello world"},
			wantReceived: 1,
		},
		{
			name:         "marker stripped and position captured",
			marker:       marker,
			lines:        []string{"    return arr" + marker},
			wantLines:    []string{"    return arr"},
			wantSeen:     true,
			wantLine:     0,
			wantReceived: 1,
		},
		{
			name:   "marker mid stream",
			marker: marker,
			lines: []string{
				"def bubble_sort(arr):",
				"    for i in range(len(arr)):",
				"    return arr",
				"",
				"    return arr" + marker,
			},
			wantLines: []string{
				"def bubble_sort(arr):",
				"    for i in range(len(arr)):",
				"    return arr",
				"",
				"    return arr",
			},
			wantSeen:     true,
			wantLine:     4,
			wantReceived: 5,
		},
		{
			name:         "only first marker position is recorded",
			marker:       marker,
			lines:        []string{"a" + marker + "b", "c" + marker + "d"},
			wantLines:    []string{"ab", "cd"},
			wantSeen:     true,
			wantLine:     0,
			wantReceived: 2,
		},
		{
			name:         "all markers on a line are stripped",
			marker:       marker,
			lines:        []string{"a" + marker + "b" + marker + "c"},
			wantLines:    []string{"abc"},
			wantSeen:     true,
			wantLine:     0,
			wantReceived: 1,
		},
		{
			name:         "marker only line is skipped",
			marker:       marker,
			lines:        []string{marker},
			wantLines:    []string{""},
			wantSeen:     true,
			wantLine:     0,
			wantReceived: 1,
			wantSkip:     true,
		},
		{
			name:         "marker with whitespace is skipped",
			marker:       marker,
			lines:        []string{"  " + marker + "  "},
			wantLines:    []string{"    "},
			wantSeen:     true,
			wantLine:     0,
			wantReceived: 1,
			wantSkip:     true,
		},
		{
			name:         "skip resets after marker only line",
			marker:       marker,
			lines:        []string{marker, "normal line"},
			wantLines:    []string{"", "normal line"},
			wantSeen:     true,
			wantLine:     0,
			wantReceived: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &StreamState{RequestState: &RequestState{}, cursorMarker: tt.marker}
			got := make([]string, 0, len(tt.lines))
			var skip bool
			for _, line := range tt.lines {
				var transformed string
				transformed, skip = ctx.TransformLine(line)
				got = append(got, transformed)
			}

			assert.Equal(t, tt.wantLines, got, "transformed lines")
			assert.Equal(t, tt.wantReceived, ctx.linesReceived, "lines received")
			line, seen := ctx.CursorMarkerPosition()
			assert.Equal(t, tt.wantSeen, seen, "marker seen")
			assert.Equal(t, tt.wantLine, line, "marker line")
			assert.Equal(t, tt.wantSkip, skip, "skip line")
		})
	}
}
