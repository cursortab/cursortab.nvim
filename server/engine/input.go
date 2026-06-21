package engine

import (
	"slices"

	"cursortab/buffer"
	"cursortab/ctx"
	"cursortab/types"
	"cursortab/utils"
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

func (e *Engine) buildCompletionRequest(input ctx.CompletionInput, sourceInput ctx.ContextSourceInput) *types.CompletionRequest {
	current := input.Current
	snapshot := sourceInput.Snapshot
	return &types.CompletionRequest{
		Source:                input.Trigger,
		WorkspacePath:         current.Workspace.Path,
		WorkspaceID:           current.Workspace.ID,
		FilePath:              current.File.Path,
		Lines:                 slices.Clone(current.File.Lines),
		Version:               current.File.Version,
		PreviousLines:         slices.Clone(e.buffer.PreviousLines()),
		OriginalLines:         slices.Clone(e.buffer.OriginalLines()),
		FileDiffHistories:     legacyFileDiffHistories(snapshot, e.config.MaxDiffTokens),
		CursorRow:             current.Cursor.Row,
		CursorCol:             current.Cursor.Col,
		ViewportHeight:        current.View.ViewportHeight,
		MaxVisibleLines:       current.View.MaxVisibleLines,
		AdditionalContext:     e.gatherContext(current.File.Path),
		RecentBufferSnapshots: legacyRecentBufferSnapshots(snapshot, e.contextLimits.MaxRecentSnapshots),
		UserActions:           legacyUserActionsForFile(snapshot, current.File.Path),
	}
}

func legacyFileDiffHistories(snapshot ctx.FileContextSnapshot, maxDiffTokens int) []*types.FileDiffHistory {
	var result []*types.FileDiffHistory
	for i := len(snapshot.RecentFiles) - 1; i >= 0; i-- {
		file := snapshot.RecentFiles[i]
		if len(file.DiffHistories) == 0 {
			continue
		}
		diffs := buffer.ProcessDiffHistory(cloneDiffEntries(file.DiffHistories), snapshot.NowNs)
		if len(diffs) > 0 {
			result = append(result, &types.FileDiffHistory{
				FileName:    file.Path,
				DiffHistory: diffs,
			})
		}
	}
	if snapshot.CurrentFile.Path != "" && len(snapshot.CurrentFile.DiffHistories) > 0 {
		diffs := buffer.ProcessDiffHistory(cloneDiffEntries(snapshot.CurrentFile.DiffHistories), snapshot.NowNs)
		if maxDiffTokens > 0 {
			diffs = utils.TrimDiffEntries(diffs, maxDiffTokens)
		}
		if len(diffs) > 0 {
			result = append(result, &types.FileDiffHistory{
				FileName:    snapshot.CurrentFile.Path,
				DiffHistory: diffs,
			})
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func legacyRecentBufferSnapshots(snapshot ctx.FileContextSnapshot, limit int) []*types.RecentBufferSnapshot {
	if limit < 0 {
		return nil
	}
	var result []*types.RecentBufferSnapshot
	for _, file := range snapshot.RecentFiles {
		if len(result) >= limit {
			break
		}
		if len(file.FirstLines) == 0 {
			continue
		}
		result = append(result, &types.RecentBufferSnapshot{
			FilePath:    file.Path,
			Lines:       slices.Clone(file.FirstLines),
			TimestampMs: file.LastAccessNs / 1e6,
		})
	}
	return result
}

func legacyUserActionsForFile(snapshot ctx.FileContextSnapshot, filePath string) []*types.UserAction {
	var result []*types.UserAction
	for _, action := range snapshot.UserActions {
		if action != nil && action.FilePath == filePath {
			clone := *action
			result = append(result, &clone)
		}
	}
	return result
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
