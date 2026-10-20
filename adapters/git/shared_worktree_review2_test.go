package git_test

// INIT-023 TASK-012 review round 2: a take-back of a refused merge is
// never forgotten because it failed early (finding 1), never defeated by
// a sequence whose deadline is spent (finding 1), and never kept pending
// for good by content no checkout makes clean (finding 2).

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/testutil"
)

// swRefused merges feature into main with MergeBound (a stand-in for a
// merge the engine refused) and returns the merge and main's base.
func swRefused(t *testing.T, ctx context.Context, c *git.CLIClient, repo string) (git.MergeResult, string) {
	t.Helper()
	base := swGit(t, repo, "rev-parse", "main")
	res, err := c.MergeBound(ctx, git.MergeOpts{Source: "feature", SourceSHA: swGit(t, repo, "rev-parse", "feature"),
		Target: "main", Strategy: "merge-commit", Message: "refused"})
	if err != nil {
		t.Fatalf("MergeBound: %v", err)
	}
	if res.SHA == base {
		t.Fatal("MergeBound did not move main")
	}
	return res, base
}

func swRead(t *testing.T, repo, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repo, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// swTreeRefused asserts that neither a sequence nor an in-place write
// scope is handed out over the tree.
func swTreeRefused(t *testing.T, ctx context.Context, c *git.CLIClient, repo, why string) {
	t.Helper()
	ran := false
	err := c.WithWorkingTree(ctx, func(context.Context) error { ran = true; return nil })
	var tre *git.TreeRestoreError
	if ran || !errors.As(err, &tre) {
		t.Fatalf("%s: a sequence ran=%v err=%v; want a *TreeRestoreError and fn not run", why, ran, err)
	}
	if scope, err := git.EnterBranch(ctx, repo, "", nil); err == nil {
		scope.Cleanup()
		t.Fatalf("%s: an in-place write scope was handed out", why)
	}
}

// swTreeHealthy asserts that a sequence runs, with main at want.
func swTreeHealthy(t *testing.T, ctx context.Context, c *git.CLIClient, repo, want, why string) {
	t.Helper()
	ran := false
	if err := c.WithWorkingTree(ctx, func(context.Context) error { ran = true; return nil }); err != nil || !ran {
		t.Fatalf("%s: a sequence ran=%v err=%v", why, ran, err)
	}
	if got := swGit(t, repo, "rev-parse", "main"); got != want {
		t.Fatalf("%s: main = %s, want %s", why, got, want)
	}
	swStagedClean(t, repo)
}

// Finding 1, the PoC: inside a held sequence the take-back runs on a
// context that is already done — the sequence's own deadline spent, or a
// caller's context cancelled. Before the fix its first git call failed,
// nothing was remembered, and the block released a tree that looked
// healthy with main at the refused merge. The take-back now runs on its
// own bounded context and completes.
func TestF9_TakeBackSurvivesASpentContext(t *testing.T) {
	for _, tc := range []struct {
		name string
		done func(context.Context) (context.Context, context.CancelFunc)
	}{
		{"cancelled", func(ctx context.Context) (context.Context, context.CancelFunc) {
			c, cancel := context.WithCancel(ctx)
			cancel()
			return c, cancel
		}},
		{"deadline spent", func(ctx context.Context) (context.Context, context.CancelFunc) {
			return context.WithDeadline(ctx, time.Now().Add(-time.Second))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repo := swFeatureRepo(t)
			c := git.NewCLIClient(repo, git.WithHomeBranch("main"))
			var base string
			var takeBackErr error
			if err := c.WithWorkingTree(ctx, func(seq context.Context) error {
				var res git.MergeResult
				res, base = swRefused(t, seq, c, repo)
				spent, cancel := tc.done(seq)
				defer cancel()
				takeBackErr = c.ResetBranch(spent, "main", base, res.SHA)
				return nil
			}); err != nil {
				t.Fatalf("sequence: %v", err)
			}
			if takeBackErr != nil {
				t.Fatalf("ResetBranch on a spent context inside the sequence: %v; want the take-back done", takeBackErr)
			}
			swTreeHealthy(t, ctx, c, repo, base, "after the take-back")
			if got := swRead(t, repo, "f.md"); got != "main\n" {
				t.Fatalf("the working tree did not follow the take-back: f.md = %q", got)
			}
		})
	}
}

// Finding 1, the arming: a take-back that fails before its commit checks
// complete — a git child that cannot run, the wait for the tree cut
// short — is remembered like one that fails at the ref move. Until it is
// done no sequence and no in-place write gets the tree; once git works
// again, the next take finishes it.
func TestF9_EarlyTakeBackFailureIsRemembered(t *testing.T) {
	t.Run("a commit check fails", func(t *testing.T) {
		ctx := context.Background()
		repo := swFeatureRepo(t)
		c := git.NewCLIClient(repo, git.WithHomeBranch("main"))
		res, base := swRefused(t, ctx, c, repo)
		flag := filepath.Join(t.TempDir(), "fail-commit-checks")
		installGitWrapper(t, `if [ -e "`+flag+`" ] && [ "$1" = rev-parse ] && [ "$2" = --verify ] && [ "$3" = --quiet ]; then
  echo "fatal: unable to run the commit check" >&2; exit 128
fi`)
		if err := os.WriteFile(flag, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := c.ResetBranch(ctx, "main", base, res.SHA); err == nil {
			t.Fatal("ResetBranch with its commit checks failing reported success")
		}
		if got := swGit(t, repo, "rev-parse", "main"); got != res.SHA {
			t.Fatalf("main = %s, want the refused merge still there", got)
		}
		swTreeRefused(t, ctx, c, repo, "while the take-back cannot run")
		if err := os.Remove(flag); err != nil {
			t.Fatal(err)
		}
		swTreeHealthy(t, ctx, c, repo, base, "once git works again")
	})
	t.Run("the wait for the tree is cut short", func(t *testing.T) {
		ctx := context.Background()
		repo := swFeatureRepo(t)
		c := git.NewCLIClient(repo, git.WithHomeBranch("main"))
		res, base := swRefused(t, ctx, c, repo)
		done, cancel := context.WithCancel(ctx)
		cancel()
		err := c.ResetBranch(done, "main", base, res.SHA)
		var we *git.TreeWaitError
		if !errors.As(err, &we) {
			t.Fatalf("ResetBranch with a done context outside a sequence = %v, want a *TreeWaitError", err)
		}
		if got := swGit(t, repo, "rev-parse", "main"); got != res.SHA {
			t.Fatalf("main = %s; nothing should have run", got)
		}
		swTreeHealthy(t, ctx, c, repo, base, "the next take finishes the take-back")
	})
	t.Run("expected names no commit: nothing to take back", func(t *testing.T) {
		ctx := context.Background()
		repo := swFeatureRepo(t)
		c := git.NewCLIClient(repo, git.WithHomeBranch("main"))
		res, base := swRefused(t, ctx, c, repo)
		blob := swGit(t, repo, "rev-parse", "main:f.md")
		if err := c.ResetBranch(ctx, "main", base, blob); err == nil {
			t.Fatal("ResetBranch with a blob as expected reported success")
		}
		// The branch cannot contain a blob: the tree is not refused for it.
		swTreeHealthy(t, ctx, c, repo, res.SHA, "a take-back of no commit")
	})
}

// swPhantomFeature makes the feature branch carry a line-ending phantom:
// notes.md committed with CRLF, then a `*.md text eol=lf` attribute.
func swPhantomFeature(t *testing.T) string {
	t.Helper()
	repo := swFeatureRepo(t)
	swGit(t, repo, "checkout", "-q", "feature")
	swCommitCRLF(t, repo)
	swGit(t, repo, "checkout", "-q", "-f", "main")
	return repo
}

// Finding 2: a refused merge whose content no checkout makes clean is
// still taken back on the shared tree. `read-tree -m -u` refuses such a
// tree for good ("Entry 'notes.md' not uptodate"), so the take-back used
// to fail, stay pending, and refuse every later take of the tree, each
// one writing a new quarantine commit. It now quarantines and resets, in
// the merge block itself and when a pending take-back is settled.
func TestF9_PhantomRefusedMergeIsTakenBack(t *testing.T) {
	t.Run("in the block", func(t *testing.T) {
		ctx := context.Background()
		repo := swPhantomFeature(t)
		c := git.NewCLIClient(repo, git.WithHomeBranch("main"))
		var base string
		if err := c.WithWorkingTree(ctx, func(seq context.Context) error {
			var res git.MergeResult
			res, base = swRefused(t, seq, c, repo)
			swStale(t, repo, "notes.md")
			if st, _ := swTry(repo, "status", "--porcelain", "--untracked-files=no"); st != "M notes.md" {
				t.Fatalf("precondition: status after the merge = %q, want the phantom", st)
			}
			return c.ResetBranch(seq, "main", base, res.SHA)
		}); err != nil {
			t.Fatalf("take-back of a phantom-carrying merge: %v", err)
		}
		swTreeHealthy(t, ctx, c, repo, base, "after the take-back")
		if _, err := os.Stat(filepath.Join(repo, "notes.md")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("notes.md of the refused merge is still in the working tree: %v", err)
		}
		if rep, err := c.RestoreWorkingTree(ctx); err != nil || rep.Repaired() {
			t.Fatalf("restore after the take-back = %+v, %v; want nothing to repair", rep, err)
		}
	})
	t.Run("settled by a later take", func(t *testing.T) {
		ctx := context.Background()
		repo := swPhantomFeature(t)
		c := git.NewCLIClient(repo, git.WithHomeBranch("main"))
		res, base := swRefused(t, ctx, c, repo)
		swStale(t, repo, "notes.md")
		release := swRefLock(t, repo, "main")
		if err := c.ResetBranch(ctx, "main", base, res.SHA); err == nil {
			t.Fatal("ResetBranch with main.lock held reported success")
		}
		release()
		swTreeHealthy(t, ctx, c, repo, base, "the settle over the phantom")
		for i := 0; i < 2; i++ {
			rep, err := c.RestoreWorkingTree(ctx)
			if err != nil || rep.Repaired() || rep.QuarantineSHA != "" {
				t.Fatalf("restore %d after the settle = %+v, %v; want nothing repaired and no new quarantine", i, rep, err)
			}
		}
	})
	t.Run("an untracked file in the way refuses it and is kept", func(t *testing.T) {
		ctx := context.Background()
		repo := swPhantomFeature(t)
		// The merge deletes keep.md; an untracked keep.md then stands where
		// the take-back's reset would write main's.
		swGit(t, repo, "checkout", "-q", "-f", "feature")
		testutil.WriteFile(t, repo, "keep.md", "")
		swGit(t, repo, "checkout", "-q", "-f", "main")
		testutil.WriteFile(t, repo, "keep.md", "main's keep\n")
		swGit(t, repo, "add", "keep.md")
		swGit(t, repo, "commit", "-q", "-m", "keep on main")
		swGit(t, repo, "checkout", "-q", "-f", "feature")
		swGit(t, repo, "merge", "-q", "--no-edit", "main")
		swGit(t, repo, "rm", "-q", "keep.md")
		swGit(t, repo, "commit", "-q", "-m", "feature drops keep.md")
		swGit(t, repo, "checkout", "-q", "-f", "main")
		c := git.NewCLIClient(repo, git.WithHomeBranch("main"))
		res, base := swRefused(t, ctx, c, repo)
		swStale(t, repo, "notes.md")
		testutil.WriteFile(t, repo, "keep.md", "an operator's precious file\n")
		if err := c.ResetBranch(ctx, "main", base, res.SHA); err == nil || !strings.Contains(err.Error(), "keep.md") {
			t.Fatalf("ResetBranch over an untracked file in the way = %v; want a refusal naming keep.md", err)
		}
		if got := swGit(t, repo, "rev-parse", "main"); got != res.SHA {
			t.Fatalf("main = %s after the refusal, want it moved forward to the merge %s", got, res.SHA)
		}
		if got := swRead(t, repo, "keep.md"); got != "an operator's precious file\n" {
			t.Fatalf("the untracked file was overwritten: %q", got)
		}
		swTreeRefused(t, ctx, c, repo, "while the untracked file is in the way")
		if got := swRead(t, repo, "keep.md"); got != "an operator's precious file\n" {
			t.Fatalf("a take of the tree overwrote the untracked file: %q", got)
		}
		if err := os.Remove(filepath.Join(repo, "keep.md")); err != nil {
			t.Fatal(err)
		}
		swTreeHealthy(t, ctx, c, repo, base, "after the operator moved the file")
	})
	t.Run("a tree without a home branch keeps the refusal", func(t *testing.T) {
		// TASK-005: a local change on such a tree is never discarded, and
		// the take-back stays pending (fail closed; KNOWN-LIMITATIONS).
		ctx := context.Background()
		repo := swPhantomFeature(t)
		c := git.NewCLIClient(repo)
		res, base := swRefused(t, ctx, c, repo)
		swStale(t, repo, "notes.md")
		if err := c.ResetBranch(ctx, "main", base, res.SHA); err == nil {
			t.Fatal("ResetBranch over a phantom on a tree without a home branch reported success")
		}
		if got := swGit(t, repo, "rev-parse", "main"); got != res.SHA {
			t.Fatalf("main = %s, want it moved forward to the merge %s", got, res.SHA)
		}
		if err := c.WithWorkingTree(ctx, func(context.Context) error { return nil }); err == nil {
			t.Fatal("a sequence ran over the pending take-back")
		}
	})
}

// Finding 1, the clear: a take-back that succeeds clears only the record
// of its own refused merge. Inside one sequence the take-back of a refused
// merge X fails (main.lock held) and stays pending; a second refused merge
// Y, made on top of X, is then taken back to X. That success says nothing
// about X, which is still on main: X's record is kept, and the block's exit
// restore finishes it. Clearing the branch's record whatever merge it named
// handed the tree on as healthy with X on main.
func TestF9_TakeBackSuccessClearsOnlyItsOwnRecord(t *testing.T) {
	ctx := context.Background()
	repo := swFeatureRepo(t)
	swGit(t, repo, "checkout", "-q", "-b", "feature2", "main")
	testutil.WriteFile(t, repo, "g.md", "feature2\n")
	swGit(t, repo, "add", "g.md")
	swGit(t, repo, "commit", "-q", "-m", "feature2 g")
	swGit(t, repo, "checkout", "-q", "main")
	c := git.NewCLIClient(repo, git.WithHomeBranch("main"))
	var base string
	if err := c.WithWorkingTree(ctx, func(seq context.Context) error {
		var x git.MergeResult
		x, base = swRefused(t, seq, c, repo)
		release := swRefLock(t, repo, "main")
		if err := c.ResetBranch(seq, "main", base, x.SHA); err == nil {
			t.Fatal("ResetBranch with main.lock held reported success")
		}
		release()
		y, err := c.MergeBound(seq, git.MergeOpts{Source: "feature2", SourceSHA: swGit(t, repo, "rev-parse", "feature2"),
			Target: "main", Strategy: "merge-commit", Message: "refused too"})
		if err != nil {
			t.Fatalf("MergeBound of the second merge: %v", err)
		}
		if err := c.ResetBranch(seq, "main", x.SHA, y.SHA); err != nil {
			t.Fatalf("take-back of the second refused merge: %v", err)
		}
		if got := swGit(t, repo, "rev-parse", "main"); got != x.SHA {
			t.Fatalf("main = %s after the second take-back, want the first refused merge %s", got, x.SHA)
		}
		return nil
	}); err != nil {
		t.Fatalf("sequence: %v", err)
	}
	if got := swGit(t, repo, "rev-parse", "main"); got != base {
		t.Fatalf("main = %s after the block, want the base %s: the first take-back was forgotten", got, base)
	}
	swTreeHealthy(t, ctx, c, repo, base, "after the block")
}

// Finding 2, the order: a take-back that stays pending does not make every
// take of the tree quarantine its phantoms again. The restore remembers the
// phantoms before it settles take-backs, so while the take-back cannot be
// done — here main moved on outside the server and still contains the
// refused merge, a refusal that waits for an operator — every take refuses
// the tree and only the first one writes a quarantine commit. Settled
// first, a failing settle left the phantoms unremembered, and each take
// quarantined them anew.
func TestF9_PendingTakeBackDoesNotRequarantinePhantoms(t *testing.T) {
	ctx := context.Background()
	repo := swPhantomFeature(t)
	c := git.NewCLIClient(repo, git.WithHomeBranch("main"))
	res, base := swRefused(t, ctx, c, repo)
	release := swRefLock(t, repo, "main")
	if err := c.ResetBranch(ctx, "main", base, res.SHA); err == nil {
		t.Fatal("ResetBranch with main.lock held reported success")
	}
	release()
	swGit(t, repo, "commit", "-q", "--allow-empty", "--no-verify", "-m", "moved on outside the server")
	swStale(t, repo, "notes.md")
	if st, _ := swTry(repo, "status", "--porcelain", "--untracked-files=no"); st != "M notes.md" {
		t.Fatalf("precondition: status = %q, want the phantom", st)
	}
	for i := 0; i < 3; i++ {
		_, err := c.RestoreWorkingTree(ctx)
		var tre *git.TreeRestoreError
		if !errors.As(err, &tre) || !strings.Contains(err.Error(), "still contains the refused merge") {
			t.Fatalf("take %d while main still contains the refused merge = %v; want the settle's refusal", i, err)
		}
		if i > 0 && tre.QuarantineSHA != "" {
			t.Fatalf("take %d quarantined the phantom again (%s) while the take-back stayed pending", i, tre.QuarantineSHA)
		}
	}
	swTreeRefused(t, ctx, c, repo, "while main still contains the refused merge")
}
