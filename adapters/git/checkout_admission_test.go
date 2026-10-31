package git_test

// INIT-024 TASK-011: the claim's key and error class, the in-flight
// checkout ceiling (CheckoutLimits.InflightMaxBytes) through EnterBranch,
// and the process gate behind it. New API: these tests have no base-commit
// counterpart (the regressions are checkout_claim_regression_test.go).

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/testutil"
)

// treeWeight is what EnterBranch weighs branch's tree at:
// git.InflightWeight of its measure (bytes plus 4 KiB per entry).
func treeWeight(t *testing.T, repo, branch string) int64 {
	t.Helper()
	st, err := git.MeasureTree(context.Background(), git.NewCLIClient(repo), branch)
	if err != nil {
		t.Fatal(err)
	}
	if w := st.TotalBytes + st.Entries*git.InflightEntryWeight; git.InflightWeight(st) != w {
		t.Fatalf("InflightWeight(%+v) = %d, want %d", st, git.InflightWeight(st), w)
	}
	return git.InflightWeight(st)
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
	sub := filepath.Join(repoA, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	dotgitLink := t.TempDir()
	if err := os.Symlink(filepath.Join(repoA, ".git"), filepath.Join(dotgitLink, ".git")); err != nil {
		t.Fatal(err)
	}
	spellings := map[string]string{"symbolic link": link, "linked worktree": lw, "same path": repoA,
		"subdirectory": sub, "symlinked .git": dotgitLink}
	// A case variant of the path, where the storage folds case (macOS's
	// default APFS): the same directory, the same key (review round 1).
	if v := caseVariant(repoA); v != "" {
		spellings["case variant"] = v
	}
	for name, dir := range spellings {
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

// TestRepoClaimKey_MatchesGitCommonDir: the in-process key is the identity
// of the directory `git rev-parse --git-common-dir` names, for every
// layout and spelling EnterBranch is given (a subdirectory is found
// upward, as git does); a directory in no repository keys by itself.
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
	sub := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	dotgitLink := t.TempDir()
	if err := os.Symlink(filepath.Join(repo, ".git"), filepath.Join(dotgitLink, ".git")); err != nil {
		t.Fatal(err)
	}

	layouts := map[string]string{"main tree": repo, "symbolic link": link, "linked worktree": lw, "relative gitdir": rel,
		"bare": bare, "subdirectory": sub, "symlinked .git": dotgitLink}
	if v := caseVariant(repo); v != "" {
		layouts["case variant"] = v
	} else {
		t.Log("case-sensitive storage: the case-variant spelling is not exercised here")
	}
	for name, dir := range layouts {
		want := git.PathIdentityForTest(gitAbs(t, dir, tbGit(t, dir, nil, "rev-parse", "--git-common-dir")))
		if got := git.RepoClaimKeyForTest(dir); got != want {
			t.Errorf("%s: key %q, want the identity of git's common dir %q", name, got, want)
		}
	}
	plain := t.TempDir()
	if got := git.RepoClaimKeyForTest(plain); got != git.PathIdentityForTest(plain) {
		t.Errorf("not a repository: key %q, want the directory's identity", got)
	}
}

// caseVariant returns repo's path with one component's case flipped when
// the storage resolves it to the same directory (case-insensitive), or "".
func caseVariant(repo string) string {
	dir := filepath.Dir(repo)
	base := filepath.Base(dir)
	flipped := strings.ToUpper(base)
	if flipped == base {
		flipped = strings.ToLower(base)
	}
	if flipped == base {
		return ""
	}
	v := filepath.Join(filepath.Dir(dir), flipped, filepath.Base(repo))
	a, errA := os.Stat(repo)
	b, errB := os.Stat(v)
	if errA != nil || errB != nil || !os.SameFile(a, b) {
		return ""
	}
	return v
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
// ceiling of 2.5 trees, six concurrent entries on the run branches of six
// repositories never hold more than the ceiling in weight (at most two
// scopes at once, and the bytes under TMPDIR never above the ceiling);
// trees that fit together proceed together, every entry is admitted in
// the end, and the gate is empty afterwards.
func TestEnterBranch_InflightCeilingBoundsDifferentBranches(t *testing.T) {
	ctx := context.Background()
	const branches = 6
	repos := make([]string, branches)
	var run string
	for i := range branches {
		repos[i], run = claimRepo(t)
	}
	weight := treeWeight(t, repos[0], run)
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
			s, err := git.EnterBranchWithLimits(ctx, repos[i], run, nil, limits)
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
			t.Errorf("repository %d: %v (every entry fits on its own and must be admitted in the end)", i, err)
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
	repoA, run := claimRepo(t)
	repo, _ := claimRepo(t)
	tbGit(t, repo, nil, "branch", "b1", run)
	weight := treeWeight(t, repo, run)
	ceiling := weight + weight/2
	b := git.DefaultTreeBudget()
	limits := git.CheckoutLimits{Budget: &b, InflightMaxBytes: ceiling}
	tmp := isolateTempDir(t)

	// Another repository's tree leaves less than one tree of room.
	held, err := git.EnterBranchWithLimits(ctx, repoA, run, nil, limits)
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
	if n := worktreeCount(t, repo); n != 1 {
		t.Errorf("worktree list has %d entries, want 1 (nothing added for the refused entries)", n)
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

type gateAdmission struct {
	release func()
	err     error
}

// admitAsync asks the gate for weight on behalf of repo in a goroutine.
// The gate is process-wide: t.Context() (pass a context derived from it)
// ends every wait a failed test leaves behind, and an admitted weight is
// released at Cleanup (releases are idempotent).
func admitAsync(t *testing.T, ctx context.Context, repo string, weight, ceiling int64) chan gateAdmission {
	t.Helper()
	ch := make(chan gateAdmission, 1)
	go func() {
		r, err := git.AdmitCheckoutForTest(ctx, repo, weight, ceiling)
		if r != nil {
			t.Cleanup(r)
		}
		ch <- gateAdmission{r, err}
	}()
	return ch
}

func admitNow(t *testing.T, ctx context.Context, repo string, weight, ceiling int64) func() {
	t.Helper()
	r, err := git.AdmitCheckoutForTest(ctx, repo, weight, ceiling)
	if err != nil {
		t.Fatalf("admit %s %d/%d: %v", repo, weight, ceiling, err)
	}
	t.Cleanup(r)
	return r
}

func receive(t *testing.T, what string, ch chan gateAdmission) gateAdmission {
	t.Helper()
	select {
	case a := <-ch:
		return a
	case <-time.After(10 * time.Second):
		t.Fatalf("%s: not admitted or refused within 10 s", what)
	}
	return gateAdmission{}
}

func requireEmptyGate(t *testing.T) {
	t.Helper()
	if h, w, r := git.InflightHeldForTest(), git.InflightWaitersForTest(), git.InflightReposForTest(); h != 0 || w != 0 || r != 0 {
		t.Errorf("gate holds %d with %d waiters over %d repositories at the end, want an empty gate", h, w, r)
	}
}

// TestInflightGate_FIFOWithinARepositoryAndCancellation: one repository's
// entries are served in arrival order — a small weight that would fit
// waits behind a large one at the head of its repository's queue, so the
// large one is never starved by its own repository — and a waiter whose
// context ends leaves the queue with the retryable refusal, letting the
// next one in. A context that is already done never acquires, even with
// room; a weight over the ceiling is refused at once.
func TestInflightGate_FIFOWithinARepositoryAndCancellation(t *testing.T) {
	ctx := t.Context()
	const ceiling = 200 // half: 100 per repository
	release60 := admitNow(t, ctx, "r1", 60, ceiling)

	bigCtx, cancelBig := context.WithCancel(ctx)
	big := admitAsync(t, bigCtx, "r1", 80, ceiling) // 140 > 100: waits for r1's share
	waitUntil(t, "the large weight to queue", func() bool { return git.InflightWaitersForTest() == 1 })
	small := admitAsync(t, ctx, "r1", 10, ceiling) // would fit (70), queues behind it
	waitUntil(t, "the small weight to queue", func() bool { return git.InflightWaitersForTest() == 2 })
	time.Sleep(50 * time.Millisecond)
	if h, w := git.InflightHeldForTest(), git.InflightWaitersForTest(); h != 60 || w != 2 {
		t.Fatalf("held %d with %d waiters; the small weight must not pass its repository's head", h, w)
	}

	cancelBig()
	requireInflightWait(t, receive(t, "cancelled head", big).err, 80, ceiling, context.Canceled)
	sm := receive(t, "the waiter behind the cancelled head", small)
	if sm.err != nil {
		t.Fatal(sm.err)
	}
	if h := git.InflightHeldForTest(); h != 70 {
		t.Errorf("held %d, want 70", h)
	}

	done, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := git.AdmitCheckoutForTest(done, "r2", 1, ceiling); !errors.Is(err, git.ErrCheckoutsInFlight) {
		t.Errorf("a done context with room: err = %v, want the refusal", err)
	}
	short, cancelShort := context.WithTimeout(ctx, 5*time.Second)
	defer cancelShort()
	if _, err := git.AdmitCheckoutForTest(short, "r2", ceiling+1, ceiling); !errors.Is(err, git.ErrTreeTooLarge) {
		t.Errorf("a weight over the ceiling: err = %v, want ErrTreeTooLarge", err)
	}

	release60()
	release60() // idempotent
	sm.release()
	requireEmptyGate(t)
}

// TestInflightGate_HalfTheCeilingPerRepository (review round 1, P3-1): a
// repository holds at most half the ceiling — its next weight waits even
// with room under the ceiling, while another repository's is admitted —
// and a single weight over half is admitted only while its repository
// holds nothing.
func TestInflightGate_HalfTheCeilingPerRepository(t *testing.T) {
	ctx := t.Context()
	const ceiling = 100
	relA := admitNow(t, ctx, "a", 40, ceiling)
	a2 := admitAsync(t, ctx, "a", 20, ceiling) // 60 > 50: waits although 60 <= 100
	waitUntil(t, "a's second weight to queue", func() bool { return git.InflightWaitersForTest() == 1 })
	relB := admitNow(t, ctx, "b", 50, ceiling) // b's half, beside a's 40
	if h := git.InflightHeldForTest(); h != 90 {
		t.Fatalf("held %d, want 90", h)
	}
	relB()
	time.Sleep(20 * time.Millisecond)
	if w := git.InflightWaitersForTest(); w != 1 {
		t.Fatalf("a's second weight was admitted over a's half (waiters %d)", w)
	}
	relA() // a holds nothing: its 20 fits its half
	if r := receive(t, "a's second weight", a2); r.err != nil {
		t.Fatal(r.err)
	} else {
		r.release()
	}

	// Over half on its own: only while the repository holds nothing.
	relSmall := admitNow(t, ctx, "c", 10, ceiling)
	big := admitAsync(t, ctx, "c", 70, ceiling)
	waitUntil(t, "c's 70 to queue", func() bool { return git.InflightWaitersForTest() == 1 })
	relSmall()
	if r := receive(t, "c's 70 once c holds nothing", big); r.err != nil {
		t.Fatal(r.err)
	} else {
		r.release()
	}
	requireEmptyGate(t)
}

// TestInflightGate_RoundRobinAcrossRepositories (review round 1, P3-1):
// when room frees, the waiting repositories are served in turn — the
// head of each before the second of any — so a repository with many
// waiters cannot take the room another repository is waiting for.
func TestInflightGate_RoundRobinAcrossRepositories(t *testing.T) {
	ctx := t.Context()
	const ceiling = 100
	relX := admitNow(t, ctx, "x", 100, ceiling)
	b1 := admitAsync(t, ctx, "b", 30, ceiling)
	waitUntil(t, "b1", func() bool { return git.InflightWaitersForTest() == 1 })
	b2 := admitAsync(t, ctx, "b", 20, ceiling)
	waitUntil(t, "b2", func() bool { return git.InflightWaitersForTest() == 2 })
	c1 := admitAsync(t, ctx, "c", 60, ceiling)
	waitUntil(t, "c1", func() bool { return git.InflightWaitersForTest() == 3 })

	relX()
	// Round-robin: b1 (30), then c1 (60) = 90; b2 (20) no longer fits.
	// Draining b first would have admitted b1 and b2 (50) and left c1 out.
	var releases []func()
	for name, ch := range map[string]chan gateAdmission{"b1": b1, "c1": c1} {
		r := receive(t, name, ch)
		if r.err != nil {
			t.Fatalf("%s: %v", name, r.err)
		}
		releases = append(releases, r.release)
	}
	defer func() {
		for _, r := range releases {
			r()
		}
	}()
	time.Sleep(20 * time.Millisecond)
	if h, w := git.InflightHeldForTest(), git.InflightWaitersForTest(); h != 90 || w != 1 {
		t.Fatalf("held %d with %d waiters, want 90 and b2 waiting", h, w)
	}
	select {
	case r := <-b2:
		t.Fatalf("b2 admitted (%v) beyond the room c1 was given", r.err)
	default:
	}
	t.Cleanup(requireEmptyGateLater(t, b2))
}

// requireEmptyGateLater returns a Cleanup that drains a pending admission
// and checks the gate empties.
func requireEmptyGateLater(t *testing.T, pending chan gateAdmission) func() {
	return func() {
		select {
		case r := <-pending:
			if r.release != nil {
				r.release()
			}
		case <-time.After(10 * time.Second):
		}
	}
}

// TestInflightGate_ForgetsIdleRepositories: a repository that holds and
// waits for nothing is dropped, so the gate's state is bounded by the
// repositories in flight.
func TestInflightGate_ForgetsIdleRepositories(t *testing.T) {
	ctx := t.Context()
	for i := range 50 {
		admitNow(t, ctx, fmt.Sprintf("idle-%d", i), 1, 100)()
	}
	cctx, cancel := context.WithCancel(ctx)
	relHold := admitNow(t, ctx, "holder", 100, 100)
	w := admitAsync(t, cctx, "cancelled", 10, 100)
	waitUntil(t, "a waiter", func() bool { return git.InflightWaitersForTest() == 1 })
	cancel()
	if r := receive(t, "cancelled waiter", w); r.err == nil {
		t.Fatal("a cancelled waiter was admitted")
	}
	relHold()
	requireEmptyGate(t)
}

// TestInflightWeight_CountsEntries (review round 1, P3-2): the weight is
// the bytes plus InflightEntryWeight per entry, saturating; the heaviest
// tree a budget admits weighs its byte limit plus its entry allowance.
func TestInflightWeight_CountsEntries(t *testing.T) {
	if git.InflightEntryWeight != 4096 {
		t.Fatalf("InflightEntryWeight = %d, want 4096", git.InflightEntryWeight)
	}
	for _, tc := range []struct {
		st   git.TreeStats
		want int64
	}{
		{git.TreeStats{}, 0},
		{git.TreeStats{TotalBytes: 10, Entries: 3, Blobs: 2, Trees: 1}, 10 + 3*4096},
		{git.TreeStats{Entries: 40000, Blobs: 20000, Trees: 20000}, 40000 * 4096},
		{git.TreeStats{TotalBytes: math.MaxInt64 - 1, Entries: 2}, math.MaxInt64},
		{git.TreeStats{TotalBytes: 1, Entries: math.MaxInt64}, math.MaxInt64},
	} {
		if got := git.InflightWeight(tc.st); got != tc.want {
			t.Errorf("InflightWeight(%+v) = %d, want %d", tc.st, got, tc.want)
		}
	}
	if got, want := git.DefaultTreeBudget().MaxInflightWeight(), int64(git.DefaultTreeMaxTotalBytes)+int64(git.DefaultTreeMaxEntries)*4096; got != want {
		t.Errorf("default MaxInflightWeight = %d, want %d", got, want)
	}
	if got := (git.TreeBudget{MaxTotalBytes: 100, MaxEntries: 2}).MaxInflightWeight(); got != 100+2*4096 {
		t.Errorf("MaxInflightWeight = %d", got)
	}
}

// TestCheckoutLimits_InflightCeiling (review round 1, P4-4): the library
// default has no ceiling; a ceiling set alone stays alone in the process
// default and in every client's limits (no implied budget: a client's
// Checkout of a repository without the neutral attributes line is not
// refused) — EnterBranch alone measures, against the default budget, to
// weigh its tree; a negative ceiling is none; EnterBranchWithLimits' own
// ceiling replaces the process default and a zero one keeps it.
func TestCheckoutLimits_InflightCeiling(t *testing.T) {
	t.Cleanup(git.ResetDefaultCheckoutLimitsForTest)
	ctx := context.Background()
	if l := git.DefaultCheckoutLimits(); l.InflightMaxBytes != 0 || l.Budget != nil {
		t.Fatalf("library default = %+v, want none", l)
	}
	git.SetDefaultCheckoutLimits(git.CheckoutLimits{InflightMaxBytes: 1 << 30})
	plain := testutil.NewTempRepo(t) // no neutral attributes line
	plainClient := git.NewCLIClient(plain)
	for name, l := range map[string]git.CheckoutLimits{
		"DefaultCheckoutLimits":    git.DefaultCheckoutLimits(),
		"CLIClient.CheckoutLimits": plainClient.CheckoutLimits(),
	} {
		if l.InflightMaxBytes != 1<<30 || l.Budget != nil {
			t.Errorf("%s = %+v %+v, want the ceiling and no budget", name, l, l.Budget)
		}
	}
	tbGit(t, plain, nil, "branch", "other", "main")
	if err := plainClient.Checkout(ctx, "other"); err != nil {
		t.Errorf("a client's Checkout under a ceiling-only default: %v (no budget is implied for it)", err)
	}

	repo, run := claimRepo(t)
	weight := treeWeight(t, repo, run)
	isolateTempDir(t)
	s, err := git.EnterBranch(ctx, repo, run, nil)
	if err != nil {
		t.Fatal(err)
	}
	if h := git.InflightHeldForTest(); h != weight {
		t.Errorf("EnterBranch under a ceiling-only default holds %d, want the measured weight %d", h, weight)
	}
	s.Cleanup()

	git.SetDefaultCheckoutLimits(git.CheckoutLimits{InflightMaxBytes: -5})
	if l := git.DefaultCheckoutLimits(); l.InflightMaxBytes != 0 || l.Budget != nil {
		t.Errorf("negative ceiling = %+v, want none", l)
	}

	git.SetDefaultCheckoutLimits(git.CheckoutLimits{InflightMaxBytes: weight / 2})
	if _, err := git.EnterBranchWithLimits(ctx, repo, run, nil, git.CheckoutLimits{}); !errors.Is(err, git.ErrTreeTooLarge) {
		t.Errorf("zero limits over a default ceiling: err = %v, want the default's refusal", err)
	}
	s, err = git.EnterBranchWithLimits(ctx, repo, run, nil, git.CheckoutLimits{InflightMaxBytes: 2 * weight})
	if err != nil {
		t.Fatalf("own ceiling over the default: %v", err)
	}
	s.Cleanup()
}

// TestEnterBranch_ScopeIsReleasedAfterItsWorktreeIsRemoved (review round
// 1, P4-1): whenever a branch scope returns its weight and its claim —
// its Cleanup, a failure after the worktree was added, a failure before
// anything was created — its private parent directory no longer exists
// and git no longer lists its worktree.
func TestEnterBranch_ScopeIsReleasedAfterItsWorktreeIsRemoved(t *testing.T) {
	ctx := context.Background()
	repo, run := claimRepo(t)
	weight := treeWeight(t, repo, run)
	tmp := isolateTempDir(t)
	b := git.DefaultTreeBudget()
	limits := git.CheckoutLimits{Budget: &b, InflightMaxBytes: 4 * weight}

	var mu sync.Mutex
	var releases int
	var violations []string
	restore := git.SetScopeReleaseHookForTest(func(repoDir, parent, worktreeDir string) {
		mu.Lock()
		defer mu.Unlock()
		releases++
		if parent != "" {
			if _, err := os.Lstat(parent); err == nil {
				violations = append(violations, "released while "+parent+" exists")
			}
			cmd := exec.Command("git", "worktree", "list", "--porcelain")
			cmd.Dir = repoDir
			out, err := cmd.Output()
			if err != nil {
				violations = append(violations, "git worktree list: "+err.Error())
			} else if strings.Contains(string(out), filepath.Base(parent)) {
				violations = append(violations, "released while git lists "+worktreeDir)
			}
		}
	})
	defer restore()

	s, err := git.EnterBranchWithLimits(ctx, repo, run, nil, limits)
	if err != nil {
		t.Fatal(err)
	}
	s.Cleanup()

	tip := tbGit(t, repo, nil, "rev-parse", run)
	tbGit(t, repo, nil, "branch", "here", tip)
	tbGit(t, repo, nil, "switch", "-q", "here")
	if s, err := git.EnterBranchWithLimits(ctx, repo, "here", nil, limits); err == nil {
		s.Cleanup()
		t.Fatal("a branch checked out in the main worktree was entered")
	}
	tbGit(t, repo, nil, "switch", "-q", "main")

	t.Setenv("TMPDIR", filepath.Join(tmp, "missing"))
	if _, err := git.EnterBranchWithLimits(ctx, repo, run, nil, limits); err == nil {
		t.Fatal("EnterBranch with no temporary directory succeeded")
	}
	t.Setenv("TMPDIR", tmp)

	mu.Lock()
	defer mu.Unlock()
	if releases != 3 {
		t.Errorf("%d releases observed, want 3", releases)
	}
	for _, v := range violations {
		t.Error(v)
	}
	if h := git.InflightHeldForTest(); h != 0 {
		t.Errorf("gate holds %d, want 0", h)
	}
}

// installBranchMover puts a git wrapper first on PATH that moves ref of
// repo when git runs "<cmd1> <cmd2>": to the next commit of commits on
// every such run (round-robin), before it execs the real git.
func installBranchMover(t *testing.T, repo, ref, cmd1, cmd2 string, commits ...string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git not on PATH: %v", err)
	}
	if realGit, err = filepath.Abs(realGit); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	state := filepath.Join(dir, "n")
	script := `#!/bin/sh
if [ "$1" = "$MOVER_CMD1" ] && [ "$2" = "$MOVER_CMD2" ]; then
  n=$(cat "$MOVER_STATE" 2>/dev/null || echo 0)
  i=0; target=""
  for c in $MOVER_COMMITS; do
    if [ $i -eq $((n % MOVER_COUNT)) ]; then target=$c; fi
    i=$((i + 1))
  done
  echo $((n + 1)) > "$MOVER_STATE"
  "$MOVER_REAL" -C "$MOVER_REPO" update-ref "$MOVER_REF" "$target"
fi
exec "$MOVER_REAL" "$@"
`
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil { //nolint:gosec // G306: the wrapper must be executable
		t.Fatal(err)
	}
	t.Setenv("MOVER_REAL", realGit)
	t.Setenv("MOVER_STATE", state)
	t.Setenv("MOVER_REPO", repo)
	t.Setenv("MOVER_REF", ref)
	t.Setenv("MOVER_CMD1", cmd1)
	t.Setenv("MOVER_CMD2", cmd2)
	t.Setenv("MOVER_COMMITS", strings.Join(commits, " "))
	t.Setenv("MOVER_COUNT", fmt.Sprint(len(commits)))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// requireBranchMoved fails unless err is the retryable moved-branch refusal.
func requireBranchMoved(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, git.ErrBranchMoved) || !errors.Is(err, git.ErrCheckoutsInFlight) {
		t.Fatalf("err = %v, want ErrBranchMoved (and ErrCheckoutsInFlight)", err)
	}
	var ge *git.GitError
	if !errors.As(err, &ge) || ge.Kind != git.ErrKindTransient || !ge.IsRetryable() || ge.Message != git.BranchMovedMessage {
		t.Fatalf("err = %#v, want a retryable *GitError %q", err, git.BranchMovedMessage)
	}
}

// TestEnterBranch_BranchMovedDuringCheckoutIsRefused (codex review round
// 3, P1): with a ceiling, a branch that moves after the tip was re-read —
// here while `git worktree add` runs, so `switch` checks out another tree
// than the one weighed — is refused with the retryable ErrBranchMoved and
// nothing is left: no directory, no worktree, no weight, no claim.
func TestEnterBranch_BranchMovedDuringCheckoutIsRefused(t *testing.T) {
	ctx := context.Background()
	repo, run := claimRepo(t)
	other := tbGit(t, repo, nil, "rev-parse", "main")
	weight := treeWeight(t, repo, run)
	b := git.DefaultTreeBudget()
	limits := git.CheckoutLimits{Budget: &b, InflightMaxBytes: 4 * weight}
	tmp := isolateTempDir(t)

	installBranchMover(t, repo, "refs/heads/"+run, "worktree", "add", other)
	_, err := git.EnterBranchWithLimits(ctx, repo, run, nil, limits)
	requireBranchMoved(t, err)
	assertEmptyDir(t, tmp)
	if n := worktreeCount(t, repo); n != 1 {
		t.Errorf("worktree list has %d entries, want 1", n)
	}
	if h := git.InflightHeldForTest(); h != 0 {
		t.Errorf("gate holds %d, want 0", h)
	}
	t.Setenv("MOVER_CMD1", "none")
	s, err := git.EnterBranchWithLimits(ctx, repo, run, nil, limits)
	if err != nil {
		t.Fatalf("the refused entry kept the claim: %v", err)
	}
	s.Cleanup()
}

// TestEnterBranch_BranchThatKeepsMovingIsRefused (codex review round 3,
// P1): a branch that moves again every time it is measured — so every
// re-read after the admission finds another tip — is re-weighed a bounded
// number of times and then refused with ErrBranchMoved, holding nothing.
// With no ceiling nothing is re-read: the entry proceeds as before the
// task.
func TestEnterBranch_BranchThatKeepsMovingIsRefused(t *testing.T) {
	ctx := context.Background()
	repo, run := claimRepo(t)
	a := tbGit(t, repo, nil, "rev-parse", run)
	bTip := tbGit(t, repo, nil, "rev-parse", "main")
	weight := treeWeight(t, repo, run)
	b := git.DefaultTreeBudget()
	limits := git.CheckoutLimits{Budget: &b, InflightMaxBytes: 4 * weight}
	tmp := isolateTempDir(t)

	installBranchMover(t, repo, "refs/heads/"+run, "ls-tree", "-r", bTip, a)
	_, err := git.EnterBranchWithLimits(ctx, repo, run, nil, limits)
	requireBranchMoved(t, err)
	assertEmptyDir(t, tmp)
	if h := git.InflightHeldForTest(); h != 0 {
		t.Errorf("gate holds %d, want 0", h)
	}

	s, err := git.EnterBranchWithLimits(ctx, repo, run, nil, git.CheckoutLimits{Budget: &b})
	if err != nil {
		t.Fatalf("no ceiling: %v", err)
	}
	s.Cleanup()
}
