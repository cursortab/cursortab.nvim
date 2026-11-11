// Package history records per-file edit history: the manual and predicted
// edits that providers receive as recent-change context, and the first lines
// of recently used files.
package history

import (
	"path/filepath"
	"slices"
	"sort"

	"cursortab/text"
	"cursortab/types"
)

// firstLinesCount is how many leading lines of a file are kept as cross-file
// context.
const firstLinesCount = 30

// noHistoryFiles are files whose edits are never recorded. Completions work in
// them even when they are unmodified.
var noHistoryFiles = []string{"COMMIT_EDITMSG"}

// SkipHistory reports whether edits to path are not recorded.
func SkipHistory(path string) bool {
	return slices.Contains(noHistoryFiles, filepath.Base(path))
}

// Edit is a line-range replacement applied to a buffer.
type Edit struct {
	StartLine  int // 1-indexed
	EndLineInc int // 1-indexed, inclusive; StartLine-1 for a pure insertion
	Lines      []string
}

// Apply returns lines with the edit applied. The range is clamped to lines.
// Like a Neovim buffer, the result always has a line: deleting every line
// leaves one empty line.
func (e Edit) Apply(lines []string) []string {
	start := min(max(e.StartLine-1, 0), len(lines))
	end := min(max(e.EndLineInc, start), len(lines))
	if out := slices.Concat(lines[:start], e.Lines, lines[end:]); len(out) > 0 {
		return out
	}
	return []string{""}
}

type file struct {
	checkpoint []string // Baseline for the next recorded diff
	firstLines []string // Leading lines of the latest known content
	diffs      []hunk
	accessNs   int64
}

// Store holds the history of the active file and the most recently used
// other files. The empty path is the active unnamed buffer: every unnamed
// buffer starts a new history, which is dropped once another file is active
// and is never cross-file context.
type Store struct {
	files     map[string]*file
	active    string
	maxRecent int
}

// NewStore returns a store that keeps the active file plus at most maxRecent
// other files.
func NewStore(maxRecent int) *Store {
	return &Store{files: make(map[string]*file), maxRecent: maxRecent}
}

// Open makes path the active file. Its stored history is kept when the stored
// checkpoint still plausibly matches lines; otherwise lines become the new
// diff baseline.
func (s *Store) Open(path string, lines []string, nowNs int64) {
	f, ok := s.files[path]
	if !ok || path == "" || !plausiblySameFile(f.checkpoint, lines) {
		f = &file{checkpoint: slices.Clone(lines)}
		s.files[path] = f
	}
	f.firstLines = firstN(lines, firstLinesCount)
	f.accessNs = nowNs
	s.active = path
	s.trim()
}

// Rename moves the history of oldPath to newPath, for a buffer whose name or
// working directory changed.
func (s *Store) Rename(oldPath, newPath string) {
	f, ok := s.files[oldPath]
	if !ok || oldPath == newPath {
		return
	}
	delete(s.files, oldPath)
	s.files[newPath] = f
	if s.active == oldPath {
		s.active = newPath
	}
}

// Seed installs a file with existing history.
func (s *Store) Seed(path string, lines []string, diffs []*types.DiffEntry, nowNs int64) {
	s.files[path] = &file{
		checkpoint: slices.Clone(lines),
		firstLines: firstN(lines, firstLinesCount),
		diffs:      hunksOf(diffs),
		accessNs:   nowNs,
	}
	s.active = path
	s.trim()
}

// Observe records the latest content of path for cross-file context.
func (s *Store) Observe(path string, lines []string) {
	s.get(path).firstLines = firstN(lines, firstLinesCount)
}

// CommitUserEdits records the difference between the checkpoint and lines as
// manual edits. It reports whether anything changed.
func (s *Store) CommitUserEdits(path string, lines []string, nowNs int64) bool {
	f := s.get(path)
	hunks := diffHunks(f.checkpoint, lines, 1, types.DiffSourceManual, nowNs)
	if len(hunks) == 0 {
		return false
	}
	if !SkipHistory(path) {
		f.diffs = appendAndCoalesce(f.diffs, hunks)
	}
	f.checkpoint = slices.Clone(lines)
	f.firstLines = firstN(lines, firstLinesCount)
	return true
}

// CommitPredicted records an accepted completion as a predicted edit. lines is
// the buffer content after the edit.
func (s *Store) CommitPredicted(path string, edit Edit, lines []string, nowNs int64) {
	f := s.get(path)
	replaced := text.LineRange(f.checkpoint, edit.StartLine, edit.EndLineInc)
	hunks := diffHunks(replaced, edit.Lines, edit.StartLine, types.DiffSourcePredicted, nowNs)
	if !SkipHistory(path) {
		f.diffs = appendAndCoalesce(f.diffs, hunks)
	}
	f.checkpoint = slices.Clone(lines)
	f.firstLines = firstN(lines, firstLinesCount)
}

// Synced clears the history of path after the buffer was written or read;
// lines, equal to the file, become the baseline.
func (s *Store) Synced(path string, lines []string) {
	f := s.get(path)
	f.diffs = nil
	f.checkpoint = slices.Clone(lines)
	f.firstLines = firstN(lines, firstLinesCount)
}

// Diffs returns a copy of the recorded edits of path.
func (s *Store) Diffs(path string) []*types.DiffEntry {
	f, ok := s.files[path]
	if !ok {
		return nil
	}
	return entriesOf(f.diffs)
}

// RecentFile is a copy of the history of a file other than the active one.
type RecentFile struct {
	Path       string
	FirstLines []string
	Diffs      []*types.DiffEntry
	AccessNs   int64
}

// Recent returns the stored files except exclude, most recently used first.
func (s *Store) Recent(exclude string) []RecentFile {
	var recent []RecentFile
	for path, f := range s.files {
		if path == exclude || path == "" {
			continue
		}
		recent = append(recent, RecentFile{
			Path:       path,
			FirstLines: slices.Clone(f.firstLines),
			Diffs:      entriesOf(f.diffs),
			AccessNs:   f.accessNs,
		})
	}
	sort.Slice(recent, func(i, j int) bool { return recent[i].AccessNs > recent[j].AccessNs })
	return recent
}

// get returns the record of path, creating an empty one when path was not
// opened.
func (s *Store) get(path string) *file {
	f, ok := s.files[path]
	if !ok {
		f = &file{}
		s.files[path] = f
	}
	return f
}

// trim keeps the active file and the maxRecent most recently used others.
// An inactive unnamed buffer is dropped.
func (s *Store) trim() {
	if s.active != "" {
		delete(s.files, "")
	}
	var others []string
	for path := range s.files {
		if path != s.active {
			others = append(others, path)
		}
	}
	if len(others) <= s.maxRecent {
		return
	}
	sort.Slice(others, func(i, j int) bool { return s.files[others[i]].accessNs > s.files[others[j]].accessNs })
	for _, path := range others[s.maxRecent:] {
		delete(s.files, path)
	}
}

// plausiblySameFile reports whether stored content still describes current:
// the line count is close and most sampled lines match.
func plausiblySameFile(stored, current []string) bool {
	if len(stored) == 0 {
		return false
	}
	if diff := len(stored) - len(current); diff != 0 {
		if diff < 0 {
			diff = -diff
		}
		if diff > max(len(stored)/10, 10) {
			return false
		}
	}

	samples := []int{0}
	if len(current) > 2 {
		samples = append(samples, len(current)/2, len(current)-1)
	}
	mismatches := 0
	for _, i := range samples {
		if i < len(stored) && i < len(current) && stored[i] != current[i] {
			mismatches++
		}
	}
	return mismatches <= len(samples)/2
}

func firstN(lines []string, n int) []string {
	return slices.Clone(lines[:min(n, len(lines))])
}

func entriesOf(hunks []hunk) []*types.DiffEntry {
	if hunks == nil {
		return nil
	}
	out := make([]*types.DiffEntry, len(hunks))
	for i, h := range hunks {
		out[i] = h.entry()
	}
	return out
}

func hunksOf(entries []*types.DiffEntry) []hunk {
	if entries == nil {
		return nil
	}
	out := make([]hunk, len(entries))
	for i, e := range entries {
		out[i] = hunkOf(e)
	}
	return out
}
