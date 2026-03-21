package queue

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// MemoryQueue implements Queue using Go channels for in-process async processing.
// This is not durable — all state is lost on process restart.
// Per ADR-005: "The in-process queue is not a durable system of record."
type MemoryQueue struct {
	mu             sync.RWMutex
	handlers       map[string][]EntryHandler
	entries        chan Entry
	acknowledged   map[string]bool // tracks acknowledged entry IDs
	idempotencySet map[string]bool // tracks seen idempotency keys
	bufferSize     int
	done           chan struct{}
}

// NewMemoryQueue creates a new in-process queue.
func NewMemoryQueue(bufferSize int) *MemoryQueue {
	if bufferSize <= 0 {
		bufferSize = 1000
	}
	return &MemoryQueue{
		handlers:       make(map[string][]EntryHandler),
		entries:        make(chan Entry, bufferSize),
		acknowledged:   make(map[string]bool),
		idempotencySet: make(map[string]bool),
		bufferSize:     bufferSize,
		done:           make(chan struct{}),
	}
}

// Publish adds an entry to the queue.
// If the entry has an idempotency key that was already seen, it is silently skipped.
func (q *MemoryQueue) Publish(ctx context.Context, entry Entry) error {
	if entry.EntryID == "" {
		return fmt.Errorf("entry_id is required")
	}

	q.mu.Lock()
	// Check idempotency
	if entry.IdempotencyKey != "" {
		if q.idempotencySet[entry.IdempotencyKey] {
			q.mu.Unlock()
			return nil // duplicate, silently skip
		}
	}
	q.mu.Unlock()

	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now()
	}

	select {
	case q.entries <- entry:
		// Record idempotency key only after successful enqueue
		if entry.IdempotencyKey != "" {
			q.mu.Lock()
			q.idempotencySet[entry.IdempotencyKey] = true
			q.mu.Unlock()
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Subscribe registers a handler for a specific entry type.
// Multiple handlers can be registered for the same type.
func (q *MemoryQueue) Subscribe(ctx context.Context, entryType string, handler EntryHandler) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.handlers[entryType] = append(q.handlers[entryType], handler)
	return nil
}

// Acknowledge marks an entry as processed.
func (q *MemoryQueue) Acknowledge(ctx context.Context, entryID string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.acknowledged[entryID] = true
	return nil
}

// Start begins processing queue entries. This is a lifecycle method,
// not part of the portability interface. Called directly in main.go during boot.
func (q *MemoryQueue) Start(ctx context.Context) {
	for {
		select {
		case entry := <-q.entries:
			q.dispatch(ctx, entry)
		case <-ctx.Done():
			return
		case <-q.done:
			return
		}
	}
}

// Stop signals the queue to stop processing.
func (q *MemoryQueue) Stop() {
	close(q.done)
}

// Len returns the number of pending entries in the queue.
func (q *MemoryQueue) Len() int {
	return len(q.entries)
}

// IsAcknowledged returns whether an entry has been acknowledged.
func (q *MemoryQueue) IsAcknowledged(entryID string) bool {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return q.acknowledged[entryID]
}

// dispatch sends an entry to all registered handlers for its type.
// If no handlers are registered yet, the entry is requeued after a short delay.
func (q *MemoryQueue) dispatch(ctx context.Context, entry Entry) {
	q.mu.RLock()
	handlers := q.handlers[entry.EntryType]
	q.mu.RUnlock()

	if len(handlers) == 0 {
		// No subscriber yet — requeue with a small delay to avoid busy loop.
		go func() {
			select {
			case <-time.After(50 * time.Millisecond):
				select {
				case q.entries <- entry:
				case <-ctx.Done():
				case <-q.done:
				}
			case <-ctx.Done():
			case <-q.done:
			}
		}()
		return
	}

	for _, handler := range handlers {
		if err := handler(ctx, entry); err != nil {
			// In v0.x, failed entries are logged but not retried by the queue itself.
			// The Workflow Engine handles retry logic at a higher level.
			continue
		}
	}
}
