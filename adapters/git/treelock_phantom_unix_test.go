//go:build unix

package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// INIT-023 TASK-012 review round 2, finding 3: the phantom bookkeeping
// identifies a working-tree entry without reading through it. A tracked
// file replaced on disk by a symbolic link — to a FIFO nobody writes to,
// or to a file outside the tree — is fingerprinted by the link's own
// target, in bounded time, and git hash-object never opens it. Before the
// fix hash-object followed the link: a FIFO blocked the restore until
// treeRestoreTimeout, and a link out of the tree was hashed through.
func TestF9_PhantomFingerprintNeverReadsThroughALink(t *testing.T) {
	dir := tlRepo(t)
	outside := t.TempDir()
	fifo := filepath.Join(outside, "pipe")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outside, "secret.md")
	if err := os.WriteFile(secret, []byte("outside the tree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.md"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tlRun(t, dir, "add", "b.md")
	tlRun(t, dir, "commit", "-q", "-m", "b")
	for rel, target := range map[string]string{"a.md": fifo, "b.md": secret} {
		if err := os.Remove(filepath.Join(dir, rel)); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, rel)); err != nil {
			t.Fatal(err)
		}
	}
	c := NewCLIClient(dir)
	ctx := context.Background()
	if st := tlRun(t, dir, "status", "--porcelain", "--untracked-files=no"); !strings.Contains(st, "T a.md") || !strings.Contains(st, "T b.md") {
		t.Fatalf("precondition: status = %q, want both type-changed", st)
	}

	type result struct {
		states map[string]phantomState
		err    error
	}
	done := make(chan result, 1)
	go func() {
		s, err := c.phantomStatesOf(ctx, "restore_tree", []string{"a.md", "b.md"})
		done <- result{s, err}
	}()
	var res result
	select {
	case res = <-done:
	case <-time.After(10 * time.Second):
		// Unblock the reader so the test can end.
		if w, err := os.OpenFile(fifo, os.O_WRONLY, 0); err == nil {
			_ = w.Close()
		}
		<-done
		t.Fatal("the phantom fingerprint blocked on a symbolic link to a FIFO")
	}
	if res.err != nil {
		t.Fatalf("phantomStatesOf: %v", res.err)
	}
	if got := res.states["a.md"].worktree; got != "symlink "+fifo {
		t.Fatalf("a.md fingerprint = %q, want the link's own target", got)
	}
	if got := res.states["b.md"].worktree; got != "symlink "+secret {
		t.Fatalf("b.md fingerprint = %q, want the link's own target (not the outside file's blob)", got)
	}
	if res.states["a.md"].index == "" || res.states["b.md"].index == "" {
		t.Fatalf("index blobs missing: %+v", res.states)
	}

	// The type changes are remembered and recognised again; a retargeted
	// link is a change.
	changes := []trackedChange{{x: ' ', y: 'T', path: "a.md"}, {x: ' ', y: 'T', path: "b.md"}}
	if got := c.rememberPhantoms(ctx, "restore_tree", "main", changes); len(got) != 2 {
		t.Fatalf("rememberPhantoms = %v", got)
	}
	if !c.knownPhantoms(ctx, "restore_tree", changes) {
		t.Fatal("unchanged type-change phantoms are not recognised: every take would quarantine them again")
	}
	if err := os.Remove(filepath.Join(dir, "b.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fifo, filepath.Join(dir, "b.md")); err != nil {
		t.Fatal(err)
	}
	if c.knownPhantoms(ctx, "restore_tree", changes) {
		t.Fatal("a retargeted link passed as the remembered phantom")
	}
	// A staged change is never a phantom.
	if c.knownPhantoms(ctx, "restore_tree", []trackedChange{{x: 'M', y: ' ', path: "a.md"}}) {
		t.Fatal("a staged change passed as a phantom")
	}
}
