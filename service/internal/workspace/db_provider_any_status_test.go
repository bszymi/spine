package workspace

// INIT-023 TASK-009 (codex review round 1): ResolveAnyStatus reaches
// inactive workspaces for operator maintenance (`spine secrets reencrypt
// --all-workspaces`) with the same ref validation as Resolve. Needs
// SPINE_REGISTRY_TEST_DATABASE_URL with the registry migrations applied.

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDBProvider_ResolveAnyStatus(t *testing.T) {
	dbURL := registryTestDB(t)
	setupRegistryTable(t, dbURL)
	insertWorkspace(t, dbURL, "ws-act", "Active", "postgres://localhost/act", "/repos/act", "active")
	insertWorkspace(t, dbURL, "ws-off", "Inactive", "postgres://localhost/off", "/repos/off", "inactive")
	insertWorkspace(t, dbURL, "ws-x", "Cross", "secret-store://ws-other/runtime_db", "/repos/x", "inactive")

	ctx := context.Background()
	p, err := NewDBProvider(ctx, dbURL, DBProviderConfig{CacheTTL: time.Second})
	if err != nil {
		t.Fatalf("NewDBProvider: %v", err)
	}
	defer p.Close()

	if _, err := p.Resolve(ctx, "ws-off"); !errors.Is(err, ErrWorkspaceInactive) {
		t.Fatalf("Resolve must keep refusing inactive workspaces: %v", err)
	}
	for id, want := range map[string]string{"ws-act": "postgres://localhost/act", "ws-off": "postgres://localhost/off"} {
		cfg, err := p.ResolveAnyStatus(ctx, id)
		if err != nil || string(cfg.DatabaseURL.Reveal()) != want || cfg.ID != id {
			t.Fatalf("ResolveAnyStatus(%s): %+v %v", id, cfg, err)
		}
	}
	if cfg, _ := p.ResolveAnyStatus(ctx, "ws-off"); cfg.Status != StatusInactive {
		t.Fatalf("status not reported: %q", cfg.Status)
	}
	if _, err := p.ResolveAnyStatus(ctx, "ws-x"); !errors.Is(err, ErrWorkspaceUnavailable) {
		t.Fatalf("cross-tenant ref not refused: %v", err)
	}
	if _, err := p.ResolveAnyStatus(ctx, "missing"); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("missing workspace: %v", err)
	}
	if _, err := p.ResolveAnyStatus(ctx, "../etc"); !errors.Is(err, ErrWorkspaceNotFound) {
		t.Fatalf("invalid id: %v", err)
	}
	// ResolveAnyStatus does not populate the routing cache.
	p.mu.RLock()
	_, cached := p.cache["ws-off"]
	p.mu.RUnlock()
	if cached {
		t.Fatal("ResolveAnyStatus cached an inactive workspace")
	}
}
