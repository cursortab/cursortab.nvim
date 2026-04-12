// Package metrics provides unified completion metrics tracking across providers.
package metrics

import (
	"context"
	"time"
)

// EventType represents the type of metrics event
type EventType string

const (
	EventShown    EventType = "shown"    // Completion was displayed to user
	EventAccepted EventType = "accepted" // User accepted the completion
	EventRejected EventType = "rejected" // User explicitly rejected (typed over, pressed escape)
	EventIgnored  EventType = "ignored"  // Completion was dismissed without action (cursor moved, etc.)
)

// CompletionInfo holds metadata about a completion for metrics tracking
type CompletionInfo struct {
	ID        string    // Provider-specific completion ID
	Additions int       // Number of lines added
	Deletions int       // Number of lines deleted
	ShownAt   time.Time // When the completion was shown (for lifespan tracking)
}

// Snapshot captures engine state at the time a completion is shown.
// Used by the community sender to collect anonymous training data.
type Snapshot struct {
	FileExt                string   `json:"file_ext"`
	Language               string   `json:"language"`
	PrefixLength           int      `json:"prefix_length"`
	TrimmedPrefixLength    int      `json:"trimmed_prefix_length"`
	LineCount              int      `json:"line_count"`
	RelativePosition       float64  `json:"relative_position"`
	AfterCursorWS          bool     `json:"after_cursor_ws"`
	LastChar               string   `json:"last_char"`
	LastNonWSChar          string   `json:"last_nonws_char"`
	IndentationLevel       int      `json:"indentation_level"`
	CompletionLines        int      `json:"completion_lines"`
	CompletionAdditions    int      `json:"completion_additions"`
	CompletionDeletions    int      `json:"completion_deletions"`
	CompletionSource       string   `json:"completion_source"`
	ManuallyTriggered      bool     `json:"manually_triggered"`
	Provider               string   `json:"provider"`
	StageIndex             int      `json:"stage_index"`
	CursorTargetDistance   int      `json:"cursor_target_distance"`
	IsPrefetched           bool     `json:"is_prefetched"`
	TimeSinceLastEditMs    int      `json:"time_since_last_edit_ms"`
	TypingSpeed            float64  `json:"typing_speed"`
	RecentActions          []string `json:"recent_actions"`
	HasDiagnostics         bool     `json:"has_diagnostics"`
	TreesitterScope        string   `json:"treesitter_scope"`
	EditCount              int      `json:"edit_count"`
	PredictedEditRatio     float64  `json:"predicted_edit_ratio"`
	CompletionsSinceAccept int      `json:"completions_since_accept"`
}

// Event represents a metrics event with type and completion info
type Event struct {
	Type     EventType
	Info     CompletionInfo
	Snapshot *Snapshot // Non-nil for community metrics (captured at show time)
}

// Sender is the interface that providers implement to send metrics to their backend.
// Implementations should handle unsupported event types gracefully (return early).
// The engine guarantees Info.ID is non-empty when SendMetric is called.
type Sender interface {
	SendMetric(ctx context.Context, event Event)
}

// MultiSender broadcasts metrics events to multiple senders.
type MultiSender struct {
	senders []Sender
}

// NewMultiSender creates a sender that broadcasts to all provided senders.
func NewMultiSender(senders ...Sender) *MultiSender {
	return &MultiSender{senders: senders}
}

func (m *MultiSender) SendMetric(ctx context.Context, event Event) {
	// First sender runs synchronously (provider — fast, important).
	// Additional senders run as fire-and-forget goroutines so slow
	// senders (e.g. HTTP) don't block the metrics channel.
	if len(m.senders) > 0 {
		m.senders[0].SendMetric(ctx, event)
	}
	for _, s := range m.senders[1:] {
		go s.SendMetric(ctx, event)
	}
}
