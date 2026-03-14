package buffer

import (
	"cursortab/types"
	"strings"
	"time"
)

const (
	// MaxDiffEntries is the maximum number of diff entries kept in history (FIFO).
	MaxDiffEntries = 5

	// ProximityLines is the line distance threshold for coalescing edits.
	ProximityLines = 8

	// PauseThreshold is the minimum time gap between edits to prevent coalescing.
	PauseThreshold = time.Second

	// IdleDecayDuration is how long before stale diff entries are dropped.
	IdleDecayDuration = 5 * time.Minute
)

// appendAndCoalesce adds new entries to history, coalescing with the last entry
// when edits are close in both time and line proximity (same source).
// Enforces a FIFO cap of MaxDiffEntries.
func appendAndCoalesce(history []*types.DiffEntry, entries []*types.DiffEntry) []*types.DiffEntry {
	for _, entry := range entries {
		history = coalesceOrAppend(history, entry)
	}

	// FIFO cap: keep only the most recent entries
	if len(history) > MaxDiffEntries {
		history = history[len(history)-MaxDiffEntries:]
	}

	return history
}

// coalesceOrAppend merges entry into the last history entry if they're close,
// otherwise appends it.
func coalesceOrAppend(history []*types.DiffEntry, entry *types.DiffEntry) []*types.DiffEntry {
	if len(history) == 0 {
		return append(history, entry)
	}

	last := history[len(history)-1]

	// Only coalesce if same source and within time/proximity thresholds
	if last.Source == entry.Source &&
		entry.TimestampNs-last.TimestampNs < int64(PauseThreshold) &&
		withinProximity(last.StartLine, entry.StartLine) {

		// Merge: combine original and updated texts
		history[len(history)-1] = mergeEntries(last, entry)
		return history
	}

	return append(history, entry)
}

// withinProximity returns true if two line positions are within ProximityLines of each other.
func withinProximity(lineA, lineB int) bool {
	diff := lineA - lineB
	if diff < 0 {
		diff = -diff
	}
	return diff <= ProximityLines
}

// mergeEntries combines two entries into one, keeping the wider range.
func mergeEntries(a, b *types.DiffEntry) *types.DiffEntry {
	startLine := a.StartLine
	if b.StartLine < startLine {
		startLine = b.StartLine
	}

	origA := a.Original
	origB := b.Original
	updA := a.Updated
	updB := b.Updated

	// Combine texts with newline separator when both are non-empty
	original := joinNonEmpty(origA, origB)
	updated := joinNonEmpty(updA, updB)

	return &types.DiffEntry{
		Original:    original,
		Updated:     updated,
		Source:      a.Source,
		TimestampNs: b.TimestampNs, // Use the newer timestamp
		StartLine:   startLine,
	}
}

// joinNonEmpty joins two strings with a newline if both are non-empty.
func joinNonEmpty(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "\n" + b
}

// CollapseNetChanges removes inverse pairs where A→B is followed by B→A,
// since the net effect is no change. Handles both exact inverses and
// partial overlaps within the same region.
func CollapseNetChanges(entries []*types.DiffEntry) []*types.DiffEntry {
	if len(entries) <= 1 {
		return entries
	}

	// Mark entries for removal
	removed := make([]bool, len(entries))

	for i := 0; i < len(entries); i++ {
		if removed[i] {
			continue
		}
		for j := i + 1; j < len(entries); j++ {
			if removed[j] {
				continue
			}
			if isInversePair(entries[i], entries[j]) {
				removed[i] = true
				removed[j] = true
				break
			}
		}
	}

	result := make([]*types.DiffEntry, 0, len(entries))
	for i, entry := range entries {
		if !removed[i] {
			result = append(result, entry)
		}
	}
	return result
}

// isInversePair returns true if b undoes a (a.Updated == b.Original and b.Updated == a.Original).
func isInversePair(a, b *types.DiffEntry) bool {
	return normalizeText(a.Updated) == normalizeText(b.Original) &&
		normalizeText(b.Updated) == normalizeText(a.Original)
}

// Deduplicate removes entries with identical Original and Updated content,
// keeping the most recent (last) occurrence.
func Deduplicate(entries []*types.DiffEntry) []*types.DiffEntry {
	if len(entries) <= 1 {
		return entries
	}

	type key struct{ original, updated string }
	seen := make(map[key]int) // key → last index

	for i, e := range entries {
		k := key{normalizeText(e.Original), normalizeText(e.Updated)}
		seen[k] = i
	}

	result := make([]*types.DiffEntry, 0, len(seen))
	for i, e := range entries {
		k := key{normalizeText(e.Original), normalizeText(e.Updated)}
		if seen[k] == i {
			result = append(result, e)
		}
	}
	return result
}

// DecayByAge removes entries older than maxAge relative to now.
func DecayByAge(entries []*types.DiffEntry, now int64, maxAge time.Duration) []*types.DiffEntry {
	if len(entries) == 0 || maxAge <= 0 {
		return entries
	}

	cutoff := now - int64(maxAge)
	result := make([]*types.DiffEntry, 0, len(entries))
	for _, e := range entries {
		if e.TimestampNs >= cutoff {
			result = append(result, e)
		}
	}
	return result
}

// ProcessDiffHistory applies the full send-time processing pipeline:
// 1. Idle decay (drop stale entries)
// 2. Net-change collapsing (cancel out inverse pairs)
// 3. Deduplication (remove identical entries)
func ProcessDiffHistory(entries []*types.DiffEntry, nowNs int64) []*types.DiffEntry {
	entries = DecayByAge(entries, nowNs, IdleDecayDuration)
	entries = CollapseNetChanges(entries)
	entries = Deduplicate(entries)
	return entries
}

// normalizeText trims whitespace for comparison purposes.
func normalizeText(s string) string {
	return strings.TrimSpace(s)
}
