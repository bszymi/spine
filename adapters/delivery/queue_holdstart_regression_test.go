package delivery

// INIT-023 TASK-010 review round 2, finding 2: MaxCircuitHold is measured
// from the later of a row's created_at and the start of its
// subscription's current hold (the circuit opening, or the first of the
// current run of resolver failures), not from created_at alone. Measured
// from created_at, a row queued longer ago than the cap — after a
// dispatcher outage, or in an SMP workspace re-warmed after a day — was
// dead-lettered by the first fault it met, without one retry, and the
// bulk hold dead-lettered the rest of the aged backlog with it.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/store"
)

func countStatus(st *queueStore, prefix, status string) int {
	st.mu.Lock()
	defer st.mu.Unlock()
	n := 0
	for id, r := range st.rows {
		if strings.HasPrefix(id, prefix) && r.Status == status {
			n++
		}
	}
	return n
}

// TestAgedBacklogSurvivesTransientFaults: twenty rows queued 25 hours ago
// (older than the default cap) meet a fault that has just begun. None is
// dead-lettered; all are delivered once the fault clears.
func TestAgedBacklogSurvivesTransientFaults(t *testing.T) {
	addAged := func(st *queueStore, prefix string) {
		for i := 0; i < 20; i++ {
			st.add(fmt.Sprintf("%s%02d", prefix, i), "sub-x", st.now().Add(-25*time.Hour+time.Duration(i)*time.Second))
		}
	}
	// The reviewer's reproduction, inverted: one resolver failure (a
	// decrypt error on a replica that has just restarted, say), then the
	// subscription reads fine. Before round 2: 20 dead, 0 delivered,
	// 1 resolver call.
	t.Run("one resolver failure", func(t *testing.T) {
		srv, hits := statusServer(t, http.StatusOK)
		st := newQueueStore()
		addAged(st, "d")
		res := &mapResolver{
			subs:  map[string]*SubscriptionDetail{"sub-x": {SubscriptionID: "sub-x", TargetURL: srv.URL}},
			errs:  map[string]error{"sub-x": errors.New("decrypt signing_secret: cipher: message authentication failed")},
			stick: map[string]bool{},
		}
		d := NewWebhookDispatcher(st, res, DispatcherConfig{Concurrency: 1})
		sharedClock(d, st)
		for i := 0; i < 30; i++ {
			d.poll(context.Background())
			st.advance(2 * time.Second)
		}
		if dead, delivered := countStatus(st, "d", "dead"), countStatus(st, "d", "delivered"); dead != 0 || delivered != 20 || hits.Load() != 20 {
			t.Fatalf("after one resolver failure: %d dead, %d delivered, %d POSTs; want 0/20/20", dead, delivered, hits.Load())
		}
	})
	// A run of failures that ended long ago does not count: the start of
	// the hold is the first failure of the current run.
	t.Run("a resolver failure a day after an earlier one cleared", func(t *testing.T) {
		srv, hits := statusServer(t, http.StatusOK)
		st := newQueueStore()
		st.add("first", "sub-x", st.now())
		res := &mapResolver{
			subs:  map[string]*SubscriptionDetail{"sub-x": {SubscriptionID: "sub-x", TargetURL: srv.URL}},
			errs:  map[string]error{"sub-x": errors.New("decrypt signing_secret: cipher: message authentication failed")},
			stick: map[string]bool{},
		}
		d := NewWebhookDispatcher(st, res, DispatcherConfig{Concurrency: 1})
		sharedClock(d, st)
		for i := 0; i < 5; i++ { // fails once, then delivers
			d.poll(context.Background())
			st.advance(2 * time.Second)
		}
		if r := st.row("first"); r.Status != "delivered" {
			t.Fatalf("first row = %+v, want delivered", r)
		}
		st.advance(25 * time.Hour)
		addAged(st, "d")
		res.mu.Lock()
		res.errs["sub-x"] = errors.New("decrypt signing_secret: cipher: message authentication failed")
		res.mu.Unlock()
		for i := 0; i < 30; i++ {
			d.poll(context.Background())
			st.advance(2 * time.Second)
		}
		if dead, delivered := countStatus(st, "d", "dead"), countStatus(st, "d", "delivered"); dead != 0 || delivered != 20 || hits.Load() != 21 {
			t.Fatalf("second fault: %d dead, %d delivered, %d POSTs; want 0/20/21", dead, delivered, hits.Load())
		}
	})
	// The circuit equivalent: an outage that begins now holds the aged
	// backlog instead of expiring it, and the backlog is delivered once
	// the probe succeeds.
	t.Run("circuit opened just now", func(t *testing.T) {
		srv, hits := statusServer(t, http.StatusOK)
		st := newQueueStore()
		addAged(st, "d")
		res := &mapResolver{subs: map[string]*SubscriptionDetail{"sub-x": {SubscriptionID: "sub-x", TargetURL: srv.URL}}}
		d := NewWebhookDispatcher(st, res, DispatcherConfig{})
		sharedClock(d, st)
		tripOpen(d.breaker, "sub-x", st.now())
		d.poll(context.Background())
		if dead, held := countStatus(st, "d", "dead"), countStatus(st, "d", "pending"); dead != 0 || held != 20 || hits.Load() != 0 {
			t.Fatalf("while the circuit is open: %d dead, %d held, %d POSTs; want 0/20/0", dead, held, hits.Load())
		}
		st.advance(circuitRecoveryTimeout)
		for i := 0; i < 10; i++ {
			d.poll(context.Background())
			st.advance(time.Second)
		}
		if dead, delivered := countStatus(st, "d", "dead"), countStatus(st, "d", "delivered"); dead != 0 || delivered != 20 {
			t.Fatalf("after the probe succeeded: %d dead, %d delivered; want 0/20", dead, delivered)
		}
	})
}

// TestCircuitHoldCapCountsFromOutageStart drives a real outage: the
// endpoint answers 503, the circuit opens through ten real failures, and
// every probe fails, re-opening it. The cap (10 minutes here) is reached
// only once the outage itself is older than the cap — failed probes do
// not restart it — and then the held rows, all queued an hour before the
// outage, go dead with the hold reason.
func TestCircuitHoldCapCountsFromOutageStart(t *testing.T) {
	const capHold = 10 * time.Minute
	bad, _ := statusServer(t, http.StatusServiceUnavailable)
	st := newQueueStore()
	for i := 0; i < 40; i++ {
		st.add(fmt.Sprintf("d%02d", i), "sub-x", st.now().Add(-time.Hour+time.Duration(i)*time.Second))
	}
	res := &mapResolver{subs: map[string]*SubscriptionDetail{"sub-x": {SubscriptionID: "sub-x", TargetURL: bad.URL}}}
	d := NewWebhookDispatcher(st, res, DispatcherConfig{MaxCircuitHold: capHold})
	sharedClock(d, st)
	for i := 0; i < 2; i++ { // 5 + 5 real failures at one instant open the circuit
		d.poll(context.Background())
	}
	opened := d.breaker.OpenedAt("sub-x")
	if d.breaker.State("sub-x") != CircuitOpen || !opened.Equal(st.now()) {
		t.Fatalf("circuit %s opened at %v, want open at %v", d.breaker.State("sub-x"), opened, st.now())
	}
	holdReason := heldTooLongReason(circuitOpenReason, capHold)
	probesFailed := 0
	for st.now().Sub(opened) <= capHold+2*circuitRecoveryTimeout {
		st.advance(10 * time.Second)
		before := d.breaker.circuits["sub-x"].lastFailureAt
		d.poll(context.Background())
		if !d.breaker.circuits["sub-x"].lastFailureAt.Equal(before) {
			probesFailed++
		}
		if !d.breaker.OpenedAt("sub-x").Equal(opened) {
			t.Fatalf("a failed probe moved the outage start to %v (want %v)", d.breaker.OpenedAt("sub-x"), opened)
		}
		if n := countLastError(st, holdReason); n > 0 && st.now().Sub(opened) <= capHold {
			t.Fatalf("%d rows dead-lettered for the hold %v into the outage (cap %v)", n, st.now().Sub(opened), capHold)
		}
	}
	if probesFailed < int(capHold/circuitRecoveryTimeout) {
		t.Fatalf("only %d failed probes over the outage; the scenario no longer cycles the circuit", probesFailed)
	}
	if n := countLastError(st, holdReason); n == 0 {
		t.Fatalf("no row dead-lettered for the hold once the outage outlasted the cap")
	}
	for i := 0; i < 40; i++ {
		r := st.row(fmt.Sprintf("d%02d", i))
		if r.Status != "dead" || (r.LastError != holdReason && r.LastError != "HTTP 503") {
			t.Fatalf("%s = %+v, want dead (hold cap or exhausted budget) once the outage outlasted the cap", r.DeliveryID, r)
		}
	}
}

func countLastError(st *queueStore, lastError string) int {
	st.mu.Lock()
	defer st.mu.Unlock()
	n := 0
	for _, r := range st.rows {
		if r.Status == "dead" && r.LastError == lastError {
			n++
		}
	}
	return n
}

// TestHoldStartedAt: the hold starts at the later of created_at and the
// subscription's hold start; an unknown (zero) hold start counts as now,
// so a missing start can delay a dead-letter but never cause one; a row
// with no created_at is never aged out.
func TestHoldStartedAt(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	d := &WebhookDispatcher{now: func() time.Time { return now }, maxCircuitHold: time.Hour}
	old, recent := now.Add(-2*time.Hour), now.Add(-time.Minute)
	for _, tc := range []struct {
		name           string
		created, since time.Time
		want           time.Time
		tooLong        bool
	}{
		{name: "old row, old hold", created: old, since: old.Add(time.Minute), want: old.Add(time.Minute), tooLong: true},
		{name: "old row, recent hold", created: old, since: recent, want: recent},
		{name: "recent row, old hold", created: recent, since: old, want: recent},
		{name: "old row, unknown hold start", created: old, since: time.Time{}, want: now},
		{name: "no created_at", created: time.Time{}, since: old, want: old},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := store.DeliveryEntry{CreatedAt: tc.created}
			if got := d.holdStartedAt(e, tc.since); !got.Equal(tc.want) {
				t.Fatalf("holdStartedAt = %v, want %v", got, tc.want)
			}
			if got := d.heldTooLong(e, tc.since, d.holdCap()); got != tc.tooLong {
				t.Fatalf("heldTooLong = %v, want %v", got, tc.tooLong)
			}
		})
	}
}
