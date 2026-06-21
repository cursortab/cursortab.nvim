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
	MaxItems int
	Data     *types.Diagnostics
}

func (Diagnostics) SourceID() ContextSourceID { return SourceDiagnostics }
func (Diagnostics) contextMaterial()          {}

func (d Diagnostics) collect(_ context.Context, input ContextSourceInput) (ContextMaterial, error) {
	if input.Buffer == nil {
		return Diagnostics{MaxItems: d.MaxItems}, nil
	}
	return Diagnostics{
		MaxItems: d.MaxItems,
		Data:     cloneDiagnostics(input.Buffer.Diagnostics(), d.MaxItems),
	}, nil
}

type Treesitter struct {
	MaxSiblings int
	Data        *types.TreesitterContext
}

func (Treesitter) SourceID() ContextSourceID { return SourceTreesitter }
func (Treesitter) contextMaterial()          {}

func (t Treesitter) collect(_ context.Context, input ContextSourceInput) (ContextMaterial, error) {
	if input.Buffer == nil {
		return Treesitter{MaxSiblings: t.MaxSiblings}, nil
	}
	data := input.Buffer.TreesitterSymbols(input.Current.Cursor.Row, input.Current.Cursor.Col, t.MaxSiblings)
	return Treesitter{
		MaxSiblings: t.MaxSiblings,
		Data:        cloneTreesitter(data),
	}, nil
}

type GitDiff struct {
	MaxBytes          int
	MaxChangedSymbols int
	Data              *types.GitDiffContext
}

func (GitDiff) SourceID() ContextSourceID { return SourceGitDiff }
func (GitDiff) contextMaterial()          {}

func (g GitDiff) collect(ctx context.Context, input ContextSourceInput) (ContextMaterial, error) {
	result := GitDiff{
		MaxBytes:          g.MaxBytes,
		MaxChangedSymbols: g.MaxChangedSymbols,
	}
	if !strings.HasSuffix(input.Current.File.Path, "COMMIT_EDITMSG") {
		return result, nil
	}
	workDir := input.Current.Workspace.Path
	if workDir == "" {
		return result, nil
	}
	maxBytes := g.MaxBytes
	if maxBytes == 0 {
		maxBytes = 4096
	}
	maxChangedSymbols := g.MaxChangedSymbols
	if maxChangedSymbols == 0 {
		maxChangedSymbols = 50
	}

	fullDiff := runGit(ctx, workDir, "diff", "--cached")
	if fullDiff == "" {
		return result, nil
	}
	if len(fullDiff) <= maxBytes {
		result.Data = &types.GitDiffContext{Diff: fullDiff}
		return result, nil
	}

	minimalDiff := runGit(ctx, workDir, "diff", "--cached", "-U0")
	if minimalDiff == "" {
		return result, nil
	}
	symbols := extractChangedSymbols(minimalDiff, maxChangedSymbols)
	if len(symbols) == 0 {
		return result, nil
	}
	result.Data = &types.GitDiffContext{Diff: strings.Join(symbols, "\n")}
	return result, nil
}

type RecentFiles struct {
	Limit      int
	FirstLines int
	Files      []*types.RecentBufferSnapshot
}

func (RecentFiles) SourceID() ContextSourceID { return SourceRecentFiles }
func (RecentFiles) contextMaterial()          {}

func (r RecentFiles) collect(_ context.Context, input ContextSourceInput) (ContextMaterial, error) {
	result := RecentFiles{
		Limit:      r.Limit,
		FirstLines: r.FirstLines,
	}
	if r.Limit < 0 {
		return result, nil
	}
	for _, file := range input.Snapshot.RecentFiles {
		if r.Limit > 0 && len(result.Files) >= r.Limit {
			break
		}
		lines := limitLines(file.FirstLines, r.FirstLines)
		if len(lines) == 0 {
			continue
		}
		result.Files = append(result.Files, &types.RecentBufferSnapshot{
			FilePath:    file.Path,
			Lines:       lines,
			TimestampMs: file.LastAccessNs / 1e6,
		})
	}
	return result, nil
}

type EditHistory struct {
	MaxTokens int
	Files     []*types.FileDiffHistory
}

func (EditHistory) SourceID() ContextSourceID { return SourceEditHistory }
func (EditHistory) contextMaterial()          {}

func (e EditHistory) collect(_ context.Context, input ContextSourceInput) (ContextMaterial, error) {
	result := EditHistory{MaxTokens: e.MaxTokens}
	for i := len(input.Snapshot.RecentFiles) - 1; i >= 0; i-- {
		file := input.Snapshot.RecentFiles[i]
		diffs := buffer.ProcessDiffHistory(cloneDiffEntries(file.DiffHistories), input.Snapshot.NowNs)
		if len(diffs) == 0 {
			continue
		}
		result.Files = append(result.Files, &types.FileDiffHistory{
			FileName:    file.Path,
			DiffHistory: diffs,
		})
	}

	current := input.Snapshot.CurrentFile
	if current.Path != "" {
		diffs := buffer.ProcessDiffHistory(cloneDiffEntries(current.DiffHistories), input.Snapshot.NowNs)
		if e.MaxTokens > 0 {
			diffs = utils.TrimDiffEntries(diffs, e.MaxTokens)
		}
		if len(diffs) > 0 {
			result.Files = append(result.Files, &types.FileDiffHistory{
				FileName:    current.Path,
				DiffHistory: diffs,
			})
		}
	}
	return result, nil
}

type UserActions struct {
	Limit   int
	Actions []*types.UserAction
}

func (UserActions) SourceID() ContextSourceID { return SourceUserActions }
func (UserActions) contextMaterial()          {}

func (u UserActions) collect(_ context.Context, input ContextSourceInput) (ContextMaterial, error) {
	result := UserActions{Limit: u.Limit}
	if u.Limit < 0 {
		return result, nil
	}
	for _, action := range input.Snapshot.UserActions {
		if action == nil || action.FilePath != input.Current.File.Path {
			continue
		}
		clone := *action
		result.Actions = append(result.Actions, &clone)
	}
	if u.Limit > 0 && len(result.Actions) > u.Limit {
		result.Actions = result.Actions[len(result.Actions)-u.Limit:]
	}
	return result, nil
}

func cloneDiagnostics(diagnostics *types.Diagnostics, maxItems int) *types.Diagnostics {
	if diagnostics == nil {
		return nil
	}
	limit := len(diagnostics.Items)
	if maxItems > 0 && limit > maxItems {
		limit = maxItems
	}
	cloned := &types.Diagnostics{
		FilePath: diagnostics.FilePath,
		Items:    make([]*types.Diagnostic, limit),
	}
	for i := 0; i < limit; i++ {
		item := diagnostics.Items[i]
		if item == nil {
			continue
		}
		clone := *item
		if item.Range != nil {
			rangeClone := *item.Range
			clone.Range = &rangeClone
		}
		cloned.Items[i] = &clone
	}
	return cloned
}

func cloneTreesitter(treesitter *types.TreesitterContext) *types.TreesitterContext {
	if treesitter == nil {
		return nil
	}
	cloned := &types.TreesitterContext{
		EnclosingSignature: treesitter.EnclosingSignature,
		Imports:            slices.Clone(treesitter.Imports),
		Siblings:           make([]*types.TreesitterSymbol, len(treesitter.Siblings)),
		SyntaxRanges:       make([]*types.LineRange, len(treesitter.SyntaxRanges)),
	}
	for i, sibling := range treesitter.Siblings {
		if sibling == nil {
			continue
		}
		clone := *sibling
		cloned.Siblings[i] = &clone
	}
	for i, lineRange := range treesitter.SyntaxRanges {
		if lineRange == nil {
			continue
		}
		clone := *lineRange
		cloned.SyntaxRanges[i] = &clone
	}
	return cloned
}

func limitLines(lines []string, limit int) []string {
	if limit < 0 {
		return nil
	}
	if limit == 0 || len(lines) <= limit {
		return slices.Clone(lines)
	}
	return slices.Clone(lines[:limit])
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
