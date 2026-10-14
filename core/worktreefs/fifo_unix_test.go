//go:build unix

package worktreefs

import (
	"os"
	"syscall"
	"testing"
	"time"
)

// INIT-023 codex 6.1 catch-up: a leaf swapped for a named pipe between its
// check and the open that pins or reads it must be refused, not waited on.
// A plain O_RDONLY open of a FIFO blocks in the kernel until a writer
// appears, so before the fix Create (the pin) and ReadFileLimit hung with
// their handles held; the SameFile check that rejects the swap ran only
// once a writer showed up.

// swapForFIFO replaces the file at rel with a named pipe nobody writes to.
func (f *fixture) swapForFIFO(t *testing.T, rel string) {
	t.Helper()
	if err := os.Remove(f.path(rel)); err != nil {
		t.Error(err)
		return
	}
	if err := syscall.Mkfifo(f.path(rel), 0o644); err != nil {
		t.Error(err)
	}
}

// returnsWithoutAWriter runs op and fails the test when op is still running
// after a few seconds: blocked opening the FIFO at fifo. A writer is then
// opened so the blocked open (and op) can return and the test can end.
func returnsWithoutAWriter(t *testing.T, fifo string, op func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		op()
	}()
	select {
	case <-done:
		return
	case <-time.After(3 * time.Second):
	}
	go func() {
		if w, err := os.OpenFile(fifo, os.O_WRONLY, 0); err == nil {
			_ = w.Close()
		}
	}()
	<-done
	t.Fatal("the call blocked opening a FIFO swapped in at the leaf (returned only once a writer opened it)")
}

// Mutation check: opening the pin with plain os.O_RDONLY fails "the call
// blocked opening a FIFO".
func TestTree_CreateLeafSwappedForFIFOBeforePin(t *testing.T) {
	resetHooks(t)
	f := newFixture(t)
	baseline := openHandles.Load()
	testHookBeforeLeafOp = func(op, _ string) {
		if op == "pin" {
			f.swapForFIFO(t, "gov/new.md")
		}
	}
	var (
		u   *Undo
		err error
	)
	returnsWithoutAWriter(t, f.path("gov/new.md"), func() {
		u, err = f.tree.Create("gov/new.md", []byte("data"), 0o644)
	})
	if u != nil {
		u.Release()
		t.Fatal("Create pinned a FIFO swapped in for the created leaf")
	}
	requireReason(t, err, ReasonChanged, "gov/new.md")
	fi, lerr := os.Lstat(f.path("gov/new.md"))
	if lerr != nil || fi.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("the swapped-in FIFO is left where it is: %v, %v", fi, lerr)
	}
	if got := openHandles.Load(); got != baseline {
		t.Fatalf("open handles: %d, want baseline %d", got, baseline)
	}
}

// Mutation check: opening the leaf with plain os.O_RDONLY fails "the call
// blocked opening a FIFO". Dropping the regular-file check on the opened
// descriptor fails "ReadFile returned bytes" on Linux: the FIFO made after
// the delete gets the freed inode number, so SameFile alone admits it and
// the non-blocking read returns empty content.
func TestTree_ReadLeafSwappedForFIFOAfterCheck(t *testing.T) {
	resetHooks(t)
	f := newFixture(t)
	baseline := openHandles.Load()
	testHookBeforeLeafOp = func(op, _ string) {
		if op == "read" {
			f.swapForFIFO(t, "gov/x.md")
		}
	}
	var (
		b   []byte
		err error
	)
	returnsWithoutAWriter(t, f.path("gov/x.md"), func() {
		b, err = f.tree.ReadFile("gov/x.md")
	})
	if b != nil {
		t.Fatalf("ReadFile returned bytes from a swapped-in FIFO: %q", b)
	}
	requireReason(t, err, ReasonChanged, "gov/x.md")
	if got := openHandles.Load(); got != baseline {
		t.Fatalf("open handles: %d, want baseline %d", got, baseline)
	}
}

// A regular file read through the non-blocking descriptor reads in full
// (O_NONBLOCK has no effect on a regular file), including one larger than
// a pipe buffer.
func TestTree_ReadFileThroughNonBlockingOpen(t *testing.T) {
	f := newFixture(t)
	big := make([]byte, 1<<20)
	for i := range big {
		big[i] = byte('a' + i%26)
	}
	mustWrite(t, f.path("gov/big.md"), string(big), 0o644)
	b, err := f.tree.ReadFile("gov/big.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != string(big) {
		t.Fatalf("read %d bytes, want %d, or content differs", len(b), len(big))
	}
}
