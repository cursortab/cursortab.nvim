package engine

import (
	"slices"

	"cursortab/ctx"
	"cursortab/types"
)

type completionInputOptions struct {
	lines             []string
	cursorRow         int
	cursorCol         int
	hasCursorOverride bool
}

func (e *Engine) buildContextSourceInput(opts completionInputOptions, requirements ctx.Materials) ctx.ContextSourceInput {
	current := e.buildCurrentSnapshot(opts)
	snapshot := e.buildFileContextSnapshot(requirements)
	return ctx.ContextSourceInput{
		Current:  current,
		Snapshot: snapshot,
		Buffer:   e.buffer,
		Limits: ctx.CollectionLimits{
			MaxSiblings:        defaultMaxSiblings,
			MaxDiffBytes:       defaultMaxDiffBytes,
			MaxChangedSymbols:  defaultMaxChangedSymbols,
			MaxRecentSnapshots: defaultMaxRecentSnapshots,
			MaxDiffTokens:      e.config.MaxDiffTokens,
			MaxUserActions:     defaultMaxUserActions,
		},
	}
}

func (e *Engine) buildCurrentSnapshot(opts completionInputOptions) ctx.CurrentSnapshot {
	lines := opts.lines
	if lines == nil {
		lines = e.buffer.Lines()
	}
	row := e.buffer.Row()
	col := e.buffer.Col()
	if opts.hasCursorOverride {
		row = opts.cursorRow
		col = opts.cursorCol
	}
	return ctx.CurrentSnapshot{
		WorkspacePath: e.WorkspacePath,
		File: ctx.FileSnapshot{
			Path:  e.buffer.Path(),
			Lines: slices.Clone(lines),
		},
		Cursor: ctx.CursorPosition{
			Row: row,
			Col: col,
		},
		ViewportHeight: e.getViewportHeightConstraint(),
	}
}

func (e *Engine) buildFileContextSnapshot(requirements ctx.Materials) ctx.FileContextSnapshot {
	needs := requirements.FileContextNeeds()
	if !needs.RecentFileLines &&
		!needs.RecentFileDiffHistories &&
		!needs.CurrentDiffHistories &&
		!needs.UserActions {
		return ctx.FileContextSnapshot{}
	}

	currentPath := e.buffer.Path()
	nowNs := e.clock.Now().UnixNano()

	var recentFiles []ctx.RecentFileSnapshot
	if needs.RecentFileLines || needs.RecentFileDiffHistories {
		recentFiles = make([]ctx.RecentFileSnapshot, 0, len(e.fileStateStore))
		for _, entry := range e.fileStatesByRecency(func(path string, _ *FileState) bool {
			return path != currentPath
		}) {
			recent := ctx.RecentFileSnapshot{
				Path:         entry.path,
				LastAccessNs: entry.state.LastAccessNs,
			}
			if needs.RecentFileLines {
				recent.FirstLines = slices.Clone(entry.state.FirstLines)
			}
			if needs.RecentFileDiffHistories {
				recent.DiffHistories = clonePtrSlice(entry.state.DiffHistories)
			}
			recentFiles = append(recentFiles, recent)
		}
	}

	var currentDiffHistories []*types.DiffEntry
	if needs.CurrentDiffHistories {
		currentDiffHistories = clonePtrSlice(e.buffer.DiffHistories())
	}

	var userActions []*types.UserAction
	if needs.UserActions {
		userActions = clonePtrSlice(e.userActions)
	}

	return ctx.FileContextSnapshot{
		CurrentDiffHistories: currentDiffHistories,
		RecentFiles:          recentFiles,
		UserActions:          userActions,
		NowNs:                nowNs,
	}
}

func clonePtrSlice[T any](items []*T) []*T {
	if items == nil {
		return nil
	}
	cloned := make([]*T, len(items))
	for i, item := range items {
		if item == nil {
			continue
		}
		clone := *item
		cloned[i] = &clone
	}
	return cloned
}
