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

// StagedCompletion holds the queue of pending stages
// Note: Uses any to avoid circular import with text package
type StagedCompletion struct {
	Stages           []any // []*text.Stage - using any to avoid circular import
	CurrentIdx       int
	SourcePath       string
	CumulativeOffset int // Tracks line count drift after each stage accept (for unequal line counts)
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
	// Multi-file diff histories in the same workspace
	FileDiffHistories []*FileDiffHistory
	// Cursor position
	CursorRow int // 1-indexed
	CursorCol int // 0-indexed
	// Viewport constraint: only set when staging is disabled (0 = no limit)
	ViewportHeight int
	// Linter errors if LSP is active
	LinterErrors *LinterErrors
}

// CompletionResponse contains both completions and cursor prediction target
type CompletionResponse struct {
	Completions  []*Completion
	CursorTarget *CursorPredictionTarget // Optional, from cursor_prediction_target
}

// LinterErrors represents linter error information for the current file
type LinterErrors struct {
	RelativeWorkspacePath string
	Errors                []*LinterError
	FileContents          string
}

// LinterError represents a single linter error
type LinterError struct {
	Message  string
	Source   string
	Severity string
	Range    *CursorRange
}

// FileDiffHistory represents cumulative diffs for a specific file in the workspace
type FileDiffHistory struct {
	FileName    string
	DiffHistory []*DiffEntry
}

// DiffEntry represents a single diff operation with structured before/after content
// This allows providers to format the diff in their required format
type DiffEntry struct {
	// Original is the content before the change (the text that was replaced/deleted)
	Original string
	// Updated is the content after the change (the new text)
	Updated string
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

// ProviderType represents the type of provider
type ProviderType string

const (
	ProviderTypeInline   ProviderType = "inline"
	ProviderTypeFIM      ProviderType = "fim"
	ProviderTypeSweep    ProviderType = "sweep"
	ProviderTypeSweepAPI ProviderType = "sweepapi"
	ProviderTypeZeta     ProviderType = "zeta"
)

// FIMTokenConfig holds FIM (Fill-in-the-Middle) token configuration
type FIMTokenConfig struct {
	Prefix string // Token before the prefix content (e.g., "<|fim_prefix|>")
	Suffix string // Token before the suffix content (e.g., "<|fim_suffix|>")
	Middle string // Token before the middle/completion (e.g., "<|fim_middle|>")
}

// ProviderConfig holds configuration for providers
type ProviderConfig struct {
	ProviderURL         string         // URL of the provider server (e.g., "http://localhost:8000")
	APIKey              string         // Resolved API key for authenticated requests
	ProviderModel       string         // Model name
	ProviderTemperature float64        // Sampling temperature
	ProviderMaxTokens   int            // Max tokens to generate (also drives input trimming)
	ProviderTopK        int            // Top-k sampling (used by some providers)
	CompletionPath      string         // API endpoint path (e.g., "/v1/completions")
	FIMTokens           FIMTokenConfig // FIM tokens configuration
	CompletionTimeout   int            // Timeout for completion requests in milliseconds
	PrivacyMode         bool           // Don't send telemetry to provider
}
