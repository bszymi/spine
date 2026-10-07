package delivery

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bszymi/spine/core/observe"
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
	state               CircuitState
	consecutiveFailures int
	lastFailureAt       time.Time
}

// CircuitBreaker tracks per-subscription failure state and prevents
// deliveries to consistently failing endpoints. In-memory only —
// resets on restart (conservative).
//
// The dispatcher drives it through Acquire and the returned Permit
// (INIT-023 TASK-010, audit P3-12). AllowDelivery/RecordSuccess/
// RecordFailure remain for compatibility but are not safe for dispatch:
// AllowDelivery grants the single half-open probe without any handle to
// resolve it, so a caller that exits after the grant without recording
// an outcome leaves the circuit half-open — refusing every delivery for
// that subscription — until the breaker is rebuilt.
type CircuitBreaker struct {
	mu       sync.Mutex
	circuits map[string]*circuitEntry
	// now is the breaker's clock (time.Now in production; tests share
	// one simulated clock between the store, dispatcher and breaker).
	now func() time.Time
}

// NewCircuitBreaker creates a new circuit breaker tracker.
func NewCircuitBreaker() *CircuitBreaker {
	return &CircuitBreaker{
		circuits: make(map[string]*circuitEntry),
		now:      time.Now,
	}
}

func (cb *CircuitBreaker) clock() time.Time {
	if cb.now == nil {
		return time.Now()
	}
	return cb.now()
}

// Permit is one admitted delivery attempt. The first of Success,
// Failure or Release to be called takes effect; later calls do nothing.
// The dispatcher defers Release right after Acquire, so every exit path
// — including the early returns that never reach the endpoint (resolver
// error, rejected URL, request build or signing failure) — resolves a
// half-open probe instead of wedging the circuit (P3-12). A nil *Permit
// is a valid no-op.
type Permit struct {
	cb             *CircuitBreaker
	subscriptionID string
	// probe is true for the permit that moved the circuit from open to
	// half-open: the single delivery whose outcome decides the circuit.
	probe bool
	done  atomic.Bool
}

// Acquire admits a delivery for subscriptionID or reports that the
// circuit holds it (ok == false: open and not yet due for a probe, or
// half-open with the probe in flight). It applies AllowDelivery's state
// transitions; the permit that takes an open circuit to half-open is
// marked as the probe.
func (cb *CircuitBreaker) Acquire(ctx context.Context, subscriptionID string) (*Permit, bool) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	entry, ok := cb.circuits[subscriptionID]
	if !ok {
		return &Permit{cb: cb, subscriptionID: subscriptionID}, true
	}
	switch entry.state {
	case CircuitClosed:
		return &Permit{cb: cb, subscriptionID: subscriptionID}, true
	case CircuitOpen:
		if cb.clock().Sub(entry.lastFailureAt) >= circuitRecoveryTimeout {
			entry.state = CircuitHalfOpen
			observe.Logger(ctx).Info("circuit half-open",
				"subscription_id", subscriptionID)
			return &Permit{cb: cb, subscriptionID: subscriptionID, probe: true}, true
		}
		return nil, false
	case CircuitHalfOpen:
		return nil, false
	}
	return &Permit{cb: cb, subscriptionID: subscriptionID}, true
}

// Success records that the endpoint accepted the delivery (as
// RecordSuccess), unless the permit is already resolved.
func (p *Permit) Success(ctx context.Context) {
	if p == nil || !p.done.CompareAndSwap(false, true) {
		return
	}
	p.cb.RecordSuccess(ctx, p.subscriptionID)
}

// Failure records an endpoint failure (as RecordFailure), unless the
// permit is already resolved.
func (p *Permit) Failure(ctx context.Context) {
	if p == nil || !p.done.CompareAndSwap(false, true) {
		return
	}
	p.cb.RecordFailure(ctx, p.subscriptionID)
}

// Release resolves a permit that ended with no endpoint outcome. For a
// non-probe permit, or one already resolved, it does nothing. For an
// unresolved probe it returns the circuit from half-open to open,
// keeping lastFailureAt, so the next Acquire may probe again at once.
//
// It is conditional on the circuit still being half-open: another permit
// may have resolved the episode first — a request admitted before the
// circuit opened can succeed late and close it — and an unconditional
// Release would re-open a closed circuit. Early exits do not count as
// endpoint failures: a runtime-DB blip during the probe must not trip a
// healthy endpoint's breaker.
func (p *Permit) Release(ctx context.Context) {
	if p == nil || !p.done.CompareAndSwap(false, true) || !p.probe {
		return
	}
	cb := p.cb
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if entry, ok := cb.circuits[p.subscriptionID]; ok && entry.state == CircuitHalfOpen {
		entry.state = CircuitOpen
		observe.Logger(ctx).Info("circuit probe released without an endpoint outcome",
			"subscription_id", p.subscriptionID)
	}
}

// NextAttemptAt is when a delivery the circuit held should next be
// claimed: the open circuit's probe time (lastFailureAt + recovery
// timeout), or now + fallback when no probe time lies ahead (a probe is
// in flight, or the circuit changed state since the delivery was held).
// The result is strictly after now for any fallback > 0.
func (cb *CircuitBreaker) NextAttemptAt(subscriptionID string, fallback time.Duration) time.Time {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	now := cb.clock()
	if entry, ok := cb.circuits[subscriptionID]; ok && entry.state == CircuitOpen {
		if probeAt := entry.lastFailureAt.Add(circuitRecoveryTimeout); probeAt.After(now) {
			return probeAt
		}
	}
	return now.Add(fallback)
}

// AllowDelivery returns true if the circuit allows a delivery attempt
// for the given subscription. If the circuit is open and the recovery
// timeout has elapsed, it transitions to half-open.
//
// Not for dispatch use: it grants the half-open probe without a way to
// resolve it on an early exit. Use Acquire and the returned Permit.
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
		if cb.clock().Sub(entry.lastFailureAt) >= circuitRecoveryTimeout {
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
	entry.lastFailureAt = cb.clock()

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
