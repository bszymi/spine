package git_test

// INIT-024 TASK-003 review round 1 (P2, both lenses): a merge is measured
// by its RESULT tree (git merge-tree --write-tree on the target tip), not
// by its sides. Fixtures are tiny.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/testutil"
)

// mtBranch commits, on a new branch from base, n files of size bytes
// under dir/ and returns the branch name.
func mtBranch(t *testing.T, repo, base, name, dir string, n, size int) string {
	t.Helper()
	tbGit(t, repo, nil, "checkout", "-q", "-b", name, base)
	for i := 0; i < n; i++ {
		testutil.WriteFile(t, repo, fmt.Sprintf("%s/f%03d.md", dir, i), strings.Repeat("z", size)+fmt.Sprint(i))
	}
	tbGit(t, repo, nil, "add", "-A")
	tbGit(t, repo, nil, "commit", "-q", "-m", name)
	tbGit(t, repo, nil, "checkout", "-q", "main")
	return name
}

// mtEntries counts main's entries the way the budget does.
func mtEntries(t *testing.T, repo string) int {
	t.Helper()
	out := tbGit(t, repo, nil, "ls-tree", "-r", "-t", "--name-only", "main")
	if out == "" {
		return 0
	}
	return len(strings.Split(out, "\n"))
}

// mergeVariants runs a merge of source into main through Merge (by
// branch) and through the bound governed merge (by exact commit id).
var mergeVariants = []struct {
	name  string
	merge func(t *testing.T, repo string, c *git.CLIClient, source string) error
}{
	{"Merge", func(_ *testing.T, _ string, c *git.CLIClient, source string) error {
		_, err := c.Merge(context.Background(), git.MergeOpts{Source: source, Target: "main", Strategy: "merge-commit", Message: "m"})
		return err
	}},
	{"MergeBound", func(t *testing.T, repo string, c *git.CLIClient, source string) error {
		sha := tbGit(t, repo, nil, "rev-parse", source)
		_, err := c.MergeBound(context.Background(), git.MergeOpts{SourceSHA: sha, Target: "main", Strategy: "merge-commit", Message: "m"})
		return err
	}},
}

// TestMerge_ResultTreeIsMeasured (correctness review reproduction): two
// branches opened from one base each add 59 files under their own
// directory — 61 entries each, within MaxEntries 100 — but merging both
// would leave 121. The first merge lands; the second is refused before
// anything is written, and main stays within the budget. At 9263fbd1 both
// were admitted (each side measured alone) and main reached 121 entries,
// after which every checkout and merge of main was refused.
func TestMerge_ResultTreeIsMeasured(t *testing.T) {
	for _, v := range mergeVariants {
		t.Run(v.name, func(t *testing.T) {
			repo, base, _, _ := tbRepo(t)
			b1 := mtBranch(t, repo, base, "b1", "d1", 59, 1)
			b2 := mtBranch(t, repo, base, "b2", "d2", 59, 1)
			c := git.NewCLIClient(repo, git.WithTreeBudget(git.TreeBudget{MaxEntries: 100, MaxTotalBytes: 1 << 30, MaxPathBytesTotal: 1 << 30}))
			if err := v.merge(t, repo, c, b1); err != nil {
				t.Fatalf("first merge: %v", err)
			}
			before := tbGit(t, repo, nil, "rev-parse", "main")
			requireTreeTooLarge(t, v.merge(t, repo, c, b2), git.TreeLimitEntries)
			if got := tbGit(t, repo, nil, "rev-parse", "main"); got != before {
				t.Fatalf("main moved on the refused merge: %s -> %s", before, got)
			}
			if n := mtEntries(t, repo); n > 100 {
				t.Fatalf("main has %d entries, over the budget", n)
			}
			if _, err := os.Stat(filepath.Join(repo, "d2")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("the refused merge wrote d2/: %v", err)
			}
			if st := tbGit(t, repo, nil, "status", "--porcelain"); st != "" {
				t.Fatalf("working tree changed:\n%s", st)
			}
		})
	}
}

// TestMerge_DisjointSidesAreMeasuredTogether (security review
// reproduction): two disjoint 60 KiB sides under MaxTotalBytes 64 KiB —
// each fits, their union does not. Also the path-bytes limit on a union.
func TestMerge_DisjointSidesAreMeasuredTogether(t *testing.T) {
	for _, tc := range []struct {
		name   string
		budget git.TreeBudget
		size   int
		limit  git.TreeLimit
	}{
		{"bytes", git.TreeBudget{MaxEntries: 1000, MaxTotalBytes: 64 << 10, MaxPathBytesTotal: 1 << 20}, 60 << 10, git.TreeLimitTotalBytes},
		{"path bytes", git.TreeBudget{MaxEntries: 1000, MaxTotalBytes: 1 << 30, MaxPathBytesTotal: 700}, 1, git.TreeLimitPathBytes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, base, _, _ := tbRepo(t)
			n := 1
			if tc.size == 1 {
				n = 40 // 40 paths of ~16 bytes per side: ~650 each, ~1300 together
			}
			b1 := mtBranch(t, repo, base, "b1", "d1", n, tc.size)
			b2 := mtBranch(t, repo, base, "b2", "d2", n, tc.size)
			c := git.NewCLIClient(repo, git.WithTreeBudget(tc.budget))
			if _, err := c.Merge(context.Background(), git.MergeOpts{Source: b1, Target: "main", Strategy: "merge-commit", Message: "m"}); err != nil {
				t.Fatalf("first side: %v", err)
			}
			_, err := c.Merge(context.Background(), git.MergeOpts{Source: b2, Target: "main", Strategy: "merge-commit", Message: "m"})
			requireTreeTooLarge(t, err, tc.limit)
		})
	}
}

// TestMerge_RepairOfAnOverBudgetMainIsAdmitted: when main is already over
// the budget (an upgrade, a lowered limit), a merge whose RESULT fits —
// one that deletes files — is admitted, and so is a replay that writes
// nothing; the target alone is never what decides. A merge that keeps
// main over the budget is still refused.
func TestMerge_RepairOfAnOverBudgetMainIsAdmitted(t *testing.T) {
	ctx := context.Background()
	repo, base, _, _ := tbRepo(t)
	big := mtBranch(t, repo, base, "big", "d", 119, 1) // 1 + 1 + 119 = 121 entries
	tbGit(t, repo, nil, "merge", "-q", "--ff-only", big)
	if n := mtEntries(t, repo); n != 121 {
		t.Fatalf("fixture: main has %d entries", n)
	}
	// repair: from main, delete 40 files.
	tbGit(t, repo, nil, "checkout", "-q", "-b", "repair", "main")
	for i := 0; i < 40; i++ {
		tbGit(t, repo, nil, "rm", "-q", fmt.Sprintf("d/f%03d.md", i))
	}
	tbGit(t, repo, nil, "commit", "-q", "-m", "repair")
	// grow: from main, add one file.
	tbGit(t, repo, nil, "checkout", "-q", "-b", "grow", "main")
	testutil.WriteFile(t, repo, "more.md", "more\n")
	tbGit(t, repo, nil, "add", "more.md")
	tbGit(t, repo, nil, "commit", "-q", "-m", "grow")
	tbGit(t, repo, nil, "checkout", "-q", "main")

	c := git.NewCLIClient(repo, git.WithTreeBudget(git.TreeBudget{MaxEntries: 100, MaxTotalBytes: 1 << 30, MaxPathBytesTotal: 1 << 30}))
	_, err := c.Merge(ctx, git.MergeOpts{Source: "grow", Target: "main", Strategy: "merge-commit", Message: "m"})
	requireTreeTooLarge(t, err, git.TreeLimitEntries)
	// A replay of what main already holds writes nothing.
	if _, err := c.Merge(ctx, git.MergeOpts{Source: big, Target: "main", Strategy: "merge-commit", Message: "m"}); err != nil {
		t.Fatalf("an up-to-date replay on an over-budget main: %v", err)
	}
	if _, err := c.Merge(ctx, git.MergeOpts{Source: "repair", Target: "main", Strategy: "merge-commit", Message: "m"}); err != nil {
		t.Fatalf("a repair merge whose result fits: %v", err)
	}
	if n := mtEntries(t, repo); n > 100 {
		t.Fatalf("main has %d entries after the repair", n)
	}
}

// TestMerge_SwitchToAnOverBudgetTargetIsMeasured: when HEAD is on another
// branch, the switch to the target writes the target's whole tree — a
// checkout — so an over-budget target is refused even when the merge's
// result would fit. (Mutation: dropping the switch measure admits it.)
func TestMerge_SwitchToAnOverBudgetTargetIsMeasured(t *testing.T) {
	ctx := context.Background()
	repo, base, _, _ := tbRepo(t)
	big := mtBranch(t, repo, base, "big", "d", 119, 1)
	tbGit(t, repo, nil, "checkout", "-q", "-b", "shrink", big)
	tbGit(t, repo, nil, "rm", "-q", "-r", "d")
	tbGit(t, repo, nil, "commit", "-q", "-m", "shrink")
	tbGit(t, repo, nil, "checkout", "-q", "main") // HEAD is not on big
	c := git.NewCLIClient(repo, git.WithTreeBudget(git.TreeBudget{MaxEntries: 100, MaxTotalBytes: 1 << 30, MaxPathBytesTotal: 1 << 30}))
	_, err := c.Merge(ctx, git.MergeOpts{Source: "shrink", Target: big, Strategy: "merge-commit", Message: "m"})
	requireTreeTooLarge(t, err, git.TreeLimitEntries)
	if got := tbGit(t, repo, nil, "symbolic-ref", "HEAD"); got != "refs/heads/main" {
		t.Fatalf("HEAD switched to %s", got)
	}
}

// TestMerge_ConcurrentMergesSeeEachOther: two merges started at once, each
// within the budget alone, together over it: the working-tree hold
// serialises them and the second measures on the first's result, so
// exactly one lands and main stays within the budget.
func TestMerge_ConcurrentMergesSeeEachOther(t *testing.T) {
	repo, base, _, _ := tbRepo(t)
	b1 := mtBranch(t, repo, base, "b1", "d1", 59, 1)
	b2 := mtBranch(t, repo, base, "b2", "d2", 59, 1)
	c := git.NewCLIClient(repo, git.WithTreeBudget(git.TreeBudget{MaxEntries: 100, MaxTotalBytes: 1 << 30, MaxPathBytesTotal: 1 << 30}))
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, b := range []string{b1, b2} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = c.Merge(context.Background(), git.MergeOpts{Source: b, Target: "main", Strategy: "merge-commit", Message: "m"})
		}()
	}
	wg.Wait()
	ok, refused := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, git.ErrTreeTooLarge):
			refused++
		default:
			t.Fatalf("unexpected merge error: %v", err)
		}
	}
	if ok != 1 || refused != 1 {
		t.Fatalf("merged %d, refused %d; want exactly one of each", ok, refused)
	}
	if n := mtEntries(t, repo); n > 100 {
		t.Fatalf("main has %d entries", n)
	}
}

// TestMerge_TargetMovedAfterTheMeasureIsRefused: a target tip that moves
// between the result's measure and the merge (an out-of-process writer;
// here the measure hook moves it) is refused as transient instead of
// merged on a tip nobody measured.
func TestMerge_TargetMovedAfterTheMeasureIsRefused(t *testing.T) {
	for _, v := range mergeVariants {
		t.Run(v.name, func(t *testing.T) {
			repo, base, _, _ := tbRepo(t)
			b1 := mtBranch(t, repo, base, "b1", "d1", 3, 1)
			other := mtBranch(t, repo, base, "other", "o", 3, 1)
			otherTip := tbGit(t, repo, nil, "rev-parse", other)
			c := git.NewCLIClient(repo, git.WithTreeBudget(git.TreeBudget{}))
			c.SetMeasureHookForTest(func(git.MeasureTrace) {
				tbGit(t, repo, nil, "update-ref", "refs/heads/main", otherTip)
			})
			err := v.merge(t, repo, c, b1)
			var ge *git.GitError
			if !errors.As(err, &ge) || !ge.IsRetryable() || !strings.Contains(ge.Message, "moved") {
				t.Fatalf("err = %v, want the transient moved-target refusal", err)
			}
		})
	}
}

// TestMerge_NoBudgetRunsNoMergeTree: without a budget no merge-tree (or
// any other measuring process) runs — the argv stays what it was.
func TestMerge_NoBudgetRunsNoMergeTree(t *testing.T) {
	repo, base, _, _ := tbRepo(t)
	b1 := mtBranch(t, repo, base, "b1", "d1", 2, 1)
	rec := testutil.InstallGitRecorder(t)
	c := git.NewCLIClient(repo)
	if _, err := c.Merge(context.Background(), git.MergeOpts{Source: b1, Target: "main", Strategy: "merge-commit", Message: "m"}); err != nil {
		t.Fatal(err)
	}
	for _, call := range rec.Calls(t) {
		if len(call) > 0 && (call[0] == "merge-tree" || call[0] == "ls-tree") {
			t.Fatalf("a client with no budget ran %v", call)
		}
	}
}

// TestCheckBudgetGitVersion: the merge measure needs git >= 2.38.
func TestCheckBudgetGitVersion(t *testing.T) {
	for line, ok := range map[string]bool{
		"git version 2.37.7":                 false,
		"git version 2.30.0":                 false,
		"git version 2.38.0":                 true,
		"git version 2.39.5":                 true,
		"git version 2.54.0 (Apple Git-157)": true,
		"git version 3.0.0":                  true,
		"no git here":                        false,
	} {
		if err := git.CheckBudgetGitVersionOutputForTest(line); (err == nil) != ok {
			t.Errorf("%q: err = %v, want ok=%v", line, err, ok)
		}
	}
	if err := git.CheckBudgetGitVersion(context.Background()); err != nil {
		t.Fatalf("this host's git: %v", err)
	}
}

// mtSides commits, on top of base, a target T and a source S that change
// the same line of each of files f0..f{n-1}.txt differently; T also
// carries extraAttrs in its .gitattributes. Returns their ids.
func mtSides(t *testing.T, repo, base string, n int, extraAttrs string) (target, source string) {
	t.Helper()
	mk := func(word string, attrs bool) map[string][]byte {
		m := map[string][]byte{}
		for i := 0; i < n; i++ {
			m[fmt.Sprintf("f%d.txt", i)] = []byte(word + "\nsep\nsep\nsep\n")
		}
		if attrs {
			m[".gitattributes"] = []byte(extraAttrs)
		}
		return m
	}
	// base: the files with "base", and README.md from tbRepo's base.
	baseFiles := mk("base", false)
	baseFiles["README.md"] = []byte("hello\n")
	b := ntTree(t, repo, base, baseFiles)
	tf := mk("ours", true)
	tf["README.md"] = []byte("hello\n")
	sf := mk("theirs", false)
	sf["README.md"] = []byte("hello\n")
	return ntTree(t, repo, b, tf), ntTree(t, repo, b, sf)
}

// TestMerge_ConvergenceMarkersAreBoundedByTheNeutralLine (review round 2,
// P1-C): convergence merges a divergence branch into a run branch while
// the shared tree's HEAD is main. A run branch that sets
// `conflict-marker-size` large made `git merge` (which reads the target's
// attributes after the switch) write markers that long, while the result
// had been measured under main's attributes — 90-300 MB past a 16 MiB
// budget in the review. With the neutral line every conflict marker is 7
// characters, and the result is measured after the switch.
func TestMerge_ConvergenceMarkersAreBoundedByTheNeutralLine(t *testing.T) {
	ctx := context.Background()
	repo, base, _, _ := tbRepo(t)
	T, S := mtSides(t, repo, base, 3, "* conflict-marker-size=100000\n")
	tbGit(t, repo, nil, "update-ref", "refs/heads/spine/run/r1", T)
	tbGit(t, repo, nil, "update-ref", "refs/heads/spine/div/d1", S)
	budget := git.TreeBudget{MaxEntries: 1000, MaxTotalBytes: 64 << 10, MaxPathBytesTotal: 1 << 20}
	c := git.NewCLIClient(repo, git.WithHomeBranch("main"), git.WithTreeBudget(budget))
	_, err := c.Merge(ctx, git.MergeOpts{Source: "spine/div/d1", Target: "spine/run/r1", Strategy: "merge-commit", Message: "m"})
	defer func() { _, _ = c.Merge(ctx, git.MergeOpts{Source: "--abort", Strategy: "abort"}) }()
	if errors.Is(err, git.ErrTreeTooLarge) {
		return // a bounded refusal is acceptable
	}
	var written int64
	for i := 0; i < 3; i++ {
		b, rerr := os.ReadFile(filepath.Join(repo, fmt.Sprintf("f%d.txt", i)))
		if rerr != nil {
			t.Fatal(rerr)
		}
		written += int64(len(b))
		if !strings.Contains(string(b), "\n<<<<<<< ") && !strings.HasPrefix(string(b), "<<<<<<< ") {
			t.Fatalf("f%d.txt has no 7-character conflict marker (merge err %v):\n%.200s", i, err, b)
		}
		if strings.Contains(string(b), "<<<<<<<<") {
			t.Fatalf("f%d.txt has a conflict marker longer than 7 characters (%d bytes)", i, len(b))
		}
	}
	if written > budget.MaxTotalBytes {
		t.Fatalf("the conflicted files total %d bytes, over the %d-byte budget", written, budget.MaxTotalBytes)
	}
}

// TestMerge_ResultIsComputedInTheTargetsWorkingTree (review round 2,
// P1-C): merge-tree reads merge attributes from the working tree it runs
// in. The target sets `merge=union` and main does not; computed before the
// switch (in main's tree) the measured result had conflict markers, while
// `git merge` after the switch unions cleanly. Now the measured tree is
// the tree the merge commits. (Mutation: computing it before the switch
// fails this test.)
func TestMerge_ResultIsComputedInTheTargetsWorkingTree(t *testing.T) {
	for _, v := range mergeVariants {
		t.Run(v.name, func(t *testing.T) {
			repo, base, _, _ := tbRepo(t)
			T, S := mtSides(t, repo, base, 1, "*.txt merge=union\n")
			tbGit(t, repo, nil, "update-ref", "refs/heads/target", T)
			tbGit(t, repo, nil, "update-ref", "refs/heads/source", S)
			c := git.NewCLIClient(repo, git.WithTreeBudget(git.TreeBudget{}))
			var measured []string
			c.SetMergeResultHookForTest(func(tree string) { measured = append(measured, tree) })
			var err error
			if v.name == "Merge" {
				_, err = c.Merge(context.Background(), git.MergeOpts{Source: "source", Target: "target", Strategy: "merge-commit", Message: "m"})
			} else {
				_, err = c.MergeBound(context.Background(), git.MergeOpts{SourceSHA: S, Target: "target", Strategy: "merge-commit", Message: "m"})
			}
			if err != nil {
				t.Fatalf("union merge: %v", err)
			}
			got := tbGit(t, repo, nil, "rev-parse", "target^{tree}")
			if len(measured) != 1 || measured[0] != got {
				t.Fatalf("measured %v, the merge committed tree %s", measured, got)
			}
		})
	}
}

// gitEnvRecorder puts a git wrapper first on PATH that logs, for every
// merge-tree and merge, the GIT_ALLOC_LIMIT it ran with.
func gitEnvRecorder(t *testing.T) string {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	d := t.TempDir()
	log := filepath.Join(d, "env.log")
	script := "#!/bin/sh\nfor a in \"$@\"; do case \"$a\" in merge-tree|merge) echo \"$a GIT_ALLOC_LIMIT=$GIT_ALLOC_LIMIT\" >> " + log + "; break;; -*) ;; *) break;; esac; done\nexec " + gitPath + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(d, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", d+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

// TestMerge_RunsUnderTheMergeAllocLimit (review round 2): merge-tree and
// the measured `git merge` run under GIT_ALLOC_LIMIT = max(MaxTotalBytes,
// the read allocation limit); without a budget neither gets one.
func TestMerge_RunsUnderTheMergeAllocLimit(t *testing.T) {
	repo, base, _, _ := tbRepo(t)
	b1 := mtBranch(t, repo, base, "b1", "d1", 2, 1)
	log := gitEnvRecorder(t)
	c := git.NewCLIClient(repo, git.WithTreeBudget(git.TreeBudget{MaxTotalBytes: 64 << 20}))
	if _, err := c.Merge(context.Background(), git.MergeOpts{Source: b1, Target: "main", Strategy: "merge-commit", Message: "m"}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(log)
	want := fmt.Sprintf("GIT_ALLOC_LIMIT=%d", 64<<20)
	if !strings.Contains(string(got), "merge-tree "+want) || !strings.Contains(string(got), "merge "+want) {
		t.Fatalf("recorded:\n%s\nwant merge-tree and merge with %s", got, want)
	}

	repo2, base2, _, _ := tbRepo(t)
	b2 := mtBranch(t, repo2, base2, "b2", "d2", 2, 1)
	_ = os.Remove(log)
	if _, err := git.NewCLIClient(repo2).Merge(context.Background(), git.MergeOpts{Source: b2, Target: "main", Strategy: "merge-commit", Message: "m"}); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(log)
	if strings.Contains(string(got), "merge-tree") || strings.Contains(string(got), "GIT_ALLOC_LIMIT=1") || strings.Contains(string(got), "GIT_ALLOC_LIMIT=2") || strings.Contains(string(got), "GIT_ALLOC_LIMIT=6") {
		t.Fatalf("an unbudgeted merge ran with a merge alloc limit:\n%s", got)
	}
}

// TestMerge_AllocRefusalIsABudgetRefusal: a merge whose computation needs
// one allocation over its limit (a 2 MB file changed on both sides, under
// a 1 MiB limit) is refused as ErrTreeTooLarge with
// MergeAllocRefusedMessage, before `git merge` runs. (Mutation: without
// the limit merge-tree succeeds and the refusal is the measure's.)
func TestMerge_AllocRefusalIsABudgetRefusal(t *testing.T) {
	repo, base, _, _ := tbRepo(t)
	var big strings.Builder
	for i := 0; i < 100000; i++ {
		fmt.Fprintf(&big, "line %07d\n", i)
	}
	lines := big.String() // ~1.3 MB
	tbGit(t, repo, nil, "checkout", "-q", "-b", "grow", base)
	testutil.WriteFile(t, repo, "big.txt", lines)
	tbGit(t, repo, nil, "add", "big.txt")
	tbGit(t, repo, nil, "commit", "-q", "-m", "big")
	grown := tbGit(t, repo, nil, "rev-parse", "HEAD")
	tbGit(t, repo, nil, "checkout", "-q", "-b", "a", grown)
	testutil.WriteFile(t, repo, "big.txt", "head a\n"+lines)
	tbGit(t, repo, nil, "commit", "-q", "-am", "a")
	tbGit(t, repo, nil, "checkout", "-q", "-b", "b", grown)
	testutil.WriteFile(t, repo, "big.txt", lines+"tail b\n")
	tbGit(t, repo, nil, "commit", "-q", "-am", "b")
	tbGit(t, repo, nil, "checkout", "-q", "a")
	c := git.NewCLIClient(repo, git.WithMaxBlobBytes(64<<10), git.WithTreeBudget(git.TreeBudget{MaxEntries: 1000, MaxTotalBytes: 1 << 20, MaxPathBytesTotal: 1 << 20}))
	_, err := c.Merge(context.Background(), git.MergeOpts{Source: "b", Target: "a", Strategy: "merge-commit", Message: "m"})
	var ge *git.GitError
	if !errors.Is(err, git.ErrTreeTooLarge) || !errors.As(err, &ge) || ge.Message != git.MergeAllocRefusedMessage {
		t.Fatalf("err = %v, want the merge allocation refusal", err)
	}
	if st := tbGit(t, repo, nil, "status", "--porcelain"); st != "" {
		t.Fatalf("working tree changed:\n%s", st)
	}
}
