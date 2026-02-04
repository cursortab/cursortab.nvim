package text

import (
	"cursortab/assert"
	"fmt"
	"testing"
)

func TestIncrementalDiffBuilder_BasicModification(t *testing.T) {
	oldLines := []string{"hello world", "foo bar", "baz qux"}
	builder := NewIncrementalDiffBuilder(oldLines)

	// Add lines that match/modify the old lines
	change1 := builder.AddLine("hello world") // exact match
	assert.Nil(t, change1, "expected no change for exact match")

	change2 := builder.AddLine("foo baz") // modification
	assert.NotNil(t, change2, "expected change for modification")
	assert.True(t, change2.Type == ChangeReplaceChars || change2.Type == ChangeModification, "expected modification type")

	change3 := builder.AddLine("baz qux") // exact match
	assert.Nil(t, change3, "expected no change for exact match")

	// Verify final state
	assert.Equal(t, 1, len(builder.Changes), "change count")
}

func TestIncrementalDiffBuilder_Addition(t *testing.T) {
	// During streaming, incremental matching uses similarity to find matches.
	// Lines that don't match are recorded as additions. The actual change
	// types are determined at stage finalization using batch diff.
	oldLines := []string{"line 1", "line 2"}
	builder := NewIncrementalDiffBuilder(oldLines)

	builder.AddLine("line 1")   // match
	builder.AddLine("new line") // no match found during streaming -> addition
	builder.AddLine("line 2")   // matches old "line 2"

	// During streaming: 1 addition ("new line")
	// Note: actual change types are refined at stage finalization
	assert.Equal(t, 1, len(builder.Changes), "change count")

	// Verify the addition
	change2, ok := builder.Changes[2]
	assert.True(t, ok, "should have change at line 2")
	assert.Equal(t, ChangeAddition, change2.Type, "expected addition during streaming")
	assert.Equal(t, "new line", change2.Content, "added content")
}

func TestIncrementalDiffBuilder_MultipleAdditions(t *testing.T) {
	// During streaming, lines that don't match are recorded as additions.
	// The actual change types are determined at stage finalization using batch diff.
	oldLines := []string{"a", "b"}
	builder := NewIncrementalDiffBuilder(oldLines)

	builder.AddLine("a") // match
	builder.AddLine("x") // no match -> addition during streaming
	builder.AddLine("y") // no match -> addition during streaming
	builder.AddLine("b") // matches old "b"

	// During streaming: 2 additions ("x", "y")
	assert.Equal(t, 2, len(builder.Changes), "change count")
}

func TestIncrementalStageBuilder_SingleStage(t *testing.T) {
	oldLines := []string{"line 1", "line 2", "line 3"}
	builder := NewIncrementalStageBuilder(
		oldLines,
		1, // baseLineOffset
		3, // proximityThreshold
		0, // maxVisibleLines (disabled)
		0, // viewportTop (disabled)
		0, // viewportBottom (disabled)
		1, // cursorRow
		0, // cursorCol
		"test.go",
	)

	// Add modified lines that should all be in the same stage
	builder.AddLine("line 1 modified") // modification
	builder.AddLine("line 2 modified") // modification
	builder.AddLine("line 3")          // match

	result := builder.Finalize()
	assert.NotNil(t, result, "staging result")

	assert.Equal(t, 1, len(result.Stages), "stage count")

	stage := result.Stages[0]
	assert.Equal(t, 2, len(stage.Changes), "changes in stage")
}

func TestIncrementalStageBuilder_MultipleStages(t *testing.T) {
	oldLines := []string{
		"line 1",
		"line 2",
		"line 3",
		"line 4",
		"line 5",
		"line 6",
		"line 7",
		"line 8",
		"line 9",
		"line 10",
	}
	builder := NewIncrementalStageBuilder(
		oldLines,
		1, // baseLineOffset
		2, // proximityThreshold (small to force multiple stages)
		0, // maxVisibleLines (disabled)
		0, // viewportTop
		0, // viewportBottom
		1, // cursorRow
		0, // cursorCol
		"test.go",
	)

	// Add lines with gaps > proximityThreshold to create multiple stages
	builder.AddLine("line 1 modified") // modification at line 1
	builder.AddLine("line 2")          // match
	builder.AddLine("line 3")          // match
	builder.AddLine("line 4")          // match
	builder.AddLine("line 5")          // match
	builder.AddLine("line 6 modified") // modification at line 6 (gap > 2)
	builder.AddLine("line 7")          // match
	builder.AddLine("line 8")          // match
	builder.AddLine("line 9")          // match
	builder.AddLine("line 10")         // match

	result := builder.Finalize()
	assert.NotNil(t, result, "staging result")

	assert.Equal(t, 2, len(result.Stages), "stage count")
}

func TestIncrementalStageBuilder_StageFinalizationOnGap(t *testing.T) {
	oldLines := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	builder := NewIncrementalStageBuilder(
		oldLines,
		1,    // baseLineOffset
		2,    // proximityThreshold
		0,    // maxVisibleLines (disabled)
		0, 0, // viewport disabled
		1, 0, // cursorRow, cursorCol
		"test.go",
	)

	// Line 1: modification, starts a stage
	finalized := builder.AddLine("a modified")
	assert.Nil(t, finalized, "should not finalize on first change")

	// Lines 2-3: matches (gap building up but not exceeding threshold)
	finalized = builder.AddLine("b") // gap = 1, threshold = 2
	assert.Nil(t, finalized, "should not finalize when gap <= threshold")
	finalized = builder.AddLine("c") // gap = 2, still not > threshold
	assert.Nil(t, finalized, "should not finalize when gap == threshold")

	// Line 4: match, but gap now exceeds threshold (gap = 3 > 2)
	// Stage should finalize even without a new change
	finalized = builder.AddLine("d")
	assert.NotNil(t, finalized, "should finalize stage when gap exceeds threshold")
	assert.Equal(t, 1, len(finalized.Changes), "finalized stage should have 1 change")

	// Line 5: modification starts a new stage
	finalized = builder.AddLine("e modified")
	assert.Nil(t, finalized, "should not finalize on first change of new stage")
}

func TestIncrementalDiffBuilder_SimilarityMatching(t *testing.T) {
	oldLines := []string{
		"func hello() {",
		"    return world",
		"}",
	}
	builder := NewIncrementalDiffBuilder(oldLines)

	// Modified version with similar structure
	change1 := builder.AddLine("func hello() {") // exact match
	assert.Nil(t, change1, "expected exact match")

	change2 := builder.AddLine("    return world + 1") // modification
	assert.NotNil(t, change2, "expected modification")
	assert.Equal(t, 2, change2.OldLineNum, "old line number")

	change3 := builder.AddLine("}") // exact match
	assert.Nil(t, change3, "expected exact match for closing brace")
}

func TestIncrementalStageBuilder_ViewportBoundary(t *testing.T) {
	// Use more distinct line content to avoid similarity matching issues
	oldLines := []string{
		"line one",
		"line two",
		"line three",
		"line four",
		"line five",
		"line six",
		"line seven",
		"line eight",
		"line nine",
		"line ten",
	}
	builder := NewIncrementalStageBuilder(
		oldLines,
		1,  // baseLineOffset
		10, // proximityThreshold (high to prevent gap-based splits)
		0,  // maxVisibleLines (disabled)
		1,  // viewportTop
		5,  // viewportBottom (first 5 lines visible)
		3,  // cursorRow
		0,  // cursorCol
		"test.go",
	)

	// Modifications in viewport (lines 1-5)
	builder.AddLine("line one modified") // in viewport, buffer line = 1
	builder.AddLine("line two")
	builder.AddLine("line three")
	builder.AddLine("line four")
	builder.AddLine("line five modified") // still in viewport, buffer line = 5

	// Add remaining lines to complete the sequence
	builder.AddLine("line six modified") // outside viewport, buffer line = 6
	builder.AddLine("line seven")
	builder.AddLine("line eight")
	builder.AddLine("line nine")
	builder.AddLine("line ten")

	// Finalize and check we got multiple stages
	result := builder.Finalize()
	assert.NotNil(t, result, "expected staging result")

	// Should have 2 stages: one for viewport changes (1, 5), one for outside (6)
	if len(result.Stages) < 2 {
		for i, stage := range result.Stages {
			t.Logf("Stage %d: BufferStart=%d, BufferEnd=%d, changes=%d",
				i, stage.BufferStart, stage.BufferEnd, len(stage.Changes))
		}
	}
	assert.GreaterOrEqual(t, len(result.Stages), 2, "expected at least 2 stages due to viewport boundary")
}

func TestIncrementalDiffBuilder_EmptyOldLines(t *testing.T) {
	builder := NewIncrementalDiffBuilder([]string{})

	change := builder.AddLine("new content")
	assert.NotNil(t, change, "expected addition change")
	assert.Equal(t, ChangeAddition, change.Type, "change type")
}

// TestIncrementalStageBuilder_BaseLineOffset verifies that BufferStart/BufferEnd
// are correctly offset when the provider trims content (baseLineOffset > 1).
// This simulates when the model only sees a window of the file, not the full file.
func TestIncrementalStageBuilder_BaseLineOffset(t *testing.T) {
	// Simulate a trimmed window: model sees lines 20-25 of original file
	// oldLines here represents the TRIMMED content (what model sees)
	oldLines := []string{
		"  if (article.tags === null) {",  // buffer line 20
		"    article.tags = tag;",         // buffer line 21
		"  } else {",                      // buffer line 22
		"    article.tags = concat(tag);", // buffer line 23
		"  }",                             // buffer line 24
	}

	baseLineOffset := 20 // Window starts at buffer line 20

	builder := NewIncrementalStageBuilder(
		oldLines,
		baseLineOffset,
		3,      // proximityThreshold
		0,      // maxVisibleLines (disabled)
		15, 30, // viewport (lines 15-30 visible)
		22, 0,  // cursorRow, cursorCol
		"test.ts",
	)

	// Model outputs modified content
	builder.AddLine("  if (article.tags === null) {") // match
	builder.AddLine("    article.tags = [tag];")      // modification
	builder.AddLine("  } else {")                     // match
	builder.AddLine("    article.tags.push(tag);")    // modification
	builder.AddLine("  }")                            // match

	result := builder.Finalize()
	assert.NotNil(t, result, "expected staging result")

	assert.Equal(t, 1, len(result.Stages), "stage count")

	stage := result.Stages[0]

	// BufferStart should be 21 (baseLineOffset + 1 for the second line where change is)
	// BufferEnd should be 24 (baseLineOffset + 3 for line 4 where last change is)
	// The key test: these should NOT be 1-5, they should be offset by baseLineOffset
	assert.GreaterOrEqual(t, stage.BufferStart, baseLineOffset, "BufferStart >= baseLineOffset")
	assert.GreaterOrEqual(t, stage.BufferEnd, baseLineOffset, "BufferEnd >= baseLineOffset")

	// More specific check: changes are on lines 2 and 4 of input (1-indexed)
	// So BufferStart should be baseLineOffset + 1 = 21
	// And BufferEnd should be baseLineOffset + 3 = 23 (line 4 of 5)
	expectedStart := 21 // Line 2 of trimmed = buffer line 21
	expectedEnd := 23   // Line 4 of trimmed = buffer line 23

	assert.Equal(t, expectedStart, stage.BufferStart, "BufferStart")
	assert.Equal(t, expectedEnd, stage.BufferEnd, "BufferEnd")

	// Verify changes exist
	assert.Equal(t, 2, len(stage.Changes), "change count")
}

// TestIncrementalStageBuilder_BaseLineOffsetWithGap tests that gap detection
// works correctly when baseLineOffset > 1 and stages finalize mid-stream.
func TestIncrementalStageBuilder_BaseLineOffsetWithGap(t *testing.T) {
	// Simulate window starting at line 50
	oldLines := []string{
		"line A", // buffer line 50
		"line B", // buffer line 51
		"line C", // buffer line 52
		"line D", // buffer line 53
		"line E", // buffer line 54
		"line F", // buffer line 55
		"line G", // buffer line 56
		"line H", // buffer line 57
	}

	baseLineOffset := 50

	builder := NewIncrementalStageBuilder(
		oldLines,
		baseLineOffset,
		2,      // proximityThreshold
		0,      // maxVisibleLines (disabled)
		40, 60, // viewport
		52, 0,  // cursorRow, cursorCol
		"test.go",
	)

	// Change on line 1 (buffer line 50)
	finalized := builder.AddLine("line A modified")
	assert.Nil(t, finalized, "should not finalize on first change")

	// Lines 2-4: no changes, building gap
	builder.AddLine("line B") // gap = 1
	builder.AddLine("line C") // gap = 2

	// Line 4: gap > threshold, should finalize
	finalized = builder.AddLine("line D") // gap = 3 > 2
	assert.NotNil(t, finalized, "expected stage to finalize on gap")

	// Check the finalized stage has correct buffer positions
	assert.Equal(t, 50, finalized.BufferStart, "finalized stage BufferStart")
	assert.Equal(t, 50, finalized.BufferEnd, "finalized stage BufferEnd")
}

// TestIncrementalStageBuilder_GapDetectionWithSimilarityMatching verifies that when
// similarity matching maps model output to scattered buffer positions, gap detection
// still groups changes appropriately based on buffer line proximity.
func TestIncrementalStageBuilder_GapDetectionWithSimilarityMatching(t *testing.T) {
	// Simulate a scenario where similarity matching maps new lines to scattered old lines.
	// Old lines represent different functions in a file.
	oldLines := []string{
		"function foo() {", // line 1 (buffer 52)
		"  const x = 1;",   // line 2 (buffer 53)
		"  return x;",      // line 3 (buffer 54)
		"}",                // line 4 (buffer 55)
		"",                 // line 5 (buffer 56)
		"function bar() {", // line 6 (buffer 57)
		"  const y = 2;",   // line 7 (buffer 58)
		"  return y;",      // line 8 (buffer 59)
		"}",                // line 9 (buffer 60)
		"",                 // line 10 (buffer 61)
		"function baz() {", // line 11 (buffer 62)
		"  const z = 3;",   // line 12 (buffer 63)
		"  return z;",      // line 13 (buffer 64)
		"}",                // line 14 (buffer 65)
	}

	baseLineOffset := 52

	builder := NewIncrementalStageBuilder(
		oldLines,
		baseLineOffset,
		3,      // proximityThreshold - gaps > 3 should split stages
		0,      // maxVisibleLines (disabled)
		40, 80, // viewport
		58, 0,  // cursorRow, cursorCol
		"test.ts",
	)

	// Model outputs content where:
	// - Line 1 matches old line 1 (exact)
	// - Line 2 is a MODIFICATION of old line 2 -> buffer 53
	// - Line 3 matches old line 3 (exact)
	// - Line 4 matches old line 4 (exact)
	// - Line 5 matches old line 5 (exact)
	// - Line 6 matches old line 6 (exact)
	// - Line 7 is a MODIFICATION of old line 7 -> buffer 58 (buffer gap = 5!)
	// - Line 8 matches old line 8 (exact)
	// - etc.

	builder.AddLine("function foo() {") // match line 1
	builder.AddLine("  const x = 100;") // MODIFY line 2 -> buffer 53
	builder.AddLine("  return x;")      // match line 3
	builder.AddLine("}")                // match line 4
	builder.AddLine("")                 // match line 5
	builder.AddLine("function bar() {") // match line 6
	builder.AddLine("  const y = 200;") // MODIFY line 7 -> buffer 58 (buffer gap = 5!)
	builder.AddLine("  return y;")      // match line 8
	builder.AddLine("}")                // match line 9
	builder.AddLine("")                 // match line 10
	builder.AddLine("function baz() {") // match line 11
	builder.AddLine("  const z = 300;") // MODIFY line 12 -> buffer 63 (buffer gap = 5!)
	builder.AddLine("  return z;")      // match line 13
	builder.AddLine("}")                // match line 14

	result := builder.Finalize()
	assert.NotNil(t, result, "expected staging result")

	// With proximityThreshold=3 and buffer gaps of 5 between functions,
	// changes should be split into separate stages
	assert.True(t, len(result.Stages) >= 3, "expected at least 3 stages")

	// Verify each stage has changes
	for _, stage := range result.Stages {
		assert.True(t, len(stage.Changes) > 0, "stage should have changes")
	}
}

// TestIncrementalStageBuilder_SimilarityMatchingToSimilarLines verifies behavior when
// model output contains lines similar to multiple locations in the original file.
func TestIncrementalStageBuilder_SimilarityMatchingToSimilarLines(t *testing.T) {
	// Simulate model outputting content from wrong function.
	// Old lines have similar patterns in different locations.
	oldLines := []string{
		"  article.title = title;",   // line 1 (buffer 20) - in setTitle()
		"  article.author = author;", // line 2 (buffer 21)
		"  return true;",             // line 3 (buffer 22)
		"}",                          // line 4 (buffer 23)
		"",                           // line 5 (buffer 24)
		"function updateTags() {",    // line 6 (buffer 25)
		"  article.tags = tags;",     // line 7 (buffer 26) - similar to line 1!
		"  article.count = count;",   // line 8 (buffer 27) - similar to line 2!
		"  return true;",             // line 9 (buffer 28)
		"}",                          // line 10 (buffer 29)
	}

	baseLineOffset := 20

	builder := NewIncrementalStageBuilder(
		oldLines,
		baseLineOffset,
		3,      // proximityThreshold
		0,      // maxVisibleLines (disabled)
		15, 35, // viewport
		23, 0,  // cursorRow, cursorCol
		"test.ts",
	)

	// Model outputs lines that SHOULD modify lines 1-3 (setTitle function)
	// but similarity matching might find matches at lines 7-9 (updateTags function)
	// if the content is similar enough.

	// Intentionally use content that's similar to BOTH locations
	builder.AddLine("  article.name = name;")   // Similar to line 1 AND line 7
	builder.AddLine("  article.value = value;") // Similar to line 2 AND line 8
	builder.AddLine("  return false;")          // Similar to line 3 AND line 9

	result := builder.Finalize()
	assert.NotNil(t, result, "expected staging result")

	// Check that changes are coherent (all in one location, not scattered)
	if len(result.Stages) == 1 {
		stage := result.Stages[0]
		bufferRange := stage.BufferEnd - stage.BufferStart
		// If all changes are coherent, range should be small (2-3 lines)
		// If scattered, range could be large (6+ lines spanning two functions)
		assert.True(t, bufferRange <= 5, "changes should be coherent")
	}
}

// TestIncrementalStageBuilder_GapDetectionBehavior tests gap detection behavior
// when changes map to non-consecutive buffer positions.
func TestIncrementalStageBuilder_GapDetectionBehavior(t *testing.T) {
	// Create old lines where we can control exactly which lines match
	// Use similar prefixes to ensure modifications are detected
	oldLines := []string{
		"  func alpha() {", // line 1 (buffer 10)
		"    return 1",     // line 2 (buffer 11)
		"  }",              // line 3 (buffer 12)
		"",                 // line 4 (buffer 13)
		"  func beta() {",  // line 5 (buffer 14)
		"    return 2",     // line 6 (buffer 15)
	}

	baseLineOffset := 10

	builder := NewIncrementalStageBuilder(
		oldLines,
		baseLineOffset,
		2,     // proximityThreshold = 2 (gap > 2 should split)
		0,     // maxVisibleLines (disabled)
		5, 20, // viewport
		12, 0, // cursorRow, cursorCol
		"test.go",
	)

	// Output lines where:
	// - New line 1: modification of old line 1 (buffer 10)
	// - New line 2: exact match of old line 2
	// - New line 3: exact match of old line 3
	// - New line 4: exact match of old line 4
	// - New line 5: modification of old line 6 (buffer 15!) - skipping old line 5
	//
	// New line gap between changes: 5 - 1 = 4 > threshold (should split)
	// Buffer line gap between changes: 15 - 10 = 5 > threshold (should split)

	builder.AddLine("  func alpha(x) {") // Modify old line 1 -> buffer 10
	builder.AddLine("    return 1")      // Match old line 2
	builder.AddLine("  }")               // Match old line 3
	builder.AddLine("")                  // Match old line 4
	builder.AddLine("    return 200")    // Modify - similar to old line 6 -> buffer 15

	result := builder.Finalize()
	assert.NotNil(t, result, "expected staging result")

	// Since BOTH new-line gap (4) and buffer-line gap (5) exceed threshold (2),
	// we expect 2 stages regardless of which gap metric is used.
	assert.Equal(t, 2, len(result.Stages), "stage count")
}

// TestIncrementalStageBuilder_ConsecutiveOutputMapsToScatteredLines tests when
// consecutive model output lines map to non-consecutive buffer positions via similarity.
func TestIncrementalStageBuilder_ConsecutiveOutputMapsToScatteredLines(t *testing.T) {
	// Old lines with distinct content. We'll output SIMILAR lines to ensure
	// they match as modifications (not additions).
	oldLines := []string{
		"  article.title = title;",   // line 1 (buffer 50) - will modify
		"  const x = 1;",             // line 2 (buffer 51) - will skip
		"  article.author = author;", // line 3 (buffer 52) - will modify
		"  const y = 2;",             // line 4 (buffer 53) - will skip
		"  article.tags = tags;",     // line 5 (buffer 54) - will modify
	}

	baseLineOffset := 50

	builder := NewIncrementalStageBuilder(
		oldLines,
		baseLineOffset,
		1,      // proximityThreshold = 1 (very strict - gap > 1 should split)
		0,      // maxVisibleLines (disabled)
		45, 60, // viewport
		52, 0,  // cursorRow, cursorCol
		"test.go",
	)

	// Model outputs CONSECUTIVE new lines (no gaps in new-line numbers)
	// These are similar enough to match the old lines at positions 1, 3, 5:
	// - New line 1 -> old line 1 (buffer 50) - similar "article.title"
	// - New line 2 -> old line 3 (buffer 52) - similar "article.author" (buffer gap = 2)
	// - New line 3 -> old line 5 (buffer 54) - similar "article.tags" (buffer gap = 2)
	//
	// New line gaps: 1, 1 (NOT > threshold)
	// Buffer line gaps: 2, 2 (> threshold)

	finalized1 := builder.AddLine("  article.title = newTitle;")   // Modify -> buffer 50
	finalized2 := builder.AddLine("  article.author = newAuthor;") // Modify -> buffer 52
	finalized3 := builder.AddLine("  article.tags = newTags;")     // Modify -> buffer 54

	// Track stages finalized during streaming
	streamFinalizedCount := 0
	if finalized1 != nil {
		streamFinalizedCount++
	}
	if finalized2 != nil {
		streamFinalizedCount++
	}
	if finalized3 != nil {
		streamFinalizedCount++
	}

	result := builder.Finalize()
	assert.NotNil(t, result, "expected staging result")

	// Verify changes exist in stages
	totalChanges := 0
	for _, stage := range result.Stages {
		totalChanges += len(stage.Changes)
	}
	assert.Greater(t, totalChanges, 0, "expected changes in stages")

	// When buffer gaps exceed threshold, expect separate stages
	assert.GreaterOrEqual(t, len(result.Stages), 3, "expected separate stages due to buffer gaps")
}

// TestIncrementalDiffBuilder_SearchWindowConstraint verifies that matching is
// constrained to the search window and doesn't match far-away lines.
func TestIncrementalDiffBuilder_SearchWindowConstraint(t *testing.T) {
	// Simulate the scenario from the logs:
	// - 98 old lines (full file)
	// - Model outputs something, and first line matches to old line 41
	//
	// Old line 0: "import { Hono } from \"hono\";"
	// Old line 40: "export { WorkflowRuntimeEntrypoint... }"
	// Old line 41: empty or comment
	//
	// If model line 1 is "import apiKeyRoutes...", it should match within [0, 10),
	// NOT to line 41.

	oldLines := make([]string, 98)
	// Fill with realistic content
	oldLines[0] = "import { Hono } from \"hono\";"
	oldLines[1] = ""
	oldLines[2] = "import auth from \"./auth\";"
	oldLines[3] = "import { ApiContext } from \"./context\";"
	for i := 4; i < 40; i++ {
		oldLines[i] = "import something from \"./something\";"
	}
	oldLines[40] = "// Export comment"
	oldLines[41] = "export { WorkflowRuntimeEntrypoint as Runtime } from \"./runtime\";"
	oldLines[42] = ""
	oldLines[43] = "// Initialize app"
	oldLines[44] = "const application = new Hono<ApiContext>();"
	for i := 45; i < 98; i++ {
		oldLines[i] = "app.route(\"/path\", handler);"
	}

	builder := NewIncrementalDiffBuilder(oldLines)

	// Model outputs "import apiKeyRoutes..." as first line
	// This should match within [0, 10), NOT to line 41
	modelLine1 := "import apiKeyRoutes from \"./routes/api-keys\";"
	change1 := builder.AddLine(modelLine1)

	// Check where it matched
	assert.True(t, len(builder.LineMapping.NewToOld) > 0, "line mapping should be populated")

	matchedOldLine := builder.LineMapping.NewToOld[0] // 1-indexed old line number

	// The search window for first line is [0, 10)
	// Matches should be constrained to this window
	assert.True(t, matchedOldLine <= 10, "first model line should match within search window")

	// Check the recorded change
	if change1 != nil {
		assert.True(t, change1.OldLineNum <= 10, "change OldLineNum should be within search window")
	}
}

// TestIncrementalDiffBuilder_SearchWindowRespected verifies the search window bounds
func TestIncrementalDiffBuilder_SearchWindowRespected(t *testing.T) {
	// Create old lines where exact match exists ONLY outside the search window
	oldLines := make([]string, 50)
	for i := range 50 {
		oldLines[i] = "generic line"
	}
	// Put a unique line at position 30 (outside initial search window [0, 10))
	oldLines[30] = "unique content at line 31"

	builder := NewIncrementalDiffBuilder(oldLines)

	// Try to match the unique line as FIRST model line
	// It should NOT match because it's outside [0, 10)
	builder.AddLine("unique content at line 31")

	matchedOldLine := builder.LineMapping.NewToOld[0]

	// Should either:
	// 1. Match to something in [0, 10) via similarity
	// 2. Or be recorded as addition (matchedOldLine == 0)
	// Should NOT match to line 31 (outside search window)
	assert.NotEqual(t, 31, matchedOldLine, "should not match to line 31")
}

// TestIncrementalDiffBuilder_OldLineIdxProgression verifies oldLineIdx advances correctly
func TestIncrementalDiffBuilder_OldLineIdxProgression(t *testing.T) {
	// Use very distinct lines to avoid similarity matching
	oldLines := []string{
		"function alpha() {",   // 1
		"function beta() {",    // 2
		"function gamma() {",   // 3
		"function delta() {",   // 4
		"function epsilon() {", // 5
	}

	builder := NewIncrementalDiffBuilder(oldLines)

	// Match line 1
	builder.AddLine("function alpha() {")
	assert.Equal(t, 1, builder.oldLineIdx, "oldLineIdx after matching line 1")

	// Match line 2
	builder.AddLine("function beta() {")
	assert.Equal(t, 2, builder.oldLineIdx, "oldLineIdx after matching line 2")

	// Add something completely different (should be addition)
	builder.AddLine("ZZZZZ COMPLETELY DIFFERENT ZZZZZ")
	// With 0.3 similarity threshold, this might still match something

	// Match line 3 (should still work because search window extends forward)
	builder.AddLine("function gamma() {}")
	assert.GreaterOrEqual(t, builder.oldLineIdx, 3, "oldLineIdx should be at least 3")
}

// TestIncrementalDiffBuilder_OutOfOrderOutput verifies behavior when model output
// contains lines in a different order than the original file.
func TestIncrementalDiffBuilder_OutOfOrderOutput(t *testing.T) {
	oldLines := []string{
		"func first() {}",
		"func second() {}",
		"func third() {}",
		"func fourth() {}",
		"func fifth() {}",
	}

	builder := NewIncrementalDiffBuilder(oldLines)

	// Output line 4 first (out of order)
	builder.AddLine("func fourth() {}")
	firstMatch := builder.LineMapping.NewToOld[0]

	// Should match to line 4 within search window [0, 10)
	assert.Equal(t, 4, firstMatch, "first match should be to line 4")

	// Output duplicate of line 4
	builder.AddLine("func fourth() {}")
	secondMatch := builder.LineMapping.NewToOld[1]

	// usedOldLines should prevent duplicate matching
	assert.True(t, secondMatch != firstMatch || firstMatch == 0, "duplicate line should not match same old line twice")
}

// TestIncrementalDiffBuilder_SearchWindowBounds tests that matching is constrained
// to a sliding search window.
func TestIncrementalDiffBuilder_SearchWindowBounds(t *testing.T) {
	tests := []struct {
		name           string
		oldLineCount   int
		startPosition  int
		uniquePosition int
		expectMatch    bool
	}{
		{
			name:           "unique line within window",
			oldLineCount:   50,
			startPosition:  0,
			uniquePosition: 5,
			expectMatch:    true,
		},
		{
			name:           "unique line outside window",
			oldLineCount:   50,
			startPosition:  0,
			uniquePosition: 30,
			expectMatch:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldLines := make([]string, tt.oldLineCount)
			for i := range oldLines {
				oldLines[i] = "generic content"
			}
			// Put unique content at specific position
			oldLines[tt.uniquePosition] = "unique_content_here"

			builder := NewIncrementalDiffBuilder(oldLines)
			builder.oldLineIdx = tt.startPosition

			builder.AddLine("unique_content_here")
			matched := builder.LineMapping.NewToOld[0]

			if tt.expectMatch {
				expectedLine := tt.uniquePosition + 1 // 1-indexed
				assert.Equal(t, expectedLine, matched, "should match at expected line")
			} else {
				// Should not match to the unique position (outside window)
				assert.NotEqual(t, tt.uniquePosition+1, matched, "should not match outside search window")
			}
		})
	}
}

// TestIncrementalDiffBuilder_LongFileWithManyExactMatches tests incremental
// diff building on a large file where most lines match exactly.
func TestIncrementalDiffBuilder_LongFileWithManyExactMatches(t *testing.T) {
	// Large file (50 lines) where first 40 lines match exactly,
	// then changes start occurring
	oldLines := make([]string, 50)
	for i := range 40 {
		oldLines[i] = "func line" + string(rune('A'+i%26)) + "() {}"
	}
	oldLines[40] = "// Comment"
	oldLines[41] = ""
	for i := 42; i < 50; i++ {
		oldLines[i] = "func other" + string(rune('A'+i%26)) + "() {}"
	}

	builder := NewIncrementalDiffBuilder(oldLines)

	// Model outputs exact same content for first 40 lines
	for i := range 40 {
		change := builder.AddLine(oldLines[i])
		assert.Nil(t, change, "line should be exact match")
	}

	// oldLineIdx should have advanced to 40
	assert.Equal(t, 40, builder.oldLineIdx, "oldLineIdx")

	// Now model outputs something different
	change := builder.AddLine("func completely_new() {}")
	assert.NotNil(t, change, "expected change for non-matching line")
}

// TestIncrementalDiffBuilder_MatchingWhenModelSkipsLines tests what happens when
// model output is out of order or skips lines.
func TestIncrementalDiffBuilder_MatchingWhenModelSkipsLines(t *testing.T) {
	oldLines := []string{
		"func first() {}",
		"func second() {}",
		"func third() {}",
		"func fourth() {}",
		"func fifth() {}",
	}

	builder := NewIncrementalDiffBuilder(oldLines)

	// Output line 4 first (skipping lines 1-3)
	builder.AddLine("func fourth() {}")
	matched1 := builder.LineMapping.NewToOld[0]

	assert.Equal(t, 4, matched1, "first match should be to line 4")

	// Output line 1 - but search window has moved past it
	builder.AddLine("func first() {}")
	matched2 := builder.LineMapping.NewToOld[1]

	// Line 1 is outside the new search window after matching line 4
	// Should not match to line 1
	assert.NotEqual(t, 1, matched2, "should not match to line 1")
}

// TestIncrementalStageBuilder_WhenModelOutputStartsMidFile tests when model
// output starts from middle of file instead of beginning.
func TestIncrementalStageBuilder_WhenModelOutputStartsMidFile(t *testing.T) {
	oldLines := []string{
		"func first() {}",
		"func second() {}",
		"func third() {}",
		"",
		"// Section 2",
		"func fourth() {}",
		"",
		"func fifth() {}",
		"func sixth() {}",
	}

	builder := NewIncrementalStageBuilder(
		oldLines,
		1,    // baseLineOffset
		3,    // proximityThreshold
		0,    // maxVisibleLines (disabled)
		0, 0, // viewport disabled
		5, 0, // cursorRow, cursorCol
		"test.go",
	)

	// Model starts outputting from line 5 (skipping lines 1-4)
	modelOutput := []string{
		"// Section 2",              // matches line 5
		"func fourth_modified() {}", // modified
		"",                          // matches line 7
		"func fifth_modified() {}",  // modified
	}

	for _, line := range modelOutput {
		builder.AddLine(line)
	}

	result := builder.Finalize()
	assert.NotNil(t, result, "expected staging result")

	// Should have at least one stage
	assert.True(t, len(result.Stages) > 0, "expected at least one stage")

	// Buffer positions should be within file bounds
	for _, stage := range result.Stages {
		assert.True(t, stage.BufferStart <= len(oldLines)+1, "stage BufferStart within bounds")
		assert.True(t, stage.BufferEnd <= len(oldLines)+10, "stage BufferEnd within bounds")
	}
}

// TestLineSimilarity verifies similarity calculation for various line comparisons.
func TestLineSimilarity(t *testing.T) {
	tests := []struct {
		name   string
		line1  string
		line2  string
		minSim float64
		maxSim float64
	}{
		{
			name:   "identical lines",
			line1:  "const x = 1;",
			line2:  "const x = 1;",
			minSim: 1.0,
			maxSim: 1.0,
		},
		{
			name:   "small modification",
			line1:  "const x = 1;",
			line2:  "const x = 2;",
			minSim: 0.8,
			maxSim: 1.0,
		},
		{
			name:   "completely different",
			line1:  "function foo() {",
			line2:  "// comment here",
			minSim: 0.0,
			maxSim: 0.3,
		},
		{
			name:   "empty vs content",
			line1:  "",
			line2:  "some content",
			minSim: 0.0,
			maxSim: 0.1,
		},
		{
			name:   "variable rename",
			line1:  "const app = new Server();",
			line2:  "const server = new Server();",
			minSim: 0.6,
			maxSim: 0.95,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			similarity := LineSimilarity(tt.line1, tt.line2)
			assert.True(t, similarity >= tt.minSim && similarity <= tt.maxSim, "similarity in expected range")
		})
	}
}

// TestIncrementalDiffBuilder_LargeFile tests incremental diff building with a large file.
func TestIncrementalDiffBuilder_LargeFile(t *testing.T) {
	// Create a large file with distinct sections
	oldLines := make([]string, 50)
	for i := range 20 {
		oldLines[i] = "func section1_line" + string(rune('A'+i)) + "() {}"
	}
	oldLines[20] = ""
	for i := 21; i < 40; i++ {
		oldLines[i] = "func section2_line" + string(rune('A'+i-21)) + "() {}"
	}
	oldLines[40] = ""
	for i := 41; i < 50; i++ {
		oldLines[i] = "func section3_line" + string(rune('A'+i-41)) + "() {}"
	}

	builder := NewIncrementalDiffBuilder(oldLines)

	// Process exact matches for first 20 lines
	for i := range 20 {
		change := builder.AddLine(oldLines[i])
		assert.Nil(t, change, "line should be exact match")
	}

	// oldLineIdx should have advanced
	assert.Equal(t, 20, builder.oldLineIdx, "oldLineIdx")

	// Add a new line that doesn't exist in the original
	// Since oldLines[20] is "", and we're adding non-empty content at the expected position,
	// the empty line will be matched and filled (append_chars)
	change := builder.AddLine("func new_function() {}")
	assert.NotNil(t, change, "expected change for new line")

	// Should be recorded as append_chars (filling empty line), addition, or modification
	assert.True(t, change.Type == ChangeAddition || change.Type == ChangeModification ||
		change.Type == ChangeReplaceChars || change.Type == ChangeAppendChars,
		"expected valid change type")
}

// TestIncrementalDiffBuilder_DuplicateLinesPrevented verifies that the same old line
// cannot be matched twice (usedOldLines tracking).
func TestIncrementalDiffBuilder_DuplicateLinesPrevented(t *testing.T) {
	oldLines := []string{
		"func alpha() {}",
		"func beta() {}",
		"func gamma() {}",
	}

	builder := NewIncrementalDiffBuilder(oldLines)

	// Match line 1
	builder.AddLine("func alpha() {}")
	firstMatch := builder.LineMapping.NewToOld[0]
	assert.Equal(t, 1, firstMatch, "first line should match old line 1")

	// Try to match line 1 again (duplicate in model output)
	builder.AddLine("func alpha() {}")
	secondMatch := builder.LineMapping.NewToOld[1]

	// Should NOT match to line 1 again
	assert.NotEqual(t, 1, secondMatch, "duplicate model line should not match same old line twice")
}

// TestIncrementalStageBuilder_DuplicateOutputHandling verifies stage building
// when model outputs duplicate lines.
func TestIncrementalStageBuilder_DuplicateOutputHandling(t *testing.T) {
	oldLines := []string{
		"func setup() {",
		"    init()",
		"}",
		"",
		"func run() {",
		"    execute()",
		"}",
	}

	builder := NewIncrementalStageBuilder(
		oldLines,
		1,    // baseLineOffset
		3,    // proximityThreshold
		0,    // maxVisibleLines (disabled)
		0, 0, // viewport disabled
		1, 0, // cursorRow, cursorCol
		"test.go",
	)

	// Exact matches for first 3 lines
	for i := range 3 {
		builder.AddLine(oldLines[i])
	}

	// Now output duplicates
	builder.AddLine("func setup() {}") // duplicate
	builder.AddLine("func setup() {}") // duplicate again

	result := builder.Finalize()
	assert.NotNil(t, result, "expected staging result")

	// Should have at least one stage with the duplicate changes
	assert.True(t, len(result.Stages) > 0, "expected at least one stage")

	// Each stage should have valid buffer coordinates
	for _, stage := range result.Stages {
		assert.GreaterOrEqual(t, stage.BufferStart, 1, "stage BufferStart valid")
		assert.GreaterOrEqual(t, stage.BufferEnd, stage.BufferStart, "stage BufferEnd >= BufferStart")
	}
}

func TestIncrementalStageBuilder_ConsistencyWithComputeDiff(t *testing.T) {
	oldLines := []string{
		"func main() {",
		"    fmt.Println(\"hello\")",
		"    return",
		"}",
	}
	newLines := []string{
		"func main() {",
		"    fmt.Println(\"hello world\")",
		"    return nil",
		"}",
	}

	// Use batch ComputeDiff
	oldText := JoinLines(oldLines)
	newText := JoinLines(newLines)
	batchResult := ComputeDiff(oldText, newText)

	// Use incremental builder
	builder := NewIncrementalDiffBuilder(oldLines)
	for _, line := range newLines {
		builder.AddLine(line)
	}

	// Compare change counts
	assert.Equal(t, len(batchResult.Changes), len(builder.Changes), "change count")

	// Both should identify modifications on lines 2 and 3
	for lineNum, change := range batchResult.Changes {
		incChange, ok := builder.Changes[lineNum]
		assert.True(t, ok, "incremental builder should have change at line")

		// Types might differ slightly (e.g., ReplaceChars vs Modification)
		// but both should identify it as a modification-like change
		batchIsMod := change.Type != ChangeAddition && change.Type != ChangeDeletion
		incIsMod := incChange.Type != ChangeAddition && incChange.Type != ChangeDeletion
		assert.Equal(t, batchIsMod, incIsMod, "batch and incremental should identify same change type")
	}
}

// TestIncrementalDiffBuilder_AllLinesIdentical verifies no changes when all lines match.
func TestIncrementalDiffBuilder_AllLinesIdentical(t *testing.T) {
	oldLines := []string{"line1", "line2", "line3", "line4", "line5"}
	builder := NewIncrementalDiffBuilder(oldLines)

	for _, line := range oldLines {
		change := builder.AddLine(line)
		assert.Nil(t, change, "expected no change for identical line")
	}

	assert.Equal(t, 0, len(builder.Changes), "change count")
}

// TestIncrementalDiffBuilder_AllLinesModified verifies all lines are detected as modified.
func TestIncrementalDiffBuilder_AllLinesModified(t *testing.T) {
	oldLines := []string{"old1", "old2", "old3"}
	builder := NewIncrementalDiffBuilder(oldLines)

	newLines := []string{"new1", "new2", "new3"}
	for _, line := range newLines {
		builder.AddLine(line)
	}

	// All lines should have changes
	assert.Equal(t, 3, len(builder.Changes), "change count")
}

// TestIncrementalDiffBuilder_WhitespaceOnlyLines tests handling of whitespace-only lines.
func TestIncrementalDiffBuilder_WhitespaceOnlyLines(t *testing.T) {
	oldLines := []string{"", "   ", "\t", "content"}
	builder := NewIncrementalDiffBuilder(oldLines)

	// Exact matches
	for _, line := range oldLines {
		change := builder.AddLine(line)
		assert.Nil(t, change, "expected no change for whitespace match")
	}
}

// TestIncrementalStageBuilder_EmptyInput verifies handling of empty input.
func TestIncrementalStageBuilder_EmptyInput(t *testing.T) {
	builder := NewIncrementalStageBuilder(
		[]string{}, // empty old lines
		1,          // baseLineOffset
		3,          // proximityThreshold
		0,          // maxVisibleLines (disabled)
		0, 0,       // viewport disabled
		1, 0,       // cursorRow, cursorCol
		"test.go",
	)

	builder.AddLine("new content")
	// Adding to empty may return nil change or an addition

	result := builder.Finalize()
	// Result may be nil for empty old + single new line (no meaningful changes)
	if result != nil && len(result.Stages) > 0 {
		// Verify the stage has valid structure
		for _, stage := range result.Stages {
			assert.GreaterOrEqual(t, stage.BufferStart, 1, "stage BufferStart valid")
		}
	}
}

// TestIncrementalStageBuilder_SingleLine verifies handling of single-line files.
func TestIncrementalStageBuilder_SingleLine(t *testing.T) {
	oldLines := []string{"single line"}
	builder := NewIncrementalStageBuilder(
		oldLines,
		1,    // baseLineOffset
		3,    // proximityThreshold
		0,    // maxVisibleLines (disabled)
		0, 0, // viewport disabled
		1, 0, // cursorRow, cursorCol
		"test.go",
	)

	// Modify the single line
	builder.AddLine("modified single line")

	result := builder.Finalize()
	assert.NotNil(t, result, "expected staging result")

	assert.Equal(t, 1, len(result.Stages), "stage count")
}

// TestIncrementalDiffBuilder_VeryLongLines tests handling of very long lines.
func TestIncrementalDiffBuilder_VeryLongLines(t *testing.T) {
	longLine := ""
	for range 1000 {
		longLine += "x"
	}

	oldLines := []string{longLine}
	builder := NewIncrementalDiffBuilder(oldLines)

	// Exact match
	change := builder.AddLine(longLine)
	assert.Nil(t, change, "expected no change for identical long line")

	// Slight modification
	builder2 := NewIncrementalDiffBuilder(oldLines)
	change = builder2.AddLine(longLine + "y")
	assert.NotNil(t, change, "expected change for modified long line")
}

// TestIncrementalStageBuilder_LargeGap verifies stage splitting with large gaps.
func TestIncrementalStageBuilder_LargeGap(t *testing.T) {
	// Create old lines with changes at beginning and end
	oldLines := make([]string, 100)
	for i := range oldLines {
		oldLines[i] = "line"
	}

	builder := NewIncrementalStageBuilder(
		oldLines,
		1,    // baseLineOffset
		3,    // proximityThreshold
		0,    // maxVisibleLines (disabled)
		0, 0, // viewport disabled
		1, 0, // cursorRow, cursorCol
		"test.go",
	)

	// Modify first line
	builder.AddLine("modified first")
	// Match lines 2-90
	for i := 1; i < 90; i++ {
		builder.AddLine(oldLines[i])
	}
	// Modify last few lines
	builder.AddLine("modified 90")
	for i := 91; i < 100; i++ {
		builder.AddLine(oldLines[i])
	}

	result := builder.Finalize()
	assert.NotNil(t, result, "expected staging result")

	// Should have 2 stages due to large gap
	assert.True(t, len(result.Stages) >= 2, "expected at least 2 stages")
}

// TestIncrementalStageBuilder_ConsecutiveModifications verifies consecutive modifications
// stay in the same stage.
func TestIncrementalStageBuilder_ConsecutiveModifications(t *testing.T) {
	oldLines := []string{"a", "b", "c", "d", "e"}
	builder := NewIncrementalStageBuilder(
		oldLines,
		1,    // baseLineOffset
		3,    // proximityThreshold
		0,    // maxVisibleLines (disabled)
		0, 0, // viewport disabled
		1, 0, // cursorRow, cursorCol
		"test.go",
	)

	// Modify lines 2-4 consecutively
	builder.AddLine("a")          // match
	builder.AddLine("B_modified") // modify
	builder.AddLine("C_modified") // modify
	builder.AddLine("D_modified") // modify
	builder.AddLine("e")          // match

	result := builder.Finalize()
	assert.NotNil(t, result, "expected staging result")

	// All consecutive modifications should be in one stage
	assert.Equal(t, 1, len(result.Stages), "stage count")

	if len(result.Stages) > 0 {
		assert.Equal(t, 3, len(result.Stages[0].Changes), "changes in stage")
	}
}

// TestIncrementalDiffBuilder_SpecialCharacters tests handling of special characters.
func TestIncrementalDiffBuilder_SpecialCharacters(t *testing.T) {
	oldLines := []string{
		"line with 'quotes'",
		"line with \"double quotes\"",
		"line with `backticks`",
		"line with special: !@#$%^&*()",
		"line with unicode: 日本語",
	}

	builder := NewIncrementalDiffBuilder(oldLines)

	// Exact matches should work
	for _, line := range oldLines {
		change := builder.AddLine(line)
		assert.Nil(t, change, "expected no change for line with special chars")
	}
}

// TestIncrementalDiffBuilder_PrefixMatch verifies that when an old line is a prefix
// of a new line, it's detected as a modification (append_chars), not an addition.
// This is critical for code completion where the user types partial content and
// the model completes it to a longer line.
func TestIncrementalDiffBuilder_PrefixMatch(t *testing.T) {
	oldLines := []string{"aaa", "", "xx"}
	builder := NewIncrementalDiffBuilder(oldLines)

	// Line 1: exact match
	change1 := builder.AddLine("aaa")
	assert.Nil(t, change1, "expected no change for exact match")

	// Line 2: exact match (empty)
	change2 := builder.AddLine("")
	assert.Nil(t, change2, "expected no change for empty line match")

	// Line 3: "xx" -> "xx completed with more text" should be append_chars, not addition
	change3 := builder.AddLine("xx completed with more text")
	assert.NotNil(t, change3, "expected change for prefix completion")
	assert.Equal(t, ChangeAppendChars, change3.Type, "expected append_chars for prefix match")
	assert.Equal(t, 3, change3.OldLineNum, "old line num")
	assert.Equal(t, 3, change3.NewLineNum, "new line num")
	assert.Equal(t, "xx", change3.OldContent, "old content")
	assert.Equal(t, "xx completed with more text", change3.Content, "new content")

	// Line 4: new addition after the prefix match
	change4 := builder.AddLine("    next line")
	assert.NotNil(t, change4, "expected change for addition")
	assert.Equal(t, ChangeAddition, change4.Type, "expected addition")
}

// TestIncrementalDiffBuilder_PrefixMatchWithFollowingAdditions tests the complete
// scenario where a prefix match is followed by multiple additions.
func TestIncrementalDiffBuilder_PrefixMatchWithFollowingAdditions(t *testing.T) {
	oldLines := []string{"header", "", "px"}
	builder := NewIncrementalDiffBuilder(oldLines)

	builder.AddLine("header")                          // exact match
	builder.AddLine("")                                // exact match
	builder.AddLine("px completed with a longer line") // prefix match -> append_chars
	builder.AddLine("    following line")              // addition

	// Should have 2 changes: append_chars on line 3, addition on line 4
	assert.Equal(t, 2, len(builder.Changes), "expected 2 changes")

	// Verify the append_chars change
	change3 := builder.Changes[3]
	assert.Equal(t, ChangeAppendChars, change3.Type, "line 3 should be append_chars")
	assert.Equal(t, "px", change3.OldContent, "old content")

	// Verify the addition
	change4 := builder.Changes[4]
	assert.Equal(t, ChangeAddition, change4.Type, "line 4 should be addition")
}

// TestIncrementalStageBuilder_LowSimilarityReplacement verifies that when we replace
// content with very different content (low similarity), the stage builder correctly
// detects it as a modification using batch diff at finalization time.
// This handles typo correction: "this commt" -> "this commit addresses..."
func TestIncrementalStageBuilder_LowSimilarityReplacement(t *testing.T) {
	oldLines := []string{"line1", "", "this commt adress"}
	builder := NewIncrementalStageBuilder(
		oldLines,
		1,    // baseLineOffset
		10,   // proximityThreshold
		0,    // maxVisibleLines (disabled)
		0, 0, // viewport disabled
		1, 0, // cursorRow, cursorCol
		"test.go",
	)

	// Line 1: exact match
	builder.AddLine("line1")

	// Line 2: exact match (empty)
	builder.AddLine("")

	// Line 3: "this commt adress" -> long corrected text
	// During streaming this may be marked as addition (low similarity),
	// but at finalization batch diff will correctly identify it as modification
	// because old line count == new line count.
	newContent := "this commit addresses the issue of incorrect cursor target calculation in the text"
	builder.AddLine(newContent)

	result := builder.Finalize()
	assert.NotNil(t, result, "expected staging result")
	assert.Equal(t, 1, len(result.Stages), "stage count")

	stage := result.Stages[0]
	assert.Equal(t, 1, len(stage.Changes), "change count")

	// The change should be a modification (not addition) because equal line counts
	change, ok := stage.Changes[1] // Line 1 relative to stage
	assert.True(t, ok, "should have change")
	assert.Equal(t, ChangeModification, change.Type, "expected modification")
	assert.Equal(t, "this commt adress", change.OldContent, "old content")
	assert.Equal(t, newContent, change.Content, "new content")
}

// TestIncrementalStageBuilder_AppendCharsWithAdditionsBelow verifies that when
// a partial line is completed (append_chars) and new lines are added below,
// the completion is correctly typed and additions follow.
func TestIncrementalStageBuilder_AppendCharsWithAdditionsBelow(t *testing.T) {
	oldLines := []string{"partial"}
	builder := NewIncrementalStageBuilder(
		oldLines,
		1,    // baseLineOffset
		10,   // proximityThreshold
		0,    // maxVisibleLines (disabled)
		0, 0, // viewport disabled
		1, 0, // cursorRow, cursorCol
		"test.txt",
	)

	builder.AddLine("partial content completed")
	builder.AddLine("new line 1")
	builder.AddLine("new line 2")

	result := builder.Finalize()
	assert.NotNil(t, result, "expected staging result")
	assert.Equal(t, 1, len(result.Stages), "stage count")

	stage := result.Stages[0]

	// First line: append_chars (prefix completion)
	change1, ok := stage.Changes[1]
	assert.True(t, ok, "should have change at line 1")
	assert.Equal(t, ChangeAppendChars, change1.Type, "line 1 should be append_chars")
	assert.Equal(t, "partial", change1.OldContent, "old content")

	// Lines 2-3: additions
	change2, ok := stage.Changes[2]
	assert.True(t, ok, "should have change at line 2")
	assert.Equal(t, ChangeAddition, change2.Type, "line 2 should be addition")

	change3, ok := stage.Changes[3]
	assert.True(t, ok, "should have change at line 3")
	assert.Equal(t, ChangeAddition, change3.Type, "line 3 should be addition")
}

// TestIncrementalStageBuilder_AdditionsAboveWithAppendChars verifies that when
// new lines are inserted above and the original line is completed (append_chars),
// additions come first and the completion is at the end.
func TestIncrementalStageBuilder_AdditionsAboveWithAppendChars(t *testing.T) {
	oldLines := []string{"partial"}
	builder := NewIncrementalStageBuilder(
		oldLines,
		1,    // baseLineOffset
		10,   // proximityThreshold
		0,    // maxVisibleLines (disabled)
		0, 0, // viewport disabled
		1, 0, // cursorRow, cursorCol
		"test.txt",
	)

	// Model outputs new lines first, then completes the original partial line
	builder.AddLine("inserted line 1")
	builder.AddLine("inserted line 2")
	builder.AddLine("partial content completed")

	result := builder.Finalize()
	assert.NotNil(t, result, "expected staging result")
	assert.Equal(t, 1, len(result.Stages), "stage count")

	stage := result.Stages[0]

	// Lines 1-2: additions (inserted above)
	change1, ok := stage.Changes[1]
	assert.True(t, ok, "should have change at line 1")
	assert.Equal(t, ChangeAddition, change1.Type, "line 1 should be addition")

	change2, ok := stage.Changes[2]
	assert.True(t, ok, "should have change at line 2")
	assert.Equal(t, ChangeAddition, change2.Type, "line 2 should be addition")

	// Line 3: append_chars (the completed partial line)
	change3, ok := stage.Changes[3]
	assert.True(t, ok, "should have change at line 3")
	assert.Equal(t, ChangeAppendChars, change3.Type, "line 3 should be append_chars")
	assert.Equal(t, "partial", change3.OldContent, "old content preserved")
}

// TestIncrementalStageBuilder_AdditionsAboveAndBelowWithAppendChars verifies
// that additions can appear both above and below a completed line.
func TestIncrementalStageBuilder_AdditionsAboveAndBelowWithAppendChars(t *testing.T) {
	oldLines := []string{"middle"}
	builder := NewIncrementalStageBuilder(
		oldLines,
		1,    // baseLineOffset
		10,   // proximityThreshold
		0,    // maxVisibleLines (disabled)
		0, 0, // viewport disabled
		1, 0, // cursorRow, cursorCol
		"test.txt",
	)

	// Model outputs: additions above, completed line, additions below
	builder.AddLine("above 1")
	builder.AddLine("above 2")
	builder.AddLine("middle completed")
	builder.AddLine("below 1")
	builder.AddLine("below 2")

	result := builder.Finalize()
	assert.NotNil(t, result, "expected staging result")
	assert.Equal(t, 1, len(result.Stages), "stage count")

	stage := result.Stages[0]

	// Lines 1-2: additions above
	change1, ok := stage.Changes[1]
	assert.True(t, ok, "should have change at line 1")
	assert.Equal(t, ChangeAddition, change1.Type, "line 1 should be addition")

	change2, ok := stage.Changes[2]
	assert.True(t, ok, "should have change at line 2")
	assert.Equal(t, ChangeAddition, change2.Type, "line 2 should be addition")

	// Line 3: append_chars (the completed line)
	change3, ok := stage.Changes[3]
	assert.True(t, ok, "should have change at line 3")
	assert.Equal(t, ChangeAppendChars, change3.Type, "line 3 should be append_chars")
	assert.Equal(t, "middle", change3.OldContent, "old content preserved")

	// Lines 4-5: additions below
	change4, ok := stage.Changes[4]
	assert.True(t, ok, "should have change at line 4")
	assert.Equal(t, ChangeAddition, change4.Type, "line 4 should be addition")

	change5, ok := stage.Changes[5]
	assert.True(t, ok, "should have change at line 5")
	assert.Equal(t, ChangeAddition, change5.Type, "line 5 should be addition")
}

// TestIncrementalStageBuilder_MaxVisibleLines tests that maxVisibleLines correctly
// splits stages in the incremental builder. This reproduces the bug where stage 2
// gets incorrect buffer coordinates after being split by maxVisibleLines.
func TestIncrementalStageBuilder_MaxVisibleLines(t *testing.T) {
	// Scenario from logs:
	// Original: "import numpy as np", "", "def bubb" (3 lines)
	// New: adds bubble_sort function (modification + multiple additions)
	oldLines := []string{"import numpy as np", "", "def bubb"}
	builder := NewIncrementalStageBuilder(
		oldLines,
		1,    // baseLineOffset
		10,   // proximityThreshold (high to prevent gap splits)
		2,    // maxVisibleLines - force split after 2 lines
		0, 0, // viewport disabled
		3, 0, // cursorRow, cursorCol
		"test.py",
	)

	// Feed the model output line by line
	builder.AddLine("import numpy as np")    // unchanged
	builder.AddLine("")                      // unchanged
	builder.AddLine("def bubble_sort(arr):") // modification (was "def bubb")
	builder.AddLine("    n = len(arr)")      // addition
	// At this point, stage 1 should have 2 lines (the modification + 1 addition)
	// and maxVisibleLines should trigger a new stage

	builder.AddLine("    for i in range(n):")           // addition - should be in stage 2
	builder.AddLine("        for j in range(0, n-i-1):") // addition - should be in stage 2

	result := builder.Finalize()
	assert.NotNil(t, result, "expected staging result")
	assert.True(t, len(result.Stages) >= 2, "should have at least 2 stages with maxVisibleLines=2")

	stage1 := result.Stages[0]
	stage2 := result.Stages[1]

	t.Logf("Stage 1: BufferStart=%d, BufferEnd=%d, Lines=%d", stage1.BufferStart, stage1.BufferEnd, len(stage1.Lines))
	for i, g := range stage1.Groups {
		t.Logf("  Group %d: type=%s, BufferLine=%d", i, g.Type, g.BufferLine)
	}
	t.Logf("Stage 2: BufferStart=%d, BufferEnd=%d, Lines=%d", stage2.BufferStart, stage2.BufferEnd, len(stage2.Lines))
	for i, g := range stage2.Groups {
		t.Logf("  Group %d: type=%s, BufferLine=%d", i, g.Type, g.BufferLine)
	}

	// Stage 1 should start at buffer line 3 (where "def bubb" is)
	assert.Equal(t, 3, stage1.BufferStart, "stage 1 should start at buffer line 3")

	// Stage 2 contains pure additions that should be INSERTED after line 3.
	// For pure additions, BufferStart should be the INSERTION POINT (anchor + 1),
	// not the anchor itself. This is because virt_lines_above renders above the
	// specified line, so to render below line 3, we need BufferStart=4.
	// This matches the non-streaming CreateStages behavior.
	assert.Equal(t, 4, stage2.BufferStart,
		fmt.Sprintf("stage 2 BufferStart should be 4 (insertion point after line 3), got %d", stage2.BufferStart))

	// Verify groups have BufferLine=4 (insertion point)
	for i, g := range stage2.Groups {
		assert.Equal(t, 4, g.BufferLine,
			fmt.Sprintf("stage 2 group %d BufferLine should be 4 (insertion point), got %d", i, g.BufferLine))
	}
}

// TestIncrementalStageBuilder_MaxVisibleLines_ThreeStages tests the cumulative offset
// calculation when there are 3+ stages split by maxVisibleLines. This reproduces the
// bug where stage 3's offset is wrong because pure addition stages (stage 2) were
// incorrectly counted as replacing 1 line instead of inserting.
func TestIncrementalStageBuilder_MaxVisibleLines_ThreeStages(t *testing.T) {
	// Original: 3 lines
	// Model output: modification + 6 additions = 7 new lines at position 3
	// With maxVisibleLines=2, we get:
	//   Stage 1: lines 3-4 (modification + 1 addition) - replaces 1 line with 2
	//   Stage 2: lines 5-6 (2 additions) - inserts 2 lines
	//   Stage 3: lines 7-8 (2 additions) - inserts 2 lines
	oldLines := []string{"import numpy as np", "", "def bubb"}
	builder := NewIncrementalStageBuilder(
		oldLines,
		1,    // baseLineOffset
		10,   // proximityThreshold (high to prevent gap splits)
		2,    // maxVisibleLines - force split after 2 lines
		0, 0, // viewport disabled
		3, 0, // cursorRow, cursorCol
		"test.py",
	)

	// Feed the model output
	builder.AddLine("import numpy as np")             // unchanged
	builder.AddLine("")                               // unchanged
	builder.AddLine("def bubble_sort(arr):")          // modification
	builder.AddLine("    n = len(arr)")               // addition (stage 1 ends here)
	builder.AddLine("    for i in range(n):")         // addition (stage 2)
	builder.AddLine("        for j in range(n-i-1):") // addition (stage 2 ends here)
	builder.AddLine("            if arr[j] > arr[j+1]:") // addition (stage 3)
	builder.AddLine("                swap(arr, j)")      // addition (stage 3 ends here)

	result := builder.Finalize()
	assert.NotNil(t, result, "expected staging result")
	assert.Equal(t, 3, len(result.Stages), "should have 3 stages with maxVisibleLines=2")

	stage1 := result.Stages[0]
	stage2 := result.Stages[1]
	stage3 := result.Stages[2]

	t.Logf("Stage 1: BufferStart=%d, BufferEnd=%d, Lines=%d", stage1.BufferStart, stage1.BufferEnd, len(stage1.Lines))
	t.Logf("Stage 2: BufferStart=%d, BufferEnd=%d, Lines=%d", stage2.BufferStart, stage2.BufferEnd, len(stage2.Lines))
	t.Logf("Stage 3: BufferStart=%d, BufferEnd=%d, Lines=%d", stage3.BufferStart, stage3.BufferEnd, len(stage3.Lines))

	// Initial coordinates (before any offset adjustments):
	// Stage 1: BufferStart=3 (modifying line 3)
	// Stage 2: BufferStart=4 (pure additions, insertion point after line 3)
	// Stage 3: BufferStart=4 (pure additions, insertion point after line 3)
	assert.Equal(t, 3, stage1.BufferStart, "stage 1 BufferStart")
	assert.Equal(t, 4, stage2.BufferStart, "stage 2 BufferStart (insertion point)")
	assert.Equal(t, 4, stage3.BufferStart, "stage 3 BufferStart (insertion point, before offset)")

	// Verify stage 2 and 3 are pure additions (all groups are "addition" type)
	for _, g := range stage2.Groups {
		assert.Equal(t, "addition", g.Type, "stage 2 should have only addition groups")
	}
	for _, g := range stage3.Groups {
		assert.Equal(t, "addition", g.Type, "stage 3 should have only addition groups")
	}

	// The key test: IsPureAddition should be detectable from groups
	// This will be used by advanceStagedCompletion to calculate correct offset
	stage2IsPureAddition := true
	for _, g := range stage2.Groups {
		if g.Type != "addition" {
			stage2IsPureAddition = false
			break
		}
	}
	assert.True(t, stage2IsPureAddition, "stage 2 should be detected as pure additions")
}

// TestIncrementalStageBuilder_BlankLineAdditions verifies that blank lines in the
// model output are correctly included as additions and not skipped.
// This tests the scenario where multi-line completions include blank lines between
// blocks of code (e.g., functions or paragraphs).
func TestIncrementalStageBuilder_BlankLineAdditions(t *testing.T) {
	// Simulate: user has partial content that gets completed with multiple blocks
	// separated by blank lines
	oldLines := []string{"header", "", "func te"}
	builder := NewIncrementalStageBuilder(
		oldLines,
		1,    // baseLineOffset
		10,   // proximityThreshold
		0,    // maxVisibleLines (disabled)
		0, 0, // viewport disabled
		3, 0, // cursorRow, cursorCol
		"test.go",
	)

	// Model outputs completed content with blocks separated by blank lines
	builder.AddLine("header")          // exact match
	builder.AddLine("")                // exact match (blank)
	builder.AddLine("func test1() {}") // append_chars (completes "func te")
	builder.AddLine("    body1")       // addition
	builder.AddLine("")                // BLANK LINE - should be addition!
	builder.AddLine("func test2() {}") // addition
	builder.AddLine("    body2")       // addition
	builder.AddLine("")                // BLANK LINE - should be addition!
	builder.AddLine("func test3() {}") // addition

	result := builder.Finalize()
	assert.NotNil(t, result, "expected staging result")
	assert.Equal(t, 1, len(result.Stages), "stage count")

	stage := result.Stages[0]

	// Verify all 7 lines (from line 3 onwards) are in the stage
	assert.Equal(t, 7, len(stage.Lines), "stage should have 7 lines")

	// Verify blank lines are included in changes
	// Line 1 (relative): "func test1() {}" - append_chars
	// Line 2 (relative): "    body1" - addition
	// Line 3 (relative): "" - addition (BLANK LINE)
	// Line 4 (relative): "func test2() {}" - addition
	// Line 5 (relative): "    body2" - addition
	// Line 6 (relative): "" - addition (BLANK LINE)
	// Line 7 (relative): "func test3() {}" - addition

	// Check that blank lines at relative positions 3 and 6 are additions
	change3, ok := stage.Changes[3]
	assert.True(t, ok, "should have change at relative line 3 (blank line)")
	assert.Equal(t, ChangeAddition, change3.Type, "blank line should be addition")
	assert.Equal(t, "", change3.Content, "blank line content should be empty")

	change6, ok := stage.Changes[6]
	assert.True(t, ok, "should have change at relative line 6 (blank line)")
	assert.Equal(t, ChangeAddition, change6.Type, "blank line should be addition")
	assert.Equal(t, "", change6.Content, "blank line content should be empty")

	// Verify groups include all lines (no gaps)
	totalLinesInGroups := 0
	for _, g := range stage.Groups {
		totalLinesInGroups += len(g.Lines)
	}
	assert.Equal(t, 7, totalLinesInGroups, "groups should cover all 7 lines including blank lines")
}

// TestIncrementalDiffBuilder_EmptyLineFilledWithContent verifies that when
// an empty line at the expected position is filled with content, it's detected
// as a modification (append_chars), not an addition.
func TestIncrementalDiffBuilder_EmptyLineFilledWithContent(t *testing.T) {
	oldLines := []string{"header", "", ""}
	builder := NewIncrementalDiffBuilder(oldLines)

	// Line 1: exact match
	change1 := builder.AddLine("header")
	assert.Nil(t, change1, "expected no change for exact match")

	// Line 2: exact match (empty)
	change2 := builder.AddLine("")
	assert.Nil(t, change2, "expected no change for empty line match")

	// Line 3: filling empty line with content should be append_chars
	change3 := builder.AddLine("new content here")
	assert.NotNil(t, change3, "expected change for filling empty line")
	assert.Equal(t, ChangeAppendChars, change3.Type, "change type")
	assert.Equal(t, 3, change3.OldLineNum, "old line num")
	assert.Equal(t, 3, change3.NewLineNum, "new line num")
	assert.Equal(t, "", change3.OldContent, "old content")
	assert.Equal(t, "new content here", change3.Content, "new content")

	// Line 4: addition beyond original file
	change4 := builder.AddLine("another line")
	assert.NotNil(t, change4, "expected change for addition")
	assert.Equal(t, ChangeAddition, change4.Type, "addition type")
}

// TestIncrementalStageBuilder_EmptyLineFilledWithContent verifies that when
// an empty line is filled with content, the stage builder correctly produces
// append_chars groups.
func TestIncrementalStageBuilder_EmptyLineFilledWithContent(t *testing.T) {
	oldLines := []string{"header", "", ""}
	builder := NewIncrementalStageBuilder(
		oldLines,
		1,    // baseLineOffset
		10,   // proximityThreshold
		0,    // maxVisibleLines (disabled)
		0, 0, // viewport disabled
		3, 0, // cursorRow, cursorCol
		"test.txt",
	)

	builder.AddLine("header")
	builder.AddLine("")
	builder.AddLine("new content here")
	builder.AddLine("another line")

	result := builder.Finalize()
	assert.NotNil(t, result, "expected staging result")
	assert.Equal(t, 1, len(result.Stages), "stage count")

	stage := result.Stages[0]

	// First change should be append_chars (filling empty line)
	change1, ok := stage.Changes[1]
	assert.True(t, ok, "should have change at relative line 1")
	assert.Equal(t, ChangeAppendChars, change1.Type, "change type")

	// First group should be modification with append_chars hint
	assert.True(t, len(stage.Groups) >= 1, "should have at least 1 group")
	firstGroup := stage.Groups[0]
	assert.Equal(t, "modification", firstGroup.Type, "group type")
	assert.Equal(t, "append_chars", firstGroup.RenderHint, "render hint")
	assert.Equal(t, 3, firstGroup.BufferLine, "buffer line")
}

// TestIncrementalStageBuilder_TrailingWhitespaceModification verifies that when the old line
// has trailing whitespace, the prefix matching still correctly identifies it as a modification.
// Without this fix, the modification would be classified as an addition during streaming,
// causing buffer_line to be off by 1.
func TestIncrementalStageBuilder_TrailingWhitespaceModification(t *testing.T) {
	oldLines := []string{
		"func main() {",
		"    x  ", // trailing whitespace
	}

	builder := NewIncrementalStageBuilder(
		oldLines,
		1,    // baseLineOffset
		10,   // proximityThreshold
		0,    // maxVisibleLines (disabled)
		0, 0, // viewport disabled
		2, 5, // cursorRow=2, cursorCol=5
		"test.go",
	)

	builder.AddLine("func main() {")
	builder.AddLine("    x = getValue()")      // modification of "    x  "
	builder.AddLine("    y = processValue(x)") // addition

	result := builder.Finalize()
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

// TestIncrementalStageBuilder_WhitespaceOnlyLineModification verifies that when the old line
// is whitespace-only (not empty), the matching correctly identifies it as a modification target.
func TestIncrementalStageBuilder_WhitespaceOnlyLineModification(t *testing.T) {
	oldLines := []string{
		"func main() {",
		"    ", // whitespace-only line (4 spaces)
	}

	builder := NewIncrementalStageBuilder(
		oldLines,
		1,    // baseLineOffset
		10,   // proximityThreshold
		0,    // maxVisibleLines (disabled)
		0, 0, // viewport disabled
		2, 4, // cursorRow=2, cursorCol=4
		"test.go",
	)

	builder.AddLine("func main() {")
	builder.AddLine("    x = getValue()")      // modification of whitespace-only line
	builder.AddLine("    y = processValue(x)") // addition

	result := builder.Finalize()
	assert.NotNil(t, result, "expected staging result")
	assert.Equal(t, 1, len(result.Stages), "stage count")

	stage := result.Stages[0]

	// BufferStart should be 2 (where the whitespace-only line is)
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

// TestLineSimilarity_EdgeCases tests similarity calculation edge cases.
func TestLineSimilarity_EdgeCases(t *testing.T) {
	tests := []struct {
		name   string
		line1  string
		line2  string
		minSim float64
		maxSim float64
	}{
		{"both empty", "", "", 1.0, 1.0},
		{"one empty", "content", "", 0.0, 0.1},
		{"single char same", "x", "x", 1.0, 1.0},
		{"single char different", "x", "y", 0.0, 0.5},
		{"whitespace same", "   ", "   ", 1.0, 1.0},
		{"whitespace different", "   ", "\t\t", 0.0, 0.5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sim := LineSimilarity(tt.line1, tt.line2)
			assert.True(t, sim >= tt.minSim && sim <= tt.maxSim, "similarity in expected range")
		})
	}
}

// TestIncrementalStageBuilder_ModificationBufferLineUsesOldPosition verifies that
// modification groups have BufferLine computed from their old line position,
// not their relative position in the new content.
func TestIncrementalStageBuilder_ModificationBufferLineUsesOldPosition(t *testing.T) {
	// Old content: 2 lines at buffer positions 5-6
	// New content: 4 lines where:
	// - Lines 1-2 are additions
	// - Line 3 modifies old line 2 (buffer line 6)
	// - Line 4 is an addition
	oldLines := []string{
		"first line",
		"second line",
	}
	builder := NewIncrementalStageBuilder(
		oldLines,
		5,    // baseLineOffset - old lines are at buffer 5-6
		10,   // proximityThreshold
		0,    // maxVisibleLines (disabled)
		0, 0, // viewport disabled
		6, 0, // cursorRow, cursorCol
		"test.go",
	)

	// Stream the new content
	builder.AddLine("new addition 1")
	builder.AddLine("new addition 2")
	builder.AddLine("second line modified") // modifies old line 2
	builder.AddLine("new addition 3")

	result := builder.Finalize()
	assert.NotNil(t, result, "result should not be nil")
	assert.Equal(t, 1, len(result.Stages), "should have 1 stage")

	stage := result.Stages[0]

	// Find the modification group
	var modGroup *Group
	for _, g := range stage.Groups {
		if g.Type == "modification" {
			modGroup = g
			break
		}
	}

	assert.NotNil(t, modGroup, "should have a modification group")
	// Modification of old line 2 (buffer line 6) should have BufferLine=6
	// Even though it's at relative position 3 in the new content
	assert.Equal(t, 6, modGroup.BufferLine,
		"modification BufferLine should match old line position (6), not relative position")
}

// TestIncrementalStageBuilder_AdditionsBeforeCursorModificationAnchoredAtCursor
// verifies that additions preceding the cursor line's modification are anchored
// at the cursor line, so they render directly above the cursor.
func TestIncrementalStageBuilder_AdditionsBeforeCursorModificationAnchoredAtCursor(t *testing.T) {
	// Old content: 2 lines at buffer positions 5-6
	// Cursor is on buffer line 6
	// New content has additions before the cursor line modification
	oldLines := []string{
		"first line",
		"cursor line content",
	}
	builder := NewIncrementalStageBuilder(
		oldLines,
		5,    // baseLineOffset
		10,   // proximityThreshold
		0,    // maxVisibleLines (disabled)
		0, 0, // viewport disabled
		6, 0, // cursorRow, cursorCol
		"test.go",
	)

	// Stream: first line modified, additions inserted, cursor line modified
	builder.AddLine("first line modified")
	builder.AddLine("added line 1")
	builder.AddLine("added line 2")
	builder.AddLine("cursor line replaced")

	result := builder.Finalize()
	assert.NotNil(t, result, "result should not be nil")
	assert.Equal(t, 1, len(result.Stages), "should have 1 stage")

	stage := result.Stages[0]

	// Find the cursor line modification
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

// TestIncrementalStageBuilder_WhitespaceLineExpansion tests the scenario where
// a whitespace-only line on the cursor row is expanded with additions before it.
// This closely matches the log scenario where:
// - Buffer line 5: "" (empty line)
// - Buffer line 6: "        " (8 spaces, cursor line)
// - Completion expands to 4 lines with additions before the modification
func TestIncrementalStageBuilder_WhitespaceLineExpansion(t *testing.T) {
	oldLines := []string{
		"",        // buffer line 5
		"        ", // buffer line 6 (cursor line)
	}
	builder := NewIncrementalStageBuilder(
		oldLines,
		5,    // baseLineOffset
		10,   // proximityThreshold
		0,    // maxVisibleLines (disabled)
		0, 0, // viewport disabled
		6, 0, // cursorRow, cursorCol
		"test.py",
	)

	// Stream the completion: adds content before cursor line, then modifies cursor line
	builder.AddLine("    Parameters")                    // modifies empty line or addition
	builder.AddLine("    ----------")                    // addition
	builder.AddLine("    rA : numpy array")              // modifies whitespace line
	builder.AddLine("        The coordinates of point A.") // addition

	result := builder.Finalize()
	assert.NotNil(t, result, "result should not be nil")
	assert.Equal(t, 1, len(result.Stages), "should have 1 stage")

	stage := result.Stages[0]

	// Find the modification of the whitespace line (cursor line)
	var cursorMod *Group
	for _, g := range stage.Groups {
		if g.Type == "modification" && len(g.OldLines) > 0 && g.OldLines[0] == "        " {
			cursorMod = g
			break
		}
	}

	assert.NotNil(t, cursorMod, "should have modification for whitespace line")
	// The modification should have BufferLine=6 (where the whitespace was)
	assert.Equal(t, 6, cursorMod.BufferLine,
		"modification of cursor line should have BufferLine=6")

	// Find additions that come before the cursor line modification
	for _, g := range stage.Groups {
		if g.Type == "addition" && g.StartLine < cursorMod.StartLine {
			// Additions before cursor modification should be anchored at cursor line (6)
			assert.Equal(t, 6, g.BufferLine,
				"additions before cursor line should be anchored at cursor line (6)")
		}
	}

	// Find additions that come after the cursor line modification
	for _, g := range stage.Groups {
		if g.Type == "addition" && g.StartLine > cursorMod.StartLine {
			// Additions after modification should be below it (BufferLine = cursorMod.BufferLine + 1)
			assert.Equal(t, 7, g.BufferLine,
				"additions after cursor line modification should have BufferLine=7")
		}
	}
}

// TestIncrementalStageBuilder_LowSimilarityModification tests that when streaming
// classifies a change as an addition (due to low similarity), the fallback matching
// in finalizeCurrentStage correctly identifies it as a modification, resulting in
// correct BufferStart computation.
//
// This was a bug where low-similarity modifications (similarity < 0.35) were
// classified as additions during streaming, causing the "pure additions" logic
// to incorrectly add +1 to BufferStart.
func TestIncrementalStageBuilder_LowSimilarityModification(t *testing.T) {
	tests := []struct {
		name           string
		oldLine        string
		newLine        string
		baseLineOffset int
		wantBufferLine int
	}{
		{
			name:           "single line with low similarity",
			oldLine:        `console.log("");`,
			newLine:        `console.log("OPENROUTER_API_KEY", process.env.OPENROUTER_API_KEY);`,
			baseLineOffset: 1,
			wantBufferLine: 1,
		},
		{
			name:           "empty parens to full content",
			oldLine:        `console.log();`,
			newLine:        `console.log("OPENROUTER_API_KEY", process.env.OPENROUTER_API_KEY || "YOUR_API_KEY");`,
			baseLineOffset: 1,
			wantBufferLine: 1,
		},
		{
			name:           "with non-zero base offset",
			oldLine:        `return null;`,
			newLine:        `return { data: response.data, status: response.status };`,
			baseLineOffset: 5,
			wantBufferLine: 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldLines := []string{tt.oldLine}

			builder := NewIncrementalStageBuilder(
				oldLines,
				tt.baseLineOffset,
				10,   // proximityThreshold
				0,    // maxVisibleLines
				0, 0, // viewport disabled
				tt.baseLineOffset, 0, // cursor at the line being modified
				"test.ts",
			)

			builder.AddLine(tt.newLine)

			result := builder.Finalize()
			assert.NotNil(t, result, "result should not be nil")
			assert.Equal(t, 1, len(result.Stages), "should have 1 stage")

			stage := result.Stages[0]
			assert.Equal(t, tt.wantBufferLine, stage.BufferStart,
				"BufferStart should match baseLineOffset for single-line modification")
			assert.Equal(t, tt.wantBufferLine, stage.BufferEnd,
				"BufferEnd should match BufferStart for single-line")

			assert.True(t, len(stage.Groups) > 0, "should have groups")
			if len(stage.Groups) > 0 {
				group := stage.Groups[0]
				assert.Equal(t, tt.wantBufferLine, group.BufferLine,
					"Group BufferLine should match expected")
				assert.Equal(t, "modification", group.Type,
					"should be classified as modification after fallback matching")
			}
		})
	}
}
