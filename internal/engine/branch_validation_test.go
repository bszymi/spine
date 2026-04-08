package engine

import (
	"context"
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/testutil"
)

func TestValidateBranch_NonPlanningRun(t *testing.T) {
	run := &domain.Run{
		RunID:      "r1",
		Mode:       domain.RunModeStandard,
		BranchName: "test-branch",
	}
	_, err := ValidateBranch(context.Background(), nil, run)
	if err == nil {
		t.Error("expected error for non-planning run")
	}
}

func TestValidateBranch_NoBranch(t *testing.T) {
	run := &domain.Run{
		RunID: "r1",
		Mode:  domain.RunModePlanning,
	}
	_, err := ValidateBranch(context.Background(), nil, run)
	if err == nil {
		t.Error("expected error for run with no branch")
	}
}

func TestValidateBranch_DiscoverAndValidate(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	client := git.NewCLIClient(repo)
	ctx := context.Background()

	// Create a base commit on main.
	testutil.WriteFile(t, repo, "README.md", "# Test\n")
	testutil.GitAdd(t, repo, ".", "initial commit")

	// Create a branch with artifacts.
	testutil.GitCheckout(t, repo, "-b", "test-branch")
	testutil.WriteFile(t, repo, "initiatives/init-001/initiative.md",
		"---\nid: INIT-001\ntype: Initiative\ntitle: Test\nstatus: Draft\ncreated: 2026-01-01\nlast_updated: 2026-01-01\n---\n# INIT-001\n")
	testutil.WriteFile(t, repo, "initiatives/init-001/epics/epic-001/epic.md",
		"---\nid: EPIC-001\ntype: Epic\ntitle: Test Epic\nstatus: Draft\ninitiative: /initiatives/init-001/initiative.md\ncreated: 2026-01-01\nlast_updated: 2026-01-01\n---\n# EPIC-001\n")
	testutil.GitAdd(t, repo, ".", "add artifacts on branch")

	run := &domain.Run{
		RunID:      "r1",
		Mode:       domain.RunModePlanning,
		BranchName: "test-branch",
	}

	result, err := ValidateBranch(ctx, client, run)
	if err != nil {
		t.Fatalf("ValidateBranch: %v", err)
	}

	if result.TotalArtifacts != 2 {
		t.Errorf("expected 2 artifacts, got %d", result.TotalArtifacts)
	}
	if result.Passed != 2 {
		t.Errorf("expected 2 passed, got %d", result.Passed)
	}
	if result.Failed != 0 {
		t.Errorf("expected 0 failed, got %d", result.Failed)
		for _, d := range result.Details {
			t.Logf("  %s: %v", d.Path, d.Errors)
		}
	}
}

func TestValidateBranch_EmptyBranch(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	client := git.NewCLIClient(repo)
	ctx := context.Background()

	// Create base and branch with no artifact changes.
	testutil.WriteFile(t, repo, "README.md", "# Test\n")
	testutil.GitAdd(t, repo, ".", "initial")
	testutil.GitCheckout(t, repo, "-b", "empty-branch")
	testutil.WriteFile(t, repo, "notes.txt", "just a note\n")
	testutil.GitAdd(t, repo, ".", "add non-artifact")

	run := &domain.Run{
		RunID:      "r1",
		Mode:       domain.RunModePlanning,
		BranchName: "empty-branch",
	}

	result, err := ValidateBranch(ctx, client, run)
	if err != nil {
		t.Fatalf("ValidateBranch: %v", err)
	}

	if result.Failed != 1 {
		t.Errorf("expected 1 failure (empty branch), got %d", result.Failed)
	}
}
