package git_test

// INIT-024 TASK-011 review round 1: regressions for the in-flight gate.
//   - P3-1: one repository's waiter at the head of a single FIFO blocked
//     every other repository's write, even one that fit in the room left.
//   - P3-2: the weight was the blob bytes only, so trees of empty files
//     and directories weighed nothing and any number of them was admitted
//     at once.
// They fail at e92b45c4 (run with a compile shim that maps the repository
// argument of AdmitCheckoutForTest away and declares InflightEntryWeight).

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
)

// TestInflightGate_OneRepositoryNeverBlocksAnother (P3-1, the review's
// head-of-line probe): repository A holds 60 of a 100 ceiling and
// repository B waits for 60; repository C's 10 fits beside A and is
// admitted at once — B's waiter is not in C's way. When A releases, B is
// admitted.
func TestInflightGate_OneRepositoryNeverBlocksAnother(t *testing.T) {
	ctx := boundedCtx(t, 10*time.Second)
	const ceiling = 100
	relA, err := git.AdmitCheckoutForTest(ctx, "repo-a", 60, ceiling)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(relA)

	type admitted struct {
		release func()
		err     error
	}
	b := make(chan admitted, 1)
	go func() {
		r, err := git.AdmitCheckoutForTest(ctx, "repo-b", 60, ceiling)
		if r != nil {
			t.Cleanup(r)
		}
		b <- admitted{r, err}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for git.InflightWaitersForTest() != 1 {
		if time.Now().After(deadline) {
			t.Fatal("repository B never queued")
		}
		time.Sleep(time.Millisecond)
	}

	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	begun := time.Now()
	relC, err := git.AdmitCheckoutForTest(cctx, "repo-c", 10, ceiling)
	if err != nil {
		t.Fatalf("repository C's 10 behind repository B's waiting 60 (60 of 100 held): %v", err)
	}
	if d := time.Since(begun); d > time.Second {
		t.Errorf("repository C waited %s for room it had", d)
	}
	relC()

	relA()
	select {
	case r := <-b:
		if r.err != nil {
			t.Fatalf("repository B after A's release: %v", r.err)
		}
		r.release()
	case <-time.After(10 * time.Second):
		t.Fatal("repository B was not admitted after A's release")
	}
	if h, w := git.InflightHeldForTest(), git.InflightWaitersForTest(); h != 0 || w != 0 {
		t.Errorf("held %d with %d waiters at the end, want an empty gate", h, w)
	}
}

// entryTreeRepo is a budget-ready repository whose branch run holds a
// tree of n directories each with one empty file: 2n+1 entries, 0 bytes.
func entryTreeRepo(t *testing.T, n int) (repo, run string) {
	t.Helper()
	repo = tbNeutralRepo(t)
	base := tbGit(t, repo, nil, "rev-parse", "HEAD")
	empty := tbGit(t, repo, []byte{}, "hash-object", "-w", "--stdin")
	sub := tbGit(t, repo, []byte("100644 blob "+empty+"\tf\n"), "mktree")
	var mk strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&mk, "040000 tree %s\t%06d\n", sub, i)
	}
	d := tbGit(t, repo, []byte(mk.String()), "mktree")
	root := tbGit(t, repo, []byte("040000 tree "+d+"\td\n"), "mktree")
	run = "spine/run/entries-1a2b3c4d"
	tbGit(t, repo, nil, "update-ref", "refs/heads/"+run, tbGit(t, repo, nil, "commit-tree", root, "-p", base, "-m", "entries"))
	return repo, run
}

// entriesUnder counts the filesystem entries under root.
func entriesUnder(root string) int64 {
	var n int64
	_ = filepath.WalkDir(root, func(string, fs.DirEntry, error) error { n++; return nil })
	return n
}

// TestEnterBranch_EntriesAreWeighed (P3-2, the review's zero-weight
// probe): three repositories whose run branches hold a tree of 3,001
// entries and no bytes, under a ceiling of 2.5 such trees, never have more
// than two in flight (each tree weighs its entries); a ceiling below one
// such tree refuses it. At e92b45c4 every one weighed 0: all three were
// admitted together, and so was the tree over the ceiling.
func TestEnterBranch_EntriesAreWeighed(t *testing.T) {
	const repos, dirs = 3, 1500
	type fixture struct{ repo, run string }
	var fx []fixture
	for range repos {
		r, b := entryTreeRepo(t, dirs)
		fx = append(fx, fixture{r, b})
	}
	st, err := git.MeasureTree(t.Context(), git.NewCLIClient(fx[0].repo), fx[0].run)
	if err != nil {
		t.Fatal(err)
	}
	if st.TotalBytes != 0 || st.Entries != 2*dirs+1 {
		t.Fatalf("fixture = %+v, want %d entries and no bytes", st, 2*dirs+1)
	}
	weight := st.Entries * git.InflightEntryWeight
	ceiling := 2*weight + weight/2
	b := git.DefaultTreeBudget()
	limits := git.CheckoutLimits{Budget: &b, MutateTimeout: git.DefaultMutateTimeout, InflightMaxBytes: ceiling}
	tmp := isolateTempDir(t)
	// Two rounds of scopes held 300 ms each; 30 s ends a wait that never
	// would.
	ctx := boundedCtx(t, 30*time.Second)

	var inScope, maxInScope, peakEntries atomic.Int64
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			if n := entriesUnder(tmp); n > peakEntries.Load() {
				peakEntries.Store(n)
			}
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Millisecond):
			}
		}
	}()
	errs := make([]error, repos)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range repos {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			s, err := git.EnterBranchWithLimits(ctx, fx[i].repo, fx[i].run, nil, limits)
			if err != nil {
				errs[i] = err
				return
			}
			n := inScope.Add(1)
			for m := maxInScope.Load(); n > m && !maxInScope.CompareAndSwap(m, n); m = maxInScope.Load() {
			}
			time.Sleep(300 * time.Millisecond)
			inScope.Add(-1)
			s.Cleanup()
		}()
	}
	close(start)
	wg.Wait()
	close(stop)
	<-stopped

	for i, err := range errs {
		if err != nil {
			t.Errorf("repository %d: %v (each tree fits on its own and must be admitted in the end)", i, err)
		}
	}
	if m := maxInScope.Load(); m > 2 {
		t.Errorf("%d trees of %d entries in flight at once under a ceiling of 2.5 such trees, want at most 2", m, st.Entries)
	}
	// Two trees' entries, plus the parents and .git files.
	if p := peakEntries.Load(); p > 2*(st.Entries+16) {
		t.Errorf("peak filesystem entries under TMPDIR %d, want at most two trees' (%d each)", p, st.Entries)
	}

	small := git.CheckoutLimits{Budget: &b, InflightMaxBytes: weight / 2}
	s, err := git.EnterBranchWithLimits(boundedCtx(t, 10*time.Second), fx[0].repo, fx[0].run, nil, small)
	if err == nil {
		s.Cleanup()
		t.Fatal("a tree weighing twice the ceiling in entries alone was admitted")
	}
	if !errors.Is(err, git.ErrTreeTooLarge) {
		t.Errorf("tree over the ceiling by its entries: err = %v, want ErrTreeTooLarge", err)
	}
	assertEmptyDir(t, tmp)
}
