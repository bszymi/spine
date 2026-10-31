package artifact_test

// INIT-024 TASK-011 AC 2 (the TASK-010 cross-task review's X-1, through
// the API path): concurrent branch-scoped writes — artifact.Service.Create
// with a write context, the path of POST /artifacts with
// write_context.run_id — onto ONE run branch whose tree is within the
// `spine serve` checkout budget. Each write entered the branch with its
// own worktree, so each wrote the whole tree to the shared temporary
// disk, including the writes git then refused. The test uses only API that
// existed before the task, so it runs (and fails) at the base commit.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/artifact"
	"github.com/bszymi/spine/core/branchprotect/config"
	"github.com/bszymi/spine/core/domain"
)

func claimGit(t *testing.T, dir string, stdin []byte, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@x.invalid", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@x.invalid")
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

func claimBytesUnder(root string) int64 {
	var n int64
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if fi, e := d.Info(); e == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// countWorktreeAdds puts a wrapper first on PATH that appends one line
// per `git worktree add` (a single short write, so concurrent processes
// never interleave) and execs the real git; it returns the count so far.
func countWorktreeAdds(t *testing.T) func() int {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git not on PATH: %v", err)
	}
	if realGit, err = filepath.Abs(realGit); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "adds.log")
	script := "#!/bin/sh\nif [ \"$1\" = worktree ] && [ \"$2\" = add ]; then printf 'add\\n' >> \"$SPINE_TEST_WTADD_LOG\"; fi\nexec \"$SPINE_TEST_WTADD_REAL\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil { //nolint:gosec // G306: the wrapper must be executable
		t.Fatal(err)
	}
	t.Setenv("SPINE_TEST_WTADD_REAL", realGit)
	t.Setenv("SPINE_TEST_WTADD_LOG", log)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() int {
		data, err := os.ReadFile(log) //nolint:gosec // G304: the test's own log
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		return strings.Count(string(data), "add\n")
	}
}

// TestCreate_ConcurrentRunBranchWritesPutOneTreeOnDisk is AC 2: four
// concurrent Creates with a write context on one run branch (a 12.5 MiB
// tree, within `spine serve`'s default budget) never have more than one
// tree under TMPDIR; only a write that succeeds adds a worktree (a refused
// one writes nothing), every successful write is on the branch, and a
// refused write keeps the classification it had (git_error). At 854a1e08
// every write added a worktree and the peak was about four trees.
func TestCreate_ConcurrentRunBranchWritesPutOneTreeOnDisk(t *testing.T) {
	const writers = 4
	svc, repo, _, _ := newServiceWithEvents(t, []config.Rule{})
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")

	// The run branch: main's tree plus docs/ with 50 entries naming one
	// 256 KiB blob.
	blob := claimGit(t, repo, make([]byte, 256<<10), "hash-object", "-w", "--stdin")
	var mk strings.Builder
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&mk, "100644 blob %s\tf%04d.bin\n", blob, i)
	}
	docs := claimGit(t, repo, []byte(mk.String()), "mktree")
	rootList := "040000 tree " + docs + "\tdocs\n"
	if mainList := claimGit(t, repo, nil, "ls-tree", "main"); mainList != "" {
		rootList = mainList + "\n" + rootList
	}
	root := claimGit(t, repo, []byte(rootList), "mktree")
	head := claimGit(t, repo, nil, "rev-parse", "main")
	tip := claimGit(t, repo, nil, "commit-tree", root, "-p", head, "-m", "run content")
	run := "spine/run/task-claim-1a2b3c4d"
	claimGit(t, repo, nil, "update-ref", "refs/heads/"+run, tip)

	// What `spine serve` installs from INIT-024 TASK-003 (defaults).
	b := git.DefaultTreeBudget()
	git.SetDefaultCheckoutLimits(git.CheckoutLimits{Budget: &b, MutateTimeout: git.DefaultMutateTimeout})
	t.Cleanup(func() { git.SetDefaultCheckoutLimits(git.CheckoutLimits{}) })
	if err := git.NewCLIClient(repo).EnsureNeutralWorkTree(context.Background()); err != nil {
		t.Fatalf("EnsureNeutralWorkTree: %v", err)
	}

	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	solo, err := git.EnterBranch(context.Background(), repo, run, nil)
	if err != nil {
		t.Fatal(err)
	}
	oneTree := claimBytesUnder(tmp)
	solo.Cleanup()

	adds := countWorktreeAdds(t)
	var peak atomic.Int64
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			if n := claimBytesUnder(tmp); n > peak.Load() {
				peak.Store(n)
			}
			select {
			case <-stop:
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()

	ctx := artifact.WithWriteContext(operatorCtx(), artifact.WriteContext{Branch: run})
	errs := make([]error, writers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = svc.Create(ctx, fmt.Sprintf("governance/claim-%d.md", i), governanceContent)
		}()
	}
	close(start)
	wg.Wait()
	close(stop)
	<-stopped

	onBranch := claimGit(t, repo, nil, "ls-tree", "--name-only", run, "governance/")
	ok := 0
	for i, err := range errs {
		name := fmt.Sprintf("governance/claim-%d.md", i)
		if err == nil {
			ok++
			if !strings.Contains(onBranch, name) {
				t.Errorf("write %d succeeded but %s is not on %s", i, name, run)
			}
			continue
		}
		var se *domain.SpineError
		if !errors.As(err, &se) || se.Code != domain.ErrGit {
			t.Errorf("write %d: err = %v, want the git_error classification", i, err)
		}
	}
	if ok == 0 {
		t.Fatal("no write succeeded")
	}
	if n := adds(); n != ok {
		t.Errorf("%d worktree adds for %d successful writes: refused writes wrote the tree", n, ok)
	}
	// One tree, plus the artifact file the admitted write adds to it (a
	// second tree would be 12.5 MiB more).
	if p := peak.Load(); p > oneTree+64<<10 {
		t.Errorf("peak bytes under TMPDIR %d > one tree %d (%.1f trees)", p, oneTree, float64(p)/float64(oneTree))
	}
	t.Logf("%d writers, %d succeeded; peak %d bytes, one tree %d", writers, ok, peak.Load(), oneTree)
}
