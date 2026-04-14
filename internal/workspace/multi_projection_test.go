package workspace

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// fakeResolver is a test double for the Resolver interface.
type fakeResolver struct {
	configs []Config
	err     error
}

func (f *fakeResolver) Resolve(_ context.Context, id string) (*Config, error) {
	for _, c := range f.configs {
		if c.ID == id {
			return &c, nil
		}
	}
	return nil, ErrWorkspaceNotFound
}

func (f *fakeResolver) List(_ context.Context) ([]Config, error) {
	return f.configs, f.err
}

func TestNewMultiProjectionSync(t *testing.T) {
	t.Setenv("SPINE_WORKSPACE_ID", "ws-mps")
	t.Setenv("SPINE_DATABASE_URL", "")
	t.Setenv("SPINE_REPO_PATH", ".")

	ctx := context.Background()
	pool := NewServicePool(ctx, NewFileProvider(), PoolConfig{})
	defer pool.Close()

	resolver := &fakeResolver{}
	ms := NewMultiProjectionSync(pool, resolver, MultiProjectionSyncConfig{})
	if ms == nil {
		t.Fatal("expected non-nil MultiProjectionSync")
	}
}

func TestNewMultiProjectionSync_DefaultInterval(t *testing.T) {
	t.Setenv("SPINE_WORKSPACE_ID", "ws-mps-default")
	t.Setenv("SPINE_DATABASE_URL", "")
	t.Setenv("SPINE_REPO_PATH", ".")

	ctx := context.Background()
	pool := NewServicePool(ctx, NewFileProvider(), PoolConfig{})
	defer pool.Close()

	resolver := &fakeResolver{}
	// Zero PollInterval should default to 30s.
	ms := NewMultiProjectionSync(pool, resolver, MultiProjectionSyncConfig{PollInterval: 0})
	if ms.pollInterval != 30*time.Second {
		t.Errorf("expected 30s default poll interval, got %v", ms.pollInterval)
	}
}

func TestMultiProjectionSync_Stop(t *testing.T) {
	t.Setenv("SPINE_WORKSPACE_ID", "ws-mps-stop")
	t.Setenv("SPINE_DATABASE_URL", "")
	t.Setenv("SPINE_REPO_PATH", ".")

	ctx := context.Background()
	pool := NewServicePool(ctx, NewFileProvider(), PoolConfig{})
	defer pool.Close()

	resolver := &fakeResolver{}
	ms := NewMultiProjectionSync(pool, resolver, MultiProjectionSyncConfig{PollInterval: 100 * time.Millisecond})

	started := make(chan struct{})
	go func() {
		close(started)
		ms.Start(ctx)
	}()
	<-started
	// Small sleep to ensure the goroutine is in the select loop.
	time.Sleep(20 * time.Millisecond)
	ms.Stop()
}

func TestMultiProjectionSync_Start_ContextCancel(t *testing.T) {
	t.Setenv("SPINE_WORKSPACE_ID", "ws-mps-ctx")
	t.Setenv("SPINE_DATABASE_URL", "")
	t.Setenv("SPINE_REPO_PATH", ".")

	ctx, cancel := context.WithCancel(context.Background())
	pool := NewServicePool(ctx, NewFileProvider(), PoolConfig{})
	defer pool.Close()

	resolver := &fakeResolver{}
	ms := NewMultiProjectionSync(pool, resolver, MultiProjectionSyncConfig{PollInterval: 100 * time.Millisecond})

	done := make(chan struct{})
	go func() {
		ms.Start(ctx)
		close(done)
	}()

	// Cancel the context to stop the sync loop.
	cancel()
	select {
	case <-done:
		// OK
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after context cancellation")
	}
}

func TestMultiProjectionSync_SyncAll_EmptyWorkspaces(t *testing.T) {
	t.Setenv("SPINE_WORKSPACE_ID", "ws-mps-empty")
	t.Setenv("SPINE_DATABASE_URL", "")
	t.Setenv("SPINE_REPO_PATH", ".")

	ctx := context.Background()
	pool := NewServicePool(ctx, NewFileProvider(), PoolConfig{})
	defer pool.Close()

	resolver := &fakeResolver{configs: []Config{}} // empty list
	ms := NewMultiProjectionSync(pool, resolver, MultiProjectionSyncConfig{})

	// Should complete without panic.
	ms.syncAll(ctx)
}

func TestMultiProjectionSync_SyncAll_ListError(t *testing.T) {
	t.Setenv("SPINE_WORKSPACE_ID", "ws-mps-listerr")
	t.Setenv("SPINE_DATABASE_URL", "")
	t.Setenv("SPINE_REPO_PATH", ".")

	ctx := context.Background()
	pool := NewServicePool(ctx, NewFileProvider(), PoolConfig{})
	defer pool.Close()

	// Resolver returns an error → syncAll should log and return without panic.
	resolver := &fakeResolver{err: fmt.Errorf("registry unavailable")}
	ms := NewMultiProjectionSync(pool, resolver, MultiProjectionSyncConfig{})
	ms.syncAll(ctx)
}

func TestMultiProjectionSync_SyncAll_PoolGetError(t *testing.T) {
	t.Setenv("SPINE_WORKSPACE_ID", "ws-mps-geterr")
	t.Setenv("SPINE_DATABASE_URL", "")
	t.Setenv("SPINE_REPO_PATH", ".")

	ctx := context.Background()
	pool := NewServicePool(ctx, NewFileProvider(), PoolConfig{})
	defer pool.Close()

	// Resolver returns a workspace ID unknown to the pool → Get returns error.
	resolver := &fakeResolver{configs: []Config{{ID: "unknown-ws-id"}}}
	ms := NewMultiProjectionSync(pool, resolver, MultiProjectionSyncConfig{})
	// Should not panic; logs error and continues.
	ms.syncAll(ctx)
}

func TestMultiProjectionSync_SyncAll_NilProjSync(t *testing.T) {
	t.Setenv("SPINE_WORKSPACE_ID", "ws-mps-nilsync")
	t.Setenv("SPINE_DATABASE_URL", "")
	t.Setenv("SPINE_REPO_PATH", ".")

	ctx := context.Background()
	pool := NewServicePool(ctx, NewFileProvider(), PoolConfig{})
	defer pool.Close()

	// The pool resolves this workspace (file provider reads env SPINE_WORKSPACE_ID).
	// Since there's no DB URL, ProjSync will be nil → syncAll releases and continues.
	resolver := &fakeResolver{configs: []Config{{ID: "ws-mps-nilsync"}}}
	ms := NewMultiProjectionSync(pool, resolver, MultiProjectionSyncConfig{})
	ms.syncAll(ctx)
}
