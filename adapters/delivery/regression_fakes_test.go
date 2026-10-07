package delivery

// Shared fakes for the INIT-023 TASK-010 regression tests
// (signature_regression_test.go, queue_regression_test.go,
// ssrf_redact_regression_test.go). They were ported from the audit
// proofs of concept with their assertions inverted.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/store"
)

// queueStore mirrors adapters/store/postgres_delivery.go closely enough
// for the dispatcher's queue behaviour to be observable:
//
//   - ClaimDeliveries uses the same predicate (pending/failed, due) and
//     the same fair order (per-subscription rank by created_at, then
//     created_at — round-robin across subscriptions, FIFO within one);
//     fifoClaim switches it back to the old global created_at order so a
//     test can show what the fair claim buys;
//   - UpdateDeliveryStatus and MarkDelivered bump attempt_count, as the
//     SQL does on every status write;
//   - DeferDelivery does not, and only touches a row still 'delivering';
//   - HoldDueDeliveries holds (or, past ExpireBefore, dead-letters) every
//     due row of one subscription without touching attempt_count, as the
//     SQL does; noBulkHold turns it into a no-op so a test can show what
//     the bulk hold buys.
//
// Its clock is the test's: advance() moves it, and now() can be wired
// into the dispatcher and the breaker so all three share one clock.
type queueStore struct {
	mu        sync.Mutex
	clockNow  time.Time
	rows      map[string]*store.DeliveryEntry
	logs      []store.DeliveryLogEntry
	defers    int
	holds     int
	fifoClaim bool
	// noBulkHold makes HoldDueDeliveries a no-op (the pre-review-round-1
	// dispatcher deferred only the rows it was handed).
	noBulkHold bool
}

func newQueueStore() *queueStore {
	return &queueStore{clockNow: time.Now(), rows: map[string]*store.DeliveryEntry{}}
}

func (s *queueStore) now() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clockNow
}

func (s *queueStore) advance(d time.Duration) {
	s.mu.Lock()
	s.clockNow = s.clockNow.Add(d)
	s.mu.Unlock()
}

func (s *queueStore) ClaimDeliveries(_ context.Context, limit int) ([]store.DeliveryEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var cand []*store.DeliveryEntry
	for _, r := range s.rows {
		if (r.Status == "pending" || r.Status == "failed") && (r.NextRetryAt == nil || !r.NextRetryAt.After(s.clockNow)) {
			cand = append(cand, r)
		}
	}
	sort.Slice(cand, func(i, j int) bool {
		if !cand[i].CreatedAt.Equal(cand[j].CreatedAt) {
			return cand[i].CreatedAt.Before(cand[j].CreatedAt)
		}
		return cand[i].DeliveryID < cand[j].DeliveryID
	})
	if !s.fifoClaim {
		// row_number() OVER (PARTITION BY subscription_id ORDER BY
		// created_at), then ORDER BY rn, created_at.
		rank := map[string]int{}
		rn := make(map[string]int, len(cand))
		for _, r := range cand {
			rank[r.SubscriptionID]++
			rn[r.DeliveryID] = rank[r.SubscriptionID]
		}
		sort.SliceStable(cand, func(i, j int) bool { return rn[cand[i].DeliveryID] < rn[cand[j].DeliveryID] })
	}
	if len(cand) > limit {
		cand = cand[:limit]
	}
	out := make([]store.DeliveryEntry, 0, len(cand))
	for _, r := range cand {
		r.Status = "delivering"
		out = append(out, *r)
	}
	return out, nil
}

func (s *queueStore) UpdateDeliveryStatus(_ context.Context, id, status, lastErr string, next *time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[id]
	if !ok {
		return nil
	}
	r.Status, r.LastError, r.NextRetryAt = status, lastErr, next
	r.AttemptCount++
	return nil
}

func (s *queueStore) DeferDelivery(_ context.Context, id, reason string, until time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[id]
	if !ok || r.Status != "delivering" {
		return nil
	}
	u := until
	r.Status, r.LastError, r.NextRetryAt = "pending", reason, &u
	s.defers++
	return nil
}

func (s *queueStore) HoldDueDeliveries(_ context.Context, h store.DeliveryHold) ([]store.DeliveryEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.holds++
	if s.noBulkHold {
		return nil, nil
	}
	var expired []store.DeliveryEntry
	for _, r := range s.rows {
		if r.SubscriptionID != h.SubscriptionID || (r.Status != "pending" && r.Status != "failed") {
			continue
		}
		if r.NextRetryAt != nil && !r.NextRetryAt.Before(h.Until) {
			continue
		}
		if !h.ExpireBefore.IsZero() && r.CreatedAt.Before(h.ExpireBefore) {
			r.Status, r.LastError, r.NextRetryAt = "dead", h.ExpiredReason, nil
			expired = append(expired, *r)
			continue
		}
		if r.Status == "pending" {
			r.LastError = h.Reason
		}
		u := h.Until
		r.NextRetryAt = &u
	}
	return expired, nil
}

func (s *queueStore) MarkDelivered(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.rows[id]; ok {
		r.Status = "delivered"
		r.AttemptCount++
	}
	return nil
}

func (s *queueStore) LogDeliveryAttempt(_ context.Context, e *store.DeliveryLogEntry) error {
	s.mu.Lock()
	s.logs = append(s.logs, *e)
	s.mu.Unlock()
	return nil
}

func (s *queueStore) EnqueueDelivery(context.Context, *store.DeliveryEntry) error { return nil }
func (s *queueStore) ListDeliveryHistory(context.Context, store.DeliveryHistoryQuery) ([]store.DeliveryLogEntry, error) {
	return nil, nil
}
func (s *queueStore) GetDelivery(context.Context, string) (*store.DeliveryEntry, error) {
	return nil, nil
}
func (s *queueStore) ListDeliveries(context.Context, string, string, int) ([]store.DeliveryEntry, error) {
	return nil, nil
}
func (s *queueStore) GetDeliveryStats(context.Context, string) (*store.DeliveryStats, error) {
	return nil, nil
}
func (s *queueStore) WriteEventLog(context.Context, *store.EventLogEntry) error { return nil }
func (s *queueStore) ListEventsAfter(context.Context, string, []string, int) ([]store.EventLogEntry, error) {
	return nil, nil
}
func (s *queueStore) DeleteExpiredDeliveries(context.Context, time.Time) (int64, error) {
	return 0, nil
}

// add enqueues a pending step_assigned row.
func (s *queueStore) add(id, sub string, created time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[id] = &store.DeliveryEntry{DeliveryID: id, SubscriptionID: sub, EventID: id,
		EventType: "step_assigned", Payload: []byte(`{}`), Status: "pending", CreatedAt: created}
}

func (s *queueStore) row(id string) store.DeliveryEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return *s.rows[id]
}

func (s *queueStore) logsFor(id string) []store.DeliveryLogEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []store.DeliveryLogEntry
	for _, l := range s.logs {
		if l.DeliveryID == id {
			out = append(out, l)
		}
	}
	return out
}

// mapResolver serves SubscriptionDetails by id. An error in errs is
// returned once and then cleared, unless the id is in stick.
type mapResolver struct {
	mu    sync.Mutex
	subs  map[string]*SubscriptionDetail
	errs  map[string]error
	stick map[string]bool
	calls atomic.Int64
}

func (r *mapResolver) GetSubscription(_ context.Context, id string) (*SubscriptionDetail, error) {
	r.calls.Add(1)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err, ok := r.errs[id]; ok {
		if !r.stick[id] {
			delete(r.errs, id)
		}
		return nil, err
	}
	sub, ok := r.subs[id]
	if !ok {
		return nil, context.Canceled
	}
	c := *sub
	return &c, nil
}

// statusServer answers every request with status and counts the hits.
func statusServer(t *testing.T, status int) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// sharedClock wires the store's clock into the dispatcher and its
// breaker, so deferral times, backoff and probe times all move with
// queueStore.advance.
func sharedClock(d *WebhookDispatcher, st *queueStore) {
	d.now = st.now
	d.breaker.now = st.now
}

// tripOpen puts sub's circuit into the open state with its last failure
// at lastFailure; the outage began then too.
func tripOpen(cb *CircuitBreaker, sub string, lastFailure time.Time) {
	tripOpenSince(cb, sub, lastFailure, lastFailure)
}

// tripOpenSince puts sub's circuit into the open state for an outage that
// began at openedAt (CircuitBreaker.OpenedAt), its last failure at
// lastFailure.
func tripOpenSince(cb *CircuitBreaker, sub string, openedAt, lastFailure time.Time) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.circuits[sub] = &circuitEntry{state: CircuitOpen, consecutiveFailures: circuitFailureThreshold, lastFailureAt: lastFailure, openedAt: openedAt}
}
