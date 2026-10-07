package delivery

import (
	"context"
	"testing"
	"time"
)

// INIT-023 TASK-010 AC14: Permit unit tests. The pre-existing
// circuit_breaker_test.go cases run unmodified.

func newClockedBreaker() (*CircuitBreaker, *time.Time) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	cb := NewCircuitBreaker()
	cb.now = func() time.Time { return now }
	return cb, &now
}

func TestPermit_AcquireTransitions(t *testing.T) {
	ctx := context.Background()
	cb, now := newClockedBreaker()

	p, ok := cb.Acquire(ctx, "s")
	if !ok || p == nil || p.probe {
		t.Fatalf("unknown circuit: permit=%v ok=%v, want a non-probe permit", p, ok)
	}

	tripOpen(cb, "s", *now)
	if p, ok := cb.Acquire(ctx, "s"); ok || p != nil {
		t.Fatal("open circuit within its recovery window admitted a delivery")
	}

	*now = now.Add(circuitRecoveryTimeout)
	probe, ok := cb.Acquire(ctx, "s")
	if !ok || !probe.probe || cb.State("s") != CircuitHalfOpen {
		t.Fatalf("due circuit: ok=%v probe=%v state=%s, want a probe and half-open", ok, probe != nil && probe.probe, cb.State("s"))
	}
	if p, ok := cb.Acquire(ctx, "s"); ok || p != nil {
		t.Fatal("half-open circuit admitted a second delivery while the probe is in flight")
	}
}

func TestPermit_ReleaseAfterOutcomeIsNoOp(t *testing.T) {
	ctx := context.Background()
	for _, outcome := range []string{"success", "failure"} {
		cb, now := newClockedBreaker()
		tripOpen(cb, "s", now.Add(-2*circuitRecoveryTimeout))
		probe, ok := cb.Acquire(ctx, "s")
		if !ok || !probe.probe {
			t.Fatal("expected a probe")
		}
		if outcome == "success" {
			probe.Success(ctx)
		} else {
			probe.Failure(ctx)
		}
		before := cb.circuits["s"].lastFailureAt
		state := cb.State("s")
		probe.Release(ctx)
		probe.Success(ctx) // first resolution wins: also a no-op
		probe.Failure(ctx)
		if cb.State("s") != state || !cb.circuits["s"].lastFailureAt.Equal(before) {
			t.Fatalf("%s: later resolutions changed the circuit (%s -> %s)", outcome, state, cb.State("s"))
		}
		want := CircuitClosed
		if outcome == "failure" {
			want = CircuitOpen
		}
		if state != want {
			t.Fatalf("%s: state %s, want %s", outcome, state, want)
		}
	}
}

func TestPermit_ReleaseOfNonProbeIsNoOp(t *testing.T) {
	ctx := context.Background()
	cb, now := newClockedBreaker()
	early, _ := cb.Acquire(ctx, "s") // non-probe permit, circuit closed
	tripOpen(cb, "s", now.Add(-2*circuitRecoveryTimeout))
	probe, _ := cb.Acquire(ctx, "s") // circuit now half-open
	early.Release(ctx)
	if cb.State("s") != CircuitHalfOpen {
		t.Fatalf("non-probe Release changed the circuit to %s", cb.State("s"))
	}
	probe.Release(ctx)
}

func TestPermit_ReleaseOfProbeReopensKeepingLastFailure(t *testing.T) {
	ctx := context.Background()
	cb, now := newClockedBreaker()
	last := now.Add(-2 * circuitRecoveryTimeout)
	tripOpen(cb, "s", last)
	probe, ok := cb.Acquire(ctx, "s")
	if !ok || !probe.probe {
		t.Fatal("expected a probe")
	}
	probe.Release(ctx)
	if cb.State("s") != CircuitOpen {
		t.Fatalf("state after probe release = %s, want open", cb.State("s"))
	}
	if got := cb.circuits["s"].lastFailureAt; !got.Equal(last) {
		t.Fatalf("lastFailureAt moved: %v -> %v", last, got)
	}
	if n := cb.circuits["s"].consecutiveFailures; n != circuitFailureThreshold {
		t.Fatalf("release counted as a failure: consecutiveFailures=%d", n)
	}
	// The next Acquire may probe again at once.
	if p, ok := cb.Acquire(ctx, "s"); !ok || !p.probe {
		t.Fatal("no immediate re-probe after a released probe")
	}
}

// TestPermitReleaseAfterOtherPermitClosed (design-check guard): a probe
// released after another permit closed the circuit leaves it closed.
func TestPermitReleaseAfterOtherPermitClosed(t *testing.T) {
	ctx := context.Background()
	cb, now := newClockedBreaker()
	early, ok := cb.Acquire(ctx, "s") // taken while closed
	if !ok {
		t.Fatal("closed circuit refused a permit")
	}
	for i := 0; i < circuitFailureThreshold; i++ {
		cb.RecordFailure(ctx, "s")
	}
	*now = now.Add(2 * circuitRecoveryTimeout)
	probe, ok := cb.Acquire(ctx, "s")
	if !ok || !probe.probe {
		t.Fatal("expected a probe permit")
	}
	early.Success(ctx) // late success of a request sent before the circuit opened
	probe.Release(ctx)
	if got := cb.State("s"); got != CircuitClosed {
		t.Fatalf("state after late success + probe release = %s, want closed", got)
	}
}

func TestPermit_NilIsNoOp(t *testing.T) {
	var p *Permit
	p.Success(context.Background())
	p.Failure(context.Background())
	p.Release(context.Background())
}

func TestCircuitBreaker_NextAttemptAt(t *testing.T) {
	ctx := context.Background()
	cb, now := newClockedBreaker()
	const fallback = 3 * time.Second

	if got := cb.NextAttemptAt("unknown", fallback); !got.Equal(now.Add(fallback)) {
		t.Fatalf("unknown circuit: %v, want now+fallback", got)
	}
	last := now.Add(-10 * time.Second)
	tripOpen(cb, "s", last)
	if got, want := cb.NextAttemptAt("s", fallback), last.Add(circuitRecoveryTimeout); !got.Equal(want) {
		t.Fatalf("open circuit: %v, want the probe time %v", got, want)
	}
	*now = now.Add(circuitRecoveryTimeout)
	if _, ok := cb.Acquire(ctx, "s"); !ok { // half-open, probe in flight
		t.Fatal("expected a probe")
	}
	if got := cb.NextAttemptAt("s", fallback); !got.Equal(now.Add(fallback)) {
		t.Fatalf("half-open circuit: %v, want now+fallback", got)
	}
}
