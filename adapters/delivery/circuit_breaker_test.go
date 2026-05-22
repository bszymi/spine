package delivery

import (
	"context"
	"testing"
	"time"
)

func TestCircuitBreaker_ClosedByDefault(t *testing.T) {
	cb := NewCircuitBreaker()
	if !cb.AllowDelivery(context.Background(), "sub-1") {
		t.Error("expected delivery allowed on new circuit")
	}
	if cb.State("sub-1") != CircuitClosed {
		t.Errorf("expected closed, got %v", cb.State("sub-1"))
	}
}

func TestCircuitBreaker_OpensAfterThreshold(t *testing.T) {
	cb := NewCircuitBreaker()

	// 9 failures should keep circuit closed
	for i := 0; i < circuitFailureThreshold-1; i++ {
		cb.RecordFailure(context.Background(), "sub-1")
	}
	if !cb.AllowDelivery(context.Background(), "sub-1") {
		t.Error("circuit should still be closed before threshold")
	}

	// 10th failure should trip the circuit
	cb.RecordFailure(context.Background(), "sub-1")
	if cb.State("sub-1") != CircuitOpen {
		t.Errorf("expected open after %d failures, got %v", circuitFailureThreshold, cb.State("sub-1"))
	}
	if cb.AllowDelivery(context.Background(), "sub-1") {
		t.Error("delivery should be blocked when circuit is open")
	}
}

func TestCircuitBreaker_HalfOpenAfterTimeout(t *testing.T) {
	cb := NewCircuitBreaker()

	// Trip the circuit
	for i := 0; i < circuitFailureThreshold; i++ {
		cb.RecordFailure(context.Background(), "sub-1")
	}

	// Manually set lastFailureAt to the past to simulate timeout
	cb.mu.Lock()
	cb.circuits["sub-1"].lastFailureAt = time.Now().Add(-circuitRecoveryTimeout - time.Second)
	cb.mu.Unlock()

	// Should transition to half-open and allow one delivery
	if !cb.AllowDelivery(context.Background(), "sub-1") {
		t.Error("should allow one probe after recovery timeout")
	}
	if cb.State("sub-1") != CircuitHalfOpen {
		t.Errorf("expected half-open, got %v", cb.State("sub-1"))
	}

	// Second delivery in half-open should be blocked
	if cb.AllowDelivery(context.Background(), "sub-1") {
		t.Error("should block additional deliveries in half-open")
	}
}

func TestCircuitBreaker_SuccessClosesHalfOpen(t *testing.T) {
	cb := NewCircuitBreaker()

	// Trip and transition to half-open
	for i := 0; i < circuitFailureThreshold; i++ {
		cb.RecordFailure(context.Background(), "sub-1")
	}
	cb.mu.Lock()
	cb.circuits["sub-1"].lastFailureAt = time.Now().Add(-circuitRecoveryTimeout - time.Second)
	cb.mu.Unlock()
	cb.AllowDelivery(context.Background(), "sub-1") // transitions to half-open

	// Success should close the circuit
	cb.RecordSuccess(context.Background(), "sub-1")
	if cb.State("sub-1") != CircuitClosed {
		t.Errorf("expected closed after success, got %v", cb.State("sub-1"))
	}
	if !cb.AllowDelivery(context.Background(), "sub-1") {
		t.Error("delivery should be allowed after circuit closes")
	}
}

func TestCircuitBreaker_FailureRetripsHalfOpen(t *testing.T) {
	cb := NewCircuitBreaker()

	// Trip and transition to half-open
	for i := 0; i < circuitFailureThreshold; i++ {
		cb.RecordFailure(context.Background(), "sub-1")
	}
	cb.mu.Lock()
	cb.circuits["sub-1"].lastFailureAt = time.Now().Add(-circuitRecoveryTimeout - time.Second)
	cb.mu.Unlock()
	cb.AllowDelivery(context.Background(), "sub-1") // transitions to half-open

	// Failure should re-trip
	cb.RecordFailure(context.Background(), "sub-1")
	if cb.State("sub-1") != CircuitOpen {
		t.Errorf("expected open after half-open failure, got %v", cb.State("sub-1"))
	}
}

func TestCircuitBreaker_SuccessResetsCount(t *testing.T) {
	cb := NewCircuitBreaker()

	// Accumulate some failures
	for i := 0; i < 5; i++ {
		cb.RecordFailure(context.Background(), "sub-1")
	}

	// Success resets
	cb.RecordSuccess(context.Background(), "sub-1")

	// Should need a full threshold again to trip
	for i := 0; i < circuitFailureThreshold-1; i++ {
		cb.RecordFailure(context.Background(), "sub-1")
	}
	if cb.State("sub-1") != CircuitClosed {
		t.Error("circuit should still be closed after reset + fewer failures")
	}
}

func TestCircuitBreaker_IndependentPerSubscription(t *testing.T) {
	cb := NewCircuitBreaker()

	// Trip sub-1
	for i := 0; i < circuitFailureThreshold; i++ {
		cb.RecordFailure(context.Background(), "sub-1")
	}

	// sub-2 should be unaffected
	if !cb.AllowDelivery(context.Background(), "sub-2") {
		t.Error("sub-2 should be independent")
	}
	if cb.AllowDelivery(context.Background(), "sub-1") {
		t.Error("sub-1 should be blocked")
	}
}

func TestCircuitState_String(t *testing.T) {
	tests := []struct {
		state CircuitState
		want  string
	}{
		{state: CircuitClosed, want: "closed"},
		{state: CircuitOpen, want: "open"},
		{state: CircuitHalfOpen, want: "half-open"},
		{state: CircuitState(42), want: "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.state.String(); got != tt.want {
				t.Errorf("CircuitState(%d).String() = %q, want %q", tt.state, got, tt.want)
			}
		})
	}
}

func TestCircuitBreaker_RecordSuccess_UnknownSubscription_NoOp(t *testing.T) {
	cb := NewCircuitBreaker()
	// Calling RecordSuccess on a subscription the breaker has never seen
	// must be a silent no-op; the uninitialized entry path was otherwise
	// untested.
	cb.RecordSuccess(context.Background(), "never-recorded")
	if cb.State("never-recorded") != CircuitClosed {
		t.Errorf("expected default closed state, got %v", cb.State("never-recorded"))
	}
}
