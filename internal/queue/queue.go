package queue

import (
	"context"
	"time"
)

// Queue defines the portability interface for async work item processing.
// Per Implementation Guide §3.2.
type Queue interface {
	Publish(ctx context.Context, entry Entry) error
	Subscribe(ctx context.Context, entryType string, handler EntryHandler) error
	Acknowledge(ctx context.Context, entryID string) error
}

// EntryHandler processes a queue entry. Return nil to acknowledge, error to retry.
type EntryHandler func(ctx context.Context, entry Entry) error

// Entry represents a work item in the queue.
type Entry struct {
	EntryID        string    `json:"entry_id"`
	EntryType      string    `json:"entry_type"`      // e.g., "step_assignment", "event_delivery"
	Payload        []byte    `json:"payload"`         // JSON-encoded payload
	IdempotencyKey string    `json:"idempotency_key"` // Prevents duplicate processing
	Priority       int       `json:"priority"`        // Higher = more urgent
	CreatedAt      time.Time `json:"created_at"`
}
