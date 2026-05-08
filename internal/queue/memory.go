package queue

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// idempotencyTTL controls how long idempotency keys are retained before eviction.
const idempotencyTTL = 10 * time.Minute

// deferredRedispatchInterval controls how often the dispatcher retries entries
// that arrived before any handler was registered. Matches the historical
// per-entry sleep so end-to-end latency on the boot-race path is unchanged.
const deferredRedispatchInterval = 50 * time.Millisecond

// maxRedispatchPerTick bounds the number of deferred entries processed per
// ticker tick so a large backlog cannot head-of-line block reads from
// q.entries. With a 50ms tick the worst-case rescan cadence stays under
// a tick interval even for thousands of orphaned entries; the rest wait
// for the next tick. Picked to be plenty larger than typical boot-race
// volumes (single digits) while keeping pathological-case stall bounded.
const maxRedispatchPerTick = 256

// MemoryQueue implements Queue using Go channels for in-process async processing.
// This is not durable — all state is lost on process restart.
// Per ADR-005: "The in-process queue is not a durable system of record."
type MemoryQueue struct {
	mu             sync.RWMutex
	handlers       map[string][]EntryHandler
	entries        chan Entry
	acknowledged   map[string]bool      // tracks acknowledged entry IDs
	idempotencySet map[string]time.Time // tracks seen idempotency keys with insertion time
	bufferSize     int
	done           chan struct{}

	// deferred holds entries dispatched while no handler was registered
	// for their type. Owned exclusively by Start()'s goroutine — accessed
	// only from dispatch and redispatchDeferred, which run within that
	// loop, so no mutex is needed.
	deferred []Entry
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
		idempotencySet: make(map[string]time.Time),
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
	if entry.IdempotencyKey != "" {
		if ts, ok := q.idempotencySet[entry.IdempotencyKey]; ok && time.Since(ts) < idempotencyTTL {
			q.mu.Unlock()
			return nil // duplicate, silently skip
		}
		// Reserve the key before releasing the lock so concurrent
		// publishers with the same key are rejected immediately.
		q.idempotencySet[entry.IdempotencyKey] = time.Now()
	}
	q.mu.Unlock()

	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now()
	}

	select {
	case q.entries <- entry:
		return nil
	case <-ctx.Done():
		if entry.IdempotencyKey != "" {
			q.mu.Lock()
			delete(q.idempotencySet, entry.IdempotencyKey)
			q.mu.Unlock()
		}
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
	evictTicker := time.NewTicker(idempotencyTTL)
	defer evictTicker.Stop()

	redispatchTicker := time.NewTicker(deferredRedispatchInterval)
	defer redispatchTicker.Stop()

	for {
		select {
		case entry := <-q.entries:
			q.dispatch(ctx, entry)
		case <-redispatchTicker.C:
			q.redispatchDeferred(ctx)
		case <-evictTicker.C:
			q.evictExpiredKeys()
		case <-ctx.Done():
			return
		case <-q.done:
			return
		}
	}
}

// evictExpiredKeys removes idempotency keys older than idempotencyTTL.
func (q *MemoryQueue) evictExpiredKeys() {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := time.Now()
	for key, ts := range q.idempotencySet {
		if now.Sub(ts) >= idempotencyTTL {
			delete(q.idempotencySet, key)
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
// If no handlers are registered yet, the entry is parked on the deferred
// list and retried by redispatchDeferred on the next ticker tick.
//
// INIT-022 EPIC-001 TASK-011: replaced the per-requeue goroutine spawn —
// a noisy publisher with no subscriber registered yet could otherwise
// fan out arbitrarily many sleeping goroutines all racing to write back
// to q.entries. The deferred list bounds the dispatcher to a single
// goroutine (Start's loop) regardless of inbound rate.
//
// Memory tradeoff: q.deferred is unbounded — pathological "no
// subscriber ever registers" workloads cause memory growth proportional
// to publish volume. Pre-fix the same workload spawned unbounded
// goroutines (≈8KB stack each), so this is strictly an improvement on
// memory cost; bounding the slice would either drop items (forbidden
// by the AC's boot-race retention) or block head-of-line for unrelated
// entry types. Per ADR-005 the in-process queue is not durable — a
// misconfigured caller publishing without a handler is an operator
// bug, not a steady-state condition.
func (q *MemoryQueue) dispatch(ctx context.Context, entry Entry) {
	q.mu.RLock()
	handlers := q.handlers[entry.EntryType]
	q.mu.RUnlock()

	if len(handlers) == 0 {
		q.deferred = append(q.deferred, entry)
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

// redispatchDeferred drains a bounded prefix of the deferred list,
// attempting to dispatch each entry against currently registered
// handlers. Entries whose type still has no handler are re-parked for
// the next tick; entries beyond the per-tick bound stay deferred so a
// large backlog does not head-of-line block reads from q.entries. Runs
// on the Start goroutine so no synchronisation is needed for q.deferred.
func (q *MemoryQueue) redispatchDeferred(ctx context.Context) {
	if len(q.deferred) == 0 {
		return
	}

	n := len(q.deferred)
	if n > maxRedispatchPerTick {
		n = maxRedispatchPerTick
	}

	// Take pending as a 3-index slice so a stray append could not write
	// into q.deferred's backing array; dispatch only ever appends to
	// q.deferred so this is defensive but free.
	pending := q.deferred[:n:n]
	q.deferred = q.deferred[n:]

	// When the deferred list is fully drained on this tick, drop the
	// slice header so the backing array becomes GC-eligible immediately
	// rather than lingering on the next-tick fast-path. (For a non-empty
	// remainder, the next dispatch append-back triggers a realloc that
	// frees the original array on its own.)
	if len(q.deferred) == 0 {
		q.deferred = nil
	}

	for _, entry := range pending {
		select {
		case <-ctx.Done():
			return
		case <-q.done:
			return
		default:
		}
		q.dispatch(ctx, entry)
	}
}
