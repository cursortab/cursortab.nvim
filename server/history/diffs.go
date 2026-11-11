package history

import (
	"slices"
	"strings"
	"time"

	"cursortab/types"

	"github.com/sergi/go-diff/diffmatchpatch"
)

const (
	// MaxDiffEntries is the maximum number of diff entries kept in history (FIFO).
	MaxDiffEntries = 5

	// ProximityLines is the largest number of unchanged lines between two
	// changed regions of one commit that still records them as one entry.
	ProximityLines = 8

	// PauseThreshold is the minimum time gap between edits to prevent coalescing.
	PauseThreshold = time.Second

	// IdleDecayDuration is how long before stale diff entries are dropped.
	IdleDecayDuration = 5 * time.Minute
)

// hunk is one recorded edit: original, at startLine, replaced by updated.
// startLine refers to the file as it was when the edit happened.
type hunk struct {
	startLine   int // 1-indexed
	original    []string
	updated     []string
	source      types.DiffSource
	timestampNs int64
}

func (h hunk) entry() *types.DiffEntry {
	return &types.DiffEntry{
		Original:    strings.Join(h.original, "\n"),
		Updated:     strings.Join(h.updated, "\n"),
		Source:      h.source,
		TimestampNs: h.timestampNs,
		StartLine:   h.startLine,
	}
}

// hunkOf converts an entry to a hunk. Empty text is read as no lines.
func hunkOf(e *types.DiffEntry) hunk {
	lines := func(s string) []string {
		if s == "" {
			return nil
		}
		return strings.Split(s, "\n")
	}
	return hunk{e.StartLine, lines(e.Original), lines(e.Updated), e.Source, e.TimestampNs}
}

// diffHunks returns the edits that turn oldLines, which start at baseLine,
// into newLines. Changed regions at most ProximityLines unchanged lines apart
// form one hunk that includes those lines. Each hunk's start line accounts
// for the line changes of the hunks before it.
func diffHunks(oldLines, newLines []string, baseLine int, source types.DiffSource, nowNs int64) []hunk {
	if slices.Equal(oldLines, newLines) {
		return nil
	}
	// Only the part between the common prefix and suffix is diffed, so the cost
	// follows the edit, not the file.
	prefix := 0
	for prefix < min(len(oldLines), len(newLines)) && oldLines[prefix] == newLines[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < min(len(oldLines), len(newLines))-prefix &&
		oldLines[len(oldLines)-1-suffix] == newLines[len(newLines)-1-suffix] {
		suffix++
	}
	oldLines, newLines = oldLines[prefix:len(oldLines)-suffix], newLines[prefix:len(newLines)-suffix]
	baseLine += prefix

	dmp := diffmatchpatch.New()
	chars1, chars2, lineArray := dmp.DiffLinesToChars(lineText(oldLines), lineText(newLines))
	diffs := dmp.DiffCharsToLines(dmp.DiffMain(chars1, chars2, false), lineArray)

	var hunks []hunk
	var cur *hunk
	var gap []string
	line := baseLine // in the content with the earlier hunks applied
	for _, d := range diffs {
		lines := strings.SplitAfter(d.Text, "\n")
		lines = lines[:len(lines)-1]
		for i := range lines {
			lines[i] = strings.TrimSuffix(lines[i], "\n")
		}
		if d.Type == diffmatchpatch.DiffEqual {
			if cur != nil {
				gap = lines
			}
			line += len(lines)
			continue
		}
		if cur != nil && gap != nil && len(gap) > ProximityLines {
			hunks = append(hunks, *cur)
			cur = nil
		}
		if cur == nil {
			cur = &hunk{startLine: line, source: source, timestampNs: nowNs}
		} else if gap != nil {
			cur.original = append(cur.original, gap...)
			cur.updated = append(cur.updated, gap...)
		}
		gap = nil
		if d.Type == diffmatchpatch.DiffDelete {
			cur.original = append(cur.original, lines...)
		} else {
			cur.updated = append(cur.updated, lines...)
			line += len(lines)
		}
	}
	if cur != nil {
		hunks = append(hunks, *cur)
	}
	return hunks
}

// lineText joins lines with a newline after each, the line-diff input format.
func lineText(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// appendAndCoalesce adds the hunks of one commit to history. A hunk merges
// into the previous one when both have the same source, are close in time,
// and the merge is exact (see compose). Enforces a FIFO cap of
// MaxDiffEntries.
func appendAndCoalesce(history []hunk, hunks []hunk) []hunk {
	for _, h := range hunks {
		if n := len(history); n > 0 {
			last := history[n-1]
			if last.source == h.source && h.timestampNs-last.timestampNs < int64(PauseThreshold) {
				if merged, ok := compose(last, h); ok {
					if slices.Equal(merged.original, merged.updated) {
						// The edit undid the previous one.
						history = history[:n-1]
					} else {
						history[n-1] = merged
					}
					continue
				}
			}
		}
		history = append(history, h)
	}
	if len(history) > MaxDiffEntries {
		history = history[len(history)-MaxDiffEntries:]
	}
	return history
}

// compose returns the single edit equal to applying a, then b, where b is in
// the coordinates of the content after a. It succeeds when b lies within the
// lines a produced, or directly touches them; otherwise the lines between
// the two edits are unknown and they cannot be merged.
func compose(a, b hunk) (hunk, bool) {
	merged := hunk{source: a.source, timestampNs: b.timestampNs}
	switch k := b.startLine - a.startLine; {
	case k >= 0 && k+len(b.original) <= len(a.updated) && slices.Equal(a.updated[k:k+len(b.original)], b.original):
		merged.startLine = a.startLine
		merged.original = a.original
		merged.updated = slices.Concat(a.updated[:k], b.updated, a.updated[k+len(b.original):])
	case k == len(a.updated):
		merged.startLine = a.startLine
		merged.original = slices.Concat(a.original, b.original)
		merged.updated = slices.Concat(a.updated, b.updated)
	case b.startLine+len(b.original) == a.startLine:
		merged.startLine = b.startLine
		merged.original = slices.Concat(b.original, a.original)
		merged.updated = slices.Concat(b.updated, a.updated)
	default:
		return hunk{}, false
	}
	return merged, true
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

// Process applies the full send-time processing pipeline:
// 1. Idle decay (drop stale entries)
// 2. Net-change collapsing (cancel out inverse pairs)
// 3. Deduplication (remove identical entries)
func Process(entries []*types.DiffEntry, nowNs int64) []*types.DiffEntry {
	entries = DecayByAge(entries, nowNs, IdleDecayDuration)
	entries = CollapseNetChanges(entries)
	entries = Deduplicate(entries)
	return entries
}

// normalizeText trims whitespace for comparison purposes.
func normalizeText(s string) string {
	return strings.TrimSpace(s)
}
