package engine

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/testutil"
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

// INIT-023 TASK-011 AC 28: branch validation ignores the new
// ChangeSet.ReadErrors field, so a planning branch carrying an
// unreadable entry (a gitlink named x.md whose commit is absent, which
// `git show` refuses as "bad object") validates exactly as before: the
// entry is skipped and the readable artifact is validated.
func TestValidateBranch_UnreadableGitlinkIsSkippedAsBefore(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	client := git.NewCLIClient(repo)
	ctx := context.Background()

	testutil.WriteFile(t, repo, "README.md", "# Test\n")
	testutil.GitAdd(t, repo, ".", "initial commit")
	testutil.GitCheckout(t, repo, "-b", "test-branch")
	testutil.WriteFile(t, repo, "initiatives/init-001/initiative.md",
		"---\nid: INIT-001\ntype: Initiative\ntitle: Test\nstatus: Draft\ncreated: 2026-01-01\nlast_updated: 2026-01-01\n---\n# INIT-001\n")
	cmd := exec.Command("git", "update-index", "--add", "--cacheinfo",
		"160000,0123456789abcdef0123456789abcdef01234567,x.md")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("add gitlink: %v\n%s", err, out)
	}
	testutil.GitAdd(t, repo, "initiatives", "add artifact and gitlink")

	result, err := ValidateBranch(ctx, client, &domain.Run{RunID: "r1", Mode: domain.RunModePlanning, BranchName: "test-branch"})
	if err != nil {
		t.Fatalf("ValidateBranch: %v", err)
	}
	if result.TotalArtifacts != 1 || result.Passed != 1 || result.Failed != 0 {
		t.Fatalf("result = %+v, want 1 artifact passed (the gitlink skipped)", result)
	}
}

// INIT-023 TASK-003 (codex review round 4): a file discovery refused —
// over the git read limit, or at an unsafe path — fails branch
// validation instead of merging unvalidated next to a valid artifact.
func TestValidateBranch_RefusedFilesFail(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	client := git.NewCLIClient(repo, git.WithMaxBlobBytes(64<<10))
	ctx := context.Background()
	testutil.WriteFile(t, repo, "README.md", "# Test\n")
	testutil.GitAdd(t, repo, ".", "initial commit")
	testutil.GitCheckout(t, repo, "-b", "refused-branch")
	testutil.WriteFile(t, repo, "initiatives/init-001/initiative.md",
		"---\nid: INIT-001\ntype: Initiative\ntitle: Test\nstatus: Draft\ncreated: 2026-01-01\nlast_updated: 2026-01-01\n---\n# INIT-001\n")
	testutil.WriteFile(t, repo, "governance/big.md",
		"---\ntype: Governance\ntitle: Big\nstatus: Living Document\n---\n\n"+strings.Repeat("x", 100<<10)+"\n")
	testutil.WriteFile(t, repo, "governance/a\tb.md", "---\ntype: Governance\ntitle: T\nstatus: Living Document\n---\n")
	testutil.GitAdd(t, repo, ".", "add artifacts on branch")

	result, err := ValidateBranch(ctx, client, &domain.Run{RunID: "r1", Mode: domain.RunModePlanning, BranchName: "refused-branch"})
	if err != nil {
		t.Fatalf("ValidateBranch: %v", err)
	}
	if result.TotalArtifacts != 3 || result.Passed != 1 || result.Failed != 2 {
		t.Fatalf("result = %+v, want 3 total, 1 passed, 2 failed", result)
	}
	got := map[string]string{}
	for _, d := range result.Details {
		got[d.Path] = strings.Join(d.Errors, "; ")
	}
	if !strings.Contains(got["governance/big.md"], "read limit") {
		t.Errorf("oversized detail = %q", got["governance/big.md"])
	}
	if !strings.Contains(got[strconv.Quote("governance/a\tb.md")], "not validated") {
		t.Errorf("unsafe-path detail missing or not quoted: %+v", got)
	}
}
