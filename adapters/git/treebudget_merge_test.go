package git_test

// INIT-024 TASK-003 review round 1 (P2, both lenses): a merge is measured
// by its RESULT tree (git merge-tree --write-tree on the target tip), not
// by its sides. Fixtures are tiny.

import (
	"context"
	"errors"
	"fmt"
	"os"
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
