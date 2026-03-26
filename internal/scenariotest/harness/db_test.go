//go:build scenario

package harness_test

import (
	"context"
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/scenariotest/harness"
	"github.com/bszymi/spine/internal/store"
)

func TestNewTestDB(t *testing.T) {
	db := harness.NewTestDB(t)

	if db.Store == nil {
		t.Fatal("expected non-nil store")
	}

	ctx := context.Background()
	if err := db.Ping(ctx); err != nil {
		t.Fatalf("ping failed: %v", err)
	}
}

func TestCleanup(t *testing.T) {
	db := harness.NewTestDB(t)
	ctx := context.Background()
	defer db.Cleanup(ctx, t)

	// Insert a run to verify cleanup removes it.
	run := &domain.Run{
		RunID:        "test-cleanup-run",
		TaskPath:     "tasks/test.md",
		WorkflowPath: "workflows/test.yaml",
		WorkflowID:   "test-workflow",
		Status:       domain.RunStatusPending,
	}
	if err := db.Store.CreateRun(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}

	// Verify run exists.
	got, err := db.Store.GetRun(ctx, "test-cleanup-run")
	if err != nil {
		t.Fatalf("get run before cleanup: %v", err)
	}
	if got.RunID != "test-cleanup-run" {
		t.Fatalf("expected test-cleanup-run, got %s", got.RunID)
	}

	// Run cleanup.
	db.Cleanup(ctx, t)

	// Verify run is gone.
	_, err = db.Store.GetRun(ctx, "test-cleanup-run")
	if err == nil {
		t.Error("expected run to be cleaned up")
	}
}

func TestSchemaMatchesProduction(t *testing.T) {
	db := harness.NewTestDB(t)
	ctx := context.Background()
	defer db.Cleanup(ctx, t)

	// Verify key tables exist by performing operations against them.
	// This confirms migrations were applied and schema matches production.

	// Runtime tables
	run := &domain.Run{
		RunID:        "test-schema-run",
		TaskPath:     "tasks/schema-test.md",
		WorkflowPath: "workflows/task-default.yaml",
		WorkflowID:   "task-default",
		Status:       domain.RunStatusPending,
	}
	if err := db.Store.CreateRun(ctx, run); err != nil {
		t.Fatalf("create run (runtime.runs table): %v", err)
	}

	// Projection tables
	proj := &store.ArtifactProjection{
		ArtifactPath: "governance/test.md",
		ArtifactType: "Governance",
		Title:        "Test",
		Status:       "Draft",
		SourceCommit: "abc123",
		ContentHash:  "hash123",
	}
	if err := db.Store.UpsertArtifactProjection(ctx, proj); err != nil {
		t.Fatalf("upsert projection (projection.artifacts table): %v", err)
	}
}
