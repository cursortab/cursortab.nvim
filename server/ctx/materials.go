package ctx

import "cursortab/types"

type Diagnostics struct {
	MaxItems int
	Data     *types.Diagnostics
}

func (Diagnostics) SourceID() ContextSourceID { return SourceDiagnostics }
func (Diagnostics) contextMaterial()          {}

type Treesitter struct {
	MaxSiblings int
	Data        *types.TreesitterContext
}

func (Treesitter) SourceID() ContextSourceID { return SourceTreesitter }
func (Treesitter) contextMaterial()          {}

type GitDiff struct {
	MaxBytes          int
	MaxChangedSymbols int
	Data              *types.GitDiffContext
}

func (GitDiff) SourceID() ContextSourceID { return SourceGitDiff }
func (GitDiff) contextMaterial()          {}

type RecentFiles struct {
	Limit      int
	FirstLines int
	Files      []*types.RecentBufferSnapshot
}

func (RecentFiles) SourceID() ContextSourceID { return SourceRecentFiles }
func (RecentFiles) contextMaterial()          {}

type EditHistory struct {
	MaxTokens int
	Files     []*types.FileDiffHistory
}

func (EditHistory) SourceID() ContextSourceID { return SourceEditHistory }
func (EditHistory) contextMaterial()          {}

type UserActions struct {
	Limit   int
	Actions []*types.UserAction
}

func (UserActions) SourceID() ContextSourceID { return SourceUserActions }
func (UserActions) contextMaterial()          {}
