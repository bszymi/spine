package workspace

import (
	"context"
	"testing"
	"time"
)

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
