package engine

import (
	"context"
	"strings"
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

// Provider owns material declarations and turns a collected CompletionInput into
// a completion response. Engine code must not derive provider policy from names.
type Provider interface {
	CanDo() ProviderCanDo
	RequiredMaterials() ctx.Materials
	GetCompletion(ctx context.Context, input ctx.CompletionInput) (*types.CompletionResponse, error)
}

type ProviderCanDo struct {
	CompleteWithTextRightOfCursor bool
	PrefetchAfterCursorTarget     bool
}

const (
	defaultMaxUserActions     = 16
	defaultFileChunkLines     = 30
	defaultMaxRecentSnapshots = 3
	defaultMaxDiffBytes       = 4096
	defaultMaxChangedSymbols  = 50
	defaultMaxSiblings        = 50
)

// ProviderStreamState is opaque to engine code except for per-line stream text transformation.
type ProviderStreamState interface {
	TransformLine(line string) (string, bool)
}

// LineStreamConfig is the engine-visible part of a prepared line stream.
type LineStreamConfig struct {
	WindowStart int
	OldLines    []string
	Prefill     string
}

type LineStreamProvider interface {
	Provider
	StreamsLines() bool
	PrepareLineStream(ctx context.Context, input ctx.CompletionInput) (LineStream, ProviderStreamState, LineStreamConfig, error)
	ValidateFirstLine(providerState ProviderStreamState, firstLine string) error
	// FinishLineStream lets the provider finalize its streamed text. Ordinary
	// streaming UI is built from streamed lines; after-accept handling consumes
	// the returned response to compute follow-up cursor prediction.
	FinishLineStream(providerState ProviderStreamState, text string, finishReason string, stoppedEarly bool) (*types.CompletionResponse, error)
}

type LineStream interface {
	LinesChan() <-chan string
	Cancel()
}

type StreamingState struct {
	StageBuilder *text.IncrementalStageBuilder

	PendingLine    string // Buffer for last line (drop if truncated)
	HasPendingLine bool

	AccumulatedText strings.Builder

	ProviderState ProviderStreamState
	Validated     bool

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
