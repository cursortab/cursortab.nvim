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

// Provider is the engine boundary for completion providers.
//
// The engine reads [Provider.CompletionKind] before call-before policy, uses
// [Provider.CanPrefetchFromSyntheticCurrent] for cursor-target prefetch policy,
// collects [Provider.RequiredMaterials] through ctx.Collect, then calls
// [Provider.Complete].
//
// Concrete providers should implement this contract through their own methods
// or a real shared implementation. Embedding this interface in a provider
// struct hides missing methods when the contract changes.
type Provider interface {
	CompletionKind() CompletionKind
	CanPrefetchFromSyntheticCurrent() bool
	RequiredMaterials() ctx.Materials
	Complete(ctx context.Context, input ctx.CompletionInput) (*types.CompletionResponse, error)
}

type StreamingProvider interface {
	StreamCompletion(ctx context.Context, input ctx.CompletionInput) (CompletionStream, error)
}

// CompletionKind describes the editing shape a provider can produce.
// Engine call-before policy uses it to decide whether the current cursor
// position is a valid request input.
type CompletionKind int

const (
	// CompletionInline inserts at the cursor and requires an inert right suffix.
	CompletionInline CompletionKind = iota
	// CompletionFIM fills between prefix and suffix supplied by the engine.
	CompletionFIM
	// CompletionEdit may rewrite a nearby region and can drive cursor targets.
	CompletionEdit
)

const (
	defaultMaxUserActions     = 16
	defaultFileChunkLines     = 30
	defaultMaxRecentSnapshots = 3
	defaultMaxDiffBytes       = 4096
	defaultMaxChangedSymbols  = 50
	defaultMaxSiblings        = 50
)

// CompletionStream is the engine-visible runtime for line streaming.
// Provider prompt details, stop rules, cursor markers, and final parsing stay
// behind [CompletionStream.Finish]; engine owns only UI lifecycle.
type CompletionStream interface {
	Lines() <-chan string
	Window() (windowStart int, oldLines []string)
	Cancel()
	Finish() (*types.CompletionResponse, error)
}

// displayedCompletion is the completion state currently rendered in the buffer.
// It is the source for accept, partial accept, typing-match rerender, and Esc
// rejection caching.
type displayedCompletion struct {
	completion      *types.Completion
	batch           buffer.Batch
	originalLines   []string
	groups          []*text.Group
	rejectCandidate *rejectedCompletion
}

func (d *displayedCompletion) show(
	completion *types.Completion,
	batch buffer.Batch,
	originalLines []string,
	groups []*text.Group,
	rejectCandidate *rejectedCompletion,
) {
	*d = displayedCompletion{
		completion:      completion,
		batch:           batch,
		originalLines:   originalLines,
		groups:          groups,
		rejectCandidate: rejectCandidate,
	}
}

func (d *displayedCompletion) reset() {
	*d = displayedCompletion{}
}

func (d *displayedCompletion) hasCompletion() bool {
	return d.completion != nil
}

func (d *displayedCompletion) current() *types.Completion {
	return d.completion
}

func (d *displayedCompletion) textGroups() []*text.Group {
	return d.groups
}

func (d *displayedCompletion) oldLines() []string {
	return d.originalLines
}

func (d *displayedCompletion) batchToApply() buffer.Batch {
	return d.batch
}

func (d *displayedCompletion) rejectionCandidate() *rejectedCompletion {
	return d.rejectCandidate
}

func (d *displayedCompletion) setRejectionCandidate(candidate *rejectedCompletion) {
	d.rejectCandidate = candidate
}

func (d *displayedCompletion) clearRejectionCandidate() {
	d.rejectCandidate = nil
}

func (d *displayedCompletion) advanceLine(wasInsertion bool) bool {
	if d.completion == nil || len(d.completion.Lines) <= 1 {
		return false
	}
	d.completion.Lines = d.completion.Lines[1:]
	d.completion.StartLine++
	d.groups = advanceGroupsAfterAccept(d.groups, wasInsertion)
	return len(d.groups) > 0
}

type streamingState struct {
	StageBuilder *text.IncrementalStageBuilder
	Manual       bool

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

type prefetchWait int

const (
	prefetchNoWait prefetchWait = iota
	prefetchAfterTab
	prefetchForCursorPrediction
)

type prefetchedCompletion struct {
	*types.CompletionResponse
	Manual bool
}

type prefetchInflight struct {
	requestID uint64
	cancel    context.CancelFunc
	wait      prefetchWait
}

type prefetchSlot struct {
	inflight *prefetchInflight
	ready    *prefetchedCompletion
}

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
