package git_test

// INIT-024 TASK-003: the adversarial review of the checkout budget —
// ways around it that the AC tests do not cover. Fixtures stay tiny.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/testutil"
)

// TestTreeBudget_InPlaceRestoreIsBounded: on a home-branch tree (the
// shared standalone tree) the entry restore of an in-place write scope
// switches back to the home branch; an over-budget home branch is
// refused before the switch writes it.
func TestTreeBudget_InPlaceRestoreIsBounded(t *testing.T) {
	ctx := context.Background()
	repo, base, bomb, _ := tbRepo(t)
	tbGit(t, repo, nil, "branch", "elsewhere", base)
	tbGit(t, repo, nil, "checkout", "-q", "elsewhere")
	tbGit(t, repo, nil, "update-ref", "refs/heads/main", bomb)
	_ = git.NewCLIClient(repo, git.WithHomeBranch("main")) // registers the home branch
	_, err := git.EnterBranchWithLimits(ctx, repo, "", nil, git.CheckoutLimits{Budget: &smallBudget})
	var tre *git.TreeRestoreError
	if !errors.As(err, &tre) || !errors.Is(err, git.ErrTreeTooLarge) {
		t.Fatalf("in-place entry over an over-budget home branch: %v", err)
	}
	if got := tbGit(t, repo, nil, "symbolic-ref", "HEAD"); got != "refs/heads/elsewhere" {
		t.Fatalf("HEAD = %s", got)
	}
	if _, err := os.Stat(filepath.Join(repo, "docs")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the home branch's bomb was written: %v", err)
	}
}

// TestTreeBudget_ResetBranchIsBounded: the take-back's `read-tree -m -u`
// is measured before the ref moves; an over-budget `to` leaves the
// branch, index and tree where they were.
func TestTreeBudget_ResetBranchIsBounded(t *testing.T) {
	ctx := context.Background()
	repo, _, bomb, _ := tbRepo(t)
	// main: bomb, then a commit that removes docs/ (what is checked out).
	tbGit(t, repo, nil, "update-ref", "refs/heads/main", bomb)
	tbGit(t, repo, nil, "read-tree", "--reset", "main")
	clean := tbGit(t, repo, nil, "commit-tree", tbGit(t, repo, nil, "rev-parse", bomb+"^{tree}"), "-p", bomb, "-m", "same")
	readme := tbGit(t, repo, []byte("hello\n"), "hash-object", "-w", "--stdin")
	small := tbGit(t, repo, []byte("100644 blob "+readme+"\tREADME.md\n"), "mktree")
	m := tbGit(t, repo, nil, "commit-tree", small, "-p", clean, "-m", "drop docs")
	tbGit(t, repo, nil, "update-ref", "refs/heads/main", m)
	tbGit(t, repo, nil, "reset", "-q", "--hard", "main")
	if _, err := os.Stat(filepath.Join(repo, "docs")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fixture: docs/ checked out: %v", err)
	}

	c := git.NewCLIClient(repo, git.WithTreeBudget(smallBudget))
	err := c.ResetBranch(ctx, "main", bomb, m)
	requireTreeTooLarge(t, err, git.TreeLimitTotalBytes)
	if got := tbGit(t, repo, nil, "rev-parse", "refs/heads/main"); got != m {
		t.Fatalf("main moved to %s", got)
	}
	if _, err := os.Stat(filepath.Join(repo, "docs")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the take-back wrote the bomb: %v", err)
	}
}

// TestTreeBudget_RepeatedSmallPushesAreMeasuredWhole: each commit adds a
// little, and each is within any per-push delta — but every measure is of
// the whole tree, so the tip that crosses the budget is refused.
func TestTreeBudget_RepeatedSmallPushesAreMeasuredWhole(t *testing.T) {
	ctx := context.Background()
	repo := tbNeutralRepo(t)
	c := git.NewCLIClient(repo, git.WithTreeBudget(git.TreeBudget{MaxEntries: 25, MaxTotalBytes: 1 << 30, MaxPathBytesTotal: 1 << 30}))
	for i := 0; i < 30; i++ {
		testutil.WriteFile(t, repo, fmt.Sprintf("f%02d.md", i), "x\n")
		tbGit(t, repo, nil, "add", "-A")
		tbGit(t, repo, nil, "commit", "-q", "-m", "one more")
		err := c.Checkout(ctx, "main")
		if i < 24 && err != nil {
			t.Fatalf("commit %d (%d entries) refused: %v", i, i+1, err)
		}
		if i >= 25 {
			requireTreeTooLarge(t, err, git.TreeLimitEntries)
		}
	}
}

// TestTreeBudget_GitlinksAndSymlinksCount: entries that write no file
// content (gitlinks become empty directories, symbolic links tiny files)
// still count against the entry budget.
func TestTreeBudget_GitlinksAndSymlinksCount(t *testing.T) {
	ctx := context.Background()
	repo, base, _, _ := tbRepo(t)
	target := tbGit(t, repo, []byte("t"), "hash-object", "-w", "--stdin")
	var mk strings.Builder
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&mk, "160000 commit %s\tsub%03d\n120000 blob %s\tlink%03d\n", base, i, target, i)
	}
	tree := tbGit(t, repo, []byte(mk.String()), "mktree")
	commit := tbGit(t, repo, nil, "commit-tree", tree, "-p", base, "-m", "links")
	st, err := git.TreeBudget{MaxEntries: 100}.Measure(ctx, git.NewCLIClient(repo), commit)
	requireTreeTooLarge(t, err, git.TreeLimitEntries)
	if st.Gitlinks == 0 || st.Blobs == 0 {
		t.Fatalf("stats = %+v", st)
	}
}

// TestClone_RefusesOverBudgetTree: a clone checks out its remote's
// default branch, which nothing has measured (a code repository is
// whatever its remote holds). With a budget the clone is made with
// --no-checkout, measured, and written only within the budget; over it,
// the clone is refused and removed, so the pool never later takes a
// half-made clone for a usable one. A clone within the budget, and an
// empty repository, still work.
func TestClone_RefusesOverBudgetTree(t *testing.T) {
	ctx := context.Background()
	src, base, bomb, _ := tbRepo(t)
	c := git.NewCLIClient("", git.WithTreeBudget(smallBudget))

	tbGit(t, src, nil, "update-ref", "refs/heads/main", bomb)
	dst := filepath.Join(t.TempDir(), "clone")
	requireTreeTooLarge(t, c.Clone(ctx, "file://"+src, dst), git.TreeLimitTotalBytes)
	if _, err := os.Stat(dst); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the refused clone was left behind: %v", err)
	}
	// Into an existing empty directory: emptied again, not removed.
	empty := t.TempDir()
	requireTreeTooLarge(t, c.Clone(ctx, "file://"+src, empty), git.TreeLimitTotalBytes)
	if names := dirEntries(t, empty); len(names) != 0 {
		t.Fatalf("the refused clone left %v", names)
	}

	tbGit(t, src, nil, "update-ref", "refs/heads/main", base)
	ok := filepath.Join(t.TempDir(), "ok")
	if err := c.Clone(ctx, "file://"+src, ok); err != nil {
		t.Fatalf("clone within the budget: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(ok, "README.md")); err != nil || string(b) != "hello\n" {
		t.Fatalf("clone within the budget not checked out: %q, %v", b, err)
	}
	if st := tbGit(t, ok, nil, "status", "--porcelain"); st != "" {
		t.Fatalf("clone within the budget is dirty:\n%s", st)
	}
	if has, err := git.HasNeutralCheckoutAttributes(ctx, ok); !has || err != nil {
		t.Fatalf("a budgeted clone lacks the neutral checkout attributes: %v %v", has, err)
	}

	bare := t.TempDir()
	tbGit(t, bare, nil, "init", "-q", "--bare")
	emptyClone := filepath.Join(t.TempDir(), "empty")
	if err := c.Clone(ctx, "file://"+bare, emptyClone); err != nil {
		t.Fatalf("clone of an empty repository: %v", err)
	}
}

// TestClone_CheckoutKeepsTheCloneEnvironment (codex round 1, P2): the
// separate `reset --hard` that writes a budgeted clone runs with the
// clone's own environment. A client without ambient credentials keeps
// the host's global git config — here a smudge filter that leaves a
// marker — out of the checkout, as the single `git clone` did; a client
// with ambient credentials still gets it (the control proves the test
// can see the filter run).
func TestClone_CheckoutKeepsTheCloneEnvironment(t *testing.T) {
	ctx := context.Background()
	src := testutil.NewTempRepo(t)
	testutil.WriteFile(t, src, ".gitattributes", "*.md filter=leak\n")
	testutil.WriteFile(t, src, "a.md", "content\n")
	tbGit(t, src, nil, "add", "-A")
	tbGit(t, src, nil, "commit", "-q", "-m", "filtered")
	marker := filepath.Join(t.TempDir(), "smudged")
	global := filepath.Join(t.TempDir(), "gitconfig")
	// Quoted: an unquoted ";" starts a comment in a git config file.
	cfg := fmt.Sprintf("[filter \"leak\"]\n\tsmudge = \"touch '%s'; cat\"\n\tclean = cat\n", marker)
	if err := os.WriteFile(global, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)

	isolated := git.NewCLIClient("", git.WithTreeBudget(smallBudget), git.WithoutAmbientCredentials())
	if err := isolated.Clone(ctx, "file://"+src, filepath.Join(t.TempDir(), "iso")); err != nil {
		t.Fatalf("isolated clone: %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the host's smudge filter ran in an isolated clone's checkout: %v", err)
	}

	// The control: a deadline-only client also splits the clone, and its
	// reset runs with the host environment, so the filter runs. (A budgeted
	// client with ambient credentials refuses such a tree outright: the
	// host defines a smudge driver for it — TestTreeBudget_FilterDriverIsUnbounded.)
	ambient := git.NewCLIClient("", git.WithMutateTimeout(time.Minute))
	amb := filepath.Join(t.TempDir(), "amb")
	if err := ambient.Clone(ctx, "file://"+src, amb); err != nil {
		t.Fatalf("ambient clone: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(amb, "a.md")); err != nil || string(b) != "content\n" {
		t.Fatalf("ambient clone a.md = %q, %v", b, err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("control: the host's smudge filter did not run for an ambient clone: %v", err)
	}
}

// TestClone_MutateTimeoutBoundsTheCheckout (codex round 1, P2): a client
// with only a mutate deadline also clones --no-checkout and writes the
// tree under the deadline — a slow checkout is stopped and the clone
// removed — while the network part of the clone stays undeadlined.
func TestClone_MutateTimeoutBoundsTheCheckout(t *testing.T) {
	ctx := context.Background()
	src := testutil.NewTempRepo(t)
	testutil.WriteFile(t, src, ".gitattributes", "*.slow filter=slow\n")
	testutil.WriteFile(t, src, "b.slow", "slow\n")
	tbGit(t, src, nil, "add", "-A")
	tbGit(t, src, nil, "commit", "-q", "-m", "slow")
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, []byte("[filter \"slow\"]\n\tsmudge = \"exec 2>/dev/null; sleep 5; cat\"\n\tclean = cat\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	c := git.NewCLIClient("", git.WithMutateTimeout(300*time.Millisecond))
	dst := filepath.Join(t.TempDir(), "clone")
	start := time.Now()
	err := c.Clone(ctx, "file://"+src, dst)
	var mt *git.MutateTimeoutError
	if !errors.As(err, &mt) || time.Since(start) > 4*time.Second {
		t.Fatalf("Clone = %v after %s, want a *MutateTimeoutError well before the filter ends", err, time.Since(start))
	}
	if _, err := os.Stat(dst); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the stopped clone was left behind: %v", err)
	}
}

// TestClone_FailedCheckoutLeavesTheDirectoryEmpty (codex round 2, P2):
// cloning into an existing empty directory, a checkout that fails half
// way — here a required smudge filter that fails on the second file —
// leaves the directory empty again, not holding the files the reset had
// already written, so a retry can clone into it.
func TestClone_FailedCheckoutLeavesTheDirectoryEmpty(t *testing.T) {
	ctx := context.Background()
	src := testutil.NewTempRepo(t)
	testutil.WriteFile(t, src, ".gitattributes", "z.md filter=broken\n")
	testutil.WriteFile(t, src, "a.md", "first\n")
	testutil.WriteFile(t, src, "z.md", "last\n")
	tbGit(t, src, nil, "add", "-A")
	tbGit(t, src, nil, "commit", "-q", "-m", "files")
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, []byte("[filter \"broken\"]\n\tsmudge = false\n\tclean = cat\n\trequired = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	dst := t.TempDir()
	// A deadline-only client splits the clone too, and carries no neutral
	// attributes (a budgeted one would turn the failing filter off).
	c := git.NewCLIClient("", git.WithMutateTimeout(time.Minute))
	if err := c.Clone(ctx, "file://"+src, dst); err == nil {
		t.Fatal("a clone whose checkout failed reported success")
	}
	if names := dirEntries(t, dst); len(names) != 0 {
		t.Fatalf("the failed clone left %v in the directory", names)
	}
}

// TestTreeBudget_RestoreMeasuresHEADBeforeItsReset (codex round 3, P2):
// the shared tree's restore resets HEAD's tree whenever the index or the
// tree differs from it, and HEAD's ref can move under the tree without a
// checkout (here an update-ref of the checked-out home branch to the
// bomb). The reset is measured first: refused, nothing is written.
func TestTreeBudget_RestoreMeasuresHEADBeforeItsReset(t *testing.T) {
	ctx := context.Background()
	repo, _, bomb, _ := tbRepo(t)
	c := git.NewCLIClient(repo, git.WithHomeBranch("main"), git.WithTreeBudget(smallBudget))
	tbGit(t, repo, nil, "update-ref", "refs/heads/main", bomb)
	_, err := c.RestoreWorkingTree(ctx)
	if !errors.Is(err, git.ErrTreeTooLarge) {
		t.Fatalf("restore over an over-budget HEAD: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "docs")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the restore wrote the bomb: %v", err)
	}
}

// TestTreeBudget_TreeObjectOverTheAllocLimitSaysWhatItIs (review round 1,
// P3): one tree object larger than GIT_ALLOC_LIMIT (here one directory of
// 10,000 long names, ~1.3 MB, against the 1 MiB floor a 64 KiB per-blob
// limit gives) cannot be listed at all. The refusal says so, and that the
// tree limits are not the knob.
func TestTreeBudget_TreeObjectOverTheAllocLimitSaysWhatItIs(t *testing.T) {
	ctx := context.Background()
	repo, base, _, _ := tbRepo(t)
	blob := tbGit(t, repo, []byte("x"), "hash-object", "-w", "--stdin")
	var mk strings.Builder
	long := strings.Repeat("n", 90)
	for i := 0; i < 10000; i++ {
		fmt.Fprintf(&mk, "100644 blob %s\t%s%05d\n", blob, long, i)
	}
	tree := tbGit(t, repo, []byte(mk.String()), "mktree")
	commit := tbGit(t, repo, nil, "commit-tree", tree, "-p", base, "-m", "wide")
	c := git.NewCLIClient(repo, git.WithMaxBlobBytes(64<<10))
	_, err := git.TreeBudget{MaxEntries: 1 << 40, MaxTotalBytes: 1 << 40, MaxPathBytesTotal: 1 << 40}.Measure(ctx, c, commit)
	var ge *git.GitError
	if !errors.As(err, &ge) || ge.Message != git.TreeObjectTooLargeMessage || !errors.Is(err, git.ErrOutputTooLarge) || errors.Is(err, git.ErrTreeTooLarge) {
		t.Fatalf("err = %v, want the tree-object refusal", err)
	}
	if !strings.Contains(err.Error(), "raising the tree limits does not help") {
		t.Fatalf("message: %v", err)
	}
}
