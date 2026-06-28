package ctx

import (
	"context"
	"slices"
	"strings"

	"cursortab/buffer"
	"cursortab/types"
	"cursortab/utils"
)

type Diagnostics struct {
	Data *types.Diagnostics
}

func (Diagnostics) collect(_ context.Context, input ContextSourceInput) (material, error) {
	return Diagnostics{Data: input.Buffer.Diagnostics()}, nil
}

type Treesitter struct {
	Data *types.TreesitterContext
}

func (Treesitter) collect(_ context.Context, input ContextSourceInput) (material, error) {
	return Treesitter{Data: input.Buffer.TreesitterSymbols(input.Current.Cursor.Row, input.Current.Cursor.Col, input.Limits.MaxSiblings)}, nil
}

type GitDiff struct {
	Data *types.GitDiffContext
}

func (GitDiff) collect(ctx context.Context, input ContextSourceInput) (material, error) {
	result := GitDiff{}
	if !strings.HasSuffix(input.Current.File.Path, "COMMIT_EDITMSG") {
		return result, nil
	}
	workDir := input.Current.WorkspacePath
	if workDir == "" {
		return result, nil
	}

	fullDiff := runGit(ctx, workDir, "diff", "--cached")
	if fullDiff == "" {
		return result, nil
	}
	if len(fullDiff) <= input.Limits.MaxDiffBytes {
		result.Data = &types.GitDiffContext{Diff: fullDiff}
		return result, nil
	}

	minimalDiff := runGit(ctx, workDir, "diff", "--cached", "-U0")
	if minimalDiff == "" {
		return result, nil
	}
	symbols := extractChangedSymbols(minimalDiff, input.Limits.MaxChangedSymbols)
	if len(symbols) == 0 {
		return result, nil
	}
	result.Data = &types.GitDiffContext{Diff: strings.Join(symbols, "\n")}
	return result, nil
}

type RecentFiles struct {
	Files []*types.RecentBufferSnapshot
}

func (RecentFiles) collect(_ context.Context, input ContextSourceInput) (material, error) {
	result := RecentFiles{}
	for _, file := range input.Snapshot.RecentFiles {
		if input.Limits.MaxRecentSnapshots > 0 && len(result.Files) >= input.Limits.MaxRecentSnapshots {
			break
		}
		if len(file.FirstLines) == 0 {
			continue
		}
		result.Files = append(result.Files, &types.RecentBufferSnapshot{
			FilePath:    file.Path,
			Lines:       slices.Clone(file.FirstLines),
			TimestampMs: file.LastAccessNs / 1e6,
		})
	}
	return result, nil
}

type EditHistory struct {
	Files []*types.FileDiffHistory
}

func (EditHistory) collect(_ context.Context, input ContextSourceInput) (material, error) {
	var result EditHistory
	for i := len(input.Snapshot.RecentFiles) - 1; i >= 0; i-- {
		file := input.Snapshot.RecentFiles[i]
		diffs := buffer.ProcessDiffHistory(file.DiffHistories, input.Snapshot.NowNs)
		if len(diffs) == 0 {
			continue
		}
		result.Files = append(result.Files, &types.FileDiffHistory{
			FileName:    file.Path,
			DiffHistory: diffs,
		})
	}

	if input.Current.File.Path != "" {
		diffs := buffer.ProcessDiffHistory(input.Snapshot.CurrentDiffHistories, input.Snapshot.NowNs)
		if input.Limits.MaxDiffTokens > 0 {
			diffs = utils.TrimDiffEntries(diffs, input.Limits.MaxDiffTokens)
		}
		if len(diffs) > 0 {
			result.Files = append(result.Files, &types.FileDiffHistory{
				FileName:    input.Current.File.Path,
				DiffHistory: diffs,
			})
		}
	}
	return result, nil
}

type UserActions struct {
	Actions []*types.UserAction
}

func (UserActions) collect(_ context.Context, input ContextSourceInput) (material, error) {
	var result UserActions
	for _, action := range input.Snapshot.UserActions {
		if action == nil || action.FilePath != input.Current.File.Path {
			continue
		}
		clone := *action
		result.Actions = append(result.Actions, &clone)
	}
	if input.Limits.MaxUserActions > 0 && len(result.Actions) > input.Limits.MaxUserActions {
		result.Actions = result.Actions[len(result.Actions)-input.Limits.MaxUserActions:]
	}
	return result, nil
}
