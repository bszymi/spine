package git_test

// INIT-024 TASK-011 delta review (round 2): how a branch scope under an
// in-flight ceiling is attached to its branch (P3-A) and how long its
// admission may wait in all (P4-1).

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/testutil"
)

// TestEnterBranch_CeilingAttachesWithoutACheckout (P3-A): with a ceiling
// the worktree, filled at the weighed commit, is attached to its branch by
// `git symbolic-ref HEAD refs/heads/<n>` — no `switch`, so no checkout —
// and is then exactly what a caller had before: on the branch, clean, and
// a commit made in it moves the branch. With no ceiling the attach is the
// `switch` it always was.
func TestEnterBranch_CeilingAttachesWithoutACheckout(t *testing.T) {
	b := git.DefaultTreeBudget()
	for _, mode := range []struct {
		name      string
		limits    git.CheckoutLimits
		symbolic  bool
		switching bool
	}{
		{"serve limits", git.CheckoutLimits{Budget: &b, MutateTimeout: git.DefaultMutateTimeout, InflightMaxBytes: 1 << 30}, true, false},
		{"ceiling only", git.CheckoutLimits{InflightMaxBytes: 1 << 30}, true, false},
		{"budget, no ceiling", git.CheckoutLimits{Budget: &b, MutateTimeout: git.DefaultMutateTimeout}, false, true},
		{"library default", git.CheckoutLimits{}, false, true},
	} {
		t.Run(mode.name, func(t *testing.T) {
			repo, run := claimRepo(t)
			tip := tbGit(t, repo, nil, "rev-parse", run)
			tmp := isolateTempDir(t)
			calls, _ := installGitCallLog(t)

			s, err := git.EnterBranchWithLimits(boundedCtx(t, 30*time.Second), repo, run, nil, mode.limits)
			if err != nil {
				t.Fatal(err)
			}
			cleanupAtEnd(t, s)
			got := calls()
			if has := slices.Contains(got, "symbolic-ref HEAD"); has != mode.symbolic {
				t.Errorf("symbolic-ref HEAD run = %v, want %v: %q", has, mode.symbolic, got)
			}
			if has := slices.Contains(got, "switch --no-guess"); has != mode.switching {
				t.Errorf("switch run = %v, want %v: %q", has, mode.switching, got)
			}
			if has := slices.Contains(got, "worktree list"); has != mode.symbolic {
				t.Errorf("worktree list run = %v, want %v (the other-worktree check): %q", has, mode.symbolic, got)
			}

			if br, err := git.CurrentBranch(context.Background(), s.RepoDir); err != nil || br != run {
				t.Errorf("CurrentBranch = %q, %v; want %s", br, err, run)
			}
			if st := tbGit(t, s.RepoDir, nil, "status", "--porcelain"); st != "" {
				t.Errorf("the worktree is not clean on its branch: %q", st)
			}
			testutil.WriteFile(t, s.RepoDir, "notes/new.md", "new\n")
			tbGit(t, s.RepoDir, nil, "add", "notes/new.md")
			tbGit(t, s.RepoDir, nil, "commit", "-q", "-m", "in scope")
			moved := tbGit(t, repo, nil, "rev-parse", run)
			if moved == tip || tbGit(t, repo, nil, "rev-parse", moved+"^") != tip {
				t.Errorf("a commit in the scope did not move %s from %s (now %s)", run, tip, moved)
			}
			if f := tbGit(t, repo, nil, "ls-tree", "--name-only", "-r", run, "notes/"); f != "notes/new.md" {
				t.Errorf("committed file on the branch = %q", f)
			}
			s.Cleanup()
			assertEmptyDir(t, tmp)
			if h := git.InflightHeldForTest(); h != 0 {
				t.Errorf("gate holds %d, want 0", h)
			}
		})
	}
}

// TestEnterBranch_BranchInAnotherWorktreeIsRefused (P3-A): the attach
// without a checkout keeps git's "already used by worktree" guard. With a
// ceiling, a branch checked out in a worktree this process did not create
// is refused with an error matching ErrBranchInUse — not a *GitError, like
// git's refusal it stands in for, and naming no path — leaving nothing:
// no directory, no registration, no weight, no claim. Without a ceiling
// git's own refusal is unchanged.
func TestEnterBranch_BranchInAnotherWorktreeIsRefused(t *testing.T) {
	repo, run := claimRepo(t)
	ext := filepath.Join(t.TempDir(), "ext")
	tbGit(t, repo, nil, "worktree", "add", "-q", ext, run)
	b := git.DefaultTreeBudget()
	ceiling := git.CheckoutLimits{Budget: &b, MutateTimeout: git.DefaultMutateTimeout, InflightMaxBytes: 1 << 30}
	tmp := isolateTempDir(t)

	s, err := git.EnterBranchWithLimits(boundedCtx(t, 30*time.Second), repo, run, nil, ceiling)
	if err == nil {
		s.Cleanup()
		t.Fatal("a branch checked out in another worktree was entered")
	}
	if !errors.Is(err, git.ErrBranchInUse) {
		t.Errorf("err = %v, want ErrBranchInUse", err)
	}
	var ge *git.GitError
	if errors.As(err, &ge) {
		t.Errorf("err = %#v is a *GitError; git's refusal it stands in for was untyped", err)
	}
	if msg := err.Error(); !strings.HasPrefix(msg, "add worktree for branch "+run+": ") || !strings.Contains(msg, "another worktree") ||
		strings.Contains(msg, ext) || strings.Contains(msg, tmp) {
		t.Errorf("message %q: want the add-worktree prefix, the other worktree, and no path", msg)
	}
	assertEmptyDir(t, tmp)
	if n := worktreeCount(t, repo); n != 2 {
		t.Errorf("worktree list has %d entries, want 2 (main and the other)", n)
	}
	if h, r := git.InflightHeldForTest(), git.InflightReposForTest(); h != 0 || r != 0 {
		t.Errorf("gate holds %d over %d repositories, want an empty gate", h, r)
	}

	if s, err := git.EnterBranchWithLimits(boundedCtx(t, 30*time.Second), repo, run, nil, git.CheckoutLimits{}); err == nil || errors.Is(err, git.ErrBranchInUse) {
		if err == nil {
			s.Cleanup()
		}
		t.Errorf("no ceiling: err = %v, want git's own refusal", err)
	}
	assertEmptyDir(t, tmp)

	tbGit(t, repo, nil, "worktree", "remove", "--force", ext)
	s, err = git.EnterBranchWithLimits(boundedCtx(t, 30*time.Second), repo, run, nil, ceiling)
	if err != nil {
		t.Fatalf("the refused entry kept the claim or the weight: %v", err)
	}
	s.Cleanup()
	if _, err := os.Stat(ext); !os.IsNotExist(err) {
		t.Errorf("the other worktree is still on disk: %v", err)
	}
}

// TestEnterBranch_UnreadableWorktreeListRefusesTheEntry (P3-A, the
// round-2 mutation N06): the other-worktree check fails closed. When `git
// worktree list` fails, the entry cannot know whether another worktree
// holds its branch, so it is refused — with git's failure, not as
// ErrBranchInUse — and nothing is left: no directory, no registration, no
// weight, no claim.
func TestEnterBranch_UnreadableWorktreeListRefusesTheEntry(t *testing.T) {
	repo, run := claimRepo(t)
	b := git.DefaultTreeBudget()
	limits := git.CheckoutLimits{Budget: &b, MutateTimeout: git.DefaultMutateTimeout, InflightMaxBytes: 1 << 30}
	tmp := isolateTempDir(t)

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git not on PATH: %v", err)
	}
	if realGit, err = filepath.Abs(realGit); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = worktree ] && [ \"$2\" = list ]; then echo 'fatal: cannot list' >&2; exit 128; fi\nexec \"$FAILLIST_REAL\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil { //nolint:gosec // G306: the wrapper must be executable
		t.Fatal(err)
	}
	t.Setenv("FAILLIST_REAL", realGit)
	path := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+path)

	s, err := git.EnterBranchWithLimits(boundedCtx(t, 30*time.Second), repo, run, nil, limits)
	if err == nil {
		s.Cleanup()
		t.Fatal("entered although the other-worktree check could not run")
	}
	if errors.Is(err, git.ErrBranchInUse) || !strings.Contains(err.Error(), "list worktrees") {
		t.Errorf("err = %v, want the failed worktree listing (not ErrBranchInUse)", err)
	}
	assertEmptyDir(t, tmp)
	if h, r := git.InflightHeldForTest(), git.InflightReposForTest(); h != 0 || r != 0 {
		t.Errorf("gate holds %d over %d repositories, want an empty gate", h, r)
	}

	t.Setenv("PATH", path)
	if n := worktreeCount(t, repo); n != 1 {
		t.Errorf("worktree list has %d entries, want 1", n)
	}
	s, err = git.EnterBranchWithLimits(boundedCtx(t, 30*time.Second), repo, run, nil, limits)
	if err != nil {
		t.Fatalf("the refused entry kept the claim or the weight: %v", err)
	}
	s.Cleanup()
}

// TestEnterBranch_ReweighWaitsShareOneDeadline (P4-1): an entry whose
// branch moves at every measure is re-weighed, and each re-weigh may wait
// for room again — but every wait ends at ONE mutate deadline, taken once
// before the first. Another repository takes the room back for 1 s each
// time it frees; with a 1.5 s mutate deadline the second wait ends at the
// deadline (the retryable ErrCheckoutsInFlight), where a deadline per
// attempt let the entry wait three times (about 3 s) and hold its claim
// all along before ErrBranchMoved.
func TestEnterBranch_ReweighWaitsShareOneDeadline(t *testing.T) {
	repo, run := claimRepo(t)
	a := tbGit(t, repo, nil, "rev-parse", run)
	bTip := tbGit(t, repo, nil, "rev-parse", "main")
	weight := treeWeight(t, repo, run)
	ceiling := weight + 100
	blocker := ceiling - 50 // nothing of the repository fits beside it
	const hold, deadline = time.Second, 1500 * time.Millisecond
	b := git.DefaultTreeBudget()
	limits := git.CheckoutLimits{Budget: &b, MutateTimeout: deadline, InflightMaxBytes: ceiling}
	isolateTempDir(t)

	ctx := boundedCtx(t, 30*time.Second)
	stop := make(chan struct{})
	acquired := make(chan struct{}, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			r, err := git.AdmitCheckoutForTest(ctx, "blocker", blocker, ceiling)
			if err != nil {
				return
			}
			select {
			case acquired <- struct{}{}:
			default:
			}
			select {
			case <-stop:
				r()
				return
			case <-time.After(hold):
			}
			r()
		}
	}()
	t.Cleanup(func() {
		close(stop)
		wg.Wait()
	})
	select {
	case <-acquired:
	case <-time.After(10 * time.Second):
		t.Fatal("the blocker was never admitted")
	}

	installBranchMover(t, repo, "refs/heads/"+run, "ls-tree", "-r", bTip, a)
	begun := time.Now()
	s, err := git.EnterBranchWithLimits(ctx, repo, run, nil, limits)
	elapsed := time.Since(begun)
	if err == nil {
		s.Cleanup()
	}
	if errors.Is(err, git.ErrBranchMoved) {
		t.Fatalf("refused with ErrBranchMoved after %s: the re-weighs waited a deadline each", elapsed)
	}
	if !errors.Is(err, git.ErrCheckoutsInFlight) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v after %s, want ErrCheckoutsInFlight at the one mutate deadline", err, elapsed)
	}
	if elapsed > deadline+900*time.Millisecond {
		t.Errorf("the admission waited %s in all, over the one %s deadline", elapsed, deadline)
	}
}
