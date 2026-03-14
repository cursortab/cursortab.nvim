package buffer

import (
	"cursortab/assert"
	"cursortab/types"
	"testing"
	"time"
)

func TestAppendAndCoalesce_FIFOCap(t *testing.T) {
	var history []*types.DiffEntry

	// Add 7 entries with different timestamps (spread apart to prevent coalescing)
	for i := 0; i < 7; i++ {
		entry := &types.DiffEntry{
			Original:    "old",
			Updated:     "new",
			Source:      types.DiffSourceManual,
			TimestampNs: int64(i) * int64(2*time.Second),
			StartLine:   i*20 + 1, // Spread apart to prevent proximity coalescing
		}
		history = appendAndCoalesce(history, []*types.DiffEntry{entry})
	}

	assert.Equal(t, MaxDiffEntries, len(history), "should cap at MaxDiffEntries")
	// Most recent entries should be kept
	assert.Equal(t, int64(6)*int64(2*time.Second), history[len(history)-1].TimestampNs, "last entry should be most recent")
}

func TestAppendAndCoalesce_ProximityMerge(t *testing.T) {
	now := time.Now().UnixNano()

	history := []*types.DiffEntry{{
		Original:    "line A",
		Updated:     "line B",
		Source:      types.DiffSourceManual,
		TimestampNs: now,
		StartLine:   10,
	}}

	// Same source, within proximity, within time threshold → should merge
	newEntry := &types.DiffEntry{
		Original:    "line C",
		Updated:     "line D",
		Source:      types.DiffSourceManual,
		TimestampNs: now + int64(500*time.Millisecond),
		StartLine:   12, // Within 8 lines
	}

	result := appendAndCoalesce(history, []*types.DiffEntry{newEntry})
	assert.Equal(t, 1, len(result), "should coalesce into one entry")
	assert.Equal(t, "line A\nline C", result[0].Original, "originals merged")
	assert.Equal(t, "line B\nline D", result[0].Updated, "updates merged")
}

func TestAppendAndCoalesce_NoMergeAcrossPause(t *testing.T) {
	now := time.Now().UnixNano()

	history := []*types.DiffEntry{{
		Original:    "line A",
		Updated:     "line B",
		Source:      types.DiffSourceManual,
		TimestampNs: now,
		StartLine:   10,
	}}

	// Same source, within proximity, but >1s gap → should NOT merge
	newEntry := &types.DiffEntry{
		Original:    "line C",
		Updated:     "line D",
		Source:      types.DiffSourceManual,
		TimestampNs: now + int64(2*time.Second),
		StartLine:   12,
	}

	result := appendAndCoalesce(history, []*types.DiffEntry{newEntry})
	assert.Equal(t, 2, len(result), "should not coalesce across pause")
}

func TestAppendAndCoalesce_NoMergeAcrossSource(t *testing.T) {
	now := time.Now().UnixNano()

	history := []*types.DiffEntry{{
		Original:    "line A",
		Updated:     "line B",
		Source:      types.DiffSourceManual,
		TimestampNs: now,
		StartLine:   10,
	}}

	// Different source → should NOT merge
	newEntry := &types.DiffEntry{
		Original:    "line C",
		Updated:     "line D",
		Source:      types.DiffSourcePredicted,
		TimestampNs: now + int64(100*time.Millisecond),
		StartLine:   12,
	}

	result := appendAndCoalesce(history, []*types.DiffEntry{newEntry})
	assert.Equal(t, 2, len(result), "should not coalesce across sources")
}

func TestCollapseNetChanges_InversePair(t *testing.T) {
	entries := []*types.DiffEntry{
		{Original: "", Updated: "hello"},
		{Original: "hello", Updated: ""},
	}

	result := CollapseNetChanges(entries)
	assert.Equal(t, 0, len(result), "inverse pair should be collapsed")
}

func TestCollapseNetChanges_NoInverse(t *testing.T) {
	entries := []*types.DiffEntry{
		{Original: "foo", Updated: "bar"},
		{Original: "baz", Updated: "qux"},
	}

	result := CollapseNetChanges(entries)
	assert.Equal(t, 2, len(result), "non-inverse entries should remain")
}

func TestCollapseNetChanges_PartialInverse(t *testing.T) {
	entries := []*types.DiffEntry{
		{Original: "", Updated: "hello"},  // added
		{Original: "world", Updated: "!"}, // modified
		{Original: "hello", Updated: ""},  // reverted the add
	}

	result := CollapseNetChanges(entries)
	assert.Equal(t, 1, len(result), "only the non-inverse entry should remain")
	assert.Equal(t, "world", result[0].Original, "remaining entry")
}

func TestDeduplicate(t *testing.T) {
	entries := []*types.DiffEntry{
		{Original: "a", Updated: "b", TimestampNs: 1},
		{Original: "c", Updated: "d", TimestampNs: 2},
		{Original: "a", Updated: "b", TimestampNs: 3},
	}

	result := Deduplicate(entries)
	assert.Equal(t, 2, len(result), "duplicate should be removed")
	// Should keep the last occurrence
	assert.Equal(t, int64(2), result[0].TimestampNs, "first unique entry kept")
	assert.Equal(t, int64(3), result[1].TimestampNs, "last occurrence of duplicate kept")
}

func TestDecayByAge(t *testing.T) {
	now := time.Now().UnixNano()
	entries := []*types.DiffEntry{
		{Original: "old", Updated: "new", TimestampNs: now - int64(10*time.Minute)},
		{Original: "recent", Updated: "new", TimestampNs: now - int64(1*time.Minute)},
	}

	result := DecayByAge(entries, now, 5*time.Minute)
	assert.Equal(t, 1, len(result), "old entry should be decayed")
	assert.Equal(t, "recent", result[0].Original, "recent entry remains")
}

func TestProcessDiffHistory_FullPipeline(t *testing.T) {
	now := time.Now().UnixNano()

	entries := []*types.DiffEntry{
		// Old entry (should be decayed)
		{Original: "ancient", Updated: "old", TimestampNs: now - int64(10*time.Minute)},
		// Edit + revert pair (should be collapsed)
		{Original: "", Updated: "hello", TimestampNs: now - int64(30*time.Second)},
		{Original: "hello", Updated: "", TimestampNs: now - int64(20*time.Second)},
		// Duplicate entries (should be deduped)
		{Original: "foo", Updated: "bar", TimestampNs: now - int64(10*time.Second)},
		{Original: "foo", Updated: "bar", TimestampNs: now - int64(5*time.Second)},
		// Normal entry (should survive)
		{Original: "baz", Updated: "qux", TimestampNs: now - int64(1*time.Second)},
	}

	result := ProcessDiffHistory(entries, now)
	assert.Equal(t, 2, len(result), "should have 2 entries after full pipeline")
}

func TestClearDiffHistory(t *testing.T) {
	buf := New(Config{NsID: 1})
	buf.lines = []string{"current 1", "current 2"}
	buf.originalLines = []string{"original 1", "original 2"}
	buf.diffHistories = []*types.DiffEntry{
		{Original: "a", Updated: "b"},
	}

	buf.ClearDiffHistory()

	assert.Equal(t, 0, len(buf.diffHistories), "history should be cleared")
	assert.Equal(t, "current 1", buf.originalLines[0], "checkpoint reset to current lines")
	assert.Equal(t, "current 2", buf.originalLines[1], "checkpoint reset to current lines")
}
