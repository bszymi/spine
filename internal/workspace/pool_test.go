package workspace

import (
	"context"
	"fmt"
	"strings"
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

	ss, err := buildServiceSet(ctx, cfg, nil)
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
