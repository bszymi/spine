//go:build scenario

package scenarios_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/bszymi/spine/internal/workspace"
)

// TestWorkspace_Lifecycle verifies workspace lifecycle: create, resolve,
// deactivate, and verify rejection after deactivation.
func TestWorkspace_Lifecycle(t *testing.T) {
	registryURL := os.Getenv("SPINE_REGISTRY_TEST_DATABASE_URL")
	if registryURL == "" {
		t.Skip("SPINE_REGISTRY_TEST_DATABASE_URL not set")
	}

	ctx := context.Background()

	// Connect to registry DB.
	provider, err := workspace.NewDBProvider(ctx, registryURL, workspace.DBProviderConfig{
		CacheTTL: 1, // minimal cache for testing
	})
	if err != nil {
		t.Fatalf("connect to registry: %v", err)
	}
	defer provider.Close()

	// Clean up test workspace at start and end.
	cleanup := func() {
		_ = provider.DeactivateWorkspace(ctx, "ws-lifecycle")
		// Delete the row entirely for clean reruns.
		provider.DeleteWorkspace(ctx, "ws-lifecycle")
	}
	cleanup()
	t.Cleanup(cleanup)

	// --- Step 1: Create workspace ---
	t.Run("create-workspace", func(t *testing.T) {
		err := provider.CreateWorkspace(ctx, workspace.Config{
			ID:          "ws-lifecycle",
			DisplayName: "Lifecycle Test Workspace",
			DatabaseURL: "postgres://fake:fake@localhost/fake",
			RepoPath:    "/tmp/fake-repo",
			Status:      workspace.StatusActive,
		})
		if err != nil {
			t.Fatalf("create workspace: %v", err)
		}
	})

	// --- Step 2: Resolve active workspace succeeds ---
	t.Run("resolve-active-workspace", func(t *testing.T) {
		cfg, err := provider.Resolve(ctx, "ws-lifecycle")
		if err != nil {
			t.Fatalf("resolve active workspace: %v", err)
		}
		if cfg.ID != "ws-lifecycle" {
			t.Errorf("expected ID %q, got %q", "ws-lifecycle", cfg.ID)
		}
		if cfg.DisplayName != "Lifecycle Test Workspace" {
			t.Errorf("expected display name %q, got %q", "Lifecycle Test Workspace", cfg.DisplayName)
		}
	})

	// --- Step 3: List includes active workspace ---
	t.Run("list-includes-active", func(t *testing.T) {
		workspaces, err := provider.List(ctx)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		found := false
		for _, ws := range workspaces {
			if ws.ID == "ws-lifecycle" {
				found = true
				break
			}
		}
		if !found {
			t.Error("ws-lifecycle not found in active workspace list")
		}
	})

	// --- Step 4: Deactivate workspace ---
	t.Run("deactivate-workspace", func(t *testing.T) {
		if err := provider.DeactivateWorkspace(ctx, "ws-lifecycle"); err != nil {
			t.Fatalf("deactivate: %v", err)
		}

		// Invalidate cache so next Resolve reads from DB.
		provider.Invalidate("ws-lifecycle")
	})

	// --- Step 5: Resolve returns ErrWorkspaceInactive ---
	t.Run("resolve-rejects-inactive", func(t *testing.T) {
		_, err := provider.Resolve(ctx, "ws-lifecycle")
		if !errors.Is(err, workspace.ErrWorkspaceInactive) {
			t.Errorf("expected ErrWorkspaceInactive, got %v", err)
		}
	})

	// --- Step 6: List excludes inactive workspace ---
	t.Run("list-excludes-inactive", func(t *testing.T) {
		workspaces, err := provider.List(ctx)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		for _, ws := range workspaces {
			if ws.ID == "ws-lifecycle" {
				t.Error("ws-lifecycle should NOT appear in active workspace list after deactivation")
			}
		}
	})

	// --- Step 7: Get still returns the workspace (with inactive status) ---
	t.Run("get-returns-inactive-workspace", func(t *testing.T) {
		cfg, err := provider.GetWorkspace(ctx, "ws-lifecycle")
		if err != nil {
			t.Fatalf("get workspace: %v", err)
		}
		if cfg.Status != workspace.StatusInactive {
			t.Errorf("expected status %q, got %q", workspace.StatusInactive, cfg.Status)
		}
	})

	// --- Step 8: Double deactivation returns not found ---
	t.Run("double-deactivate-returns-not-found", func(t *testing.T) {
		err := provider.DeactivateWorkspace(ctx, "ws-lifecycle")
		if !errors.Is(err, workspace.ErrWorkspaceNotFound) {
			t.Errorf("expected ErrWorkspaceNotFound on double deactivate, got %v", err)
		}
	})
}
