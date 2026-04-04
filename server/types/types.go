package types

// Completion represents a code completion with line range and content
type Completion struct {
	StartLine  int // 1-indexed
	EndLineInc int // 1-indexed, inclusive
	Lines      []string
}

type CompletionSource int

const (
	CompletionSourceTyping CompletionSource = iota
	CompletionSourceIdle
)

// CursorPredictionTarget represents the target for cursor jump with additional metadata
type CursorPredictionTarget struct {
	RelativePath    string
	LineNumber      int32 // 1-indexed
	ExpectedContent string
	ShouldRetrigger bool
}

// CompletionRequest contains all the context needed for unified completion requests
type CompletionRequest struct {
	Source        CompletionSource
	WorkspacePath string
	WorkspaceID   string
	// File context
	FilePath string
	Lines    []string
	Version  int
	// PreviousLines is the file content before the most recent edit
	PreviousLines []string
	// OriginalLines is the file content when first opened (checkpoint baseline)
	OriginalLines []string
	// Multi-file diff histories in the same workspace
	FileDiffHistories []*FileDiffHistory
	// Cursor position
	CursorRow int // 1-indexed
	CursorCol int // 0-indexed
	// Viewport constraint: only set when staging is disabled (0 = no limit)
	ViewportHeight int
	// MaxVisibleLines limits max visible lines per completion (0 = no limit)
	MaxVisibleLines int
	// AdditionalContext holds gathered context from context sources (diagnostics, etc.)
	AdditionalContext *ContextResult
	// RecentBufferSnapshots contains snapshots of recently accessed files for cross-file context
	RecentBufferSnapshots []*RecentBufferSnapshot
	// UserActions contains recent user edit actions for the current file
	UserActions []*UserAction
}

// CompletionResponse contains both completions and cursor prediction target
type CompletionResponse struct {
	Completions  []*Completion
	CursorTarget *CursorPredictionTarget // Optional, from cursor_prediction_target
	MetricsInfo  *MetricsInfo            // Optional, for providers that track metrics
}

// MetricsInfo holds metadata for metrics tracking
type MetricsInfo struct {
	ID        string // Provider-specific completion ID
	Additions int    // Number of lines added
	Deletions int    // Number of lines deleted
}

// DiagnosticSeverity matches Neovim's vim.diagnostic.severity values.
type DiagnosticSeverity int

const (
	SeverityError       DiagnosticSeverity = 1
	SeverityWarning     DiagnosticSeverity = 2
	SeverityInformation DiagnosticSeverity = 3
	SeverityHint        DiagnosticSeverity = 4
)

// String returns the short uppercase label for the severity.
func (s DiagnosticSeverity) String() string {
	switch s {
	case SeverityError:
		return "ERROR"
	case SeverityWarning:
		return "WARNING"
	case SeverityInformation:
		return "INFORMATION"
	case SeverityHint:
		return "HINT"
	default:
		return "ERROR"
	}
}

// Diagnostics holds LSP diagnostics for a buffer.
type Diagnostics struct {
	FilePath string        // Workspace-relative path
	Items    []*Diagnostic // Individual diagnostic entries
}

// Diagnostic represents a single LSP diagnostic from Neovim.
type Diagnostic struct {
	Message  string
	Source   string
	Severity DiagnosticSeverity
	Range    *CursorRange
}

// TreesitterContext holds treesitter-derived scope information around the cursor
type TreesitterContext struct {
	EnclosingSignature string
	Siblings           []*TreesitterSymbol
	Imports            []string
	// SyntaxRanges contains ancestor AST node line ranges around the cursor,
	// ordered innermost to outermost. Used to snap editable/context regions
	// to meaningful syntax boundaries (e.g. function, class, block).
	SyntaxRanges []*LineRange
}

// LineRange represents a 1-indexed inclusive line range
type LineRange struct {
	StartLine int // 1-indexed
	EndLine   int // 1-indexed, inclusive
}

// TreesitterSymbol represents a named symbol extracted from treesitter
type TreesitterSymbol struct {
	Name      string
	Signature string
	Line      int // 1-indexed
}

// GitDiffContext holds staged git diff information for commit message editing.
// Contains either the full unified diff (when small) or extracted symbol lines.
type GitDiffContext struct {
	Diff string // Full unified diff or symbol summary in git diff format
}

// ContextResult holds gathered context from context sources
type ContextResult struct {
	Diagnostics *Diagnostics       // LSP diagnostics (nil if unavailable)
	Treesitter  *TreesitterContext // Treesitter scope context (nil if unavailable)
	GitDiff     *GitDiffContext    // Staged git diff (nil if not COMMIT_EDITMSG)
}

// GetDiagnostics returns diagnostics from AdditionalContext, or nil if unavailable
func (r *CompletionRequest) GetDiagnostics() *Diagnostics {
	if r.AdditionalContext == nil {
		return nil
	}
	return r.AdditionalContext.Diagnostics
}

// GetTreesitter returns treesitter context from AdditionalContext, or nil if unavailable
func (r *CompletionRequest) GetTreesitter() *TreesitterContext {
	if r.AdditionalContext == nil {
		return nil
	}
	return r.AdditionalContext.Treesitter
}

// GetGitDiff returns git diff context from AdditionalContext, or nil if unavailable
func (r *CompletionRequest) GetGitDiff() *GitDiffContext {
	if r.AdditionalContext == nil {
		return nil
	}
	return r.AdditionalContext.GitDiff
}

// FileDiffHistory represents cumulative diffs for a specific file in the workspace
type FileDiffHistory struct {
	FileName    string
	DiffHistory []*DiffEntry
}

// DiffSource indicates the origin of a diff entry
type DiffSource string

const (
	DiffSourceManual    DiffSource = "manual"
	DiffSourcePredicted DiffSource = "predicted"
)

// DiffEntry represents a single diff operation with structured before/after content
// This allows providers to format the diff in their required format
type DiffEntry struct {
	// Original is the content before the change (the text that was replaced/deleted)
	Original string
	// Updated is the content after the change (the new text)
	Updated string
	// Source indicates whether this change was manual (user) or predicted (AI)
	Source DiffSource
	// TimestampNs is when the change was recorded (UnixNano)
	TimestampNs int64
	// StartLine is the approximate 1-indexed line in the buffer where the change starts
	StartLine int
}

// GetOriginal returns the original content (implements utils.DiffEntry interface)
func (d *DiffEntry) GetOriginal() string { return d.Original }

// GetUpdated returns the updated content (implements utils.DiffEntry interface)
func (d *DiffEntry) GetUpdated() string { return d.Updated }

// CursorRange represents a range in the file (follows LSP conventions)
type CursorRange struct {
	StartLine      int // 1-indexed
	StartCharacter int // 0-indexed
	EndLine        int // 1-indexed
	EndCharacter   int // 0-indexed
}

// RecentBufferSnapshot represents a snapshot of another open file for context
type RecentBufferSnapshot struct {
	FilePath    string   // Full file path
	Lines       []string // First N lines of the file
	TimestampMs int64    // Unix epoch milliseconds when file was last accessed
}

// UserActionType represents the type of user action
type UserActionType string

const (
	ActionInsertChar      UserActionType = "INSERT_CHAR"
	ActionInsertSelection UserActionType = "INSERT_SELECTION"
	ActionDeleteChar      UserActionType = "DELETE_CHAR"
	ActionDeleteSelection UserActionType = "DELETE_SELECTION"
	ActionCursorMovement  UserActionType = "CURSOR_MOVEMENT"
)

// UserAction represents a tracked user edit action
type UserAction struct {
	ActionType  UserActionType
	FilePath    string
	LineNumber  int   // 1-indexed
	Offset      int   // Byte offset in file
	TimestampMs int64 // Unix epoch milliseconds
}

// ProviderType represents the type of provider
type ProviderType string

const (
	ProviderTypeInline     ProviderType = "inline"
	ProviderTypeFIM        ProviderType = "fim"
	ProviderTypeSweep      ProviderType = "sweep"
	ProviderTypeZeta       ProviderType = "zeta"
	ProviderTypeZeta2      ProviderType = "zeta-2"
	ProviderTypeCopilot    ProviderType = "copilot"
	ProviderTypeMercuryAPI ProviderType = "mercuryapi"
)

// IsEditCompletion reports whether the provider produces edit-style (multi-line,
// region-replacing) completions, as opposed to inline/FIM single-point fills.
func (p ProviderType) IsEditCompletion() bool {
	switch p {
	case ProviderTypeSweep, ProviderTypeZeta, ProviderTypeZeta2,
		ProviderTypeCopilot, ProviderTypeMercuryAPI:
		return true
	}
	return false
}

// FIMTokenConfig holds FIM (Fill-in-the-Middle) token configuration
type FIMTokenConfig struct {
	Prefix   string // Token before the prefix content (e.g., "<|fim_prefix|>")
	Suffix   string // Token before the suffix content (e.g., "<|fim_suffix|>")
	Middle   string // Token before the middle/completion (e.g., "<|fim_middle|>")
	RepoName string // Optional repo-level FIM token (e.g., "<|repo_name|>")
	FileSep  string // Optional file separator token (e.g., "<|file_sep|>")
}

// ProviderConfig holds configuration for providers
type ProviderConfig struct {
	ProviderURL         string         // URL of the provider server (e.g., "http://localhost:8000")
	APIKey              string         // Resolved API key for authenticated requests
	ProviderModel       string         // Model name
	ProviderTemperature float64        // Sampling temperature
	ProviderContextSize int            // Max input context size in tokens (0 = use ProviderMaxTokens)
	ProviderMaxTokens   int            // Max tokens to generate
	ProviderTopK        int            // Top-k sampling (used by some providers)
	CompletionPath      string         // API endpoint path (e.g., "/v1/completions")
	FIMTokens           FIMTokenConfig // FIM tokens configuration
	CompletionTimeout   int            // Timeout for completion requests in milliseconds
	PrivacyMode         bool           // Don't send telemetry to provider
	Version             string         // Plugin version for metrics/telemetry
	EditorVersion       string         // Editor version (e.g., "0.10.0")
	EditorOS            string         // Operating system name (e.g., "Darwin")
	StateDir            string         // State directory for persistent data (device_id, etc.)
	DeviceID            string         // Persistent device identifier
}
