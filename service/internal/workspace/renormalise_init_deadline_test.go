package workspace

// INIT-024 integration A (TASK-003 x TASK-008): buildServiceSet brings a
// work tree checked out with conversions to its stored bytes
// (EnsureNeutralWorkTree, TASK-003), and that renormalisation waits for the
// tree's hold. TASK-008 runs the build's one-shot calls under the init
// deadline; the renormalisation ran on the pool's lifetime context instead,
// so a tree another operation held (a merge of an evicted ServiceSet, say)
// kept the build — and with it every Get of the workspace — waiting until
// the hold was released. It now stops at the init deadline, stays pending
// for the next build, and the workspace is served.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/testutil"
)

// convertedWorkTree is a repository whose a.txt was checked out under
// `* text eol=crlf`: once the neutral checkout attributes turn the
// conversion off, the file looks modified, so adding the line leaves the
// tree's renormalisation pending until it holds the tree.
func convertedWorkTree(t *testing.T) string {
	t.Helper()
	repo := testutil.NewTempRepo(t)
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@x.invalid", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@x.invalid")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	testutil.WriteFile(t, repo, ".gitattributes", "* text eol=crlf\n")
	testutil.WriteFile(t, repo, "a.txt", "one\ntwo\n")
	run("add", "-A")
	run("commit", "-q", "-m", "files")
	if err := os.Remove(filepath.Join(repo, "a.txt")); err != nil {
		t.Fatal(err)
	}
	run("checkout", "--", "a.txt")
	if b, _ := os.ReadFile(filepath.Join(repo, "a.txt")); !strings.Contains(string(b), "\r\n") {
		t.Fatalf("fixture: a.txt not converted: %q", b)
	}
	return repo
}

func TestServicePool_PendingRenormalisationStopsAtTheInitDeadline(t *testing.T) {
	git.SetDefaultCheckoutLimits(git.CheckoutLimits{Budget: &git.TreeBudget{}})
	t.Cleanup(func() { git.SetDefaultCheckoutLimits(git.CheckoutLimits{}) })
	repo := convertedWorkTree(t)

	// Another operation holds the work tree for the whole test.
	held, release := make(chan struct{}), make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- git.NewCLIClient(repo).WithWorkingTree(context.Background(), func(context.Context) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	const initTimeout = 200 * time.Millisecond
	pool := NewServicePool(context.Background(), &multiConfigResolver{configs: map[string]Config{
		"ws": {ID: "ws", RepoPath: repo, Status: StatusActive},
	}}, PoolConfig{IdleCheckInterval: -1, InitTimeout: initTimeout})
	t.Cleanup(pool.Close)
	t.Cleanup(func() { // before pool.Close
		close(release)
		if err := <-holderDone; err != nil {
			t.Errorf("holder: %v", err)
		}
	})

	// While the tree stays held the workspace is served within a few init
	// deadlines: the first Get may be abandoned at InitTimeout, the late
	// build is then installed.
	deadline := time.Now().Add(initTimeout + 10*time.Second)
	for {
		_, err := pool.Get(context.Background(), "ws")
		if err == nil {
			pool.Release("ws")
			break
		}
		if !errors.Is(err, ErrWorkspaceUnavailable) {
			t.Fatalf("Get: %v; want a ServiceSet, or ErrWorkspaceUnavailable while the build is abandoned", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("the workspace was never served while its tree was held: the build's renormalisation waited for the hold past the init deadline")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The renormalisation was left pending, not skipped: the line is in
	// place and the tree is still marked for the next build.
	if has, err := git.HasNeutralCheckoutAttributes(context.Background(), repo); err != nil || !has {
		t.Fatalf("neutral checkout attributes after the build: %v, %v; want the line", has, err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git", "spine-renormalise-pending")); err != nil {
		t.Fatalf("renormalise marker after the build: %v; want it still pending", err)
	}
}
