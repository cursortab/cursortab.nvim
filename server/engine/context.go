package engine

import (
	"slices"
	"sort"

	"cursortab/buffer"
	"cursortab/logger"
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
		PreviousLines: slices.Clone(e.buffer.PreviousLines()),
		DiffHistories: slices.Clone(e.buffer.DiffHistories()),
		OriginalLines: slices.Clone(e.buffer.OriginalLines()),
		DiskLines:     slices.Clone(e.buffer.DiskLines()),
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
	state.FirstLines = firstN(e.buffer.Lines(), defaultFileChunkLines)
	e.fileStateStore[e.buffer.Path()] = state
	e.trimFileStateStore(defaultMaxRecentSnapshots)
}

// handleFileSwitch manages file state when switching between files.
func (e *Engine) handleFileSwitch(oldPath, newPath string, currentLines []string) bool {
	if oldPath == newPath {
		return false
	}

	// Drop any in-flight or visible completion work tied to the old file.
	// The old file's prefetched/streaming/staged completions reference its
	// line content; applying them against the new buffer would render
	// against the wrong rows. Late-arriving responses for the cancelled
	// requests are then discarded by the state guards in events.go.
	e.cancelCurrentRequest()
	e.cancelPrefetch()
	e.cancelStreaming()
	e.cursorTarget = nil
	e.stagedCompletion = nil
	e.resetCompletionFields()
	e.state = stateIdle
	e.buffer.ClearUI()

	if oldPath != "" {
		state := e.newFileStateFromBuffer()
		state.FirstLines = firstN(currentLines, defaultFileChunkLines)
		e.fileStateStore[oldPath] = state
		e.trimFileStateStore(defaultMaxRecentSnapshots)
	}

	if state, exists := e.fileStateStore[newPath]; exists {
		if e.isFileStateValid(state, currentLines) {
			e.buffer.SetFileContext(buffer.FileContext{
				PreviousLines: state.PreviousLines,
				OriginalLines: state.OriginalLines,
				DiskLines:     state.DiskLines,
				DiffHistories: state.DiffHistories,
			})
			state.LastAccessNs = e.clock.Now().UnixNano()
			return true
		}
		delete(e.fileStateStore, newPath)
	}

	e.buffer.SetFileContext(buffer.FileContext{
		PreviousLines: slices.Clone(currentLines),
		OriginalLines: slices.Clone(currentLines),
		DiskLines:     slices.Clone(currentLines),
	})
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
		diff := utils.Abs(origLen - currLen)
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

// fileStatesByRecency returns (path, state) pairs from the file state store,
// sorted by LastAccessNs descending (most recently accessed first). Pairs for
// which keep returns false are skipped.
func (e *Engine) fileStatesByRecency(keep func(path string, state *FileState) bool) []fileStateEntry {
	entries := make([]fileStateEntry, 0, len(e.fileStateStore))
	for path, state := range e.fileStateStore {
		if keep == nil || keep(path, state) {
			entries = append(entries, fileStateEntry{path, state})
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].state.LastAccessNs > entries[j].state.LastAccessNs
	})
	return entries
}

type fileStateEntry struct {
	path  string
	state *FileState
}

// trimFileStateStore keeps only the most recently accessed maxFiles files
func (e *Engine) trimFileStateStore(maxFiles int) {
	if maxFiles <= 0 {
		return
	}
	if len(e.fileStateStore) <= maxFiles {
		return
	}
	entries := e.fileStatesByRecency(nil)
	e.fileStateStore = make(map[string]*FileState, maxFiles)
	for i := 0; i < maxFiles && i < len(entries); i++ {
		e.fileStateStore[entries[i].path] = entries[i].state
	}
}

// firstN returns a clone of the first n lines (or all of them if n exceeds the length).
func firstN(lines []string, n int) []string {
	if n < 0 || len(lines) <= n {
		return slices.Clone(lines)
	}
	return slices.Clone(lines[:n])
}
