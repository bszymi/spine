//go:build unix

package git_test

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
)

// TestRepoClaimKey_ReadsOnlyRegularAdministrativeFiles (INIT-024 TASK-011):
// the claim key is derived from git-administrative entries only, and none
// is followed or opened unless it is a regular file. A .git that is a
// symbolic link is not followed (the root is the key), and a commondir
// that is a FIFO is never opened — opening it would block the entry — so
// the key falls back to the worktree's git directory.
func TestRepoClaimKey_ReadsOnlyRegularAdministrativeFiles(t *testing.T) {
	repo, _, _, _ := tbRepo(t)

	linked := t.TempDir()
	if err := os.Symlink(filepath.Join(repo, ".git"), filepath.Join(linked, ".git")); err != nil {
		t.Fatal(err)
	}
	if got := git.RepoClaimKeyForTest(linked); got != canonical(t, linked) {
		t.Errorf(".git symbolic link: key %q, want the root (not followed)", got)
	}

	lw := filepath.Join(t.TempDir(), "lw")
	tbGit(t, repo, nil, "worktree", "add", "-q", "-b", "lw-branch", lw, "main")
	gitDir := strings.TrimSpace(strings.TrimPrefix(readFile(t, filepath.Join(lw, ".git")), "gitdir:"))
	commondir := filepath.Join(gitDir, "commondir")
	if err := os.Remove(commondir); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(commondir, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	got := make(chan string, 1)
	go func() { got <- git.RepoClaimKeyForTest(lw) }()
	select {
	case k := <-got:
		if k != canonical(t, gitDir) {
			t.Errorf("FIFO commondir: key %q, want the worktree's git directory %q", k, canonical(t, gitDir))
		}
	case <-time.After(10 * time.Second):
		// Unblock the open so the goroutine can end, then fail.
		if f, err := os.OpenFile(commondir, os.O_WRONLY, 0); err == nil {
			_ = f.Close()
		}
		t.Fatal("the key derivation opened a FIFO commondir and blocked")
	}
}
