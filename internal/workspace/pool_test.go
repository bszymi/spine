package workspace

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// multiConfigResolver is a test-only resolver that knows about several
// workspace IDs. It returns a fresh *Config from the embedded map on
// each Resolve call so concurrent tests don't share mutable state.
type multiConfigResolver struct {
	configs map[string]Config
}

func (r *multiConfigResolver) Resolve(_ context.Context, id string) (*Config, error) {
	cfg, ok := r.configs[id]
	if !ok {
		return nil, ErrWorkspaceNotFound
	}
	return &cfg, nil
}

func (r *multiConfigResolver) List(_ context.Context) ([]Config, error) {
	out := make([]Config, 0, len(r.configs))
	for _, c := range r.configs {
		out = append(out, c)
	}
	return out, nil
}

func TestServicePool_Get(t *testing.T) {
	t.Setenv("SPINE_WORKSPACE_ID", "ws-pool")
	t.Setenv("SPINE_DATABASE_URL", "")
	t.Setenv("SPINE_REPO_PATH", ".")

	ctx := context.Background()
	provider := NewFileProvider()
	pool := NewServicePool(ctx, provider, PoolConfig{IdleTimeout: 5 * time.Second})
	defer pool.Close()

	// First get initializes the service set.
	ss, err := pool.Get(ctx, "ws-pool")
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	if ss == nil {
		t.Fatal("expected non-nil service set")
	}
	if ss.Config.ID != "ws-pool" {
		t.Errorf("expected workspace ID %q, got %q", "ws-pool", ss.Config.ID)
	}
	if ss.GitClient == nil {
		t.Error("expected non-nil GitClient")
	}
	if ss.Artifacts == nil {
		t.Error("expected non-nil Artifacts")
	}

	// Second get returns the same cached set.
	ss2, err := pool.Get(ctx, "ws-pool")
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if ss != ss2 {
		t.Error("expected same service set from cache")
	}

	if pool.ActiveCount() != 1 {
		t.Errorf("expected 1 active workspace, got %d", pool.ActiveCount())
	}
}

func TestServicePool_Get_NotFound(t *testing.T) {
	t.Setenv("SPINE_WORKSPACE_ID", "ws-pool")
	t.Setenv("SPINE_DATABASE_URL", "")
	t.Setenv("SPINE_REPO_PATH", ".")

	ctx := context.Background()
	provider := NewFileProvider()
	pool := NewServicePool(ctx, provider, PoolConfig{})
	defer pool.Close()

	_, err := pool.Get(ctx, "wrong-id")
	if err == nil {
		t.Fatal("expected error for unknown workspace")
	}
}

func TestServicePool_EvictIdle(t *testing.T) {
	t.Setenv("SPINE_WORKSPACE_ID", "ws-evict")
	t.Setenv("SPINE_DATABASE_URL", "")
	t.Setenv("SPINE_REPO_PATH", ".")

	ctx := context.Background()
	provider := NewFileProvider()
	pool := NewServicePool(ctx, provider, PoolConfig{IdleTimeout: 1 * time.Millisecond})
	defer pool.Close()

	ss, err := pool.Get(ctx, "ws-evict")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	_ = ss
	if pool.ActiveCount() != 1 {
		t.Fatalf("expected 1 active, got %d", pool.ActiveCount())
	}

	// Release the reference so eviction can proceed.
	pool.Release("ws-evict")

	// Wait for idle timeout.
	time.Sleep(5 * time.Millisecond)
	pool.EvictIdle()

	if pool.ActiveCount() != 0 {
		t.Errorf("expected 0 active after eviction, got %d", pool.ActiveCount())
	}
}

func TestServicePool_Builder_Called(t *testing.T) {
	t.Setenv("SPINE_WORKSPACE_ID", "ws-builder")
	t.Setenv("SPINE_DATABASE_URL", "")
	t.Setenv("SPINE_REPO_PATH", ".")

	var builderCalled bool
	var builderSS *ServiceSet

	ctx := context.Background()
	provider := NewFileProvider()
	pool := NewServicePool(ctx, provider, PoolConfig{
		Builder: func(_ context.Context, ss *ServiceSet) error {
			builderCalled = true
			builderSS = ss
			return nil
		},
	})
	defer pool.Close()

	ss, err := pool.Get(ctx, "ws-builder")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if !builderCalled {
		t.Fatal("expected builder to be called")
	}
	if builderSS != ss {
		t.Error("builder received different ServiceSet than the one returned")
	}
}

func TestServicePool_Builder_CanExtendServiceSet(t *testing.T) {
	t.Setenv("SPINE_WORKSPACE_ID", "ws-extend")
	t.Setenv("SPINE_DATABASE_URL", "")
	t.Setenv("SPINE_REPO_PATH", ".")

	ctx := context.Background()
	provider := NewFileProvider()
	pool := NewServicePool(ctx, provider, PoolConfig{
		Builder: func(_ context.Context, ss *ServiceSet) error {
			ss.CommitRetryFn = func(_ context.Context, _ string) error { return nil }
			ss.RunStarter = "test-run-starter"
			return nil
		},
	})
	defer pool.Close()

	ss, err := pool.Get(ctx, "ws-extend")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if ss.CommitRetryFn == nil {
		t.Error("expected CommitRetryFn to be set by builder")
	}
	if ss.RunStarter != "test-run-starter" {
		t.Error("expected RunStarter to be set by builder")
	}
}

func TestServicePool_Builder_Error_PreventsCreation(t *testing.T) {
	t.Setenv("SPINE_WORKSPACE_ID", "ws-fail")
	t.Setenv("SPINE_DATABASE_URL", "")
	t.Setenv("SPINE_REPO_PATH", ".")

	ctx := context.Background()
	provider := NewFileProvider()
	pool := NewServicePool(ctx, provider, PoolConfig{
		Builder: func(_ context.Context, _ *ServiceSet) error {
			return fmt.Errorf("builder failed")
		},
	})
	defer pool.Close()

	_, err := pool.Get(ctx, "ws-fail")
	if err == nil {
		t.Fatal("expected error when builder fails")
	}
	if !strings.Contains(err.Error(), "builder failed") {
		t.Errorf("expected builder error in message, got: %v", err)
	}

	if pool.ActiveCount() != 0 {
		t.Error("failed builder should not leave an entry in the pool")
	}
}

func TestServicePool_NilBuilder_Works(t *testing.T) {
	t.Setenv("SPINE_WORKSPACE_ID", "ws-nobuilder")
	t.Setenv("SPINE_DATABASE_URL", "")
	t.Setenv("SPINE_REPO_PATH", ".")

	ctx := context.Background()
	provider := NewFileProvider()
	pool := NewServicePool(ctx, provider, PoolConfig{})
	defer pool.Close()

	ss, err := pool.Get(ctx, "ws-nobuilder")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ss == nil {
		t.Fatal("expected non-nil service set without builder")
	}
}

func TestBuildServiceSet_NoStore_NilValidatorAndDivergence(t *testing.T) {
	t.Setenv("SPINE_WORKSPACE_ID", "ws-nostore")
	t.Setenv("SPINE_DATABASE_URL", "")
	t.Setenv("SPINE_REPO_PATH", ".")

	ctx := context.Background()
	cfg := Config{ID: "ws-nostore", RepoPath: "."}

	ss, err := buildServiceSet(ctx, cfg, nil, nil)
	if err != nil {
		t.Fatalf("buildServiceSet: %v", err)
	}
	defer ss.close()

	if ss.Validator != nil {
		t.Error("expected nil Validator when no database URL")
	}
	if ss.Divergence != nil {
		t.Error("expected nil Divergence when no database URL")
	}
}

func TestServicePool_Evict_NoRefs(t *testing.T) {
	t.Setenv("SPINE_WORKSPACE_ID", "ws-evict-noref")
	t.Setenv("SPINE_DATABASE_URL", "")
	t.Setenv("SPINE_REPO_PATH", ".")

	ctx := context.Background()
	provider := NewFileProvider()
	pool := NewServicePool(ctx, provider, PoolConfig{})
	defer pool.Close()

	_, err := pool.Get(ctx, "ws-evict-noref")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	// Release the reference so eviction can proceed immediately.
	pool.Release("ws-evict-noref")

	if pool.ActiveCount() != 1 {
		t.Fatalf("expected 1 active before evict, got %d", pool.ActiveCount())
	}

	// Evict with no active refs → immediate removal.
	pool.Evict("ws-evict-noref")

	if pool.ActiveCount() != 0 {
		t.Errorf("expected 0 active after evict, got %d", pool.ActiveCount())
	}
}

func TestServicePool_Evict_WithRefs_DeferredClose(t *testing.T) {
	t.Setenv("SPINE_WORKSPACE_ID", "ws-deferred")
	t.Setenv("SPINE_DATABASE_URL", "")
	t.Setenv("SPINE_REPO_PATH", ".")

	ctx := context.Background()
	provider := NewFileProvider()
	pool := NewServicePool(ctx, provider, PoolConfig{})
	defer pool.Close()

	// Acquire reference (refCount=1).
	_, err := pool.Get(ctx, "ws-deferred")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	// Evict while ref is held → marks for deferred close.
	pool.Evict("ws-deferred")

	// Still active because a ref is held.
	if pool.ActiveCount() != 1 {
		t.Errorf("expected 1 active (deferred), got %d", pool.ActiveCount())
	}

	// Release the last ref → triggers deferred close.
	pool.Release("ws-deferred")

	if pool.ActiveCount() != 0 {
		t.Errorf("expected 0 active after deferred close, got %d", pool.ActiveCount())
	}
}

func TestServicePool_Evict_NonExistent(t *testing.T) {
	ctx := context.Background()
	provider := NewFileProvider()
	pool := NewServicePool(ctx, provider, PoolConfig{})
	defer pool.Close()

	// Evicting a workspace that's not in the pool should be a no-op.
	pool.Evict("does-not-exist")
	if pool.ActiveCount() != 0 {
		t.Errorf("expected 0 active, got %d", pool.ActiveCount())
	}
}

func TestServicePool_Concurrent_SameWorkspace_SingleInit(t *testing.T) {
	t.Setenv("SPINE_REPO_PATH", ".")

	ctx := context.Background()
	resolver := &multiConfigResolver{configs: map[string]Config{
		"ws-single": {ID: "ws-single", RepoPath: ".", Status: StatusActive},
	}}

	var builderCalls int32
	pool := NewServicePool(ctx, resolver, PoolConfig{
		Builder: func(_ context.Context, _ *ServiceSet) error {
			atomic.AddInt32(&builderCalls, 1)
			// Give concurrent callers time to pile up while init is in flight.
			time.Sleep(50 * time.Millisecond)
			return nil
		},
	})
	defer pool.Close()

	const n = 10
	results := make([]*ServiceSet, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			ss, err := pool.Get(ctx, "ws-single")
			results[i], errs[i] = ss, err
		}(i)
	}
	wg.Wait()

	if got := atomic.LoadInt32(&builderCalls); got != 1 {
		t.Fatalf("expected exactly one builder invocation, got %d", got)
	}
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d got error: %v", i, errs[i])
		}
		if results[i] == nil {
			t.Fatalf("goroutine %d got nil service set", i)
		}
		if results[i] != results[0] {
			t.Errorf("goroutine %d returned a different *ServiceSet than goroutine 0", i)
		}
	}
	if got := pool.RefCount("ws-single"); got != n {
		t.Errorf("expected refCount=%d after n Gets, got %d", n, got)
	}
}

func TestServicePool_Concurrent_DifferentWorkspaces_ParallelInit(t *testing.T) {
	t.Setenv("SPINE_REPO_PATH", ".")

	ctx := context.Background()
	resolver := &multiConfigResolver{configs: map[string]Config{
		"ws-slow": {ID: "ws-slow", RepoPath: ".", Status: StatusActive},
		"ws-fast": {ID: "ws-fast", RepoPath: ".", Status: StatusActive},
	}}

	// slowStarted fires as soon as ws-slow's builder has started; slowHold
	// is released once ws-fast has finished so we can measure whether
	// ws-fast initialized without waiting on ws-slow.
	slowStarted := make(chan struct{})
	slowHold := make(chan struct{})

	pool := NewServicePool(ctx, resolver, PoolConfig{
		Builder: func(_ context.Context, ss *ServiceSet) error {
			if ss.Config.ID == "ws-slow" {
				close(slowStarted)
				<-slowHold
			}
			return nil
		},
	})
	defer pool.Close()

	// Kick off the slow init.
	slowDone := make(chan struct{})
	var slowSS *ServiceSet
	var slowErr error
	go func() {
		slowSS, slowErr = pool.Get(ctx, "ws-slow")
		close(slowDone)
	}()

	// Wait until we're sure the slow builder is executing — i.e. the
	// pool has released the mutex and entered buildServiceSet.
	select {
	case <-slowStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("slow builder never started")
	}

	// A Get on a different workspace must complete without waiting on
	// ws-slow. If the pool still held the mutex during buildServiceSet,
	// this call would block until slowHold is closed.
	fastDone := make(chan struct{})
	var fastErr error
	go func() {
		_, fastErr = pool.Get(ctx, "ws-fast")
		close(fastDone)
	}()
	select {
	case <-fastDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("ws-fast Get blocked on ws-slow initialization — pool mutex appears held during buildServiceSet")
	}
	if fastErr != nil {
		t.Fatalf("ws-fast Get: %v", fastErr)
	}

	// Unblock ws-slow and make sure it also completes cleanly.
	close(slowHold)
	<-slowDone
	if slowErr != nil {
		t.Fatalf("ws-slow Get: %v", slowErr)
	}
	if slowSS == nil {
		t.Fatal("expected non-nil ws-slow service set")
	}
}

func TestServicePool_FailedInit_AllowsRetry(t *testing.T) {
	t.Setenv("SPINE_REPO_PATH", ".")

	ctx := context.Background()
	resolver := &multiConfigResolver{configs: map[string]Config{
		"ws-retry": {ID: "ws-retry", RepoPath: ".", Status: StatusActive},
	}}

	var calls int32
	pool := NewServicePool(ctx, resolver, PoolConfig{
		Builder: func(_ context.Context, _ *ServiceSet) error {
			if atomic.AddInt32(&calls, 1) == 1 {
				return fmt.Errorf("simulated first-time failure")
			}
			return nil
		},
	})
	defer pool.Close()

	if _, err := pool.Get(ctx, "ws-retry"); err == nil {
		t.Fatal("expected first Get to fail")
	}
	if pool.ActiveCount() != 0 {
		t.Errorf("failed init must not leave an entry behind; active=%d", pool.ActiveCount())
	}

	ss, err := pool.Get(ctx, "ws-retry")
	if err != nil {
		t.Fatalf("retry Get: %v", err)
	}
	if ss == nil {
		t.Fatal("retry Get returned nil service set")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("expected builder to be re-invoked on retry (calls=2), got %d", got)
	}
}

func TestServicePool_ConcurrentFailedInit_SharedError(t *testing.T) {
	t.Setenv("SPINE_REPO_PATH", ".")

	ctx := context.Background()
	resolver := &multiConfigResolver{configs: map[string]Config{
		"ws-fail": {ID: "ws-fail", RepoPath: ".", Status: StatusActive},
	}}

	var calls int32
	pool := NewServicePool(ctx, resolver, PoolConfig{
		Builder: func(_ context.Context, _ *ServiceSet) error {
			atomic.AddInt32(&calls, 1)
			time.Sleep(20 * time.Millisecond)
			return fmt.Errorf("init failed")
		},
	})
	defer pool.Close()

	const n = 5
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			_, errs[i] = pool.Get(ctx, "ws-fail")
		}(i)
	}
	wg.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("expected exactly one builder invocation for concurrent failed init, got %d", got)
	}
	for i, err := range errs {
		if err == nil {
			t.Errorf("goroutine %d: expected error, got nil", i)
			continue
		}
		if !strings.Contains(err.Error(), "init failed") {
			t.Errorf("goroutine %d: expected shared init error, got: %v", i, err)
		}
	}
	if pool.ActiveCount() != 0 {
		t.Errorf("failed init must not leave an entry behind; active=%d", pool.ActiveCount())
	}
}

func TestServicePool_Close_DuringInit(t *testing.T) {
	t.Setenv("SPINE_REPO_PATH", ".")

	ctx := context.Background()
	resolver := &multiConfigResolver{configs: map[string]Config{
		"ws-close-init": {ID: "ws-close-init", RepoPath: ".", Status: StatusActive},
	}}

	builderStarted := make(chan struct{})
	builderRelease := make(chan struct{})
	pool := NewServicePool(ctx, resolver, PoolConfig{
		Builder: func(_ context.Context, _ *ServiceSet) error {
			close(builderStarted)
			<-builderRelease
			return nil
		},
	})

	getDone := make(chan struct{})
	var getErr error
	go func() {
		_, getErr = pool.Get(ctx, "ws-close-init")
		close(getDone)
	}()

	select {
	case <-builderStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("builder never started")
	}

	// Close while init is in flight. Close must not wait for the
	// builder, so it should return promptly.
	closeDone := make(chan struct{})
	go func() {
		pool.Close()
		close(closeDone)
	}()
	select {
	case <-closeDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Close blocked waiting for in-flight init")
	}

	// Let the builder finish. Get should return a closed-pool error
	// and no entries should leak.
	close(builderRelease)
	select {
	case <-getDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Get never returned after Close")
	}
	if getErr == nil {
		t.Fatal("expected error from Get racing with Close")
	}
	if !strings.Contains(getErr.Error(), "closed") {
		t.Errorf("expected closed-pool error, got: %v", getErr)
	}
	if pool.ActiveCount() != 0 {
		t.Errorf("expected 0 active entries after Close; got %d", pool.ActiveCount())
	}
}

func TestServicePool_Evict_DuringInit_DeferredCloseOnRelease(t *testing.T) {
	t.Setenv("SPINE_REPO_PATH", ".")

	ctx := context.Background()
	resolver := &multiConfigResolver{configs: map[string]Config{
		"ws-evict-init": {ID: "ws-evict-init", RepoPath: ".", Status: StatusActive},
	}}

	builderStarted := make(chan struct{})
	builderRelease := make(chan struct{})
	pool := NewServicePool(ctx, resolver, PoolConfig{
		Builder: func(_ context.Context, _ *ServiceSet) error {
			close(builderStarted)
			<-builderRelease
			return nil
		},
	})
	defer pool.Close()

	getDone := make(chan struct{})
	var ss *ServiceSet
	var getErr error
	go func() {
		ss, getErr = pool.Get(ctx, "ws-evict-init")
		close(getDone)
	}()

	<-builderStarted

	// Evict while init is in flight. With refCount=1 held by the
	// initiator, this should mark the entry for deferred close rather
	// than touching the (still nil) service set.
	pool.Evict("ws-evict-init")

	close(builderRelease)
	<-getDone
	if getErr != nil {
		t.Fatalf("Get: %v", getErr)
	}
	if ss == nil {
		t.Fatal("expected non-nil service set even with concurrent Evict")
	}

	// Release should now trigger the deferred close path and remove the
	// entry from the pool.
	pool.Release("ws-evict-init")
	if pool.ActiveCount() != 0 {
		t.Errorf("expected 0 active after Release following Evict-during-init; got %d", pool.ActiveCount())
	}
}

func TestServicePool_Close(t *testing.T) {
	t.Setenv("SPINE_WORKSPACE_ID", "ws-close")
	t.Setenv("SPINE_DATABASE_URL", "")
	t.Setenv("SPINE_REPO_PATH", ".")

	ctx := context.Background()
	provider := NewFileProvider()
	pool := NewServicePool(ctx, provider, PoolConfig{})

	_, err := pool.Get(ctx, "ws-close")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	pool.Close()

	if pool.ActiveCount() != 0 {
		t.Errorf("expected 0 active after close, got %d", pool.ActiveCount())
	}

	// Get after close should fail.
	_, err = pool.Get(ctx, "ws-close")
	if err == nil {
		t.Fatal("expected error after pool closed")
	}
}
