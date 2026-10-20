package git_test

// INIT-023 TASK-012: the CLIClient half of the shared-working-tree fix —
// the explicit merge target (AC 4), the home-branch restore (AC 11),
// SIGTERM on cancel (AC 24a) and HEAD on a home-branch tree (AC 25).

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

// swGit runs git in dir with a fixed identity.
func swGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.test",
		"-c", "commit.gpgsign=false"}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func swTry(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// swFeatureRepo: main has f.md "main\n"; feature changes it to "feature\n".
func swFeatureRepo(t *testing.T) string {
	t.Helper()
	repo := testutil.NewTempRepo(t)
	testutil.WriteFile(t, repo, "f.md", "main\n")
	swGit(t, repo, "add", "f.md")
	swGit(t, repo, "commit", "-q", "-m", "main f")
	swGit(t, repo, "checkout", "-q", "-b", "feature")
	testutil.WriteFile(t, repo, "f.md", "feature\n")
	swGit(t, repo, "commit", "-q", "-am", "feature f")
	swGit(t, repo, "checkout", "-q", "main")
	return repo
}

func swHead(t *testing.T, repo string) string {
	t.Helper()
	out, _ := swTry(repo, "symbolic-ref", "-q", "HEAD")
	return out
}

func swRefs(t *testing.T, repo string) string {
	t.Helper()
	return swGit(t, repo, "for-each-ref", "--format=%(refname)")
}

// --- AC 4: the merge target rule ---

func TestF8_MergeRefusesEmptyTarget(t *testing.T) {
	repo := swFeatureRepo(t)
	rec := testutil.InstallGitRecorder(t)
	c := git.NewCLIClient(repo)
	before := swGit(t, repo, "rev-parse", "main")
	feat := swGit(t, repo, "rev-parse", "feature")
	rec.Reset(t)
	for _, opts := range []git.MergeOpts{
		{Source: "feature", Strategy: "merge-commit", Message: "m"},
		{Source: "feature", SourceSHA: feat, Strategy: "merge-commit", Message: "m"},
	} {
		_, err := c.Merge(context.Background(), opts)
		var ge *git.GitError
		if !errors.As(err, &ge) || ge.IsRetryable() || !strings.Contains(ge.Message, "merge target required") {
			t.Fatalf("Merge(%+v) = %v, want a non-retryable *GitError \"merge target required\"", opts, err)
		}
	}
	if got := rec.Calls(t); len(got) != 0 {
		t.Fatalf("a refused merge started git: %q", got)
	}
	if swGit(t, repo, "rev-parse", "main") != before {
		t.Fatal("main moved")
	}
}

func TestF8_MergeAbortStillWorksWithoutTarget(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts git.MergeOpts
	}{
		{"strategy and source", git.MergeOpts{Source: "--abort", Strategy: "abort"}},
		{"source sentinel only (TASK-002 parity)", git.MergeOpts{Source: "--abort"}},
		{"strategy only", git.MergeOpts{Strategy: "abort"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := swFeatureRepo(t)
			testutil.WriteFile(t, repo, "f.md", "conflict\n")
			swGit(t, repo, "commit", "-q", "-am", "main conflict")
			c := git.NewCLIClient(repo)
			if _, err := c.Merge(context.Background(), git.MergeOpts{Source: "feature", Target: "main",
				Strategy: "merge-commit", Message: "m"}); err == nil {
				t.Fatal("expected a conflict")
			}
			rec := testutil.InstallGitRecorder(t)
			rec.Reset(t)
			if _, err := c.Merge(context.Background(), tc.opts); err != nil {
				t.Fatalf("abort %+v: %v", tc.opts, err)
			}
			if got := rec.Calls(t); len(got) != 1 || strings.Join(got[0], " ") != "merge --abort" {
				t.Fatalf("abort argv = %q, want exactly [merge --abort]", got)
			}
			if _, err := swTry(repo, "rev-parse", "-q", "--verify", "MERGE_HEAD"); err == nil {
				t.Fatal("MERGE_HEAD survived the abort")
			}
		})
	}
}

func TestF8_MergeAbortShapesRefusedBeforeGit(t *testing.T) {
	repo := swFeatureRepo(t)
	c := git.NewCLIClient(repo)
	rec := testutil.InstallGitRecorder(t)
	for _, opts := range []git.MergeOpts{
		{Strategy: "abort", Target: "x"},
		{Source: "--abort", Target: "x"},
		{Strategy: "abort", Source: "feature"},
	} {
		rec.Reset(t)
		_, err := c.Merge(context.Background(), opts)
		var ge *git.GitError
		if !errors.As(err, &ge) || ge.IsRetryable() {
			t.Fatalf("Merge(%+v) = %v, want a permanent refusal", opts, err)
		}
		if got := rec.Calls(t); len(got) != 0 {
			t.Fatalf("Merge(%+v) started git: %q", opts, got)
		}
	}
}

// --- AC 11: restore semantics ---

func TestF9_RestoreQuarantinesAndReturnsToMain(t *testing.T) {
	ctx := context.Background()
	repo := swFeatureRepo(t)
	c := git.NewCLIClient(repo, git.WithHomeBranch("main"))
	refsBefore := swRefs(t, repo)
	swGit(t, repo, "checkout", "-q", "feature")
	testutil.WriteFile(t, repo, "f.md", "staged edit\n")
	swGit(t, repo, "add", "f.md")
	testutil.WriteFile(t, repo, "untracked.txt", "keep me\n")

	rep, err := c.RestoreWorkingTree(ctx)
	if err != nil {
		t.Fatalf("RestoreWorkingTree: %v", err)
	}
	if rep.PreviousRef != "refs/heads/feature" || rep.QuarantineSHA == "" || !rep.SwitchedHEAD || !rep.Repaired() {
		t.Fatalf("report = %+v", rep)
	}
	if got := swGit(t, repo, "show", rep.QuarantineSHA+":f.md"); got != "staged edit" {
		t.Fatalf("the quarantine commit does not hold the edit: %q", got)
	}
	if got := swHead(t, repo); got != "refs/heads/main" {
		t.Fatalf("HEAD = %s", got)
	}
	if got := swGit(t, repo, "status", "--porcelain", "--untracked-files=no"); got != "" {
		t.Fatalf("tracked changes remain: %q", got)
	}
	if _, err := os.Stat(filepath.Join(repo, "untracked.txt")); err != nil {
		t.Fatalf("the restore removed an untracked file: %v", err)
	}
	if got := swRefs(t, repo); got != refsBefore {
		t.Fatalf("the restore created a ref:\n%s\nwas:\n%s", got, refsBefore)
	}
	if got := swGit(t, repo, "show", "feature:f.md"); got != "feature" {
		t.Fatalf("the foreign branch was changed: %q", got)
	}

	rep, err = c.RestoreWorkingTree(ctx)
	if err != nil || rep.Repaired() {
		t.Fatalf("restore of a clean tree = %+v, %v; want a no-op", rep, err)
	}

	// A merge in progress is aborted.
	testutil.WriteFile(t, repo, "f.md", "main conflict\n")
	swGit(t, repo, "commit", "-q", "-am", "conflict on main")
	if _, err := swTry(repo, "-c", "user.name=t", "-c", "user.email=t@example.test", "merge", "feature"); err == nil {
		t.Fatal("expected a conflicted merge")
	}
	rep, err = c.RestoreWorkingTree(ctx)
	if err != nil || !rep.AbortedMerge {
		t.Fatalf("restore of a conflicted merge = %+v, %v; want AbortedMerge", rep, err)
	}
	if _, err := swTry(repo, "rev-parse", "-q", "--verify", "MERGE_HEAD"); err == nil {
		t.Fatal("MERGE_HEAD survived the restore")
	}
	if got := swGit(t, repo, "status", "--porcelain", "--untracked-files=no"); got != "" {
		t.Fatalf("tracked changes remain after the abort: %q", got)
	}
}

func TestF9_HomeBranchSelfHealsOnlyWhenConfigured(t *testing.T) {
	ctx := context.Background()
	poison := func(repo string) {
		swGit(t, repo, "checkout", "-q", "feature")
		testutil.WriteFile(t, repo, "f.md", "staged\n")
		swGit(t, repo, "add", "f.md")
	}

	t.Run("home branch configured", func(t *testing.T) {
		repo := swFeatureRepo(t)
		shared := git.NewCLIClient(repo, git.WithHomeBranch("main"))
		mainTip := swGit(t, repo, "rev-parse", "main")
		poison(repo)
		if err := shared.CreateBranch(ctx, "spine/run/new", "HEAD"); err != nil {
			t.Fatalf("CreateBranch: %v", err)
		}
		if got := swGit(t, repo, "rev-parse", "spine/run/new"); got != mainTip {
			t.Fatalf("CreateBranch(HEAD) on a poisoned shared tree cut from %s, want main %s", got, mainTip)
		}
		poison(repo)
		// Another client (the artifact service's EnterBranch receives only
		// the directory) sees the same home branch.
		scope, err := git.EnterBranch(ctx, repo, "", nil)
		if err != nil {
			t.Fatalf("EnterBranch: %v", err)
		}
		if got := swHead(t, repo); got != "refs/heads/main" {
			scope.Cleanup()
			t.Fatalf("in-place scope on %s", got)
		}
		scope.Cleanup()
		if git.NewCLIClient(repo).HomeBranch() != "main" {
			t.Fatal("a second client on the tree does not see the registered home branch")
		}
	})

	t.Run("no home branch", func(t *testing.T) {
		repo := swFeatureRepo(t)
		c := git.NewCLIClient(repo)
		poison(repo)
		if err := c.WithWorkingTree(ctx, func(context.Context) error { return nil }); err != nil {
			t.Fatal(err)
		}
		if got := swHead(t, repo); got != "refs/heads/feature" {
			t.Fatalf("a tree without a home branch was moved to %s", got)
		}
		if got := swGit(t, repo, "status", "--porcelain", "--untracked-files=no"); got == "" {
			t.Fatal("a tree without a home branch was cleaned")
		}
		if c.HomeBranch() != "" {
			t.Fatalf("HomeBranch() = %q", c.HomeBranch())
		}
		if _, err := c.RestoreWorkingTree(ctx); err == nil {
			t.Fatal("RestoreWorkingTree without a home branch: want an error")
		}
	})

	t.Run("restore failure stops the sequence", func(t *testing.T) {
		repo := swFeatureRepo(t)
		testutil.WriteFile(t, repo, "notes.md", "tracked on main\n")
		swGit(t, repo, "add", "notes.md")
		swGit(t, repo, "commit", "-q", "-m", "notes")
		c := git.NewCLIClient(repo, git.WithHomeBranch("main"))
		swGit(t, repo, "checkout", "-q", "feature") // feature has no notes.md
		testutil.WriteFile(t, repo, "notes.md", "an untracked file in the way\n")
		ran := false
		err := c.WithWorkingTree(ctx, func(context.Context) error { ran = true; return nil })
		var re *git.TreeRestoreError
		if !errors.As(err, &re) || ran {
			t.Fatalf("WithWorkingTree = %v (fn ran=%v), want *TreeRestoreError and fn not run", err, ran)
		}
		if b, _ := os.ReadFile(filepath.Join(repo, "notes.md")); string(b) != "an untracked file in the way\n" {
			t.Fatalf("the restore changed the untracked file: %q", b)
		}
	})

	t.Run("invalid home branch fails closed", func(t *testing.T) {
		repo := swFeatureRepo(t)
		c := git.NewCLIClient(repo, git.WithHomeBranch("bad..name"))
		ran := false
		err := c.WithWorkingTree(ctx, func(context.Context) error { ran = true; return nil })
		var re *git.TreeRestoreError
		if !errors.As(err, &re) || ran || !errors.Is(err, git.ErrInvalidRef) {
			t.Fatalf("WithWorkingTree with an invalid home = %v (ran=%v)", err, ran)
		}
	})
}

// A .git/index.lock is reported, not deleted (owner question 5).
func TestF9_RestoreReportsStaleIndexLock(t *testing.T) {
	ctx := context.Background()
	repo := swFeatureRepo(t)
	c := git.NewCLIClient(repo, git.WithHomeBranch("main"))
	lock := filepath.Join(repo, ".git", "index.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := c.RestoreWorkingTree(ctx)
	var re *git.TreeRestoreError
	var ge *git.GitError
	if !errors.As(err, &re) || !strings.Contains(err.Error(), "index.lock") ||
		!errors.As(err, &ge) || !ge.IsRetryable() {
		t.Fatalf("RestoreWorkingTree with index.lock = %v, want a *TreeRestoreError naming index.lock with a retryable *GitError", err)
	}
	ran := false
	if err := c.WithWorkingTree(ctx, func(context.Context) error { ran = true; return nil }); !errors.As(err, &re) || ran {
		t.Fatalf("WithWorkingTree with index.lock = %v (ran=%v)", err, ran)
	}
	if _, err := os.Stat(lock); err != nil {
		t.Fatalf("the restore deleted index.lock: %v", err)
	}
}

// --- §2.2: every stateful call holds the tree ---

func TestF9_StatefulCallsWaitForTheTree(t *testing.T) {
	repo := swFeatureRepo(t)
	c := git.NewCLIClient(repo)
	feat := swGit(t, repo, "rev-parse", "feature")
	mainOID := swGit(t, repo, "rev-parse", "main")
	calls := map[string]func(ctx context.Context) error{
		"Checkout": func(ctx context.Context) error { return c.Checkout(ctx, "feature") },
		"Commit": func(ctx context.Context) error {
			_, err := c.Commit(ctx, git.CommitOpts{Message: "x", AllowEmpty: true})
			return err
		},
		"Merge": func(ctx context.Context) error {
			_, err := c.Merge(ctx, git.MergeOpts{Source: "feature", Target: "main"})
			return err
		},
		"Merge abort": func(ctx context.Context) error { _, err := c.Merge(ctx, git.MergeOpts{Source: "--abort"}); return err },
		"MergeBound": func(ctx context.Context) error {
			_, err := c.MergeBound(ctx, git.MergeOpts{SourceSHA: feat, Target: "main"})
			return err
		},
		"CreateBranch":      func(ctx context.Context) error { return c.CreateBranch(ctx, "n1", "main") },
		"DeleteBranch":      func(ctx context.Context) error { return c.DeleteBranch(ctx, "feature") },
		"WriteAndStageFile": func(ctx context.Context) error { return c.WriteAndStageFile(ctx, "w.md", "w\n") },
		"ResetBranch":       func(ctx context.Context) error { return c.ResetBranch(ctx, "feature", mainOID, feat) },
		"EnterBranch(\"\")": func(ctx context.Context) error {
			s, err := git.EnterBranch(ctx, repo, "", nil)
			if err == nil {
				s.Cleanup()
			}
			return err
		},
	}
	held, release := make(chan struct{}), make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = c.WithWorkingTree(context.Background(), func(context.Context) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	defer func() { close(release); <-done }()
	for name, call := range calls {
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		err := call(ctx)
		cancel()
		var we *git.TreeWaitError
		if !errors.As(err, &we) {
			t.Errorf("%s while another sequence holds the tree = %v, want *TreeWaitError", name, err)
		}
	}
	if got := swGit(t, repo, "rev-parse", "main"); got != mainOID {
		t.Fatal("a waiting call changed main")
	}
}

// --- AC 24a: a cancelled git leaves no lock file ---

func TestF9_CancelledGitLeavesNoLockFile(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	swGit(t, repo, "config", "filter.slow.smudge", "sleep 2; cat")
	swGit(t, repo, "config", "filter.slow.clean", "cat")
	testutil.WriteFile(t, repo, ".gitattributes", "*.slow filter=slow\n")
	swGit(t, repo, "add", ".gitattributes")
	swGit(t, repo, "commit", "-q", "-m", "attributes")
	swGit(t, repo, "checkout", "-q", "-b", "slow")
	testutil.WriteFile(t, repo, "b.slow", "slow content\n")
	swGit(t, repo, "add", "b.slow")
	swGit(t, repo, "commit", "-q", "-m", "slow file")
	swGit(t, repo, "checkout", "-q", "main")

	c := git.NewCLIClient(repo)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := c.Checkout(ctx, "slow"); err == nil {
		t.Fatal("a checkout cancelled during its smudge filter reported success")
	}
	if _, err := os.Stat(filepath.Join(repo, ".git", "index.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf(".git/index.lock after a cancelled checkout: %v (git was killed, not terminated)", err)
	}
}

// --- AC 25 (adapter half): HEAD on a home-branch tree is the home branch ---

func TestF9_HeadReadsResolveToHomeBranch(t *testing.T) {
	ctx := context.Background()
	repo := swFeatureRepo(t)
	mainOID := swGit(t, repo, "rev-parse", "main")
	shared := git.NewCLIClient(repo, git.WithHomeBranch("main"))
	// A sequence has a run branch checked out.
	swGit(t, repo, "checkout", "-q", "feature")

	if b, err := shared.ReadFile(ctx, "HEAD", "f.md"); err != nil || string(b) != "main\n" {
		t.Fatalf("ReadFile(HEAD) = %q, %v; want main's content", b, err)
	}
	if h, err := shared.Head(ctx); err != nil || h != mainOID {
		t.Fatalf("Head() = %s, %v; want main %s", h, err, mainOID)
	}
	if s, err := shared.RefSHA(ctx, "HEAD"); err != nil || s != mainOID {
		t.Fatalf("RefSHA(HEAD) = %s, %v", s, err)
	}
	if typ, err := shared.TreeEntryType(ctx, "HEAD", "f.md"); err != nil || typ != git.TreeEntryBlob {
		t.Fatalf("TreeEntryType(HEAD) = %q, %v", typ, err)
	}
	if files, err := shared.ListFiles(ctx, "HEAD", ""); err != nil || len(files) != 1 || files[0] != "f.md" {
		t.Fatalf("ListFiles(HEAD) = %v, %v", files, err)
	}
	if d, err := shared.Diff(ctx, "HEAD", "feature"); err != nil || len(d) != 1 {
		t.Fatalf("Diff(HEAD, feature) = %v, %v; want f.md modified (HEAD is main)", d, err)
	}
	if mb, err := shared.MergeBase(ctx, "HEAD", "feature"); err != nil || mb != mainOID {
		t.Fatalf("MergeBase(HEAD, feature) = %s, %v", mb, err)
	}
	if log, err := shared.Log(ctx, git.LogOpts{Limit: 1}); err != nil || len(log) != 1 || log[0].SHA != mainOID {
		t.Fatalf("Log() = %+v, %v; want main's tip first", log, err)
	}
	first := swGit(t, repo, "rev-list", "--max-parents=0", "main")
	if log, err := shared.Log(ctx, git.LogOpts{Since: first}); err != nil || len(log) != 1 || log[0].SHA != mainOID {
		t.Fatalf("Log(Since) = %+v, %v; want <since>..main", log, err)
	}

	// The same tree read through a client without the option sees the
	// registered home; a tree with none keeps the real HEAD.
	if b, err := git.NewCLIClient(repo).ReadFile(ctx, "HEAD", "f.md"); err != nil || string(b) != "main\n" {
		t.Fatalf("second client ReadFile(HEAD) = %q, %v", b, err)
	}
	other := swFeatureRepo(t)
	swGit(t, other, "checkout", "-q", "feature")
	oc := git.NewCLIClient(other)
	if b, err := oc.ReadFile(ctx, "HEAD", "f.md"); err != nil || string(b) != "feature\n" {
		t.Fatalf("no-home ReadFile(HEAD) = %q, %v; want the real HEAD", b, err)
	}
	if h, err := oc.Head(ctx); err != nil || h != swGit(t, other, "rev-parse", "feature") {
		t.Fatalf("no-home Head() = %s, %v", h, err)
	}
}

// Codex review round 2 [P1]: an untracked file standing where `reset
// --hard` would write a tracked path is never deleted; the restore
// refuses and changes nothing.
func TestF9_RestoreNeverDeletesObstructingUntrackedFiles(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, repo string) (path, content string)
	}{
		{"untracked file replaces a tracked directory", func(t *testing.T, repo string) (string, string) {
			testutil.WriteFile(t, repo, "dir/file.md", "tracked\n")
			swGit(t, repo, "add", "dir/file.md")
			swGit(t, repo, "commit", "-q", "-m", "dir")
			swGit(t, repo, "rm", "-r", "-q", "dir")
			testutil.WriteFile(t, repo, "dir", "precious untracked content\n")
			return "dir", "precious untracked content\n"
		}},
		{"tracked file untracked and rewritten", func(t *testing.T, repo string) (string, string) {
			swGit(t, repo, "rm", "--cached", "-q", "f.md")
			testutil.WriteFile(t, repo, "f.md", "precious rewrite\n")
			return "f.md", "precious rewrite\n"
		}},
		{"tracked file replaced by a directory", func(t *testing.T, repo string) (string, string) {
			swGit(t, repo, "rm", "-q", "f.md")
			testutil.WriteFile(t, repo, "f.md/inner.txt", "precious nested\n")
			return "f.md/inner.txt", "precious nested\n"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := swFeatureRepo(t)
			c := git.NewCLIClient(repo, git.WithHomeBranch("main"))
			path, content := tc.setup(t, repo)
			statusBefore := swGit(t, repo, "status", "--porcelain")
			_, err := c.RestoreWorkingTree(ctx)
			var re *git.TreeRestoreError
			if !errors.As(err, &re) || re.QuarantineSHA != "" || !strings.Contains(err.Error(), "untracked file is in the way") {
				t.Fatalf("RestoreWorkingTree = %v, want a *TreeRestoreError refusing the obstruction before any quarantine", err)
			}
			if b, rerr := os.ReadFile(filepath.Join(repo, path)); rerr != nil || string(b) != content {
				t.Fatalf("%s after the refused restore: %q, %v", path, b, rerr)
			}
			if got := swGit(t, repo, "status", "--porcelain"); got != statusBefore {
				t.Fatalf("a refused restore changed the tree:\n%s\nwas:\n%s", got, statusBefore)
			}
		})
	}
}

// Codex review round 2 [P2]: when a later step fails after tracked changes
// were quarantined, the quarantine SHA is logged and carried in the error.
func TestF9_RestoreFailureAfterQuarantineCarriesTheSHA(t *testing.T) {
	ctx := context.Background()
	repo := swFeatureRepo(t)
	testutil.WriteFile(t, repo, "notes.md", "tracked on main\n")
	swGit(t, repo, "add", "notes.md")
	swGit(t, repo, "commit", "-q", "-m", "notes")
	c := git.NewCLIClient(repo, git.WithHomeBranch("main"))
	swGit(t, repo, "checkout", "-q", "feature") // feature has no notes.md
	testutil.WriteFile(t, repo, "f.md", "edit to quarantine\n")
	testutil.WriteFile(t, repo, "notes.md", "an untracked file blocking the switch\n")
	_, err := c.RestoreWorkingTree(ctx)
	var re *git.TreeRestoreError
	if !errors.As(err, &re) || re.QuarantineSHA == "" || !strings.Contains(err.Error(), re.QuarantineSHA) {
		t.Fatalf("RestoreWorkingTree = %v, want a *TreeRestoreError carrying the quarantine SHA", err)
	}
	if got := swGit(t, repo, "show", re.QuarantineSHA+":f.md"); got != "edit to quarantine" {
		t.Fatalf("the quarantine commit does not hold the edit: %q", got)
	}
}

// Codex review round 3 [P1]: the switch home never overwrites an ignored
// file at a path the home branch tracks (git's default is
// --overwrite-ignore).
func TestF9_RestoreNeverOverwritesIgnoredFiles(t *testing.T) {
	ctx := context.Background()
	repo := swFeatureRepo(t)
	testutil.WriteFile(t, repo, "data.bin", "tracked on main\n")
	swGit(t, repo, "add", "data.bin")
	swGit(t, repo, "commit", "-q", "-m", "data on main")
	c := git.NewCLIClient(repo, git.WithHomeBranch("main"))
	swGit(t, repo, "checkout", "-q", "feature") // feature has no data.bin
	testutil.WriteFile(t, repo, ".git/info/exclude", "data.bin\n")
	testutil.WriteFile(t, repo, "data.bin", "precious ignored content\n")
	_, err := c.RestoreWorkingTree(ctx)
	var re *git.TreeRestoreError
	if !errors.As(err, &re) {
		t.Fatalf("RestoreWorkingTree = %v, want a *TreeRestoreError (an ignored file is in the way of the switch)", err)
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "data.bin")); string(b) != "precious ignored content\n" {
		t.Fatalf("the restore overwrote the ignored file: %q", b)
	}
	if got := swHead(t, repo); got != "refs/heads/feature" {
		t.Fatalf("HEAD = %s, want the switch refused", got)
	}
}
