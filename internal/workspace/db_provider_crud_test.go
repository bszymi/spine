package workspace

import (
	"context"
	"testing"
	"time"
)

// TestDBProvider_CRUD exercises the write-side methods (Create / Deactivate /
// Delete / Invalidate) and the read-side helpers (Get / ListAll) that back
// the workspace management handlers. Requires SPINE_REGISTRY_TEST_DATABASE_URL.
func TestDBProvider_CRUD(t *testing.T) {
	dbURL := registryTestDB(t)
	setupRegistryTable(t, dbURL)

	ctx := context.Background()
	p, err := NewDBProvider(ctx, dbURL, DBProviderConfig{CacheTTL: time.Second})
	if err != nil {
		t.Fatalf("NewDBProvider: %v", err)
	}
	defer p.Close()

	cfg := Config{
		ID:          "ws-crud",
		DisplayName: "CRUD Workspace",
		DatabaseURL: "postgres://localhost/ws-crud",
		RepoPath:    "/repos/ws-crud",
		Status:      StatusActive,
	}
	if err := p.CreateWorkspace(ctx, cfg); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	t.Cleanup(func() { _ = p.DeleteWorkspace(context.Background(), cfg.ID) })

	// GetWorkspace happy path.
	got, err := p.GetWorkspace(ctx, cfg.ID)
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if got.DisplayName != cfg.DisplayName {
		t.Errorf("DisplayName = %q, want %q", got.DisplayName, cfg.DisplayName)
	}

	// Warm the resolve cache, then Invalidate should clear it.
	if _, err := p.Resolve(ctx, cfg.ID); err != nil {
		t.Fatalf("Resolve pre-invalidate: %v", err)
	}
	p.Invalidate(cfg.ID)
	p.mu.RLock()
	_, cached := p.cache[cfg.ID]
	p.mu.RUnlock()
	if cached {
		t.Errorf("Invalidate did not clear cache entry")
	}

	// ListAllWorkspaces includes our inserted row.
	all, err := p.ListAllWorkspaces(ctx)
	if err != nil {
		t.Fatalf("ListAllWorkspaces: %v", err)
	}
	found := false
	for _, w := range all {
		if w.ID == cfg.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("ListAllWorkspaces missing %q", cfg.ID)
	}

	// DeactivateWorkspace succeeds, then Resolve reports Inactive.
	if err := p.DeactivateWorkspace(ctx, cfg.ID); err != nil {
		t.Fatalf("DeactivateWorkspace: %v", err)
	}
	p.Invalidate(cfg.ID)
	if _, err := p.Resolve(ctx, cfg.ID); err != ErrWorkspaceInactive {
		t.Errorf("Resolve after deactivate: want ErrWorkspaceInactive, got %v", err)
	}

	// DeactivateWorkspace on a now-inactive row reports not found (no rows
	// matched the active filter).
	if err := p.DeactivateWorkspace(ctx, cfg.ID); err != ErrWorkspaceNotFound {
		t.Errorf("Deactivate on already-inactive: want ErrWorkspaceNotFound, got %v", err)
	}

	// GetWorkspace returns the inactive row — status reflects the update.
	got2, err := p.GetWorkspace(ctx, cfg.ID)
	if err != nil {
		t.Fatalf("GetWorkspace inactive: %v", err)
	}
	if got2.Status != StatusInactive {
		t.Errorf("Status after deactivate = %q, want %q", got2.Status, StatusInactive)
	}

	// GetWorkspace returns ErrWorkspaceNotFound for unknown IDs.
	if _, err := p.GetWorkspace(ctx, "does-not-exist"); err != ErrWorkspaceNotFound {
		t.Errorf("GetWorkspace missing: want ErrWorkspaceNotFound, got %v", err)
	}

	// DeleteWorkspace cleanly removes the row.
	if err := p.DeleteWorkspace(ctx, cfg.ID); err != nil {
		t.Fatalf("DeleteWorkspace: %v", err)
	}
	if _, err := p.GetWorkspace(ctx, cfg.ID); err != ErrWorkspaceNotFound {
		t.Errorf("GetWorkspace after delete: want ErrWorkspaceNotFound, got %v", err)
	}
}
