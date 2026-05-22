package gateway

import "sync"

// sseLimiter caps concurrent SSE connections per actor.
//
// A zero cap means no limit. Acquire increments the counter for an
// actor if the cap is not yet reached and returns true; Release
// decrements it. The limiter is safe for concurrent use.
type sseLimiter struct {
	mu     sync.Mutex
	counts map[string]int
	max    int
}

// newSSELimiter constructs a limiter with the given per-actor cap.
// max <= 0 disables limiting.
func newSSELimiter(max int) *sseLimiter {
	return &sseLimiter{counts: make(map[string]int), max: max}
}

// Acquire reserves a slot for the given actor. Returns false if the
// actor is already at the cap. Passing an empty actorID bypasses the
// limiter (returns true).
func (l *sseLimiter) Acquire(actorID string) bool {
	if l == nil || l.max <= 0 || actorID == "" {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.counts[actorID] >= l.max {
		return false
	}
	l.counts[actorID]++
	return true
}

// Release frees a previously acquired slot. Safe to call with an
// empty actorID (no-op).
func (l *sseLimiter) Release(actorID string) {
	if l == nil || l.max <= 0 || actorID == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	n := l.counts[actorID]
	if n <= 1 {
		delete(l.counts, actorID)
		return
	}
	l.counts[actorID] = n - 1
}
