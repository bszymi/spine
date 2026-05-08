package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
	provider := NewFileProvider(nil)
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
	provider := NewFileProvider(nil)
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
	provider := NewFileProvider(nil)
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
	provider := NewFileProvider(nil)
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
	provider := NewFileProvider(nil)
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
	provider := NewFileProvider(nil)
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
	provider := NewFileProvider(nil)
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

	ss, err := buildServiceSet(ctx, cfg, nil, nil, nil, PoolPolicy{}, "")
	if err != nil {
		t.Fatalf("buildServiceSet: %v", err)
	}
	defer ss.close("shutdown")

	if ss.Validator != nil {
		t.Error("expected nil Validator when no database URL")
	}
	if ss.Divergence != nil {
		t.Error("expected nil Divergence when no database URL")
	}
	// INIT-014 EPIC-003 TASK-003: every workspace must have a non-nil
	// repository registry and Git client pool, even when no store is
	// available. Governance reads otherwise have no entry point for
	// the multi-repo abstractions, which defeats single-repo
	// backward compatibility (the pool degenerates to "primary
	// only" but must always exist).
	if ss.Registry == nil {
		t.Error("expected non-nil Registry on ServiceSet")
	}
	if ss.GitPool == nil {
		t.Error("expected non-nil GitPool on ServiceSet")
	}
	if ss.GitPool != nil && ss.GitPool.PrimaryClient() == nil {
		t.Error("GitPool.PrimaryClient must return a usable client")
	}
}

// TestPerWorkspaceCodeRepoBase_EmptyBaseDisables confirms an empty
// deployment base returns "" regardless of workspace ID — dev /
// single-workspace setups without containment keep working.
func TestPerWorkspaceCodeRepoBase_EmptyBaseDisables(t *testing.T) {
	got, err := perWorkspaceCodeRepoBase("", "acme")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty result, got %q", got)
	}
}

// TestPerWorkspaceCodeRepoBase_NarrowsToWorkspaceSubtree confirms two
// workspace IDs under the same deployment base produce different
// narrowed paths. This is the per-workspace isolation invariant
// codex pass 3 flagged: workspace A and B share the same configured
// root but their pools must enforce different boundaries.
func TestPerWorkspaceCodeRepoBase_NarrowsToWorkspaceSubtree(t *testing.T) {
	deployment := t.TempDir()
	gotA, err := perWorkspaceCodeRepoBase(deployment, "acme")
	if err != nil {
		t.Fatalf("acme: %v", err)
	}
	gotB, err := perWorkspaceCodeRepoBase(deployment, "globex")
	if err != nil {
		t.Fatalf("globex: %v", err)
	}
	if gotA == gotB {
		t.Fatalf("expected distinct narrowed paths, both = %q", gotA)
	}
	if gotA != filepath.Join(deployment, "acme") {
		t.Fatalf("acme narrowed = %q, want %q", gotA, filepath.Join(deployment, "acme"))
	}
	if gotB != filepath.Join(deployment, "globex") {
		t.Fatalf("globex narrowed = %q, want %q", gotB, filepath.Join(deployment, "globex"))
	}
}

// TestPerWorkspaceCodeRepoBase_RejectsRegularFile is the regression
// bait for codex pass 8: if `<base>/<workspaceID>` already exists as
// a regular file, EvalSymlinks would still succeed and the helper
// would silently accept it as the workspace base — every later
// clone/open would then fail with ENOTDIR. Fail fast at startup
// instead so the operator sees a clear "not a directory" error.
func TestPerWorkspaceCodeRepoBase_RejectsRegularFile(t *testing.T) {
	deployment := t.TempDir()
	regular := filepath.Join(deployment, "acme")
	if err := os.WriteFile(regular, []byte("not a dir"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := perWorkspaceCodeRepoBase(deployment, "acme"); err == nil {
		t.Fatal("expected error when workspace base path is a regular file")
	}
}

// TestPerWorkspaceCodeRepoBase_RejectsTraversalShapedID is the
// regression bait for codex pass 7: a workspace ID like ".." would
// otherwise let filepath.Join normalize both the narrowed path and
// the expected check to the parent of the deployment base — escape
// achieved without tripping the EvalSymlinks compare. Validate the
// workspace ID through the package-wide allowlist before any join.
func TestPerWorkspaceCodeRepoBase_RejectsTraversalShapedID(t *testing.T) {
	deployment := t.TempDir()
	cases := []string{"", "..", "../sibling", ".", "/abs", "ws..bad", "ws/with/slash"}
	for _, id := range cases {
		t.Run(id, func(t *testing.T) {
			if _, err := perWorkspaceCodeRepoBase(deployment, id); err == nil {
				t.Fatalf("expected error for invalid workspace ID %q", id)
			}
		})
	}
}

// TestPerWorkspaceCodeRepoBase_RejectsSymlinkedSubdirEscape is the
// regression bait for codex pass 4: when the workspace's subdirectory
// is itself a symlink that points outside the deployment root (e.g.
// `<base>/acme -> /etc`), the gitpool's EvalSymlinks-based
// validateRepoBase would otherwise accept LocalPaths inside the
// symlink's target — losing the deployment-root invariant. Refuse the
// workspace pool init outright in that case.
func TestPerWorkspaceCodeRepoBase_RejectsSymlinkedSubdirEscape(t *testing.T) {
	root := t.TempDir()
	deployment := filepath.Join(root, "deployment")
	if err := os.Mkdir(deployment, 0o700); err != nil {
		t.Fatalf("mkdir deployment: %v", err)
	}
	// Place the symlink target outside the deployment root so a join
	// of `<deployment>/acme` resolves to `<elsewhere>` after EvalSymlinks.
	elsewhere := filepath.Join(root, "elsewhere")
	if err := os.Mkdir(elsewhere, 0o700); err != nil {
		t.Fatalf("mkdir elsewhere: %v", err)
	}
	link := filepath.Join(deployment, "acme")
	if err := os.Symlink(elsewhere, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	_, err := perWorkspaceCodeRepoBase(deployment, "acme")
	if err == nil {
		t.Fatal("expected error when workspace subdir resolves outside deployment root")
	}
}

// TestPerWorkspaceCodeRepoBase_RejectsSymlinkedSiblingTree is the
// regression bait for codex pass 5: even when `<base>/acme` is a
// symlink pointing at another workspace's subtree (still under the
// deployment root, e.g. `<base>/acme -> <base>/globex`), pool init
// must refuse. A prefix-check against the deployment root would let
// this pass and allow workspace A's pool to validate and serve paths
// inside workspace B's tree. The strict-equality match against
// `EvalSymlinks(base)/<workspaceID>` catches this.
func TestPerWorkspaceCodeRepoBase_RejectsSymlinkedSiblingTree(t *testing.T) {
	deployment := t.TempDir()
	globex := filepath.Join(deployment, "globex")
	if err := os.Mkdir(globex, 0o700); err != nil {
		t.Fatalf("mkdir globex: %v", err)
	}
	acme := filepath.Join(deployment, "acme")
	if err := os.Symlink(globex, acme); err != nil {
		t.Fatalf("symlink acme -> globex: %v", err)
	}
	_, err := perWorkspaceCodeRepoBase(deployment, "acme")
	if err == nil {
		t.Fatal("expected error when workspace subdir symlinks into a sibling tree")
	}
}

// TestPerWorkspaceCodeRepoBase_AcceptsRealSubdir confirms the static
// check does NOT block the legitimate case (real subdirectory under
// the deployment root, both present at startup).
func TestPerWorkspaceCodeRepoBase_AcceptsRealSubdir(t *testing.T) {
	deployment := t.TempDir()
	if err := os.Mkdir(filepath.Join(deployment, "acme"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	got, err := perWorkspaceCodeRepoBase(deployment, "acme")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != filepath.Join(deployment, "acme") {
		t.Fatalf("unexpected result %q", got)
	}
}

// TestPerWorkspaceCodeRepoBase_CreatesMissingWorkspaceSubdir covers
// the typical fresh-deployment case: the workspace subdir doesn't
// exist yet at startup. The helper creates it (MkdirAll 0700) so we
// own a real directory and the subsequent EvalSymlinks check passes.
// This closes the symlink race that codex pass 6 flagged: if the
// directory were left absent, an attacker could create it later as a
// symlink to widen gitpool's boundary at first clone.
func TestPerWorkspaceCodeRepoBase_CreatesMissingWorkspaceSubdir(t *testing.T) {
	deployment := t.TempDir()
	narrowed := filepath.Join(deployment, "acme")
	got, err := perWorkspaceCodeRepoBase(deployment, "acme")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != narrowed {
		t.Fatalf("unexpected result %q", got)
	}
	info, err := os.Lstat(narrowed)
	if err != nil {
		t.Fatalf("workspace subdir was not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("expected real directory, got mode %v", info.Mode())
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("expected real directory, got symlink — would defeat the containment guarantee")
	}
}

// TestPerWorkspaceCodeRepoBase_FailsWhenDeploymentBaseAbsent confirms
// we surface a clear startup error when the operator has misconfigured
// SPINE_CODE_REPO_BASE to a path that doesn't exist at all. Better to
// fail fast than to silently create a tree at an unintended location.
func TestPerWorkspaceCodeRepoBase_FailsWhenDeploymentBaseAbsent(t *testing.T) {
	deployment := filepath.Join(t.TempDir(), "missing", "deployment")
	if _, err := perWorkspaceCodeRepoBase(deployment, "acme"); err == nil {
		t.Fatal("expected error when deployment base is absent")
	}
}

func TestServicePool_Evict_NoRefs(t *testing.T) {
	t.Setenv("SPINE_WORKSPACE_ID", "ws-evict-noref")
	t.Setenv("SPINE_DATABASE_URL", "")
	t.Setenv("SPINE_REPO_PATH", ".")

	ctx := context.Background()
	provider := NewFileProvider(nil)
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
	provider := NewFileProvider(nil)
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
	provider := NewFileProvider(nil)
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

// slowResolver lets one workspace's first Resolve call block on a
// release channel while every other call (including subsequent
// resolves of slowID) returns immediately. It exists to prove the
// two TASK-015 contracts: (1) the pool mutex is not held across
// resolver.Resolve, so a slow upstream binding lookup for workspace
// A does not stall Get on unrelated workspace B; (2) when an Evict
// for workspaceID races a cold Get's Resolve, Get retries with a
// fresh Resolve so the pre-invalidation cfg is never cached.
type slowResolver struct {
	configs   map[string]Config
	slowID    string
	enterOnce sync.Once
	entered   chan struct{} // closed when slow Resolve enters its wait
	release   chan struct{} // closed by the test to unblock slow Resolve

	mu        sync.Mutex
	callCount map[string]int // per-ID Resolve invocation count
}

func (r *slowResolver) Resolve(ctx context.Context, id string) (*Config, error) {
	r.mu.Lock()
	if r.callCount == nil {
		r.callCount = map[string]int{}
	}
	r.callCount[id]++
	first := r.callCount[id] == 1
	r.mu.Unlock()

	// Only the FIRST call for slowID blocks. Subsequent calls
	// (including retries from the same Get) complete immediately so
	// the test can assert the retry path was actually taken.
	if id == r.slowID && first {
		r.enterOnce.Do(func() { close(r.entered) })
		select {
		case <-r.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	cfg, ok := r.configs[id]
	if !ok {
		return nil, ErrWorkspaceNotFound
	}
	return &cfg, nil
}

func (r *slowResolver) calls(id string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.callCount[id]
}

func (r *slowResolver) List(_ context.Context) ([]Config, error) {
	out := make([]Config, 0, len(r.configs))
	for _, c := range r.configs {
		out = append(out, c)
	}
	return out, nil
}

// TestServicePool_Get_SlowResolveDoesNotBlockOtherWorkspaces locks
// INIT-022 EPIC-001 TASK-015: Get must drop p.mu around
// resolver.Resolve so that a slow platform-binding lookup for
// workspace A cannot stall a Get for unrelated workspace B. Before
// the fix, Resolve ran while holding p.mu and this test's fast Get
// would wait on the release channel.
func TestServicePool_Get_SlowResolveDoesNotBlockOtherWorkspaces(t *testing.T) {
	t.Setenv("SPINE_REPO_PATH", ".")

	ctx := context.Background()
	resolver := &slowResolver{
		configs: map[string]Config{
			"ws-slow": {ID: "ws-slow", RepoPath: ".", Status: StatusActive},
			"ws-fast": {ID: "ws-fast", RepoPath: ".", Status: StatusActive},
		},
		slowID:  "ws-slow",
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	pool := NewServicePool(ctx, resolver, PoolConfig{IdleCheckInterval: -1})
	defer pool.Close()

	released := false
	releaseSlow := func() {
		if !released {
			close(resolver.release)
			released = true
		}
	}
	defer releaseSlow()

	slowDone := make(chan error, 1)
	go func() {
		_, err := pool.Get(ctx, "ws-slow")
		slowDone <- err
	}()

	// Wait until the slow Resolve is in flight before issuing the fast
	// Get; otherwise the test would race the slow goroutine's first
	// Lock() and report a false negative.
	select {
	case <-resolver.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("slow Resolve did not enter within 2s")
	}

	// Fast Get must complete promptly. If the pool mutex were held
	// across Resolve, this call would block until releaseSlow() runs.
	fastCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ss, err := pool.Get(fastCtx, "ws-fast")
	if err != nil {
		t.Fatalf("fast Get blocked behind slow Resolve: %v", err)
	}
	if ss == nil || ss.Config.ID != "ws-fast" {
		t.Fatalf("fast Get returned unexpected ServiceSet: %+v", ss)
	}

	// Release the slow Resolve and confirm it also completes.
	releaseSlow()
	select {
	case err := <-slowDone:
		if err != nil {
			t.Fatalf("slow Get failed after release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("slow Get did not complete within 5s after release")
	}
}

// TestServicePool_Evict_DuringColdResolve_RetriesResolve locks
// INIT-022 EPIC-001 TASK-015's invalidation-race contract: an Evict
// for workspaceID that fires while a cold Get's resolver.Resolve is
// in flight must not be silently lost just because no cache entry
// exists yet. The pool bumps an eviction generation under
// activeResolves[workspaceID]>0; the Get observes the bump on its
// post-Resolve recheck and retries with a fresh Resolve so the
// pre-invalidation cfg is never cached. Without this, dropping the
// mutex around Resolve would leave a stale cfg cached until idle
// eviction or the next Evict.
func TestServicePool_Evict_DuringColdResolve_RetriesResolve(t *testing.T) {
	t.Setenv("SPINE_REPO_PATH", ".")

	ctx := context.Background()
	resolver := &slowResolver{
		configs: map[string]Config{
			"ws-evict-race": {ID: "ws-evict-race", RepoPath: ".", Status: StatusActive},
		},
		slowID:  "ws-evict-race",
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	pool := NewServicePool(ctx, resolver, PoolConfig{IdleCheckInterval: -1})
	defer pool.Close()

	released := false
	releaseSlow := func() {
		if !released {
			close(resolver.release)
			released = true
		}
	}
	defer releaseSlow()

	type result struct {
		ss  *ServiceSet
		err error
	}
	getDone := make(chan result, 1)
	go func() {
		ss, err := pool.Get(ctx, "ws-evict-race")
		getDone <- result{ss, err}
	}()

	// Wait for the slow Resolve to be in flight before issuing the
	// concurrent Evict — that guarantees Evict sees no entry but
	// activeResolves>0, so it bumps evictGen rather than no-op'ing.
	select {
	case <-resolver.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("slow Resolve did not enter within 2s")
	}

	pool.Evict("ws-evict-race")

	// Let the cold Get's first Resolve return. The post-Resolve
	// recheck observes the gen bump and retries; the retry's
	// Resolve completes immediately (slowResolver only blocks the
	// first call) and the fresh cfg is cached.
	releaseSlow()

	res := <-getDone
	if res.err != nil {
		t.Fatalf("Get during Evict race: %v", res.err)
	}
	if res.ss == nil {
		t.Fatal("expected non-nil services from Get")
	}

	// The retry contract: Resolve must have been called more than
	// once for this workspaceID. Exactly twice is the expected
	// shape (one raced call, one retry); a future tightening of
	// the bounded-retry policy could allow more, so accept >=2.
	if got := resolver.calls("ws-evict-race"); got < 2 {
		t.Fatalf("expected resolver to be retried after Evict race, call count=%d (want >=2)", got)
	}

	// The cached entry is the FRESH retry result — it is not
	// marked evicting and stays in the pool until idle eviction.
	// (Pre-TASK-015 with the mutex held across Resolve, the same
	// scenario would have cached the stale cfg and only torn it
	// down via the standard refCount/Release path; the gen+retry
	// approach is strictly stronger because the stale cfg never
	// makes it into the cache.)
	if got := pool.RefCount("ws-evict-race"); got != 1 {
		t.Fatalf("expected refCount=1 on fresh entry, got %d", got)
	}
	pool.Release("ws-evict-race")
	if got := pool.RefCount("ws-evict-race"); got != 0 {
		t.Fatalf("expected refCount=0 after Release, got %d", got)
	}
}

// evictTriggerResolver invokes pool.Evict on its Nth Resolve call,
// simulating an invalidation arriving while an unlocked Resolve is
// in flight. Used to exercise the hot-cache invalidation race —
// the entry-exists path of Evict must still bump evictGen so the
// in-flight Get retries instead of caching stale cfg.
type evictTriggerResolver struct {
	cfg     Config
	pool    *ServicePool
	mu      sync.Mutex
	calls   int
	evictOn int
}

func (r *evictTriggerResolver) Resolve(_ context.Context, _ string) (*Config, error) {
	r.mu.Lock()
	r.calls++
	n := r.calls
	r.mu.Unlock()
	if n == r.evictOn {
		r.pool.Evict(r.cfg.ID)
	}
	cfg := r.cfg
	return &cfg, nil
}

func (r *evictTriggerResolver) List(_ context.Context) ([]Config, error) {
	return []Config{r.cfg}, nil
}

func (r *evictTriggerResolver) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// TestServicePool_Evict_DuringResolve_HotCache_RetriesResolve
// locks INIT-022 EPIC-001 TASK-015's hot-cache invalidation
// contract: when an Evict races a Get whose unlocked Resolve is
// in flight AND a cached entry already exists for the same
// workspaceID, Evict must still bump evictGen so the in-flight
// Get's post-Resolve recheck observes the invalidation and
// retries. Without this, the hot-cache path of Evict would no-op
// the gen bump (because an entry was found) and the Get would
// proceed to cache a fresh entry built from the pre-invalidation
// cfg.
func TestServicePool_Evict_DuringResolve_HotCache_RetriesResolve(t *testing.T) {
	t.Setenv("SPINE_REPO_PATH", ".")

	ctx := context.Background()
	cfg := Config{ID: "ws-hot-evict", RepoPath: ".", Status: StatusActive}
	resolver := &evictTriggerResolver{cfg: cfg, evictOn: 2}
	pool := NewServicePool(ctx, resolver, PoolConfig{IdleCheckInterval: -1})
	defer pool.Close()
	resolver.pool = pool

	// Pre-populate the cache. Resolve #1 runs without triggering Evict
	// (evictOn=2). Release immediately so the entry has refCount=0.
	if _, err := pool.Get(ctx, "ws-hot-evict"); err != nil {
		t.Fatalf("pre-populate Get: %v", err)
	}
	pool.Release("ws-hot-evict")
	if got := pool.ActiveCount(); got != 1 {
		t.Fatalf("expected entry cached after pre-populate, ActiveCount=%d", got)
	}

	// Second Get triggers Evict on its Resolve (call #2). At that
	// point the cached entry from pre-populate exists with
	// refCount=0; Evict's hot-cache branch removes it AND must
	// bump evictGen so this Get's post-Resolve recheck observes
	// the race and retries (Resolve call #3, which does not
	// trigger Evict because evictOn==2 is past).
	if _, err := pool.Get(ctx, "ws-hot-evict"); err != nil {
		t.Fatalf("racing Get: %v", err)
	}

	// Three Resolve calls total: pre-populate + raced + retry.
	// Without the hot-cache bump, this would be two: pre-populate
	// + raced (no retry, stale cache).
	if got := resolver.callCount(); got != 3 {
		t.Fatalf("expected 3 resolver calls (pre-populate + raced + retry), got %d", got)
	}
}

// TestServicePool_Evict_NoActiveResolve_NoMapGrowth locks the
// activeResolves gate on Evict's gen bump: an Evict for a workspace
// with no in-flight Resolve must be a clean no-op and must NOT
// leave a permanent marker behind. This bounds memory growth in
// deployments that receive invalidations for unknown / cold /
// deleted workspace IDs (e.g. misrouted webhooks).
func TestServicePool_Evict_NoActiveResolve_NoMapGrowth(t *testing.T) {
	t.Setenv("SPINE_REPO_PATH", ".")

	ctx := context.Background()
	resolver := &multiConfigResolver{configs: map[string]Config{
		"ws-known": {ID: "ws-known", RepoPath: ".", Status: StatusActive},
	}}
	pool := NewServicePool(ctx, resolver, PoolConfig{IdleCheckInterval: -1})
	defer pool.Close()

	// Evict an unknown workspace ID 100 times. Each call must
	// be a clean no-op — no map entry, no panic, no error.
	for i := 0; i < 100; i++ {
		pool.Evict("ws-unknown")
	}

	// A subsequent Get for a DIFFERENT, known workspace must not
	// see any spurious evicting state from the prior Evicts.
	ss, err := pool.Get(ctx, "ws-known")
	if err != nil {
		t.Fatalf("Get(ws-known): %v", err)
	}
	if ss == nil {
		t.Fatal("expected non-nil services for ws-known")
	}
	if got := pool.RefCount("ws-known"); got != 1 {
		t.Fatalf("expected refCount=1 on fresh ws-known, got %d", got)
	}

	// And a Get for the previously-Evicted (but never resolved) ID
	// must succeed without inheriting any phantom evicting state —
	// the no-op contract for unknown IDs is preserved.
	resolver.configs["ws-unknown"] = Config{ID: "ws-unknown", RepoPath: ".", Status: StatusActive}
	ss2, err := pool.Get(ctx, "ws-unknown")
	if err != nil {
		t.Fatalf("Get(ws-unknown) after prior Evicts: %v", err)
	}
	if ss2 == nil {
		t.Fatal("expected non-nil services for ws-unknown")
	}
	if got := pool.RefCount("ws-unknown"); got != 1 {
		t.Fatalf("expected refCount=1 on fresh ws-unknown after prior Evicts, got %d", got)
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

func TestServicePool_Close_WakesWaitersOnInFlightEntry(t *testing.T) {
	t.Setenv("SPINE_REPO_PATH", ".")

	ctx := context.Background()
	resolver := &multiConfigResolver{configs: map[string]Config{
		"ws-close-wait": {ID: "ws-close-wait", RepoPath: ".", Status: StatusActive},
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

	// Initiator Get kicks off the in-flight init.
	initDone := make(chan struct{})
	go func() {
		_, _ = pool.Get(ctx, "ws-close-wait")
		close(initDone)
	}()
	<-builderStarted

	// A second Get parks on the in-flight entry's ready channel.
	waiterDone := make(chan struct{})
	var waiterErr error
	go func() {
		_, waiterErr = pool.Get(ctx, "ws-close-wait")
		close(waiterDone)
	}()
	// Give the waiter time to enter the select on ready.
	time.Sleep(20 * time.Millisecond)

	// Close should wake the waiter with a closed-pool error promptly,
	// without waiting on the (still-blocked) builder.
	closeDone := make(chan struct{})
	go func() {
		pool.Close()
		close(closeDone)
	}()
	select {
	case <-waiterDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Close did not wake Get waiter on in-flight entry")
	}
	if waiterErr == nil || !strings.Contains(waiterErr.Error(), "closed") {
		t.Errorf("expected closed-pool error for waiter, got: %v", waiterErr)
	}

	close(builderRelease)
	<-initDone
	<-closeDone
	if pool.ActiveCount() != 0 {
		t.Errorf("expected 0 active after close; got %d", pool.ActiveCount())
	}
}

func TestServicePool_Evict_DuringInit_ConcurrentGet_DoesNotOverwrite(t *testing.T) {
	t.Setenv("SPINE_REPO_PATH", ".")

	ctx := context.Background()
	resolver := &multiConfigResolver{configs: map[string]Config{
		"ws-evict-race": {ID: "ws-evict-race", RepoPath: ".", Status: StatusActive},
	}}

	builderStarted := make(chan struct{}, 1)
	builderRelease := make(chan struct{})
	var builderCalls int32
	pool := NewServicePool(ctx, resolver, PoolConfig{
		Builder: func(_ context.Context, _ *ServiceSet) error {
			call := atomic.AddInt32(&builderCalls, 1)
			if call == 1 {
				builderStarted <- struct{}{}
				<-builderRelease
			}
			return nil
		},
	})
	defer pool.Close()

	// First Get starts the slow init.
	firstDone := make(chan struct{})
	var firstSS *ServiceSet
	go func() {
		firstSS, _ = pool.Get(ctx, "ws-evict-race")
		close(firstDone)
	}()
	<-builderStarted

	// Evict while init is in flight — this marks the entry evicting.
	pool.Evict("ws-evict-race")

	// A concurrent Get for the same workspace must not overwrite the
	// in-flight entry; it should wait for the old entry to leave the
	// map before starting a fresh initialization.
	secondDone := make(chan struct{})
	var secondSS *ServiceSet
	var secondErr error
	go func() {
		secondSS, secondErr = pool.Get(ctx, "ws-evict-race")
		close(secondDone)
	}()
	// Give the second Get enough time to observe the evicting entry and
	// park on its gone channel.
	time.Sleep(20 * time.Millisecond)

	// Let the first init finish. The first Get now holds a ref; Release
	// should close the old entry and wake the second Get.
	close(builderRelease)
	<-firstDone
	if firstSS == nil {
		t.Fatal("first Get returned nil")
	}
	pool.Release("ws-evict-race")

	<-secondDone
	if secondErr != nil {
		t.Fatalf("second Get: %v", secondErr)
	}
	if secondSS == nil {
		t.Fatal("second Get returned nil")
	}
	if secondSS == firstSS {
		t.Error("second Get should have produced a fresh service set after Evict, got the same instance")
	}
	if got := atomic.LoadInt32(&builderCalls); got != 2 {
		t.Errorf("expected 2 builder calls (one per entry), got %d", got)
	}
	if got := pool.RefCount("ws-evict-race"); got != 1 {
		t.Errorf("expected refCount=1 on fresh entry, got %d", got)
	}
}

func TestServicePool_Close(t *testing.T) {
	t.Setenv("SPINE_WORKSPACE_ID", "ws-close")
	t.Setenv("SPINE_DATABASE_URL", "")
	t.Setenv("SPINE_REPO_PATH", ".")

	ctx := context.Background()
	provider := NewFileProvider(nil)
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

// countingResolver wraps multiConfigResolver and counts Resolve calls
// per workspace ID. Used to assert that Evict + Get triggers a fresh
// resolver lookup (re-resolve binding after invalidation).
type countingResolver struct {
	inner multiConfigResolver
	mu    sync.Mutex
	calls map[string]int
}

func (r *countingResolver) Resolve(ctx context.Context, id string) (*Config, error) {
	r.mu.Lock()
	if r.calls == nil {
		r.calls = make(map[string]int)
	}
	r.calls[id]++
	r.mu.Unlock()
	return r.inner.Resolve(ctx, id)
}

func (r *countingResolver) List(ctx context.Context) ([]Config, error) { return r.inner.List(ctx) }

func (r *countingResolver) callsFor(id string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[id]
}

// TestServicePool_BackgroundIdleEvictor verifies that a ServicePool
// constructed with a short IdleCheckInterval evicts idle workspaces
// without an explicit EvictIdle() call. ADR-012 requires that idle
// pools be closed automatically; this test pins the contract.
func TestServicePool_BackgroundIdleEvictor(t *testing.T) {
	t.Setenv("SPINE_REPO_PATH", ".")

	ctx := context.Background()
	resolver := &multiConfigResolver{configs: map[string]Config{
		"ws-bg": {ID: "ws-bg", RepoPath: ".", Status: StatusActive},
	}}
	pool := NewServicePool(ctx, resolver, PoolConfig{
		IdleTimeout:       10 * time.Millisecond,
		IdleCheckInterval: 5 * time.Millisecond,
	})
	defer pool.Close()

	if _, err := pool.Get(ctx, "ws-bg"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	pool.Release("ws-bg")
	if pool.ActiveCount() != 1 {
		t.Fatalf("expected 1 active before idle, got %d", pool.ActiveCount())
	}

	// The background loop should drop the entry without us calling
	// EvictIdle() ourselves.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if pool.ActiveCount() == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("background evictor did not remove idle entry (active=%d)", pool.ActiveCount())
}

// TestServicePool_BackgroundIdleEvictor_DisabledByNegativeInterval
// ensures unit-test callers can opt out of the background loop by
// passing IdleCheckInterval < 0 — otherwise tests that drive
// EvictIdle by hand would race the ticker.
func TestServicePool_BackgroundIdleEvictor_DisabledByNegativeInterval(t *testing.T) {
	t.Setenv("SPINE_REPO_PATH", ".")

	ctx := context.Background()
	resolver := &multiConfigResolver{configs: map[string]Config{
		"ws-disabled": {ID: "ws-disabled", RepoPath: ".", Status: StatusActive},
	}}
	pool := NewServicePool(ctx, resolver, PoolConfig{
		IdleTimeout:       1 * time.Millisecond,
		IdleCheckInterval: -1,
	})
	defer pool.Close()

	if _, err := pool.Get(ctx, "ws-disabled"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	pool.Release("ws-disabled")

	// Even after the idle timeout passes the entry must still be
	// present, because the loop is disabled and no one calls
	// EvictIdle.
	time.Sleep(20 * time.Millisecond)
	if pool.ActiveCount() != 1 {
		t.Fatalf("disabled background loop should not evict; got active=%d", pool.ActiveCount())
	}
}

// TestServicePool_Evict_IsolatesWorkspaces asserts the ADR-011
// guarantee that invalidating workspace A does not disturb B or C.
func TestServicePool_Evict_IsolatesWorkspaces(t *testing.T) {
	t.Setenv("SPINE_REPO_PATH", ".")

	ctx := context.Background()
	resolver := &multiConfigResolver{configs: map[string]Config{
		"acme":    {ID: "acme", RepoPath: ".", Status: StatusActive},
		"globex":  {ID: "globex", RepoPath: ".", Status: StatusActive},
		"initech": {ID: "initech", RepoPath: ".", Status: StatusActive},
	}}
	pool := NewServicePool(ctx, resolver, PoolConfig{IdleCheckInterval: -1})
	defer pool.Close()

	for _, id := range []string{"acme", "globex", "initech"} {
		if _, err := pool.Get(ctx, id); err != nil {
			t.Fatalf("Get(%s): %v", id, err)
		}
		pool.Release(id)
	}
	if pool.ActiveCount() != 3 {
		t.Fatalf("expected 3 active before evict, got %d", pool.ActiveCount())
	}

	pool.Evict("acme")

	if pool.ActiveCount() != 2 {
		t.Fatalf("expected 2 active after evicting acme, got %d", pool.ActiveCount())
	}
	if pool.RefCount("acme") != 0 {
		t.Errorf("acme should be gone, refCount=%d", pool.RefCount("acme"))
	}
	if _, ok := poolHasEntry(pool, "globex"); !ok {
		t.Error("evict acme accidentally dropped globex")
	}
	if _, ok := poolHasEntry(pool, "initech"); !ok {
		t.Error("evict acme accidentally dropped initech")
	}
}

// poolHasEntry is a test-only probe of ServicePool.entries that
// avoids exposing the internal map publicly.
func poolHasEntry(p *ServicePool, id string) (*poolEntry, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.entries[id]
	return e, ok
}

// TestServicePool_GetAfterEvict_ReResolves verifies that after
// Evict, the next Get for the same workspace runs the resolver again
// — this is how an invalidation webhook causes credentials to be
// re-fetched (ADR-012 invalidation triggers).
func TestServicePool_GetAfterEvict_ReResolves(t *testing.T) {
	t.Setenv("SPINE_REPO_PATH", ".")

	ctx := context.Background()
	resolver := &countingResolver{inner: multiConfigResolver{configs: map[string]Config{
		"ws-rotate": {ID: "ws-rotate", RepoPath: ".", Status: StatusActive},
	}}}
	pool := NewServicePool(ctx, resolver, PoolConfig{IdleCheckInterval: -1})
	defer pool.Close()

	first, err := pool.Get(ctx, "ws-rotate")
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	pool.Release("ws-rotate")

	// Cache hit: resolver may be called by Get (it canonicalizes the
	// workspace ID), but the service set is reused.
	cached, err := pool.Get(ctx, "ws-rotate")
	if err != nil {
		t.Fatalf("cached Get: %v", err)
	}
	pool.Release("ws-rotate")
	if cached != first {
		t.Fatal("expected cached service set to be reused before evict")
	}

	callsBefore := resolver.callsFor("ws-rotate")

	// Simulate a binding-invalidate webhook hitting this workspace.
	pool.Evict("ws-rotate")
	if pool.ActiveCount() != 0 {
		t.Fatalf("expected 0 active after evict, got %d", pool.ActiveCount())
	}

	// Subsequent Get must re-resolve and produce a fresh service set.
	rebuilt, err := pool.Get(ctx, "ws-rotate")
	if err != nil {
		t.Fatalf("post-evict Get: %v", err)
	}
	defer pool.Release("ws-rotate")

	if rebuilt == first {
		t.Error("post-evict Get returned the same service set; expected a fresh build")
	}
	if got := resolver.callsFor("ws-rotate"); got <= callsBefore {
		t.Errorf("expected resolver to be called again after evict; calls before=%d after=%d", callsBefore, got)
	}
}

// AppendCloser is the seam used by the cmd/spine pool builder to
// register per-workspace event delivery cancellation (TASK-003). The
// contract: appended closers run BEFORE the existing chain so dependent
// goroutines (subscriber/dispatcher/retention) cancel their context
// ahead of foundational teardown like queue.Stop. Without this ordering
// the dispatcher would observe a stopped queue mid-emit.
func TestAppendCloser_RunsAppendedFnFirst(t *testing.T) {
	var order []string
	ss := &ServiceSet{
		close: func(string) { order = append(order, "existing") },
	}
	ss.AppendCloser(func(string) { order = append(order, "appended") })

	ss.close("shutdown")

	if got, want := strings.Join(order, ","), "appended,existing"; got != want {
		t.Errorf("close order = %q, want %q", got, want)
	}
}

// AppendCloser also has to handle ServiceSets that have no prior close
// — namely test-only constructions. Without the nil check the wrapper
// would call (*ServiceSet)(nil)'s zero-value func and panic.
func TestAppendCloser_NoPriorClose_FnRunsAlone(t *testing.T) {
	ss := &ServiceSet{}
	called := false
	ss.AppendCloser(func(string) { called = true })

	ss.close("shutdown")

	if !called {
		t.Error("appended closer should run when there is no prior close")
	}
}

// Stacked AppendCloser calls compose in LIFO order: the most recently
// registered closer runs first, then the previous, then the original
// chain. Mirrors the in-construction LIFO closer behavior the rest of
// buildServiceSet relies on.
func TestAppendCloser_StacksLIFO(t *testing.T) {
	var order []string
	ss := &ServiceSet{
		close: func(string) { order = append(order, "base") },
	}
	ss.AppendCloser(func(string) { order = append(order, "first-appended") })
	ss.AppendCloser(func(string) { order = append(order, "second-appended") })

	ss.close("shutdown")

	if got, want := strings.Join(order, ","), "second-appended,first-appended,base"; got != want {
		t.Errorf("close order = %q, want %q", got, want)
	}
}

// The reason argument propagates to every closer in the chain.
func TestAppendCloser_PropagatesReason(t *testing.T) {
	var existingReason, appendedReason string
	ss := &ServiceSet{
		close: func(reason string) { existingReason = reason },
	}
	ss.AppendCloser(func(reason string) { appendedReason = reason })

	ss.close("idle")

	if appendedReason != "idle" || existingReason != "idle" {
		t.Errorf("reason propagation: appended=%q existing=%q", appendedReason, existingReason)
	}
}

// ServiceSet.Done is the eviction signal SSE relies on. Built at
// buildServiceSet time and closed at the start of close() so observers
// react before resources tear down — without this, a long-lived SSE
// stream that already released the pool ref keeps streaming
// heartbeats from a halted subscriber's broadcaster, silently missing
// every event after eviction.
func TestServiceSet_Done_ClosedOnClose(t *testing.T) {
	t.Setenv("SPINE_WORKSPACE_ID", "ws-done")
	t.Setenv("SPINE_DATABASE_URL", "")
	t.Setenv("SPINE_REPO_PATH", ".")

	ctx := context.Background()
	cfg, err := NewFileProvider(nil).Resolve(ctx, "ws-done")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	ss, err := buildServiceSet(ctx, *cfg, nil, nil, nil, PoolPolicy{}, "")
	if err != nil {
		t.Fatalf("buildServiceSet: %v", err)
	}

	if ss.Done == nil {
		t.Fatal("ss.Done must be non-nil for buildServiceSet results")
	}

	select {
	case <-ss.Done:
		t.Fatal("ss.Done should not be closed before close() runs")
	default:
	}

	ss.close("idle")

	select {
	case <-ss.Done:
		// expected
	case <-time.After(time.Second):
		t.Fatal("ss.Done should be closed after close() runs")
	}
}
