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

// maxRedispatchPerTick bounds the number of deferred entries dispatched
// per ticker tick so a large backlog cannot head-of-line block reads
// from q.entries. With a 50ms tick the worst-case rescan cadence stays
// under a tick interval even for thousands of orphaned entries; the
// rest wait for the next tick. Picked to be plenty larger than typical
// boot-race volumes (single digits) while keeping pathological-case
// stall bounded.
//
// Budget is total across all handled buckets; no-handler buckets are
// never visited (see redispatchDeferred), so a publisher dumping many
// distinct unhandled types cannot inflate the per-tick scan. Map
// iteration order over handled types is randomised by the runtime,
// which gives natural fairness across ticks when several handled
// buckets are non-empty.
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
	// for their type, bucketed by EntryType. Owned exclusively by Start()'s
	// goroutine — accessed only from dispatch and redispatchDeferred, which
	// run within that loop, so no mutex is needed. Bucketing (INIT-022
	// EPIC-001 TASK-033) means redispatchDeferred can skip whole noisy
	// no-handler buckets and only spend its per-tick budget on buckets
	// whose handlers are registered, so a B entry parked behind N A entries
	// is no longer delayed by ⌈N/maxRedispatchPerTick⌉ ticks once B's
	// handler arrives.
	deferred map[string][]Entry
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
		deferred:       make(map[string][]Entry),
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
// If no handlers are registered yet, the entry is parked in the
// per-type deferred bucket and retried by redispatchDeferred on the
// next ticker tick.
//
// INIT-022 EPIC-001 TASK-011: replaced the per-requeue goroutine spawn —
// a noisy publisher with no subscriber registered yet could otherwise
// fan out arbitrarily many sleeping goroutines all racing to write back
// to q.entries. The deferred map bounds the dispatcher to a single
// goroutine (Start's loop) regardless of inbound rate.
//
// INIT-022 EPIC-001 TASK-033: deferred is bucketed per EntryType so that
// a noisy no-handler type cannot head-of-line block a different type
// whose handler has just arrived. redispatchDeferred only consumes
// budget against buckets with a registered handler.
//
// Memory tradeoff: q.deferred is unbounded — pathological "no
// subscriber ever registers" workloads cause memory growth proportional
// to publish volume. Pre-fix the same workload spawned unbounded
// goroutines (≈8KB stack each), so this is strictly an improvement on
// memory cost; bounding the buckets would either drop items (forbidden
// by the AC's boot-race retention) or invite per-type policy decisions
// the in-process queue is not the right place to make. Per ADR-005 the
// in-process queue is not durable — a misconfigured caller publishing
// without a handler is an operator bug, not a steady-state condition.
func (q *MemoryQueue) dispatch(ctx context.Context, entry Entry) {
	q.mu.RLock()
	handlers := q.handlers[entry.EntryType]
	q.mu.RUnlock()

	if len(handlers) == 0 {
		q.deferred[entry.EntryType] = append(q.deferred[entry.EntryType], entry)
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

// redispatchDeferred consumes a bounded total budget across the per-type
// deferred buckets, dispatching entries from buckets whose handler has
// since registered. The walk iterates the *handlers* map rather than
// q.deferred, so no-handler buckets are never visited — a publisher
// dumping many distinct unhandled types cannot inflate the per-tick
// scan, and a noisy no-handler type cannot starve a handled type within
// the per-tick budget. This is the head-of-line fairness guarantee
// from INIT-022 EPIC-001 TASK-033. Map iteration order is randomised
// by the runtime, so across ticks several handled buckets share the
// budget roughly proportionally. Runs on the Start goroutine so no
// synchronisation is needed for q.deferred itself; q.handlers is read
// once under RLock to snapshot which types have handlers.
//
// Handler removal is not part of the queue API (Subscribe only appends),
// so a type captured in the snapshot is guaranteed to still have a
// handler by the time q.dispatch re-reads handlers — every entry
// drained from a handled bucket reaches its handler this tick and does
// not re-park.
func (q *MemoryQueue) redispatchDeferred(ctx context.Context) {
	if len(q.deferred) == 0 {
		return
	}

	q.mu.RLock()
	handledTypes := make([]string, 0, len(q.handlers))
	for typ, hs := range q.handlers {
		if len(hs) > 0 {
			handledTypes = append(handledTypes, typ)
		}
	}
	q.mu.RUnlock()

	budget := maxRedispatchPerTick
	for _, typ := range handledTypes {
		if budget <= 0 {
			break
		}
		entries, ok := q.deferred[typ]
		if !ok || len(entries) == 0 {
			continue
		}

		n := len(entries)
		if n > budget {
			n = budget
		}

		// 3-index slice so a stray append could not write into entries'
		// backing array. Defensive — dispatch only writes back to the
		// bucket via append on q.deferred[typ], not via this slice.
		pending := entries[:n:n]
		if n == len(entries) {
			delete(q.deferred, typ)
		} else {
			q.deferred[typ] = entries[n:]
		}
		budget -= n

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
}
