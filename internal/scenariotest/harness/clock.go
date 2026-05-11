package harness

import (
	"fmt"
	"sync"
	"time"
)

// Clock is a controllable test clock. Scenarios advance it via
// Advance(d) and register tick handlers via OnAdvance(name, fn) so a
// single Advance call both (a) bumps the time observed by anything
// reading from c.Now and (b) drives the side-effects that would
// normally fire on the next scheduler tick — without sleeping.
//
// Scope: the clock only governs seams that explicitly read from it.
// Production code paths still call time.Now directly; only the
// scheduler scan loops (via scheduler.WithClock) and any scenario-side
// closures that take the Now method opt in. Per TASK-028, this is the
// minimum surface needed to make time-based scheduler tests
// deterministic.
type Clock struct {
	mu       sync.Mutex
	t        time.Time
	handlers []clockHandler
}

type clockHandler struct {
	name string
	fn   func() error
}

// NewClock returns a Clock initialized to t0. The harness seeds this
// with the wall clock at TestRuntime construction so timestamps written
// by production code (engine.StartRun, etc.) are comparable to the
// clock without a flag-day rewrite of every time.Now call site.
func NewClock(t0 time.Time) *Clock {
	return &Clock{t: t0}
}

// Now returns the current clock time. Pass this method value
// (clock.Now) into scheduler.WithClock so a single Advance is
// observed by every scan loop.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// Advance bumps the clock by d and invokes every registered tick
// handler in registration order. Handlers run with the lock released
// so they may re-enter Now safely; the first handler error is
// returned (subsequent handlers still run, so all observable side
// effects are visible to the failing assertion).
func (c *Clock) Advance(d time.Duration) error {
	c.mu.Lock()
	c.t = c.t.Add(d)
	handlers := make([]clockHandler, len(c.handlers))
	copy(handlers, c.handlers)
	c.mu.Unlock()

	var firstErr error
	for _, h := range handlers {
		if err := h.fn(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("clock tick %q: %w", h.name, err)
		}
	}
	return firstErr
}

// OnAdvance registers a tick handler. Scenarios that have constructed
// a scheduler register its scan methods so Advance both bumps the
// clock and observes the resulting side effects in one synchronous
// call. The name is used only in error messages.
func (c *Clock) OnAdvance(name string, fn func() error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.handlers = append(c.handlers, clockHandler{name: name, fn: fn})
}
