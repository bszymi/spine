//go:build scenario

package harness_test

import (
	"context"
	"testing"

	"github.com/bszymi/spine/internal/scenariotest/harness"
)

func TestNewTestEnvironment_Bare(t *testing.T) {
	env := harness.NewTestEnvironment(t)

	if env.Repo == nil {
		t.Error("expected non-nil Repo")
	}
	if env.DB == nil {
		t.Error("expected non-nil DB")
	}
	if env.Runtime == nil {
		t.Error("expected non-nil Runtime")
	}

	// Bare environment has no governance files.
	if env.Repo.FileExists("governance/charter.md") {
		t.Error("bare environment should not have governance files")
	}
}

func TestNewTestEnvironment_Seeded(t *testing.T) {
	env := harness.NewTestEnvironment(t, harness.Seeded()...)

	// Governance should be seeded.
	if !env.Repo.FileExists("governance/charter.md") {
		t.Error("expected charter.md after Seeded()")
	}
	if !env.Repo.FileExists("governance/constitution.md") {
		t.Error("expected constitution.md after Seeded()")
	}

	// Workflows should be seeded.
	if !env.Repo.FileExists("workflows/task-default.yaml") {
		t.Error("expected task-default.yaml after Seeded()")
	}
}

func TestNewTestEnvironment_WithGovernanceOnly(t *testing.T) {
	env := harness.NewTestEnvironment(t, harness.WithGovernance())

	if !env.Repo.FileExists("governance/charter.md") {
		t.Error("expected charter.md with WithGovernance()")
	}
	// Workflows should NOT be seeded.
	if env.Repo.FileExists("workflows/task-default.yaml") {
		t.Error("workflows should not be seeded with WithGovernance() alone")
	}
}

func TestNewTestEnvironment_WithRuntimeOptions(t *testing.T) {
	env := harness.NewTestEnvironment(t,
		harness.WithRuntimeEvents(),
		harness.WithRuntimeValidation(),
	)

	if env.Runtime.Events == nil {
		t.Error("expected non-nil Events with WithRuntimeEvents()")
	}
	if env.Runtime.Validator == nil {
		t.Error("expected non-nil Validator with WithRuntimeValidation()")
	}
}

func TestNewTestEnvironment_EndToEnd(t *testing.T) {
	env := harness.NewTestEnvironment(t, harness.Seeded()...)
	ctx := context.Background()

	// Create artifact through runtime.
	content := `---
type: Governance
title: Env Test
status: Draft
---

# Env Test
`
	_, err := env.Runtime.Artifacts.Create(ctx, "governance/env-test.md", content)
	if err != nil {
		t.Fatalf("create artifact: %v", err)
	}

	// Sync and verify.
	if err := env.Runtime.Projections.FullRebuild(ctx); err != nil {
		t.Fatalf("full rebuild: %v", err)
	}

	proj, err := env.Runtime.Store.GetArtifactProjection(ctx, "governance/env-test.md")
	if err != nil {
		t.Fatalf("get projection: %v", err)
	}
	if proj.Title != "Env Test" {
		t.Errorf("expected 'Env Test', got %q", proj.Title)
	}
}
