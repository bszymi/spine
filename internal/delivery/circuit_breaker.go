package delivery

import (
	"context"
	"sync"
	"time"

	"github.com/bszymi/spine/internal/observe"
)

// CircuitState represents the state of a circuit breaker.
type CircuitState int

const (
	CircuitClosed   CircuitState = iota // normal operation
	CircuitOpen                         // failing, no deliveries
	CircuitHalfOpen                     // probing with single delivery
)

func (s CircuitState) String() string {
	switch s {
	case CircuitClosed:
		return "closed"
	case CircuitOpen:
		return "open"
	case CircuitHalfOpen:
		return "half-open"
	}
	return "unknown"
}

const (
	circuitFailureThreshold = 10
	circuitRecoveryTimeout  = 60 * time.Second
)

// circuitEntry tracks circuit breaker state for a single subscription.
type circuitEntry struct {
	state              CircuitState
	consecutiveFailures int
	lastFailureAt      time.Time
}

// CircuitBreaker tracks per-subscription failure state and prevents
// deliveries to consistently failing endpoints. In-memory only —
// resets on restart (conservative).
type CircuitBreaker struct {
	mu       sync.Mutex
	circuits map[string]*circuitEntry
}

// NewCircuitBreaker creates a new circuit breaker tracker.
func NewCircuitBreaker() *CircuitBreaker {
	return &CircuitBreaker{
		circuits: make(map[string]*circuitEntry),
	}
}

// AllowDelivery returns true if the circuit allows a delivery attempt
// for the given subscription. If the circuit is open and the recovery
// timeout has elapsed, it transitions to half-open.
func (cb *CircuitBreaker) AllowDelivery(ctx context.Context, subscriptionID string) bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	entry, ok := cb.circuits[subscriptionID]
	if !ok {
		return true // no circuit = closed
	}

	switch entry.state {
	case CircuitClosed:
		return true
	case CircuitOpen:
		if time.Since(entry.lastFailureAt) >= circuitRecoveryTimeout {
			entry.state = CircuitHalfOpen
			observe.Logger(ctx).Info("circuit half-open",
				"subscription_id", subscriptionID)
			return true // allow one probe
		}
		return false
	case CircuitHalfOpen:
		return false // already probing, don't allow more
	}
	return true
}

// RecordSuccess records a successful delivery. If the circuit is
// half-open, this closes it. Resets consecutive failure count.
func (cb *CircuitBreaker) RecordSuccess(ctx context.Context, subscriptionID string) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	entry, ok := cb.circuits[subscriptionID]
	if !ok {
		return
	}

	if entry.state == CircuitHalfOpen {
		observe.Logger(ctx).Info("circuit closed",
			"subscription_id", subscriptionID)
	}

	entry.state = CircuitClosed
	entry.consecutiveFailures = 0
}

// RecordFailure records a failed delivery. If consecutive failures
// reach the threshold, the circuit opens.
func (cb *CircuitBreaker) RecordFailure(ctx context.Context, subscriptionID string) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	entry, ok := cb.circuits[subscriptionID]
	if !ok {
		entry = &circuitEntry{}
		cb.circuits[subscriptionID] = entry
	}

	entry.consecutiveFailures++
	entry.lastFailureAt = time.Now()

	if entry.state == CircuitHalfOpen {
		entry.state = CircuitOpen
		observe.Logger(ctx).Info("circuit re-tripped (half-open probe failed)",
			"subscription_id", subscriptionID,
			"failures", entry.consecutiveFailures)
		return
	}

	if entry.consecutiveFailures >= circuitFailureThreshold && entry.state == CircuitClosed {
		entry.state = CircuitOpen
		observe.Logger(ctx).Info("circuit opened",
			"subscription_id", subscriptionID,
			"failures", entry.consecutiveFailures)
	}
}

// State returns the current circuit state for a subscription.
func (cb *CircuitBreaker) State(subscriptionID string) CircuitState {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	entry, ok := cb.circuits[subscriptionID]
	if !ok {
		return CircuitClosed
	}
	return entry.state
}
