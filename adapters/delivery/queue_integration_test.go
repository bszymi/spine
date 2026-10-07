//go:build integration

package delivery

// INIT-023 TASK-010 AC11/AC12: the queue fixes against real Postgres —
// the real PostgresStore SQL (fair ClaimDeliveries, DeferDelivery,
// retention), the real StoreSubscriptionResolver and the real
// WebhookDispatcher. Ported from the audit's real-Postgres queue
// starvation proof of concept with its assertions inverted.
//
// Gated by SPINE_TEST_DATABASE_URL like every integration test. Each
// scenario empties runtime.event_delivery_queue / event_delivery_log
// first (the claim is queue-wide, so a stray claimable row would change
// what is claimed); no other package's tests use those tables.
// Subscriptions get unique ids and are removed on cleanup.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/store"
)

func queuePG(t *testing.T) (*store.PostgresStore, string) {
	t.Helper()
	s := store.NewTestStore(t)
	ctx := context.Background()
	for _, q := range []string{`DELETE FROM runtime.event_delivery_log`, `DELETE FROM runtime.event_delivery_queue`} {
		if err := s.ExecRaw(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	prefix := "t010-" + hex.EncodeToString(b) + "-"
	t.Cleanup(func() {
		ctx := context.Background()
		_ = s.ExecRaw(ctx, `DELETE FROM runtime.event_delivery_log WHERE subscription_id LIKE $1`, prefix+"%")
		_ = s.ExecRaw(ctx, `DELETE FROM runtime.event_delivery_queue WHERE subscription_id LIKE $1`, prefix+"%")
		_ = s.ExecRaw(ctx, `DELETE FROM runtime.event_subscriptions WHERE subscription_id LIKE $1`, prefix+"%")
	})
	return s, prefix
}

func queuePGSub(t *testing.T, s *store.PostgresStore, id, url string) {
	t.Helper()
	now := time.Now().UTC()
	if err := s.CreateSubscription(context.Background(), &store.EventSubscription{SubscriptionID: id, Name: id,
		TargetType: "webhook", TargetURL: url, EventTypes: []string{}, SigningSecret: "s", Status: "active",
		Metadata: []byte(`{}`), CreatedBy: "admin", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
}

func queuePGEnqueue(t *testing.T, s *store.PostgresStore, sub, evt string, at time.Time) string {
	t.Helper()
	id := sub + "-" + evt
	if err := s.EnqueueDelivery(context.Background(), &store.DeliveryEntry{DeliveryID: id, SubscriptionID: sub,
		EventID: evt, EventType: "step_assigned", Payload: []byte(fmt.Sprintf(`{"event_id":%q}`, evt)),
		Status: "pending", CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
	return id
}

func queuePGCount(t *testing.T, s *store.PostgresStore, where string, args ...any) int {
	t.Helper()
	var n int
	if err := s.RawPool().QueryRow(context.Background(), `SELECT count(*) FROM runtime.event_delivery_queue WHERE `+where, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func queuePGServers(t *testing.T, badStatus int) (*httptest.Server, *httptest.Server, *int64, *int64) {
	t.Helper()
	bh, gh := new(int64), new(int64)
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(bh, 1)
		w.WriteHeader(badStatus)
	}))
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(gh, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(bad.Close)
	t.Cleanup(good.Close)
	return bad, good, bh, gh
}

// Scenario 1: five permanent-4xx partner rows ahead of five smp-internal
// rows. Each partner row gets one attempt and is dead; smp-internal gets
// all five; retention then reaps all ten.
func TestQueuePG_Permanent4xxDeadLettersAndIsReaped(t *testing.T) {
	ctx := context.Background()
	s, p := queuePG(t)
	bad, good, bh, gh := queuePGServers(t, http.StatusGone)
	partner, internal := p+"partner", p+"smp-internal"
	queuePGSub(t, s, partner, bad.URL)
	queuePGSub(t, s, internal, good.URL)
	base := time.Now().Add(-time.Minute)
	for i := 0; i < 5; i++ {
		queuePGEnqueue(t, s, partner, fmt.Sprintf("e%d", i), base.Add(time.Duration(i)*time.Millisecond))
	}
	for i := 5; i < 10; i++ {
		queuePGEnqueue(t, s, internal, fmt.Sprintf("e%d", i), base.Add(time.Duration(i)*time.Second))
	}
	d := NewWebhookDispatcher(s, NewStoreSubscriptionResolver(s), DispatcherConfig{})
	for i := 0; i < 60; i++ {
		d.poll(ctx)
	}
	if got := atomic.LoadInt64(gh); got != 5 {
		t.Fatalf("smp-internal POSTs=%d, want 5", got)
	}
	if got := atomic.LoadInt64(bh); got != 5 {
		t.Fatalf("partner POSTs=%d, want 5 (one attempt per row)", got)
	}
	if n := queuePGCount(t, s, `subscription_id = $1 AND status = 'dead' AND attempt_count = 1 AND last_error = 'HTTP 410'`, partner); n != 5 {
		t.Fatalf("partner dead rows=%d, want 5", n)
	}
	if n, err := s.DeleteExpiredDeliveries(ctx, time.Now().Add(time.Hour)); err != nil || n != 10 {
		t.Fatalf("retention deleted %d (err %v), want 10 (5 dead + 5 delivered)", n, err)
	}
}

// Scenario 2: the partner's circuit opens on 503s; the open circuit then
// handles partner rows, which are deferred without consuming attempts
// and are never immediately re-claimable; new smp-internal rows are all
// delivered meanwhile.
func TestQueuePG_CircuitOpenDoesNotStarve(t *testing.T) {
	ctx := context.Background()
	s, p := queuePG(t)
	bad, good, bh, gh := queuePGServers(t, http.StatusServiceUnavailable)
	partner, internal := p+"partner", p+"smp-internal"
	queuePGSub(t, s, partner, bad.URL)
	queuePGSub(t, s, internal, good.URL)
	base := time.Now().Add(-time.Minute)
	for i := 0; i < 10; i++ {
		queuePGEnqueue(t, s, partner, fmt.Sprintf("p%d", i), base.Add(time.Duration(i)*time.Millisecond))
	}
	d := NewWebhookDispatcher(s, NewStoreSubscriptionResolver(s), DispatcherConfig{})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && d.breaker.State(partner) != CircuitOpen {
		d.poll(ctx)
		time.Sleep(100 * time.Millisecond)
	}
	if d.breaker.State(partner) != CircuitOpen {
		t.Fatalf("circuit never opened")
	}
	// Let the partner rows' backoff expire so the open circuit handles them.
	held := time.Now().Add(10 * time.Second)
	for queuePGCount(t, s, `subscription_id = $1 AND last_error = 'circuit open'`, partner) < 5 {
		if time.Now().After(held) {
			t.Fatalf("open circuit never handled 5 partner rows")
		}
		d.poll(ctx)
		time.Sleep(100 * time.Millisecond)
	}
	for i := 0; i < 5; i++ {
		queuePGEnqueue(t, s, internal, fmt.Sprintf("g%d", i), time.Now())
	}
	end := time.Now().Add(3 * time.Second)
	for time.Now().Before(end) && atomic.LoadInt64(gh) < 5 {
		d.poll(ctx)
		time.Sleep(100 * time.Millisecond)
	}
	if got := atomic.LoadInt64(gh); got != 5 {
		t.Fatalf("smp-internal POSTs=%d while the partner circuit is open, want 5", got)
	}
	var maxAtt int
	if err := s.RawPool().QueryRow(ctx, `SELECT max(attempt_count) FROM runtime.event_delivery_queue WHERE subscription_id = $1`, partner).Scan(&maxAtt); err != nil {
		t.Fatal(err)
	}
	if maxAtt > MaxRetriesDomain {
		t.Fatalf("partner attempt_count=%d exceeds the retry budget %d (inflated by holds)", maxAtt, MaxRetriesDomain)
	}
	// Holds consume no attempts: every attempt_count increment is a real
	// POST (the circuit has not reached its probe time in this test).
	var sumAtt int64
	if err := s.RawPool().QueryRow(ctx, `SELECT coalesce(sum(attempt_count), 0) FROM runtime.event_delivery_queue WHERE subscription_id = $1`, partner).Scan(&sumAtt); err != nil {
		t.Fatal(err)
	}
	if posts := atomic.LoadInt64(bh); sumAtt != posts {
		t.Fatalf("partner attempt_count total %d != %d real POSTs: holds consumed attempts", sumAtt, posts)
	}
	if n := queuePGCount(t, s, `subscription_id = $1 AND status IN ('pending','failed') AND (next_retry_at IS NULL OR next_retry_at <= now())`, partner); n != 0 {
		t.Fatalf("%d partner rows are immediately re-claimable while the circuit is open", n)
	}
}

// Scenario 3: rows of a deleted subscription are dead-lettered once;
// smp-internal gets all of its deliveries.
func TestQueuePG_OrphanedRowsDeadLetter(t *testing.T) {
	ctx := context.Background()
	s, p := queuePG(t)
	_, good, _, gh := queuePGServers(t, http.StatusOK)
	internal, deleted := p+"smp-internal", p+"deleted-sub"
	queuePGSub(t, s, internal, good.URL)
	base := time.Now().Add(-time.Minute)
	for i := 0; i < 5; i++ {
		queuePGEnqueue(t, s, deleted, fmt.Sprintf("x%d", i), base.Add(time.Duration(i)*time.Millisecond))
	}
	for i := 0; i < 5; i++ {
		queuePGEnqueue(t, s, internal, fmt.Sprintf("y%d", i), base.Add(time.Second))
	}
	d := NewWebhookDispatcher(s, NewStoreSubscriptionResolver(s), DispatcherConfig{})
	for i := 0; i < 20; i++ {
		d.poll(ctx)
	}
	if got := atomic.LoadInt64(gh); got != 5 {
		t.Fatalf("smp-internal POSTs=%d, want 5", got)
	}
	if n := queuePGCount(t, s, `subscription_id = $1 AND status = 'dead' AND last_error = 'subscription not found'`, deleted); n != 5 {
		t.Fatalf("orphaned dead rows=%d, want 5", n)
	}
}

// Scenario 4: the fair claim. 400 claimable rows of subscription A, all
// older than one row of B, plus one A row deferred an hour ahead.
func TestQueuePG_FairClaim(t *testing.T) {
	ctx := context.Background()
	s, p := queuePG(t)
	a, b := p+"sub-a", p+"sub-b"
	base := time.Now().Add(-10 * time.Minute)
	for i := 0; i < 400; i++ {
		queuePGEnqueue(t, s, a, fmt.Sprintf("a%04d", i), base.Add(time.Duration(i)*time.Millisecond))
	}
	later := queuePGEnqueue(t, s, a, "a-later", base.Add(-time.Hour))
	if err := s.ExecRaw(ctx, `UPDATE runtime.event_delivery_queue SET next_retry_at = now() + interval '1 hour' WHERE delivery_id = $1`, later); err != nil {
		t.Fatal(err)
	}
	bRow := queuePGEnqueue(t, s, b, "b0", time.Now())

	claimed, err := s.ClaimDeliveries(ctx, 5)
	if err != nil {
		t.Fatal(err)
	}
	gotB := false
	for i := range claimed {
		e := &claimed[i]
		if e.DeliveryID == bRow {
			gotB = true
		}
		if e.DeliveryID == later {
			t.Fatalf("claimed a row deferred into the future")
		}
		if e.Status != "delivering" {
			t.Fatalf("claimed row %s returned with status %q", e.DeliveryID, e.Status)
		}
	}
	if len(claimed) != 5 || !gotB {
		t.Fatalf("first claim of 5 = %v, want it to include B's newer row", ids(claimed))
	}
	// A's rows in the claim are A's oldest (RETURNING order is not
	// claim order, so compare as a set).
	var aIDs []string
	for i := range claimed {
		if claimed[i].SubscriptionID == a {
			aIDs = append(aIDs, claimed[i].DeliveryID)
		}
	}
	sort.Strings(aIDs)
	for i, id := range aIDs {
		if want := fmt.Sprintf("%s-a%04d", a, i); id != want {
			t.Fatalf("A's claimed rows = %v, want A's %d oldest", aIDs, len(aIDs))
		}
	}

	// With only A's rows claimable, the claim is plain created_at order.
	if err := s.ExecRaw(ctx, `DELETE FROM runtime.event_delivery_queue WHERE subscription_id = $1`, b); err != nil {
		t.Fatal(err)
	}
	if err := s.ExecRaw(ctx, `UPDATE runtime.event_delivery_queue SET status = 'pending' WHERE status = 'delivering'`); err != nil {
		t.Fatal(err)
	}
	claimed, err = s.ClaimDeliveries(ctx, 5)
	if err != nil {
		t.Fatal(err)
	}
	got := ids(claimed)
	sort.Strings(got)
	if len(got) != 5 {
		t.Fatalf("single-subscription claim returned %d rows", len(got))
	}
	for i, id := range got {
		if want := fmt.Sprintf("%s-a%04d", a, i); id != want {
			t.Fatalf("single-subscription claim = %v, want the 5 oldest (created_at order)", got)
		}
	}
}

// AC12: DeferDelivery re-schedules a 'delivering' row without consuming
// an attempt and leaves any other row untouched.
func TestQueuePG_DeferDelivery(t *testing.T) {
	ctx := context.Background()
	s, p := queuePG(t)
	sub := p + "defer"
	id := queuePGEnqueue(t, s, sub, "d1", time.Now().Add(-time.Minute))
	if err := s.ExecRaw(ctx, `UPDATE runtime.event_delivery_queue SET status = 'delivering', attempt_count = 2 WHERE delivery_id = $1`, id); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(42 * time.Second).UTC().Truncate(time.Microsecond)
	if err := s.DeferDelivery(ctx, id, "circuit open", until); err != nil {
		t.Fatalf("DeferDelivery: %v", err)
	}
	got, err := s.GetDelivery(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "pending" || got.AttemptCount != 2 || got.LastError != "circuit open" ||
		got.NextRetryAt == nil || !got.NextRetryAt.Equal(until) {
		t.Fatalf("after DeferDelivery: %+v, want pending / attempts 2 / circuit open / %v", got, until)
	}

	// A row no longer 'delivering' (replayed, redriven, already
	// resolved) is left exactly as it is.
	for _, status := range []string{"pending", "failed", "dead", "delivered"} {
		rid := queuePGEnqueue(t, s, sub, "s-"+status, time.Now())
		if err := s.ExecRaw(ctx, `UPDATE runtime.event_delivery_queue SET status = $2, attempt_count = 3, last_error = 'before' WHERE delivery_id = $1`, rid, status); err != nil {
			t.Fatal(err)
		}
		if err := s.DeferDelivery(ctx, rid, "circuit open", until); err != nil {
			t.Fatal(err)
		}
		row, err := s.GetDelivery(ctx, rid)
		if err != nil {
			t.Fatal(err)
		}
		if row.Status != status || row.AttemptCount != 3 || row.LastError != "before" || row.NextRetryAt != nil {
			t.Fatalf("DeferDelivery touched a %q row: %+v", status, row)
		}
	}
}

func ids(es []store.DeliveryEntry) []string {
	out := make([]string, len(es))
	for i := range es {
		out[i] = es[i].DeliveryID
	}
	return out
}
