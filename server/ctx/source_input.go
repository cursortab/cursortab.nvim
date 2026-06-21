package ctx

import "cursortab/types"

type ContextSourceInput struct {
	Current  CurrentSnapshot
	Snapshot FileContextSnapshot
	Buffer   BufferContextReader
}

type BufferContextReader interface {
	Diagnostics() *types.Diagnostics
	TreesitterSymbols(row int, col int, maxSiblings int) *types.TreesitterContext
}

type FileContextSnapshot struct {
	CurrentFile FileContextFile
	RecentFiles []FileContextFile
	UserActions []*types.UserAction
	NowNs       int64
}

type FileContextFile struct {
	Path          string
	FirstLines    []string
	DiffHistories []*types.DiffEntry
	LastAccessNs  int64
}
