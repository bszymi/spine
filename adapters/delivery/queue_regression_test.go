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

// TestCircuitHoldCapDeadLetters (AC9): a held row older than
// MaxCircuitHold (from created_at) is dead-lettered without an HTTP
// attempt; a younger one is deferred.
func TestCircuitHoldCapDeadLetters(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cfg    time.Duration
		effect time.Duration
	}{
		{name: "default 24h", cfg: 0, effect: DefaultMaxCircuitHold},
		{name: "configured 1h", cfg: time.Hour, effect: time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, hits := statusServer(t, http.StatusOK)
			st := newQueueStore()
			st.add("old", "sub-x", st.now().Add(-tc.effect-time.Minute))
			st.add("young", "sub-x", st.now().Add(-tc.effect+time.Minute))
			res := &mapResolver{subs: map[string]*SubscriptionDetail{"sub-x": {SubscriptionID: "sub-x", TargetURL: srv.URL}}}
			d := NewWebhookDispatcher(st, res, DispatcherConfig{MaxCircuitHold: tc.cfg})
			sharedClock(d, st)
			tripOpen(d.breaker, "sub-x", st.now())
			d.poll(context.Background())
			if hits.Load() != 0 {
				t.Fatalf("held rows reached the endpoint (%d hits)", hits.Load())
			}
			old := st.row("old")
			wantReason := "circuit open: held longer than " + tc.effect.String()
			if old.Status != "dead" || old.LastError != wantReason {
				t.Fatalf("old row = %q/%q, want dead/%q", old.Status, old.LastError, wantReason)
			}
			if logs := st.logsFor("old"); len(logs) != 1 || logs[0].Error != wantReason || logs[0].StatusCode != nil {
				t.Fatalf("old row's dead-letter not logged: %+v", logs)
			}
			young := st.row("young")
			if young.Status != "pending" || young.AttemptCount != 0 || young.NextRetryAt == nil {
				t.Fatalf("young row = %+v, want deferred", young)
			}
		})
	}
}

// TestHeldBacklogDoesNotStarveOtherSubscriptions (AC9a, design check):
// N rows of one subscription are held behind an open circuit; one new
// healthy-subscription row arrives per simulated second for 900 polls.
// With the old global created_at claim, all held rows become claimable
// at the same probe instant and are re-claimed oldest-first 5 per poll:
// above 5 x 60 = 300 held rows the healthy subscription is never claimed
// again (900/900 polls at N = 400). The fair claim keeps every healthy
// row within one poll. The fifo-claim control shows the scenario does
// discriminate: it starves on the old ordering.
func TestHeldBacklogDoesNotStarveOtherSubscriptions(t *testing.T) {
	run := func(t *testing.T, n int, fifo bool) (delivered int64, maxLag int) {
		bad, _ := statusServer(t, http.StatusServiceUnavailable)
		good, goodHits := statusServer(t, http.StatusOK)
		st := newQueueStore()
		st.fifoClaim = fifo
		base := st.now().Add(-10 * time.Minute)
		for i := 0; i < n; i++ {
			st.add(fmt.Sprintf("bad-%05d", i), "sub-bad", base.Add(time.Duration(i)*time.Millisecond))
		}
		res := &mapResolver{subs: map[string]*SubscriptionDetail{
			"sub-bad":  {SubscriptionID: "sub-bad", TargetURL: bad.URL},
			"sub-good": {SubscriptionID: "sub-good", TargetURL: good.URL},
		}}
		d := NewWebhookDispatcher(st, res, DispatcherConfig{})
		sharedClock(d, st)
		tripOpen(d.breaker, "sub-bad", st.now())

		enqueuedAt := map[string]int{}
		for tick := 0; tick < 900; tick++ {
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
		for i := 0; i < n; i++ {
			if r := st.row(fmt.Sprintf("bad-%05d", i)); r.AttemptCount > MaxRetriesDomain {
				t.Fatalf("held row %s attempt_count=%d exceeds the retry budget", r.DeliveryID, r.AttemptCount)
			}
		}
		return goodHits.Load(), maxLag
	}
	for _, n := range []int{100, 400, 5000} {
		t.Run(fmt.Sprintf("held=%d", n), func(t *testing.T) {
			delivered, maxLag := run(t, n, false)
			if delivered < 899 {
				t.Fatalf("held=%d: only %d/900 healthy rows delivered", n, delivered)
			}
			if maxLag > 1 {
				t.Fatalf("held=%d: a healthy row waited %d polls behind the held backlog (want <= 1)", n, maxLag)
			}
		})
	}
	t.Run("control: fifo claim starves at held=400", func(t *testing.T) {
		delivered, _ := run(t, 400, true)
		if delivered >= 899 {
			t.Fatalf("fifo claim delivered %d/900: the scenario no longer exercises the starvation", delivered)
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

// TestTransientResolverErrorBacksOffWithFixedText (AC10 + design check):
// a transient resolver failure rides the backoff and is dead at the
// budget; the error columns carry a fixed reason, never the pgconn text
// (which names the database host, user and database). The full error
// reaches the operator log only.
func TestTransientResolverErrorBacksOffWithFixedText(t *testing.T) {
	logs := captureLogs(t)
	infraErr := errors.New("failed to connect to `user=u database=d`: 10.9.8.7:5432 (db.internal): dial error: connection refused")
	st := newQueueStore()
	st.add("d1", "sub-x", st.now())
	res := &mapResolver{
		subs:  map[string]*SubscriptionDetail{},
		errs:  map[string]error{"sub-x": infraErr},
		stick: map[string]bool{"sub-x": true},
	}
	d := NewWebhookDispatcher(st, res, DispatcherConfig{})
	sharedClock(d, st)

	d.poll(context.Background())
	r := st.row("d1")
	if r.Status != "failed" || r.NextRetryAt == nil || !r.NextRetryAt.After(st.now()) {
		t.Fatalf("after one transient failure: %+v, want failed with a future retry", r)
	}
	for i := 0; i < 2*MaxRetriesDomain+5; i++ {
		st.advance(20 * time.Second) // past every backoff step
		d.poll(context.Background())
	}
	r = st.row("d1")
	if r.Status != "dead" || r.AttemptCount != MaxRetriesDomain {
		t.Fatalf("after the budget: status %q attempts %d, want dead/%d", r.Status, r.AttemptCount, MaxRetriesDomain)
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
