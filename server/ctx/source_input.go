package ctx

import "cursortab/types"

type fileContextNeeds struct {
	RecentFileLines         bool
	RecentFileDiffHistories bool
	CurrentDiffHistories    bool
	UserActions             bool
}

// FileContextNeeds reports which frozen file-context fields collection reads.
func (materials Materials) FileContextNeeds() fileContextNeeds {
	var needs fileContextNeeds
	for _, material := range materials {
		switch material.(type) {
		case RecentFiles:
			needs.RecentFileLines = true
		case EditHistory:
			needs.RecentFileDiffHistories = true
			needs.CurrentDiffHistories = true
		case UserActions:
			needs.UserActions = true
		}
	}
	return needs
}

// ContextSourceInput is collector-only input. It may hold live readers and
// engine limits, but providers only see collected material values.
type ContextSourceInput struct {
	Current  CurrentSnapshot
	Snapshot FileContextSnapshot
	Buffer   bufferContextReader
	Limits   CollectionLimits
}

type bufferContextReader interface {
	Diagnostics() *types.Diagnostics
	TreesitterSymbols(row int, col int, maxSiblings int) *types.TreesitterContext
}

type FileContextSnapshot struct {
	CurrentDiffHistories []*types.DiffEntry
	RecentFiles          []RecentFileSnapshot
	UserActions          []*types.UserAction
	NowNs                int64
}

type RecentFileSnapshot struct {
	Path          string
	FirstLines    []string
	DiffHistories []*types.DiffEntry
	LastAccessNs  int64
}

// CollectionLimits are engine-owned execution bounds for collection.
// Providers choose material types; the engine chooses runtime limits.
type CollectionLimits struct {
	MaxSiblings        int
	MaxDiffBytes       int
	MaxChangedSymbols  int
	MaxRecentSnapshots int
	MaxDiffTokens      int
	MaxUserActions     int
}
