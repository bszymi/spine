//go:build scenario

package harness_test

import (
	"context"
	"testing"

	"github.com/bszymi/spine/internal/scenariotest/harness"
)

func TestNewTestRuntime_Default(t *testing.T) {
	repo := harness.NewTestRepo(t)
	db := harness.NewTestDB(t)
	rt := harness.NewTestRuntime(t, repo, db)
	defer db.Cleanup(context.Background(), t)

	if rt.Store == nil {
		t.Error("expected non-nil Store")
	}
	if rt.Artifacts == nil {
		t.Error("expected non-nil Artifacts service")
	}
	if rt.Projections == nil {
		t.Error("expected non-nil Projections service")
	}
	// Without options, these should be nil.
	if rt.Events != nil {
		t.Error("expected nil Events without WithEvents()")
	}
	if rt.Validator != nil {
		t.Error("expected nil Validator without WithValidation()")
	}
}

func TestNewTestRuntime_WithEvents(t *testing.T) {
	repo := harness.NewTestRepo(t)
	db := harness.NewTestDB(t)
	rt := harness.NewTestRuntime(t, repo, db, harness.WithEvents())
	defer db.Cleanup(context.Background(), t)

	if rt.Events == nil {
		t.Error("expected non-nil Events with WithEvents()")
	}
	if rt.Queue == nil {
		t.Error("expected non-nil Queue with WithEvents()")
	}
}

func TestNewTestRuntime_WithValidation(t *testing.T) {
	repo := harness.NewTestRepo(t)
	db := harness.NewTestDB(t)
	rt := harness.NewTestRuntime(t, repo, db, harness.WithValidation())
	defer db.Cleanup(context.Background(), t)

	if rt.Validator == nil {
		t.Error("expected non-nil Validator with WithValidation()")
	}
}

func TestNewTestRuntime_CreateArtifact(t *testing.T) {
	repo := harness.NewTestRepo(t)
	repo.SeedGovernance(t)
	db := harness.NewTestDB(t)
	rt := harness.NewTestRuntime(t, repo, db)
	defer db.Cleanup(context.Background(), t)

	ctx := context.Background()

	// Create an artifact through the runtime.
	content := `---
type: Governance
title: Runtime Test
status: Draft
---

# Runtime Test
`
	result, err := rt.Artifacts.Create(ctx, "governance/runtime-test.md", content)
	if err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	if result.Artifact.Title != "Runtime Test" {
		t.Errorf("expected title 'Runtime Test', got %q", result.Artifact.Title)
	}

	// Sync projections and verify.
	if err := rt.Projections.FullRebuild(ctx); err != nil {
		t.Fatalf("full rebuild: %v", err)
	}

	proj, err := rt.Store.GetArtifactProjection(ctx, "governance/runtime-test.md")
	if err != nil {
		t.Fatalf("get projection: %v", err)
	}
	if proj.Title != "Runtime Test" {
		t.Errorf("expected projection title 'Runtime Test', got %q", proj.Title)
	}
}
