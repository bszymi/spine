package delivery

// INIT-023 TASK-010 regression tests for audit P3-5 (non-retryable and
// circuit-held deliveries are re-claimed ahead of newer rows and starve
// the queue), ported from the audit's delivery-scheduler proofs of
// concept with their assertions inverted, plus the design check's
// held-backlog starvation probe.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/domain"
)

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLogs routes slog.Default (which observe.Logger builds on) into a
// buffer for the rest of the test. Tests using it must not be parallel.
func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// TestNonRetryable4xxDeadLettersAndDoesNotStarve (AC8): five 410 rows
// ahead of one healthy row. Each 410 row gets exactly one attempt and is
// dead; the healthy row is delivered. Before the fix the 410 rows were
// written back as immediately claimable 'failed' and re-POSTed every
// poll, ahead of the healthy row, forever.
func TestNonRetryable4xxDeadLettersAndDoesNotStarve(t *testing.T) {
	bad, badHits := statusServer(t, http.StatusGone)
	good, goodHits := statusServer(t, http.StatusOK)
	st := newQueueStore()
	base := st.now().Add(-time.Hour)
	for i := 0; i < 5; i++ {
		st.add(fmt.Sprintf("bad-%d", i), "sub-bad", base.Add(time.Duration(i)*time.Second))
	}
	st.add("good-1", "sub-good", base.Add(time.Minute))
	res := &mapResolver{subs: map[string]*SubscriptionDetail{
		"sub-bad":  {SubscriptionID: "sub-bad", TargetURL: bad.URL},
		"sub-good": {SubscriptionID: "sub-good", TargetURL: good.URL},
	}}
	d := NewWebhookDispatcher(st, res, DispatcherConfig{})
	sharedClock(d, st)
	for i := 0; i < 50; i++ {
		d.poll(context.Background())
	}
	if goodHits.Load() != 1 {
		t.Fatalf("healthy subscription: %d deliveries, want exactly 1", goodHits.Load())
	}
	if badHits.Load() != 5 {
		t.Fatalf("410 endpoint POSTed %d times, want exactly one attempt per row (5)", badHits.Load())
	}
	for i := 0; i < 5; i++ {
		r := st.row(fmt.Sprintf("bad-%d", i))
		if r.Status != "dead" || r.AttemptCount != 1 || r.LastError != "HTTP 410" {
			t.Fatalf("row %s = status %q attempts %d last_error %q, want dead/1/HTTP 410", r.DeliveryID, r.Status, r.AttemptCount, r.LastError)
		}
	}
}

// TestTLSConfigErrorDeadLetters (AC8): a per-subscription TLS config the
// dispatcher cannot build a client from is permanent; the row is dead
// and the endpoint failure is still recorded on the breaker.
func TestTLSConfigErrorDeadLetters(t *testing.T) {
	srv, hits := statusServer(t, http.StatusOK)
	st := newQueueStore()
	st.add("d1", "sub-tls", st.now())
	res := &mapResolver{subs: map[string]*SubscriptionDetail{"sub-tls": {
		SubscriptionID: "sub-tls", TargetURL: srv.URL,
		TLS: &SubscriptionTLSConfig{CABundlePEM: "not a pem bundle"},
	}}}
	d := NewWebhookDispatcher(st, res, DispatcherConfig{})
	sharedClock(d, st)
	d.poll(context.Background())
	r := st.row("d1")
	if r.Status != "dead" || hits.Load() != 0 {
		t.Fatalf("row %+v hits %d, want dead with no request", r, hits.Load())
	}
	if n := d.breaker.circuits["sub-tls"]; n == nil || n.consecutiveFailures != 1 {
		t.Fatalf("breaker failure not recorded for the TLS config error")
	}
}

// TestCircuitOpenDefersWithoutStarvingOrInflating (AC9): 15 rows answered
// 503, then one healthy row. The circuit opens; the rows it holds are
// deferred to the probe time without consuming an attempt; the healthy
// row is delivered. Before the fix a held row went back to 'pending'
// with no next_retry_at and attempt_count+1 on every poll.
func TestCircuitOpenDefersWithoutStarvingOrInflating(t *testing.T) {
	bad, _ := statusServer(t, http.StatusServiceUnavailable)
	good, goodHits := statusServer(t, http.StatusOK)
	st := newQueueStore()
	base := st.now().Add(-time.Hour)
	for i := 0; i < 15; i++ {
		st.add(fmt.Sprintf("bad-%02d", i), "sub-bad", base.Add(time.Duration(i)*time.Second))
	}
	st.add("good-1", "sub-good", base.Add(time.Minute))
	res := &mapResolver{subs: map[string]*SubscriptionDetail{
		"sub-bad":  {SubscriptionID: "sub-bad", TargetURL: bad.URL},
		"sub-good": {SubscriptionID: "sub-good", TargetURL: good.URL},
	}}
	// Concurrency 1 makes the order exact: ten attempts open the
	// circuit, the remaining five rows are held.
	d := NewWebhookDispatcher(st, res, DispatcherConfig{Concurrency: 1})
	sharedClock(d, st)
	for i := 0; i < 20; i++ {
		d.poll(context.Background())
	}
	if d.breaker.State("sub-bad") != CircuitOpen {
		t.Fatalf("breaker = %s, want open", d.breaker.State("sub-bad"))
	}
	if goodHits.Load() != 1 {
		t.Fatalf("healthy subscription starved while the circuit is open: %d deliveries", goodHits.Load())
	}
	held := 0
	for i := 0; i < 15; i++ {
		r := st.row(fmt.Sprintf("bad-%02d", i))
		if r.LastError != "circuit open" {
			continue
		}
		held++
		if r.Status != "pending" || r.AttemptCount != 0 {
			t.Fatalf("held row %s status %q attempt_count %d, want pending/0 (no attempt was made)", r.DeliveryID, r.Status, r.AttemptCount)
		}
		if r.NextRetryAt == nil || !r.NextRetryAt.After(st.now()) {
			t.Fatalf("held row %s next_retry_at=%v, want a time after the store clock", r.DeliveryID, r.NextRetryAt)
		}
	}
	if held != 5 {
		t.Fatalf("%d rows held by the open circuit, want 5", held)
	}
}

// TestCircuitHoldCapDeadLetters (AC9, amended by review round 2): a row
// held longer than MaxCircuitHold is dead-lettered without an HTTP
// attempt; a younger one is deferred. The hold is measured from the later
// of the row's created_at and the moment the circuit opened, so while the
// outage itself is younger than the cap the old row is deferred too — a
// backlog queued long before an outage is not dead-lettered the moment
// the outage begins (round 2: it used to be, from created_at alone).
func TestCircuitHoldCapDeadLetters(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cfg    time.Duration
		effect time.Duration
	}{
		{name: "default 24h", cfg: 0, effect: DefaultMaxCircuitHold},
		{name: "configured 1h", cfg: time.Hour, effect: time.Hour},
	} {
		for _, outage := range []struct {
			name    string
			age     time.Duration // how long the circuit has been open
			oldDead bool
		}{
			{name: "outage longer than the cap", age: tc.effect + time.Minute, oldDead: true},
			{name: "outage younger than the cap", age: tc.effect - time.Minute, oldDead: false},
			{name: "outage just began", age: 0, oldDead: false},
		} {
			t.Run(tc.name+"/"+outage.name, func(t *testing.T) {
				srv, hits := statusServer(t, http.StatusOK)
				st := newQueueStore()
				st.add("old", "sub-x", st.now().Add(-tc.effect-2*time.Minute))
				st.add("young", "sub-x", st.now().Add(-tc.effect+2*time.Minute))
				res := &mapResolver{subs: map[string]*SubscriptionDetail{"sub-x": {SubscriptionID: "sub-x", TargetURL: srv.URL}}}
				d := NewWebhookDispatcher(st, res, DispatcherConfig{MaxCircuitHold: tc.cfg})
				sharedClock(d, st)
				tripOpenSince(d.breaker, "sub-x", st.now().Add(-outage.age), st.now())
				d.poll(context.Background())
				if hits.Load() != 0 {
					t.Fatalf("held rows reached the endpoint (%d hits)", hits.Load())
				}
				old := st.row("old")
				wantReason := "circuit open: held longer than " + tc.effect.String()
				if outage.oldDead {
					if old.Status != "dead" || old.LastError != wantReason {
						t.Fatalf("old row = %q/%q, want dead/%q", old.Status, old.LastError, wantReason)
					}
					if logs := st.logsFor("old"); len(logs) != 1 || logs[0].Error != wantReason || logs[0].StatusCode != nil {
						t.Fatalf("old row's dead-letter not logged: %+v", logs)
					}
				} else if old.Status != "pending" || old.AttemptCount != 0 || old.LastError != circuitOpenReason || old.NextRetryAt == nil {
					t.Fatalf("old row = %+v, want deferred: the outage is younger than the cap", old)
				}
				young := st.row("young")
				if young.Status != "pending" || young.AttemptCount != 0 || young.NextRetryAt == nil {
					t.Fatalf("young row = %+v, want deferred", young)
				}
			})
		}
	}
}

// TestHeldBacklogDoesNotStarveOtherSubscriptions (AC9a, design check and
// review round 1): `subs` subscriptions each hold `rows` rows behind an
// open circuit; one new healthy-subscription row arrives per simulated
// second for 900 polls.
//
// Two mechanisms keep the healthy subscription served, and the controls
// show each one is needed:
//
//   - With the old global created_at claim and per-row deferral, every
//     held row becomes claimable at the shared probe instant and is
//     re-claimed oldest-first 5 per poll: above 5 x 60 = 300 held rows
//     the healthy subscription is never claimed again (design check).
//   - The fair claim alone fixes one held subscription but not
//     `concurrency` (5) or more: each held subscription's oldest row is
//     older than any healthy row, so five of them take every slot on
//     every poll while each still has a due row (review round 1: 5 x 61
//     rows starved the healthy subscription for all 900 polls).
//   - Holding a subscription's whole due backlog at once (HoldDueDeliveries)
//     leaves it nothing claimable until its next probe, so it costs about
//     two slots per probe cycle (the probe and the first hold after it).
//
// A healthy row therefore waits at most one poll while fewer than
// `concurrency` subscriptions are held, and at most ceil(2 x subs /
// concurrency) polls otherwise, however many rows they hold.
func TestHeldBacklogDoesNotStarveOtherSubscriptions(t *testing.T) {
	const concurrency, ticks = 5, 900
	run := func(t *testing.T, subs, rows int, fifo, noBulk bool) (delivered int64, maxLag int) {
		bad, _ := statusServer(t, http.StatusServiceUnavailable)
		good, goodHits := statusServer(t, http.StatusOK)
		st := newQueueStore()
		st.fifoClaim, st.noBulkHold = fifo, noBulk
		base := st.now().Add(-10 * time.Minute)
		res := &mapResolver{subs: map[string]*SubscriptionDetail{
			"sub-good": {SubscriptionID: "sub-good", TargetURL: good.URL},
		}}
		d := NewWebhookDispatcher(st, res, DispatcherConfig{Concurrency: concurrency})
		sharedClock(d, st)
		for j := 0; j < subs; j++ {
			sub := fmt.Sprintf("sub-bad-%02d", j)
			res.subs[sub] = &SubscriptionDetail{SubscriptionID: sub, TargetURL: bad.URL}
			tripOpen(d.breaker, sub, st.now())
			for i := 0; i < rows; i++ {
				st.add(fmt.Sprintf("bad-%02d-%05d", j, i), sub, base.Add(time.Duration(i*subs+j)*time.Millisecond))
			}
		}

		enqueuedAt := map[string]int{}
		for tick := 0; tick < ticks; tick++ {
			id := fmt.Sprintf("good-%04d", tick)
			st.add(id, "sub-good", st.now())
			enqueuedAt[id] = tick
			d.poll(context.Background())
			for gid, at := range enqueuedAt {
				if st.row(gid).Status == "delivered" {
					if lag := tick - at; lag > maxLag {
						maxLag = lag
					}
					delete(enqueuedAt, gid)
				} else if tick-at > maxLag {
					maxLag = tick - at
				}
			}
			st.advance(time.Second)
		}
		for j := 0; j < subs; j++ {
			for i := 0; i < rows; i++ {
				r := st.row(fmt.Sprintf("bad-%02d-%05d", j, i))
				if r.AttemptCount > MaxRetriesDomain {
					t.Fatalf("held row %s attempt_count=%d exceeds the retry budget", r.DeliveryID, r.AttemptCount)
				}
				if r.Status == "dead" && r.AttemptCount < MaxRetriesDomain {
					t.Fatalf("held row %s dead after %d attempts within MaxCircuitHold: %q", r.DeliveryID, r.AttemptCount, r.LastError)
				}
			}
		}
		return goodHits.Load(), maxLag
	}
	for _, tc := range []struct{ subs, rows int }{
		{1, 100}, {1, 400}, {1, 5000}, // design check: one held subscription
		{4, 100}, {5, 61}, {5, 80}, {6, 60}, {10, 40}, {10, 400}, // review round 1: concurrency or more
	} {
		t.Run(fmt.Sprintf("held=%dx%d", tc.subs, tc.rows), func(t *testing.T) {
			bound := 1
			if tc.subs >= concurrency {
				bound = (2*tc.subs + concurrency - 1) / concurrency
			}
			delivered, maxLag := run(t, tc.subs, tc.rows, false, false)
			t.Logf("%d held subscriptions x %d rows: %d/%d healthy rows delivered, max wait %d polls (bound %d)", tc.subs, tc.rows, delivered, ticks, maxLag, bound)
			if delivered < int64(ticks-bound) {
				t.Fatalf("%d held subscriptions x %d rows: only %d/%d healthy rows delivered", tc.subs, tc.rows, delivered, ticks)
			}
			if maxLag > bound {
				t.Fatalf("%d held subscriptions x %d rows: a healthy row waited %d polls (want <= %d)", tc.subs, tc.rows, maxLag, bound)
			}
		})
	}
	// Controls: the scenario discriminates. Each mechanism switched off
	// (the claim back to global created_at order, or the bulk hold back
	// to per-row deferral) starves the healthy subscription.
	t.Run("control: fifo claim, per-row deferral starves at 1x400", func(t *testing.T) {
		delivered, _ := run(t, 1, 400, true, true)
		t.Logf("fifo claim, per-row deferral, 1x400: %d/%d healthy rows delivered", delivered, ticks)
		if delivered >= ticks-1 {
			t.Fatalf("delivered %d/%d: the scenario no longer exercises the design-check starvation", delivered, ticks)
		}
	})
	t.Run("control: fair claim, per-row deferral starves at 5x61", func(t *testing.T) {
		delivered, _ := run(t, 5, 61, false, true)
		t.Logf("fair claim, per-row deferral, 5x61: %d/%d healthy rows delivered", delivered, ticks)
		if delivered >= ticks-1 {
			t.Fatalf("delivered %d/%d: the scenario no longer exercises the review-round-1 starvation", delivered, ticks)
		}
	})
}

// TestOrphanedRowsDeadLetter (AC10): rows whose subscription was deleted
// are dead-lettered once, not re-claimed ahead of every newer row.
func TestOrphanedRowsDeadLetter(t *testing.T) {
	good, goodHits := statusServer(t, http.StatusOK)
	st := newQueueStore()
	base := st.now().Add(-time.Hour)
	for i := 0; i < 5; i++ {
		st.add(fmt.Sprintf("orphan-%d", i), "sub-deleted", base.Add(time.Duration(i)*time.Second))
	}
	st.add("good-1", "sub-good", base.Add(time.Minute))
	res := &mapResolver{
		subs:  map[string]*SubscriptionDetail{"sub-good": {SubscriptionID: "sub-good", TargetURL: good.URL}},
		errs:  map[string]error{"sub-deleted": domain.NewError(domain.ErrNotFound, "subscription not found")},
		stick: map[string]bool{"sub-deleted": true},
	}
	d := NewWebhookDispatcher(st, res, DispatcherConfig{})
	sharedClock(d, st)
	for i := 0; i < 100; i++ {
		d.poll(context.Background())
	}
	if goodHits.Load() != 1 {
		t.Fatalf("healthy subscription: %d deliveries, want 1", goodHits.Load())
	}
	if n := res.calls.Load(); n > 6 {
		t.Fatalf("resolver called %d times; orphaned rows are still hot-looping", n)
	}
	for i := 0; i < 5; i++ {
		r := st.row(fmt.Sprintf("orphan-%d", i))
		if r.Status != "dead" || r.LastError != "subscription not found" {
			t.Fatalf("orphan %s = %q/%q, want dead/subscription not found", r.DeliveryID, r.Status, r.LastError)
		}
	}
}

// TestTransientResolverErrorBacksOffWithFixedText (AC10 + design check,
// amended by review round 1): a transient resolver failure rides the
// backoff with a fixed reason in the error columns, never the pgconn text
// (which names the database host, user and database); the full error
// reaches the operator log only. Past the attempt budget the row stays
// 'failed' (visible to a stuck-delivery view as attempt_count >= budget),
// is retried every resolveRetryInterval and is delivered as soon as the
// subscription can be read again — it is not dead-lettered for a
// resolver fault before MaxCircuitHold. Before INIT-023 such a row was
// retried on every poll and delivered once the fault cleared; the first
// cut of this task dead-lettered it after ~15 s, which on a replica with
// a missing or wrong decryption key turned every dispatch into a manual
// replay.
func TestTransientResolverErrorBacksOffWithFixedText(t *testing.T) {
	logs := captureLogs(t)
	infraErr := errors.New("failed to connect to `user=u database=d`: 10.9.8.7:5432 (db.internal): dial error: connection refused")
	srv, hits := statusServer(t, http.StatusOK)
	st := newQueueStore()
	st.add("d1", "sub-x", st.now())
	res := &mapResolver{
		subs:  map[string]*SubscriptionDetail{"sub-x": {SubscriptionID: "sub-x", TargetURL: srv.URL}},
		errs:  map[string]error{"sub-x": infraErr},
		stick: map[string]bool{"sub-x": true},
	}
	d := NewWebhookDispatcher(st, res, DispatcherConfig{})
	sharedClock(d, st)

	d.poll(context.Background())
	r := st.row("d1")
	if r.Status != "failed" || r.AttemptCount != 1 || r.NextRetryAt == nil || !r.NextRetryAt.After(st.now()) {
		t.Fatalf("after one transient failure: %+v, want failed/attempt 1 with a future retry", r)
	}
	if got := r.NextRetryAt.Sub(st.now()); got != nextRetryDelay(0) {
		t.Fatalf("first retry in %v, want the backoff schedule's %v", got, nextRetryDelay(0))
	}
	for i := 0; i < 2*MaxRetriesDomain+5; i++ {
		st.advance(time.Minute + time.Second) // past every retry interval
		d.poll(context.Background())
	}
	r = st.row("d1")
	if r.Status != "failed" || r.AttemptCount < MaxRetriesDomain {
		t.Fatalf("past the budget: status %q attempts %d, want failed with attempt_count >= %d (retried, not dead)", r.Status, r.AttemptCount, MaxRetriesDomain)
	}
	if r.NextRetryAt == nil || r.NextRetryAt.Sub(st.now()) != resolveRetryInterval {
		t.Fatalf("past the budget the retry is at %v, want now + %v", r.NextRetryAt, resolveRetryInterval)
	}
	texts := []string{r.LastError}
	for _, l := range st.logsFor("d1") {
		texts = append(texts, l.Error)
	}
	for _, s := range texts {
		if s != resolveUnavailableReason {
			t.Fatalf("error column %q, want exactly %q", s, resolveUnavailableReason)
		}
		for _, leak := range []string{"10.9.8.7", "user=", "database=", "db.internal"} {
			if strings.Contains(s, leak) {
				t.Fatalf("error column leaks %q: %q", leak, s)
			}
		}
	}
	if !strings.Contains(logs.String(), "10.9.8.7:5432") {
		t.Fatalf("the operator log lost the resolver error detail")
	}
	if hits.Load() != 0 {
		t.Fatalf("endpoint reached while the subscription was unreadable")
	}

	// The fault clears (a key is fixed, the database is back): the row is
	// delivered on its next retry with no operator action.
	res.mu.Lock()
	delete(res.errs, "sub-x")
	res.mu.Unlock()
	st.advance(resolveRetryInterval)
	d.poll(context.Background())
	if r := st.row("d1"); r.Status != "delivered" || hits.Load() != 1 {
		t.Fatalf("after the fault cleared: %+v, %d hits, want delivered", r, hits.Load())
	}
}

// TestUnreadableSubscriptionHoldsBacklog (review round 1): while a
// subscription cannot be read, only the row being tried consumes
// attempts; the rest of its due backlog is held to the same retry
// instant without attempts, so the subscription takes one claim slot
// per retry however large its backlog. A row older than MaxCircuitHold
// is dead-lettered with the hold reason.
func TestUnreadableSubscriptionHoldsBacklog(t *testing.T) {
	st := newQueueStore()
	base := st.now().Add(-time.Hour)
	for i := 0; i < 4; i++ {
		st.add(fmt.Sprintf("d%d", i), "sub-x", base.Add(time.Duration(i)*time.Second))
	}
	res := &mapResolver{
		subs:  map[string]*SubscriptionDetail{},
		errs:  map[string]error{"sub-x": errors.New("decrypt signing_secret: encrypted value found but cipher is not configured")},
		stick: map[string]bool{"sub-x": true},
	}
	d := NewWebhookDispatcher(st, res, DispatcherConfig{Concurrency: 1})
	sharedClock(d, st)
	d.poll(context.Background())
	tried := st.row("d0")
	if tried.Status != "failed" || tried.AttemptCount != 1 || tried.NextRetryAt == nil {
		t.Fatalf("tried row = %+v, want failed/attempt 1/scheduled", tried)
	}
	for i := 1; i < 4; i++ {
		r := st.row(fmt.Sprintf("d%d", i))
		if r.Status != "pending" || r.AttemptCount != 0 || r.NextRetryAt == nil || !r.NextRetryAt.Equal(*tried.NextRetryAt) || r.LastError != resolveUnavailableReason {
			t.Fatalf("backlog row %s = %+v, want pending/0 held to %v with the fixed reason", r.DeliveryID, r, *tried.NextRetryAt)
		}
	}
	// Ten more polls before the retry instant claim nothing.
	calls := res.calls.Load()
	for i := 0; i < 10; i++ {
		d.poll(context.Background())
	}
	if res.calls.Load() != calls {
		t.Fatalf("held backlog was claimed before its retry instant")
	}

	// Review round 2: rows queued longer ago than MaxCircuitHold are not
	// dead-lettered by the first failure — the hold starts when the
	// subscription became unreadable — but once it has been unreadable
	// for longer than the cap, the tried row and the aged rest of the
	// backlog go dead with the hold reason, and a younger row stays held.
	old := newQueueStore()
	old.add("o1", "sub-x", old.now().Add(-DefaultMaxCircuitHold-2*time.Minute))
	old.add("o2", "sub-x", old.now().Add(-DefaultMaxCircuitHold-time.Minute))
	d = NewWebhookDispatcher(old, res, DispatcherConfig{Concurrency: 1})
	sharedClock(d, old)
	d.poll(context.Background())
	if r := old.row("o1"); r.Status != "failed" || r.AttemptCount != 1 || r.NextRetryAt == nil {
		t.Fatalf("aged row after the first failure = %+v, want failed/attempt 1/scheduled (not dead)", r)
	}
	if r := old.row("o2"); r.Status != "pending" || r.AttemptCount != 0 || r.LastError != resolveUnavailableReason {
		t.Fatalf("aged backlog row after the first failure = %+v, want held (not dead)", r)
	}
	old.advance(DefaultMaxCircuitHold + time.Minute)
	old.add("y1", "sub-x", old.now().Add(-time.Minute))
	d.poll(context.Background())
	want := resolveUnavailableReason + ": held longer than " + DefaultMaxCircuitHold.String()
	for _, id := range []string{"o1", "o2"} {
		if r := old.row(id); r.Status != "dead" || r.LastError != want {
			t.Fatalf("%s after %v unreadable = %q/%q, want dead/%q", id, DefaultMaxCircuitHold+time.Minute, r.Status, r.LastError, want)
		}
	}
	if logs := old.logsFor("o2"); len(logs) != 1 || logs[0].Error != want {
		t.Fatalf("aged backlog row's dead-letter not logged: %+v", logs)
	}
	if r := old.row("y1"); r.Status != "pending" || r.AttemptCount != 0 || r.NextRetryAt == nil {
		t.Fatalf("young row = %+v, want held", r)
	}
}

// TestCircuitHoldExpiresUnclaimedBacklog (review round 1, amended by
// round 2): the hold cap reaches rows the dispatcher was never handed.
// With the circuit open for longer than MaxCircuitHold, one claimed row
// dead-letters itself if it is too old, holds the younger rest of the
// backlog without attempts, and dead-letters (with a delivery-log row
// each) the backlog rows older than MaxCircuitHold. With an outage that
// has just begun, nothing is expired however old the rows are.
func TestCircuitHoldExpiresUnclaimedBacklog(t *testing.T) {
	t.Run("outage just began", func(t *testing.T) {
		srv, _ := statusServer(t, http.StatusOK)
		st := newQueueStore()
		st.add("old-a", "sub-x", st.now().Add(-DefaultMaxCircuitHold-2*time.Minute))
		st.add("old-b", "sub-x", st.now().Add(-DefaultMaxCircuitHold-time.Minute))
		res := &mapResolver{subs: map[string]*SubscriptionDetail{"sub-x": {SubscriptionID: "sub-x", TargetURL: srv.URL}}}
		d := NewWebhookDispatcher(st, res, DispatcherConfig{Concurrency: 1})
		sharedClock(d, st)
		tripOpen(d.breaker, "sub-x", st.now())
		d.poll(context.Background())
		for _, id := range []string{"old-a", "old-b"} {
			if r := st.row(id); r.Status != "pending" || r.LastError != circuitOpenReason || len(st.logsFor(id)) != 0 {
				t.Fatalf("%s = %+v, want held without a dead-letter", id, r)
			}
		}
	})
	srv, hits := statusServer(t, http.StatusOK)
	st := newQueueStore()
	st.add("old-a", "sub-x", st.now().Add(-DefaultMaxCircuitHold-2*time.Minute))
	st.add("old-b", "sub-x", st.now().Add(-DefaultMaxCircuitHold-time.Minute))
	st.add("young-a", "sub-x", st.now().Add(-DefaultMaxCircuitHold+time.Minute))
	st.add("young-b", "sub-x", st.now().Add(-time.Minute))
	st.add("other", "sub-y", st.now().Add(-DefaultMaxCircuitHold-time.Hour))
	st.rows["other"].Status = "dead" // another subscription's row is never touched
	res := &mapResolver{subs: map[string]*SubscriptionDetail{"sub-x": {SubscriptionID: "sub-x", TargetURL: srv.URL}}}
	d := NewWebhookDispatcher(st, res, DispatcherConfig{Concurrency: 1})
	sharedClock(d, st)
	tripOpenSince(d.breaker, "sub-x", st.now().Add(-DefaultMaxCircuitHold-time.Hour), st.now())
	d.poll(context.Background())

	if hits.Load() != 0 {
		t.Fatalf("held rows reached the endpoint (%d hits)", hits.Load())
	}
	want := "circuit open: held longer than " + DefaultMaxCircuitHold.String()
	for _, id := range []string{"old-a", "old-b"} {
		r := st.row(id)
		if r.Status != "dead" || r.LastError != want {
			t.Fatalf("%s = %q/%q, want dead/%q", id, r.Status, r.LastError, want)
		}
		if logs := st.logsFor(id); len(logs) != 1 || logs[0].Error != want || logs[0].StatusCode != nil {
			t.Fatalf("%s: dead-letter log rows %+v, want one explaining it", id, logs)
		}
	}
	probeAt := st.now().Add(circuitRecoveryTimeout)
	for _, id := range []string{"young-a", "young-b"} {
		r := st.row(id)
		if r.Status != "pending" || r.AttemptCount != 0 || r.LastError != "circuit open" || r.NextRetryAt == nil || !r.NextRetryAt.Equal(probeAt) {
			t.Fatalf("%s = %+v, want pending/0/circuit open held to the probe time %v", id, r, probeAt)
		}
	}
	if r := st.row("other"); r.LastError != "" || r.NextRetryAt != nil {
		t.Fatalf("another subscription's row was touched: %+v", r)
	}
}

// TestHTTPOutcomeTextIsStable (AC20, I8): HTTP-status outcomes keep the
// exact text "HTTP <code>" in last_error and the delivery log. SMP's
// rotation requeue matches last_error == "HTTP 401".
func TestHTTPOutcomeTextIsStable(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		attempts   int
		wantStatus string
	}{
		{name: "401 non-retryable", status: http.StatusUnauthorized, wantStatus: "dead"},
		{name: "503 retryable", status: http.StatusServiceUnavailable, wantStatus: "failed"},
		{name: "503 budget exhausted", status: http.StatusServiceUnavailable, attempts: MaxRetriesDomain - 1, wantStatus: "dead"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := statusServer(t, tc.status)
			st := newQueueStore()
			st.add("d1", "sub-x", st.now())
			st.rows["d1"].AttemptCount = tc.attempts
			res := &mapResolver{subs: map[string]*SubscriptionDetail{"sub-x": {SubscriptionID: "sub-x", TargetURL: srv.URL}}}
			d := NewWebhookDispatcher(st, res, DispatcherConfig{})
			sharedClock(d, st)
			d.poll(context.Background())
			want := fmt.Sprintf("HTTP %d", tc.status)
			r := st.row("d1")
			if r.Status != tc.wantStatus || r.LastError != want {
				t.Fatalf("row = %q/%q, want %q/%q", r.Status, r.LastError, tc.wantStatus, want)
			}
			if tc.wantStatus == "failed" && (r.NextRetryAt == nil || !r.NextRetryAt.After(st.now())) {
				t.Fatalf("retryable failure not scheduled: %v", r.NextRetryAt)
			}
			logs := st.logsFor("d1")
			if len(logs) != 1 || logs[0].Error != want || logs[0].StatusCode == nil || *logs[0].StatusCode != tc.status {
				t.Fatalf("delivery log = %+v, want one row with error %q", logs, want)
			}
		})
	}
}

// Compile-time guard: the regression fake satisfies the real interface,
// including DeferDelivery.
var _ store.DeliveryStore = (*queueStore)(nil)
