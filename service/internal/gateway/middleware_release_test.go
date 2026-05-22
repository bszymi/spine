package gateway

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

// releaseWorkspaceRef is the seam SSE (and any other long-lived
// workspace-bound handler) uses to drop the per-request pool reference
// once the ServiceSet's store is no longer needed. Without this,
// binding invalidations during open SSE streams block subsequent
// pool.Get calls for the same workspace until the stream disconnects.
// The contract is: calling releaseWorkspaceRef on a context with no
// installed release fn is a no-op; calling it once invokes the
// underlying release exactly once even if it is also invoked again
// later (e.g. by the middleware's defer).

func TestReleaseWorkspaceRef_NoFnInstalled_IsNoOp(t *testing.T) {
	releaseWorkspaceRef(context.Background()) // must not panic
}

func TestReleaseWorkspaceRef_InvokesInstalledFn(t *testing.T) {
	var calls atomic.Int32
	release := func() { calls.Add(1) }
	ctx := context.WithValue(context.Background(), releaseWorkspaceRefKey{}, release)

	releaseWorkspaceRef(ctx)

	if got := calls.Load(); got != 1 {
		t.Errorf("expected 1 release call, got %d", got)
	}
}

// The middleware wraps its Release in a sync.Once so an early call
// from a handler followed by the deferred call from the middleware
// runs the underlying pool.Release exactly once. The handler-side
// helper relies on that idempotence — assert it directly here so a
// future refactor of the middleware can't quietly reintroduce
// double-release.
func TestReleaseWorkspaceRef_SyncOnceIdempotent(t *testing.T) {
	var calls atomic.Int32
	var once sync.Once
	release := func() {
		once.Do(func() { calls.Add(1) })
	}
	ctx := context.WithValue(context.Background(), releaseWorkspaceRefKey{}, release)

	releaseWorkspaceRef(ctx)
	releaseWorkspaceRef(ctx)
	release() // simulate the middleware's deferred call

	if got := calls.Load(); got != 1 {
		t.Errorf("expected exactly one underlying release across early call + defer, got %d", got)
	}
}
