package engine

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/artifact"
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
// ChangeSet.ReadErrors field. Its fixture was a planning branch carrying
// a gitlink named x.md whose commit is absent (which `git show` refused
// as "bad object"): the read error was skipped and the branch validated.
//
// Since the INIT-023 wave 4 follow-up the git client refuses a gitlink as
// not a file (git.ErrNotBlob) and discovery records it as a not_a_file
// refusal, which — like TASK-003's other refusals — fails validation: a
// gitlink .md was never read, so the branch cannot be vouched for, and it
// would never be projected after the merge. The readable artifact is
// still validated next to it; ReadErrors are still ignored
// (TestValidateBranch_ReadErrorsAreIgnored keeps AC 28's own claim).
func TestValidateBranch_GitlinkFailsAsNotAFile(t *testing.T) {
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
	if result.TotalArtifacts != 2 || result.Passed != 1 || result.Failed != 1 || len(result.Details) != 1 {
		t.Fatalf("result = %+v, want the artifact passed and the gitlink failed", result)
	}
	if d := result.Details[0]; d.Path != "x.md" || len(d.Errors) != 1 || !strings.Contains(d.Errors[0], "gitlink") {
		t.Fatalf("detail = %+v, want x.md not validated as a gitlink", d)
	}
}

// readFailingGit fails every ReadFile of one path with a plain git error
// (a read error, not a refusal) and otherwise is the real client.
type readFailingGit struct {
	git.GitClient
	path string
}

func (g readFailingGit) ReadFile(ctx context.Context, ref, path string) ([]byte, error) {
	if path == g.path {
		return nil, &git.GitError{Kind: git.ErrKindPermanent, Op: "read_file", Message: "fatal: simulated"}
	}
	return g.GitClient.ReadFile(ctx, ref, path)
}

// INIT-023 TASK-011 AC 28, as it stands after the wave 4 follow-up:
// branch validation ignores ChangeSet.ReadErrors — a changed .md whose
// read fails is skipped and the readable artifact is validated, exactly
// as before TASK-011.
func TestValidateBranch_ReadErrorsAreIgnored(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	ctx := context.Background()
	testutil.WriteFile(t, repo, "README.md", "# Test\n")
	testutil.GitAdd(t, repo, ".", "initial commit")
	testutil.GitCheckout(t, repo, "-b", "test-branch")
	testutil.WriteFile(t, repo, "initiatives/init-001/initiative.md",
		"---\nid: INIT-001\ntype: Initiative\ntitle: Test\nstatus: Draft\ncreated: 2026-01-01\nlast_updated: 2026-01-01\n---\n# INIT-001\n")
	testutil.WriteFile(t, repo, "x.md", "# unreadable\n")
	testutil.GitAdd(t, repo, ".", "add artifact and x.md")

	client := readFailingGit{GitClient: git.NewCLIClient(repo), path: "x.md"}
	result, err := ValidateBranch(ctx, client, &domain.Run{RunID: "r1", Mode: domain.RunModePlanning, BranchName: "test-branch"})
	if err != nil {
		t.Fatalf("ValidateBranch: %v", err)
	}
	if result.TotalArtifacts != 1 || result.Passed != 1 || result.Failed != 0 {
		t.Fatalf("result = %+v, want 1 artifact passed (the unreadable file skipped)", result)
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

// Review round 1 (P3): a file git refused to produce under its
// allocation limit is reported with that limit, not as "over" a per-blob
// read limit it may well be under.
func TestRefusalReport_NamesTheLimitThatRefused(t *testing.T) {
	alloc := refusalReport(artifact.RefusedFile{Path: "x.md", Reason: artifact.RefusedOversized,
		Size: -1, Limit: 4 << 20, GitAllocLimit: 16 << 20})
	if !strings.Contains(alloc, "16777216-byte git allocation limit") || strings.Contains(alloc, "4194304") {
		t.Errorf("alloc refusal report = %q, want the git allocation limit and not the read limit", alloc)
	}
	size := refusalReport(artifact.RefusedFile{Path: "x.md", Reason: artifact.RefusedOversized, Size: 5 << 20, Limit: 4 << 20})
	if !strings.Contains(size, "5242880 bytes, over the 4194304-byte git read limit") {
		t.Errorf("size refusal report = %q", size)
	}
}
