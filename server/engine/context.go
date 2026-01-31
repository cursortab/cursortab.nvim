package engine

import (
	"sort"

	"cursortab/logger"
	"cursortab/types"
	"cursortab/utils"
)

// syncBuffer syncs the buffer state and handles file switching.
func (e *Engine) syncBuffer() {
	result, err := e.buffer.Sync(e.WorkspacePath)
	if err != nil {
		logger.Debug("sync error: %v", err)
		return
	}

	if result != nil && result.BufferChanged {
		e.handleFileSwitch(result.OldPath, result.NewPath, e.buffer.Lines())
	}
}

// newFileStateFromBuffer creates a FileState snapshot from current buffer state.
func (e *Engine) newFileStateFromBuffer() *FileState {
	return &FileState{
		PreviousLines: copyLines(e.buffer.PreviousLines()),
		DiffHistories: copyDiffs(e.buffer.DiffHistories()),
		OriginalLines: copyLines(e.buffer.OriginalLines()),
		LastAccessNs:  e.clock.Now().UnixNano(),
		Version:       e.buffer.Version(),
	}
}

// saveCurrentFileState saves the current buffer state to the file state store
func (e *Engine) saveCurrentFileState() {
	if e.buffer.Path() == "" {
		return
	}

	state := e.newFileStateFromBuffer()
	// Capture first lines for FileChunks context
	state.FirstLines = copyFirstN(e.buffer.Lines(), FileChunkLines)
	e.fileStateStore[e.buffer.Path()] = state
	e.trimFileStateStore(3) // Keep at most 3 files for FileChunks
}

// handleFileSwitch manages file state when switching between files.
func (e *Engine) handleFileSwitch(oldPath, newPath string, currentLines []string) bool {
	if oldPath == newPath {
		return false
	}

	if oldPath != "" {
		state := e.newFileStateFromBuffer()
		// Capture first lines for FileChunks context
		state.FirstLines = copyFirstN(currentLines, FileChunkLines)
		e.fileStateStore[oldPath] = state
	}

	if state, exists := e.fileStateStore[newPath]; exists {
		if e.isFileStateValid(state, currentLines) {
			e.buffer.SetFileContext(state.PreviousLines, state.OriginalLines, state.DiffHistories)
			state.LastAccessNs = e.clock.Now().UnixNano()
			return true
		}
		delete(e.fileStateStore, newPath)
	}

	e.buffer.SetFileContext(nil, copyLines(currentLines), nil)
	return false
}

// isFileStateValid checks if saved state is still valid for the current file content.
func (e *Engine) isFileStateValid(state *FileState, currentLines []string) bool {
	if len(state.OriginalLines) == 0 {
		return false
	}

	origLen := len(state.OriginalLines)
	currLen := len(currentLines)
	if origLen != currLen {
		diff := origLen - currLen
		if diff < 0 {
			diff = -diff
		}
		threshold := max(origLen/10, 10)
		if diff > threshold {
			return false
		}
	}

	checkIndices := []int{0}
	if currLen > 2 {
		checkIndices = append(checkIndices, currLen/2, currLen-1)
	}

	mismatches := 0
	for _, i := range checkIndices {
		if i < len(state.OriginalLines) && i < len(currentLines) {
			if state.OriginalLines[i] != currentLines[i] {
				mismatches++
			}
		}
	}

	return mismatches <= len(checkIndices)/2
}

// trimFileStateStore keeps only the most recently accessed maxFiles files
func (e *Engine) trimFileStateStore(maxFiles int) {
	if len(e.fileStateStore) <= maxFiles {
		return
	}

	type entry struct {
		path  string
		state *FileState
	}

	entries := make([]entry, 0, len(e.fileStateStore))
	for path, state := range e.fileStateStore {
		entries = append(entries, entry{path, state})
	}

	for i := 0; i < len(entries)-1; i++ {
		for j := i + 1; j < len(entries); j++ {
			if entries[i].state.LastAccessNs < entries[j].state.LastAccessNs {
				entries[i], entries[j] = entries[j], entries[i]
			}
		}
	}

	e.fileStateStore = make(map[string]*FileState)
	for i := 0; i < maxFiles && i < len(entries); i++ {
		e.fileStateStore[entries[i].path] = entries[i].state
	}
}

// getAllFileDiffHistories returns diff history for the current file only.
func (e *Engine) getAllFileDiffHistories() []*types.FileDiffHistory {
	if e.buffer.Path() == "" || len(e.buffer.DiffHistories()) == 0 {
		return nil
	}

	diffs := copyDiffs(e.buffer.DiffHistories())

	if e.config.MaxDiffTokens > 0 {
		diffs = utils.TrimDiffEntries(diffs, e.config.MaxDiffTokens)
	}

	if len(diffs) == 0 {
		return nil
	}

	return []*types.FileDiffHistory{{
		FileName:    e.buffer.Path(),
		DiffHistory: diffs,
	}}
}

// copyLines creates a deep copy of a string slice
func copyLines(lines []string) []string {
	if lines == nil {
		return nil
	}
	result := make([]string, len(lines))
	copy(result, lines)
	return result
}

// copyDiffs creates a deep copy of a DiffEntry slice
func copyDiffs(diffs []*types.DiffEntry) []*types.DiffEntry {
	if diffs == nil {
		return nil
	}
	result := make([]*types.DiffEntry, len(diffs))
	copy(result, diffs)
	return result
}

// copyFirstN creates a copy of the first n lines
func copyFirstN(lines []string, n int) []string {
	if lines == nil {
		return nil
	}
	if len(lines) <= n {
		return copyLines(lines)
	}
	return copyLines(lines[:n])
}

// getRecentBufferSnapshots returns up to limit recent buffer snapshots
// excluding the current file, sorted by most recently accessed
func (e *Engine) getRecentBufferSnapshots(excludePath string, limit int) []*types.RecentBufferSnapshot {
	type entry struct {
		path  string
		state *FileState
	}

	var entries []entry
	for path, state := range e.fileStateStore {
		if path != excludePath && len(state.FirstLines) > 0 {
			entries = append(entries, entry{path, state})
		}
	}

	// Sort by LastAccessNs descending (most recent first)
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].state.LastAccessNs > entries[j].state.LastAccessNs
	})

	var result []*types.RecentBufferSnapshot
	for i := 0; i < limit && i < len(entries); i++ {
		result = append(result, &types.RecentBufferSnapshot{
			FilePath:    entries[i].path,
			Lines:       entries[i].state.FirstLines,
			TimestampMs: entries[i].state.LastAccessNs / 1e6, // ns to ms
		})
	}
	return result
}
