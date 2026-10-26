package workspace

// INIT-024 TASK-008 (codex review, rounds 3 and 4): a workspace whose
// services never finish building — a database that accepts the connection
// and never answers, the build running on the pool-lifetime context — no
// longer holds every Get of it for good: past InitTimeout the initiator and
// every waiter fail with ErrWorkspaceUnavailable. Until the abandoned build
// returns every Get fails at once and no second build starts beside it (a
// late build would register its database pool over the replacement's and
// close it); when it returns, its set is closed first and only then is the
// entry removed, so the next Get builds afresh.

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestServicePool_InitializationTimesOut(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	var builds atomic.Int32
	var pool *ServicePool
	lateClosed := make(chan error, 1) // what a Get answers while the late set is being closed
	res := &multiConfigResolver{configs: map[string]Config{"ws-slow": {ID: "ws-slow", RepoPath: t.TempDir(), Status: StatusActive}}}
	pool = NewServicePool(context.Background(), res, PoolConfig{
		IdleCheckInterval: -1,
		InitTimeout:       150 * time.Millisecond,
		Builder: func(_ context.Context, ss *ServiceSet) error {
			if builds.Add(1) == 1 {
				<-release // the first build hangs until the test lets it go
				ss.AppendCloser(func(reason string) {
					_, err := pool.Get(context.Background(), "ws-slow")
					if reason != "init-error" {
						err = errors.New("closed with " + reason)
					}
					lateClosed <- err
				})
			}
			return nil
		},
	})
	t.Cleanup(pool.Close)

	errs := make(chan error, 2)
	start := time.Now()
	for i := 0; i < 2; i++ { // the initiator and a waiter
		go func() {
			_, err := pool.Get(context.Background(), "ws-slow")
			errs <- err
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case err := <-errs:
			if !errors.Is(err, ErrWorkspaceUnavailable) {
				t.Fatalf("Get of a workspace whose build hangs = %v, want ErrWorkspaceUnavailable", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("Get of a workspace whose build hangs did not return; InitTimeout is not applied")
		}
	}
	if took := time.Since(start); took < 150*time.Millisecond {
		t.Fatalf("Get gave up after %s, before InitTimeout", took)
	}

	// While the abandoned build runs: every Get fails at once, Evict and
	// EvictIdle leave the entry, and no second build starts.
	pool.Evict("ws-slow")
	pool.EvictIdle()
	quick, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	t0 := time.Now()
	if _, err := pool.Get(quick, "ws-slow"); !errors.Is(err, ErrWorkspaceUnavailable) {
		t.Fatalf("Get while the abandoned build runs = %v, want ErrWorkspaceUnavailable", err)
	}
	if took := time.Since(t0); took > time.Second {
		t.Fatalf("Get while the abandoned build runs took %s, want an immediate failure", took)
	}
	if n := builds.Load(); n != 1 {
		t.Fatalf("builds = %d while the first is still running, want 1 (no second build beside it)", n)
	}

	// The abandoned build returns: its set is closed while the entry still
	// refuses Gets (no replacement can register resources first), then the
	// entry goes and the next Get builds afresh.
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-lateClosed:
		if !errors.Is(err, ErrWorkspaceUnavailable) {
			t.Fatalf("a Get during the late set's close = %v, want ErrWorkspaceUnavailable (the entry must stay until it is closed)", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the abandoned build's set was never closed")
	}
	deadline := time.Now().Add(5 * time.Second)
	for pool.ActiveCount() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the abandoned entry was not removed after its build returned")
		}
		time.Sleep(5 * time.Millisecond)
	}
	ss, err := pool.Get(context.Background(), "ws-slow")
	if err != nil || ss == nil {
		t.Fatalf("Get after the abandoned build drained = %v, %v; want a fresh build", ss, err)
	}
	pool.Release("ws-slow")
	if n := builds.Load(); n != 2 {
		t.Fatalf("builds = %d, want 2", n)
	}
}

func TestServicePool_InitTimeoutDefault(t *testing.T) {
	pool := NewServicePool(context.Background(), &multiConfigResolver{}, PoolConfig{IdleCheckInterval: -1})
	t.Cleanup(pool.Close)
	if pool.initTimeout != 0 {
		t.Fatalf("initTimeout = %s, want 0 (DefaultInitTimeout applies)", pool.initTimeout)
	}
	if DefaultInitTimeout != 2*time.Minute {
		t.Fatalf("DefaultInitTimeout = %s, want 2m", DefaultInitTimeout)
	}
}

// A pool closed while a build is abandoned still closes the late result.
func TestServicePool_CloseDrainsAnAbandonedBuild(t *testing.T) {
	release := make(chan struct{})
	closed := make(chan string, 1)
	res := &multiConfigResolver{configs: map[string]Config{"ws-slow": {ID: "ws-slow", RepoPath: t.TempDir(), Status: StatusActive}}}
	pool := NewServicePool(context.Background(), res, PoolConfig{
		IdleCheckInterval: -1,
		InitTimeout:       50 * time.Millisecond,
		Builder: func(_ context.Context, ss *ServiceSet) error {
			<-release
			ss.AppendCloser(func(reason string) { closed <- reason })
			return nil
		},
	})
	if _, err := pool.Get(context.Background(), "ws-slow"); !errors.Is(err, ErrWorkspaceUnavailable) {
		t.Fatalf("Get = %v, want ErrWorkspaceUnavailable", err)
	}
	pool.Close()
	close(release)
	select {
	case reason := <-closed:
		if reason != "init-error" {
			t.Fatalf("late set closed with %q, want init-error", reason)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the abandoned build's set was never closed after the pool closed")
	}
}

// No caller holds an abandoned entry: its refcount is zero.
func TestServicePool_AbandonedEntryHoldsNoReference(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	res := &multiConfigResolver{configs: map[string]Config{"ws-slow": {ID: "ws-slow", RepoPath: t.TempDir(), Status: StatusActive}}}
	pool := NewServicePool(context.Background(), res, PoolConfig{
		IdleCheckInterval: -1,
		InitTimeout:       50 * time.Millisecond,
		Builder:           func(context.Context, *ServiceSet) error { <-release; return nil },
	})
	t.Cleanup(pool.Close)
	if _, err := pool.Get(context.Background(), "ws-slow"); !errors.Is(err, ErrWorkspaceUnavailable) {
		t.Fatalf("Get = %v, want ErrWorkspaceUnavailable", err)
	}
	if n := pool.RefCount("ws-slow"); n != 0 {
		t.Fatalf("abandoned entry refcount = %d, want 0", n)
	}
}
