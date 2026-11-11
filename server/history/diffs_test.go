package history

import (
	"cursortab/assert"
	"cursortab/types"
	"slices"
	"testing"
	"time"
)

func TestAppendAndCoalesce_FIFOCap(t *testing.T) {
	var history []hunk
	for i := range 7 {
		history = appendAndCoalesce(history, []hunk{{
			startLine:   i*20 + 1,
			original:    []string{"old"},
			updated:     []string{"new"},
			source:      types.DiffSourceManual,
			timestampNs: int64(i) * int64(2*time.Second),
		}})
	}

	assert.Equal(t, MaxDiffEntries, len(history), "should cap at MaxDiffEntries")
	assert.Equal(t, int64(6)*int64(2*time.Second), history[len(history)-1].timestampNs, "last entry should be most recent")
}

func TestAppendAndCoalesce(t *testing.T) {
	at := func(start int, original, updated []string, offsetMs int) hunk {
		return hunk{start, original, updated, types.DiffSourceManual, int64(offsetMs) * int64(time.Millisecond)}
	}
	l := func(lines ...string) []string { return lines }
	tests := []struct {
		name  string
		first hunk
		next  []hunk
		want  []hunk
	}{
		{
			name:  "same line edited again composes",
			first: at(5, l("hello"), l("hello! "), 0),
			next:  []hunk{at(5, l("hello! "), l("hello! big "), 100)},
			want:  []hunk{at(5, l("hello"), l("hello! big "), 100)},
		},
		{
			name:  "edit inside a multi-line result composes",
			first: at(5, l("a"), l("a", "b", "c"), 0),
			next:  []hunk{at(6, l("b"), l("B"), 100)},
			want:  []hunk{at(5, l("a"), l("a", "B", "c"), 100)},
		},
		{
			name:  "line right after the result joins",
			first: at(5, l("a"), l("A"), 0),
			next:  []hunk{at(6, l("b"), l("B"), 100)},
			want:  []hunk{at(5, l("a", "b"), l("A", "B"), 100)},
		},
		{
			name:  "line right before the result joins",
			first: at(5, l("b"), l("B"), 0),
			next:  []hunk{at(4, l("a"), l("A"), 100)},
			want:  []hunk{at(4, l("a", "b"), l("A", "B"), 100)},
		},
		{
			name:  "insertion after a deletion composes",
			first: at(5, l("old"), nil, 0),
			next:  []hunk{at(5, nil, l("new"), 100)},
			want:  []hunk{at(5, l("old"), l("new"), 100)},
		},
		{
			name:  "deleted blank line keeps the next edit in place",
			first: at(5, l(""), nil, 0),
			next:  []hunk{at(5, l("x"), l("X"), 100)},
			want:  []hunk{at(5, l("", "x"), l("X"), 100)},
		},
		{
			name:  "edit undone before a pause leaves no entry",
			first: at(5, l("a"), l("A"), 0),
			next:  []hunk{at(5, l("A"), l("a"), 100)},
			want:  []hunk{},
		},
		{
			name:  "edits across a pause stay separate",
			first: at(5, l("a"), l("A"), 0),
			next:  []hunk{at(5, l("A"), l("AA"), 2000)},
			want:  []hunk{at(5, l("a"), l("A"), 0), at(5, l("A"), l("AA"), 2000)},
		},
		{
			name:  "edits of different sources stay separate",
			first: at(5, l("a"), l("A"), 0),
			next:  []hunk{{5, l("A"), l("AA"), types.DiffSourcePredicted, int64(100 * time.Millisecond)}},
			want:  []hunk{at(5, l("a"), l("A"), 0), {5, l("A"), l("AA"), types.DiffSourcePredicted, int64(100 * time.Millisecond)}},
		},
		{
			name:  "lines with an unknown gap stay separate",
			first: at(10, l("line A"), l("line B"), 0),
			next:  []hunk{at(12, l("line C"), l("line D"), 100)},
			want:  []hunk{at(10, l("line A"), l("line B"), 0), at(12, l("line C"), l("line D"), 100)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, appendAndCoalesce([]hunk{tt.first}, tt.next), "history")
		})
	}
}

func TestDiffHunks(t *testing.T) {
	l := func(lines ...string) []string { return lines }
	at := func(start int, original, updated []string) hunk {
		return hunk{start, original, updated, types.DiffSourceManual, 0}
	}
	far := make([]string, ProximityLines+1)
	for i := range far {
		far[i] = "keep"
	}
	tests := []struct {
		name     string
		old, new []string
		want     []hunk
	}{
		{"identical content", l("a"), l("a"), nil},
		{"one changed line", l("x", "a", "y"), l("x", "b", "y"), []hunk{at(11, l("a"), l("b"))}},
		{"deleted line", l("a", "b"), l("a"), []hunk{at(11, l("b"), nil)}},
		{"blank line replaced", l("a", "", "c"), l("a", "b", "c"), []hunk{at(11, l(""), l("b"))}},
		{"blank line deleted", l("a", "", "c"), l("a", "c"), []hunk{at(11, l(""), nil)}},
		{
			"nearby changes form one hunk with the lines between",
			l("a", "k", "b"), l("A", "k", "B"),
			[]hunk{at(10, l("a", "k", "b"), l("A", "k", "B"))},
		},
		{
			"distant changes stay separate and later ones shift",
			slices.Concat(l("a"), far, l("b")), slices.Concat(l("A", "A2"), far, l("B")),
			[]hunk{at(10, l("a"), l("A", "A2")), at(12+len(far), l("b"), l("B"))},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, diffHunks(tt.old, tt.new, 10, types.DiffSourceManual, 0), "hunks")
		})
	}
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

	result := Process(entries, now)
	assert.Equal(t, 2, len(result), "should have 2 entries after full pipeline")
}
