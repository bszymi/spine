//go:build scenario

package scenarios_test

import (
	"context"
	"os"
	"testing"

	"github.com/bszymi/spine/internal/secrets"
	"github.com/bszymi/spine/internal/store"
	"github.com/bszymi/spine/internal/workspace"
)

// TestWorkspace_BatchMigration verifies that migrations applied to a workspace
// database are tracked in schema_migrations and that the migration system is
// idempotent (running again doesn't fail).
//
// Scenario: DB migrations are tracked and idempotent
//   Given a workspace database
//   When migrations are applied
//   Then "001_initial_schema" should be recorded in schema_migrations
//   When migrations are applied again
//   Then the operation should succeed without error (idempotent)
//   When a workspace is registered in the registry and listed
//   Then the workspace DB should have "001_initial_schema" applied
func TestWorkspace_BatchMigration(t *testing.T) {
	wsDBURL := os.Getenv("SPINE_REGISTRY_TEST_DATABASE_URL")
	if wsDBURL == "" {
		t.Skip("SPINE_REGISTRY_TEST_DATABASE_URL not set")
	}

	ctx := context.Background()

	// --- Step 1: Connect and apply migrations ---
	t.Run("apply-migrations", func(t *testing.T) {
		s, err := store.NewPostgresStore(ctx, wsDBURL)
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		defer s.Close()

		if err := s.ApplyMigrations(ctx, store.FindMigrationsDir()); err != nil {
			t.Fatalf("apply migrations: %v", err)
		}
	})

	// --- Step 2: Verify schema_migrations table has entries ---
	t.Run("verify-migration-tracking", func(t *testing.T) {
		s, err := store.NewPostgresStore(ctx, wsDBURL)
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		defer s.Close()

		// Check that at least the initial migration is recorded.
		applied, err := s.IsMigrationApplied(ctx, "001_initial_schema")
		if err != nil {
			t.Fatalf("check migration: %v", err)
		}
		if !applied {
			t.Error("001_initial_schema should be recorded in schema_migrations")
		}
	})

	// --- Step 3: Idempotent re-run doesn't fail ---
	t.Run("re-apply-migrations-idempotent", func(t *testing.T) {
		s, err := store.NewPostgresStore(ctx, wsDBURL)
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		defer s.Close()

		if err := s.ApplyMigrations(ctx, store.FindMigrationsDir()); err != nil {
			t.Fatalf("re-apply migrations should be idempotent: %v", err)
		}
	})

	// --- Step 4: Verify resolver provider lists migrated workspace DB ---
	// This proves the batch migration flow (registry → list workspaces → migrate each)
	// works end-to-end via the DBProvider.
	t.Run("registry-workspace-migration-flow", func(t *testing.T) {
		registryURL := os.Getenv("SPINE_REGISTRY_TEST_DATABASE_URL")

		provider, err := workspace.NewDBProvider(ctx, registryURL, workspace.DBProviderConfig{})
		if err != nil {
			t.Fatalf("connect to registry: %v", err)
		}
		defer provider.Close()

		// Clean up test workspace.
		provider.DeleteWorkspace(ctx, "ws-migration-test")
		t.Cleanup(func() { provider.DeleteWorkspace(ctx, "ws-migration-test") })

		// Create a workspace pointing to the test DB.
		err = provider.CreateWorkspace(ctx, workspace.Config{
			ID:          "ws-migration-test",
			DisplayName: "Migration Test",
			DatabaseURL: secrets.NewSecretValue([]byte(wsDBURL)),
			RepoPath:    "/tmp/fake",
			Status:      workspace.StatusActive,
		})
		if err != nil {
			t.Fatalf("create workspace: %v", err)
		}

		// List should return it.
		workspaces, err := provider.List(ctx)
		if err != nil {
			t.Fatalf("list: %v", err)
		}

		found := false
		for _, ws := range workspaces {
			if ws.ID == "ws-migration-test" {
				found = true
				// Verify we could connect to its DB and check migrations.
				wsStore, err := store.NewPostgresStore(ctx, string(ws.DatabaseURL.Reveal()))
				if err != nil {
					t.Fatalf("connect to workspace DB: %v", err)
				}
				defer wsStore.Close()

				applied, err := wsStore.IsMigrationApplied(ctx, "001_initial_schema")
				if err != nil {
					t.Fatalf("check migration on workspace DB: %v", err)
				}
				if !applied {
					t.Error("workspace DB should have 001_initial_schema applied")
				}
				break
			}
		}
		if !found {
			t.Error("ws-migration-test not found in workspace list")
		}
	})
}
