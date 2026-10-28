package workspace

// INIT-024 integration A (TASK-003 x TASK-008): buildServiceSet brings a
// work tree checked out with conversions to its stored bytes
// (EnsureNeutralWorkTree, TASK-003), and that call waits for the tree's
// hold — to add the neutral line (since TASK-003 review round 4) and to
// renormalise a marked tree. TASK-008 runs the build's one-shot calls under
// the init deadline; EnsureNeutralWorkTree ran on the pool's lifetime
// context instead, so a tree another operation held (a merge of an evicted
// ServiceSet, say) kept the build — and with it every Get of the workspace
// — waiting until the hold was released. It now gives up at the init
// deadline, the workspace is served, and the next build does the work.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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

	// Another operation holds the work tree until the test releases it.
	held, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
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
	releaseHold := func() {
		releaseOnce.Do(func() {
			close(release)
			if err := <-holderDone; err != nil {
				t.Errorf("holder: %v", err)
			}
		})
	}
	t.Cleanup(releaseHold) // before pool.Close

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

	// Nothing changed under the holder: the line is added only under the
	// tree's hold, so it is not there yet, and a.txt keeps its converted
	// bytes.
	if has, err := git.HasNeutralCheckoutAttributes(context.Background(), repo); err != nil || has {
		t.Fatalf("neutral checkout attributes while the tree is held: %v, %v; want no line yet", has, err)
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "a.txt")); !strings.Contains(string(b), "\r\n") {
		t.Fatalf("a.txt was rewritten while another operation held the tree: %q", b)
	}

	// The work is left for the next build, not skipped: once the hold is
	// released, a fresh build adds the line, brings the tree to its stored
	// bytes and clears the mark. (A second pool with a generous InitTimeout,
	// so a loaded machine cannot cut this build short.)
	releaseHold()
	pool.Close()
	pool2 := NewServicePool(context.Background(), &multiConfigResolver{configs: map[string]Config{
		"ws": {ID: "ws", RepoPath: repo, Status: StatusActive},
	}}, PoolConfig{IdleCheckInterval: -1, InitTimeout: time.Minute})
	t.Cleanup(pool2.Close)
	if _, err := pool2.Get(context.Background(), "ws"); err != nil {
		t.Fatalf("Get after the hold was released: %v", err)
	}
	pool2.Release("ws")
	if has, err := git.HasNeutralCheckoutAttributes(context.Background(), repo); err != nil || !has {
		t.Fatalf("neutral checkout attributes after the next build: %v, %v; want the line", has, err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git", "spine-renormalise-pending")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("renormalise marker after the next build: %v; want it cleared", err)
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "a.txt")); string(b) != "one\ntwo\n" {
		t.Fatalf("a.txt after the next build: %q; want its stored bytes", b)
	}
}
