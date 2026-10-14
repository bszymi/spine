package git_test

// INIT-023 TASK-005: *git.CLIClient as engine.CommitBinder.
//
//   - AC 14: the shared conformance suite (core/engine/enginetest) against
//     CLIClient, the same suite SMP TASK-009 runs against its operator.
//   - AC 13 (amended by DC-1): PushCommit's argv carries no force of any
//     kind, and grammar refusals start no git process.
//   - Merge with SourceSHA honours it; with an empty SourceSHA the
//     existing Merge argv is unchanged (TestRefGrammar_ArgvShape).

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/engine"
	"github.com/bszymi/spine/core/engine/enginetest"
	"github.com/bszymi/spine/testutil"
)

var _ engine.CommitBinder = (*git.CLIClient)(nil)

// cliBinderEnv is the CLIClient harness: the binder's repository is the
// suite's Source and Local, with a bare origin as the push remote.
func cliBinderEnv(t *testing.T) enginetest.Env {
	t.Helper()
	testutil.HermeticGit(t)
	repo := testutil.NewTempRepo(t)
	origin := filepath.Join(t.TempDir(), "origin.git")
	runGit(t, repo, "init", "-q", "--bare", origin)
	runGit(t, repo, "remote", "add", "origin", origin)
	return enginetest.Env{
		Binder:    git.NewCLIClient(repo),
		Source:    repo,
		Remote:    "origin",
		RemoteDir: origin,
		Local:     repo,
	}
}

func TestCommitBinder_CLIClientConformance(t *testing.T) {
	enginetest.RunCommitBinderConformance(t, enginetest.Harness{New: cliBinderEnv})
}

// TestCommitBinder_PushCommitArgvHasNoForce is AC 13's argv case and
// mutation M10's catcher: the push is exactly
// `push --end-of-options <remote> <sha>^{commit}:refs/heads/<n>`.
func TestCommitBinder_PushCommitArgvHasNoForce(t *testing.T) {
	c, _, _, mainOID, _ := grammarRepo(t)
	rec := testutil.InstallGitRecorder(t)
	ctx := context.Background()
	if err := c.PushCommit(ctx, "origin", "main", mainOID); err != nil {
		t.Fatalf("PushCommit: %v", err)
	}
	var push []string
	for _, call := range rec.Calls(t) {
		for i, a := range call {
			if a == "push" {
				push = call[i:]
			}
		}
	}
	want := []string{"push", "--end-of-options", "origin", mainOID + "^{commit}:refs/heads/main"}
	if strings.Join(push, " ") != strings.Join(want, " ") {
		t.Fatalf("PushCommit argv = %q, want %q", push, want)
	}
	for _, a := range push {
		if strings.HasPrefix(a, "--force") || strings.HasPrefix(a, "+") || a == "-f" {
			t.Fatalf("PushCommit argv carries a force: %q", push)
		}
	}
}

// TestCommitBinder_RefusalsStartNoGit: malformed object ids, remotes and
// branches are refused before any git process (AC 13 last bullet, AC 14
// "before shelling out").
func TestCommitBinder_RefusalsStartNoGit(t *testing.T) {
	c, _, _, mainOID, _ := grammarRepo(t)
	rec := testutil.InstallGitRecorder(t)
	ctx := context.Background()
	calls := []struct {
		name string
		run  func() error
	}{
		{"PushCommit sha", func() error { return c.PushCommit(ctx, "origin", "main", "abc") }},
		{"PushCommit uppercase sha", func() error { return c.PushCommit(ctx, "origin", "main", strings.ToUpper(mainOID)) }},
		{"PushCommit remote", func() error { return c.PushCommit(ctx, "--upload-pack=x", "main", mainOID) }},
		{"PushCommit branch", func() error { return c.PushCommit(ctx, "origin", "refs/tags/x", mainOID) }},
		{"ResolveBranchHead", func() error { _, err := c.ResolveBranchHead(ctx, "-x"); return err }},
		{"ResolveBranchHead tag", func() error { _, err := c.ResolveBranchHead(ctx, "refs/tags/t1"); return err }},
		{"VerifyCommit", func() error { return c.VerifyCommit(ctx, "--upload-pack=x") }},
		{"IsAncestor", func() error { _, err := c.IsAncestor(ctx, mainOID[:7], mainOID); return err }},
		{"CommitParents", func() error { _, err := c.CommitParents(ctx, "main"); return err }},
		{"CommitFilesOnto base", func() error {
			_, err := c.CommitFilesOnto(ctx, "main", map[string]string{"a": "b"}, git.CommitOpts{Message: "m"})
			return err
		}},
		{"CommitFilesOnto path", func() error {
			_, err := c.CommitFilesOnto(ctx, mainOID, map[string]string{"../x": "b"}, git.CommitOpts{Message: "m"})
			return err
		}},
		{"MergeBound sha", func() error {
			_, err := c.MergeBound(ctx, git.MergeOpts{SourceSHA: "main", Target: "main", Strategy: "merge-commit"})
			return err
		}},
		{"Merge SourceSHA", func() error {
			_, err := c.Merge(ctx, git.MergeOpts{SourceSHA: mainOID[:12], Target: "main", Strategy: "merge-commit"})
			return err
		}},
		{"ResetBranch branch", func() error { return c.ResetBranch(ctx, "--orphan", mainOID, mainOID) }},
		{"ResetBranch target", func() error { return c.ResetBranch(ctx, "main", "HEAD~1", mainOID) }},
		{"ResetBranch expected", func() error { return c.ResetBranch(ctx, "main", mainOID, strings.ToUpper(mainOID)) }},
	}
	for _, tc := range calls {
		rec.Reset(t)
		err := tc.run()
		if !errors.Is(err, git.ErrInvalidRef) {
			t.Errorf("%s: err = %v, want ErrInvalidRef", tc.name, err)
		}
		if got := rec.Calls(t); len(got) != 0 {
			t.Errorf("%s: refused call started git: %q", tc.name, got)
		}
	}
}

// TestCommitBinder_MergeHonoursSourceSHA: Merge with SourceSHA merges
// that commit, not Source's tip, and reports BaseSHA.
func TestCommitBinder_MergeHonoursSourceSHA(t *testing.T) {
	c, repo, _, mainOID, featOID := grammarRepo(t)
	ctx := context.Background()
	runGit(t, repo, "switch", "-q", "feat")
	testutil.WriteFile(t, repo, "later.txt", "later\n")
	runGit(t, repo, "add", "later.txt")
	runGit(t, repo, "commit", "-qm", "later")
	runGit(t, repo, "switch", "-q", "main")
	tip := runGit(t, repo, "rev-parse", "refs/heads/feat")

	res, err := c.Merge(ctx, git.MergeOpts{Source: "feat", SourceSHA: featOID, Target: "main",
		Strategy: "merge-commit", Message: "Merge exact"})
	if err != nil {
		t.Fatalf("Merge(SourceSHA): %v", err)
	}
	if res.BaseSHA != mainOID {
		t.Fatalf("BaseSHA = %s, want %s", res.BaseSHA, mainOID)
	}
	parents := strings.Fields(runGit(t, repo, "rev-list", "--parents", "-n", "1", res.SHA))
	if len(parents) != 3 || parents[1] != mainOID || parents[2] != featOID {
		t.Fatalf("merge parents = %v, want [%s %s]", parents[1:], mainOID, featOID)
	}
	if _, err := c.ReadFile(ctx, "main", "later.txt"); err == nil {
		t.Fatalf("Merge(SourceSHA) merged Source's tip %s", tip)
	}
}

// TestCommitBinder_VerifyCommitErrorKinds: an id that is not a commit is
// a non-retryable ErrKindNotFound, decided by rev-parse's exit status
// (git still prints a reason for a tree id under --quiet).
func TestCommitBinder_VerifyCommitErrorKinds(t *testing.T) {
	c, repo, _, mainOID, _ := grammarRepo(t)
	ctx := context.Background()
	tree := runGit(t, repo, "rev-parse", "main^{tree}")
	var ge *git.GitError
	if err := c.VerifyCommit(ctx, tree); !errors.As(err, &ge) || ge.Kind != git.ErrKindNotFound || ge.IsRetryable() {
		t.Fatalf("VerifyCommit(tree) = %v, want a non-retryable ErrKindNotFound", err)
	}
	if err := c.VerifyCommit(ctx, mainOID); err != nil {
		t.Fatalf("VerifyCommit(main) = %v", err)
	}
}

func readRepoFile(t *testing.T, repo, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repo, rel)) //nolint:gosec // G304: test fixture path
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// refusedMerge merges feat into main (a stand-in for a merge the engine
// refused) and returns the merge commit; main stays checked out.
func refusedMerge(t *testing.T, c *git.CLIClient, featOID string) git.MergeResult {
	t.Helper()
	res, err := c.MergeBound(context.Background(), git.MergeOpts{SourceSHA: featOID, Target: "main", Strategy: "merge-commit", Message: "Merge refused"})
	if err != nil {
		t.Fatalf("MergeBound: %v", err)
	}
	return res
}

// TestCommitBinder_ResetBranchRestoresTheCheckedOutTree: the take-back of
// a refused merge on the checked-out branch (TASK-005 review round 1)
// moves the ref, the index and the working tree together.
func TestCommitBinder_ResetBranchRestoresTheCheckedOutTree(t *testing.T) {
	c, repo, _, mainOID, featOID := grammarRepo(t)
	res := refusedMerge(t, c, featOID)
	if err := c.ResetBranch(context.Background(), "main", mainOID, res.SHA); err != nil {
		t.Fatalf("ResetBranch: %v", err)
	}
	if got := runGit(t, repo, "rev-parse", "refs/heads/main"); got != mainOID {
		t.Fatalf("main = %s, want %s", got, mainOID)
	}
	if st := runGit(t, repo, "status", "--porcelain"); st != "" {
		t.Fatalf("working tree not restored:\n%s", st)
	}
	if got := readRepoFile(t, repo, "f.txt"); got != "base\n" {
		t.Fatalf("f.txt = %q, want the restored base content", got)
	}
	if got := runGit(t, repo, "symbolic-ref", "HEAD"); got != "refs/heads/main" {
		t.Fatalf("HEAD = %s, want it still on main", got)
	}
}

// TestCommitBinder_ResetBranchKeepsLocalChanges: a local change the
// switch would overwrite refuses the take-back, and the ref is moved
// forward again, so the branch, index and working tree never disagree
// and the change is kept.
func TestCommitBinder_ResetBranchKeepsLocalChanges(t *testing.T) {
	c, repo, _, mainOID, featOID := grammarRepo(t)
	res := refusedMerge(t, c, featOID)
	testutil.WriteFile(t, repo, "f.txt", "someone's edit\n")
	if err := c.ResetBranch(context.Background(), "main", mainOID, res.SHA); err == nil {
		t.Fatal("ResetBranch over a conflicting local change: want an error")
	}
	if got := runGit(t, repo, "rev-parse", "refs/heads/main"); got != res.SHA {
		t.Fatalf("main = %s, want it back at the merge %s", got, res.SHA)
	}
	if got := readRepoFile(t, repo, "f.txt"); got != "someone's edit\n" {
		t.Fatalf("local change lost: f.txt = %q", got)
	}
	if out := runGit(t, repo, "diff", "--cached", "--name-only", "HEAD"); out != "" {
		t.Fatalf("index disagrees with HEAD after the refusal: %s", out)
	}
}

// TestCommitBinder_ResetBranchCompareAndSwap: a branch not at the
// expected commit is left alone; a branch that is not checked out moves
// without touching the working tree.
func TestCommitBinder_ResetBranchCompareAndSwap(t *testing.T) {
	c, repo, _, mainOID, featOID := grammarRepo(t)
	ctx := context.Background()
	res := refusedMerge(t, c, featOID)
	if err := c.ResetBranch(ctx, "main", mainOID, featOID); err == nil {
		t.Fatal("ResetBranch with a stale expected value: want an error")
	}
	if got := runGit(t, repo, "rev-parse", "refs/heads/main"); got != res.SHA {
		t.Fatalf("main moved to %s on a compare-and-swap mismatch", got)
	}
	runGit(t, repo, "branch", "side", res.SHA)
	if err := c.ResetBranch(ctx, "side", mainOID, res.SHA); err != nil {
		t.Fatalf("ResetBranch(side): %v", err)
	}
	if got := runGit(t, repo, "rev-parse", "refs/heads/side"); got != mainOID {
		t.Fatalf("side = %s, want %s", got, mainOID)
	}
	if got := readRepoFile(t, repo, "f.txt"); got != "feat\n" {
		t.Fatalf("the checked-out main's tree changed: f.txt = %q", got)
	}
	if st := runGit(t, repo, "status", "--porcelain"); st != "" {
		t.Fatalf("working tree changed:\n%s", st)
	}
}
