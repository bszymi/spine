package git

// INIT-023 TASK-012 AC 10: the shared working-tree lock (treelock.go).
// Run under -race too.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tlRun runs git in dir with a fixed identity and fails the test on error.
func tlRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.test",
		"-c", "commit.gpgsign=false"}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// tlRepo is a repository on main with a second branch "feature".
func tlRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	tlRun(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tlRun(t, dir, "add", "a.md")
	tlRun(t, dir, "commit", "-q", "-m", "init")
	tlRun(t, dir, "branch", "feature")
	return dir
}

// tlHold takes the tree through c in a goroutine and keeps it until the
// returned release is called.
func tlHold(t *testing.T, c *CLIClient) (release func()) {
	t.Helper()
	entered, done, exited := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(exited)
		_ = c.WithWorkingTree(context.Background(), func(context.Context) error {
			close(entered)
			<-done
			return nil
		})
	}()
	<-entered
	var released bool
	return func() {
		if !released {
			released = true
			close(done)
			<-exited
		}
	}
}

// tlBlocks reports whether call is still running after d.
func tlBlocks(call func() error, d time.Duration) (blocked bool, result chan error) {
	result = make(chan error, 1)
	go func() { result <- call() }()
	select {
	case err := <-result:
		result <- err
		return false, result
	case <-time.After(d):
		return true, result
	}
}

func tlRegistered(dir string) bool {
	treeMu.Lock()
	defer treeMu.Unlock()
	_, ok := treeLocks[canonicalTreeKey(dir)]
	return ok
}

// (a) A nested stateful call inside WithWorkingTree re-enters.
func TestTreeLock_NestedCallReenters(t *testing.T) {
	dir := tlRepo(t)
	c := NewCLIClient(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := c.WithWorkingTree(ctx, func(ctx context.Context) error {
		if err := c.Checkout(ctx, "feature"); err != nil {
			return err
		}
		return c.WithWorkingTree(ctx, func(ctx context.Context) error { return c.Checkout(ctx, "main") })
	})
	if err != nil {
		t.Fatalf("nested calls inside WithWorkingTree: %v", err)
	}
}

// (b) A second client on the same directory, spelled differently, waits:
// the uncleaned "repo/." (review round 1: filepath.Join cleaned the
// spelling the test meant to pass, so the canonical key was never
// exercised), a symlink to the repository, and — on macOS, where t.TempDir
// is under the /var -> /private/var symlink — the resolved path.
func TestTreeLock_SameTreeDifferentSpellingShareOneLock(t *testing.T) {
	dir := tlRepo(t)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	spellings := map[string]string{"repo/.": dir + string(filepath.Separator) + ".", "symlink": alias}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil && resolved != dir {
		spellings["resolved"] = resolved
	}
	c1 := NewCLIClient(dir, WithHomeBranch("main"))
	for name, spelling := range spellings {
		t.Run(name, func(t *testing.T) {
			c2 := NewCLIClient(spelling)
			if c2.HomeBranch() != "main" {
				t.Fatalf("the home branch is not found through %q", spelling)
			}
			release := tlHold(t, c1)
			defer release()
			blocked, res := tlBlocks(func() error { return c2.Checkout(context.Background(), "feature") }, 300*time.Millisecond)
			if !blocked {
				t.Fatalf("a second client on %q ran while the first held the tree (err=%v)", spelling, <-res)
			}
			treeMu.Lock()
			l := treeLocks[canonicalTreeKey(dir)]
			n := 0
			if l != nil {
				n = l.refs
			}
			treeMu.Unlock()
			if n != 2 {
				t.Fatalf("holder and waiter on %q share one lock entry with refs %d, want 2", spelling, n)
			}
			release()
			if err := <-res; err != nil {
				t.Fatalf("Checkout after release: %v", err)
			}
			if err := c2.Checkout(context.Background(), "main"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// (c) Waiting ends with the ctx: a *TreeWaitError that unwraps to it.
func TestTreeLock_WaitHonoursContext(t *testing.T) {
	dir := tlRepo(t)
	c := NewCLIClient(dir)
	release := tlHold(t, c)
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	ran := false
	err := c.WithWorkingTree(ctx, func(context.Context) error { ran = true; return nil })
	var we *TreeWaitError
	if !errors.As(err, &we) || !errors.Is(err, context.DeadlineExceeded) || ran {
		t.Fatalf("WithWorkingTree while held = %v (ran=%v), want *TreeWaitError wrapping DeadlineExceeded and fn not run", err, ran)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel2()
	if err := c.Checkout(ctx2, "feature"); !errors.As(err, &we) {
		t.Fatalf("Checkout while held = %v, want *TreeWaitError", err)
	}
}

// (d) The registry entry is gone once every holder and waiter left.
func TestTreeLock_RegistryEmptyAfterRelease(t *testing.T) {
	dir := tlRepo(t)
	c := NewCLIClient(dir)
	release := tlHold(t, c)
	waited := make(chan error, 1)
	go func() { waited <- c.Checkout(context.Background(), "feature") }()
	time.Sleep(50 * time.Millisecond)
	if !tlRegistered(dir) {
		t.Fatal("no registry entry while the tree is held")
	}
	release()
	if err := <-waited; err != nil {
		t.Fatal(err)
	}
	if err := c.WithWorkingTree(context.Background(), func(ctx context.Context) error {
		return c.Checkout(ctx, "main")
	}); err != nil {
		t.Fatal(err)
	}
	if tlRegistered(dir) {
		t.Fatal("the lock registry kept an entry after every holder released")
	}
}

// (e) A ticket that escaped a released sequence is not honoured.
func TestTreeLock_StaleTicketDoesNotBypass(t *testing.T) {
	dir := tlRepo(t)
	c := NewCLIClient(dir)
	var leaked context.Context
	if err := c.WithWorkingTree(context.Background(), func(ctx context.Context) error {
		leaked = ctx
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	release := tlHold(t, c)
	defer release()
	blocked, res := tlBlocks(func() error { return c.Checkout(context.WithoutCancel(leaked), "feature") }, 300*time.Millisecond)
	if !blocked {
		t.Fatalf("a released sequence's ticket bypassed the lock (err=%v)", <-res)
	}
	release()
	if err := <-res; err != nil {
		t.Fatal(err)
	}
}

// (f) An in-place write scope holds the tree until Cleanup.
func TestTreeLock_InPlaceScopeHoldsTree(t *testing.T) {
	dir := tlRepo(t)
	c := NewCLIClient(dir)
	scope, err := EnterBranch(context.Background(), dir, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	blocked, res := tlBlocks(func() error { return c.Checkout(context.Background(), "feature") }, 300*time.Millisecond)
	if !blocked {
		scope.Cleanup()
		t.Fatalf("Checkout ran inside an open in-place scope (err=%v)", <-res)
	}
	scope.Cleanup()
	scope.Cleanup() // idempotent
	if err := <-res; err != nil {
		t.Fatalf("Checkout after Cleanup: %v", err)
	}
}

// (g) Inside an in-place scope, scope.Context(ctx) re-enters; the plain
// ctx waits for the scope itself until its deadline (the documented trap).
func TestTreeLock_InPlaceScopeContextReenters(t *testing.T) {
	dir := tlRepo(t)
	c := NewCLIClient(dir)
	ctx := context.Background()
	scope, err := EnterBranch(ctx, dir, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer scope.Cleanup()
	done := make(chan error, 1)
	go func() { done <- c.Checkout(scope.Context(ctx), "feature") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Checkout(scope.Context(ctx)): %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a call made with scope.Context(ctx) waited for its own scope")
	}
	plain, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = c.Checkout(plain, "main")
	var we *TreeWaitError
	if !errors.As(err, &we) || time.Since(start) < 250*time.Millisecond {
		t.Fatalf("Checkout(plain ctx) inside the scope = %v after %v, want *TreeWaitError at the deadline", err, time.Since(start))
	}
}

// (h) A done ctx never acquires, even when the lock is free.
func TestTreeLock_DoneContextNeverAcquires(t *testing.T) {
	dir := tlRepo(t)
	c := NewCLIClient(dir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ran := 0
	for i := 0; i < 200; i++ {
		err := c.WithWorkingTree(ctx, func(context.Context) error { ran++; return nil })
		var we *TreeWaitError
		if !errors.As(err, &we) {
			t.Fatalf("call %d: WithWorkingTree(done ctx) = %v, want *TreeWaitError", i, err)
		}
	}
	if ran != 0 {
		t.Fatalf("fn ran %d/200 times with a done ctx", ran)
	}
	if tlRegistered(dir) {
		t.Fatal("refused acquisitions left a registry entry")
	}
}

// (i) On a home-branch tree, fn's ctx survives the caller's cancellation
// and is bounded by treeSequenceTimeout.
func TestTreeLock_SequenceIsDetachedAndBounded(t *testing.T) {
	dir := tlRepo(t)
	c := NewCLIClient(dir, WithHomeBranch("main"))
	ctx, cancel := context.WithCancel(context.Background())
	start := time.Now()
	var errAfter error
	var deadline time.Time
	var hasDeadline bool
	err := c.WithWorkingTree(ctx, func(fctx context.Context) error {
		cancel()
		errAfter = fctx.Err()
		deadline, hasDeadline = fctx.Deadline()
		return c.Checkout(fctx, "feature") // still runs: the sequence is not abandoned
	})
	if err != nil {
		t.Fatalf("WithWorkingTree: %v", err)
	}
	if errAfter != nil {
		t.Fatalf("fn's ctx was cancelled with the caller's: %v", errAfter)
	}
	if !hasDeadline || deadline.After(start.Add(treeSequenceTimeout+time.Second)) {
		t.Fatalf("fn's ctx deadline = %v (set %v), want at most treeSequenceTimeout (%v) from the start", deadline, hasDeadline, treeSequenceTimeout)
	}
	if got := tlRun(t, dir, "symbolic-ref", "HEAD"); got != "refs/heads/main" {
		t.Fatalf("exit restore did not return the tree home: HEAD=%s", got)
	}
}

// (i, codex review round 1) On a tree without a home branch the sequence
// only holds the lock: fn's ctx keeps the caller's cancellation.
func TestTreeLock_NoHomeSequenceKeepsCancellation(t *testing.T) {
	dir := tlRepo(t)
	c := NewCLIClient(dir)
	ctx, cancel := context.WithCancel(context.Background())
	var errAfter error
	err := c.WithWorkingTree(ctx, func(fctx context.Context) error {
		cancel()
		errAfter = fctx.Err()
		return nil
	})
	if err != nil {
		t.Fatalf("WithWorkingTree: %v", err)
	}
	if !errors.Is(errAfter, context.Canceled) {
		t.Fatalf("fn's ctx after the caller cancelled: %v, want context.Canceled on a tree without a home branch", errAfter)
	}
}

// Every git child of this package is built by newGitCmd (SIGTERM on
// cancel, WaitDelay): no other file may call exec.Command or
// exec.CommandContext (review round 1 — a runner going back to
// exec.CommandContext would be SIGKILLed again and leave index.lock).
func TestGitChildrenAreBuiltByNewGitCmd(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "gitcmd.go" {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			code, _, _ := strings.Cut(line, "//")
			if strings.Contains(code, "exec.Command(") || strings.Contains(code, "exec.CommandContext(") {
				t.Errorf("%s:%d starts a process without newGitCmd: %s", f, i+1, strings.TrimSpace(line))
			}
		}
	}
	cmd := newGitCmd(context.Background(), "version")
	if cmd.Cancel == nil || cmd.WaitDelay <= 0 {
		t.Fatal("newGitCmd must set Cancel (SIGTERM) and WaitDelay")
	}
}

// AC 24a on the CommitBinder runner (review round 1): runWith — MergeBound,
// VerifyCommit, the restore and ResetBranch run git through it — stops a
// cancelled git with SIGTERM too. A `read-tree -m -u` that checks out a
// file through a slow smudge filter is cancelled at 300 ms; git's own
// handler removes .git/index.lock. (Review round 2 moved this from
// ResetBranch, which now runs its git on a detached context, to runWith
// itself.)
func TestF9_CancelledCommitBinderGitLeavesNoLockFile(t *testing.T) {
	dir := tlRepo(t)
	tlRun(t, dir, "config", "filter.slow.smudge", "sleep 2; cat")
	tlRun(t, dir, "config", "filter.slow.clean", "cat")
	if err := os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("*.slow filter=slow\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tlRun(t, dir, "add", ".gitattributes")
	tlRun(t, dir, "commit", "-q", "-m", "attributes")
	from := tlRun(t, dir, "rev-parse", "main")
	tlRun(t, dir, "checkout", "-q", "-b", "slow")
	if err := os.WriteFile(filepath.Join(dir, "b.slow"), []byte("slow content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tlRun(t, dir, "add", "b.slow")
	tlRun(t, dir, "commit", "-q", "-m", "slow file")
	to := tlRun(t, dir, "rev-parse", "slow")
	tlRun(t, dir, "checkout", "-q", "main")

	c := NewCLIClient(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, _, err := c.runWith(ctx, "reset_branch", runOpts{}, "read-tree", "-m", "-u", "--end-of-options", from, to); err == nil {
		t.Fatal("a read-tree cancelled during its smudge filter reported success")
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "index.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf(".git/index.lock after a cancelled runWith: %v (git was killed, not terminated)", err)
	}
}
