package git_test

// INIT-023 TASK-012 review round 1: tree states the restore can never
// make clean (phantoms) must not wedge the shared tree, and a take-back a
// failed ResetBranch left undone must be settled before the tree is used
// again.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/testutil"
)

// swCommitCRLF commits, on the current branch, notes.md with CRLF line
// endings and then a .gitattributes demanding LF for *.md: the blob keeps
// its CRLF, so git reports the checked-out file modified however often it
// is rewritten (the review's line-ending phantom; it reproduces on any OS).
func swCommitCRLF(t *testing.T, repo string) {
	t.Helper()
	testutil.WriteFile(t, repo, "notes.md", "line one\r\nline two\r\n")
	swGit(t, repo, "add", "notes.md")
	swGit(t, repo, "commit", "-q", "-m", "notes with CRLF")
	testutil.WriteFile(t, repo, ".gitattributes", "*.md text eol=lf\n")
	swGit(t, repo, "add", ".gitattributes")
	swGit(t, repo, "commit", "-q", "-m", "attributes")
}

// swStale moves a file's mtime into the past, so git re-reads its content
// instead of trusting the index's stat data.
func swStale(t *testing.T, repo, rel string) {
	t.Helper()
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(repo, rel), old, old); err != nil {
		t.Fatal(err)
	}
}

func swStagedClean(t *testing.T, repo string) {
	t.Helper()
	if _, err := swTry(repo, "diff-index", "--cached", "--quiet", "HEAD", "--"); err != nil {
		t.Fatalf("the index differs from HEAD after the restore: %s", swGit(t, repo, "status", "--porcelain"))
	}
}

// A line-ending phantom on the home branch is left in place: the restore
// succeeds, sequences run, and later restores neither quarantine nor
// reset it again. A real edit to the same path is still quarantined.
func TestF9_RestoreLeavesLineEndingPhantomsInPlace(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewTempRepo(t)
	swCommitCRLF(t, repo)
	swStale(t, repo, "notes.md")
	if st, _ := swTry(repo, "status", "--porcelain", "--untracked-files=no"); st != "M notes.md" {
		t.Fatalf("precondition: status = %q, want the phantom \"M notes.md\"", st)
	}
	c := git.NewCLIClient(repo, git.WithHomeBranch("main"))

	rep, err := c.RestoreWorkingTree(ctx)
	if err != nil {
		t.Fatalf("a line-ending phantom failed the restore: %v", err)
	}
	if len(rep.Phantoms) != 1 || rep.Phantoms[0] != "notes.md" {
		t.Fatalf("report phantoms = %v, want [notes.md]", rep.Phantoms)
	}
	if swHead(t, repo) != "refs/heads/main" {
		t.Fatal("HEAD left main")
	}
	swStagedClean(t, repo)

	for i := 0; i < 3; i++ {
		ran := false
		if err := c.WithWorkingTree(ctx, func(context.Context) error { ran = true; return nil }); err != nil || !ran {
			t.Fatalf("sequence %d over the phantom: ran=%v err=%v", i, ran, err)
		}
		rep, err := c.RestoreWorkingTree(ctx)
		if err != nil || rep.QuarantineSHA != "" || rep.Repaired() {
			t.Fatalf("restore %d over a known phantom = %+v, %v; want nothing repaired and no new quarantine", i, rep, err)
		}
	}

	// An edit to the phantom path is a real change: quarantined.
	testutil.WriteFile(t, repo, "notes.md", "an operator's edit\n")
	rep, err = c.RestoreWorkingTree(ctx)
	if err != nil || rep.QuarantineSHA == "" {
		t.Fatalf("restore after a real edit of a phantom path = %+v, %v; want a quarantine", rep, err)
	}
	if got := swGit(t, repo, "show", rep.QuarantineSHA+":notes.md"); !strings.Contains(got, "an operator's edit") {
		t.Fatalf("the quarantine does not hold the edit: %q", got)
	}
	swStagedClean(t, repo)
}

// The convergence shape of the review's reproduction: a sequence merges a
// branch carrying the phantom into a run branch, so its exit restore
// finds the tree on the run branch with a change no reset removes. The
// tree still ends on a clean main, and the next sequence runs.
func TestF9_PhantomOnRunBranchNeverWedgesTheTree(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewTempRepo(t)
	swGit(t, repo, "branch", "spine/run/r1")
	swGit(t, repo, "checkout", "-q", "-b", "spine/run/r2")
	testutil.WriteFile(t, repo, "r2.md", "run two\n")
	swGit(t, repo, "add", "r2.md")
	swGit(t, repo, "commit", "-q", "-m", "r2 work")
	swGit(t, repo, "checkout", "-q", "-b", "spine/r1/div1/b1", "main")
	swCommitCRLF(t, repo)
	swGit(t, repo, "checkout", "-q", "-f", "main") // the phantom blocks a plain checkout
	c := git.NewCLIClient(repo, git.WithHomeBranch("main"))
	mainBefore := swGit(t, repo, "rev-parse", "main")

	if err := c.WithWorkingTree(ctx, func(ctx context.Context) error {
		_, err := c.Merge(ctx, git.MergeOpts{Source: "spine/r1/div1/b1", Target: "spine/run/r1", Strategy: "merge-commit", Message: "converge"})
		if err == nil {
			swStale(t, repo, "notes.md")
		}
		return err
	}); err != nil {
		t.Fatalf("convergence-shaped sequence: %v", err)
	}
	if swHead(t, repo) != "refs/heads/main" {
		t.Fatalf("tree left on %s after the phantom sequence", swHead(t, repo))
	}
	if st, _ := swTry(repo, "status", "--porcelain", "--untracked-files=no"); st != "" {
		t.Fatalf("tree not clean on main: %q", st)
	}
	if _, err := swTry(repo, "cat-file", "-e", "spine/run/r1:notes.md"); err != nil {
		t.Fatal("the convergence did not land on the run branch")
	}

	ran := false
	if err := c.WithWorkingTree(ctx, func(ctx context.Context) error {
		ran = true
		_, err := c.Merge(ctx, git.MergeOpts{Source: "spine/run/r2", Target: "main", Strategy: "merge-commit", Message: "governed"})
		return err
	}); err != nil || !ran {
		t.Fatalf("the next unrelated sequence: ran=%v err=%v", ran, err)
	}
	if got := swGit(t, repo, "rev-parse", "main"); got == mainBefore {
		t.Fatal("the governed merge after the phantom did not land")
	}
}

// caseInsensitive reports whether dir's filesystem folds case.
func caseInsensitive(t *testing.T, dir string) bool {
	t.Helper()
	p := filepath.Join(dir, "case-probe")
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(p)
	_, err := os.Stat(filepath.Join(dir, "CASE-PROBE"))
	return err == nil
}

// swCommitCaseCollision commits a.md and A.md on a new branch made from
// main with plumbing (a case-folding filesystem cannot hold both files).
func swCommitCaseCollision(t *testing.T, repo, branch string) {
	t.Helper()
	idx := filepath.Join(t.TempDir(), "index")
	env := append(os.Environ(), "GIT_INDEX_FILE="+idx)
	run := func(stdin string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = env
		if stdin != "" {
			cmd.Stdin = strings.NewReader(stdin)
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("", "read-tree", "main")
	lower := run("lower\n", "hash-object", "-w", "--stdin")
	upper := run("UPPER\n", "hash-object", "-w", "--stdin")
	run("", "update-index", "--add", "--cacheinfo", "100644,"+lower+",a.md")
	run("", "update-index", "--add", "--cacheinfo", "100644,"+upper+",A.md")
	tree := run("", "write-tree")
	commit := run("collide\n", "-c", "user.name=t", "-c", "user.email=t@example.test", "commit-tree", tree, "-p", "main")
	swGit(t, repo, "branch", branch, commit)
}

// Two paths that differ only in case, on a case-insensitive filesystem:
// one of them always reads as modified. Merged into a run branch (the
// tree then rests on main without them) and into main itself (the
// phantom then lives on main), the tree never wedges.
func TestF9_CaseCollisionPhantomNeverWedgesTheTree(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewTempRepo(t)
	if !caseInsensitive(t, repo) {
		t.Skip("needs a case-insensitive filesystem (macOS APFS default, Windows)")
	}
	swCommitCaseCollision(t, repo, "collide")
	swGit(t, repo, "branch", "spine/run/r1")
	c := git.NewCLIClient(repo, git.WithHomeBranch("main"))
	for _, target := range []string{"spine/run/r1", "main"} {
		if err := c.WithWorkingTree(ctx, func(ctx context.Context) error {
			_, err := c.Merge(ctx, git.MergeOpts{Source: "collide", Target: target, Strategy: "merge-commit", Message: "collide into " + target})
			return err
		}); err != nil {
			t.Fatalf("merge into %s: %v", target, err)
		}
		if swHead(t, repo) != "refs/heads/main" {
			t.Fatalf("after the merge into %s the tree is on %s", target, swHead(t, repo))
		}
		swStagedClean(t, repo)
		for i := 0; i < 2; i++ {
			ran := false
			if err := c.WithWorkingTree(ctx, func(context.Context) error { ran = true; return nil }); err != nil || !ran {
				t.Fatalf("after the merge into %s, sequence %d: ran=%v err=%v", target, i, ran, err)
			}
		}
	}
	rep, err := c.RestoreWorkingTree(ctx)
	if err != nil || rep.QuarantineSHA != "" {
		t.Fatalf("restore over the known case phantom on main = %+v, %v", rep, err)
	}
}

// swRefLock holds refs/heads/<branch>.lock (git's own ref lock, as a
// detached `git gc --auto` running pack-refs holds it) until released.
func swRefLock(t *testing.T, repo, branch string) (release func()) {
	t.Helper()
	p := filepath.Join(repo, ".git", "refs", "heads", branch+".lock")
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return func() { _ = os.Remove(p) }
}

// A take-back that failed is not forgotten: until it is done, no
// sequence and no in-place write runs on the tree; once the ref lock is
// gone, the next take finishes it before anything else.
func TestF9_FailedTakeBackIsSettledBeforeReuse(t *testing.T) {
	ctx := context.Background()
	t.Run("home-branch tree", func(t *testing.T) {
		repo := swFeatureRepo(t)
		base := swGit(t, repo, "rev-parse", "main")
		c := git.NewCLIClient(repo, git.WithHomeBranch("main"))
		res, err := c.MergeBound(ctx, git.MergeOpts{Source: "feature", SourceSHA: swGit(t, repo, "rev-parse", "feature"),
			Target: "main", Strategy: "merge-commit", Message: "refused"})
		if err != nil {
			t.Fatal(err)
		}
		release := swRefLock(t, repo, "main")
		defer release()
		err = c.ResetBranch(ctx, "main", base, res.SHA)
		var ge *git.GitError
		if !errors.As(err, &ge) || !ge.IsRetryable() {
			t.Fatalf("ResetBranch with main.lock held = %v, want a transient *GitError", err)
		}

		ran := false
		err = c.WithWorkingTree(ctx, func(context.Context) error { ran = true; return nil })
		var tre *git.TreeRestoreError
		if !errors.As(err, &tre) || ran || !strings.Contains(err.Error(), res.SHA) || !errors.As(err, &ge) || !ge.IsRetryable() {
			t.Fatalf("a sequence over an unfinished take-back: ran=%v err=%v; want a transient *TreeRestoreError naming %s", ran, err, res.SHA)
		}
		if scope, err := git.EnterBranch(ctx, repo, "", nil); err == nil {
			scope.Cleanup()
			t.Fatal("an in-place write scope was handed out over an unfinished take-back")
		}
		if got := swGit(t, repo, "rev-parse", "main"); got != res.SHA {
			t.Fatalf("main = %s, want the refused merge still there while the lock is held", got)
		}

		release()
		ran = false
		if err := c.WithWorkingTree(ctx, func(context.Context) error { ran = true; return nil }); err != nil || !ran {
			t.Fatalf("the sequence after the lock went away: ran=%v err=%v", ran, err)
		}
		if got := swGit(t, repo, "rev-parse", "main"); got != base {
			t.Fatalf("main = %s after the settle, want the base %s", got, base)
		}
		if rep, err := c.RestoreWorkingTree(ctx); err != nil || rep.Repaired() {
			t.Fatalf("restore after the settle = %+v, %v; want nothing left to do", rep, err)
		}
	})
	t.Run("moved on outside the server: refused until reset by hand", func(t *testing.T) {
		repo := swFeatureRepo(t)
		base := swGit(t, repo, "rev-parse", "main")
		c := git.NewCLIClient(repo, git.WithHomeBranch("main"))
		res, err := c.MergeBound(ctx, git.MergeOpts{Source: "feature", SourceSHA: swGit(t, repo, "rev-parse", "feature"),
			Target: "main", Strategy: "merge-commit", Message: "refused"})
		if err != nil {
			t.Fatal(err)
		}
		release := swRefLock(t, repo, "main")
		_ = c.ResetBranch(ctx, "main", base, res.SHA)
		release()
		// Another process commits on top of the refused merge.
		testutil.WriteFile(t, repo, "other.md", "other\n")
		swGit(t, repo, "add", "other.md")
		swGit(t, repo, "commit", "-q", "-m", "outside")
		ran := false
		err = c.WithWorkingTree(ctx, func(context.Context) error { ran = true; return nil })
		var ge *git.GitError
		if ran || !errors.As(err, &ge) || ge.IsRetryable() || !strings.Contains(err.Error(), "still contains the refused merge") {
			t.Fatalf("a sequence over a branch that still contains the refused merge: ran=%v err=%v", ran, err)
		}
		swGit(t, repo, "reset", "-q", "--hard", base)
		if err := c.WithWorkingTree(ctx, func(context.Context) error { ran = true; return nil }); err != nil || !ran {
			t.Fatalf("after the operator's reset: ran=%v err=%v", ran, err)
		}
	})
	t.Run("tree without a home branch", func(t *testing.T) {
		repo := swFeatureRepo(t)
		base := swGit(t, repo, "rev-parse", "main")
		c := git.NewCLIClient(repo)
		res, err := c.MergeBound(ctx, git.MergeOpts{Source: "feature", SourceSHA: swGit(t, repo, "rev-parse", "feature"),
			Target: "main", Strategy: "merge-commit", Message: "refused"})
		if err != nil {
			t.Fatal(err)
		}
		release := swRefLock(t, repo, "main")
		defer release()
		_ = c.ResetBranch(ctx, "main", base, res.SHA)
		ran := false
		if err := c.WithWorkingTree(ctx, func(context.Context) error { ran = true; return nil }); err == nil || ran {
			t.Fatalf("a code-repository sequence over an unfinished take-back: ran=%v err=%v", ran, err)
		}
		if scope, err := git.EnterBranch(ctx, repo, "", nil); err == nil {
			scope.Cleanup()
			t.Fatal("an in-place scope was handed out over an unfinished take-back")
		}
		release()
		if err := c.WithWorkingTree(ctx, func(context.Context) error { ran = true; return nil }); err != nil || !ran {
			t.Fatalf("after the lock went away: ran=%v err=%v", ran, err)
		}
		if got := swGit(t, repo, "rev-parse", "main"); got != base {
			t.Fatalf("main = %s, want the base %s", got, base)
		}
	})
}
