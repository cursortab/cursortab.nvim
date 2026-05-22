package text

import (
	"cursortab/assert"
	"fmt"
	"testing"
)

func TestStageDistanceFromCursor(t *testing.T) {
	// Stage with buffer range 10-15
	stage := &Stage{
		BufferStart: 10,
		BufferEnd:   15,
	}

	tests := []struct {
		cursorRow int // buffer coordinates
		expected  int
	}{
		{5, 5},  // cursor before stage (buffer line 5, stage starts at buffer 10)
		{10, 0}, // cursor at start (buffer line 10)
		{12, 0}, // cursor inside (buffer line 12)
		{15, 0}, // cursor at end (buffer line 15)
		{20, 5}, // cursor after stage (buffer line 20, stage ends at buffer 15)
	}

	for _, tt := range tests {
		result := distanceFromCursor(stage.BufferStart, stage.BufferEnd, tt.cursorRow)
		assert.Equal(t, tt.expected, result, fmt.Sprintf("distance for cursor at %d", tt.cursorRow))
	}
}

func TestStageDistanceFromCursor_NoOffset(t *testing.T) {
	// Stage with buffer range matching coordinates
	stage := &Stage{
		BufferStart: 10,
		BufferEnd:   15,
	}

	tests := []struct {
		cursorRow int
		expected  int
	}{
		{5, 5},  // cursor before stage
		{10, 0}, // cursor at start
		{12, 0}, // cursor inside
		{15, 0}, // cursor at end
		{20, 5}, // cursor after stage
	}

	for _, tt := range tests {
		result := distanceFromCursor(stage.BufferStart, stage.BufferEnd, tt.cursorRow)
		assert.Equal(t, tt.expected, result, fmt.Sprintf("distance for cursor at %d", tt.cursorRow))
	}
}

// TestCreateStages_ConsecutiveDeletionsBlankLines verifies that two consecutive
// blank line deletions result in a single group with both lines, not one dropped.
func TestCreateStages_ConsecutiveDeletionsBlankLines(t *testing.T) {
	oldLines := []string{
		"def foo():",
		"    pass",
		" ",
		"",
		"def bar():",
		"    pass",
	}
	newLines := []string{
		"def foo():",
		"    pass",
		"def bar():",
		"    pass",
	}

	text1 := JoinLines(oldLines)
	text2 := JoinLines(newLines)
	diff := ComputeDiff(text1, text2)

	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          3,
		CursorCol:          0,
		ViewportTop:        1,
		ViewportBottom:     50,
		BaseLineOffset:     1,
		ProximityThreshold: 5,
		MaxLines:           0,
		FilePath:           "test.py",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	assert.NotNil(t, result, "result not nil")
	assert.Equal(t, 1, len(result.Stages), "1 stage")

	stage := result.Stages[0]
	assert.Equal(t, 2, len(stage.Changes), "stage has 2 deletion changes")
	assert.Equal(t, 1, len(stage.Groups), "both consecutive deletions form 1 group")

	group := stage.Groups[0]
	assert.Equal(t, "deletion", group.Type, "group type is deletion")
	assert.Equal(t, 2, len(group.Lines), "group spans both deleted lines")
}

// TestCreateStages_PureDeletionLastStage_CursorTargetLandsAtSurvivingLine
// verifies the cursor target for a pure-deletion last stage points to the
// buffer line that surfaces after the deletion (bufferStart), not bufferStart-1.
// At bufferStart=1 the buggy formula yields LineNumber=0, which downstream
// code treats as invalid and silently drops the retrigger target.
func TestCreateStages_PureDeletionLastStage_CursorTargetLandsAtSurvivingLine(t *testing.T) {
	tests := []struct {
		name       string
		oldLines   []string
		newLines   []string
		wantTarget int32
	}{
		{
			name:       "deletion at top of buffer",
			oldLines:   []string{"delete1", "delete2", "keep1", "keep2"},
			newLines:   []string{"keep1", "keep2"},
			wantTarget: 1,
		},
		{
			name:       "deletion in middle of buffer",
			oldLines:   []string{"keep1", "keep2", "delete1", "delete2", "keep3"},
			newLines:   []string{"keep1", "keep2", "keep3"},
			wantTarget: 3,
		},
		{
			// bufferStart=2 would land past EOF after the deletion. Must clamp
			// to the last surviving line in the completion's view (line 1).
			name:       "deletion at end of completion view",
			oldLines:   []string{"keep1", "delete1", "delete2"},
			newLines:   []string{"keep1"},
			wantTarget: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diff := ComputeDiff(JoinLines(tt.oldLines), JoinLines(tt.newLines))
			result := CreateStages(&StagingParams{
				Diff:               diff,
				CursorRow:          1,
				CursorCol:          0,
				ViewportTop:        1,
				ViewportBottom:     50,
				BaseLineOffset:     1,
				ProximityThreshold: 5,
				FilePath:           "test.go",
				NewLines:           tt.newLines,
				OldLines:           tt.oldLines,
			})

			assert.NotNil(t, result, "result")
			assert.Equal(t, 1, len(result.Stages), "single pure-deletion stage")

			stage := result.Stages[0]
			assert.Equal(t, 0, len(stage.Lines), "pure deletion has no new content")
			assert.True(t, stage.IsLastStage, "single stage is last stage")

			assert.NotNil(t, stage.CursorTarget, "last stage has cursor target")
			assert.Equal(t, tt.wantTarget, stage.CursorTarget.LineNumber, "cursor target line")
		})
	}
}

func TestJoinLines(t *testing.T) {
	lines := []string{"line1", "line2", "line3"}
	result := JoinLines(lines)
	// JoinLines adds \n after each line for diffmatchpatch compatibility
	expected := "line1\nline2\nline3\n"

	assert.Equal(t, expected, result, "JoinLines result")
}

func TestCreateStages_PureAdditionsPreservesEmptyLines(t *testing.T) {
	oldLines := []string{"line 1", ""}
	newLines := []string{"line 1", "", "block A:", "    content", "", "block B:", "    content"}

	text1 := JoinLines(oldLines)
	text2 := JoinLines(newLines)
	diff := ComputeDiff(text1, text2)

	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          1,
		CursorCol:          0,
		ViewportTop:        1,
		ViewportBottom:     50,
		BaseLineOffset:     1,
		ProximityThreshold: 3,
		MaxLines:           0,
		FilePath:           "test.go",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	assert.NotNil(t, result, "result")
	assert.True(t, len(result.Stages) >= 1, "should have at least 1 stage")

	stage := result.Stages[0]

	assert.Equal(t, 5, len(stage.Changes), "stage should have 5 changes")

	line3Change, exists := stage.Changes[3]
	assert.True(t, exists, "should have change at stage line 3")
	if exists {
		assert.Equal(t, ChangeAddition, line3Change.Type, "stage line 3 should be addition")
		assert.Equal(t, "", line3Change.Content, "stage line 3 should be empty string")
	}

	totalLinesInGroups := 0
	for _, g := range stage.Groups {
		totalLinesInGroups += g.EndLine - g.StartLine + 1
	}
	assert.Equal(t, 5, totalLinesInGroups, "groups should cover all 5 lines")
}

func TestCreateStages_MultipleAdditionsWithEmptyLineSeparators(t *testing.T) {
	oldLines := []string{"header line", ""}
	newLines := []string{
		"header line",
		"",
		"block 1:",
		"    line a",
		"",
		"block 2:",
		"    line b",
		"",
		"block 3:",
		"    line c",
	}

	text1 := JoinLines(oldLines)
	text2 := JoinLines(newLines)

	diff := ComputeDiff(text1, text2)

	assert.Equal(t, 8, len(diff.Changes), "should have 8 additions")

	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          1,
		CursorCol:          0,
		ViewportTop:        1,
		ViewportBottom:     50,
		BaseLineOffset:     1,
		ProximityThreshold: 3,
		MaxLines:           0,
		FilePath:           "test.go",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	assert.NotNil(t, result, "result")
	assert.True(t, len(result.Stages) >= 1, "should have at least 1 stage")

	stage := result.Stages[0]

	assert.Equal(t, 8, len(stage.Changes), "stage should have 8 changes")

	assert.Equal(t, 8, len(stage.Lines), "stage should have 8 lines")

	line3Change, exists := stage.Changes[3]
	assert.True(t, exists, "should have change at stage line 3")
	if exists {
		assert.Equal(t, "", line3Change.Content, "stage line 3 should be empty")
	}

	line6Change, exists := stage.Changes[6]
	assert.True(t, exists, "should have change at stage line 6")
	if exists {
		assert.Equal(t, "", line6Change.Content, "stage line 6 should be empty")
	}

	totalLinesInGroups := 0
	for _, g := range stage.Groups {
		totalLinesInGroups += g.EndLine - g.StartLine + 1
	}
	assert.Equal(t, 8, totalLinesInGroups, "groups should cover all 8 lines")
}

func TestCreateStages_EmptyDiff(t *testing.T) {
	diff := &DiffResult{Changes: nil}
	stages := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          10,
		CursorCol:          0,
		ViewportTop:        1,
		ViewportBottom:     50,
		BaseLineOffset:     1,
		ProximityThreshold: 3,
		MaxLines:           0,
		FilePath:           "test.go",
		NewLines:           []string{},
		OldLines:           []string{},
	})

	assert.Nil(t, stages, "stages for empty diff")
}

func TestCreateStages_SingleCluster(t *testing.T) {
	// All changes within proximity threshold - should still return stages (always return stages now)
	diff := &DiffResult{
		Changes: []LineChange{
			{Type: ChangeModification, OldLineNum: 10, NewLineNum: 10, Content: "new10", OldContent: "old10"},
			{Type: ChangeModification, OldLineNum: 11, NewLineNum: 11, Content: "new11", OldContent: "old11"},
			{Type: ChangeModification, OldLineNum: 12, NewLineNum: 12, Content: "new12", OldContent: "old12"},
		},
	}
	newLines := make([]string, 20)
	oldLines := make([]string, 20)
	for i := range newLines {
		newLines[i] = "line"
		oldLines[i] = "line"
	}

	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          10,
		CursorCol:          0,
		ViewportTop:        1,
		ViewportBottom:     50,
		BaseLineOffset:     1,
		ProximityThreshold: 3,
		MaxLines:           0,
		FilePath:           "test.go",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	// Single cluster still returns a result with 1 stage
	assert.NotNil(t, result, "result for single cluster")
	assert.Len(t, 1, result.Stages, "stages")
}

func TestCreateStages_TwoClusters(t *testing.T) {
	// Changes at lines 10-11 and 25-26 (gap > threshold of 3)
	diff := &DiffResult{
		Changes: []LineChange{
			{Type: ChangeModification, OldLineNum: 10, NewLineNum: 10, Content: "new10", OldContent: "old10"},
			{Type: ChangeModification, OldLineNum: 11, NewLineNum: 11, Content: "new11", OldContent: "old11"},
			{Type: ChangeModification, OldLineNum: 25, NewLineNum: 25, Content: "new25", OldContent: "old25"},
			{Type: ChangeModification, OldLineNum: 26, NewLineNum: 26, Content: "new26", OldContent: "old26"},
		},
	}
	newLines := make([]string, 30)
	oldLines := make([]string, 30)
	for i := range newLines {
		newLines[i] = "content"
		oldLines[i] = "content"
	}

	// Cursor at line 15, baseLineOffset=1, so cluster 10-11 is closer
	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          15,
		CursorCol:          0,
		ViewportTop:        1,
		ViewportBottom:     50,
		BaseLineOffset:     1,
		ProximityThreshold: 3,
		MaxLines:           0,
		FilePath:           "test.go",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	assert.NotNil(t, result, "result")
	assert.Len(t, 2, result.Stages, "stages")

	// First stage should be the closer cluster (10-11)
	assert.Equal(t, 10, result.Stages[0].BufferStart, "first stage buffer start")
	assert.Equal(t, 11, result.Stages[0].BufferEnd, "first stage buffer end")

	// Second stage should be 25-26
	assert.Equal(t, 25, result.Stages[1].BufferStart, "second stage buffer start")

	// First stage cursor target should point to next stage
	assert.Equal(t, int32(25), result.Stages[0].CursorTarget.LineNumber, "first stage cursor target line")
	assert.False(t, result.Stages[0].CursorTarget.ShouldRetrigger, "first stage ShouldRetrigger")

	// Last stage should have ShouldRetrigger=true
	assert.True(t, result.Stages[1].CursorTarget.ShouldRetrigger, "last stage ShouldRetrigger")
	assert.True(t, result.Stages[1].IsLastStage, "second stage IsLastStage")
}

func TestCreateStages_CursorDistanceSorting(t *testing.T) {
	// Three clusters: 5-6, 20-21, 35-36
	// Cursor at 22 - closest to cluster 20-21
	diff := &DiffResult{
		Changes: []LineChange{
			{Type: ChangeModification, OldLineNum: 5, NewLineNum: 5, Content: "new5", OldContent: "old5"},
			{Type: ChangeModification, OldLineNum: 6, NewLineNum: 6, Content: "new6", OldContent: "old6"},
			{Type: ChangeModification, OldLineNum: 20, NewLineNum: 20, Content: "new20", OldContent: "old20"},
			{Type: ChangeModification, OldLineNum: 21, NewLineNum: 21, Content: "new21", OldContent: "old21"},
			{Type: ChangeModification, OldLineNum: 35, NewLineNum: 35, Content: "new35", OldContent: "old35"},
			{Type: ChangeModification, OldLineNum: 36, NewLineNum: 36, Content: "new36", OldContent: "old36"},
		},
	}
	newLines := make([]string, 40)
	oldLines := make([]string, 40)
	for i := range newLines {
		newLines[i] = "content"
		oldLines[i] = "content"
	}

	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          22,
		CursorCol:          0,
		ViewportTop:        1,
		ViewportBottom:     50,
		BaseLineOffset:     1,
		ProximityThreshold: 3,
		MaxLines:           0,
		FilePath:           "test.go",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	assert.NotNil(t, result, "result")
	assert.Len(t, 3, result.Stages, "stages")

	// First stage should be closest to cursor (20-21)
	assert.Equal(t, 20, result.Stages[0].BufferStart, "first stage should be closest cluster (20-21)")
}

func TestCreateStages_ViewportPartitioning(t *testing.T) {
	// Changes at lines 10 (in viewport) and 100 (out of viewport)
	// Viewport is 1-50
	diff := &DiffResult{
		Changes: []LineChange{
			{Type: ChangeModification, OldLineNum: 10, NewLineNum: 10, Content: "new10", OldContent: "old10"},
			{Type: ChangeModification, OldLineNum: 100, NewLineNum: 100, Content: "new100", OldContent: "old100"},
		},
	}
	newLines := make([]string, 110)
	oldLines := make([]string, 110)
	for i := range newLines {
		newLines[i] = "content"
		oldLines[i] = "content"
	}

	// Cursor at 10, viewport 1-50
	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          10,
		CursorCol:          0,
		ViewportTop:        1,
		ViewportBottom:     50,
		BaseLineOffset:     1,
		ProximityThreshold: 3,
		MaxLines:           0,
		FilePath:           "test.go",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	assert.NotNil(t, result, "result")
	assert.Len(t, 2, result.Stages, "stages")

	// In-view changes should come first (line 10)
	assert.Equal(t, 10, result.Stages[0].BufferStart, "first stage should be in-viewport change")

	// Out-of-view change should be second (line 100)
	assert.Equal(t, 100, result.Stages[1].BufferStart, "second stage should be out-of-viewport change")
}

func TestCreateStages_ProximityGrouping(t *testing.T) {
	// Changes at lines 10, 12, 14 (all within threshold of 3)
	// Should form single cluster
	diff := &DiffResult{
		Changes: []LineChange{
			{Type: ChangeModification, OldLineNum: 10, NewLineNum: 10, Content: "new10", OldContent: "old10"},
			{Type: ChangeModification, OldLineNum: 12, NewLineNum: 12, Content: "new12", OldContent: "old12"},
			{Type: ChangeModification, OldLineNum: 14, NewLineNum: 14, Content: "new14", OldContent: "old14"},
		},
	}
	newLines := make([]string, 20)
	oldLines := make([]string, 20)
	for i := range newLines {
		newLines[i] = "content"
		oldLines[i] = "content"
	}

	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          10,
		CursorCol:          0,
		ViewportTop:        1,
		ViewportBottom:     50,
		BaseLineOffset:     1,
		ProximityThreshold: 3,
		MaxLines:           0,
		FilePath:           "test.go",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	// Gap between 10->12 is 2, 12->14 is 2 - all within threshold
	// Now returns 1 stage (always returns stages)
	assert.NotNil(t, result, "result for single cluster")
	assert.Len(t, 1, result.Stages, "stages for single cluster (all within threshold)")
}

func TestCreateStages_ProximityGrouping_SplitByGap(t *testing.T) {
	// Changes at lines 10, 12 (gap=2) and 20 (gap=8 from 12)
	// With threshold=3, should split into two clusters
	diff := &DiffResult{
		Changes: []LineChange{
			{Type: ChangeModification, OldLineNum: 10, NewLineNum: 10, Content: "new10", OldContent: "old10"},
			{Type: ChangeModification, OldLineNum: 12, NewLineNum: 12, Content: "new12", OldContent: "old12"},
			{Type: ChangeModification, OldLineNum: 20, NewLineNum: 20, Content: "new20", OldContent: "old20"},
		},
	}
	newLines := make([]string, 25)
	oldLines := make([]string, 25)
	for i := range newLines {
		newLines[i] = "content"
		oldLines[i] = "content"
	}

	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          10,
		CursorCol:          0,
		ViewportTop:        1,
		ViewportBottom:     50,
		BaseLineOffset:     1,
		ProximityThreshold: 3,
		MaxLines:           0,
		FilePath:           "test.go",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	assert.NotNil(t, result, "result")
	assert.Len(t, 2, result.Stages, "stages (gap > threshold)")

	// First cluster should be 10-12
	assert.Equal(t, 10, result.Stages[0].BufferStart, "first stage start line")
	assert.Equal(t, 12, result.Stages[0].BufferEnd, "first stage end line")

	// Second cluster should be 20
	assert.Equal(t, 20, result.Stages[1].BufferStart, "second stage start line")
}

func TestCreateStages_WithBaseLineOffset(t *testing.T) {
	// Diff coordinates are relative (1-indexed from start of extraction)
	// baseLineOffset=50 means diff line 1 = buffer line 50
	diff := &DiffResult{
		Changes: []LineChange{
			{Type: ChangeModification, OldLineNum: 1, NewLineNum: 1, Content: "new1", OldContent: "old1"},
			{Type: ChangeModification, OldLineNum: 10, NewLineNum: 10, Content: "new10", OldContent: "old10"},
		},
	}
	newLines := make([]string, 15)
	oldLines := make([]string, 15)
	for i := range newLines {
		newLines[i] = "content"
		oldLines[i] = "content"
	}

	// baseLineOffset=50, so diff line 1 = buffer line 50, diff line 10 = buffer line 59
	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          55,
		CursorCol:          0,
		ViewportTop:        1,
		ViewportBottom:     100,
		BaseLineOffset:     50,
		ProximityThreshold: 3,
		MaxLines:           0,
		FilePath:           "test.go",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	assert.NotNil(t, result, "result")
	assert.Len(t, 2, result.Stages, "stages")

	// Verify buffer coordinates in stage
	assert.True(t, result.Stages[0].BufferStart == 50 || result.Stages[0].BufferStart == 59,
		fmt.Sprintf("stage should have buffer coordinates (50 or 59), got %d", result.Stages[0].BufferStart))
}

func TestCreateStages_GroupsComputed(t *testing.T) {
	// Verify that groups are computed for stages
	diff := &DiffResult{
		Changes: []LineChange{
			{Type: ChangeModification, OldLineNum: 1, NewLineNum: 1, Content: "new1", OldContent: "old1"},
			{Type: ChangeModification, OldLineNum: 2, NewLineNum: 2, Content: "new2", OldContent: "old2"},
			// Gap
			{Type: ChangeModification, OldLineNum: 10, NewLineNum: 10, Content: "new10", OldContent: "old10"},
		},
	}
	newLines := []string{"new1", "new2", "", "", "", "", "", "", "", "new10"}
	oldLines := []string{"old1", "old2", "", "", "", "", "", "", "", "old10"}

	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          1,
		CursorCol:          0,
		ViewportTop:        1,
		ViewportBottom:     50,
		BaseLineOffset:     1,
		ProximityThreshold: 3,
		MaxLines:           0,
		FilePath:           "test.go",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	assert.NotNil(t, result, "result")
	assert.Len(t, 2, result.Stages, "stages")

	// First stage (lines 1-2) should have groups
	assert.NotNil(t, result.Stages[0].Groups, "first stage groups")
}

func TestGroupChangesIntoStages(t *testing.T) {
	diff := &DiffResult{
		Changes: []LineChange{
			{Type: ChangeModification, OldLineNum: 5, NewLineNum: 5},
			{Type: ChangeModification, OldLineNum: 6, NewLineNum: 6},
			{Type: ChangeModification, OldLineNum: 7, NewLineNum: 7},
			{Type: ChangeModification, OldLineNum: 20, NewLineNum: 20},
			{Type: ChangeModification, OldLineNum: 21, NewLineNum: 21},
		},
	}

	indexed := []indexedChange{
		{diff.Changes[0], 5},
		{diff.Changes[1], 6},
		{diff.Changes[2], 7},
		{diff.Changes[3], 20},
		{diff.Changes[4], 21},
	}
	stages := groupChangesIntoStages(indexed, 3, 0, 0, 1, diff)

	assert.Len(t, 2, stages, "stages")

	// First stage: 5-7
	assert.Equal(t, 5, stages[0].startLine, "first stage start line")
	assert.Equal(t, 7, stages[0].endLine, "first stage end line")
	assert.Equal(t, 3, len(stages[0].rawChanges), "first stage change count")

	// Second stage: 20-21
	assert.Equal(t, 20, stages[1].startLine, "second stage start line")
	assert.Equal(t, 21, stages[1].endLine, "second stage end line")
}

func TestGroupChangesIntoStages_EmptyInput(t *testing.T) {
	stages := groupChangesIntoStages(nil, 3, 0, 0, 1, &DiffResult{})

	assert.Nil(t, stages, "stages for empty input")
}

func TestCreateStages_WithInsertions(t *testing.T) {
	// Test staging when completion adds lines (net line increase)
	// Old: 3 lines, New: 5 lines (2 insertions at different locations)
	diff := &DiffResult{
		Changes: []LineChange{
			{Type: ChangeAddition, NewLineNum: 2, OldLineNum: -1, Content: "inserted1"},
			{Type: ChangeAddition, NewLineNum: 5, OldLineNum: -1, Content: "inserted2"},
		},
		OldLineCount: 3,
		NewLineCount: 5,
		LineMapping: &LineMapping{
			NewToOld: []int{1, -1, 2, 3, -1}, // line 2 and 5 are insertions
			OldToNew: []int{1, 3, 4},         // old lines map to new positions
		},
	}
	newLines := []string{"line1", "inserted1", "line2", "line3", "inserted2"}
	oldLines := []string{"line1", "line2", "line3"}

	// Buffer-line gap between the two insertions is 2 (bufferLine 2 and 4).
	// With threshold 1 they should be separate stages.
	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          1,
		CursorCol:          0,
		ViewportTop:        1,
		ViewportBottom:     50,
		BaseLineOffset:     1,
		ProximityThreshold: 1,
		MaxLines:           0,
		FilePath:           "test.go",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	assert.NotNil(t, result, "result")
	assert.Len(t, 2, result.Stages, "stages for separated insertions")

	// Verify stages have valid content
	for i, stage := range result.Stages {
		assert.True(t, len(stage.Lines) > 0, fmt.Sprintf("stage %d has lines", i))
	}
}

func TestCreateStages_WithDeletions(t *testing.T) {
	// Test staging when completion removes lines (net line decrease)
	diff := &DiffResult{
		Changes: []LineChange{
			{Type: ChangeDeletion, OldLineNum: 2, NewLineNum: -1, Content: "deleted1"},
			{Type: ChangeDeletion, OldLineNum: 10, NewLineNum: -1, Content: "deleted2"},
		},
		OldLineCount: 12,
		NewLineCount: 10,
		LineMapping: &LineMapping{
			NewToOld: []int{1, 3, 4, 5, 6, 7, 8, 9, 11, 12},
			OldToNew: []int{1, -1, 2, 3, 4, 5, 6, 7, 8, -1, 9, 10},
		},
	}
	newLines := make([]string, 10)
	oldLines := make([]string, 12)
	for i := range newLines {
		newLines[i] = "content"
	}
	for i := range oldLines {
		oldLines[i] = "content"
	}

	// Gap is large (10-2=8), should create 2 stages
	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          1,
		CursorCol:          0,
		ViewportTop:        1,
		ViewportBottom:     50,
		BaseLineOffset:     1,
		ProximityThreshold: 3,
		MaxLines:           0,
		FilePath:           "test.go",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	assert.NotNil(t, result, "result")
	assert.Len(t, 2, result.Stages, "stages for separated deletions")
}

func TestCreateStages_MixedInsertionDeletion(t *testing.T) {
	// Test with both insertions and deletions in different regions
	diff := &DiffResult{
		Changes: []LineChange{
			{Type: ChangeAddition, NewLineNum: 2, OldLineNum: 1, Content: "inserted"},
			{Type: ChangeDeletion, OldLineNum: 15, NewLineNum: 14, Content: "deleted"},
		},
		OldLineCount: 20,
		NewLineCount: 20, // net zero change
		LineMapping: &LineMapping{
			NewToOld: make([]int, 20),
			OldToNew: make([]int, 20),
		},
	}
	// Initialize mappings
	for i := range diff.LineMapping.NewToOld {
		diff.LineMapping.NewToOld[i] = i + 1
	}
	for i := range diff.LineMapping.OldToNew {
		diff.LineMapping.OldToNew[i] = i + 1
	}
	diff.LineMapping.NewToOld[1] = -1 // line 2 is insertion

	newLines := make([]string, 20)
	oldLines := make([]string, 20)
	for i := range newLines {
		newLines[i] = "content"
		oldLines[i] = "content"
	}

	// Large gap (15-2=13), should create 2 stages
	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          1,
		CursorCol:          0,
		ViewportTop:        1,
		ViewportBottom:     50,
		BaseLineOffset:     1,
		ProximityThreshold: 3,
		MaxLines:           0,
		FilePath:           "test.go",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	assert.NotNil(t, result, "result")
	assert.Len(t, 2, result.Stages, "stages")
}

func TestGetBufferLine_Insertion(t *testing.T) {
	// Test that insertions correctly calculate buffer position
	mapping := &LineMapping{
		NewToOld: []int{1, -1, -1, 2}, // lines 2,3 are insertions after line 1
		OldToNew: []int{1, 4},
	}

	// Pure insertion at new line 2, should anchor to old line 1
	change := LineChange{
		Type:       ChangeAddition,
		NewLineNum: 2,
		OldLineNum: -1, // pure insertion
	}

	bufferLine := mapping.GetBufferLine(change, 1)

	// Forward walk finds old line 2 at NewToOld[3], returns 2 (insert before old line 2)
	assert.Equal(t, 2, bufferLine, "buffer line for insertion (before next mapped old line)")
}

func TestGetBufferLine_Modification(t *testing.T) {
	// Test that modifications use OldLineNum directly
	mapping := &LineMapping{
		NewToOld: []int{1, 2, 3},
		OldToNew: []int{1, 2, 3},
	}

	change := LineChange{
		Type:       ChangeModification,
		NewLineNum: 2,
		OldLineNum: 2,
	}

	bufferLine := mapping.GetBufferLine(change, 10)

	// baseLineOffset=10, oldLineNum=2: 2 + 10 - 1 = 11
	assert.Equal(t, 11, bufferLine, "buffer line for modification")
}

func TestGetStageBufferRange_WithInsertions(t *testing.T) {
	// Stage containing insertions should still compute valid buffer range
	diff := &DiffResult{
		Changes: []LineChange{
			{Type: ChangeAddition, NewLineNum: 2, OldLineNum: -1},
			{Type: ChangeAddition, NewLineNum: 3, OldLineNum: -1},
		},
		LineMapping: &LineMapping{
			NewToOld: []int{1, -1, -1, 2}, // insertions at lines 2,3
			OldToNew: []int{1, 4},
		},
	}

	stage := &stageBuilder{
		startLine:  2,
		endLine:    3,
		rawChanges: diff.Changes,
	}

	computeStageRanges(stage, 1, diff)

	// Both insertions anchor to line 1, so range should be 1-1
	assert.True(t, stage.bufferStart <= stage.bufferEnd, fmt.Sprintf("invalid range: start=%d > end=%d", stage.bufferStart, stage.bufferEnd))
}

func TestGetBufferLine_DeletionAtLine1(t *testing.T) {
	// Edge case: deletion at line 1 with no preceding anchor
	mapping := &LineMapping{
		NewToOld: []int{2, 3}, // old line 1 deleted
		OldToNew: []int{-1, 1, 2},
	}

	change := LineChange{
		Type:       ChangeDeletion,
		OldLineNum: 1,
		NewLineNum: -1, // no new line for deletions
	}

	bufferLine := mapping.GetBufferLine(change, 1)

	// Should use OldLineNum directly: 1 + 1 - 1 = 1
	assert.Equal(t, 1, bufferLine, "buffer line for deletion at line 1")
}

func TestGetBufferLine_InsertionWithNoAnchor(t *testing.T) {
	// Edge case: insertion at line 1 with no preceding mapped line
	mapping := &LineMapping{
		NewToOld: []int{-1, 1}, // new line 1 is insertion, new line 2 maps to old line 1
		OldToNew: []int{2},
	}

	change := LineChange{
		Type:       ChangeAddition,
		NewLineNum: 1,
		OldLineNum: -1, // pure insertion
	}

	bufferLine := mapping.GetBufferLine(change, 1)

	// No anchor found, should fallback to mapKey: 1 + 1 - 1 = 1
	assert.Equal(t, 1, bufferLine, "buffer line for insertion at line 1 with no anchor")
}

func TestCreateStages_CumulativeOffsetScenario(t *testing.T) {
	// Scenario: first stage increases line count, affecting second stage coordinates
	diff := &DiffResult{
		Changes: []LineChange{
			{Type: ChangeAddition, NewLineNum: 2, OldLineNum: 1, Content: "insert1"},
			{Type: ChangeAddition, NewLineNum: 3, OldLineNum: 1, Content: "insert2"},
			{Type: ChangeModification, NewLineNum: 22, OldLineNum: 20, Content: "modified"},
		},
		OldLineCount: 25,
		NewLineCount: 27, // +2 from insertions
		LineMapping: &LineMapping{
			NewToOld: make([]int, 27),
			OldToNew: make([]int, 25),
		},
	}
	// Initialize mappings
	for i := range diff.LineMapping.OldToNew {
		diff.LineMapping.OldToNew[i] = i + 3 // offset by 2 after line 1
	}
	diff.LineMapping.OldToNew[0] = 1
	for i := range diff.LineMapping.NewToOld {
		switch i {
		case 0:
			diff.LineMapping.NewToOld[i] = 1
		case 1, 2:
			diff.LineMapping.NewToOld[i] = -1 // insertions
		default:
			diff.LineMapping.NewToOld[i] = i - 1
		}
	}

	newLines := make([]string, 27)
	oldLines := make([]string, 25)
	for i := range newLines {
		newLines[i] = fmt.Sprintf("line%d", i+1)
	}
	for i := range oldLines {
		oldLines[i] = fmt.Sprintf("line%d", i+1)
	}

	// Large gap (20-3=17), should create 2 stages
	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          1,
		CursorCol:          0,
		ViewportTop:        1,
		ViewportBottom:     50,
		BaseLineOffset:     1,
		ProximityThreshold: 3,
		MaxLines:           0,
		FilePath:           "test.go",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	assert.NotNil(t, result, "result")
	assert.Len(t, 2, result.Stages, "stages for insertions + distant modification")

	// Verify both stages have valid content
	for i, stage := range result.Stages {
		assert.True(t, stage.BufferStart > 0, fmt.Sprintf("stage %d has valid BufferStart", i))
	}
}

func TestCreateStages_AllDeletions(t *testing.T) {
	// Edge case: completion that only contains deletions
	diff := &DiffResult{
		Changes: []LineChange{
			{Type: ChangeDeletion, OldLineNum: 2, NewLineNum: -1, Content: "deleted1"},
			{Type: ChangeDeletion, OldLineNum: 3, NewLineNum: -1, Content: "deleted2"},
		},
		OldLineCount: 5,
		NewLineCount: 3,
		LineMapping: &LineMapping{
			NewToOld: []int{1, 4, 5},
			OldToNew: []int{1, -1, -1, 2, 3},
		},
	}
	newLines := []string{"line1", "line4", "line5"}
	oldLines := []string{"line1", "deleted1", "deleted2", "line4", "line5"}

	// Deletions are at same location, should form single cluster
	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          1,
		CursorCol:          0,
		ViewportTop:        1,
		ViewportBottom:     50,
		BaseLineOffset:     1,
		ProximityThreshold: 3,
		MaxLines:           0,
		FilePath:           "test.go",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	// Single cluster = 1 stage
	assert.NotNil(t, result, "result for single cluster of deletions")
	assert.Len(t, 1, result.Stages, "single cluster of deletions")
}

func TestGetStageBufferRange_AllInsertions(t *testing.T) {
	// Stage containing only insertions (all OldLineNum = -1 but have mapping)
	diff := &DiffResult{
		Changes: []LineChange{
			{Type: ChangeAddition, NewLineNum: 2, OldLineNum: -1},
			{Type: ChangeAddition, NewLineNum: 3, OldLineNum: -1},
			{Type: ChangeAddition, NewLineNum: 4, OldLineNum: -1},
		},
		LineMapping: &LineMapping{
			NewToOld: []int{1, -1, -1, -1, 2}, // lines 2,3,4 are insertions
			OldToNew: []int{1, 5},
		},
	}

	stage := &stageBuilder{
		startLine:  2,
		endLine:    4,
		rawChanges: diff.Changes,
	}

	computeStageRanges(stage, 1, diff)

	// Pure additions with valid anchors (from mapping) use insertion point (anchor + 1).
	// The mapping shows these insertions are anchored to old line 1, so insertion point is 2.
	assert.True(t, stage.bufferStart <= stage.bufferEnd, fmt.Sprintf("valid range: start=%d end=%d", stage.bufferStart, stage.bufferEnd))
	assert.Equal(t, 2, stage.bufferStart, "pure additions with mapping anchor: insertion point is anchor + 1")
}

func TestStageGroups_ShouldNotExceedStageContent(t *testing.T) {
	// Stage's groups should only reference lines within the stage's content.
	var changes []LineChange

	// Add changes at low line numbers (will be in first cluster)
	for i := 1; i <= 17; i++ {
		changes = append(changes, LineChange{
			Type:       ChangeAddition,
			NewLineNum: i,
			OldLineNum: -1,
			Content:    fmt.Sprintf("line%d", i),
		})
	}

	// Add changes at high line numbers (should be in separate cluster)
	for i := 41; i <= 54; i++ {
		changes = append(changes, LineChange{
			Type:       ChangeAddition,
			NewLineNum: i,
			OldLineNum: -1,
			Content:    fmt.Sprintf("line%d", i),
		})
	}

	diff := &DiffResult{
		Changes:      changes,
		OldLineCount: 3,
		NewLineCount: 54,
	}

	// Create newLines for full completion
	newLines := make([]string, 54)
	oldLines := make([]string, 3)
	for i := range newLines {
		newLines[i] = fmt.Sprintf("content%d", i+1)
	}
	for i := range oldLines {
		oldLines[i] = fmt.Sprintf("old%d", i+1)
	}

	// Create stages with proximity threshold of 3
	// Gap between line 17 and 41 is 24, so they should be in separate clusters
	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          1,
		CursorCol:          0,
		ViewportTop:        1,
		ViewportBottom:     100,
		BaseLineOffset:     1,
		ProximityThreshold: 3,
		MaxLines:           0,
		FilePath:           "test.go",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	// Should have at least 2 stages (may have more due to viewport partitioning)
	assert.NotNil(t, result, "result")
	assert.True(t, len(result.Stages) >= 2, fmt.Sprintf("should have at least 2 stages, got %d", len(result.Stages)))

	// CRITICAL: For ALL stages, Groups should NOT reference lines
	// beyond the stage's content line count
	for i, stage := range result.Stages {
		stageLineCount := len(stage.Lines)

		for _, g := range stage.Groups {
			assert.True(t, g.StartLine <= stageLineCount,
				fmt.Sprintf("Stage %d: Group StartLine (%d) exceeds stage content line count (%d)",
					i, g.StartLine, stageLineCount))
			assert.True(t, g.EndLine <= stageLineCount,
				fmt.Sprintf("Stage %d: Group EndLine (%d) exceeds stage content line count (%d)",
					i, g.EndLine, stageLineCount))
		}
	}
}

func TestGetStageBufferRange_AdditionsAtEndOfFile(t *testing.T) {
	// When a stage contains additions that extend beyond the original buffer,
	// the buffer range should extend to the end of the original buffer.

	// Scenario: 10-line file, modification at line 8, additions at lines 9-12
	diff := &DiffResult{
		Changes: []LineChange{
			{Type: ChangeModification, NewLineNum: 8, OldLineNum: 8, Content: "modified", OldContent: "original"},
			{Type: ChangeAddition, NewLineNum: 9, OldLineNum: 8, Content: "added1"},
			{Type: ChangeAddition, NewLineNum: 10, OldLineNum: 8, Content: "added2"},
			{Type: ChangeAddition, NewLineNum: 11, OldLineNum: 8, Content: "added3"},
			{Type: ChangeAddition, NewLineNum: 12, OldLineNum: 8, Content: "added4"},
		},
		OldLineCount: 10,
		NewLineCount: 14,
	}

	stage := &stageBuilder{
		startLine:  8,
		endLine:    12,
		rawChanges: diff.Changes,
	}

	computeStageRanges(stage, 1, diff)

	assert.Equal(t, 8, stage.bufferStart, "buffer start line")
	assert.Equal(t, 8, stage.bufferEnd, "buffer end covers modification + addition anchor")
}

func TestGetStageBufferRange_AdditionsWithinBuffer(t *testing.T) {
	// When additions don't extend beyond the original buffer,
	// the buffer range should not be artificially extended.

	// Scenario: 20-line file, additions at lines 5-7 (well within buffer)
	diff := &DiffResult{
		Changes: []LineChange{
			{Type: ChangeAddition, NewLineNum: 5, OldLineNum: 4, Content: "added1"},
			{Type: ChangeAddition, NewLineNum: 6, OldLineNum: 4, Content: "added2"},
			{Type: ChangeAddition, NewLineNum: 7, OldLineNum: 4, Content: "added3"},
		},
		OldLineCount: 20,
		NewLineCount: 23,
		LineMapping: &LineMapping{
			NewToOld: []int{1, 2, 3, 4, -1, -1, -1, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20},
			OldToNew: []int{1, 2, 3, 4, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23},
		},
	}

	stage := &stageBuilder{
		startLine:  5,
		endLine:    7,
		rawChanges: diff.Changes,
	}

	computeStageRanges(stage, 1, diff)

	// Pure additions with anchor at line 4: insertion point is anchor + 1 = 5
	assert.Equal(t, 5, stage.bufferStart, "buffer start line is insertion point (anchor + 1)")
	assert.Equal(t, 5, stage.bufferEnd, "buffer end line equals start for pure additions")
}

func TestCreateStages_AdditionsAtEndOfFile(t *testing.T) {
	// End-to-end test: stage should have correct buffer range when
	// additions extend beyond the original buffer.

	// Scenario: 15-line file with changes at lines 12-18 (modification + 6 additions)
	diff := &DiffResult{
		Changes: []LineChange{
			{Type: ChangeModification, NewLineNum: 12, OldLineNum: 12, Content: "modified", OldContent: "original"},
			{Type: ChangeAddition, NewLineNum: 13, OldLineNum: 12, Content: "added1"},
			{Type: ChangeAddition, NewLineNum: 14, OldLineNum: 12, Content: "added2"},
			{Type: ChangeAddition, NewLineNum: 15, OldLineNum: 12, Content: "added3"},
			{Type: ChangeAddition, NewLineNum: 16, OldLineNum: 12, Content: "added4"},
			{Type: ChangeAddition, NewLineNum: 17, OldLineNum: 12, Content: "added5"},
			{Type: ChangeAddition, NewLineNum: 18, OldLineNum: 12, Content: "added6"},
		},
		OldLineCount: 15,
		NewLineCount: 21,
		LineMapping: &LineMapping{
			// Old 1-11 → new 1-11, old 12 → new 12 (modified), old 13-15 → new 19-21
			OldToNew: []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 19, 20, 21},
			NewToOld: []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, -1, -1, -1, -1, -1, -1, 13, 14, 15},
		},
	}

	newLines := make([]string, 21)
	oldLines := make([]string, 15)
	for i := range newLines {
		newLines[i] = fmt.Sprintf("line%d", i+1)
	}
	for i := range oldLines {
		oldLines[i] = fmt.Sprintf("line%d", i+1)
	}

	// Cursor at line 1 (far from changes), viewport covers all
	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          1,
		CursorCol:          0,
		ViewportTop:        1,
		ViewportBottom:     30,
		BaseLineOffset:     1,
		ProximityThreshold: 3,
		MaxLines:           0,
		FilePath:           "test.go",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	assert.NotNil(t, result, "result")
	assert.True(t, len(result.Stages) >= 1, "should have at least 1 stage")

	// Find stage with changes at line 12+
	var stage *struct{ BufferStart, BufferEnd, Lines int }
	for _, s := range result.Stages {
		if s.BufferStart >= 12 {
			stage = &struct{ BufferStart, BufferEnd, Lines int }{
				s.BufferStart, s.BufferEnd, len(s.Lines),
			}
			break
		}
	}

	assert.NotNil(t, stage, "should have stage at line 12+")
	if stage != nil {
		assert.Equal(t, 12, stage.BufferStart, "stage BufferStart")
		assert.Equal(t, 12, stage.BufferEnd, "stage BufferEnd should cover the modified old line")
		assert.Equal(t, 7, stage.Lines, "stage should have 7 lines (new lines 12-18)")
	}
}

func TestGetStageBufferRange_AdditionsAnchoredBeforeModifications(t *testing.T) {
	// When modifications exist at line N and additions are anchored to line N-1,
	// the buffer range should start at the first modification, not the anchor.

	diff := &DiffResult{
		Changes: []LineChange{
			// Modifications at lines 43-44 (have OldLineNum)
			{Type: ChangeModification, NewLineNum: 43, OldLineNum: 43, Content: "mod1", OldContent: "old1"},
			{Type: ChangeModification, NewLineNum: 44, OldLineNum: 44, Content: "mod2", OldContent: "old2"},
			// Additions at lines 45-50, anchored to line 42 (line before modification region)
			{Type: ChangeAddition, NewLineNum: 45, OldLineNum: 42, Content: "added1"},
			{Type: ChangeAddition, NewLineNum: 46, OldLineNum: 42, Content: "added2"},
			{Type: ChangeAddition, NewLineNum: 47, OldLineNum: 42, Content: "added3"},
			{Type: ChangeAddition, NewLineNum: 48, OldLineNum: 42, Content: "added4"},
			{Type: ChangeAddition, NewLineNum: 49, OldLineNum: 42, Content: "added5"},
			{Type: ChangeAddition, NewLineNum: 50, OldLineNum: 42, Content: "added6"},
		},
		OldLineCount: 44,
		NewLineCount: 50,
	}

	stage := &stageBuilder{
		startLine:  43,
		endLine:    50,
		rawChanges: diff.Changes,
	}

	computeStageRanges(stage, 1, diff)

	// StartLine should be 43 (first modification), NOT 42 (anchor of additions)
	assert.Equal(t, 43, stage.bufferStart,
		"buffer start should be 43 (first modification), not 42 (addition anchor)")

	// EndLine should be 44 (end of original buffer)
	assert.Equal(t, 44, stage.bufferEnd, "buffer end should be 44")
}

func TestGetStageBufferRange_OnlyAdditionsWithAnchor(t *testing.T) {
	// When a stage has ONLY additions (no modifications), the insertion point
	// (anchor + 1) determines the buffer range.

	diff := &DiffResult{
		Changes: []LineChange{
			// Only additions, all anchored to line 10
			{Type: ChangeAddition, NewLineNum: 11, OldLineNum: 10, Content: "added1"},
			{Type: ChangeAddition, NewLineNum: 12, OldLineNum: 10, Content: "added2"},
			{Type: ChangeAddition, NewLineNum: 13, OldLineNum: 10, Content: "added3"},
		},
		OldLineCount: 15,
		NewLineCount: 18,
	}

	stage := &stageBuilder{
		startLine:  11,
		endLine:    13,
		rawChanges: diff.Changes,
	}

	computeStageRanges(stage, 1, diff)

	// Pure additions with anchor at line 10: insertion point is anchor + 1 = 11
	assert.Equal(t, 11, stage.bufferStart, "buffer start should be insertion point (anchor + 1)")
	assert.Equal(t, 11, stage.bufferEnd, "buffer end equals start for pure additions")
}

func TestGetStageBufferRange_OnlyAdditionsBeyondBuffer(t *testing.T) {
	// When additions extend beyond the original buffer, the insertion point is still anchor + 1

	diff := &DiffResult{
		Changes: []LineChange{
			// Additions beyond original buffer (OldLineCount=10, additions at new lines 11-15)
			{Type: ChangeAddition, NewLineNum: 11, OldLineNum: 10, Content: "added1"},
			{Type: ChangeAddition, NewLineNum: 12, OldLineNum: 10, Content: "added2"},
			{Type: ChangeAddition, NewLineNum: 13, OldLineNum: 10, Content: "added3"},
			{Type: ChangeAddition, NewLineNum: 14, OldLineNum: 10, Content: "added4"},
			{Type: ChangeAddition, NewLineNum: 15, OldLineNum: 10, Content: "added5"},
		},
		OldLineCount: 10,
		NewLineCount: 15,
	}

	stage := &stageBuilder{
		startLine:  11,
		endLine:    15,
		rawChanges: diff.Changes,
	}

	computeStageRanges(stage, 1, diff)

	// Pure additions with anchor at line 10: insertion point is anchor + 1 = 11
	assert.Equal(t, 11, stage.bufferStart, "buffer start should be insertion point (anchor + 1)")
	assert.Equal(t, 11, stage.bufferEnd, "buffer end equals start for pure additions (anchor + 1)")
}

func TestCreateStages_EmptyNewLines(t *testing.T) {
	// Edge case: newLines slice is empty but diff has changes

	diff := &DiffResult{
		Changes: []LineChange{
			{Type: ChangeModification, NewLineNum: 1, OldLineNum: 1, Content: "mod", OldContent: "old"},
			{Type: ChangeModification, NewLineNum: 5, OldLineNum: 5, Content: "mod2", OldContent: "old2"},
		},
		OldLineCount: 10,
		NewLineCount: 10,
	}

	// Empty newLines slice
	newLines := []string{}
	oldLines := []string{}

	// Should not panic, should return result with empty Lines in stages
	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          3,
		CursorCol:          0,
		ViewportTop:        1,
		ViewportBottom:     20,
		BaseLineOffset:     1,
		ProximityThreshold: 2,
		MaxLines:           0,
		FilePath:           "test.go",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	assert.NotNil(t, result, "result should not be nil")
	assert.True(t, len(result.Stages) >= 1, "should have stages")

	// Stages should have empty Lines arrays
	for i, stage := range result.Stages {
		// Lines will be empty since newLines is empty
		assert.Equal(t, 0, len(stage.Lines), fmt.Sprintf("stage %d should have empty lines", i))
	}
}

func TestStageDistanceFromCursor_CursorAtZero(t *testing.T) {
	// Edge case: cursor at line 0 (invalid but should handle gracefully)

	stage := &Stage{
		BufferStart: 5,
		BufferEnd:   5,
	}

	// Cursor at line 0
	distance := distanceFromCursor(stage.BufferStart, stage.BufferEnd, 0)

	// Buffer line for stage is 5, cursor is 0
	// Distance should be 5 - 0 = 5
	assert.Equal(t, 5, distance, "distance from cursor 0 to line 5")
}

func TestGetStageBufferRange_AllAdditionsNoValidAnchor(t *testing.T) {
	// Edge case: all additions with OldLineNum=0 and no LineMapping

	diff := &DiffResult{
		Changes: []LineChange{
			// All additions with no valid anchor (OldLineNum=0, no mapping)
			{Type: ChangeAddition, NewLineNum: 5, OldLineNum: 0, Content: "added1"},
			{Type: ChangeAddition, NewLineNum: 6, OldLineNum: 0, Content: "added2"},
			{Type: ChangeAddition, NewLineNum: 7, OldLineNum: 0, Content: "added3"},
		},
		OldLineCount: 10,
		NewLineCount: 13,
		LineMapping:  nil, // No mapping available
	}

	stage := &stageBuilder{
		startLine:  5,
		endLine:    7,
		rawChanges: diff.Changes,
	}

	computeStageRanges(stage, 1, diff)

	// Anchorless additions with no mapping: falls back to stage start/end lines
	assert.Equal(t, 5, stage.bufferStart, "should fallback to stage.startLine")
	assert.Equal(t, 7, stage.bufferEnd, "should fallback to stage.endLine")
}

func TestGetStageBufferRange_BaseLineOffsetZero(t *testing.T) {
	// Edge case: baseLineOffset = 0

	diff := &DiffResult{
		Changes: []LineChange{
			{Type: ChangeModification, NewLineNum: 5, OldLineNum: 5, Content: "mod", OldContent: "old"},
		},
		OldLineCount: 10,
		NewLineCount: 10,
	}

	stage := &stageBuilder{
		startLine:  5,
		endLine:    5,
		rawChanges: diff.Changes,
	}

	computeStageRanges(stage, 0, diff)

	// With baseLineOffset=0: bufferLine = 5 + 0 - 1 = 4
	assert.Equal(t, 4, stage.bufferStart, "buffer start with offset 0")
	assert.Equal(t, 4, stage.bufferEnd, "buffer end with offset 0")
}

func TestCreateStages_PartiallyVisibleSingleCluster_FarFromCursor(t *testing.T) {
	// Edge case: single cluster that is partially visible but far from cursor

	diff := &DiffResult{
		Changes: []LineChange{
			// Cluster spans lines 45-55, viewport is 1-50, so partially visible
			{Type: ChangeModification, NewLineNum: 45, OldLineNum: 45, Content: "mod1", OldContent: "old1"},
			{Type: ChangeModification, NewLineNum: 50, OldLineNum: 50, Content: "mod2", OldContent: "old2"},
			{Type: ChangeModification, NewLineNum: 55, OldLineNum: 55, Content: "mod3", OldContent: "old3"},
		},
		OldLineCount: 60,
		NewLineCount: 60,
	}

	newLines := make([]string, 60)
	oldLines := make([]string, 60)
	for i := range newLines {
		newLines[i] = fmt.Sprintf("line%d", i+1)
		oldLines[i] = fmt.Sprintf("line%d", i+1)
	}

	// Cursor at line 5 (far from cluster at 45-55), viewport 1-50 (cluster partially visible)
	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          5,
		CursorCol:          0,
		ViewportTop:        1,
		ViewportBottom:     50,
		BaseLineOffset:     1,
		ProximityThreshold: 3,
		MaxLines:           0,
		FilePath:           "test.go",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	// Single cluster that's partially visible but far from cursor
	assert.NotNil(t, result, "should create staging result")
	assert.True(t, len(result.Stages) >= 1, "should have at least 1 stage")

	// Distance from cursor (5) to cluster start (45) = 40 > threshold (3)
	assert.True(t, result.FirstNeedsNavigation,
		"FirstNeedsNavigation should be true when cluster is far from cursor")
}

func TestCreateStages_PartiallyVisibleSingleCluster_CloseToCursor(t *testing.T) {
	// Edge case: single cluster that is partially visible but close to cursor

	diff := &DiffResult{
		Changes: []LineChange{
			// Cluster spans lines 48-55, viewport is 1-50
			// Cursor at 47 is within threshold of 48
			{Type: ChangeModification, NewLineNum: 48, OldLineNum: 48, Content: "mod1", OldContent: "old1"},
			{Type: ChangeModification, NewLineNum: 55, OldLineNum: 55, Content: "mod2", OldContent: "old2"},
		},
		OldLineCount: 60,
		NewLineCount: 60,
	}

	newLines := make([]string, 60)
	oldLines := make([]string, 60)
	for i := range newLines {
		newLines[i] = fmt.Sprintf("line%d", i+1)
		oldLines[i] = fmt.Sprintf("line%d", i+1)
	}

	// Cursor at line 47, viewport 1-50
	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          47,
		CursorCol:          0,
		ViewportTop:        1,
		ViewportBottom:     50,
		BaseLineOffset:     1,
		ProximityThreshold: 3,
		MaxLines:           0,
		FilePath:           "test.go",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	assert.NotNil(t, result, "should create staging result for partially visible cluster")
}

func TestCreateStages_SingleClusterEntirelyOutsideViewport(t *testing.T) {
	// Edge case: single cluster entirely outside viewport

	diff := &DiffResult{
		Changes: []LineChange{
			{Type: ChangeModification, NewLineNum: 100, OldLineNum: 100, Content: "mod", OldContent: "old"},
		},
		OldLineCount: 150,
		NewLineCount: 150,
	}

	newLines := make([]string, 150)
	oldLines := make([]string, 150)
	for i := range newLines {
		newLines[i] = fmt.Sprintf("line%d", i+1)
		oldLines[i] = fmt.Sprintf("line%d", i+1)
	}

	// Cursor at line 10, viewport 1-50, cluster at 100 (entirely outside)
	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          10,
		CursorCol:          0,
		ViewportTop:        1,
		ViewportBottom:     50,
		BaseLineOffset:     1,
		ProximityThreshold: 3,
		MaxLines:           0,
		FilePath:           "test.go",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	assert.NotNil(t, result, "should create staging result")
	assert.Equal(t, 1, len(result.Stages), "should have 1 stage")
	assert.True(t, result.FirstNeedsNavigation,
		"FirstNeedsNavigation should be true when cluster is outside viewport")
}

func TestStageNeedsNavigation_PartiallyVisible(t *testing.T) {
	// Test stageNeedsNavigation for partially visible stage

	stage := &Stage{
		BufferStart: 45,
		BufferEnd:   55,
	}

	// Viewport 1-50: stage 45-55 is partially visible
	// Cursor at 47: distance to 45-55 is 0 (cursor within range)
	needsNav := StageNeedsNavigation(stage, 47, 1, 50, 3)

	// Not entirely outside viewport, cursor within stage
	assert.False(t, needsNav, "should not need navigation when cursor is within stage")

	// Cursor at 10: distance to 45-55 is 35
	needsNav = StageNeedsNavigation(stage, 10, 1, 50, 3)

	// Not entirely outside viewport, but far from cursor
	assert.True(t, needsNav, "should need navigation when far from cursor")
}

func TestStageNeedsNavigation_AdditionsAtEndOfFile(t *testing.T) {
	stage := &Stage{
		BufferStart: 3,
		BufferEnd:   3,
	}

	// Viewport [1, 2]: BufferStart 3 is outside viewport, needs navigation
	// even though cursor at line 2 is close (distance 1 < threshold 2).
	needsNav := StageNeedsNavigation(stage, 2, 1, 2, 2)
	assert.True(t, needsNav, "additions outside viewport should need navigation")

	// Viewport [1, 10]: BufferStart 3 is inside viewport, no navigation needed.
	needsNav = StageNeedsNavigation(stage, 2, 1, 10, 2)
	assert.False(t, needsNav, "additions inside viewport should not need navigation")
}

func TestCreateStages_NoViewportInfo(t *testing.T) {
	// Edge case: viewportTop=0 and viewportBottom=0 (no viewport info)

	diff := &DiffResult{
		Changes: []LineChange{
			{Type: ChangeModification, NewLineNum: 10, OldLineNum: 10, Content: "mod1", OldContent: "old1"},
			{Type: ChangeModification, NewLineNum: 100, OldLineNum: 100, Content: "mod2", OldContent: "old2"},
		},
		OldLineCount: 150,
		NewLineCount: 150,
	}

	newLines := make([]string, 150)
	oldLines := make([]string, 150)
	for i := range newLines {
		newLines[i] = fmt.Sprintf("line%d", i+1)
		oldLines[i] = fmt.Sprintf("line%d", i+1)
	}

	// No viewport info (0, 0), cursor at 50
	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          50,
		CursorCol:          0,
		ViewportTop:        0,
		ViewportBottom:     0,
		BaseLineOffset:     1,
		ProximityThreshold: 3,
		MaxLines:           0,
		FilePath:           "test.go",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	assert.NotNil(t, result, "should create staging result")
	assert.Equal(t, 2, len(result.Stages), "should have 2 stages (large gap between 10 and 100)")

	// Both should be treated as in-view, sorted by distance to cursor
	// Cursor at 50: distance to 10 is 40, distance to 100 is 50
	// So line 10 should be first
	assert.Equal(t, 10, result.Stages[0].BufferStart, "closer stage first")
}

func TestGetStageNewLineRangeFromChanges_DerivedFromChanges(t *testing.T) {
	// New range is derived from changes' NewLineNum values and unchanged old
	// lines within the buffer range.
	// Old lines 5-7, where old 6 is deleted → new has lines 5 and 7 (shifted).
	mapping := &LineMapping{
		OldToNew: []int{1, 2, 3, 4, -1, 5, 6, 7, 8, 9},
		NewToOld: []int{1, 2, 3, 4, 6, 7, 8, 9, 10},
	}
	stage := &stageBuilder{
		bufferStart: 5,
		bufferEnd:   7,
		rawChanges: []LineChange{
			{Type: ChangeModification, OldLineNum: 5, NewLineNum: 5},
			{Type: ChangeDeletion, OldLineNum: 6, NewLineNum: -1},
			{Type: ChangeModification, OldLineNum: 7, NewLineNum: 6},
		},
	}
	// Changes have NewLineNum 5 and 6 (ignoring deletion).
	// Unchanged old lines: old 5 maps to 4 (outside range check uses OldToNew
	// but old 5 is OldToNew[4]=-1, old 6 is OldToNew[5]=5, old 7 is OldToNew[6]=6).
	// Combined: newStart=5, newEnd=6
	diff := &DiffResult{LineMapping: mapping, Changes: stage.rawChanges}
	computeStageRanges(stage, 1, diff)
	assert.Equal(t, 5, stage.newLineStart, "newStart")
	assert.Equal(t, 6, stage.newLineEnd, "newEnd")
}

func TestFinalizeStages_SingleDeletion(t *testing.T) {
	// Edge case: stage with only deletions (no new content)

	diff := &DiffResult{
		Changes: []LineChange{
			{Type: ChangeDeletion, NewLineNum: 0, OldLineNum: 5, OldContent: "deleted"},
		},
		OldLineCount: 10,
		NewLineCount: 9,
	}

	b := &stageBuilder{
		startLine:  5,
		endLine:    5,
		rawChanges: diff.Changes,
	}
	computeStageRanges(b, 1, diff)

	newLines := []string{"1", "2", "3", "4", "6", "7", "8", "9", "10"} // Line 5 deleted

	stages := finalizeStages([]*stageBuilder{b}, newLines, nil, "test.go", 1, diff, 1, 0, 0)

	assert.Equal(t, 1, len(stages), "should have 1 stage")
	// For deletions, lines might be empty or contain surrounding context
	// The important thing is it doesn't panic
	assert.NotNil(t, stages[0], "stage should not be nil")
}

// TestStageCoordinates_ModificationHasCorrectMapping verifies that modifications
// in a stage have correct coordinate mapping for rendering.
func TestStageCoordinates_ModificationHasCorrectMapping(t *testing.T) {
	// Create a diff with a modification
	diff := &DiffResult{
		Changes: []LineChange{
			{Type: ChangeModification, NewLineNum: 2, OldLineNum: 2, Content: "new line 2", OldContent: "old line 2"},
		},
		OldLineCount: 5,
		NewLineCount: 5,
	}

	newLines := []string{"line1", "new line 2", "line3", "line4", "line5"}
	oldLines := []string{"line1", "old line 2", "line3", "line4", "line5"}

	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          2,
		CursorCol:          0,
		ViewportTop:        1,
		ViewportBottom:     50,
		BaseLineOffset:     1,
		ProximityThreshold: 3,
		MaxLines:           0,
		FilePath:           "test.go",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	assert.NotNil(t, result, "result")
	assert.True(t, len(result.Stages) >= 1, "should have at least 1 stage")

	stage := result.Stages[0]
	assert.True(t, len(stage.Changes) > 0, "stage should have changes")

	// Verify the change has correct NewLineNum
	for _, change := range stage.Changes {
		if change.Type == ChangeModification {
			assert.True(t, change.NewLineNum > 0, "modification should have NewLineNum > 0")
		}
	}
}

// TestStageCoordinates_AdditionMapping verifies that additions have correct coordinate mapping.
func TestStageCoordinates_AdditionMapping(t *testing.T) {
	diff := &DiffResult{
		Changes: []LineChange{
			{Type: ChangeAddition, NewLineNum: 2, OldLineNum: 1, Content: "added line"},
		},
		OldLineCount: 3,
		NewLineCount: 4,
		LineMapping: &LineMapping{
			NewToOld: []int{1, -1, 2, 3},
			OldToNew: []int{1, 3, 4},
		},
	}

	newLines := []string{"line1", "added line", "line2", "line3"}
	oldLines := []string{"line1", "line2", "line3"}

	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          1,
		CursorCol:          0,
		ViewportTop:        1,
		ViewportBottom:     50,
		BaseLineOffset:     1,
		ProximityThreshold: 3,
		MaxLines:           0,
		FilePath:           "test.go",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	assert.NotNil(t, result, "result")
	assert.True(t, len(result.Stages) >= 1, "should have at least 1 stage")
}

// TestSingleLineToMultipleLinesAllIncluded verifies that when one old line becomes
// multiple new lines, all the new lines are included in the stage.
func TestSingleLineToMultipleLinesAllIncluded(t *testing.T) {
	// Scenario: one whitespace line becomes three content lines
	oldText := `        {

        }`
	newText := `        {
            "timestamp": "2022-01-04T01:00:00Z",
            "value": 260,
            "name": "John"
        }`

	diffResult := ComputeDiff(oldText, newText)

	// We should have changes for new lines 2, 3, 4 (the three content lines)
	assert.True(t, len(diffResult.Changes) >= 2,
		fmt.Sprintf("Expected at least 2 changes, got %d", len(diffResult.Changes)))

	// Verify that additions from a delete+insert block stay together for staging
	var additionOldLineNums []int
	for _, change := range diffResult.Changes {
		if change.Type == ChangeAddition {
			additionOldLineNums = append(additionOldLineNums, change.OldLineNum)
		}
	}

	// All additions should have the same anchor (OldLineNum)
	for i := 1; i < len(additionOldLineNums); i++ {
		assert.Equal(t, additionOldLineNums[0], additionOldLineNums[i],
			"All additions in a delete+insert block should have the same OldLineNum anchor")
	}
}

// TestStageIncludesAllLinesFromDeleteInsertBlock verifies that when creating stages
// from a diff where one line becomes multiple lines, all the new lines are included.
func TestStageIncludesAllLinesFromDeleteInsertBlock(t *testing.T) {
	// Scenario: one whitespace line becomes three content lines
	oldText := "            " // just whitespace
	newText := `            "timestamp": "2022-01-04T01:00:00Z",
            "value": 260,
            "name": "John"`

	diffResult := ComputeDiff(oldText, newText)

	// Create stages from this diff
	newLines := SplitLines(newText)
	oldLines := SplitLines(oldText)
	stagingResult := CreateStages(&StagingParams{
		Diff:               diffResult,
		CursorRow:          1,
		CursorCol:          0,
		ViewportTop:        0,
		ViewportBottom:     0,
		BaseLineOffset:     1,
		ProximityThreshold: 3,
		MaxLines:           0,
		FilePath:           "test.json",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	// All changes should be in one stage since they're from the same delete+insert block
	if stagingResult != nil && len(stagingResult.Stages) > 0 {
		stage := stagingResult.Stages[0]

		// The stage should have all 3 lines
		assert.Equal(t, 3, len(stage.Lines),
			fmt.Sprintf("Stage should have 3 lines, got %d", len(stage.Lines)))

		// The groups should cover the changed lines
		totalLinesInGroups := 0
		for _, g := range stage.Groups {
			totalLinesInGroups += g.EndLine - g.StartLine + 1
		}
		assert.True(t, totalLinesInGroups >= 2,
			fmt.Sprintf("Groups should cover at least 2 lines, got %d", totalLinesInGroups))
	}
}

// TestMixedChangesCoordinates verifies that a mix of modifications and additions
// have correct coordinate mappings.
func TestMixedChangesCoordinates(t *testing.T) {
	// Simulate: 5 old lines replaced by 8 new lines
	diff := &DiffResult{
		Changes: []LineChange{
			{Type: ChangeModification, NewLineNum: 4, OldLineNum: 4, Content: "MODIFIED line 4", OldContent: "line 4"},
			{Type: ChangeAddition, NewLineNum: 5, OldLineNum: 4, Content: "ADDED line 5"},
			{Type: ChangeAddition, NewLineNum: 6, OldLineNum: 4, Content: "ADDED line 6"},
			{Type: ChangeAddition, NewLineNum: 7, OldLineNum: 4, Content: "ADDED line 7"},
			{Type: ChangeAddition, NewLineNum: 8, OldLineNum: 4, Content: "ADDED line 8"},
		},
		OldLineCount: 5,
		NewLineCount: 8,
	}

	newLines := []string{
		"unchanged1",
		"unchanged2",
		"unchanged3",
		"MODIFIED line 4",
		"ADDED line 5",
		"ADDED line 6",
		"ADDED line 7",
		"ADDED line 8",
	}
	oldLines := []string{
		"unchanged1",
		"unchanged2",
		"unchanged3",
		"line 4",
		"line 5",
	}

	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          4,
		CursorCol:          0,
		ViewportTop:        1,
		ViewportBottom:     50,
		BaseLineOffset:     1,
		ProximityThreshold: 3,
		MaxLines:           0,
		FilePath:           "test.go",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	assert.NotNil(t, result, "result")
	assert.True(t, len(result.Stages) >= 1, "should have at least 1 stage")

	// Find the stage with changes
	stage := result.Stages[0]

	// Should have changes
	assert.True(t, len(stage.Changes) > 0, "stage should have changes")

	// Check that modification has OldLineNum
	for _, change := range stage.Changes {
		if change.Type == ChangeModification {
			assert.True(t, change.OldLineNum > 0, "modification should have OldLineNum > 0")
		}
	}
}

// TestGroupsDoNotOverlapWithModifications verifies that when staging creates groups,
// they don't overlap.
func TestGroupsDoNotOverlapWithModifications(t *testing.T) {
	diff := &DiffResult{
		Changes: []LineChange{
			// Modification at new line 1
			{Type: ChangeModification, NewLineNum: 1, OldLineNum: 3, Content: "new1", OldContent: "old3"},
			// Character-level change at new line 2
			{Type: ChangeDeleteChars, NewLineNum: 2, OldLineNum: 1, Content: "new2", OldContent: "old1", ColStart: 0, ColEnd: 4},
			// Addition at new line 3
			{Type: ChangeAddition, NewLineNum: 3, OldLineNum: -1, Content: "added3"},
			// Addition at new line 4
			{Type: ChangeAddition, NewLineNum: 4, OldLineNum: -1, Content: "added4"},
		},
		OldLineCount: 5,
		NewLineCount: 8,
	}

	newLines := []string{"new1", "new2", "added3", "added4", "added5", "added6", "added7", "added8"}
	oldLines := []string{"", "", "old3", "", "old1"}

	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          1,
		CursorCol:          0,
		ViewportTop:        1,
		ViewportBottom:     50,
		BaseLineOffset:     1,
		ProximityThreshold: 3,
		MaxLines:           0,
		FilePath:           "test.go",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	assert.NotNil(t, result, "result")

	// Verify groups don't reference lines beyond stage content bounds
	for _, stage := range result.Stages {
		for _, g := range stage.Groups {
			assert.True(t, g.StartLine >= 1 && g.StartLine <= len(stage.Lines),
				fmt.Sprintf("Group StartLine %d should be within [1, %d]", g.StartLine, len(stage.Lines)))
			assert.True(t, g.EndLine >= 1 && g.EndLine <= len(stage.Lines),
				fmt.Sprintf("Group EndLine %d should be within [1, %d]", g.EndLine, len(stage.Lines)))
		}
	}
}

// TestBufferLineCalculation simulates how buffer positions are calculated and verifies
// that stage coordinates are correct.
func TestBufferLineCalculation(t *testing.T) {
	// Simulate a stage that covers buffer lines 28-32 (baseLineOffset=28)
	// with 8 new lines replacing 5 old lines
	diff := &DiffResult{
		Changes: []LineChange{
			{Type: ChangeModification, NewLineNum: 4, OldLineNum: 4, Content: "MODIFIED line 4", OldContent: "old 4"},
			{Type: ChangeAddition, NewLineNum: 5, OldLineNum: 4, Content: "ADDED 5"},
			{Type: ChangeAddition, NewLineNum: 6, OldLineNum: 4, Content: "ADDED 6"},
			{Type: ChangeAddition, NewLineNum: 7, OldLineNum: 4, Content: "ADDED 7"},
			{Type: ChangeAddition, NewLineNum: 8, OldLineNum: 4, Content: "ADDED 8"},
		},
		OldLineCount: 5,
		NewLineCount: 8,
	}

	newLines := []string{
		"line1", "line2", "line3",
		"MODIFIED line 4",
		"ADDED 5", "ADDED 6", "ADDED 7", "ADDED 8",
	}
	oldLines := []string{"line1", "line2", "line3", "old 4", "line5"}

	baseLineOffset := 28
	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          30,
		CursorCol:          0,
		ViewportTop:        1,
		ViewportBottom:     100,
		BaseLineOffset:     baseLineOffset,
		ProximityThreshold: 3,
		MaxLines:           0,
		FilePath:           "test.go",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	assert.NotNil(t, result, "result")
	assert.True(t, len(result.Stages) >= 1, "should have at least 1 stage")

	// Check that BufferStart is correctly offset
	stage := result.Stages[0]
	assert.True(t, stage.BufferStart >= baseLineOffset,
		fmt.Sprintf("BufferStart (%d) should be >= baseLineOffset (%d)", stage.BufferStart, baseLineOffset))
}

// TestPureAdditionsAfterExistingContent verifies that when adding lines after
// the end of existing content, BufferStart points to the first new line, not the anchor.
func TestPureAdditionsAfterExistingContent(t *testing.T) {
	// File has 2 lines, completion adds 8 more lines
	// Lines 1-2 unchanged, lines 3-10 are pure additions
	oldLines := []string{"const x = 1;", ""}
	newLines := []string{
		"const x = 1;",
		"",
		"func helper1() {}",
		"func helper2() {}",
		"",
		"func helper3() {}",
		"func helper4() {}",
		"",
		"func helper5() {}",
		"func helper6() {}",
	}

	oldText := JoinLines(oldLines)
	newText := JoinLines(newLines)
	diff := ComputeDiff(oldText, newText)

	// Lines 3-10 should be additions
	assert.True(t, len(diff.Changes) >= 8, fmt.Sprintf("Expected at least 8 changes, got %d", len(diff.Changes)))

	// All changes should be additions
	for k, change := range diff.Changes {
		assert.Equal(t, ChangeAddition, change.Type,
			fmt.Sprintf("Change at key %d should be addition", k))
	}

	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          2,
		CursorCol:          0,
		ViewportTop:        0,
		ViewportBottom:     0,
		BaseLineOffset:     1,
		ProximityThreshold: 3,
		MaxLines:           0,
		FilePath:           "test.go",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	assert.NotNil(t, result, "result should not be nil")
	assert.True(t, len(result.Stages) >= 1, "should have at least 1 stage")

	stage := result.Stages[0]

	// BufferStart should be 3 (insertion point after last old line)
	assert.Equal(t, 3, stage.BufferStart,
		fmt.Sprintf("BufferStart should be 3 (insertion point), got %d", stage.BufferStart))

	// BufferEnd equals BufferStart for pure same-anchor additions (insertion semantics)
	assert.Equal(t, 3, stage.BufferEnd,
		fmt.Sprintf("BufferEnd should equal BufferStart for pure insertion, got %d", stage.BufferEnd))
}

// TestMixedDeletionAndAdditions verifies correct staging when old content has a
// leading line that's deleted while new lines are added at the end.
func TestMixedDeletionAndAdditions(t *testing.T) {
	// Old has leading empty line, new does not (trimmed)
	oldLines := []string{"", "// Comment", "const x = 1;", ""}
	newLines := []string{"// Comment", "const x = 1;", "", "// New section", "const y = 2;", ""}

	oldText := JoinLines(oldLines)
	newText := JoinLines(newLines)
	diff := ComputeDiff(oldText, newText)

	assert.True(t, len(diff.Changes) >= 1, fmt.Sprintf("Expected at least 1 change, got %d", len(diff.Changes)))

	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          1,
		CursorCol:          0,
		ViewportTop:        1,
		ViewportBottom:     100,
		BaseLineOffset:     1,
		ProximityThreshold: 3,
		MaxLines:           0,
		FilePath:           "test.go",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	assert.NotNil(t, result, "result should not be nil")
	// Deletion at buffer line 1 and additions at buffer line 5 are separated
	// by a gap of 4 (> proximity threshold 3), producing 2 stages.
	assert.Len(t, 2, result.Stages, "stages")

	// First stage (closest to cursor at row 1) is the deletion
	assert.Equal(t, 1, result.Stages[0].BufferStart, "deletion stage BufferStart")

	// Second stage has the additions
	assert.True(t, len(result.Stages[1].Lines) >= 3,
		fmt.Sprintf("Addition stage should have at least 3 lines, got %d", len(result.Stages[1].Lines)))
}

// TestShortBufferDiffComputation tests what happens when the buffer has fewer
// lines than expected by the completion range.
func TestShortBufferDiffComputation(t *testing.T) {
	// Old: 1 line (buffer only had this much)
	oldLines := []string{"// Comment"}

	// New: 6 lines from completion
	newLines := []string{
		"// Comment",
		"const x = 1;",
		"",
		"// Section",
		"const y = 2;",
		"",
	}

	oldText := JoinLines(oldLines)
	newText := JoinLines(newLines)
	diff := ComputeDiff(oldText, newText)

	// The diff should detect additions (lines 2-6)
	assert.True(t, len(diff.Changes) >= 5,
		fmt.Sprintf("Expected at least 5 changes (additions), got %d", len(diff.Changes)))

	// Create stages
	baseLineOffset := 43
	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          43,
		CursorCol:          0,
		ViewportTop:        1,
		ViewportBottom:     100,
		BaseLineOffset:     baseLineOffset,
		ProximityThreshold: 3,
		MaxLines:           0,
		FilePath:           "test.ts",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	assert.NotNil(t, result, "result should not be nil")
	assert.True(t, len(result.Stages) >= 1, "should have at least 1 stage")

	stage := result.Stages[0]

	// The stage should have all 5 additions
	assert.True(t, len(stage.Lines) >= 5,
		fmt.Sprintf("Stage should have at least 5 lines (additions), got %d", len(stage.Lines)))

	// BufferStart should be 44 (after the unchanged line 43, for pure additions)
	assert.Equal(t, 44, stage.BufferStart,
		fmt.Sprintf("BufferStart should be 44 (insertion point after anchor 43), got %d", stage.BufferStart))
}

// TestEmptyOldContent tests what happens when old content is empty
// (buffer has fewer lines than StartLine). All new lines become additions.
func TestEmptyOldContent(t *testing.T) {
	// Old: empty (buffer didn't have lines in this range)
	oldLines := []string{}

	// New: 6 lines from completion
	newLines := []string{
		"// Initialize Hono app with types",
		"const application = new Hono<ApiContext>();",
		"",
		"// Global middleware",
		"application.use(\"*\", corsMiddleware);",
		"",
	}

	oldText := JoinLines(oldLines)
	newText := JoinLines(newLines)
	diff := ComputeDiff(oldText, newText)

	// All 6 new lines should be additions
	assert.Equal(t, 6, len(diff.Changes), "All 6 lines should be additions")

	// Create stages
	baseLineOffset := 43
	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          43,
		CursorCol:          0,
		ViewportTop:        1,
		ViewportBottom:     100,
		BaseLineOffset:     baseLineOffset,
		ProximityThreshold: 3,
		MaxLines:           0,
		FilePath:           "test.ts",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	if result == nil {
		// Staging may return nil for empty old content
		return
	}

	// All additions should be in a single stage
	assert.True(t, len(result.Stages) >= 1, "should have at least 1 stage")

	// Total lines should be 6
	totalLines := 0
	for _, stage := range result.Stages {
		totalLines += len(stage.Lines)
	}
	assert.Equal(t, 6, totalLines, "Total lines should be 6")
}

// TestLeadingEmptyLineDeletion tests staging when old content has a leading
// empty line that gets deleted while additions occur.
func TestLeadingEmptyLineDeletion(t *testing.T) {
	// Original has leading blank line that gets trimmed in completion
	oldLines := []string{
		"", // leading blank line
		"// Comment",
		"const x = 1;",
		"",
	}

	// Completion with leading newline removed, plus additions
	newLines := []string{
		"// Comment",
		"const x = 1;",
		"",
		"// New section",
		"const y = 2;",
		"",
	}

	oldText := JoinLines(oldLines)
	newText := JoinLines(newLines)
	diff := ComputeDiff(oldText, newText)

	// Create stages
	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          5,
		CursorCol:          0,
		ViewportTop:        1,
		ViewportBottom:     100,
		BaseLineOffset:     1,
		ProximityThreshold: 3,
		MaxLines:           0,
		FilePath:           "test.go",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	if result == nil {
		assert.NotNil(t, result, "staging result should not be nil")
		return
	}
	assert.True(t, len(result.Stages) >= 1, "should have at least 1 stage")

	// The total lines across all stages should be more than 1
	totalLines := 0
	for _, stage := range result.Stages {
		totalLines += len(stage.Lines)
	}
	assert.True(t, totalLines >= 3,
		fmt.Sprintf("Total lines across stages should be at least 3, got %d", totalLines))
}

// TestStageGroupBounds verifies that stage groups don't exceed stage content bounds.
func TestStageGroupBounds(t *testing.T) {
	// Create changes at different line numbers with a gap
	diff := &DiffResult{
		Changes: []LineChange{
			{Type: ChangeAddition, NewLineNum: 1, OldLineNum: -1, Content: "line1"},
			{Type: ChangeAddition, NewLineNum: 2, OldLineNum: -1, Content: "line2"},
			{Type: ChangeAddition, NewLineNum: 3, OldLineNum: -1, Content: "line3"},
			// Gap
			{Type: ChangeAddition, NewLineNum: 20, OldLineNum: -1, Content: "line20"},
			{Type: ChangeAddition, NewLineNum: 21, OldLineNum: -1, Content: "line21"},
		},
		OldLineCount: 3,
		NewLineCount: 21,
	}

	newLines := make([]string, 21)
	for i := range newLines {
		newLines[i] = fmt.Sprintf("line%d", i+1)
	}
	oldLines := []string{"old1", "old2", "old3"}

	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          1,
		CursorCol:          0,
		ViewportTop:        1,
		ViewportBottom:     50,
		BaseLineOffset:     1,
		ProximityThreshold: 3,
		MaxLines:           0,
		FilePath:           "test.go",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	assert.NotNil(t, result, "result")
	assert.True(t, len(result.Stages) >= 2, "should have at least 2 stages (gap between 3 and 20)")

	// Each stage's groups should only reference lines within that stage's content
	for i, stage := range result.Stages {
		stageLineCount := len(stage.Lines)
		for _, g := range stage.Groups {
			assert.True(t, g.StartLine <= stageLineCount,
				fmt.Sprintf("Stage %d: Group StartLine (%d) exceeds stage line count (%d)",
					i, g.StartLine, stageLineCount))
			assert.True(t, g.EndLine <= stageLineCount,
				fmt.Sprintf("Stage %d: Group EndLine (%d) exceeds stage line count (%d)",
					i, g.EndLine, stageLineCount))
		}
	}
}

// TestCreateStages_PureAdditionsGroupBufferLineConsistency verifies that
// for pure additions, all groups have buffer_line values consistent with
// the stage's buffer range.
func TestCreateStages_PureAdditionsGroupBufferLineConsistency(t *testing.T) {
	// When a completion contains only additions, groups should have buffer_line
	// that matches the stage's BufferStart (the insertion point).
	diff := &DiffResult{
		Changes: []LineChange{
			{Type: ChangeAddition, NewLineNum: 1, OldLineNum: 3, Content: "added line"},
		},
		OldLineCount: 3,
		NewLineCount: 4,
		LineMapping: &LineMapping{
			NewToOld: []int{1, 2, 3, -1},
			OldToNew: []int{1, 2, 3},
		},
	}

	newLines := []string{"added line"}
	oldLines := []string{""}

	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          1,
		CursorCol:          0,
		ViewportTop:        1,
		ViewportBottom:     50,
		BaseLineOffset:     4,
		ProximityThreshold: 3,
		MaxLines:           0,
		FilePath:           "test.txt",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	assert.NotNil(t, result, "result should not be nil")
	assert.Equal(t, 1, len(result.Stages), "should have 1 stage")

	stage := result.Stages[0]

	// Verify groups have consistent buffer_line with stage
	for _, g := range stage.Groups {
		assert.Equal(t, stage.BufferStart, g.BufferLine,
			"group BufferLine should match stage BufferStart for pure additions")
	}
}

// TestGetStageBufferRange_PureAdditionsBufferLineMapping verifies that
// the bufferLines map is populated with correct insertion points for pure additions.
func TestGetStageBufferRange_PureAdditionsBufferLineMapping(t *testing.T) {
	// For pure additions with valid anchors, the bufferLines map should contain
	// insertion points (anchor + 1), not anchor positions.
	diff := &DiffResult{
		Changes: []LineChange{
			{Type: ChangeAddition, NewLineNum: 1, OldLineNum: 3, Content: "added"},
		},
		OldLineCount: 3,
		NewLineCount: 4,
		LineMapping: &LineMapping{
			NewToOld: []int{1, 2, 3, -1},
			OldToNew: []int{1, 2, 3},
		},
	}

	stage := &stageBuilder{
		startLine:  1,
		endLine:    1,
		rawChanges: diff.Changes,
	}

	baseLineOffset := 4

	computeStageRanges(stage, baseLineOffset, diff)

	// Stage buffer range should be at insertion point
	assert.True(t, stage.bufferStart == stage.bufferEnd, "pure additions have start == end")

	// Buffer lines for each change should match the stage range
	for _, change := range diff.Changes {
		bufLine := diff.LineMapping.GetBufferLine(change, baseLineOffset)
		assert.Equal(t, stage.bufferStart, bufLine,
			"buffer lines should match stage range for pure additions")
	}
}

// TestCreateStages_PureAdditionsMultipleBaseOffsets tests that pure additions
// work correctly with various base line offsets.
func TestCreateStages_PureAdditionsMultipleBaseOffsets(t *testing.T) {
	testCases := []struct {
		name           string
		baseLineOffset int
	}{
		{"at_line_1", 1},
		{"at_line_10", 10},
		{"at_line_100", 100},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			diff := &DiffResult{
				Changes: []LineChange{
					{Type: ChangeAddition, NewLineNum: 1, OldLineNum: 0, Content: "new line"},
				},
				OldLineCount: 1,
				NewLineCount: 1,
				LineMapping: &LineMapping{
					NewToOld: []int{-1},
					OldToNew: []int{1},
				},
			}

			newLines := []string{"new line"}
			oldLines := []string{""}

			result := CreateStages(&StagingParams{
				Diff:               diff,
				CursorRow:          1,
				CursorCol:          0,
				ViewportTop:        1,
				ViewportBottom:     200,
				BaseLineOffset:     tc.baseLineOffset,
				ProximityThreshold: 3,
				MaxLines:           0,
				FilePath:           "test.txt",
				NewLines:           newLines,
				OldLines:           oldLines,
			})

			assert.NotNil(t, result, "result should not be nil")
			assert.Equal(t, 1, len(result.Stages), "should have 1 stage")

			stage := result.Stages[0]

			// Verify all groups have consistent buffer_line
			for _, g := range stage.Groups {
				assert.Equal(t, stage.BufferStart, g.BufferLine,
					"group BufferLine should match stage BufferStart")
			}
		})
	}
}

// TestCreateStages_MixedAdditionsAndModifications verifies that when a stage
// contains both additions and modifications, only modifications use their
// original buffer positions while additions after modifications are positioned
// correctly.
func TestCreateStages_MixedAdditionsAndModifications(t *testing.T) {
	diff := &DiffResult{
		Changes: []LineChange{
			{Type: ChangeModification, NewLineNum: 1, OldLineNum: 1, Content: "modified", OldContent: "old"},
			{Type: ChangeAddition, NewLineNum: 2, OldLineNum: 1, Content: "added after"},
		},
		OldLineCount: 1,
		NewLineCount: 2,
		LineMapping: &LineMapping{
			NewToOld: []int{1, -1},
			OldToNew: []int{1},
		},
	}

	newLines := []string{"modified", "added after"}
	oldLines := []string{"old"}

	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          1,
		CursorCol:          0,
		ViewportTop:        1,
		ViewportBottom:     50,
		BaseLineOffset:     10,
		ProximityThreshold: 3,
		MaxLines:           0,
		FilePath:           "test.txt",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	assert.NotNil(t, result, "result should not be nil")
	assert.Equal(t, 1, len(result.Stages), "should have 1 stage")

	stage := result.Stages[0]

	// Stage should start at the modification's buffer position
	assert.Equal(t, 10, stage.BufferStart, "stage should start at modification position")

	// All groups should have valid buffer_line values within or after the stage
	for _, g := range stage.Groups {
		assert.True(t, g.BufferLine >= stage.BufferStart,
			"group BufferLine should be >= stage BufferStart")
	}
}

// TestCreateStages_CursorTargetPointsToEndOfNewContent verifies that the cursor target
// for the last stage points to the end of the NEW content, not the old buffer end.
// This is critical when a stage has modifications followed by additions that extend
// beyond the original buffer.
func TestCreateStages_CursorTargetPointsToEndOfNewContent(t *testing.T) {
	// Original buffer has 3 lines, new content has 10 lines
	// Modification on line 3, additions on lines 4-10
	diff := &DiffResult{
		Changes: []LineChange{
			{Type: ChangeAppendChars, NewLineNum: 3, OldLineNum: 3, Content: "line3 extended", OldContent: "line3", ColStart: 5, ColEnd: 14},
			{Type: ChangeAddition, NewLineNum: 4, OldLineNum: 3, Content: "added line 4"},
			{Type: ChangeAddition, NewLineNum: 5, OldLineNum: 3, Content: "added line 5"},
			{Type: ChangeAddition, NewLineNum: 6, OldLineNum: 3, Content: "added line 6"},
			{Type: ChangeAddition, NewLineNum: 7, OldLineNum: 3, Content: "added line 7"},
			{Type: ChangeAddition, NewLineNum: 8, OldLineNum: 3, Content: "added line 8"},
			{Type: ChangeAddition, NewLineNum: 9, OldLineNum: 3, Content: "added line 9"},
			{Type: ChangeAddition, NewLineNum: 10, OldLineNum: 3, Content: "added line 10"},
		},
		OldLineCount: 3,
		NewLineCount: 10,
		LineMapping: &LineMapping{
			NewToOld: []int{1, 2, 3, -1, -1, -1, -1, -1, -1, -1},
			OldToNew: []int{1, 2, 3},
		},
	}

	newLines := []string{"line1", "line2", "line3 extended", "added line 4", "added line 5", "added line 6", "added line 7", "added line 8", "added line 9", "added line 10"}
	oldLines := []string{"line1", "line2", "line3"}

	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          3,
		CursorCol:          0,
		ViewportTop:        1,
		ViewportBottom:     50,
		BaseLineOffset:     1,
		ProximityThreshold: 3,
		MaxLines:           0,
		FilePath:           "test.txt",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	assert.NotNil(t, result, "result should not be nil")
	assert.Equal(t, 1, len(result.Stages), "should have 1 stage")

	stage := result.Stages[0]
	assert.Equal(t, 3, stage.BufferStart, "stage should start at line 3")
	assert.Equal(t, 8, len(stage.Lines), "stage should have 8 lines (lines 3-10)")

	// The cursor target should point to the end of the NEW content (line 10),
	// not the old buffer end (line 3)
	expectedCursorTargetLine := stage.BufferStart + len(stage.Lines) - 1 // 3 + 8 - 1 = 10
	assert.Equal(t, int32(expectedCursorTargetLine), stage.CursorTarget.LineNumber,
		"cursor target should point to end of new content, not old buffer end")
}

// TestCreateStages_MaxVisibleLines tests that maxVisibleLines correctly splits stages.
// This reproduces the scenario from the bug where stage 2 has incorrect coordinates.
func TestCreateStages_MaxVisibleLines(t *testing.T) {
	// Scenario from logs:
	// Original: "import numpy as np", "", "def bubb" (3 lines)
	// New: adds bubble_sort function (modification + multiple additions)
	oldLines := []string{"import numpy as np", "", "def bubb"}
	newLines := []string{
		"import numpy as np",
		"",
		"def bubble_sort(arr):",
		"    n = len(arr)",
		"    for i in range(n):",
		"        for j in range(0, n-i-1):",
	}

	text1 := JoinLines(oldLines)
	text2 := JoinLines(newLines)
	diff := ComputeDiff(text1, text2)

	// maxVisibleLines=2 should split into stages
	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          3,
		CursorCol:          0,
		ViewportTop:        1,
		ViewportBottom:     50,
		BaseLineOffset:     1,
		ProximityThreshold: 3,
		MaxLines:           2,
		FilePath:           "test.py",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	assert.NotNil(t, result, "result should not be nil")
	assert.True(t, len(result.Stages) >= 2, "should have at least 2 stages with maxLines=2")

	// Stage 1: should contain the modification at line 3 + 1 addition (2 lines total)
	stage1 := result.Stages[0]
	t.Logf("Stage 1: BufferStart=%d, BufferEnd=%d, Lines=%d", stage1.BufferStart, stage1.BufferEnd, len(stage1.Lines))
	for i, g := range stage1.Groups {
		t.Logf("  Group %d: type=%s, BufferLine=%d, Lines=%v", i, g.Type, g.BufferLine, g.Lines)
	}

	// Stage 2: should contain pure additions (for loops)
	stage2 := result.Stages[1]
	t.Logf("Stage 2: BufferStart=%d, BufferEnd=%d, Lines=%d", stage2.BufferStart, stage2.BufferEnd, len(stage2.Lines))
	for i, g := range stage2.Groups {
		t.Logf("  Group %d: type=%s, BufferLine=%d, Lines=%v", i, g.Type, g.BufferLine, g.Lines)
	}

	// Stage 1 should start at buffer line 3 (where "def bubb" is)
	assert.Equal(t, 3, stage1.BufferStart, "stage 1 should start at buffer line 3")

	// Stage 2 contains pure additions that come AFTER the original content
	// Since original file has 3 lines, additions go after line 3
	// Stage 2's BufferStart should be 3 (anchored to last line where additions are inserted)
	// or 4 if counting as "after line 3"
	assert.True(t, stage2.BufferStart >= 3, "stage 2 BufferStart should be >= 3 (after original content)")

	// Verify groups have correct BufferLine values
	for _, g := range stage2.Groups {
		assert.True(t, g.BufferLine >= 3,
			fmt.Sprintf("stage 2 group BufferLine should be >= 3, got %d", g.BufferLine))
	}
}

// TestModificationBufferLineWithPrecedingAdditions verifies that when additions
// precede a modification in the new content, the modification's BufferLine
// correctly points to the original buffer line being modified, not an offset
// based on its relative position in the new content.
func TestModificationBufferLineWithPrecedingAdditions(t *testing.T) {
	// Scenario: Buffer lines 5-6 contain whitespace, completion replaces with 4 lines
	// where the first 2 are additions and line 3 is a modification of original line 6.
	//
	// Old (buffer lines 5-6):
	//   Line 5: "    " (whitespace)
	//   Line 6: "         " (whitespace that gets modified)
	//
	// New (4 lines):
	//   Line 1: "    Parameters" (addition)
	//   Line 2: "    ----------" (addition)
	//   Line 3: "    rA : array" (modification of old line 6)
	//   Line 4: "        coord" (addition)
	//
	// The modification at relative line 3 should have BufferLine=6 (where the
	// original "         " was), NOT BufferLine=7 (which would be wrong).

	oldLines := []string{
		"    ",      // buffer line 5 (will be deleted/replaced)
		"         ", // buffer line 6 (will be modified)
	}
	newLines := []string{
		"    Parameters",
		"    ----------",
		"    rA : array",
		"        coord",
	}

	text1 := JoinLines(oldLines)
	text2 := JoinLines(newLines)
	diff := ComputeDiff(text1, text2)

	// baseLineOffset=5 means the diff starts at buffer line 5
	baseLineOffset := 5
	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          5,
		CursorCol:          0,
		ViewportTop:        1,
		ViewportBottom:     50,
		BaseLineOffset:     baseLineOffset,
		ProximityThreshold: 10,
		MaxLines:           0,
		FilePath:           "test.py",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	assert.NotNil(t, result, "result should not be nil")
	assert.Equal(t, 1, len(result.Stages), "should have 1 stage")

	stage := result.Stages[0]

	// Find the modification group
	var modificationGroup *Group
	for _, g := range stage.Groups {
		if g.Type == "modification" {
			modificationGroup = g
			break
		}
	}

	assert.NotNil(t, modificationGroup, "should have a modification group")

	// The modification maps to old line 1 ("    " → "    Parameters"), BufferLine = 5
	assert.Equal(t, 5, modificationGroup.BufferLine,
		"modification BufferLine should be 5 (old line 1 + baseLineOffset - 1)")
}

// TestModificationBufferLineMatchesOldPosition verifies that when a modification
// replaces old content, its BufferLine points to the original buffer position
// of that content, not an offset based on new content position.
func TestModificationBufferLineMatchesOldPosition(t *testing.T) {
	// Old content at buffer lines 10-11
	// New content expands to 4 lines with additions before the modification
	oldLines := []string{
		"old line 1",
		"old line 2",
	}
	newLines := []string{
		"new line 1",
		"new line 2",
		"new line 3",
		"new line 4",
	}

	text1 := JoinLines(oldLines)
	text2 := JoinLines(newLines)
	diff := ComputeDiff(text1, text2)

	baseLineOffset := 10
	cursorRow := 11
	cursorCol := 0
	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          cursorRow,
		CursorCol:          cursorCol,
		ViewportTop:        1,
		ViewportBottom:     50,
		BaseLineOffset:     baseLineOffset,
		ProximityThreshold: 10,
		MaxLines:           0,
		FilePath:           "test.go",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	assert.NotNil(t, result, "result should not be nil")
	assert.Equal(t, 1, len(result.Stages), "should have 1 stage")

	stage := result.Stages[0]

	// Find modification groups and verify their BufferLine matches the old position
	for _, g := range stage.Groups {
		if g.Type == "modification" && len(g.OldLines) > 0 {
			// Modification of old line 1 should have BufferLine=10
			if g.OldLines[0] == "old line 1" {
				assert.Equal(t, 10, g.BufferLine,
					"modification of old line 1 should have BufferLine=10")
			}
			// Modification of old line 2 should have BufferLine=11
			if g.OldLines[0] == "old line 2" {
				assert.Equal(t, 11, g.BufferLine,
					"modification of old line 2 should have BufferLine=11")
			}
		}
	}
}

// TestAdditionsBeforeCursorLineAnchoredAtCursor verifies that additions
// preceding the cursor line's modification are anchored at the cursor line,
// so they render directly above the cursor.
func TestAdditionsBeforeCursorLineAnchoredAtCursor(t *testing.T) {
	// Old content: 2 lines at buffer positions 5-6
	// New content: 4 lines with additions inserted before cursor line modification
	oldLines := []string{
		"first",
		"cursor line content",
	}
	newLines := []string{
		"first modified",
		"added line 1",
		"added line 2",
		"cursor line replaced",
	}

	text1 := JoinLines(oldLines)
	text2 := JoinLines(newLines)
	diff := ComputeDiff(text1, text2)

	baseLineOffset := 5
	cursorRow := 6 // cursor is on buffer line 6
	cursorCol := 0
	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          cursorRow,
		CursorCol:          cursorCol,
		ViewportTop:        1,
		ViewportBottom:     50,
		BaseLineOffset:     baseLineOffset,
		ProximityThreshold: 10,
		MaxLines:           0,
		FilePath:           "test.go",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	assert.NotNil(t, result, "result should not be nil")
	assert.Equal(t, 1, len(result.Stages), "should have 1 stage")

	stage := result.Stages[0]

	// Find the cursor line modification (old content "cursor line content")
	var cursorMod *Group
	for _, g := range stage.Groups {
		if g.Type == "modification" && len(g.OldLines) > 0 && g.OldLines[0] == "cursor line content" {
			cursorMod = g
			break
		}
	}

	assert.NotNil(t, cursorMod, "should have modification for cursor line")
	assert.Equal(t, 6, cursorMod.BufferLine, "cursor line modification should have BufferLine=6")

	// Find addition groups that precede the cursor line modification
	for _, g := range stage.Groups {
		if g.Type == "addition" && g.StartLine < cursorMod.StartLine {
			// Additions before cursor modification should be anchored at cursor line
			assert.Equal(t, 6, g.BufferLine,
				"additions before cursor line should be anchored at cursor line (6)")
		}
	}
}

// TestModificationBufferLineUsesOldLinePosition verifies that modification groups
// have BufferLine computed from their old line position, not their relative position
// in the new content.
func TestModificationBufferLineUsesOldLinePosition(t *testing.T) {
	// Old content: 2 lines at buffer positions 5-6
	// New content: 4 lines where:
	// - Lines 1-2 are additions (no old correspondence)
	// - Line 3 modifies old line 2 (buffer line 6)
	// - Line 4 is an addition
	oldLines := []string{
		"first line",
		"second line",
	}
	newLines := []string{
		"new addition 1",
		"new addition 2",
		"second line modified",
		"new addition 3",
	}

	text1 := JoinLines(oldLines)
	text2 := JoinLines(newLines)
	diff := ComputeDiff(text1, text2)

	baseLineOffset := 5
	cursorRow := 6 // cursor on buffer line 6 (old "second line")
	cursorCol := 0
	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          cursorRow,
		CursorCol:          cursorCol,
		ViewportTop:        1,
		ViewportBottom:     50,
		BaseLineOffset:     baseLineOffset,
		ProximityThreshold: 10,
		MaxLines:           0,
		FilePath:           "test.go",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	assert.NotNil(t, result, "result should not be nil")
	assert.Equal(t, 1, len(result.Stages), "should have 1 stage")

	stage := result.Stages[0]

	// Find the modification group for "second line" (the one at old line 2, buffer 6)
	var modGroup *Group
	for _, g := range stage.Groups {
		if g.Type == "modification" && len(g.OldLines) > 0 && g.OldLines[0] == "second line" {
			modGroup = g
			break
		}
	}

	assert.NotNil(t, modGroup, "should have a modification group for 'second line'")
	// Modification of old line 2 (buffer line 6) should have BufferLine=6
	// Even though it's at relative position 3 in the new content
	assert.Equal(t, 6, modGroup.BufferLine,
		"modification BufferLine should match old line position (6), not relative position")
}

// TestCreateStages_TrailingWhitespaceModification verifies non-streaming staging
// handles trailing whitespace correctly when the old line has trailing spaces
// and the new line is a completion of that content.
func TestCreateStages_TrailingWhitespaceModification(t *testing.T) {
	oldLines := []string{
		"func main() {",
		"    x  ", // trailing whitespace
	}
	newLines := []string{
		"func main() {",
		"    x = getValue()",      // modification of "    x  "
		"    y = processValue(x)", // addition
	}

	oldText := JoinLines(oldLines)
	newText := JoinLines(newLines)
	diff := ComputeDiff(oldText, newText)

	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          2,
		CursorCol:          5,
		ViewportTop:        0,
		ViewportBottom:     0,
		BaseLineOffset:     1,
		ProximityThreshold: 10,
		MaxLines:           0,
		FilePath:           "test.go",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	assert.NotNil(t, result, "expected staging result")
	assert.Equal(t, 1, len(result.Stages), "stage count")

	stage := result.Stages[0]

	// BufferStart should be 2 (where the modification is)
	assert.Equal(t, 2, stage.BufferStart, "BufferStart")

	// The modification group should have BufferLine = 2
	var modGroup *Group
	for _, g := range stage.Groups {
		if g.Type == "modification" {
			modGroup = g
			break
		}
	}
	assert.NotNil(t, modGroup, "should have a modification group")
	assert.Equal(t, 2, modGroup.BufferLine, "modification BufferLine")

	// The addition group should have BufferLine = 3 (below the modification)
	var addGroup *Group
	for _, g := range stage.Groups {
		if g.Type == "addition" {
			addGroup = g
			break
		}
	}
	assert.NotNil(t, addGroup, "should have an addition group")
	assert.Equal(t, 3, addGroup.BufferLine, "addition BufferLine")
}

// TestIfElseSimplificationClosingBraceInSameStage verifies that when an if/else
// block is simplified to a single line, all 5 lines (if, body1, else, body2, })
// end up in the same stage. Reproduces a bug from cursortab.3.log where the
// closing `}` was split into a separate stage.
func TestIfElseSimplificationClosingBraceInSameStage(t *testing.T) {
	oldLines := []string{
		"  });",
		"",
		"  return true;",
		"}",
		"",
		"export function addTag(id: string, tag: string): boolean {",
		"  const article = store[id];",
		"  if (!article) {",
		"    return false;",
		"  }",
		"",
		"  if (article.tags.length === 0) {",
		"    article.tags.push(tag);",
		"  } else {",
		"    article.tags.push(tag);",
		"  }",
		"",
		"  inc(\"tag_added\");",
		"",
		"  return true;",
		"}",
		"",
		"export function hasTags(id: string): boolean {",
		"  const article = store[id];",
		"  if (!article) {",
		"    return false;",
		"  }",
		"",
		"  if (article.tags === null) {",
	}

	newLines := []string{
		"  });",
		"",
		"  return true;",
		"}",
		"",
		"export function addTag(id: string, tag: string): boolean {",
		"  const article = store[id];",
		"  if (!article) {",
		"    return false;",
		"  }",
		"",
		"  article.tags.push(tag);",
		"",
		"  inc(\"tag_added\");",
		"",
		"  return true;",
		"}",
		"",
		"export function hasTags(id: string): boolean {",
		"  const article = store[id];",
		"  if (!article) {",
		"    return false;",
		"  }",
		"",
		"  if (article.tags.length === 0) {",
	}

	oldText := JoinLines(oldLines)
	newText := JoinLines(newLines)
	diff := ComputeDiff(oldText, newText)

	// BaseLineOffset=64 to match the real scenario (editable starts at file line 64)
	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          78, // cursor at the push line
		CursorCol:          0,
		ViewportTop:        0,
		ViewportBottom:     0,
		BaseLineOffset:     64,
		ProximityThreshold: 2,
		MaxLines:           12,
		FilePath:           "tmp/test.ts",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	assert.NotNil(t, result, "expected staging result")

	// Log all stages and their groups
	for i, stage := range result.Stages {
		t.Logf("Stage %d: BufferStart=%d, BufferEnd=%d, Lines=%d",
			i, stage.BufferStart, stage.BufferEnd, len(stage.Lines))
		for j, g := range stage.Groups {
			t.Logf("  Group %d: type=%s, BufferLine=%d, Lines=%v, OldLines=%v",
				j, g.Type, g.BufferLine, g.Lines, g.OldLines)
		}
	}

	// The if/else removal (buffer lines 75-79) should be in ONE stage, not split
	firstStage := result.Stages[0]

	// The closing brace `}` at buffer line 79 should be in the first stage
	hasClosingBrace := false
	for _, g := range firstStage.Groups {
		for _, line := range g.Lines {
			if line == "  }" {
				hasClosingBrace = true
			}
		}
	}
	assert.True(t, hasClosingBrace,
		"closing brace should be in the first stage, not split into a separate stage")

	// BufferEnd should cover line 79 (the closing brace)
	assert.True(t, firstStage.BufferEnd >= 79,
		fmt.Sprintf("first stage BufferEnd should be >= 79 (got %d)", firstStage.BufferEnd))
}

// TestIfElseSimplificationViewportSplit verifies that viewport boundaries don't
// split logically connected changes. When the viewport bottom is at line 78,
// the closing `}` at buffer line 79 falls outside the viewport and ends up in
// a separate outView stage.
func TestIfElseSimplificationViewportSplit(t *testing.T) {
	oldLines := []string{
		"  });",
		"",
		"  return true;",
		"}",
		"",
		"export function addTag(id: string, tag: string): boolean {",
		"  const article = store[id];",
		"  if (!article) {",
		"    return false;",
		"  }",
		"",
		"  if (article.tags.length === 0) {",
		"    article.tags.push(tag);",
		"  } else {",
		"    article.tags.push(tag);",
		"  }",
		"",
		"  inc(\"tag_added\");",
		"",
		"  return true;",
		"}",
		"",
		"export function hasTags(id: string): boolean {",
		"  const article = store[id];",
		"  if (!article) {",
		"    return false;",
		"  }",
		"",
		"  if (article.tags === null) {",
	}

	newLines := []string{
		"  });",
		"",
		"  return true;",
		"}",
		"",
		"export function addTag(id: string, tag: string): boolean {",
		"  const article = store[id];",
		"  if (!article) {",
		"    return false;",
		"  }",
		"",
		"  article.tags.push(tag);",
		"",
		"  inc(\"tag_added\");",
		"",
		"  return true;",
		"}",
		"",
		"export function hasTags(id: string): boolean {",
		"  const article = store[id];",
		"  if (!article) {",
		"    return false;",
		"  }",
		"",
		"  if (article.tags.length === 0) {",
	}

	oldText := JoinLines(oldLines)
	newText := JoinLines(newLines)
	diff := ComputeDiff(oldText, newText)

	// Viewport bottom at 78 — the closing `}` at buffer 79 falls just outside
	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          78,
		CursorCol:          0,
		ViewportTop:        50,
		ViewportBottom:     78,
		BaseLineOffset:     64,
		ProximityThreshold: 2,
		MaxLines:           12,
		FilePath:           "tmp/test.ts",
		NewLines:           newLines,
		OldLines:           oldLines,
	})

	assert.NotNil(t, result, "expected staging result")

	for i, stage := range result.Stages {
		t.Logf("Stage %d: BufferStart=%d, BufferEnd=%d, Lines=%d",
			i, stage.BufferStart, stage.BufferEnd, len(stage.Lines))
		for j, g := range stage.Groups {
			t.Logf("  Group %d: type=%s, BufferLine=%d, Lines=%v",
				j, g.Type, g.BufferLine, g.Lines)
		}
	}

	// The closing brace should be in the first stage, not split out
	firstStage := result.Stages[0]
	hasClosingBrace := false
	for _, g := range firstStage.Groups {
		for _, line := range g.Lines {
			if line == "  }" {
				hasClosingBrace = true
			}
		}
	}
	assert.True(t, hasClosingBrace,
		"closing brace should be in the first stage, not split by viewport boundary")
	assert.True(t, firstStage.BufferEnd >= 79,
		fmt.Sprintf("first stage BufferEnd should be >= 79 (got %d)", firstStage.BufferEnd))
}

// applyStage simulates nvim_buf_set_lines applying a stage's replacement to
// the original buffer. Returns the resulting buffer.
func applyStage(oldLines []string, stage *Stage) []string {
	out := make([]string, 0, len(oldLines))
	out = append(out, oldLines[:stage.BufferStart-1]...)
	out = append(out, stage.Lines...)
	if stage.BufferEnd < len(oldLines) {
		out = append(out, oldLines[stage.BufferEnd:]...)
	}
	return out
}

// TestCreateStages_AnchorlessAdditionAboveModification verifies that an
// anchorless addition (insertion at the top of the file) grouped with a
// modification below produces a stage whose BufferStart..BufferEnd correctly
// covers the insertion point. Without the fix, the mixed-branch range
// computation only considers anchored additions; the resulting buffer range
// fails to include the insertion point and applying the stage duplicates
// every line between the insertion point and the modification.
func TestCreateStages_AnchorlessAdditionAboveModification(t *testing.T) {
	oldLines := []string{
		"package main",
		"import \"fmt\"",
		"func hello() { print() }",
	}
	newLines := []string{
		"// Copyright 2024",
		"package main",
		"import \"fmt\"",
		"func hello() { fmt.Println() }",
	}

	diff := ComputeDiff(JoinLines(oldLines), JoinLines(newLines))
	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          3,
		CursorCol:          0,
		ViewportTop:        1,
		ViewportBottom:     50,
		BaseLineOffset:     1,
		ProximityThreshold: 3,
		NewLines:           newLines,
		OldLines:           oldLines,
		FilePath:           "test.go",
	})

	assert.NotNil(t, result, "result")
	assert.Len(t, 1, result.Stages, "should produce a single stage")

	got := applyStage(oldLines, result.Stages[0])
	assert.Equal(t, newLines, got, "applying stage should reproduce new buffer")
}

// TestCreateStages_AnchorlessAdditionBelowModification covers the symmetric
// case: a modification near the top and an anchorless addition past the end.
// Without the fix, BufferEnd does not extend to cover the unchanged old lines
// that map to the new lines bundled into Stage.Lines, so applying the stage
// leaves the original middle lines duplicated below the new content.
func TestCreateStages_AnchorlessAdditionBelowModification(t *testing.T) {
	oldLines := []string{
		"a",
		"b",
		"c",
	}
	newLines := []string{
		"X",
		"b",
		"c",
		"Y",
	}

	diff := ComputeDiff(JoinLines(oldLines), JoinLines(newLines))
	result := CreateStages(&StagingParams{
		Diff:               diff,
		CursorRow:          1,
		CursorCol:          0,
		ViewportTop:        1,
		ViewportBottom:     50,
		BaseLineOffset:     1,
		ProximityThreshold: 5,
		NewLines:           newLines,
		OldLines:           oldLines,
		FilePath:           "test.go",
	})

	assert.NotNil(t, result, "result")
	assert.Len(t, 1, result.Stages, "should produce a single stage")

	got := applyStage(oldLines, result.Stages[0])
	assert.Equal(t, newLines, got, "applying stage should reproduce new buffer")
}
