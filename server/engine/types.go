package engine

import (
	"context"
	"time"

	"cursortab/buffer"
	"cursortab/ctx"
	"cursortab/text"
	"cursortab/types"
)

type Buffer interface {
	Sync(workspacePath string) (*buffer.SyncResult, error)
	Lines() []string
	Row() int
	Col() int
	Path() string
	Version() int
	ViewportBounds() (top, bottom int)
	AvailableWidth() int
	PreviousLines() []string
	OriginalLines() []string
	DiffHistories() []*types.DiffEntry
	DiskLines() []string
	Diagnostics() *types.Diagnostics
	TreesitterSymbols(row int, col int, maxSiblings int) *types.TreesitterContext
	SetFileContext(ctx buffer.FileContext)
	HasChanges(startLine, endLineInc int, lines []string) bool
	PrepareCompletion(startLine, endLineInc int, lines []string, groups []*text.Group) buffer.Batch
	CommitPending()
	CommitUserEdits() bool  // Returns true if changes were committed
	ClearDiffHistory()      // Reset diff history and checkpoint on save
	IsModified() bool       // True if buffer content differs from the last-saved checkpoint
	CursorScopes() []string // Treesitter node types from cursor to root
	SkipHistory() bool      // True for files where diff history is not recorded
	ShowCursorTarget(line int) error
	ClearUI() error
	MoveCursor(line int, center, mark bool) error
	RegisterEventHandler(handler func(event string)) error
	InsertText(line, col int, text string, keepUI bool) error // Insert text at position (1-indexed line, 0-indexed col)
	ReplaceLine(line int, content string, keepUI bool) error  // Replace a single line (1-indexed)
	InsertLine(line int, content string, keepUI bool) error   // Insert a new line at position (1-indexed)
}

// Provider exposes the facts engine needs before a request, then runs the
// provider with collected public context.
type Provider interface {
	CompletionKind() CompletionKind
	CompletionInputAuthority() CompletionInputAuthority
	RequiredMaterials() ctx.Materials
	StartCompletion(ctx context.Context, input ctx.CompletionInput, allowStream bool) (*types.CompletionResponse, CompletionStream, error)
}

type CompletionKind int

const (
	CompletionInline CompletionKind = iota
	CompletionFIM
	CompletionEdit
)

type CompletionInputAuthority int

const (
	InputSuppliedCurrent CompletionInputAuthority = iota
	InputLiveEditorState
)

const (
	defaultMaxUserActions     = 16
	defaultFileChunkLines     = 30
	defaultMaxRecentSnapshots = 3
	defaultMaxDiffBytes       = 4096
	defaultMaxChangedSymbols  = 50
	defaultMaxSiblings        = 50
)

type CompletionStream interface {
	Lines() <-chan string
	Window() (windowStart int, oldLines []string)
	Cancel()
	Finish() (*types.CompletionResponse, error)
}

type StreamingState struct {
	StageBuilder *text.IncrementalStageBuilder

	PendingLine    string // Buffer for last line (drop if truncated)
	HasPendingLine bool

	FirstStageRendered bool
}

type state int

const (
	stateIdle state = iota
	statePendingCompletion
	stateHasCompletion
	stateHasCursorTarget
	stateStreamingCompletion
)

func (s state) String() string {
	switch s {
	case stateIdle:
		return "Idle"
	case statePendingCompletion:
		return "PendingCompletion"
	case stateHasCompletion:
		return "HasCompletion"
	case stateHasCursorTarget:
		return "HasCursorTarget"
	case stateStreamingCompletion:
		return "StreamingCompletion"
	default:
		return "Unknown"
	}
}

type prefetchState int

const (
	prefetchNone prefetchState = iota
	prefetchInFlight
	prefetchWaitingForTab
	prefetchWaitingForCursorPrediction
	prefetchReady
)

// CursorPredictionConfig holds cursor prediction settings
type CursorPredictionConfig struct {
	Enabled            bool // Show jump indicators (default: true)
	AutoAdvance        bool // On no-op, jump to last line + retrigger (default: true)
	ProximityThreshold int  // Lines apart to trigger staging (default: 3)
}

// FileState holds per-file context that persists across file switches
type FileState struct {
	PreviousLines []string           // Content before user started editing this file
	DiffHistories []*types.DiffEntry // Cumulative diffs for this file
	OriginalLines []string           // Checkpoint for granular diffs (resets on CommitUserEdits)
	DiskLines     []string           // File content as last written to disk (resets only on save)
	LastAccessNs  int64              // Monotonic timestamp for LRU eviction
	Version       int                // Buffer version when last active
	FirstLines    []string           // First 30 lines for FileChunks context
}

// EngineConfig holds engine configuration
type EngineConfig struct {
	NsID                   int
	ProviderName           string
	CompletionTimeout      time.Duration
	IdleCompletionDelay    time.Duration
	TextChangeDebounce     time.Duration
	CursorPrediction       CursorPredictionConfig
	MaxDiffTokens          int      // Maximum tokens for diff history per file (0 = no limit)
	MaxVisibleLines        int      // Maximum lines per stage (0 = no limit)
	CompleteInInsert       bool     // Show completions in insert mode
	CompleteInNormal       bool     // Show completions in normal mode
	DisabledIn             []string // Treesitter scopes where completions are suppressed
	DisableProviderMetrics bool     // Skip wiring provider as metrics.Sender (eval harness sets this)
}
