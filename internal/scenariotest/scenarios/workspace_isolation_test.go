//go:build scenario

package scenarios_test

import (
	"context"
	"testing"
	"time"

	"os"

	"github.com/bszymi/spine/internal/artifact"
	"github.com/bszymi/spine/internal/event"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/projection"
	"github.com/bszymi/spine/internal/queue"
	"github.com/bszymi/spine/internal/scenariotest/harness"
	"github.com/bszymi/spine/internal/store"
)

// TestWorkspace_Isolation verifies that two workspaces operating in the same
// process are fully isolated: artifacts created in one workspace are invisible
// to the other.
func TestWorkspace_Isolation(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")

	ctx := context.Background()

	// --- Workspace Alpha: use the standard test DB + repo ---
	envAlpha := harness.NewTestEnvironment(t, harness.WithGovernance())

	// --- Workspace Beta: SEPARATE DB + separate repo ---
	// Use SPINE_REGISTRY_TEST_DATABASE_URL as beta's workspace DB (different from alpha's).
	// This proves isolation at the connection level.
	betaDBURL := os.Getenv("SPINE_REGISTRY_TEST_DATABASE_URL")
	if betaDBURL == "" {
		t.Skip("SPINE_REGISTRY_TEST_DATABASE_URL not set — need a second DB for isolation test")
	}

	repoBeta := harness.NewTestRepo(t)
	repoBeta.SeedGovernance(t)

	betaStore, err := store.NewPostgresStore(ctx, betaDBURL)
	if err != nil {
		t.Fatalf("connect to beta DB: %v", err)
	}
	t.Cleanup(func() { betaStore.Close() })
	if err := betaStore.ApplyMigrations(ctx, store.FindMigrationsDir()); err != nil {
		t.Fatalf("apply beta migrations: %v", err)
	}
	betaStore.CleanupTestData(ctx, t)

	// Build Beta's services manually (same as TestRuntime but separate instances).
	gitBeta := git.NewCLIClient(repoBeta.Dir)
	qBeta := queue.NewMemoryQueue(100)
	go qBeta.Start(ctx)
	t.Cleanup(func() { qBeta.Stop() })
	eventsBeta := event.NewQueueRouter(qBeta)

	artifactsBeta := artifact.NewService(gitBeta, eventsBeta, repoBeta.Dir)
	projBeta := projection.NewService(gitBeta, betaStore, eventsBeta, 30*time.Second)

	// --- Step 1: Create artifact in Alpha ---
	t.Run("create-artifact-in-alpha", func(t *testing.T) {
		content := `---
type: Governance
title: Alpha Document
status: Living Document
version: "0.1"
---

# Alpha Document

Created in workspace alpha.
`
		_, err := envAlpha.Runtime.Artifacts.Create(ctx, "governance/alpha-only.md", content)
		if err != nil {
			t.Fatalf("create artifact in alpha: %v", err)
		}
	})

	// --- Step 2: Sync projections in both workspaces ---
	t.Run("sync-alpha-projections", func(t *testing.T) {
		if err := envAlpha.Runtime.Projections.FullRebuild(ctx); err != nil {
			t.Fatalf("sync alpha: %v", err)
		}
	})

	t.Run("sync-beta-projections", func(t *testing.T) {
		if err := projBeta.FullRebuild(ctx); err != nil {
			t.Fatalf("sync beta: %v", err)
		}
	})

	// --- Step 3: Verify artifact visible in Alpha ---
	t.Run("artifact-visible-in-alpha", func(t *testing.T) {
		result, err := envAlpha.DB.Store.QueryArtifacts(ctx, store.ArtifactQuery{})
		if err != nil {
			t.Fatalf("query alpha: %v", err)
		}

		found := false
		for _, a := range result.Items {
			if a.ArtifactPath == "governance/alpha-only.md" {
				found = true
				break
			}
		}
		if !found {
			t.Error("artifact governance/alpha-only.md not found in alpha projections")
		}
	})

	// --- Step 4: Verify artifact NOT visible in Beta ---
	t.Run("artifact-not-visible-in-beta", func(t *testing.T) {
		result, err := betaStore.QueryArtifacts(ctx, store.ArtifactQuery{})
		if err != nil {
			t.Fatalf("query beta: %v", err)
		}

		for _, a := range result.Items {
			if a.ArtifactPath == "governance/alpha-only.md" {
				t.Error("artifact governance/alpha-only.md should NOT be visible in beta projections")
			}
		}
	})

	// --- Step 5: Create artifact in Beta, verify NOT visible in Alpha ---
	t.Run("create-artifact-in-beta", func(t *testing.T) {
		content := `---
type: Governance
title: Beta Document
status: Living Document
version: "0.1"
---

# Beta Document

Created in workspace beta.
`
		_, err := artifactsBeta.Create(ctx, "governance/beta-only.md", content)
		if err != nil {
			t.Fatalf("create artifact in beta: %v", err)
		}
	})

	t.Run("sync-beta-after-create", func(t *testing.T) {
		if err := projBeta.FullRebuild(ctx); err != nil {
			t.Fatalf("sync beta: %v", err)
		}
	})

	t.Run("beta-artifact-visible-in-beta", func(t *testing.T) {
		result, err := betaStore.QueryArtifacts(ctx, store.ArtifactQuery{})
		if err != nil {
			t.Fatalf("query beta: %v", err)
		}

		found := false
		for _, a := range result.Items {
			if a.ArtifactPath == "governance/beta-only.md" {
				found = true
				break
			}
		}
		if !found {
			t.Error("artifact governance/beta-only.md not found in beta projections")
		}
	})

	t.Run("beta-artifact-not-visible-in-alpha", func(t *testing.T) {
		// Re-sync alpha to be sure.
		if err := envAlpha.Runtime.Projections.FullRebuild(ctx); err != nil {
			t.Fatalf("sync alpha: %v", err)
		}

		result, err := envAlpha.DB.Store.QueryArtifacts(ctx, store.ArtifactQuery{})
		if err != nil {
			t.Fatalf("query alpha: %v", err)
		}

		for _, a := range result.Items {
			if a.ArtifactPath == "governance/beta-only.md" {
				t.Error("artifact governance/beta-only.md should NOT be visible in alpha projections")
			}
		}
	})
}
