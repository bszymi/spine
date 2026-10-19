package worktreefs

import (
	"errors"
	"io/fs"
	"path/filepath"
	"runtime"
	"testing"
)

// INIT-023 TASK-012 review round 2: LstatEntry identifies a leaf of any
// type without following or opening it, and refuses a path whose
// components are not real directories, like every other operation.
func TestTree_LstatEntry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links")
	}
	f := newFixture(t)
	f.link(t, "gov/out.md", filepath.Join(f.outside, "victim.md"))
	f.link(t, "gov/rel.md", "../other/x.md")
	f.link(t, "gov/dangling.md", "nowhere.md")
	f.link(t, "linkdir", f.outside)

	if fi, target, err := f.tree.LstatEntry("gov/x.md"); err != nil || !fi.Mode().IsRegular() || fi.Size() != 1 || target != "" {
		t.Fatalf("regular leaf = %v %q %v", fi, target, err)
	}
	if fi, target, err := f.tree.LstatEntry("gov"); err != nil || !fi.IsDir() || target != "" {
		t.Fatalf("directory leaf = %v %q %v", fi, target, err)
	}
	for rel, want := range map[string]string{
		"gov/out.md":      filepath.Join(f.outside, "victim.md"),
		"gov/rel.md":      "../other/x.md",
		"gov/dangling.md": "nowhere.md",
	} {
		fi, target, err := f.tree.LstatEntry(rel)
		if err != nil || fi.Mode()&fs.ModeSymlink == 0 || target != want {
			t.Fatalf("%s = %v %q %v; want the link itself, target %q", rel, fi, target, err, want)
		}
		// The link's own size, never the target's ("victim" is 6 bytes).
		if fi.Size() != int64(len(want)) {
			t.Fatalf("%s size = %d, want the link's %d (the target was followed)", rel, fi.Size(), len(want))
		}
	}
	if _, _, err := f.tree.LstatEntry("linkdir/victim.md"); err == nil {
		t.Fatal("a path through a symlinked directory must be refused")
	} else {
		requireReason(t, err, ReasonSymlink, "linkdir")
	}
	if _, _, err := f.tree.LstatEntry("gov/x.md/y.md"); err == nil {
		t.Fatal("a file used as a directory must be refused")
	} else {
		requireReason(t, err, ReasonNotDir, "gov/x.md")
	}
	if _, _, err := f.tree.LstatEntry("gov/missing.md"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing leaf: %v", err)
	}
	if _, _, err := f.tree.LstatEntry("nodir/missing.md"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing directory: %v", err)
	}
	if _, _, err := f.tree.LstatEntry(".git/info/exclude"); err == nil {
		t.Fatal("a path under .git must be refused")
	} else {
		requireReason(t, err, ReasonGitDir, ".git")
	}
	if _, _, err := f.tree.LstatEntry("../outside/victim.md"); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("an escaping path: %v", err)
	}
	if got := readString(t, filepath.Join(f.outside, "victim.md")); got != "victim" {
		t.Fatalf("outside file changed: %q", got)
	}
}
