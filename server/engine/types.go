package engine

import (
	"context"
	"cursortab/history"
	"time"

	"cursortab/buffer"
	"cursortab/ctx"
	"cursortab/text"
	"cursortab/types"
)

// Buffer is the editor side of the engine: a cached snapshot of the current
// buffer and window that Sync refreshes, plus rendering and edit operations.
type Buffer interface {
	// Sync refreshes the snapshot and reports whether the current buffer
	// changed since the previous sync.
	Sync() (switched bool, err error)
	WorkspacePath() string
	Path() string
	Modified() bool // Changes since the buffer was last written
	Lines() []string
	Row() int // 1-indexed
	Col() int // 0-indexed byte column
	ViewportBounds() (top, bottom int)
	AvailableWidth() int

	Diagnostics() *types.Diagnostics
	TreesitterSymbols(row int, col int, maxSiblings int) *types.TreesitterContext
	CursorScopes() []string // Treesitter node types from cursor to root

	// PrepareCompletion renders a completion and returns the batch that
	// applies it to the buffer and the snapshot.
	PrepareCompletion(startLine, endLineInc int, lines []string, groups []*text.Group) buffer.Batch
	ShowCursorTarget(line int)
	ClearUI()

	MoveCursor(line int, center, mark bool) error
	AppendText(line int, text string) error // Appends to the end of line (1-indexed)
	// SetLines applies edit and moves the cursor to the end of its last line.
	SetLines(edit history.Edit) error
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
	linesAfter      int // buffer lines after the completion's range
	groups          []*text.Group
	rejectCandidate *rejectedCompletion
}

func (d *displayedCompletion) show(
	completion *types.Completion,
	batch buffer.Batch,
	originalLines []string,
	linesAfter int,
	groups []*text.Group,
	rejectCandidate *rejectedCompletion,
) {
	*d = displayedCompletion{
		completion:      completion,
		batch:           batch,
		originalLines:   originalLines,
		linesAfter:      linesAfter,
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

// activeStream is a completion being received line by line.
type activeStream struct {
	stream       CompletionStream
	lines        <-chan string
	stageBuilder *text.IncrementalStageBuilder
	manual       bool

	// pendingLine holds the last received line until the next one arrives,
	// so a line truncated by the end of the stream is never staged.
	pendingLine    string
	hasPendingLine bool

	firstStageRendered bool
	// acceptedDuring is set when the user accepted the rendered stage while
	// the stream was still running; the rest of the stream then becomes the
	// next prediction.
	acceptedDuring bool
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

// Config holds engine configuration
type Config struct {
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
