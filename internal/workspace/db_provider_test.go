package workspace

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests require a running PostgreSQL instance with the workspace_registry
// table. Set SPINE_REGISTRY_TEST_DATABASE_URL to enable them.
// Start the test DB with: docker compose -f docker-compose.test.yaml up -d spine-test-registry-db

func registryTestDB(t *testing.T) string {
	t.Helper()
	url := os.Getenv("SPINE_REGISTRY_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("SPINE_REGISTRY_TEST_DATABASE_URL not set, skipping integration test")
	}
	return url
}

func setupRegistryTable(t *testing.T, dbURL string) {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect to registry DB: %v", err)
	}
	defer pool.Close()

	// Create table (idempotent).
	_, err = pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS public.workspace_registry (
			workspace_id  text        PRIMARY KEY,
			display_name  text        NOT NULL,
			database_url  text        NOT NULL,
			repo_path     text        NOT NULL,
			actor_scope   text        NOT NULL DEFAULT '',
			status        text        NOT NULL DEFAULT 'active',
			created_at    timestamptz NOT NULL DEFAULT now(),
			updated_at    timestamptz NOT NULL DEFAULT now()
		)`)
	if err != nil {
		t.Fatalf("create registry table: %v", err)
	}

	// Clean up before test.
	_, _ = pool.Exec(ctx, `DELETE FROM public.workspace_registry`)
}

func insertWorkspace(t *testing.T, dbURL, id, displayName, wsDBURL, repoPath, status string) {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	_, err = pool.Exec(ctx,
		`INSERT INTO public.workspace_registry (workspace_id, display_name, database_url, repo_path, status)
		 VALUES ($1, $2, $3, $4, $5)`,
		id, displayName, wsDBURL, repoPath, status)
	if err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
}

func TestDBProvider_Resolve(t *testing.T) {
	dbURL := registryTestDB(t)
	setupRegistryTable(t, dbURL)
	insertWorkspace(t, dbURL, "ws-1", "Workspace One", "postgres://localhost/ws1", "/repos/ws1", "active")

	ctx := context.Background()
	p, err := NewDBProvider(ctx, dbURL, DBProviderConfig{CacheTTL: 1 * time.Second})
	if err != nil {
		t.Fatalf("NewDBProvider: %v", err)
	}
	defer p.Close()

	cfg, err := p.Resolve(ctx, "ws-1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.ID != "ws-1" {
		t.Errorf("expected ID %q, got %q", "ws-1", cfg.ID)
	}
	if cfg.DisplayName != "Workspace One" {
		t.Errorf("expected DisplayName %q, got %q", "Workspace One", cfg.DisplayName)
	}
	if cfg.DatabaseURL != "postgres://localhost/ws1" {
		t.Errorf("expected DatabaseURL %q, got %q", "postgres://localhost/ws1", cfg.DatabaseURL)
	}
	if cfg.Status != StatusActive {
		t.Errorf("expected Status %q, got %q", StatusActive, cfg.Status)
	}
}

func TestDBProvider_Resolve_NotFound(t *testing.T) {
	dbURL := registryTestDB(t)
	setupRegistryTable(t, dbURL)

	ctx := context.Background()
	p, err := NewDBProvider(ctx, dbURL, DBProviderConfig{CacheTTL: 1 * time.Second})
	if err != nil {
		t.Fatalf("NewDBProvider: %v", err)
	}
	defer p.Close()

	_, err = p.Resolve(ctx, "nonexistent")
	if err != ErrWorkspaceNotFound {
		t.Errorf("expected ErrWorkspaceNotFound, got %v", err)
	}
}

func TestDBProvider_Resolve_Inactive(t *testing.T) {
	dbURL := registryTestDB(t)
	setupRegistryTable(t, dbURL)
	insertWorkspace(t, dbURL, "ws-inactive", "Inactive WS", "postgres://localhost/inactive", "/repos/inactive", "inactive")

	ctx := context.Background()
	p, err := NewDBProvider(ctx, dbURL, DBProviderConfig{CacheTTL: 1 * time.Second})
	if err != nil {
		t.Fatalf("NewDBProvider: %v", err)
	}
	defer p.Close()

	_, err = p.Resolve(ctx, "ws-inactive")
	if err != ErrWorkspaceInactive {
		t.Errorf("expected ErrWorkspaceInactive, got %v", err)
	}
}

func TestDBProvider_Resolve_Cache(t *testing.T) {
	dbURL := registryTestDB(t)
	setupRegistryTable(t, dbURL)
	insertWorkspace(t, dbURL, "ws-cached", "Cached WS", "postgres://localhost/cached", "/repos/cached", "active")

	ctx := context.Background()
	p, err := NewDBProvider(ctx, dbURL, DBProviderConfig{CacheTTL: 5 * time.Second})
	if err != nil {
		t.Fatalf("NewDBProvider: %v", err)
	}
	defer p.Close()

	// First call populates cache.
	cfg1, err := p.Resolve(ctx, "ws-cached")
	if err != nil {
		t.Fatalf("first Resolve: %v", err)
	}

	// Second call should hit cache (same result).
	cfg2, err := p.Resolve(ctx, "ws-cached")
	if err != nil {
		t.Fatalf("second Resolve: %v", err)
	}

	if cfg1.ID != cfg2.ID {
		t.Errorf("cached result mismatch: %q vs %q", cfg1.ID, cfg2.ID)
	}
}

func TestDBProvider_List(t *testing.T) {
	dbURL := registryTestDB(t)
	setupRegistryTable(t, dbURL)
	insertWorkspace(t, dbURL, "ws-a", "WS A", "postgres://localhost/a", "/repos/a", "active")
	insertWorkspace(t, dbURL, "ws-b", "WS B", "postgres://localhost/b", "/repos/b", "active")
	insertWorkspace(t, dbURL, "ws-c", "WS C", "postgres://localhost/c", "/repos/c", "inactive")

	ctx := context.Background()
	p, err := NewDBProvider(ctx, dbURL, DBProviderConfig{CacheTTL: 1 * time.Second})
	if err != nil {
		t.Fatalf("NewDBProvider: %v", err)
	}
	defer p.Close()

	configs, err := p.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(configs) != 2 {
		t.Fatalf("expected 2 active workspaces, got %d", len(configs))
	}

	// Should be ordered by workspace_id.
	if configs[0].ID != "ws-a" || configs[1].ID != "ws-b" {
		t.Errorf("unexpected order: %q, %q", configs[0].ID, configs[1].ID)
	}
}
