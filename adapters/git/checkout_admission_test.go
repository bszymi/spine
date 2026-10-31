package git_test

// INIT-024 TASK-011: the claim's key and error class, the in-flight
// checkout ceiling (CheckoutLimits.InflightMaxBytes) through EnterBranch,
// and the process gate behind it. New API: these tests have no base-commit
// counterpart (the regressions are checkout_claim_regression_test.go).

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
)

// treeWeight is what EnterBranch weighs branch's tree at: its measured
// TotalBytes.
func treeWeight(t *testing.T, repo, branch string) int64 {
	t.Helper()
	st, err := git.MeasureTree(context.Background(), git.NewCLIClient(repo), branch)
	if err != nil {
		t.Fatal(err)
	}
	return st.TotalBytes
}

// waitUntil polls cond for up to 10 s.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// requireInflightWait fails unless err is the transient in-flight refusal
// for a tree of weight under ceiling, whose wait ended with ctxErr.
func requireInflightWait(t *testing.T, err error, weight, ceiling int64, ctxErr error) {
	t.Helper()
	if !errors.Is(err, git.ErrCheckoutsInFlight) || !errors.Is(err, ctxErr) {
		t.Fatalf("err = %v, want ErrCheckoutsInFlight and %v", err, ctxErr)
	}
	var ge *git.GitError
	if !errors.As(err, &ge) || ge.Kind != git.ErrKindTransient || !ge.IsRetryable() || ge.Message != git.CheckoutsInFlightMessage || ge.Op != "worktree" {
		t.Fatalf("err = %#v, want a retryable *GitError %q", err, git.CheckoutsInFlightMessage)
	}
	var we *git.InflightWaitError
	if !errors.As(err, &we) || we.Weight != weight || we.Ceiling != ceiling {
		t.Fatalf("cause = %+v, want weight %d under ceiling %d", we, weight, ceiling)
	}
	if errors.Is(err, git.ErrTreeTooLarge) || errors.Is(err, git.ErrBranchInUse) {
		t.Fatalf("err = %v matches another refusal", err)
	}
}

// canonical resolves p through symbolic links.
func canonical(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// --- the claim ---

// TestEnterBranch_BranchClaimIsPerRepository: the claim's key is (git
// common directory, branch). Another repository's branch of the same name
// and another branch of the same repository are separate; the same
// repository reached through a symbolic link, through a linked worktree
// of it, or as a bare repository is the same key.
func TestEnterBranch_BranchClaimIsPerRepository(t *testing.T) {
	ctx := context.Background()
	repoA, _, _, run := tbRepo(t)
	repoB, _, _, _ := tbRepo(t)
	isolateTempDir(t)

	held, err := git.EnterBranch(ctx, repoA, run, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Cleanup()

	other, err := git.EnterBranch(ctx, repoB, run, nil)
	if err != nil {
		t.Fatalf("same branch name, other repository: %v", err)
	}
	other.Cleanup()

	tbGit(t, repoA, nil, "branch", "side", "main")
	side, err := git.EnterBranch(ctx, repoA, "side", nil)
	if err != nil {
		t.Fatalf("other branch, same repository: %v", err)
	}
	side.Cleanup()

	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(repoA, link); err != nil {
		t.Fatal(err)
	}
	lw := filepath.Join(t.TempDir(), "lw")
	tbGit(t, repoA, nil, "worktree", "add", "-q", "-b", "lw-branch", lw, "main")
	for name, dir := range map[string]string{"symbolic link": link, "linked worktree": lw, "same path": repoA} {
		if s, err := git.EnterBranch(ctx, dir, run, nil); !errors.Is(err, git.ErrBranchInUse) {
			if err == nil {
				s.Cleanup()
			}
			t.Errorf("%s: err = %v, want ErrBranchInUse", name, err)
		}
	}

	bare := filepath.Join(t.TempDir(), "bare.git")
	tbGit(t, repoA, nil, "clone", "-q", "--bare", repoA, bare)
	bheld, err := git.EnterBranch(ctx, bare, run, nil)
	if err != nil {
		t.Fatalf("bare repository: %v", err)
	}
	if s, err := git.EnterBranch(ctx, bare, run, nil); !errors.Is(err, git.ErrBranchInUse) {
		if err == nil {
			s.Cleanup()
		}
		t.Errorf("bare repository, second entry: err = %v, want ErrBranchInUse", err)
	}
	bheld.Cleanup()
}

// TestRepoClaimKey_MatchesGitCommonDir: the in-process key is the
// directory `git rev-parse --git-common-dir` names, for every layout
// EnterBranch is given; a directory that is no repository keys by itself.
func TestRepoClaimKey_MatchesGitCommonDir(t *testing.T) {
	repo, _, _, _ := tbRepo(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(repo, link); err != nil {
		t.Fatal(err)
	}
	lw := filepath.Join(t.TempDir(), "lw")
	tbGit(t, repo, nil, "worktree", "add", "-q", "-b", "lw-branch", lw, "main")
	rel := filepath.Join(t.TempDir(), "rel")
	tbGit(t, repo, nil, "worktree", "add", "-q", "-b", "rel-branch", rel, "main")
	// A relative gitdir in the .git file (git worktree repair writes
	// absolute ones; worktree.useRelativePaths writes relative ones).
	gd := strings.TrimSpace(strings.TrimPrefix(readFile(t, filepath.Join(rel, ".git")), "gitdir:"))
	r, err := filepath.Rel(canonical(t, rel), canonical(t, gd))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rel, ".git"), []byte("gitdir: "+r+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(t.TempDir(), "bare.git")
	tbGit(t, repo, nil, "clone", "-q", "--bare", repo, bare)

	for name, dir := range map[string]string{"main tree": repo, "symbolic link": link, "linked worktree": lw, "relative gitdir": rel, "bare": bare} {
		want := canonical(t, gitAbs(t, dir, tbGit(t, dir, nil, "rev-parse", "--git-common-dir")))
		if got := git.RepoClaimKeyForTest(dir); got != want {
			t.Errorf("%s: key %q, want git's common dir %q", name, got, want)
		}
	}
	plain := t.TempDir()
	if got := git.RepoClaimKeyForTest(plain); got != canonical(t, plain) {
		t.Errorf("not a repository: key %q, want the directory", got)
	}
}

func gitAbs(t *testing.T, dir, p string) string {
	t.Helper()
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(dir, p)
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p) //nolint:gosec // G304: a test fixture path
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestEnterBranch_BranchInUseKeepsTheRefusalClass is AC 4's
// classification half: the refusal is not a *GitError (git's own
// "already checked out" refusal was an untyped error, which the artifact
// and workflow services report as git_error), keeps the "add worktree for
// branch <n>: " prefix and names no temporary path; a validator still
// runs before the claim.
func TestEnterBranch_BranchInUseKeepsTheRefusalClass(t *testing.T) {
	ctx := context.Background()
	repo, _, _, run := tbRepo(t)
	tmp := isolateTempDir(t)
	held, err := git.EnterBranch(ctx, repo, run, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Cleanup()

	_, err = git.EnterBranch(ctx, repo, run, nil)
	if !errors.Is(err, git.ErrBranchInUse) {
		t.Fatalf("err = %v, want ErrBranchInUse", err)
	}
	var ge *git.GitError
	if errors.As(err, &ge) {
		t.Errorf("err = %#v is a *GitError; the refusal it replaces was untyped", err)
	}
	if !strings.HasPrefix(err.Error(), "add worktree for branch "+run+": ") || strings.Contains(err.Error(), tmp) {
		t.Errorf("message %q: want the add-worktree prefix and no temporary path", err)
	}

	sentinel := errors.New("validator refused")
	if _, err := git.EnterBranch(ctx, repo, run, func(string) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Errorf("validator on a held branch: err = %v, want the validator's error", err)
	}
}

// --- the in-flight ceiling (AC 3) ---

// TestEnterBranch_InflightCeilingBoundsDifferentBranches is AC 3: with a
// ceiling of 2.5 trees, six concurrent entries on six branches never hold
// more than the ceiling in measured bytes (at most two scopes at once,
// and the bytes under TMPDIR never above the ceiling); smaller trees
// still proceed together, every entry is admitted in the end, and the
// gate is empty afterwards.
func TestEnterBranch_InflightCeilingBoundsDifferentBranches(t *testing.T) {
	ctx := context.Background()
	repo, run := claimRepo(t)
	tip := tbGit(t, repo, nil, "rev-parse", run)
	const branches = 6
	for i := range branches {
		tbGit(t, repo, nil, "branch", fmt.Sprintf("b%d", i), tip)
	}
	weight := treeWeight(t, repo, run)
	ceiling := 2*weight + weight/2
	b := git.DefaultTreeBudget()
	limits := git.CheckoutLimits{Budget: &b, MutateTimeout: git.DefaultMutateTimeout, InflightMaxBytes: ceiling}
	tmp := isolateTempDir(t)

	watch := watchPeak(tmp)
	var inScope, maxInScope, gateOver atomic.Int64
	errs := make([]error, branches)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range branches {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			s, err := git.EnterBranchWithLimits(ctx, repo, fmt.Sprintf("b%d", i), nil, limits)
			if err != nil {
				errs[i] = err
				return
			}
			n := inScope.Add(1)
			for m := maxInScope.Load(); n > m && !maxInScope.CompareAndSwap(m, n); m = maxInScope.Load() {
			}
			if h := git.InflightHeldForTest(); h > ceiling {
				gateOver.Store(h)
			}
			time.Sleep(200 * time.Millisecond)
			inScope.Add(-1)
			s.Cleanup()
		}()
	}
	close(start)
	wg.Wait()
	peak := watch.Stop()

	for i, err := range errs {
		if err != nil {
			t.Errorf("entry b%d: %v (every entry fits on its own and must be admitted in the end)", i, err)
		}
	}
	if m := maxInScope.Load(); m > 2 {
		t.Errorf("%d scopes held at once, want at most 2 under a 2.5-tree ceiling", m)
	} else if m < 2 {
		t.Errorf("%d scope(s) held at once, want 2: trees that fit together must proceed together", m)
	}
	if h := gateOver.Load(); h != 0 {
		t.Errorf("gate held %d > ceiling %d", h, ceiling)
	}
	if peak > ceiling {
		t.Errorf("peak bytes under TMPDIR %d > ceiling %d", peak, ceiling)
	}
	if h := git.InflightHeldForTest(); h != 0 {
		t.Errorf("gate holds %d after every Cleanup, want 0", h)
	}
	assertEmptyDir(t, tmp)
}

// TestEnterBranch_InflightWaitEndsWithRetryableError is AC 3: an entry
// whose tree does not fit beside the held ones waits; a context that ends
// first — or the mutate deadline, for a context with none — refuses it
// with the retryable ErrCheckoutsInFlight and nothing is written; a
// waiting entry is admitted as soon as the holder's Cleanup returns the
// room.
func TestEnterBranch_InflightWaitEndsWithRetryableError(t *testing.T) {
	ctx := context.Background()
	repo, run := claimRepo(t)
	tip := tbGit(t, repo, nil, "rev-parse", run)
	tbGit(t, repo, nil, "branch", "b0", tip)
	tbGit(t, repo, nil, "branch", "b1", tip)
	weight := treeWeight(t, repo, run)
	ceiling := weight + weight/2
	b := git.DefaultTreeBudget()
	limits := git.CheckoutLimits{Budget: &b, InflightMaxBytes: ceiling}
	tmp := isolateTempDir(t)

	held, err := git.EnterBranchWithLimits(ctx, repo, "b0", nil, limits)
	if err != nil {
		t.Fatal(err)
	}

	wctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	begun := time.Now()
	_, err = git.EnterBranchWithLimits(wctx, repo, "b1", nil, limits)
	cancel()
	requireInflightWait(t, err, weight, ceiling, context.DeadlineExceeded)
	if d := time.Since(begun); d < 250*time.Millisecond {
		t.Errorf("refused after %s: it must wait until the context ends", d)
	}

	bounded := limits
	bounded.MutateTimeout = 300 * time.Millisecond
	// The 20 s context only keeps a broken bound from hanging the test.
	safety, cancelSafety := context.WithTimeout(ctx, 20*time.Second)
	begun = time.Now()
	_, err = git.EnterBranchWithLimits(safety, repo, "b1", nil, bounded)
	cancelSafety()
	requireInflightWait(t, err, weight, ceiling, context.DeadlineExceeded)
	if d := time.Since(begun); d > 10*time.Second {
		t.Errorf("the mutate deadline did not bound the wait: %s", d)
	}

	if p := worktreeParents(t, tmp); len(p) != 1 {
		t.Errorf("worktree parents = %v, want only the held scope's", p)
	}
	if n := worktreeCount(t, repo); n != 2 {
		t.Errorf("worktree list has %d entries, want 2 (main and the held scope)", n)
	}
	if h, w := git.InflightHeldForTest(), git.InflightWaitersForTest(); h != weight || w != 0 {
		t.Errorf("gate holds %d with %d waiters, want %d and none", h, w, weight)
	}

	type result struct {
		s   *git.WriteScope
		err error
	}
	got := make(chan result, 1)
	go func() {
		// t.Context(): a failure below must not leave this wait in the
		// process-wide gate.
		s, err := git.EnterBranchWithLimits(t.Context(), repo, "b1", nil, limits)
		got <- result{s, err}
	}()
	waitUntil(t, "the entry to wait in the gate", func() bool { return git.InflightWaitersForTest() == 1 })
	held.Cleanup()
	select {
	case r := <-got:
		if r.err != nil {
			t.Fatalf("waiting entry after the holder's Cleanup: %v", r.err)
		}
		r.s.Cleanup()
	case <-time.After(10 * time.Second):
		t.Fatal("the waiting entry was not admitted after the holder's Cleanup")
	}
	if h := git.InflightHeldForTest(); h != 0 {
		t.Errorf("gate holds %d, want 0", h)
	}
	assertEmptyDir(t, tmp)
}

// TestEnterBranch_TreeOverTheInflightCeilingIsRefused is AC 3: a tree
// larger than the ceiling on its own could never be admitted, so it is
// refused at once — the permanent ErrTreeTooLarge (TreeLimitInflightBytes,
// Seen = the measured bytes) — even with the gate empty, and nothing is
// written; a tree within the ceiling is admitted.
func TestEnterBranch_TreeOverTheInflightCeilingIsRefused(t *testing.T) {
	ctx := context.Background()
	repo, run := claimRepo(t)
	weight := treeWeight(t, repo, run)
	b := git.DefaultTreeBudget()
	limits := git.CheckoutLimits{Budget: &b, InflightMaxBytes: weight / 2}
	tmp := isolateTempDir(t)

	// The 10 s context only keeps a refusal that waits from hanging the
	// test; the refusal must come at once.
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	begun := time.Now()
	s, err := git.EnterBranchWithLimits(wctx, repo, run, nil, limits)
	if err == nil {
		s.Cleanup()
		t.Fatal("a tree over the ceiling was admitted")
	}
	tl := requireTreeTooLarge(t, err, git.TreeLimitInflightBytes)
	if tl.Seen != weight || tl.Max != weight/2 || !strings.Contains(err.Error(), "InflightMaxBytes") {
		t.Errorf("refusal = %+v (%v), want Seen %d, Max %d, naming InflightMaxBytes", tl, err, weight, weight/2)
	}
	if d := time.Since(begun); d > 5*time.Second {
		t.Errorf("refusal took %s: it must not wait", d)
	}
	assertEmptyDir(t, tmp)
	if h := git.InflightHeldForTest(); h != 0 {
		t.Errorf("gate holds %d, want 0", h)
	}

	tbGit(t, repo, nil, "branch", "small", "main")
	ok, err := git.EnterBranchWithLimits(ctx, repo, "small", nil, limits)
	if err != nil {
		t.Fatalf("a tree within the ceiling: %v", err)
	}
	ok.Cleanup()
}

// TestEnterBranch_InflightWeightIsReturnedOnEveryExit: the weight is held
// exactly while a scope is (Cleanup returns it once, however often it is
// called), every failure after the admission returns it — git's refusal
// of a branch checked out in the main worktree (the case the in-process
// claim cannot see) and a temporary directory that cannot be created —
// and the claim with it; zero limits never touch the gate (AC 4).
func TestEnterBranch_InflightWeightIsReturnedOnEveryExit(t *testing.T) {
	ctx := context.Background()
	t.Cleanup(git.ResetDefaultCheckoutLimitsForTest)
	repo, run := claimRepo(t)
	weight := treeWeight(t, repo, run)
	tmp := isolateTempDir(t)

	s, err := git.EnterBranch(ctx, repo, run, nil)
	if err != nil {
		t.Fatal(err)
	}
	if h := git.InflightHeldForTest(); h != 0 {
		t.Errorf("zero limits: gate holds %d, want 0", h)
	}
	s.Cleanup()

	git.SetDefaultCheckoutLimits(git.CheckoutLimits{InflightMaxBytes: 4 * weight})
	s, err = git.EnterBranch(ctx, repo, run, nil)
	if err != nil {
		t.Fatal(err)
	}
	if h := git.InflightHeldForTest(); h != weight {
		t.Errorf("process default ceiling: gate holds %d while the scope is open, want %d", h, weight)
	}
	s.Cleanup()
	s.Cleanup()
	if h := git.InflightHeldForTest(); h != 0 {
		t.Errorf("after Cleanup (twice): gate holds %d, want 0", h)
	}

	tip := tbGit(t, repo, nil, "rev-parse", run)
	tbGit(t, repo, nil, "branch", "here", tip)
	tbGit(t, repo, nil, "switch", "-q", "here")
	if s, err := git.EnterBranch(ctx, repo, "here", nil); err == nil || errors.Is(err, git.ErrBranchInUse) {
		if s != nil {
			s.Cleanup()
		}
		t.Fatalf("branch checked out in the main worktree: err = %v, want git's refusal", err)
	}
	if h := git.InflightHeldForTest(); h != 0 {
		t.Errorf("after git's refusal: gate holds %d, want 0", h)
	}
	assertEmptyDir(t, tmp)
	tbGit(t, repo, nil, "switch", "-q", "main")

	t.Setenv("TMPDIR", filepath.Join(tmp, "missing"))
	if _, err := git.EnterBranch(ctx, repo, run, nil); err == nil {
		t.Fatal("EnterBranch with no temporary directory succeeded")
	}
	if h := git.InflightHeldForTest(); h != 0 {
		t.Errorf("after a failed temporary directory: gate holds %d, want 0", h)
	}
	t.Setenv("TMPDIR", tmp)
	s, err = git.EnterBranch(ctx, repo, run, nil)
	if err != nil {
		t.Fatalf("the failed entry kept the claim or the weight: %v", err)
	}
	s.Cleanup()
	assertEmptyDir(t, tmp)
}

// --- the gate ---

// TestInflightGate_FIFOAndCancellation: admission is first come, first
// served — a small weight that would fit waits behind a large one at the
// head, so the large one is never starved — and a waiter whose context
// ends leaves the queue with the retryable refusal, letting the next one
// in. A context that is already done never acquires, even with room.
func TestInflightGate_FIFOAndCancellation(t *testing.T) {
	// The gate is process-wide: t.Context() ends every wait this test
	// leaves behind, and each admitted weight is released at Cleanup
	// (releases are idempotent), so a failure here cannot block the gate
	// for the tests after it.
	ctx := t.Context()
	const ceiling = 100
	release60, err := git.AdmitCheckoutForTest(ctx, 60, ceiling)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release60)

	type admitted struct {
		release func()
		err     error
	}
	admit := func(ctx context.Context, w int64) chan admitted {
		ch := make(chan admitted, 1)
		go func() {
			r, err := git.AdmitCheckoutForTest(ctx, w, ceiling)
			if r != nil {
				t.Cleanup(r)
			}
			ch <- admitted{r, err}
		}()
		return ch
	}
	bigCtx, cancelBig := context.WithCancel(ctx)
	big := admit(bigCtx, 80)
	waitUntil(t, "the large weight to queue", func() bool { return git.InflightWaitersForTest() == 1 })
	small := admit(ctx, 10)
	waitUntil(t, "the small weight to queue", func() bool { return git.InflightWaitersForTest() == 2 })
	time.Sleep(50 * time.Millisecond)
	if h, w := git.InflightHeldForTest(), git.InflightWaitersForTest(); h != 60 || w != 2 {
		t.Fatalf("held %d with %d waiters; the small weight must not pass the large one at the head", h, w)
	}

	cancelBig()
	r := <-big
	requireInflightWait(t, r.err, 80, ceiling, context.Canceled)
	var s admitted
	select {
	case s = <-small:
	case <-time.After(10 * time.Second):
		t.Fatal("removing the cancelled head did not admit the next waiter")
	}
	if s.err != nil {
		t.Fatal(s.err)
	}
	if h := git.InflightHeldForTest(); h != 70 {
		t.Errorf("held %d, want 70", h)
	}

	done, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := git.AdmitCheckoutForTest(done, 1, ceiling); !errors.Is(err, git.ErrCheckoutsInFlight) {
		t.Errorf("a done context with room: err = %v, want the refusal", err)
	}
	short, cancelShort := context.WithTimeout(ctx, 5*time.Second)
	defer cancelShort()
	if _, err := git.AdmitCheckoutForTest(short, ceiling+1, ceiling); !errors.Is(err, git.ErrTreeTooLarge) {
		t.Errorf("a weight over the ceiling: err = %v, want ErrTreeTooLarge", err)
	}

	// Releases admit every waiter that fits, in order (w2 alone would fit
	// now; it queues behind w1).
	w1 := admit(ctx, 40)
	waitUntil(t, "the first waiter", func() bool { return git.InflightWaitersForTest() == 1 })
	w2 := admit(ctx, 20)
	waitUntil(t, "two waiters", func() bool { return git.InflightWaitersForTest() == 2 })
	release60()
	release60() // idempotent
	for _, ch := range []chan admitted{w1, w2} {
		a := <-ch
		if a.err != nil {
			t.Fatal(a.err)
		}
		a.release()
	}
	s.release()
	if h, w := git.InflightHeldForTest(), git.InflightWaitersForTest(); h != 0 || w != 0 {
		t.Errorf("held %d with %d waiters at the end, want an empty gate", h, w)
	}
}

// TestCheckoutLimits_InflightCeiling: the library default has no
// ceiling; a ceiling set alone brings the default budget (its weight is
// the measure); a negative one is none; EnterBranchWithLimits' own
// ceiling replaces the process default and a zero one keeps it.
func TestCheckoutLimits_InflightCeiling(t *testing.T) {
	t.Cleanup(git.ResetDefaultCheckoutLimitsForTest)
	if l := git.DefaultCheckoutLimits(); l.InflightMaxBytes != 0 || l.Budget != nil {
		t.Fatalf("library default = %+v, want none", l)
	}
	git.SetDefaultCheckoutLimits(git.CheckoutLimits{InflightMaxBytes: 1 << 20})
	for name, l := range map[string]git.CheckoutLimits{
		"DefaultCheckoutLimits":    git.DefaultCheckoutLimits(),
		"CLIClient.CheckoutLimits": git.NewCLIClient(t.TempDir()).CheckoutLimits(),
	} {
		if l.InflightMaxBytes != 1<<20 || l.Budget == nil || *l.Budget != git.DefaultTreeBudget() {
			t.Errorf("%s = %+v %+v, want the ceiling and the default budget", name, l, l.Budget)
		}
	}
	git.SetDefaultCheckoutLimits(git.CheckoutLimits{InflightMaxBytes: -5})
	if l := git.DefaultCheckoutLimits(); l.InflightMaxBytes != 0 || l.Budget != nil {
		t.Errorf("negative ceiling = %+v, want none", l)
	}

	ctx := context.Background()
	repo, run := claimRepo(t)
	weight := treeWeight(t, repo, run)
	isolateTempDir(t)
	git.SetDefaultCheckoutLimits(git.CheckoutLimits{InflightMaxBytes: weight / 2})
	if _, err := git.EnterBranchWithLimits(ctx, repo, run, nil, git.CheckoutLimits{}); !errors.Is(err, git.ErrTreeTooLarge) {
		t.Errorf("zero limits over a default ceiling: err = %v, want the default's refusal", err)
	}
	s, err := git.EnterBranchWithLimits(ctx, repo, run, nil, git.CheckoutLimits{InflightMaxBytes: 2 * weight})
	if err != nil {
		t.Fatalf("own ceiling over the default: %v", err)
	}
	s.Cleanup()
}
