package gateway

import (
	"sync"
	"testing"
)

func TestSSELimiter_RespectsMax(t *testing.T) {
	l := newSSELimiter(3)
	for i := 0; i < 3; i++ {
		if !l.Acquire("actor-1") {
			t.Fatalf("slot %d: expected acquire to succeed", i)
		}
	}
	if l.Acquire("actor-1") {
		t.Fatal("4th acquire should be rejected")
	}
	// Different actor has its own bucket.
	if !l.Acquire("actor-2") {
		t.Fatal("actor-2 should get its own bucket")
	}
}

func TestSSELimiter_ReleaseFreesSlot(t *testing.T) {
	l := newSSELimiter(1)
	if !l.Acquire("actor-1") {
		t.Fatal("first acquire failed")
	}
	if l.Acquire("actor-1") {
		t.Fatal("second acquire should be rejected")
	}
	l.Release("actor-1")
	if !l.Acquire("actor-1") {
		t.Fatal("acquire after release should succeed")
	}
}

func TestSSELimiter_ZeroMaxDisabled(t *testing.T) {
	l := newSSELimiter(0)
	for i := 0; i < 1000; i++ {
		if !l.Acquire("a") {
			t.Fatalf("iteration %d: disabled limiter should always accept", i)
		}
	}
}

func TestSSELimiter_EmptyActorIDBypasses(t *testing.T) {
	l := newSSELimiter(1)
	for i := 0; i < 10; i++ {
		if !l.Acquire("") {
			t.Fatal("empty actor id should bypass the limiter")
		}
	}
}

func TestSSELimiter_ConcurrentAcquireRelease(t *testing.T) {
	l := newSSELimiter(5)
	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			if l.Acquire("actor-1") {
				l.Release("actor-1")
			}
		}()
	}
	wg.Wait()

	// After all goroutines finish, map entry should be cleaned up.
	l.mu.Lock()
	defer l.mu.Unlock()
	if c := l.counts["actor-1"]; c != 0 {
		t.Fatalf("expected counter to drain to 0, got %d", c)
	}
}
