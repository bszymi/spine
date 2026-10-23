package git_test

// INIT-024 TASK-003 (review finding N-1): the checkout and merge
// amplification budget. Every fixture here is built from a handful of
// tiny objects and stays small even when it IS written out (at the base
// commit the regression tests below check a fixture out in full): the
// largest expands to a few hundred KiB, so a budget failure can never
// fill the disk the test runs on.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/testutil"
)

// tbGit runs git in dir with a fixed identity and returns trimmed stdout.
func tbGit(t *testing.T, dir string, stdin []byte, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@x.invalid",
		"GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@x.invalid",
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, stderr.String())
	}
	return strings.TrimSpace(string(out))
}

// tbBomb writes the review PoC's object shape (scratch/git/f4,
// TestRV_CheckoutAmplification), scaled down: ONE blob of blobSize bytes
// named by fanout entries of docs/, committed on top of base. It returns
// the commit id. The objects cost one blob and two trees whatever the
// fanout; a checkout writes fanout x blobSize bytes.
func tbBomb(t *testing.T, repo, base string, fanout, blobSize int) string {
	t.Helper()
	blob := tbGit(t, repo, bytes.Repeat([]byte("x"), blobSize), "hash-object", "-w", "--stdin")
	var mk strings.Builder
	for i := 0; i < fanout; i++ {
		fmt.Fprintf(&mk, "100644 blob %s\tf%05d.md\n", blob, i)
	}
	inner := tbGit(t, repo, []byte(mk.String()), "mktree")
	root := tbGit(t, repo, []byte("040000 tree "+inner+"\tdocs\n"), "mktree")
	return tbGit(t, repo, nil, "commit-tree", root, "-p", base, "-m", "bomb")
}

// tbRepo is a repository on main with one small committed file, and a
// run branch holding the bomb (50 x 4 KiB = 200 KiB when written).
func tbRepo(t *testing.T) (repo, base, bomb, run string) {
	t.Helper()
	repo = testutil.NewTempRepo(t)
	testutil.WriteFile(t, repo, "README.md", "hello\n")
	tbGit(t, repo, nil, "add", "README.md")
	tbGit(t, repo, nil, "commit", "-q", "-m", "readme")
	base = tbGit(t, repo, nil, "rev-parse", "main")
	bomb = tbBomb(t, repo, base, 50, 4<<10)
	run = "spine/run/task-x-1a2b3c4d"
	tbGit(t, repo, nil, "update-ref", "refs/heads/"+run, bomb)
	return repo, base, bomb, run
}

// smallBudget refuses the bomb on bytes (200 KiB > 64 KiB) and admits
// the base tree (one 6-byte file).
var smallBudget = git.TreeBudget{MaxEntries: 1000, MaxTotalBytes: 64 << 10, MaxPathBytesTotal: 1 << 20}

// requireTreeTooLarge fails unless err is the typed, permanent budget
// refusal for limit.
func requireTreeTooLarge(t *testing.T, err error, limit git.TreeLimit) *git.TreeTooLargeError {
	t.Helper()
	if !errors.Is(err, git.ErrTreeTooLarge) {
		t.Fatalf("err = %v, want ErrTreeTooLarge", err)
	}
	var ge *git.GitError
	if !errors.As(err, &ge) || ge.Kind != git.ErrKindPermanent || ge.IsRetryable() || ge.Message != git.TreeTooLargeMessage {
		t.Fatalf("err = %#v, want a permanent *GitError %q", err, git.TreeTooLargeMessage)
	}
	var tl *git.TreeTooLargeError
	if !errors.As(err, &tl) {
		t.Fatalf("err = %v, want a *TreeTooLargeError cause", err)
	}
	if tl.Limit != limit || tl.Seen <= tl.Max {
		t.Fatalf("refusal = %+v, want limit %s with Seen > Max", tl, limit)
	}
	return tl
}

// dirEntries lists the names in dir.
func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

// duBytes sums the regular-file bytes under root.
func duBytes(root string) (n int64, files int) {
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if fi, e := d.Info(); e == nil {
				n += fi.Size()
				files++
			}
		}
		return nil
	})
	return n, files
}

// --- AC 1: checkout is bounded ---

// TestEnterBranch_RefusesOverBudgetTree ports the review PoC
// (TestRV_CheckoutAmplification) with a small budget: the amplifying
// commit makes EnterBranch return ErrTreeTooLarge before anything exists
// on disk — no spine-wt-parent-* directory under TMPDIR, no worktree
// registration. At a560c210 EnterBranch wrote the expanded tree. Both
// ways a host turns the budget on are covered: the explicit limits and
// the process default `spine serve` installs.
func TestEnterBranch_RefusesOverBudgetTree(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		enter func(repo, branch string) (*git.WriteScope, error)
	}{
		{"explicit limits", func(repo, branch string) (*git.WriteScope, error) {
			return git.EnterBranchWithLimits(ctx, repo, branch, nil, git.CheckoutLimits{Budget: &smallBudget})
		}},
		{"process default", func(repo, branch string) (*git.WriteScope, error) {
			git.SetDefaultCheckoutLimits(git.CheckoutLimits{Budget: &smallBudget})
			t.Cleanup(git.ResetDefaultCheckoutLimitsForTest)
			return git.EnterBranch(ctx, repo, branch, nil)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, _, _, run := tbRepo(t)
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)

			scope, err := tc.enter(repo, run)
			if err == nil {
				n, nf := duBytes(scope.RepoDir)
				scope.Cleanup()
				t.Fatalf("EnterBranch admitted the amplifying tree and wrote %d files, %d bytes", nf, n)
			}
			requireTreeTooLarge(t, err, git.TreeLimitTotalBytes)
			if names := dirEntries(t, tmp); len(names) != 0 {
				t.Fatalf("TMPDIR after the refusal = %v, want empty (no worktree parent created)", names)
			}
			if wt := tbGit(t, repo, nil, "worktree", "list", "--porcelain"); strings.Count(wt, "worktree ") != 1 {
				t.Fatalf("a worktree was registered:\n%s", wt)
			}

			// The same budget admits a branch whose tree is within it.
			tbGit(t, repo, nil, "branch", "small", "main")
			ok, err := tc.enter(repo, "small")
			if err != nil {
				t.Fatalf("EnterBranch(small) under the budget: %v", err)
			}
			if _, err := os.Stat(filepath.Join(ok.RepoDir, "README.md")); err != nil {
				t.Fatalf("small tree not checked out: %v", err)
			}
			ok.Cleanup()
		})
	}
}

// TestEnterBranch_RefusesEntryAndPathBombs covers the other two limits on
// the same path: many tiny entries (one empty blob 2,000 times), a deep
// tree (each level adds to every path below it) and a single long path.
func TestEnterBranch_RefusesEntryAndPathBombs(t *testing.T) {
	ctx := context.Background()
	repo, base, _, _ := tbRepo(t)
	budget := git.TreeBudget{MaxEntries: 500, MaxTotalBytes: 1 << 20, MaxPathBytesTotal: 16 << 10}

	entries := tbBomb(t, repo, base, 2000, 0)
	tbGit(t, repo, nil, "update-ref", "refs/heads/entries", entries)

	// depth 150, one-byte names: the path at depth k is 2k-1 bytes, so
	// the paths sum to 150^2 = 22,500 bytes (and the file at the bottom):
	// 151 entries, under the entry limit, over the 16 KiB path limit.
	leaf := tbGit(t, repo, []byte("100644 blob "+tbGit(t, repo, []byte("x"), "hash-object", "-w", "--stdin")+"\tf\n"), "mktree")
	tree := leaf
	for i := 0; i < 150; i++ {
		tree = tbGit(t, repo, []byte("040000 tree "+tree+"\td\n"), "mktree")
	}
	deep := tbGit(t, repo, nil, "commit-tree", tree, "-p", base, "-m", "deep")
	tbGit(t, repo, nil, "update-ref", "refs/heads/deep", deep)

	long := strings.Repeat("n", 200)
	ltree := tbGit(t, repo, []byte("100644 blob "+tbGit(t, repo, []byte("y"), "hash-object", "-w", "--stdin")+"\t"+long+"\n"), "mktree")
	longC := tbGit(t, repo, nil, "commit-tree", ltree, "-p", base, "-m", "long")
	tbGit(t, repo, nil, "update-ref", "refs/heads/long", longC)

	for _, tc := range []struct {
		branch string
		budget git.TreeBudget
		limit  git.TreeLimit
	}{
		{"entries", budget, git.TreeLimitEntries},
		{"deep", budget, git.TreeLimitPathBytes},
		{"long", git.TreeBudget{MaxEntries: 10, MaxTotalBytes: 10, MaxPathBytesTotal: 100}, git.TreeLimitPathBytes},
	} {
		t.Run(tc.branch, func(t *testing.T) {
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)
			b := tc.budget
			scope, err := git.EnterBranchWithLimits(ctx, repo, tc.branch, nil, git.CheckoutLimits{Budget: &b})
			if err == nil {
				scope.Cleanup()
				t.Fatal("admitted")
			}
			requireTreeTooLarge(t, err, tc.limit)
			if names := dirEntries(t, tmp); len(names) != 0 {
				t.Fatalf("TMPDIR = %v, want empty", names)
			}
		})
	}
}

// --- AC 2: merge is bounded ---

// TestMerge_RefusesOverBudgetTree: the same commit as a merge source —
// a branch, and an exact SourceSHA — is refused before the working tree
// changes; main does not move and no file of the bomb is written. A
// merge INTO an over-budget target is refused too (the switch would write
// it).
func TestMerge_RefusesOverBudgetTree(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		opts func(bomb, run string) git.MergeOpts
	}{
		{"branch source", func(_, run string) git.MergeOpts {
			return git.MergeOpts{Source: run, Target: "main", Strategy: "merge-commit", Message: "m"}
		}},
		{"source sha", func(bomb, _ string) git.MergeOpts {
			return git.MergeOpts{SourceSHA: bomb, Target: "main", Strategy: "merge-commit", Message: "m"}
		}},
		{"fast-forward", func(_, run string) git.MergeOpts {
			return git.MergeOpts{Source: run, Target: "main", Strategy: "fast-forward"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, base, bomb, run := tbRepo(t)
			c := git.NewCLIClient(repo, git.WithTreeBudget(smallBudget))
			_, err := c.Merge(ctx, tc.opts(bomb, run))
			requireTreeTooLarge(t, err, git.TreeLimitTotalBytes)
			assertUntouched(t, repo, base)
		})
	}

	t.Run("over-budget target", func(t *testing.T) {
		repo, base, bomb, _ := tbRepo(t)
		tbGit(t, repo, nil, "branch", "big", bomb)
		tbGit(t, repo, nil, "branch", "small", base)
		c := git.NewCLIClient(repo, git.WithTreeBudget(smallBudget))
		_, err := c.Merge(ctx, git.MergeOpts{Source: "small", Target: "big", Strategy: "merge-commit", Message: "m"})
		requireTreeTooLarge(t, err, git.TreeLimitTotalBytes)
		assertUntouched(t, repo, base)
	})

	t.Run("within budget merges", func(t *testing.T) {
		repo, base, _, _ := tbRepo(t)
		tbGit(t, repo, nil, "checkout", "-q", "-b", "feature")
		testutil.WriteFile(t, repo, "notes.md", "note\n")
		tbGit(t, repo, nil, "add", "notes.md")
		tbGit(t, repo, nil, "commit", "-q", "-m", "notes")
		tbGit(t, repo, nil, "checkout", "-q", "main")
		c := git.NewCLIClient(repo, git.WithTreeBudget(smallBudget))
		res, err := c.Merge(ctx, git.MergeOpts{Source: "feature", Target: "main", Strategy: "merge-commit", Message: "m"})
		if err != nil {
			t.Fatalf("Merge within the budget: %v", err)
		}
		if res.SHA == base {
			t.Fatal("main did not move")
		}
	})
}

// TestMergeBound_RefusesOverBudgetTree: the bound governed merge refuses
// the bomb as its SourceSHA with nothing switched or merged.
func TestMergeBound_RefusesOverBudgetTree(t *testing.T) {
	ctx := context.Background()
	repo, base, bomb, _ := tbRepo(t)
	c := git.NewCLIClient(repo, git.WithTreeBudget(smallBudget))
	_, err := c.MergeBound(ctx, git.MergeOpts{SourceSHA: bomb, Target: "main", Strategy: "merge-commit", Message: "Merge run"})
	requireTreeTooLarge(t, err, git.TreeLimitTotalBytes)
	assertUntouched(t, repo, base)
}

// TestCheckout_RefusesOverBudgetTree: CLIClient.Checkout of the bomb
// branch is refused before `git switch`.
func TestCheckout_RefusesOverBudgetTree(t *testing.T) {
	ctx := context.Background()
	repo, base, _, run := tbRepo(t)
	c := git.NewCLIClient(repo, git.WithTreeBudget(smallBudget))
	requireTreeTooLarge(t, c.Checkout(ctx, run), git.TreeLimitTotalBytes)
	assertUntouched(t, repo, base)
	if err := c.Checkout(ctx, "main"); err != nil {
		t.Fatalf("Checkout(main) within the budget: %v", err)
	}
}

// assertUntouched: main is at base, HEAD on main, the working tree clean
// and free of the bomb's files.
func assertUntouched(t *testing.T, repo, base string) {
	t.Helper()
	if got := tbGit(t, repo, nil, "rev-parse", "refs/heads/main"); got != base {
		t.Fatalf("main moved: %s -> %s", base, got)
	}
	if got := tbGit(t, repo, nil, "symbolic-ref", "HEAD"); got != "refs/heads/main" {
		t.Fatalf("HEAD = %s, want refs/heads/main", got)
	}
	if st := tbGit(t, repo, nil, "status", "--porcelain"); st != "" {
		t.Fatalf("working tree changed:\n%s", st)
	}
	if _, err := os.Stat(filepath.Join(repo, "docs")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the bomb's docs/ was written: %v", err)
	}
}

// --- AC 3: MeasureTree is accurate and cheap ---

// TestMeasureTree_CountsEntriesAndBytes: every total matches git's own
// listing — blobs (files, an executable, a symbolic link), gitlinks,
// directories, sizes and path bytes — through a branch name, a commit id
// and a tree id.
func TestMeasureTree_CountsEntriesAndBytes(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewTempRepo(t)
	testutil.WriteFile(t, repo, "a/b/c.md", "content of c\n")
	testutil.WriteFile(t, repo, "a/d.md", strings.Repeat("d", 1000))
	testutil.WriteFile(t, repo, "top.sh", "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(repo, "top.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("d.md", filepath.Join(repo, "a", "link")); err != nil {
		t.Fatal(err)
	}
	tbGit(t, repo, nil, "add", "-A")
	tbGit(t, repo, nil, "commit", "-q", "-m", "files")
	head := tbGit(t, repo, nil, "rev-parse", "HEAD")
	tbGit(t, repo, nil, "update-index", "--add", "--cacheinfo", "160000,"+head+",vendor/sub")
	tbGit(t, repo, nil, "commit", "-q", "-m", "gitlink")
	commit := tbGit(t, repo, nil, "rev-parse", "HEAD")
	tree := tbGit(t, repo, nil, "rev-parse", "HEAD^{tree}")

	// What git says.
	var wantBlobs, wantGitlinks, wantBytes int64
	for _, line := range strings.Split(tbGit(t, repo, nil, "ls-tree", "-r", "-l", "HEAD"), "\n") {
		f := strings.Fields(line)
		switch f[1] {
		case "blob":
			wantBlobs++
			n, _ := strconv.ParseInt(f[3], 10, 64)
			wantBytes += n
		case "commit":
			wantGitlinks++
		}
	}
	var wantEntries, wantPathBytes int64
	for _, p := range strings.Split(strings.TrimSuffix(tbGit(t, repo, nil, "ls-tree", "-r", "-t", "-z", "--name-only", "HEAD"), "\x00"), "\x00") {
		wantEntries++
		wantPathBytes += int64(len(p))
	}
	wantTrees := wantEntries - wantBlobs - wantGitlinks

	c := git.NewCLIClient(repo)
	for _, rev := range []string{"main", commit, tree} {
		st, err := git.MeasureTree(ctx, c, rev)
		if err != nil {
			t.Fatalf("MeasureTree(%s): %v", rev, err)
		}
		if st.Entries != wantEntries || st.Blobs != wantBlobs || st.Gitlinks != wantGitlinks || st.Trees != wantTrees ||
			st.TotalBytes != wantBytes || st.PathBytes != wantPathBytes || !st.Complete || st.Expansion != 1 {
			t.Fatalf("MeasureTree(%s) = %+v; want entries %d blobs %d gitlinks %d trees %d bytes %d path bytes %d",
				rev, st, wantEntries, wantBlobs, wantGitlinks, wantTrees, wantBytes, wantPathBytes)
		}
	}
	if wantBlobs != 4 || wantGitlinks != 1 || wantTrees != 3 {
		t.Fatalf("fixture shape changed: blobs %d gitlinks %d trees %d", wantBlobs, wantGitlinks, wantTrees)
	}

	// A tree-ish that is not one is an error, never an empty tree.
	blob := tbGit(t, repo, nil, "rev-parse", "HEAD:top.sh")
	if _, err := git.MeasureTree(ctx, c, blob); err == nil {
		t.Fatal("MeasureTree of a blob succeeded")
	}
	if _, err := git.MeasureTree(ctx, c, "no-such-branch"); err == nil {
		t.Fatal("MeasureTree of a missing ref succeeded")
	}
	if _, err := git.MeasureTree(ctx, c, "--output=/tmp/x"); !errors.Is(err, git.ErrInvalidRef) {
		t.Fatalf("an option-shaped tree-ish: %v, want ErrInvalidRef", err)
	}
}

// TestMeasureTree_StopsAtFirstBudgetCrossing: an over-budget tree returns
// as soon as a running total crosses, before the listing is read. The
// tree has 20,000 entries (a ~1.4 MB listing); the parser is handed a
// small fraction of it (the measure hook reports what git delivered) and
// the totals stop one record past the limit.
func TestMeasureTree_StopsAtFirstBudgetCrossing(t *testing.T) {
	ctx := context.Background()
	repo, base, _, _ := tbRepo(t)
	big := tbBomb(t, repo, base, 20000, 16)
	full := len(tbGit(t, repo, nil, "ls-tree", "-r", "-t", "-l", "-z", big))
	if full < 1<<20 {
		t.Fatalf("fixture listing is %d bytes; want over 1 MiB", full)
	}
	for _, tc := range []struct {
		name   string
		budget git.TreeBudget
		limit  git.TreeLimit
	}{
		{"entries", git.TreeBudget{MaxEntries: 100, MaxTotalBytes: 1 << 40, MaxPathBytesTotal: 1 << 40}, git.TreeLimitEntries},
		{"total bytes", git.TreeBudget{MaxEntries: 1 << 40, MaxTotalBytes: 1000, MaxPathBytesTotal: 1 << 40}, git.TreeLimitTotalBytes},
		{"path bytes", git.TreeBudget{MaxEntries: 1 << 40, MaxTotalBytes: 1 << 40, MaxPathBytesTotal: 2000}, git.TreeLimitPathBytes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := git.NewCLIClient(repo)
			var trace git.MeasureTrace
			c.SetMeasureHookForTest(func(tr git.MeasureTrace) { trace = tr })
			st, err := tc.budget.Measure(ctx, c, big)
			tl := requireTreeTooLarge(t, err, tc.limit)
			if st.Complete {
				t.Fatal("stats claim the full listing was read")
			}
			if trace.Delivered >= int64(full)/4 {
				t.Fatalf("the parser was handed %d of %d listing bytes; want it stopped early", trace.Delivered, full)
			}
			if st.Entries > 400 {
				t.Fatalf("parsed %d records past a crossing at %s=%d", st.Entries, tl.Limit, tl.Max)
			}
		})
	}
	// The same tree under a budget that fits it is measured to the end.
	c := git.NewCLIClient(repo)
	st, err := git.TreeBudget{}.Measure(ctx, c, big)
	if err != nil || !st.Complete || st.Blobs != 20000 {
		t.Fatalf("default budget: %+v, %v", st, err)
	}
}

// --- AC 4: legitimate trees pass ---

// TestTreeBudget_RealRepositoryPasses: this repository's own HEAD tree is
// within the default budget with at least 10x headroom on every limit.
func TestTreeBudget_RealRepositoryPasses(t *testing.T) {
	top, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skip("not running inside a git work tree")
	}
	c := git.NewCLIClient(strings.TrimSpace(string(top)))
	st, err := git.MeasureTree(context.Background(), c, "HEAD")
	if err != nil {
		t.Fatalf("MeasureTree(HEAD): %v", err)
	}
	b := git.DefaultTreeBudget()
	if st.Entries*10 > b.MaxEntries || int64(float64(st.TotalBytes)*st.Expansion)*10 > b.MaxTotalBytes || st.PathBytes*10 > b.MaxPathBytesTotal {
		t.Fatalf("HEAD %+v has under 10x headroom against %+v", st, b)
	}
	if err := b.Check(context.Background(), c, "HEAD"); err != nil {
		t.Fatalf("Check(HEAD): %v", err)
	}
	t.Logf("HEAD: %d entries, %d bytes, %d path bytes, expansion %.2f", st.Entries, st.TotalBytes, st.PathBytes, st.Expansion)
}

// --- AC 7: the mutate deadline ---

// slowSmudgeRepo is a repository whose branch "slow" holds a file
// checked out through a smudge filter that sleeps 5 s. The filter's
// stderr is closed first so a stopped git does not leave the test's
// pipe held open by its orphaned filter.
func slowSmudgeRepo(t *testing.T) string {
	t.Helper()
	repo := testutil.NewTempRepo(t)
	tbGit(t, repo, nil, "config", "filter.slow.smudge", "exec 2>/dev/null; sleep 5; cat")
	tbGit(t, repo, nil, "config", "filter.slow.clean", "cat")
	testutil.WriteFile(t, repo, ".gitattributes", "*.slow filter=slow\n")
	tbGit(t, repo, nil, "add", ".gitattributes")
	tbGit(t, repo, nil, "commit", "-q", "-m", "attributes")
	tbGit(t, repo, nil, "checkout", "-q", "-b", "slow")
	testutil.WriteFile(t, repo, "b.slow", "slow content\n")
	tbGit(t, repo, nil, "add", "b.slow")
	tbGit(t, repo, nil, "commit", "-q", "-m", "slow file")
	tbGit(t, repo, nil, "checkout", "-q", "main")
	return repo
}

// TestCLIClient_MutateTimeoutCancels: a mutating command past the
// deadline is stopped — git receives SIGTERM (its exit status says so)
// and its own handler removes .git/index.lock — and the call returns the
// permanent *MutateTimeoutError, well before the filter would finish.
// The caller's own context has no deadline. Checkout (runSink), the
// merge binding's plumbing (runWith) and EnterBranch's worktree add are
// each deadlined.
func TestCLIClient_MutateTimeoutCancels(t *testing.T) {
	const d = 300 * time.Millisecond
	for _, tc := range []struct {
		name string
		run  func(repo string) error
	}{
		{"checkout", func(repo string) error {
			return git.NewCLIClient(repo, git.WithMutateTimeout(d)).Checkout(context.Background(), "slow")
		}},
		{"read-tree", func(repo string) error {
			c := git.NewCLIClient(repo, git.WithMutateTimeout(d))
			main := tbGit(t, repo, nil, "rev-parse", "main")
			slow := tbGit(t, repo, nil, "rev-parse", "slow")
			return c.ReadTreeForTest(context.Background(), main, slow)
		}},
		{"worktree add", func(repo string) error {
			t.Setenv("TMPDIR", t.TempDir())
			scope, err := git.EnterBranchWithLimits(context.Background(), repo, "slow", nil, git.CheckoutLimits{MutateTimeout: d})
			if err == nil {
				scope.Cleanup()
			}
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := slowSmudgeRepo(t)
			start := time.Now()
			err := tc.run(repo)
			elapsed := time.Since(start)
			if err == nil {
				t.Fatal("a command stopped at its deadline reported success")
			}
			if elapsed > 4*time.Second {
				t.Fatalf("returned after %s; the deadline was %s", elapsed, d)
			}
			var mt *git.MutateTimeoutError
			if !errors.As(err, &mt) || mt.Timeout != d || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("err = %v, want a *MutateTimeoutError (%s) matching context.DeadlineExceeded", err, d)
			}
			var ge *git.GitError
			if !errors.As(err, &ge) || ge.Kind != git.ErrKindPermanent || ge.Message != git.MutateTimeoutMessage {
				t.Fatalf("err = %#v, want a permanent *GitError %q", err, git.MutateTimeoutMessage)
			}
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("err = %v carries no *exec.ExitError", err)
			}
			if sig, ok := exitSignal(exitErr); !ok || sig != "terminated" {
				t.Fatalf("git exit = %v (signal %q); want it terminated by SIGTERM", exitErr, sig)
			}
			if _, err := os.Stat(filepath.Join(repo, ".git", "index.lock")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf(".git/index.lock after the stop: %v", err)
			}
		})
	}

	// Without a deadline the same checkout runs to completion.
	repo := slowSmudgeRepo(t)
	if testing.Short() {
		t.Skip("the undeadlined control waits for the 5 s filter")
	}
	if err := git.NewCLIClient(repo).Checkout(context.Background(), "slow"); err != nil {
		t.Fatalf("undeadlined checkout: %v", err)
	}
}

// --- AC 8: error mapping ---

// TestErrTreeTooLarge_IsPermanent: a budget refusal is a permanent,
// non-retryable *GitError matching ErrTreeTooLarge only — never the read
// bounds' ErrBlobTooLarge or ErrOutputTooLarge, and theirs never match it.
func TestErrTreeTooLarge_IsPermanent(t *testing.T) {
	repo, _, bomb, _ := tbRepo(t)
	c := git.NewCLIClient(repo)
	err := smallBudget.Check(context.Background(), c, bomb)
	tl := requireTreeTooLarge(t, err, git.TreeLimitTotalBytes)
	if errors.Is(err, git.ErrBlobTooLarge) || errors.Is(err, git.ErrOutputTooLarge) {
		t.Fatalf("ErrTreeTooLarge also matches a read-bound sentinel: %v", err)
	}
	if !strings.Contains(err.Error(), "MaxTotalBytes") || !strings.Contains(err.Error(), strconv.FormatInt(tl.Max, 10)) {
		t.Fatalf("message does not name the limit: %q", err)
	}
	if strings.Contains(err.Error(), bomb) {
		t.Fatalf("message echoes the tree-ish: %q", err)
	}
	for _, other := range []error{
		&git.GitError{Kind: git.ErrKindPermanent, Err: &git.BlobTooLargeError{Limit: 1}},
		&git.GitError{Kind: git.ErrKindPermanent, Err: &git.OutputTooLargeError{Limit: 1}},
	} {
		if errors.Is(other, git.ErrTreeTooLarge) {
			t.Fatalf("%v matches ErrTreeTooLarge", other)
		}
	}
}

// --- Opt-in (INIT-024 rule 2) ---

// TestTreeBudget_OffStartsNoProcess: with no budget and no deadline — a
// plain NewCLIClient in a process that never set a default, which is
// every embedder — Checkout, Merge, MergeBound and EnterBranch run exactly
// the argv they ran before this task: no ls-tree, no lookup added.
func TestTreeBudget_OffStartsNoProcess(t *testing.T) {
	ctx := context.Background()
	repo, base, _, _ := tbRepo(t)
	tbGit(t, repo, nil, "branch", "feature", base)
	rec := testutil.InstallGitRecorder(t)
	c := git.NewCLIClient(repo)
	if err := c.Checkout(ctx, "feature"); err != nil {
		t.Fatal(err)
	}
	if err := c.Checkout(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Merge(ctx, git.MergeOpts{Source: "feature", Target: "main", Strategy: "fast-forward"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.MergeBound(ctx, git.MergeOpts{SourceSHA: base, Target: "main", Strategy: "merge-commit", Message: "m"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", t.TempDir())
	scope, err := git.EnterBranch(ctx, repo, "feature", nil)
	if err != nil {
		t.Fatal(err)
	}
	scope.Cleanup()
	for _, call := range rec.Calls(t) {
		if len(call) > 0 && call[0] == "ls-tree" {
			t.Fatalf("a client with no budget ran %v", call)
		}
	}
}

// TestCheckoutLimits_ProcessDefault: SetDefaultCheckoutLimits reaches
// clients built before and after it; a client's own option replaces a
// default limit but nothing removes one.
func TestCheckoutLimits_ProcessDefault(t *testing.T) {
	t.Cleanup(git.ResetDefaultCheckoutLimitsForTest)
	before := git.NewCLIClient(t.TempDir())
	if l := before.CheckoutLimits(); l.Budget != nil || l.MutateTimeout != 0 {
		t.Fatalf("library default = %+v, want none", l)
	}
	git.SetDefaultCheckoutLimits(git.CheckoutLimits{Budget: &git.TreeBudget{MaxEntries: 7}, MutateTimeout: time.Minute})
	after := git.NewCLIClient(t.TempDir())
	for _, c := range []*git.CLIClient{before, after} {
		l := c.CheckoutLimits()
		if l.Budget == nil || l.Budget.MaxEntries != 7 || l.Budget.MaxTotalBytes != git.DefaultTreeMaxTotalBytes || l.MutateTimeout != time.Minute {
			t.Fatalf("CheckoutLimits() = %+v %+v", l, l.Budget)
		}
	}
	own := git.NewCLIClient(t.TempDir(), git.WithTreeBudget(git.TreeBudget{MaxEntries: 9}), git.WithMutateTimeout(0))
	if l := own.CheckoutLimits(); l.Budget.MaxEntries != 9 || l.MutateTimeout != time.Minute {
		t.Fatalf("own options over the default = %+v %+v", l, l.Budget)
	}
	if l := git.DefaultCheckoutLimits(); l.Budget == nil || l.Budget.MaxEntries != 7 {
		t.Fatalf("DefaultCheckoutLimits() = %+v", l)
	}
}
