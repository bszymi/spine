package artifact_test

// INIT-024 TASK-011 delta review (round 2, P3-A): under an in-flight
// checkout ceiling a branch scope is attached to its branch without a
// checkout (`git symbolic-ref`, after the worktree was filled at the
// weighed commit). The artifact service's branch-scoped write must still
// commit onto the run branch and push it, as it does with `switch`.

import (
	"context"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/artifact"
	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/branchprotect/config"
	"github.com/bszymi/spine/testutil"
)

func TestCreate_UnderACeilingTheWriteIsCommittedAndPushedOnTheBranch(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "")
	repo := testutil.NewTempRepo(t)
	bare := addBareRemote(t, repo)
	run := "spine/run/ceiling-1a2b3c4d"
	claimGit(t, repo, nil, "branch", run, "main")
	before := claimGit(t, repo, nil, "rev-parse", run)
	if err := git.EnsureNeutralCheckoutAttributes(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
	// What `spine serve` installs: the budget, the mutate deadline and a
	// ceiling.
	b := git.DefaultTreeBudget()
	git.SetDefaultCheckoutLimits(git.CheckoutLimits{Budget: &b, MutateTimeout: git.DefaultMutateTimeout, InflightMaxBytes: 1 << 30})
	t.Cleanup(func() { git.SetDefaultCheckoutLimits(git.CheckoutLimits{}) })
	t.Setenv("TMPDIR", t.TempDir())

	svc := artifact.NewService(git.NewCLIClient(repo), &captureEvents{}, repo)
	svc.WithPolicy(branchprotect.New(branchprotect.StaticRules([]config.Rule{})))
	ctx, cancel := context.WithTimeout(artifact.WithWriteContext(operatorCtx(), artifact.WriteContext{Branch: run}), 30*time.Second)
	defer cancel()
	res, err := svc.Create(ctx, "governance/ceiling.md", governanceContent)
	if err != nil {
		t.Fatalf("Create on the run branch under a ceiling: %v", err)
	}
	if got := claimGit(t, repo, nil, "rev-parse", run); got != res.CommitSHA {
		t.Errorf("%s = %s, want the write's commit %s", run, got, res.CommitSHA)
	}
	if parent := claimGit(t, repo, nil, "rev-parse", res.CommitSHA+"^"); parent != before {
		t.Errorf("the write's commit has parent %s, want the branch's previous tip %s", parent, before)
	}
	if got := claimGit(t, repo, nil, "ls-tree", "--name-only", run, "governance/"); got != "governance/ceiling.md" {
		t.Errorf("files under governance/ on %s = %q", run, got)
	}
	if got := claimGit(t, bare, nil, "rev-parse", "refs/heads/"+run); got != res.CommitSHA {
		t.Errorf("remote %s = %s, want the write's commit %s", run, got, res.CommitSHA)
	}
	// The scope was released: the branch can be entered again at once.
	again, err := git.EnterBranch(ctx, repo, run, nil)
	if err != nil {
		t.Fatalf("entering %s after the write: %v", run, err)
	}
	again.Cleanup()
}
