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

// Event represents a metrics event with type and completion info
type Event struct {
	Type EventType
	Info CompletionInfo
}

// Sender is the interface that providers implement to send metrics to their backend.
// Implementations should handle unsupported event types gracefully (return early).
// The engine guarantees Info.ID is non-empty when SendMetric is called.
type Sender interface {
	SendMetric(ctx context.Context, event Event)
}
