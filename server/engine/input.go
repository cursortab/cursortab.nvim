package engine

import (
	"slices"

	"cursortab/ctx"
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
		WorkspacePath: e.buffer.WorkspacePath(),
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
	snapshot := ctx.FileContextSnapshot{NowNs: e.clock.Now().UnixNano()}
	if needs.RecentFileLines || needs.RecentFileDiffHistories {
		for _, file := range e.history.Recent(currentPath) {
			recent := ctx.RecentFileSnapshot{Path: file.Path, LastAccessNs: file.AccessNs}
			if needs.RecentFileLines {
				recent.FirstLines = file.FirstLines
			}
			if needs.RecentFileDiffHistories {
				recent.DiffHistories = file.Diffs
			}
			snapshot.RecentFiles = append(snapshot.RecentFiles, recent)
		}
	}
	if needs.CurrentDiffHistories {
		snapshot.CurrentDiffHistories = e.history.Diffs(currentPath)
	}
	if needs.UserActions {
		snapshot.UserActions = clonePtrSlice(e.userActions)
	}
	return snapshot
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
