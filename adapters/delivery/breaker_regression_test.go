package delivery

// INIT-023 TASK-010 regression tests for audit P3-12 (a half-open probe
// that exits early wedges the subscription), ported from the audit's
// delivery-scheduler proof of concept with its assertion inverted.

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// TestHalfOpenProbeEarlyReturnDoesNotWedge: the circuit is open past its
// recovery time and the resolver fails once during the probe. The probe
// must be released, so a later poll delivers the row and closes the
// circuit. Before the fix: 30 polls, 0 endpoint hits, breaker half-open.
func TestHalfOpenProbeEarlyReturnDoesNotWedge(t *testing.T) {
	srv, hits := statusServer(t, http.StatusOK)
	st := newQueueStore()
	st.add("d1", "sub-x", st.now().Add(-time.Hour))
	res := &mapResolver{
		subs: map[string]*SubscriptionDetail{"sub-x": {SubscriptionID: "sub-x", TargetURL: srv.URL}},
		errs: map[string]error{"sub-x": context.DeadlineExceeded}, // once
	}
	d := NewWebhookDispatcher(st, res, DispatcherConfig{})
	sharedClock(d, st)
	tripOpen(d.breaker, "sub-x", st.now().Add(-2*circuitRecoveryTimeout))

	for i := 0; i < 30; i++ {
		d.poll(context.Background())
		st.advance(2 * time.Second) // past any backoff the failed probe scheduled
	}
	if hits.Load() != 1 || st.row("d1").Status != "delivered" {
		t.Fatalf("hits=%d row=%+v breaker=%s: half-open probe wedged the subscription",
			hits.Load(), st.row("d1"), d.breaker.State("sub-x"))
	}
	if d.breaker.State("sub-x") != CircuitClosed {
		t.Fatalf("breaker=%s, want closed after a successful probe", d.breaker.State("sub-x"))
	}
}

// TestHalfOpenProbeRejectedURLReleasesProbe: the URL-rejected early
// return also resolves the probe.
func TestHalfOpenProbeRejectedURLReleasesProbe(t *testing.T) {
	st := newQueueStore()
	st.add("d1", "sub-x", st.now().Add(-time.Hour))
	res := &mapResolver{subs: map[string]*SubscriptionDetail{
		"sub-x": {SubscriptionID: "sub-x", TargetURL: "https://169.254.169.254/latest/meta-data/"},
	}}
	d := NewWebhookDispatcher(st, res, DispatcherConfig{Targets: NewTargetValidator(nil)})
	sharedClock(d, st)
	tripOpen(d.breaker, "sub-x", st.now().Add(-2*circuitRecoveryTimeout))
	d.poll(context.Background())
	if s := d.breaker.State("sub-x"); s == CircuitHalfOpen {
		t.Fatalf("breaker left half-open after a URL-rejected probe")
	}
}
