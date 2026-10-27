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
	"net"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/secrets"
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

// Codex review round 5: the build runs in its own goroutine, out of reach
// of the caller's recovery, so a panic in it (here the builder hook) is
// recovered and returned as an initialization error instead of ending the
// process; the entry is removed and the next Get builds again.
func TestServicePool_BuildPanicIsAnInitializationError(t *testing.T) {
	var builds atomic.Int32
	res := &multiConfigResolver{configs: map[string]Config{"ws-p": {ID: "ws-p", RepoPath: t.TempDir(), Status: StatusActive}}}
	pool := NewServicePool(context.Background(), res, PoolConfig{
		IdleCheckInterval: -1,
		Builder: func(context.Context, *ServiceSet) error {
			if builds.Add(1) == 1 {
				panic("builder hook failed")
			}
			return nil
		},
	})
	t.Cleanup(pool.Close)
	_, err := pool.Get(context.Background(), "ws-p")
	if !errors.Is(err, ErrWorkspaceUnavailable) {
		t.Fatalf("Get whose build panicked = %v, want ErrWorkspaceUnavailable", err)
	}
	if strings.Contains(err.Error(), "builder hook failed") {
		t.Fatalf("the panic value reached the error: %v", err)
	}
	if n := pool.ActiveCount(); n != 0 {
		t.Fatalf("%d entries after the panicked build, want 0", n)
	}
	if _, err := pool.Get(context.Background(), "ws-p"); err != nil {
		t.Fatalf("Get after the panicked build = %v, want a fresh build", err)
	}
	pool.Release("ws-p")
}

// Regression (review round 2; red at 250924d4): a Get already waiting on an
// entry that was evicted while its build ran waited only for the entry to
// go — which an abandoned build never lets happen until it returns — so it
// outlived InitTimeout, up to its own context. It now wakes on the build's
// outcome too and fails at about InitTimeout.
func TestServicePool_EvictedWaiterFailsAtInitTimeout(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	res := &multiConfigResolver{configs: map[string]Config{"ws": {ID: "ws", RepoPath: t.TempDir(), Status: StatusActive}}}
	pool := NewServicePool(context.Background(), res, PoolConfig{
		IdleCheckInterval: -1,
		InitTimeout:       150 * time.Millisecond,
		Builder:           func(context.Context, *ServiceSet) error { <-release; return nil },
	})
	t.Cleanup(pool.Close)
	start := time.Now()
	go func() { _, _ = pool.Get(context.Background(), "ws") }() // the initiator
	deadline := time.Now().Add(5 * time.Second)
	for pool.ActiveCount() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the build never started")
		}
		time.Sleep(time.Millisecond)
	}
	pool.Evict("ws") // during the build: marks the entry evicting
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := pool.Get(ctx, "ws")
	took := time.Since(start)
	if !errors.Is(err, ErrWorkspaceUnavailable) {
		t.Fatalf("a Get waiting on the evicted entry = %v after %s, want ErrWorkspaceUnavailable at about InitTimeout", err, took)
	}
	if took > 3*time.Second {
		t.Fatalf("a Get waiting on the evicted entry returned after %s, want about the 150ms InitTimeout", took)
	}
}

// Regression (review round 2; red at 250924d4): a healthy build that always
// takes longer than InitTimeout was closed every time it finished late and
// rebuilt, so the workspace was never served. A late success is installed
// (the entry was not evicted and the pool is open) and served from then on.
func TestServicePool_SlowHealthyBuildIsEventuallyServed(t *testing.T) {
	var builds atomic.Int32
	res := &multiConfigResolver{configs: map[string]Config{"ws": {ID: "ws", RepoPath: t.TempDir(), Status: StatusActive}}}
	pool := NewServicePool(context.Background(), res, PoolConfig{
		IdleCheckInterval: -1,
		InitTimeout:       60 * time.Millisecond,
		Builder: func(context.Context, *ServiceSet) error {
			builds.Add(1)
			time.Sleep(90 * time.Millisecond) // healthy, 1.5x InitTimeout
			return nil
		},
	})
	t.Cleanup(pool.Close)
	deadline := time.Now().Add(5 * time.Second)
	for {
		ss, err := pool.Get(context.Background(), "ws")
		if err == nil && ss != nil {
			pool.Release("ws")
			break
		}
		if !errors.Is(err, ErrWorkspaceUnavailable) {
			t.Fatalf("Get = %v, want ErrWorkspaceUnavailable while the build runs", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("a healthy build at 1.5x InitTimeout was never served (%d builds)", builds.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if n := builds.Load(); n != 1 {
		t.Fatalf("builds = %d, want 1 (the late success is installed, not rebuilt)", n)
	}
}

// A late success for a workspace evicted while its build ran was built from
// the configuration the eviction invalidated: it is closed, not installed,
// and the next Get builds afresh.
func TestServicePool_LateBuildOfAnEvictedWorkspaceIsClosed(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	var builds atomic.Int32
	closedReason := make(chan string, 1)
	res := &multiConfigResolver{configs: map[string]Config{"ws": {ID: "ws", RepoPath: t.TempDir(), Status: StatusActive}}}
	pool := NewServicePool(context.Background(), res, PoolConfig{
		IdleCheckInterval: -1,
		InitTimeout:       50 * time.Millisecond,
		Builder: func(_ context.Context, ss *ServiceSet) error {
			if builds.Add(1) == 1 {
				<-release
				ss.AppendCloser(func(reason string) { closedReason <- reason })
			}
			return nil
		},
	})
	t.Cleanup(pool.Close)
	if _, err := pool.Get(context.Background(), "ws"); !errors.Is(err, ErrWorkspaceUnavailable) {
		t.Fatalf("Get = %v, want ErrWorkspaceUnavailable", err)
	}
	pool.Evict("ws") // while abandoned: an invalidation
	once.Do(func() { close(release) })
	select {
	case reason := <-closedReason:
		if reason != "init-error" {
			t.Fatalf("late set closed with %q, want init-error", reason)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the late set of an evicted workspace was not closed")
	}
	deadline := time.Now().Add(5 * time.Second)
	for pool.ActiveCount() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the entry was not removed")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := pool.Get(context.Background(), "ws"); err != nil {
		t.Fatalf("Get after the evicted late build = %v, want a fresh build", err)
	}
	pool.Release("ws")
	if n := builds.Load(); n != 2 {
		t.Fatalf("builds = %d, want 2", n)
	}
}

// Regression (review round 2; red at 250924d4): a panicking build ran none
// of the closers of what it had already started, so repeated panics leaked
// goroutines. buildServiceSet now closes it all before the pool's recovery
// classifies the panic.
func TestServicePool_PanickingBuildsCloseWhatTheyStarted(t *testing.T) {
	var closed atomic.Int32
	res := &multiConfigResolver{configs: map[string]Config{"ws": {ID: "ws", RepoPath: t.TempDir(), Status: StatusActive}}}
	pool := NewServicePool(context.Background(), res, PoolConfig{
		IdleCheckInterval: -1,
		Builder: func(_ context.Context, ss *ServiceSet) error {
			ss.AppendCloser(func(string) { closed.Add(1) })
			panic("builder hook failed")
		},
	})
	t.Cleanup(pool.Close)
	// One build first, so the goroutines that exist once per process are
	// counted in the baseline.
	if _, err := pool.Get(context.Background(), "ws"); !errors.Is(err, ErrWorkspaceUnavailable) {
		t.Fatalf("Get = %v, want ErrWorkspaceUnavailable", err)
	}
	time.Sleep(50 * time.Millisecond)
	before := runtime.NumGoroutine()
	const n = 30
	for i := 0; i < n; i++ {
		if _, err := pool.Get(context.Background(), "ws"); !errors.Is(err, ErrWorkspaceUnavailable) {
			t.Fatalf("Get = %v, want ErrWorkspaceUnavailable", err)
		}
	}
	if got := closed.Load(); got != n+1 {
		t.Fatalf("closers run = %d after %d panicked builds, want one per build", got, n+1)
	}
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > before+2 {
		if time.Now().After(deadline) {
			t.Fatalf("goroutines %d before %d panicked builds, %d after: they leak", before, n, runtime.NumGoroutine())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// silentDB accepts connections and never answers, like a database host
// that holds the connection open without speaking.
func silentDB(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	return "postgres://u:p@" + ln.Addr().String() + "/db?sslmode=disable"
}

// Regression (review round 2; red at 250924d4): the build's database ping
// ran on the pool-lifetime context, so a database that never answers held
// the abandoned build — and with it every Get of the workspace — until the
// process restarted. The ping now runs under the build's init deadline:
// the build fails, the entry goes, and the next Get tries again.
func TestServicePool_SilentDatabaseFailsTheBuildAtItsDeadline(t *testing.T) {
	cfg := Config{ID: "ws-db", RepoPath: t.TempDir(), Status: StatusActive,
		DatabaseURL: secrets.NewSecretValue([]byte(silentDB(t)))}
	pool := NewServicePool(context.Background(), &multiConfigResolver{configs: map[string]Config{"ws-db": cfg}},
		PoolConfig{IdleCheckInterval: -1, InitTimeout: 200 * time.Millisecond})
	t.Cleanup(pool.Close)
	if _, err := pool.Get(context.Background(), "ws-db"); !errors.Is(err, ErrWorkspaceUnavailable) {
		t.Fatalf("Get of a workspace whose database never answers = %v, want ErrWorkspaceUnavailable", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for pool.ActiveCount() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the build never returned: its database ping is not bounded by the init deadline")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The builder hook's context lives as long as the set; InitContext bounds
// its one-shot calls by the build's deadline. Outside a pool build it adds
// none.
func TestInitContext(t *testing.T) {
	seen := make(chan [2]bool, 1)
	res := &multiConfigResolver{configs: map[string]Config{"ws": {ID: "ws", RepoPath: t.TempDir(), Status: StatusActive}}}
	pool := NewServicePool(context.Background(), res, PoolConfig{
		IdleCheckInterval: -1,
		InitTimeout:       time.Minute,
		Builder: func(ctx context.Context, _ *ServiceSet) error {
			_, lifetimeHasDeadline := ctx.Deadline()
			initCtx, cancel := InitContext(ctx)
			defer cancel()
			d, initHasDeadline := initCtx.Deadline()
			seen <- [2]bool{lifetimeHasDeadline, initHasDeadline && time.Until(d) <= time.Minute}
			return nil
		},
	})
	t.Cleanup(pool.Close)
	if _, err := pool.Get(context.Background(), "ws"); err != nil {
		t.Fatal(err)
	}
	pool.Release("ws")
	got := <-seen
	if got[0] || !got[1] {
		t.Fatalf("builder ctx has a deadline = %v (want false); InitContext has the init deadline = %v (want true)", got[0], got[1])
	}
	ctx, cancel := InitContext(context.Background())
	defer cancel()
	if _, ok := ctx.Deadline(); ok {
		t.Fatal("InitContext outside a build added a deadline")
	}
}
