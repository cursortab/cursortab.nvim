package engine

import (
	"slices"

	"cursortab/ctx"
	"cursortab/types"
)

type completionInputOptions struct {
	kind              ctx.RequestKind
	source            types.CompletionSource
	lines             []string
	cursorRow         int
	cursorCol         int
	hasCursorOverride bool
}

func (e *Engine) buildCompletionInput(opts completionInputOptions) (ctx.CompletionInput, ctx.ContextSourceInput) {
	current := e.buildCurrentSnapshot(opts)
	snapshot := e.buildFileContextSnapshot()
	sourceInput := buildContextSourceInput(current, snapshot, e.buffer)
	return ctx.CompletionInput{
		Kind:    opts.kind,
		Trigger: opts.source,
		Current: current,
	}, sourceInput
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
		Workspace: ctx.WorkspaceRef{
			Path: e.WorkspacePath,
			ID:   e.WorkspaceID,
		},
		File: ctx.FileSnapshot{
			Path:    e.buffer.Path(),
			Lines:   slices.Clone(lines),
			Version: e.buffer.Version(),
		},
		Cursor: ctx.CursorPosition{
			Row: row,
			Col: col,
		},
		View: ctx.ViewConstraints{
			ViewportHeight:  e.getViewportHeightConstraint(),
			MaxVisibleLines: e.config.MaxVisibleLines,
		},
	}
}

func (e *Engine) buildFileContextSnapshot() ctx.FileContextSnapshot {
	currentPath := e.buffer.Path()
	nowNs := e.clock.Now().UnixNano()
	recentFiles := make([]ctx.FileContextFile, 0, len(e.fileStateStore))
	for _, entry := range e.fileStatesByRecency(func(path string, _ *FileState) bool {
		return path != currentPath
	}) {
		recentFiles = append(recentFiles, ctx.FileContextFile{
			Path:          entry.path,
			FirstLines:    slices.Clone(entry.state.FirstLines),
			DiffHistories: cloneDiffEntries(entry.state.DiffHistories),
			LastAccessNs:  entry.state.LastAccessNs,
		})
	}
	return ctx.FileContextSnapshot{
		CurrentFile: ctx.FileContextFile{
			Path:          currentPath,
			FirstLines:    firstN(e.buffer.Lines(), e.contextLimits.FileChunkLines),
			DiffHistories: cloneDiffEntries(e.buffer.DiffHistories()),
			LastAccessNs:  nowNs,
		},
		RecentFiles: recentFiles,
		UserActions: cloneUserActions(e.userActions),
		NowNs:       nowNs,
	}
}

func buildContextSourceInput(current ctx.CurrentSnapshot, snapshot ctx.FileContextSnapshot, buffer ctx.BufferContextReader) ctx.ContextSourceInput {
	return ctx.ContextSourceInput{
		Current:  current,
		Snapshot: snapshot,
		Buffer:   buffer,
	}
}

func cloneDiffEntries(entries []*types.DiffEntry) []*types.DiffEntry {
	if entries == nil {
		return nil
	}
	cloned := make([]*types.DiffEntry, len(entries))
	for i, entry := range entries {
		if entry == nil {
			continue
		}
		clone := *entry
		cloned[i] = &clone
	}
	return cloned
}

func cloneUserActions(actions []*types.UserAction) []*types.UserAction {
	if actions == nil {
		return nil
	}
	cloned := make([]*types.UserAction, len(actions))
	for i, action := range actions {
		if action == nil {
			continue
		}
		clone := *action
		cloned[i] = &clone
	}
	return cloned
}
