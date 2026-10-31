package git_test

// INIT-024 TASK-011 (the TASK-010 cross-task review's X-1): one write
// scope per branch. Concurrent EnterBranch calls for one branch each
// measured, created a temporary parent and wrote the whole tree before
// git refused all but one of them, so refused writes put the tree on the
// shared temporary disk too, and git's own guard raced (several worktrees
// could attach to one branch). These tests use only API that existed
// before the task (plus ErrBranchInUse), so they run at the base commit,
// where they fail.

import (
	"context"
	"errors"
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

// installGitCallLog puts a wrapper first on PATH that appends "<arg1>
// <arg2>" of every git process to a log, one short write per process (so
// concurrent processes never interleave a line), then execs the real git.
// It returns the lines logged since the last reset, and the reset. Like
// t.Setenv the test must not be parallel.
func installGitCallLog(t *testing.T) (calls func() []string, reset func()) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git not on PATH: %v", err)
	}
	if realGit, err = filepath.Abs(realGit); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\nprintf '%s %s\\n' \"$1\" \"$2\" >> \"$SPINE_TEST_GITCALL_LOG\"\nexec \"$SPINE_TEST_GITCALL_REAL\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil { //nolint:gosec // G306: the wrapper must be executable
		t.Fatal(err)
	}
	t.Setenv("SPINE_TEST_GITCALL_REAL", realGit)
	t.Setenv("SPINE_TEST_GITCALL_LOG", log)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	calls = func() []string {
		data, err := os.ReadFile(log)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		var out []string
		for _, l := range strings.Split(string(data), "\n") {
			if l != "" {
				out = append(out, l)
			}
		}
		return out
	}
	reset = func() {
		if err := os.WriteFile(log, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return calls, reset
}

func countCalls(calls []string, want string) int {
	n := 0
	for _, c := range calls {
		if c == want {
			n++
		}
	}
	return n
}

// peakWatch samples the regular-file bytes under a directory every
// millisecond and keeps the largest sum it saw.
type peakWatch struct {
	peak atomic.Int64
	stop chan struct{}
	done chan struct{}
}

func watchPeak(dir string) *peakWatch {
	w := &peakWatch{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(w.done)
		for {
			if n, _ := duBytes(dir); n > w.peak.Load() {
				w.peak.Store(n)
			}
			select {
			case <-w.stop:
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()
	return w
}

// Stop ends the sampling (after one last sample) and returns the peak.
func (w *peakWatch) Stop() int64 {
	close(w.stop)
	<-w.done
	return w.peak.Load()
}

// worktreeParents lists the spine-wt-parent-* directories under dir.
func worktreeParents(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	for _, n := range dirEntries(t, dir) {
		if strings.HasPrefix(n, "spine-wt-parent-") {
			out = append(out, n)
		}
	}
	return out
}

// claimRepo is a budget-ready repository whose run branch holds a tree of
// 40 entries naming one 256 KiB blob: 10 MiB on disk per checkout, well
// within the default budget.
func claimRepo(t *testing.T) (repo, run string) {
	t.Helper()
	repo = tbNeutralRepo(t)
	testutil.WriteFile(t, repo, "README.md", "hello\n")
	tbGit(t, repo, nil, "add", "README.md")
	tbGit(t, repo, nil, "commit", "-q", "-m", "readme")
	base := tbGit(t, repo, nil, "rev-parse", "main")
	run = "spine/run/task-x-1a2b3c4d"
	tbGit(t, repo, nil, "update-ref", "refs/heads/"+run, tbBomb(t, repo, base, 40, 256<<10))
	return repo, run
}

// claimModes are the limits the claim tests run under: the library
// default (no limits; the claim applies regardless) and what `spine serve`
// installs from TASK-003 (the default budget and mutate deadline).
func claimModes() []struct {
	name   string
	limits git.CheckoutLimits
} {
	b := git.DefaultTreeBudget()
	return []struct {
		name   string
		limits git.CheckoutLimits
	}{
		{"library default", git.CheckoutLimits{}},
		{"serve budget", git.CheckoutLimits{Budget: &b, MutateTimeout: git.DefaultMutateTimeout}},
	}
}

// TestEnterBranch_SameBranchEntriesAreRefusedBeforeWriting is AC 1 (a
// held scope): while one write scope holds the run branch, concurrent
// entries for it are refused with ErrBranchInUse before any git process
// runs — no lookup, no measure, no spine-wt-parent-* directory, no
// worktree add — so the bytes under TMPDIR never exceed the held tree.
// Once the scope's Cleanup ran, the branch can be entered again.
// At 854a1e08 each entry wrote the tree before git refused it.
func TestEnterBranch_SameBranchEntriesAreRefusedBeforeWriting(t *testing.T) {
	ctx := context.Background()
	for _, mode := range claimModes() {
		t.Run(mode.name, func(t *testing.T) {
			repo, run := claimRepo(t)
			tmp := isolateTempDir(t)
			held, err := git.EnterBranchWithLimits(ctx, repo, run, nil, mode.limits)
			if err != nil {
				t.Fatalf("first EnterBranch: %v", err)
			}
			heldBytes, _ := duBytes(tmp)

			calls, _ := installGitCallLog(t)
			watch := watchPeak(tmp)
			const others = 4
			errs := make([]error, others)
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i := range others {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					s, err := git.EnterBranchWithLimits(ctx, repo, run, nil, mode.limits)
					if err == nil {
						s.Cleanup()
					}
					errs[i] = err
				}()
			}
			close(start)
			wg.Wait()
			peak := watch.Stop()

			for i, err := range errs {
				if !errors.Is(err, git.ErrBranchInUse) {
					t.Errorf("entry %d: err = %v, want ErrBranchInUse", i, err)
				}
			}
			if got := calls(); len(got) != 0 {
				t.Errorf("refused entries ran %d git processes, want none: %q", len(got), got)
			}
			if peak > heldBytes {
				t.Errorf("peak bytes under TMPDIR %d > the held scope's %d (%.1f trees): refused entries wrote", peak, heldBytes, float64(peak)/float64(heldBytes))
			}
			if p := worktreeParents(t, tmp); len(p) != 1 {
				t.Errorf("worktree parents = %v, want only the held scope's", p)
			}

			held.Cleanup()
			again, err := git.EnterBranchWithLimits(ctx, repo, run, nil, mode.limits)
			if err != nil {
				t.Fatalf("EnterBranch after the holder's Cleanup: %v", err)
			}
			again.Cleanup()
			assertEmptyDir(t, tmp)
			if n := worktreeCount(t, repo); n != 1 {
				t.Errorf("worktree list has %d entries, want 1", n)
			}
		})
	}
}

// TestEnterBranch_RacingSameBranchEntriesWriteOneTree is AC 1 (racing
// entries, the P4 next to X-1): entries for one branch started together,
// each keeping its scope until all have returned, admit exactly one
// writer; the others are refused with ErrBranchInUse without a worktree
// add, and the peak under TMPDIR is one tree. At 854a1e08 every entry
// added a worktree and wrote the tree, and git's guard sometimes let two
// attach to the branch.
func TestEnterBranch_RacingSameBranchEntriesWriteOneTree(t *testing.T) {
	ctx := context.Background()
	for _, mode := range claimModes() {
		t.Run(mode.name, func(t *testing.T) {
			repo, run := claimRepo(t)
			tmp := isolateTempDir(t)
			solo, err := git.EnterBranchWithLimits(ctx, repo, run, nil, mode.limits)
			if err != nil {
				t.Fatal(err)
			}
			oneTree, _ := duBytes(tmp)
			solo.Cleanup()

			calls, _ := installGitCallLog(t)
			watch := watchPeak(tmp)
			const writers = 4
			scopes := make([]*git.WriteScope, writers)
			errs := make([]error, writers)
			start, done := make(chan struct{}), make(chan struct{})
			var entered, finished sync.WaitGroup
			for i := range writers {
				entered.Add(1)
				finished.Add(1)
				go func() {
					defer finished.Done()
					<-start
					scopes[i], errs[i] = git.EnterBranchWithLimits(ctx, repo, run, nil, mode.limits)
					entered.Done()
					<-done
					if scopes[i] != nil {
						scopes[i].Cleanup()
					}
				}()
			}
			close(start)
			entered.Wait()
			peak := watch.Stop()
			adds := countCalls(calls(), "worktree add")
			close(done)
			finished.Wait()

			ok := 0
			for i, err := range errs {
				switch {
				case err == nil:
					ok++
				case !errors.Is(err, git.ErrBranchInUse):
					t.Errorf("entry %d: err = %v, want ErrBranchInUse", i, err)
				}
			}
			if ok != 1 {
				t.Errorf("%d entries hold the branch at once, want exactly 1", ok)
			}
			if adds != 1 {
				t.Errorf("%d worktree adds for %d writers, want 1 (only the admitted writer writes)", adds, writers)
			}
			if peak > oneTree {
				t.Errorf("peak bytes under TMPDIR %d > one tree %d (%.1f trees)", peak, oneTree, float64(peak)/float64(oneTree))
			}
			assertEmptyDir(t, tmp)
			if n := worktreeCount(t, repo); n != 1 {
				t.Errorf("worktree list has %d entries, want 1", n)
			}
		})
	}
}
