//go:build integration

package projection_test

import (
	"context"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/projection"
	"github.com/bszymi/spine/internal/store"
	"github.com/bszymi/spine/internal/testutil"
)

func setupIntegrationTest(t *testing.T) (*projection.Service, *git.CLIClient, string, *store.PostgresStore) {
	t.Helper()

	repo := testutil.NewTempRepo(t)
	client := git.NewCLIClient(repo)
	db := store.NewTestStore(t)
	svc := projection.NewService(client, db, nil, 1*time.Second)

	// Add test artifacts
	testutil.WriteFile(t, repo, "governance/charter.md", `---
type: Governance
title: Charter
status: Foundational
---

# Charter
`)
	testutil.WriteFile(t, repo, "architecture/domain-model.md", `---
type: Architecture
title: Domain Model
status: Living Document
version: "0.1"
links:
  - type: related_to
    target: /governance/charter.md
---

# Domain Model
`)
	testutil.WriteFile(t, repo, "workflows/task-execution.yaml", `id: task-execution
name: Task Execution
version: "1.0"
status: Active
applies_to:
  - Task
`)
	testutil.WriteFile(t, repo, "README.md", "# Readme\n")
	testutil.GitAdd(t, repo, ".", "add test content")

	return svc, client, repo, db
}

func TestFullRebuild(t *testing.T) {
	svc, _, _, db := setupIntegrationTest(t)
	ctx := context.Background()
	defer db.CleanupTestData(ctx, t)

	if err := svc.FullRebuild(ctx); err != nil {
		t.Fatalf("FullRebuild: %v", err)
	}

	// Check artifacts were projected
	charter, err := db.GetArtifactProjection(ctx, "governance/charter.md")
	if err != nil {
		t.Fatalf("GetArtifactProjection charter: %v", err)
	}
	if charter.Title != "Charter" {
		t.Errorf("expected Charter, got %s", charter.Title)
	}
	if charter.ArtifactType != "Governance" {
		t.Errorf("expected Governance, got %s", charter.ArtifactType)
	}

	domain, err := db.GetArtifactProjection(ctx, "architecture/domain-model.md")
	if err != nil {
		t.Fatalf("GetArtifactProjection domain-model: %v", err)
	}
	if domain.Title != "Domain Model" {
		t.Errorf("expected Domain Model, got %s", domain.Title)
	}

	// README should NOT be projected
	_, err = db.GetArtifactProjection(ctx, "README.md")
	if err == nil {
		t.Error("README should not be projected")
	}

	// Check sync state
	state, err := db.GetSyncState(ctx)
	if err != nil {
		t.Fatalf("GetSyncState: %v", err)
	}
	if state == nil {
		t.Fatal("expected sync state")
	}
	if state.Status != "idle" {
		t.Errorf("expected idle, got %s", state.Status)
	}
}

func TestIncrementalSync(t *testing.T) {
	svc, _, repo, db := setupIntegrationTest(t)
	ctx := context.Background()
	defer db.CleanupTestData(ctx, t)

	// First do a full rebuild
	if err := svc.FullRebuild(ctx); err != nil {
		t.Fatalf("FullRebuild: %v", err)
	}

	// Add a new artifact
	testutil.WriteFile(t, repo, "governance/guidelines.md", `---
type: Governance
title: Guidelines
status: Living Document
---

# Guidelines
`)
	testutil.GitAdd(t, repo, "governance/guidelines.md", "add guidelines")

	// Run incremental sync
	if err := svc.IncrementalSync(ctx); err != nil {
		t.Fatalf("IncrementalSync: %v", err)
	}

	// New artifact should be projected
	guidelines, err := db.GetArtifactProjection(ctx, "governance/guidelines.md")
	if err != nil {
		t.Fatalf("GetArtifactProjection guidelines: %v", err)
	}
	if guidelines.Title != "Guidelines" {
		t.Errorf("expected Guidelines, got %s", guidelines.Title)
	}

	// Existing artifacts should still be there
	_, err = db.GetArtifactProjection(ctx, "governance/charter.md")
	if err != nil {
		t.Fatalf("charter should still exist: %v", err)
	}
}

func TestIncrementalSyncDelete(t *testing.T) {
	svc, _, repo, db := setupIntegrationTest(t)
	ctx := context.Background()
	defer db.CleanupTestData(ctx, t)

	if err := svc.FullRebuild(ctx); err != nil {
		t.Fatalf("FullRebuild: %v", err)
	}

	// Verify charter exists
	_, err := db.GetArtifactProjection(ctx, "governance/charter.md")
	if err != nil {
		t.Fatalf("charter should exist before delete: %v", err)
	}

	// Delete an artifact
	testutil.GitRm(t, repo, "governance/charter.md", "delete charter")

	// Run incremental sync
	if err := svc.IncrementalSync(ctx); err != nil {
		t.Fatalf("IncrementalSync: %v", err)
	}

	// Deleted artifact should be gone
	_, err = db.GetArtifactProjection(ctx, "governance/charter.md")
	if err == nil {
		t.Error("charter should have been deleted from projections")
	}
}

func TestIncrementalSyncNoChanges(t *testing.T) {
	svc, _, _, db := setupIntegrationTest(t)
	ctx := context.Background()
	defer db.CleanupTestData(ctx, t)

	if err := svc.FullRebuild(ctx); err != nil {
		t.Fatalf("FullRebuild: %v", err)
	}

	// Run incremental sync with no changes — should be no-op
	if err := svc.IncrementalSync(ctx); err != nil {
		t.Fatalf("IncrementalSync (no changes): %v", err)
	}
}

func TestFullRebuildAndIncrementalProduceSameResult(t *testing.T) {
	svc, client, repo, db := setupIntegrationTest(t)
	ctx := context.Background()
	defer db.CleanupTestData(ctx, t)

	// Full rebuild
	if err := svc.FullRebuild(ctx); err != nil {
		t.Fatalf("FullRebuild: %v", err)
	}

	// Get projected state
	charterAfterRebuild, _ := db.GetArtifactProjection(ctx, "governance/charter.md")
	domainAfterRebuild, _ := db.GetArtifactProjection(ctx, "architecture/domain-model.md")

	// Add a new artifact
	testutil.WriteFile(t, repo, "governance/style-guide.md", `---
type: Governance
title: Style Guide
status: Living Document
---

# Style Guide
`)
	testutil.GitAdd(t, repo, "governance/style-guide.md", "add style guide")

	// Incremental sync
	if err := svc.IncrementalSync(ctx); err != nil {
		t.Fatalf("IncrementalSync: %v", err)
	}

	// Now do another full rebuild
	if err := svc.FullRebuild(ctx); err != nil {
		t.Fatalf("second FullRebuild: %v", err)
	}

	// Check that charter and domain model are the same
	charterAfterSecondRebuild, _ := db.GetArtifactProjection(ctx, "governance/charter.md")
	domainAfterSecondRebuild, _ := db.GetArtifactProjection(ctx, "architecture/domain-model.md")

	if charterAfterRebuild.ContentHash != charterAfterSecondRebuild.ContentHash {
		t.Error("charter content hash differs between rebuilds")
	}
	if domainAfterRebuild.ContentHash != domainAfterSecondRebuild.ContentHash {
		t.Error("domain model content hash differs between rebuilds")
	}

	// Style guide should exist after both paths
	_, err := db.GetArtifactProjection(ctx, "governance/style-guide.md")
	if err != nil {
		t.Fatalf("style guide should exist: %v", err)
	}

	_ = client // used indirectly
}
