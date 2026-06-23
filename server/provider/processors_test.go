package provider

import (
	"cursortab/assert"
	"cursortab/client/openai"
	sourcectx "cursortab/ctx"
	"cursortab/types"
	"strings"
	"testing"
)

// --- Diff History Processor Tests ---

func TestDiffEntryToUnifiedDiff(t *testing.T) {
	tests := []struct {
		name     string
		original string
		updated  string
		want     string
	}{
		{
			name:     "no change",
			original: "same",
			updated:  "same",
			want:     "",
		},
		{
			name:     "single line change",
			original: "old",
			updated:  "new",
			want:     "@@ -1,1 +1,1 @@\n-old\n+new",
		},
		{
			name:     "multi line change",
			original: "line 1\nline 2",
			updated:  "line 1\nmodified",
			want:     "@@ -1,2 +1,2 @@\n-line 1\n-line 2\n+line 1\n+modified",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := &types.DiffEntry{Original: tt.original, Updated: tt.updated}
			got := DiffEntryToUnifiedDiff(entry)
			assert.Equal(t, tt.want, got, "DiffEntryToUnifiedDiff result")
		})
	}
}

func TestFormatDiffHistory_Unified(t *testing.T) {
	history := []*types.FileDiffHistory{
		{
			FileName: "test.go",
			DiffHistory: []*types.DiffEntry{
				{Original: "old line", Updated: "new line"},
			},
		},
	}

	result := FormatDiffHistory(history, DiffHistoryOptions{
		HeaderTemplate: "User edited %q:\n",
		Prefix:         "```diff\n",
		Suffix:         "\n```",
		Separator:      "\n\n",
	})
	assert.True(t, strings.Contains(result, "User edited \"test.go\""), "should have file name")
	assert.True(t, strings.Contains(result, "```diff"), "should have diff block")
	assert.True(t, strings.Contains(result, "-old line"), "should have removed line")
	assert.True(t, strings.Contains(result, "+new line"), "should have added line")
}

func TestFormatDiffHistory_NoPrefix(t *testing.T) {
	history := []*types.FileDiffHistory{
		{
			FileName: "test.go",
			DiffHistory: []*types.DiffEntry{
				{Original: "old line", Updated: "new line"},
			},
		},
	}

	result := FormatDiffHistory(history, DiffHistoryOptions{
		HeaderTemplate: "<|file_sep|>%s.diff\n",
		Prefix:         "",
		Suffix:         "\n",
		Separator:      "",
	})
	assert.True(t, strings.Contains(result, "<|file_sep|>test.go.diff"), "should have file separator")
	assert.True(t, strings.Contains(result, "-old line"), "should have removed line")
	assert.True(t, strings.Contains(result, "+new line"), "should have added line")
}

func TestRejectEmpty(t *testing.T) {
	prov := &Provider{Name: "test"}

	tests := []struct {
		name     string
		text     string
		wantDone bool
	}{
		{"empty string", "", true},
		{"only whitespace", "   \n\t  ", true},
		{"has content", "hello", false},
		{"content with whitespace", "  hello  ", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, done := RejectEmptyResult(prov, &openai.StreamResult{Text: tt.text})

			assert.Equal(t, tt.wantDone, done, "RejectEmpty done status")
		})
	}
}

func TestFindAnchorLine(t *testing.T) {
	oldLines := []string{
		"func main() {",
		"    fmt.Println(\"hello\")",
		"    x := 42",
		"    return x",
		"}",
	}

	tests := []struct {
		name        string
		needle      string
		expectedPos int
		wantIdx     int
	}{
		{
			name:        "exact match",
			needle:      "    fmt.Println(\"hello\")",
			expectedPos: 1,
			wantIdx:     1,
		},
		{
			name:        "similar match",
			needle:      "    fmt.Println(\"world\")", // similar to line 1
			expectedPos: 1,
			wantIdx:     1,
		},
		{
			name:        "no match",
			needle:      "completely different line",
			expectedPos: 2,
			wantIdx:     -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findAnchorLine(tt.needle, oldLines, tt.expectedPos)
			assert.Equal(t, tt.wantIdx, got, "findAnchorLine")
		})
	}
}

func TestFindAnchorLineFullSearch(t *testing.T) {
	oldLines := []string{
		"line 0",
		"line 1",
		"unique line here",
		"line 3",
		"line 4",
	}

	tests := []struct {
		name    string
		needle  string
		wantIdx int
	}{
		{
			name:    "find at position 2",
			needle:  "unique line here",
			wantIdx: 2,
		},
		{
			name:    "no match",
			needle:  "not in file",
			wantIdx: -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findAnchorLineFullSearch(tt.needle, oldLines)
			assert.Equal(t, tt.wantIdx, got, "findAnchorLineFullSearch")
		})
	}
}

func TestAnchorTruncation(t *testing.T) {
	prov := &Provider{Name: "test"}

	// Create context with enough lines to trigger validation
	oldLines := make([]string, 20)
	for i := range oldLines {
		oldLines[i] = "original line content"
	}

	tests := []struct {
		name         string
		text         string
		finishReason string
		threshold    float64
		wantDone     bool
		wantEndLine  int
	}{
		{
			name:         "not truncated",
			text:         "line 1\nline 2",
			finishReason: "stop",
			threshold:    0.75,
			wantDone:     false,
		},
		{
			name:         "truncated but enough lines",
			text:         "line 1\nline 2\nline 3\nline 4\nline 5\nline 6\nline 7\nline 8\nline 9\nline 10\nline 11\nline 12\nline 13\nline 14\nline 15\nincomplete",
			finishReason: "length",
			threshold:    0.75,
			wantDone:     false,
			wantEndLine:  15,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &RequestState{
				TrimmedLines: oldLines,
				WindowStart:  0,
				Input: sourcectx.CompletionInput{
					Current: sourcectx.CurrentSnapshot{
						File: sourcectx.FileSnapshot{Lines: oldLines},
					},
				},
			}
			result := &openai.StreamResult{
				Text:         tt.text,
				FinishReason: tt.finishReason,
			}

			endLineInc, _, done := AnchorTruncationResult(prov, state, result, tt.threshold)

			assert.Equal(t, tt.wantDone, done, "AnchorTruncation done status")
			assert.Equal(t, tt.wantEndLine, endLineInc, "AnchorTruncation end line")
		})
	}
}

func TestValidateAnchorPosition(t *testing.T) {
	prov := &Provider{Name: "test"}

	// Create 20 unique lines
	oldLines := make([]string, 20)
	for i := 0; i < len(oldLines); i++ {
		oldLines[i] = "line " + string(rune('A'+i)) // unique content per line
	}

	tests := []struct {
		name           string
		firstLine      string
		maxAnchorRatio float64
		wantDone       bool
	}{
		{
			name:           "first line anchors at start - valid",
			firstLine:      "line A", // matches index 0, which is < 0.25 * 20 = 5
			maxAnchorRatio: 0.25,
			wantDone:       false,
		},
		{
			name:           "first line anchors far - invalid",
			firstLine:      "line O", // matches index 14, which is > 0.25 * 20 = 5
			maxAnchorRatio: 0.25,
			wantDone:       true, // Should reject because anchor is too far
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &RequestState{
				TrimmedLines: oldLines,
				WindowStart:  0,
				Input: sourcectx.CompletionInput{
					Current: sourcectx.CurrentSnapshot{
						File: sourcectx.FileSnapshot{Lines: oldLines},
					},
				},
			}
			result := &openai.StreamResult{
				Text: tt.firstLine + "\nmore content",
			}

			_, done := ValidateAnchorPositionResult(prov, state, result, tt.maxAnchorRatio)

			assert.Equal(t, tt.wantDone, done, "ValidateAnchorPosition done status")
		})
	}
}

func TestFirstLineAnchorValidator(t *testing.T) {
	prov := &Provider{Name: "test"}

	// Create 20 unique lines
	oldLines := make([]string, 20)
	for i := 0; i < len(oldLines); i++ {
		oldLines[i] = "line " + string(rune('A'+i)) // unique content per line
	}

	tests := []struct {
		name           string
		firstLine      string
		maxAnchorRatio float64
		wantErr        bool
	}{
		{
			name:           "anchors at start - valid",
			firstLine:      "line A", // matches index 0, which is < 0.25 * 20 = 5
			maxAnchorRatio: 0.25,
			wantErr:        false,
		},
		{
			name:           "anchors far from start - invalid",
			firstLine:      "line O", // matches index 14, which is > 0.25 * 20 = 5
			maxAnchorRatio: 0.25,
			wantErr:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &StreamState{
				RequestState: &RequestState{
					TrimmedLines: oldLines,
					WindowStart:  0,
					Input: sourcectx.CompletionInput{
						Current: sourcectx.CurrentSnapshot{
							File: sourcectx.FileSnapshot{Lines: oldLines},
						},
					},
				},
			}

			validator := FirstLineAnchorValidator(tt.maxAnchorRatio)
			err := validator(prov, ctx, tt.firstLine)

			gotErr := err != nil
			assert.Equal(t, tt.wantErr, gotErr, "FirstLineAnchorValidator error status")
		})
	}
}

func TestFirstLineAnchorValidator_SmallFile(t *testing.T) {
	prov := &Provider{Name: "test"}

	// Small file (< 10 lines) should skip validation
	oldLines := []string{"line 1", "line 2", "line 3"}

	ctx := &StreamState{
		RequestState: &RequestState{
			TrimmedLines: oldLines,
			WindowStart:  0,
			Input: sourcectx.CompletionInput{
				Current: sourcectx.CurrentSnapshot{
					File: sourcectx.FileSnapshot{Lines: oldLines},
				},
			},
		},
	}

	validator := FirstLineAnchorValidator(0.25)
	err := validator(prov, ctx, "completely different")

	// Should not error for small files
	assert.NoError(t, err, "FirstLineAnchorValidator for small files")
}
