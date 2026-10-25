package workspace

// INIT-024 TASK-008 (codex review, round 3): a workspace whose services
// never finish building — a database that accepts the connection and never
// answers, the build running on the pool-lifetime context — no longer holds
// every Get of it for good: past InitTimeout the entry fails with
// ErrWorkspaceUnavailable for the initiator and every waiter and is
// removed, the abandoned build's late result is closed, and the next Get
// builds afresh.

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
	lateClosed := make(chan string, 1)
	res := &multiConfigResolver{configs: map[string]Config{"ws-slow": {ID: "ws-slow", RepoPath: t.TempDir(), Status: StatusActive}}}
	pool := NewServicePool(context.Background(), res, PoolConfig{
		IdleCheckInterval: -1,
		InitTimeout:       150 * time.Millisecond,
		Builder: func(_ context.Context, ss *ServiceSet) error {
			if builds.Add(1) == 1 {
				<-release // the first build hangs until the test lets it go
				ss.AppendCloser(func(reason string) { lateClosed <- reason })
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
	if n := pool.ActiveCount(); n != 0 {
		t.Fatalf("%d entries left after the timed-out build, want 0", n)
	}

	// The next Get builds afresh (the first build is still hanging).
	ss, err := pool.Get(context.Background(), "ws-slow")
	if err != nil || ss == nil {
		t.Fatalf("Get after the timeout = %v, %v; want a fresh build", ss, err)
	}
	pool.Release("ws-slow")
	if n := builds.Load(); n != 2 {
		t.Fatalf("builds = %d, want 2", n)
	}

	// The abandoned build finishes: its set is closed, never cached.
	releaseOnce.Do(func() { close(release) })
	select {
	case reason := <-lateClosed:
		if reason != "init-error" {
			t.Fatalf("the abandoned build's set closed with %q, want init-error", reason)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the abandoned build's set was never closed")
	}
	if got, err := pool.Get(context.Background(), "ws-slow"); err != nil || got != ss {
		t.Fatalf("Get after the abandoned build finished = %p, %v; want the cached fresh set %p", got, err, ss)
	}
	pool.Release("ws-slow")
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
