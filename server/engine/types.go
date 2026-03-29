package engine

import (
	"context"
	"strings"
	"time"

	"cursortab/buffer"
	"cursortab/text"
	"cursortab/types"
)

// Buffer defines the interface for buffer operations.
// Implemented by buffer.NvimBuffer for Neovim integration.
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
	SetFileContext(ctx buffer.FileContext)
	HasChanges(startLine, endLineInc int, lines []string) bool
	PrepareCompletion(startLine, endLineInc int, lines []string, groups []*text.Group) buffer.Batch
	CommitPending()
	CommitUserEdits() bool // Returns true if changes were committed
	ClearDiffHistory()     // Reset diff history and checkpoint on save
	IsModified() bool      // True if buffer content differs from the last-saved checkpoint
	SkipHistory() bool     // True for files where diff history is not recorded
	ShowCursorTarget(line int) error
	ClearUI() error
	MoveCursor(line int, center, mark bool) error
	RegisterEventHandler(handler func(event string)) error
	// Partial accept operations
	// keepUI=true skips clearing the extmark namespace so the UI persists until rerenderPartial replaces it.
	InsertText(line, col int, text string, keepUI bool) error // Insert text at position (1-indexed line, 0-indexed col)
	ReplaceLine(line int, content string, keepUI bool) error  // Replace a single line (1-indexed)
	InsertLine(line int, content string, keepUI bool) error   // Insert a new line at position (1-indexed)
}

// Provider defines the interface that all AI providers must implement.
// Implemented by inline.Provider, sweep.Provider, zeta.Provider.
type Provider interface {
	GetCompletion(ctx context.Context, req *types.CompletionRequest) (*types.CompletionResponse, error)
	GetContextLimits() ContextLimits
}

// ContextLimits controls how much context is gathered and sent per provider.
// Zero values use defaults. -1 means the feature is disabled.
type ContextLimits struct {
	MaxUserActions     int // Ring buffer size for user action tracking (default: 16)
	FileChunkLines     int // Lines per recent file snapshot (default: 30)
	MaxRecentSnapshots int // Number of recent file snapshots (default: 3, -1 = disabled)
	MaxDiffBytes       int // Git diff byte threshold before switching to symbols (default: 4096)
	MaxChangedSymbols  int // Max symbols extracted from large diffs (default: 50)
	MaxSiblings        int // Max treesitter sibling nodes (default: 50)
	MaxInputLines      int // Input line limit for hosted APIs (default: 50000)
	MaxInputBytes      int // Input byte limit for hosted APIs (default: 10_000_000)
}

// DefaultContextLimits returns the default context limits.
func DefaultContextLimits() ContextLimits {
	return ContextLimits{
		MaxUserActions:     16,
		FileChunkLines:     30,
		MaxRecentSnapshots: 3,
		MaxDiffBytes:       4096,
		MaxChangedSymbols:  50,
		MaxSiblings:        50,
		MaxInputLines:      50_000,
		MaxInputBytes:      10_000_000,
	}
}

// WithDefaults returns a copy with zero values replaced by defaults.
func (cl ContextLimits) WithDefaults() ContextLimits {
	d := DefaultContextLimits()
	if cl.MaxUserActions == 0 {
		cl.MaxUserActions = d.MaxUserActions
	}
	if cl.FileChunkLines == 0 {
		cl.FileChunkLines = d.FileChunkLines
	}
	if cl.MaxRecentSnapshots == 0 {
		cl.MaxRecentSnapshots = d.MaxRecentSnapshots
	}
	if cl.MaxDiffBytes == 0 {
		cl.MaxDiffBytes = d.MaxDiffBytes
	}
	if cl.MaxChangedSymbols == 0 {
		cl.MaxChangedSymbols = d.MaxChangedSymbols
	}
	if cl.MaxSiblings == 0 {
		cl.MaxSiblings = d.MaxSiblings
	}
	if cl.MaxInputLines == 0 {
		cl.MaxInputLines = d.MaxInputLines
	}
	if cl.MaxInputBytes == 0 {
		cl.MaxInputBytes = d.MaxInputBytes
	}
	return cl
}

// LineStreamProvider extends Provider with line-by-line streaming capabilities.
// For providers like sweep, zeta, fim that stream by lines.
type LineStreamProvider interface {
	Provider
	// GetStreamingType returns: 0=none, 1=lines, 2=tokens
	GetStreamingType() int
	// PrepareLineStream prepares the stream and returns it along with provider context
	PrepareLineStream(ctx context.Context, req *types.CompletionRequest) (LineStream, any, error)
	// ValidateFirstLine validates the first line (called after first line received)
	ValidateFirstLine(providerCtx any, firstLine string) error
	// FinishLineStream runs postprocessors on the final accumulated result
	FinishLineStream(providerCtx any, text string, finishReason string, stoppedEarly bool) (*types.CompletionResponse, error)
}

// TokenStreamProvider extends Provider with token-by-token streaming capabilities.
// For providers like inline that stream individual tokens for ghost text.
type TokenStreamProvider interface {
	Provider
	// GetStreamingType returns: 0=none, 1=lines, 2=tokens
	GetStreamingType() int
	// PrepareTokenStream prepares the stream and returns it along with provider context.
	// The stream emits cumulative text (not deltas) for idempotent UI updates.
	PrepareTokenStream(ctx context.Context, req *types.CompletionRequest) (LineStream, any, error)
	// FinishTokenStream runs postprocessors on the final accumulated result
	FinishTokenStream(providerCtx any, text string) (*types.CompletionResponse, error)
}

// Streaming type constants
const (
	StreamingTypeNone   = 0 // Batch mode
	StreamingTypeLines  = 1 // Line-by-line (sweep, zeta, fim)
	StreamingTypeTokens = 2 // Token-by-token (inline)
)

// LineStream provides incremental line-by-line streaming
type LineStream interface {
	LinesChan() <-chan string // Channel for complete lines
	Cancel()                  // Cancel the stream early
}

// TrimmedContext provides access to trim info from the provider.
// Implemented by provider.Context to allow engine to extract window offset.
type TrimmedContext interface {
	GetWindowStart() int       // 0-indexed start offset of trimmed window
	GetTrimmedLines() []string // Lines sent to the model (nil if no trimming)
}

// StreamContext provides custom old lines and line transforms for streaming.
// Used by FIM providers where streamed lines are partial (middle-fill) and need
// transformation before diffing. When implemented, the engine uses these old lines
// and base offset instead of TrimmedContext, and transforms the first/last lines.
type StreamContext interface {
	GetStreamOldLines() []string           // old lines for diff (nil = not applicable)
	GetStreamBaseOffset() int              // 0-indexed base offset in buffer
	TransformFirstLine(line string) string // transform first streamed line
	TransformLastLine(line string) string  // transform last streamed line
}

// StreamingState holds state during incremental line streaming
type StreamingState struct {
	// Stage building
	StageBuilder *text.IncrementalStageBuilder

	// Buffering for truncation safety
	PendingLine    string // Buffer for last line (drop if truncated)
	HasPendingLine bool

	// Accumulated text for postprocessing
	AccumulatedText strings.Builder

	// Provider context for postprocessing
	ProviderContext any
	Validated       bool

	// Request data needed for finalization
	Request *types.CompletionRequest

	// Track if we've rendered the first stage during streaming
	// Only render one stage during streaming; rest handled at completion
	FirstStageRendered bool
}

// TokenStreamingState holds state during token-by-token streaming
type TokenStreamingState struct {
	// Accumulated text (cumulative, not deltas)
	AccumulatedText string

	// Provider context for postprocessing
	ProviderContext any

	// Request data needed for finalization
	Request *types.CompletionRequest

	// Line prefix: text before cursor on current line (for rendering full line)
	LinePrefix string

	// Line number where ghost text is shown (1-indexed)
	LineNum int
}

// state represents the current state of the engine's state machine
type state int

const (
	stateIdle state = iota
	statePendingCompletion
	stateHasCompletion
	stateHasCursorTarget
	stateStreamingCompletion // New state for incremental streaming
)

// String returns a human-readable name for the state
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

// prefetchState represents the state of prefetch operations
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
	MaxDiffTokens          int  // Maximum tokens for diff history per file (0 = no limit)
	MaxVisibleLines        int  // Maximum lines per stage (0 = no limit)
	CompleteInInsert       bool // Show completions in insert mode
	CompleteInNormal       bool // Show completions in normal mode
	EditCompletionProvider bool // True for edit-prediction providers (sweep, zeta, mercury, copilot NES)
}
