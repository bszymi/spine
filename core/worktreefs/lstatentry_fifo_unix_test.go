//go:build unix

package worktreefs

import (
	"io/fs"
	"syscall"
	"testing"
)

// INIT-023 TASK-012 review round 2: LstatEntry never opens its leaf, so a
// FIFO nobody writes to, or a symbolic link to one, is identified without
// blocking.
func TestTree_LstatEntryNeverOpensAFIFO(t *testing.T) {
	f := newFixture(t)
	if err := syscall.Mkfifo(f.path("gov/pipe"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.link(t, "gov/to-pipe", f.path("gov/pipe"))
	returnsWithoutAWriter(t, f.path("gov/pipe"), func() {
		fi, target, err := f.tree.LstatEntry("gov/pipe")
		if err != nil || fi.Mode()&fs.ModeNamedPipe == 0 || target != "" {
			t.Errorf("FIFO leaf = %v %q %v", fi, target, err)
		}
		fi, target, err = f.tree.LstatEntry("gov/to-pipe")
		if err != nil || fi.Mode()&fs.ModeSymlink == 0 || target != f.path("gov/pipe") {
			t.Errorf("link to a FIFO = %v %q %v", fi, target, err)
		}
	})
}
