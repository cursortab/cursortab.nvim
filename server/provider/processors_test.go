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
	processor := FormatDiffHistory(DiffHistoryOptions{
		HeaderTemplate: "User edited %q:\n",
		Prefix:         "```diff\n",
		Suffix:         "\n```",
		Separator:      "\n\n",
	})

	history := []*types.FileDiffHistory{
		{
			FileName: "test.go",
			DiffHistory: []*types.DiffEntry{
				{Original: "old line", Updated: "new line"},
			},
		},
	}

	result := processor(history)
	assert.True(t, strings.Contains(result, "User edited \"test.go\""), "should have file name")
	assert.True(t, strings.Contains(result, "```diff"), "should have diff block")
	assert.True(t, strings.Contains(result, "-old line"), "should have removed line")
	assert.True(t, strings.Contains(result, "+new line"), "should have added line")
}

func TestFormatDiffHistory_NoPrefix(t *testing.T) {
	processor := FormatDiffHistory(DiffHistoryOptions{
		HeaderTemplate: "<|file_sep|>%s.diff\n",
		Prefix:         "",
		Suffix:         "\n",
		Separator:      "",
	})

	history := []*types.FileDiffHistory{
		{
			FileName: "test.go",
			DiffHistory: []*types.DiffEntry{
				{Original: "old line", Updated: "new line"},
			},
		},
	}

	result := processor(history)
	assert.True(t, strings.Contains(result, "<|file_sep|>test.go.diff"), "should have file separator")
	assert.True(t, strings.Contains(result, "-old line"), "should have removed line")
	assert.True(t, strings.Contains(result, "+new line"), "should have added line")
}

func TestFormatDiffHistoryOriginalUpdated(t *testing.T) {
	processor := FormatDiffHistoryOriginalUpdated("<|file_sep|>%s.diff\n")

	history := []*types.FileDiffHistory{
		{
			FileName: "test.go",
			DiffHistory: []*types.DiffEntry{
				{Original: "old line", Updated: "new line"},
			},
		},
	}

	result := processor(history)
	assert.True(t, strings.Contains(result, "<|file_sep|>test.go.diff"), "should have file separator")
	assert.True(t, strings.Contains(result, "original:\nold line"), "should have original section")
	assert.True(t, strings.Contains(result, "updated:\nnew line"), "should have updated section")
}

func TestFormatDiffHistoryOriginalUpdated_NoChange(t *testing.T) {
	processor := FormatDiffHistoryOriginalUpdated("<|file_sep|>%s.diff\n")

	history := []*types.FileDiffHistory{
		{
			FileName: "test.go",
			DiffHistory: []*types.DiffEntry{
				{Original: "same content", Updated: "same content"},
			},
		},
	}

	result := processor(history)
	assert.Equal(t, "", result, "should be empty when original equals updated")
}

// --- Preprocessor Tests ---

func TestTrimContent_SmallFile(t *testing.T) {
	prov := &Provider{
		Config: &types.ProviderConfig{
			ProviderMaxTokens: 1000,
		},
	}

	ctx := &Context{
		Input: sourcectx.CompletionInput{
			Current: sourcectx.CurrentSnapshot{
				File:   sourcectx.FileSnapshot{Lines: []string{"line 1", "line 2", "line 3"}},
				Cursor: sourcectx.CursorPosition{Row: 2, Col: 5},
			},
		},
	}

	preprocessor := TrimContent()
	err := preprocessor(prov, ctx)

	assert.NoError(t, err, "TrimContent should not return error")

	// Small file shouldn't be trimmed
	assert.Equal(t, 3, len(ctx.TrimmedLines), "TrimmedLines length")
	assert.Equal(t, 1, ctx.CursorLine, "CursorLine")
}

func TestTrimContent_LargeFile(t *testing.T) {
	prov := &Provider{
		Config: &types.ProviderConfig{
			ProviderMaxTokens: 50, // Small token limit to force trimming
		},
	}

	// Create a large file
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = "this is a long line with some content"
	}

	ctx := &Context{
		Input: sourcectx.CompletionInput{
			Current: sourcectx.CurrentSnapshot{
				File:   sourcectx.FileSnapshot{Lines: lines},
				Cursor: sourcectx.CursorPosition{Row: 50, Col: 0},
			},
		},
	}

	preprocessor := TrimContent()
	err := preprocessor(prov, ctx)

	assert.NoError(t, err, "TrimContent should not return error")

	// Should be trimmed
	assert.True(t, len(ctx.TrimmedLines) < 100, "TrimmedLines should be trimmed")
}

func TestSkipIfTextAfterCursor(t *testing.T) {
	prov := &Provider{Name: "test"}

	tests := []struct {
		name      string
		lines     []string
		cursorRow int
		cursorCol int
		wantSkip  bool
	}{
		{
			name:      "text after cursor",
			lines:     []string{"hello world"},
			cursorRow: 1,
			cursorCol: 5, // cursor at "hello|world"
			wantSkip:  true,
		},
		{
			name:      "cursor at end of line",
			lines:     []string{"hello"},
			cursorRow: 1,
			cursorCol: 5, // cursor at "hello|"
			wantSkip:  false,
		},
		{
			name:      "cursor beyond line length",
			lines:     []string{"hi"},
			cursorRow: 1,
			cursorCol: 10, // cursor beyond line
			wantSkip:  false,
		},
		{
			name:      "empty line",
			lines:     []string{""},
			cursorRow: 1,
			cursorCol: 0,
			wantSkip:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &Context{
				Input: sourcectx.CompletionInput{
					Current: sourcectx.CurrentSnapshot{
						File:   sourcectx.FileSnapshot{Lines: tt.lines},
						Cursor: sourcectx.CursorPosition{Row: tt.cursorRow, Col: tt.cursorCol},
					},
				},
			}

			preprocessor := SkipIfTextAfterCursor()
			err := preprocessor(prov, ctx)

			gotSkip := err == ErrSkipCompletion
			assert.Equal(t, tt.wantSkip, gotSkip, "SkipIfTextAfterCursor skip status")
		})
	}
}

// --- Postprocessor Tests ---

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
			ctx := &Context{
				Result: &openai.StreamResult{Text: tt.text},
			}

			postprocessor := RejectEmpty()
			_, done := postprocessor(prov, ctx)

			assert.Equal(t, tt.wantDone, done, "RejectEmpty done status")
		})
	}
}

func TestRejectTruncated(t *testing.T) {
	prov := &Provider{Name: "test"}

	tests := []struct {
		name         string
		finishReason string
		wantDone     bool
	}{
		{"finish_reason=length", "length", true},
		{"finish_reason=stop", "stop", false},
		{"finish_reason=empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &Context{
				Result: &openai.StreamResult{
					Text:         "some content",
					FinishReason: tt.finishReason,
				},
			}

			postprocessor := RejectTruncated()
			_, done := postprocessor(prov, ctx)

			assert.Equal(t, tt.wantDone, done, "RejectTruncated done status")
		})
	}
}

func TestDropLastLineIfTruncated(t *testing.T) {
	prov := &Provider{Name: "test"}

	tests := []struct {
		name           string
		text           string
		finishReason   string
		stoppedEarly   bool
		wantDone       bool
		wantTextAfter  string
		wantEndLineInc int
	}{
		{
			name:          "not truncated",
			text:          "line 1\nline 2",
			finishReason:  "stop",
			stoppedEarly:  false,
			wantDone:      false,
			wantTextAfter: "line 1\nline 2", // unchanged
		},
		{
			name:           "truncated multi-line",
			text:           "line 1\nline 2\nincomplete",
			finishReason:   "length",
			stoppedEarly:   false,
			wantDone:       false,
			wantTextAfter:  "line 1\nline 2",
			wantEndLineInc: 2, // WindowStart(0) + 2 lines
		},
		{
			name:         "truncated single line - reject",
			text:         "incomplete line",
			finishReason: "length",
			stoppedEarly: false,
			wantDone:     true,
		},
		{
			name:           "stopped early multi-line",
			text:           "line 1\nline 2\nincomplete",
			finishReason:   "",
			stoppedEarly:   true,
			wantDone:       false,
			wantTextAfter:  "line 1\nline 2",
			wantEndLineInc: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &Context{
				WindowStart: 0,
				Result: &openai.StreamResult{
					Text:         tt.text,
					FinishReason: tt.finishReason,
					StoppedEarly: tt.stoppedEarly,
				},
			}

			postprocessor := DropLastLineIfTruncated()
			_, done := postprocessor(prov, ctx)

			assert.Equal(t, tt.wantDone, done, "DropLastLineIfTruncated done status")

			if !done && tt.wantTextAfter != "" {
				assert.Equal(t, tt.wantTextAfter, ctx.Result.Text, "Result.Text")
			}

			if !done && tt.wantEndLineInc > 0 {
				assert.Equal(t, tt.wantEndLineInc, ctx.EndLineInc, "EndLineInc")
			}
		})
	}
}

// --- Helper Function Tests ---

func TestIsNoOpReplacement(t *testing.T) {
	tests := []struct {
		name     string
		newLines []string
		oldLines []string
		want     bool
	}{
		{
			name:     "identical",
			newLines: []string{"line 1", "line 2"},
			oldLines: []string{"line 1", "line 2"},
			want:     true,
		},
		{
			name:     "different content",
			newLines: []string{"line 1", "modified"},
			oldLines: []string{"line 1", "line 2"},
			want:     false,
		},
		{
			name:     "trailing whitespace new",
			newLines: []string{"line 1  "},
			oldLines: []string{"line 1"},
			want:     true, // trimmed before comparison
		},
		{
			name:     "trailing newlines",
			newLines: []string{"line 1", ""},
			oldLines: []string{"line 1"},
			want:     true, // trimmed before comparison
		},
		{
			name:     "different line count",
			newLines: []string{"line 1", "line 2", "line 3"},
			oldLines: []string{"line 1", "line 2"},
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsNoOpReplacement(tt.newLines, tt.oldLines)
			assert.Equal(t, tt.want, got, "IsNoOpReplacement")
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
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &Context{
				WindowStart: 0,
				WindowEnd:   len(oldLines),
				Request: &types.CompletionRequest{
					Lines: oldLines,
				},
				Result: &openai.StreamResult{
					Text:         tt.text,
					FinishReason: tt.finishReason,
				},
			}

			postprocessor := AnchorTruncation(tt.threshold)
			_, done := postprocessor(prov, ctx)

			assert.Equal(t, tt.wantDone, done, "AnchorTruncation done status")
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
			ctx := &Context{
				WindowStart: 0,
				WindowEnd:   len(oldLines),
				Request: &types.CompletionRequest{
					Lines: oldLines,
				},
				Result: &openai.StreamResult{
					Text: tt.firstLine + "\nmore content",
				},
			}

			postprocessor := ValidateAnchorPosition(tt.maxAnchorRatio)
			_, done := postprocessor(prov, ctx)

			assert.Equal(t, tt.wantDone, done, "ValidateAnchorPosition done status")
		})
	}
}

func TestValidateFirstLineAnchor(t *testing.T) {
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
			ctx := &Context{
				WindowStart: 0,
				WindowEnd:   len(oldLines),
				Request: &types.CompletionRequest{
					Lines: oldLines,
				},
			}

			validator := ValidateFirstLineAnchor(tt.maxAnchorRatio)
			err := validator(prov, ctx, tt.firstLine)

			gotErr := err != nil
			assert.Equal(t, tt.wantErr, gotErr, "ValidateFirstLineAnchor error status")
		})
	}
}

func TestValidateFirstLineAnchor_SmallFile(t *testing.T) {
	prov := &Provider{Name: "test"}

	// Small file (< 10 lines) should skip validation
	oldLines := []string{"line 1", "line 2", "line 3"}

	ctx := &Context{
		WindowStart: 0,
		WindowEnd:   len(oldLines),
		Request: &types.CompletionRequest{
			Lines: oldLines,
		},
	}

	validator := ValidateFirstLineAnchor(0.25)
	err := validator(prov, ctx, "completely different")

	// Should not error for small files
	assert.NoError(t, err, "ValidateFirstLineAnchor for small files")
}
