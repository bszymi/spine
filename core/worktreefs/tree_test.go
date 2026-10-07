package worktreefs

import (
	"errors"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fixture is a tree root next to an "outside" directory:
//
//	base/repo/            the Tree root (a main working tree: .git/ is a dir)
//	  .git/info/exclude
//	  gov/x.md            "x"
//	  other/x.md          "other"
//	  realdir/x.md        "real"
//	base/outside/victim.md "victim"
type fixture struct {
	base, root, outside string
	tree                *Tree
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	base := t.TempDir()
	f := &fixture{base: base, root: filepath.Join(base, "repo"), outside: filepath.Join(base, "outside")}
	for _, d := range []string{".git/info", "gov", "other", "realdir"} {
		mustMkdir(t, filepath.Join(f.root, d))
	}
	mustMkdir(t, f.outside)
	mustWrite(t, filepath.Join(f.root, ".git/info/exclude"), "ex", 0o644)
	mustWrite(t, filepath.Join(f.root, "gov/x.md"), "x", 0o644)
	mustWrite(t, filepath.Join(f.root, "other/x.md"), "other", 0o644)
	mustWrite(t, filepath.Join(f.root, "realdir/x.md"), "real", 0o644)
	mustWrite(t, filepath.Join(f.outside, "victim.md"), "victim", 0o644)
	tr, err := Open(f.root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	f.tree = tr
	t.Cleanup(func() { _ = tr.Close() })
	return f
}

func (f *fixture) link(t *testing.T, rel, target string) {
	t.Helper()
	if err := os.Symlink(target, filepath.Join(f.root, rel)); err != nil {
		t.Fatalf("symlink %s -> %s: %v", rel, target, err)
	}
}

func (f *fixture) path(rel string) string { return filepath.Join(f.root, filepath.FromSlash(rel)) }

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, p, data string, perm fs.FileMode) {
	t.Helper()
	if err := os.WriteFile(p, []byte(data), perm); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, perm); err != nil { // defeat umask
		t.Fatal(err)
	}
}

func readString(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// resetHooks restores every package test hook when the test ends.
func resetHooks(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		testHookSkipComponentChecks = false
		testHookAfterComponentLstat = nil
		testHookBeforeLeafOp = nil
		testHookAfterLeafOpen = nil
		testHookObserveRead = nil
	})
}

type snapEntry struct {
	kind string // "dir", "file", "link", "other"
	data string // file content or link target
	mode fs.FileMode
	mod  time.Time
}

// snapshot records every path below each dir (paths, contents, link
// targets, modes and mtimes, directories included), so "nothing changed"
// is a single map comparison.
func snapshot(t *testing.T, dirs ...string) map[string]snapEntry {
	t.Helper()
	// Collect the paths first and inspect them after the walk, so no
	// filesystem call runs inside the WalkDir callback.
	var paths []string
	for _, d := range dirs {
		err := filepath.WalkDir(d, func(p string, _ fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			paths = append(paths, p)
			return nil
		})
		if err != nil {
			t.Fatalf("snapshot %s: %v", d, err)
		}
	}
	out := map[string]snapEntry{}
	for _, p := range paths {
		fi, err := os.Lstat(p)
		if err != nil {
			t.Fatalf("snapshot lstat %s: %v", p, err)
		}
		e := snapEntry{mode: fi.Mode(), mod: fi.ModTime()}
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			e.kind = "link"
			e.data, _ = os.Readlink(p)
		case fi.IsDir():
			e.kind = "dir"
		case fi.Mode().IsRegular():
			e.kind = "file"
			b, _ := os.ReadFile(p)
			e.data = string(b)
		default:
			e.kind = "other"
		}
		out[p] = e
	}
	return out
}

func diffSnapshots(before, after map[string]snapEntry) []string {
	var diffs []string
	for p, b := range before {
		a, ok := after[p]
		switch {
		case !ok:
			diffs = append(diffs, "removed "+p)
		case a != b:
			diffs = append(diffs, "changed "+p)
		}
	}
	for p := range after {
		if _, ok := before[p]; !ok {
			diffs = append(diffs, "added "+p)
		}
	}
	return diffs
}

func requireReason(t *testing.T, err error, reason Reason, component string) {
	t.Helper()
	if !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("want ErrUnsafePath (%s), got %v", reason, err)
	}
	var pe *PathError
	if !errors.As(err, &pe) {
		t.Fatalf("want *PathError, got %T %v", err, err)
	}
	if pe.Reason != reason {
		t.Fatalf("reason = %s (%v), want %s", pe.Reason, err, reason)
	}
	if component != "" && pe.Component != component {
		t.Fatalf("component = %q, want %q", pe.Component, component)
	}
}

// TestTree_RefusesSymlinkComponents is AC3 (design AC1): every operation
// refuses every link shape, with ReasonSymlink naming the exact
// repo-relative prefix, before any filesystem change.
//
// Mutation check: "Stat instead of Lstat in the walk" makes the
// tree-root "gitlink -> .git/info" Create land in .git/info/attributes (a
// snapshot diff), and the same-directory "gov/samedir.md -> x.md" ReadFile
// return x.md's bytes — both are links os.Root itself would follow.
func TestTree_RefusesSymlinkComponents(t *testing.T) {
	type shape struct {
		name      string
		link      string // repo-relative link to plant
		target    func(f *fixture) string
		existing  string // op path naming an existing file through the link
		fresh     string // op path naming a new file through the link
		component string // the refused prefix
	}
	abs := func(rel string) func(*fixture) string {
		return func(f *fixture) string { return filepath.Join(f.outside, rel) }
	}
	lit := func(s string) func(*fixture) string { return func(*fixture) string { return s } }

	shapes := []shape{
		// Leaf links: the op path is the link itself.
		{"leaf dangling absolute", "gov/abs.md", abs("abs.md"), "gov/abs.md", "gov/abs.md", "gov/abs.md"},
		{"leaf dangling relative escaping", "gov/rel.md", lit("../../outside/rel.md"), "gov/rel.md", "gov/rel.md", "gov/rel.md"},
		{"leaf absolute to existing outside", "gov/victim.md", abs("victim.md"), "gov/victim.md", "gov/victim.md", "gov/victim.md"},
		{"leaf in-root same-dir to existing", "gov/samedir.md", lit("x.md"), "gov/samedir.md", "gov/samedir.md", "gov/samedir.md"},
		{"leaf in-root into .git/info", "gov/attrs.md", lit("../.git/info/attributes"), "gov/attrs.md", "gov/attrs.md", "gov/attrs.md"},
		{"leaf tree-root into .git/info", "attrs.md", lit(".git/info/attributes"), "attrs.md", "attrs.md", "attrs.md"},
		{"leaf tree-root to existing .git file", "exclude.md", lit(".git/info/exclude"), "exclude.md", "exclude.md", "exclude.md"},
		// Intermediate directory links.
		{"dir in-root", "linkdir", lit("realdir"), "linkdir/x.md", "linkdir/new.md", "linkdir"},
		{"dir tree-root into .git/info", "gitlink", lit(".git/info"), "gitlink/exclude", "gitlink/attributes", "gitlink"},
		{"dir in-root sibling", "gov/sib", lit("../other"), "gov/sib/x.md", "gov/sib/new.md", "gov/sib"},
		{"dir escaping absolute", "outdir", func(f *fixture) string { return f.outside }, "outdir/victim.md", "outdir/new.md", "outdir"},
		{"dir escaping relative", "gov/up", lit("../../outside"), "gov/up/victim.md", "gov/up/new.md", "gov/up"},
	}

	type op struct {
		name  string
		fresh bool
		run   func(tr *Tree, rel string) error
	}
	ops := []op{
		{"Lstat", false, func(tr *Tree, rel string) error { _, err := tr.Lstat(rel); return err }},
		{"ReadFile", false, func(tr *Tree, rel string) error {
			b, err := tr.ReadFile(rel)
			if b != nil {
				return errors.New("ReadFile returned bytes through a link: " + string(b))
			}
			return err
		}},
		{"Create", true, func(tr *Tree, rel string) error {
			u, err := tr.Create(rel, []byte("* merge=union\n"), 0o644)
			u.Release()
			return err
		}},
		{"Replace", false, func(tr *Tree, rel string) error { return tr.Replace(rel, []byte("evil")) }},
		{"WriteFile/new", true, func(tr *Tree, rel string) error { return tr.WriteFile(rel, []byte("evil"), 0o644) }},
		{"WriteFile/existing", false, func(tr *Tree, rel string) error { return tr.WriteFile(rel, []byte("evil"), 0o644) }},
		{"Remove", false, func(tr *Tree, rel string) error { return tr.Remove(rel) }},
	}

	for _, s := range shapes {
		for _, o := range ops {
			t.Run(s.name+"/"+o.name, func(t *testing.T) {
				f := newFixture(t)
				f.link(t, s.link, s.target(f))
				before := snapshot(t, f.root, f.outside)

				rel := s.existing
				if o.fresh {
					rel = s.fresh
				}
				err := o.run(f.tree, rel)

				if d := diffSnapshots(before, snapshot(t, f.root, f.outside)); len(d) != 0 {
					t.Fatalf("filesystem changed: %v (err=%v)", d, err)
				}
				requireReason(t, err, ReasonSymlink, s.component)
			})
		}
	}
}

// TestTree_ComponentSwappedBetweenCheckAndOpen is AC4: a checked
// intermediate directory swapped for a link between its Lstat and its
// OpenRoot is refused (ReasonChanged), and nothing is written at the link
// target.
//
// Mutation check: removing the os.SameFile comparison in descend lets the
// Create rows write other/attributes or .git/info/attributes.
func TestTree_ComponentSwappedBetweenCheckAndOpen(t *testing.T) {
	for _, target := range []string{"other", ".git/info"} {
		for _, opName := range []string{"Create", "Replace", "WriteFile"} {
			t.Run(target+"/"+opName, func(t *testing.T) {
				resetHooks(t)
				f := newFixture(t)
				mustWrite(t, f.path("gov/attributes"), "gov-orig", 0o644)
				if opName != "Create" {
					// Give the swap target a file the op would hit, so a
					// missing identity check shows up as an overwrite.
					mustWrite(t, f.path(target+"/attributes"), "target-orig", 0o644)
				}
				swapped := false
				testHookAfterComponentLstat = func(_, prefix string) {
					if prefix != "gov" || swapped {
						return
					}
					swapped = true
					if err := os.Rename(f.path("gov"), f.path("gov.bak")); err != nil {
						t.Errorf("rename: %v", err)
					}
					f.link(t, "gov", target)
				}
				var err error
				switch opName {
				case "Create":
					var u *Undo
					u, err = f.tree.Create("gov/attributes", []byte("* merge=union\n"), 0o644)
					u.Release()
				case "Replace":
					err = f.tree.Replace("gov/attributes", []byte("* merge=union\n"))
				case "WriteFile":
					err = f.tree.WriteFile("gov/attributes", []byte("* merge=union\n"), 0o644)
				}
				if !swapped {
					t.Fatal("hook never ran")
				}
				var pe *PathError
				if !errors.As(err, &pe) || (pe.Reason != ReasonChanged && pe.Reason != ReasonSymlink) {
					t.Fatalf("want ReasonChanged/ReasonSymlink, got %v", err)
				}
				tp := f.path(target + "/attributes")
				if opName == "Create" {
					if exists(tp) {
						t.Fatalf("%s was created through the swapped component", tp)
					}
				} else if got := readString(t, tp); got != "target-orig" {
					t.Fatalf("%s was overwritten: %q", tp, got)
				}
				if got := readString(t, f.path("gov.bak/attributes")); got != "gov-orig" {
					t.Fatalf("checked original changed: %q", got)
				}
			})
		}
	}
}

// TestTree_ComponentSwappedAfterWalk documents the post-walk case (AC4
// leaf-hook row): once the walk holds a directory handle, swapping the
// directory's name for a link cannot redirect the write — it lands in the
// renamed, checked directory (or fails ENOENT), never in .git/ or at the
// link target.
func TestTree_ComponentSwappedAfterWalk(t *testing.T) {
	for _, target := range []string{"other", ".git/info"} {
		t.Run(target, func(t *testing.T) {
			resetHooks(t)
			f := newFixture(t)
			testHookBeforeLeafOp = func(op, _ string) {
				if op != "create" {
					return
				}
				if err := os.Rename(f.path("gov"), f.path("gov.bak")); err != nil {
					t.Errorf("rename: %v", err)
				}
				f.link(t, "gov", target)
			}
			u, err := f.tree.Create("gov/attributes", []byte("* merge=union\n"), 0o644)
			u.Release()
			if exists(f.path(target + "/attributes")) {
				t.Fatalf("write followed the post-walk swap into %s", target)
			}
			if err == nil && !exists(f.path("gov.bak/attributes")) {
				t.Fatal("Create succeeded but the file is not in the checked directory")
			}
		})
	}
}

// TestTree_RootConfinementWithoutWalk is AC5 (design AC3): with every walk
// check disabled, os.Root alone still keeps absolute and escaping links
// from reaching outside the root — including a link that stays inside the
// tree but leaves its parent directory, which the per-directory sub-root
// refuses. Every row asserts the side effect, not only the error.
//
// Mutation check: replacing the os.Root operations with
// os.OpenFile(filepath.Join(...)) makes the escaping rows write or read
// outside the root.
func TestTree_RootConfinementWithoutWalk(t *testing.T) {
	type row struct {
		name, link string
		target     func(f *fixture) string
		run        func(tr *Tree) error
		check      func(t *testing.T, err error)
	}
	escapes := func(t *testing.T, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "escapes") {
			t.Fatalf("want an os.Root \"path escapes\" error, got %v", err)
		}
	}
	isExist := func(t *testing.T, err error) {
		t.Helper()
		if !errors.Is(err, fs.ErrExist) {
			t.Fatalf("want fs.ErrExist from O_EXCL, got %v", err)
		}
	}
	noError := func(t *testing.T, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("want the link entry replaced/removed in place, got %v", err)
		}
	}
	outsideAbs := func(rel string) func(*fixture) string {
		return func(f *fixture) string { return filepath.Join(f.outside, rel) }
	}
	lit := func(s string) func(*fixture) string { return func(*fixture) string { return s } }
	create := func(rel string) func(*Tree) error {
		return func(tr *Tree) error { u, err := tr.Create(rel, []byte("evil"), 0o644); u.Release(); return err }
	}
	read := func(rel string) func(*Tree) error {
		return func(tr *Tree) error {
			b, err := tr.ReadFile(rel)
			if b != nil {
				return errors.New("read through link: " + string(b))
			}
			return err
		}
	}

	rows := []row{
		{"Create leaf dangling absolute", "gov/abs.md", outsideAbs("abs.md"), create("gov/abs.md"), isExist},
		{"Create leaf dangling relative escaping", "gov/rel.md", lit("../../outside/rel.md"), create("gov/rel.md"), isExist},
		{"Create leaf absolute to existing", "gov/v.md", outsideAbs("victim.md"), create("gov/v.md"), isExist},
		{"Create under absolute dir link", "outdir", func(f *fixture) string { return f.outside }, create("outdir/new.md"), escapes},
		{"Create under escaping relative dir link", "gov/up", lit("../../outside"), create("gov/up/new.md"), escapes},
		{"Create under in-tree link leaving its parent", "gov/sib", lit("../other"), create("gov/sib/new.md"), escapes},
		{"ReadFile leaf absolute", "gov/v.md", outsideAbs("victim.md"), read("gov/v.md"), escapes},
		{"ReadFile leaf relative escaping", "gov/v.md", lit("../../outside/victim.md"), read("gov/v.md"), escapes},
		{"ReadFile leaf in-tree leaving its parent", "gov/v.md", lit("../other/x.md"), read("gov/v.md"), escapes},
		{"ReadFile under absolute dir link", "outdir", func(f *fixture) string { return f.outside }, read("outdir/victim.md"), escapes},
		{"Replace leaf absolute", "gov/v.md", outsideAbs("victim.md"), func(tr *Tree) error { return tr.Replace("gov/v.md", []byte("evil")) }, noError},
		{"Replace under escaping dir link", "gov/up", lit("../../outside"), func(tr *Tree) error { return tr.Replace("gov/up/victim.md", []byte("evil")) }, escapes},
		{"WriteFile under absolute dir link", "outdir", func(f *fixture) string { return f.outside }, func(tr *Tree) error { return tr.WriteFile("outdir/victim.md", []byte("evil"), 0o644) }, escapes},
		{"Remove leaf absolute", "gov/v.md", outsideAbs("victim.md"), func(tr *Tree) error { return tr.Remove("gov/v.md") }, noError},
		{"Remove under escaping dir link", "gov/up", lit("../../outside"), func(tr *Tree) error { return tr.Remove("gov/up/victim.md") }, escapes},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			resetHooks(t)
			f := newFixture(t)
			f.link(t, r.link, r.target(f))
			outsideBefore := snapshot(t, f.outside)
			gitBefore := snapshot(t, f.path(".git"))
			otherBefore := snapshot(t, f.path("other"))

			testHookSkipComponentChecks = true
			err := r.run(f.tree)
			testHookSkipComponentChecks = false

			for _, d := range [][]string{
				diffSnapshots(outsideBefore, snapshot(t, f.outside)),
				diffSnapshots(gitBefore, snapshot(t, f.path(".git"))),
				diffSnapshots(otherBefore, snapshot(t, f.path("other"))),
			} {
				if len(d) != 0 {
					t.Fatalf("os.Root let the operation reach outside its directory: %v (err=%v)", d, err)
				}
			}
			r.check(t, err)
		})
	}
}

// TestTree_RootAloneFollowsInRootLinks pins why the walk exists (Decision
// 1): with the walk disabled, os.Root follows a tree-root link that stays
// inside the root — straight into .git/info. With the walk enabled the same
// call is refused (TestTree_RefusesSymlinkComponents covers it).
func TestTree_RootAloneFollowsInRootLinks(t *testing.T) {
	resetHooks(t)
	f := newFixture(t)
	f.link(t, "gitlink", ".git/info")
	testHookSkipComponentChecks = true
	u, err := f.tree.Create("gitlink/attributes", []byte("* merge=union\n"), 0o644)
	u.Release()
	testHookSkipComponentChecks = false
	if err != nil {
		t.Fatalf("expected os.Root alone to follow the in-root link, got %v", err)
	}
	if got := readString(t, f.path(".git/info/attributes")); got != "* merge=union\n" {
		t.Fatalf(".git/info/attributes = %q", got)
	}
}

// TestTree_CreateExistingLeaf is AC6(a).
func TestTree_CreateExistingLeaf(t *testing.T) {
	f := newFixture(t)
	past := time.Unix(1_000_000_000, 0)
	if err := os.Chtimes(f.path("gov/x.md"), past, past); err != nil {
		t.Fatal(err)
	}
	u, err := f.tree.Create("gov/x.md", []byte("new"), 0o644)
	if u != nil {
		t.Fatal("Create returned an Undo for an existing leaf")
	}
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("want fs.ErrExist, got %v", err)
	}
	if errors.Is(err, ErrUnsafePath) {
		t.Fatalf("an existing regular file is not an unsafe path: %v", err)
	}
	fi, _ := os.Stat(f.path("gov/x.md"))
	if got := readString(t, f.path("gov/x.md")); got != "x" || !fi.ModTime().Equal(past) {
		t.Fatalf("existing file touched: %q %v", got, fi.ModTime())
	}
}

func TestTree_CreateExistingDirectoryLeaf(t *testing.T) {
	f := newFixture(t)
	u, err := f.tree.Create("gov", []byte("new"), 0o644)
	u.Release()
	requireReason(t, err, ReasonNotRegular, "gov")
}

// TestTree_CreateLeafSwappedForLinkAfterCheck is AC6(b): a link to a file
// in the same directory swapped in at the leaf after the check is refused
// and its target is untouched.
//
// Mutation check: O_TRUNC instead of O_EXCL follows the same-directory
// link (os.Root allows it) and overwrites victim.md.
func TestTree_CreateLeafSwappedForLinkAfterCheck(t *testing.T) {
	resetHooks(t)
	f := newFixture(t)
	mustWrite(t, f.path("gov/victim.md"), "orig", 0o644)
	past := time.Unix(1_000_000_000, 0)
	if err := os.Chtimes(f.path("gov/victim.md"), past, past); err != nil {
		t.Fatal(err)
	}
	testHookBeforeLeafOp = func(op, _ string) {
		if op == "create" {
			f.link(t, "gov/new.md", "victim.md")
		}
	}
	u, err := f.tree.Create("gov/new.md", []byte("evil"), 0o644)
	u.Release()
	requireReason(t, err, ReasonSymlink, "gov/new.md")
	fi, _ := os.Stat(f.path("gov/victim.md"))
	if got := readString(t, f.path("gov/victim.md")); got != "orig" || !fi.ModTime().Equal(past) {
		t.Fatalf("link target written: %q mtime=%v", got, fi.ModTime())
	}
}

// TestTree_CreateWriteFailureRemovesLeafAndDirs is AC6(c).
//
// Mutation check: dropping the deferred Rollback in Create leaves pre/a,
// pre/a/b and the leaf behind.
func TestTree_CreateWriteFailureRemovesLeafAndDirs(t *testing.T) {
	resetHooks(t)
	f := newFixture(t)
	mustMkdir(t, f.path("pre"))
	injected := errors.New("injected write failure")
	testHookAfterLeafOpen = func(op, _ string) error {
		if op == "create" {
			return injected
		}
		return nil
	}
	u, err := f.tree.Create("pre/a/b/new.md", []byte("data"), 0o644)
	if u != nil || !errors.Is(err, injected) {
		t.Fatalf("want the injected failure and no Undo, got %v / %v", u, err)
	}
	for _, p := range []string{"pre/a/b/new.md", "pre/a/b", "pre/a"} {
		if exists(f.path(p)) {
			t.Errorf("%s left behind", p)
		}
	}
	if !exists(f.path("pre")) {
		t.Fatal("pre-existing directory removed")
	}
}

// TestTree_UndoRollback is AC6(d): Rollback removes exactly the created
// leaf and directories.
func TestTree_UndoRollback(t *testing.T) {
	t.Run("removes leaf and created dirs only", func(t *testing.T) {
		f := newFixture(t)
		u, err := f.tree.Create("gov/a/b/new.md", []byte("data"), 0o644)
		if err != nil {
			t.Fatal(err)
		}
		if got := readString(t, f.path("gov/a/b/new.md")); got != "data" {
			t.Fatalf("content = %q", got)
		}
		if err := u.Rollback(); err != nil {
			t.Fatalf("Rollback: %v", err)
		}
		for _, p := range []string{"gov/a/b/new.md", "gov/a/b", "gov/a"} {
			if exists(f.path(p)) {
				t.Errorf("%s left behind", p)
			}
		}
		if readString(t, f.path("gov/x.md")) != "x" {
			t.Fatal("sibling changed")
		}
	})
	t.Run("keeps a created dir that gained other content", func(t *testing.T) {
		f := newFixture(t)
		u, err := f.tree.Create("gov/a/new.md", []byte("data"), 0o644)
		if err != nil {
			t.Fatal(err)
		}
		mustWrite(t, f.path("gov/a/later.md"), "later", 0o644)
		if err := u.Rollback(); err != nil {
			t.Fatalf("Rollback: %v", err)
		}
		if exists(f.path("gov/a/new.md")) {
			t.Error("created leaf left behind")
		}
		if readString(t, f.path("gov/a/later.md")) != "later" {
			t.Error("unrelated file removed")
		}
	})
	// The replacement may get the same inode number (ext4, overlayfs and
	// tmpfs reuse a freed inode at once), so identity alone is not enough:
	// Rollback also matches the size and mtime of the version it wrote.
	for _, repl := range []struct{ name, data string }{
		{"different size", "someone else"},
		{"same size", "DATA"},
	} {
		t.Run("leaves a leaf that was replaced since/"+repl.name, func(t *testing.T) {
			f := newFixture(t)
			u, err := f.tree.Create("gov/new.md", []byte("data"), 0o644)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(f.path("gov/new.md")); err != nil {
				t.Fatal(err)
			}
			mustWrite(t, f.path("gov/new.md"), repl.data, 0o644)
			past := time.Unix(1_000_000_000, 0)
			if err := os.Chtimes(f.path("gov/new.md"), past, past); err != nil {
				t.Fatal(err)
			}
			if err := u.Rollback(); err != nil {
				t.Fatalf("Rollback: %v", err)
			}
			if readString(t, f.path("gov/new.md")) != repl.data {
				t.Fatal("Rollback removed a file it did not create")
			}
		})
	}
	t.Run("usable after Tree.Close", func(t *testing.T) {
		f := newFixture(t)
		u, err := f.tree.Create("gov/a/new.md", []byte("data"), 0o644)
		if err != nil {
			t.Fatal(err)
		}
		_ = f.tree.Close()
		if err := u.Rollback(); err != nil {
			t.Fatalf("Rollback after Close: %v", err)
		}
		if exists(f.path("gov/a")) {
			t.Fatal("created dir left behind")
		}
	})
}

// TestTree_ReplaceKeepsModeNoTemp is AC6(e).
func TestTree_ReplaceKeepsModeNoTemp(t *testing.T) {
	resetHooks(t)
	f := newFixture(t)
	mustWrite(t, f.path("gov/tool.sh"), "v1", 0o755)
	if err := f.tree.Replace("gov/tool.sh", []byte("v2")); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	fi, _ := os.Lstat(f.path("gov/tool.sh"))
	if readString(t, f.path("gov/tool.sh")) != "v2" || fi.Mode().Perm() != 0o755 || !fi.Mode().IsRegular() {
		t.Fatalf("content/mode not preserved: %v", fi.Mode())
	}
	requireNoTemp(t, f.path("gov"))

	injected := errors.New("injected")
	testHookAfterLeafOpen = func(op, _ string) error {
		if op == "replace" {
			return injected
		}
		return nil
	}
	if err := f.tree.Replace("gov/tool.sh", []byte("v3")); !errors.Is(err, injected) {
		t.Fatalf("want injected failure, got %v", err)
	}
	if readString(t, f.path("gov/tool.sh")) != "v2" {
		t.Fatal("failed Replace changed the file")
	}
	requireNoTemp(t, f.path("gov"))
}

func requireNoTemp(t *testing.T, dir string) {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".spine-tmp-") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

// TestTree_ReplaceLeafSwappedBeforeRename is AC6(f): a leaf swapped for a
// link before the rename ends as a regular file; the link target is
// untouched.
func TestTree_ReplaceLeafSwappedBeforeRename(t *testing.T) {
	resetHooks(t)
	f := newFixture(t)
	mustWrite(t, f.path("gov/victim.md"), "orig", 0o644)
	past := time.Unix(1_000_000_000, 0)
	if err := os.Chtimes(f.path("gov/victim.md"), past, past); err != nil {
		t.Fatal(err)
	}
	testHookBeforeLeafOp = func(op, _ string) {
		if op != "replace" {
			return
		}
		if err := os.Remove(f.path("gov/x.md")); err != nil {
			t.Error(err)
		}
		f.link(t, "gov/x.md", "victim.md")
	}
	if err := f.tree.Replace("gov/x.md", []byte("new")); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	fi, _ := os.Lstat(f.path("gov/x.md"))
	if !fi.Mode().IsRegular() || readString(t, f.path("gov/x.md")) != "new" {
		t.Fatalf("leaf is not the replaced regular file: %v", fi.Mode())
	}
	vfi, _ := os.Stat(f.path("gov/victim.md"))
	if readString(t, f.path("gov/victim.md")) != "orig" || !vfi.ModTime().Equal(past) {
		t.Fatal("link target written")
	}
}

func TestTree_BasicOperations(t *testing.T) {
	f := newFixture(t)

	if _, err := f.tree.Lstat("gov/missing.md"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Lstat missing leaf: %v", err)
	}
	if _, err := f.tree.Lstat("nodir/missing.md"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Lstat missing dir: %v", err)
	}
	if fi, err := f.tree.Lstat("gov/x.md"); err != nil || fi.Size() != 1 {
		t.Fatalf("Lstat: %v %v", fi, err)
	}
	if _, err := f.tree.Lstat("gov"); err == nil {
		t.Fatal("Lstat of a directory leaf must be refused")
	} else {
		requireReason(t, err, ReasonNotRegular, "gov")
	}
	if _, err := f.tree.Lstat("gov/x.md/y.md"); err == nil {
		t.Fatal("file used as a directory must be refused")
	} else {
		requireReason(t, err, ReasonNotDir, "gov/x.md")
	}
	if _, err := f.tree.ReadFile(".git/info/exclude"); err == nil {
		t.Fatal("ReadFile under .git must be refused")
	} else {
		requireReason(t, err, ReasonGitDir, ".git")
	}

	b, err := f.tree.ReadFile("gov/x.md")
	if err != nil || string(b) != "x" {
		t.Fatalf("ReadFile: %q %v", b, err)
	}
	if _, err := f.tree.ReadFile("gov/missing.md"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ReadFile missing: %v", err)
	}

	if err := f.tree.WriteFile("gov/new/w.md", []byte("w1"), 0o644); err != nil {
		t.Fatalf("WriteFile create: %v", err)
	}
	if err := f.tree.WriteFile("gov/new/w.md", []byte("w2"), 0o600); err != nil {
		t.Fatalf("WriteFile replace: %v", err)
	}
	fi, _ := os.Lstat(f.path("gov/new/w.md"))
	if readString(t, f.path("gov/new/w.md")) != "w2" || fi.Mode().Perm()&0o044 == 0 {
		t.Fatalf("WriteFile replace must keep the existing mode: %v", fi.Mode())
	}
	if err := f.tree.Replace("gov/missing.md", []byte("x")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Replace missing: %v", err)
	}

	if err := f.tree.Remove("gov/new/w.md"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if exists(f.path("gov/new/w.md")) {
		t.Fatal("Remove left the file")
	}
	if err := f.tree.Remove("gov/new/w.md"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Remove missing: %v", err)
	}
	if err := f.tree.Remove("gov"); err == nil {
		t.Fatal("Remove of a directory must be refused")
	}

	if err := f.tree.RemoveEmptyDir("gov"); err == nil {
		t.Fatal("RemoveEmptyDir on a non-empty dir must fail")
	}
	if err := f.tree.RemoveEmptyDir("gov/new"); err != nil {
		t.Fatalf("RemoveEmptyDir: %v", err)
	}
	if exists(f.path("gov/new")) {
		t.Fatal("RemoveEmptyDir left the directory")
	}
	if err := f.tree.RemoveEmptyDir("gov/x.md"); err == nil {
		t.Fatal("RemoveEmptyDir on a file must be refused")
	}
	f.link(t, "emptylink", "realdir")
	if err := f.tree.RemoveEmptyDir("emptylink"); err == nil {
		t.Fatal("RemoveEmptyDir on a link must be refused")
	} else {
		requireReason(t, err, ReasonSymlink, "emptylink")
	}

	_ = f.tree.Close()
	_ = f.tree.Close() // idempotent
	if _, err := f.tree.ReadFile("gov/x.md"); err == nil {
		t.Fatal("use after Close must fail")
	}
}

func TestTree_ErrorsAreRepoRelative(t *testing.T) {
	f := newFixture(t)
	f.link(t, "gov/abs.md", filepath.Join(f.outside, "abs.md"))
	for name, err := range map[string]error{
		"missing": func() error { _, err := f.tree.ReadFile("gov/missing.md"); return err }(),
		"symlink": func() error { _, err := f.tree.ReadFile("gov/abs.md"); return err }(),
		"notdir":  func() error { _, err := f.tree.ReadFile("gov/x.md/y"); return err }(),
	} {
		if err == nil {
			t.Fatalf("%s: expected an error", name)
		}
		if strings.Contains(err.Error(), f.base) {
			t.Errorf("%s: error leaks an absolute path: %q", name, err.Error())
		}
	}
}

// TestTree_WalkRegularFiles: symlinks, non-regular entries and every
// git-dir spelling are never yielded or entered (I3), and the order is
// lexical.
func TestTree_WalkRegularFiles(t *testing.T) {
	f := newFixture(t)
	mustMkdir(t, f.path("gov/sub/.GIT"))
	mustWrite(t, f.path("gov/sub/.GIT/hidden.md"), "h", 0o644)
	mustWrite(t, f.path("gov/sub/b.md"), "b", 0o644)
	mustWrite(t, f.path("gov/a.md"), "a", 0o644)
	f.link(t, "gov/link.md", "x.md")
	f.link(t, "gov/linkdir", "../realdir")
	f.link(t, "gov/out", f.outside)

	var got []string
	if err := f.tree.WalkRegularFiles("gov", func(rel string) error {
		got = append(got, rel)
		return nil
	}); err != nil {
		t.Fatalf("WalkRegularFiles: %v", err)
	}
	want := []string{"gov/a.md", "gov/sub/b.md", "gov/x.md"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", got, want)
	}

	got = nil
	if err := f.tree.WalkRegularFiles("", func(rel string) error {
		got = append(got, rel)
		return nil
	}); err != nil {
		t.Fatalf("WalkRegularFiles(root): %v", err)
	}
	// .git/info/exclude, gov/sub/.GIT/hidden.md and every link are absent.
	want = []string{"gov/a.md", "gov/sub/b.md", "gov/x.md", "other/x.md", "realdir/x.md"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("root walk got %v, want %v", got, want)
	}

	stop := errors.New("stop")
	n := 0
	if err := f.tree.WalkRegularFiles("gov", func(string) error { n++; return stop }); !errors.Is(err, stop) || n != 1 {
		t.Fatalf("fn error must stop the walk: %v after %d", err, n)
	}
	if err := f.tree.WalkRegularFiles("gov/linkdir", func(string) error { return nil }); err == nil {
		t.Fatal("walking from a symlinked dir must be refused")
	}
	if err := f.tree.WalkRegularFiles("nope", func(string) error { return nil }); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing dir: %v", err)
	}
	if err := f.tree.WalkRegularFiles(".git", func(string) error { return nil }); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("walking .git must be refused: %v", err)
	}
}

// TestTree_ReadFileOverLimit is AC15(b).
//
// Mutation checks: dropping the Lstat size pre-check makes the first case
// read (and allocate) the whole file; dropping the io.LimitReader makes the
// growth case read past limit+1.
func TestTree_ReadFileOverLimit(t *testing.T) {
	t.Run("refused before reading", func(t *testing.T) {
		f := newFixture(t)
		if err := os.WriteFile(f.path("gov/big.md"), make([]byte, DefaultMaxReadBytes+1), 0o644); err != nil {
			t.Fatal(err)
		}
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		b, err := f.tree.ReadFile("gov/big.md")
		runtime.ReadMemStats(&after)
		var se *SizeError
		if b != nil || !errors.As(err, &se) || !errors.Is(err, ErrTooLarge) || errors.Is(err, ErrUnsafePath) {
			t.Fatalf("want *SizeError, got %v (%d bytes)", err, len(b))
		}
		if se.Limit != DefaultMaxReadBytes || se.Size != DefaultMaxReadBytes+1 || se.Path != "gov/big.md" {
			t.Fatalf("SizeError = %+v", se)
		}
		if grew := after.TotalAlloc - before.TotalAlloc; grew >= 1<<20 {
			t.Fatalf("refusal allocated %d bytes; the file must not be read", grew)
		}
	})
	t.Run("exactly at the limit is read", func(t *testing.T) {
		f := newFixture(t)
		mustWrite(t, f.path("gov/edge.md"), strings.Repeat("e", 1024), 0o644)
		b, err := f.tree.ReadFileLimit("gov/edge.md", 1024)
		if err != nil || len(b) != 1024 {
			t.Fatalf("got %d bytes, %v", len(b), err)
		}
	})
	t.Run("growth after the check is capped", func(t *testing.T) {
		resetHooks(t)
		f := newFixture(t)
		mustWrite(t, f.path("gov/grow.md"), strings.Repeat("g", 100), 0o644)
		testHookBeforeLeafOp = func(op, _ string) {
			if op == "read" {
				mustWrite(t, f.path("gov/grow.md"), strings.Repeat("g", 100_000), 0o644)
			}
		}
		var observed int64 = -1
		testHookObserveRead = func(_ string, n int64) { observed = n }
		const limit = 1024
		b, err := f.tree.ReadFileLimit("gov/grow.md", limit)
		var se *SizeError
		if b != nil || !errors.As(err, &se) {
			t.Fatalf("want *SizeError, got %v", err)
		}
		if observed != limit+1 {
			t.Fatalf("read %d bytes from a growing file, want exactly limit+1 = %d", observed, limit+1)
		}
	})
	// math.MaxInt64 ("no cap") must not overflow limit+1 into a negative
	// LimitReader bound, which reads nothing and returns empty content.
	t.Run("math.MaxInt64 limit reads the whole file", func(t *testing.T) {
		f := newFixture(t)
		for _, limit := range []int64{math.MaxInt64, math.MaxInt64 - 1} {
			b, err := f.tree.ReadFileLimit("gov/x.md", limit)
			if err != nil || string(b) != "x" {
				t.Fatalf("ReadFileLimit(limit=%d) = %q, %v; want \"x\"", limit, b, err)
			}
		}
	})
	t.Run("negative limit admits only empty files", func(t *testing.T) {
		f := newFixture(t)
		if _, err := f.tree.ReadFileLimit("gov/x.md", -5); !errors.Is(err, ErrTooLarge) {
			t.Fatalf("want ErrTooLarge, got %v", err)
		}
	})
}

// TestTree_NoHandleLeak is AC16 (I8): 1 000 mixed operations — successful
// and refused Create, Replace, ReadFile, Remove, Rollback then Release —
// leave the package's open-handle counter (and, on Linux, the process's
// descriptor count) where it started; Rollback then Release and a double
// Release close nothing twice (the counter would go below the baseline).
func TestTree_NoHandleLeak(t *testing.T) {
	f := newFixture(t)
	f.link(t, "gov/link.md", "x.md")
	f.link(t, "linkdir", "realdir")
	baseline := openHandles.Load()
	fdBefore := countFDs()

	for i := 0; i < 1000; i++ {
		switch i % 10 {
		case 0:
			u, err := f.tree.Create("gov/n/new.md", []byte("n"), 0o644)
			if err != nil {
				t.Fatal(err)
			}
			if err := u.Rollback(); err != nil {
				t.Fatal(err)
			}
			u.Release()
		case 1:
			u, err := f.tree.Create("gov/r.md", []byte("r"), 0o644)
			if err != nil {
				t.Fatal(err)
			}
			u.Release()
			u.Release()
			if err := f.tree.Remove("gov/r.md"); err != nil {
				t.Fatal(err)
			}
		case 2:
			if _, err := f.tree.Create("gov/link.md", []byte("x"), 0o644); err == nil {
				t.Fatal("refusal expected")
			}
		case 3:
			if _, err := f.tree.Create("linkdir/a/b.md", []byte("x"), 0o644); err == nil {
				t.Fatal("refusal expected")
			}
		case 4:
			if err := f.tree.Replace("gov/x.md", []byte("x")); err != nil {
				t.Fatal(err)
			}
		case 5:
			if err := f.tree.Replace("gov/link.md", []byte("x")); err == nil {
				t.Fatal("refusal expected")
			}
		case 6:
			if _, err := f.tree.ReadFile("gov/x.md"); err != nil {
				t.Fatal(err)
			}
		case 7:
			if _, err := f.tree.ReadFile("linkdir/x.md"); err == nil {
				t.Fatal("refusal expected")
			}
		case 8:
			if err := f.tree.Remove("gov/missing.md"); err == nil {
				t.Fatal("refusal expected")
			}
		case 9:
			_ = f.tree.WalkRegularFiles("", func(rel string) error {
				_, err := f.tree.ReadFile(rel)
				return err
			})
		}
	}
	var nilUndo *Undo
	nilUndo.Release()
	_ = nilUndo.Rollback()

	if got := openHandles.Load(); got != baseline {
		t.Fatalf("open handles: %d, want baseline %d", got, baseline)
	}
	if fdBefore >= 0 {
		if fdAfter := countFDs(); fdAfter != fdBefore {
			t.Fatalf("/proc/self/fd: %d descriptors, want %d", fdAfter, fdBefore)
		}
	}
}

// countFDs returns the process's open descriptor count on Linux, or -1
// where /proc/self/fd is unavailable.
func countFDs() int {
	if runtime.GOOS != "linux" {
		return -1
	}
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	return len(ents)
}

// TestTree_ReadLeafSwappedForLinkAfterCheck pins ReadFileLimit's post-open
// identity check: a leaf swapped, after its Lstat, for a same-directory
// link (which os.Root follows for O_RDONLY) yields no bytes.
//
// Mutation check: dropping the os.SameFile(lfi, ffi) comparison returns
// the link target's bytes ("secret").
func TestTree_ReadLeafSwappedForLinkAfterCheck(t *testing.T) {
	resetHooks(t)
	f := newFixture(t)
	mustWrite(t, f.path("gov/secret.md"), "secret", 0o644)
	swapped := false
	testHookBeforeLeafOp = func(op, _ string) {
		if op != "read" || swapped {
			return
		}
		swapped = true
		if err := os.Remove(f.path("gov/x.md")); err != nil {
			t.Error(err)
		}
		f.link(t, "gov/x.md", "secret.md")
	}
	b, err := f.tree.ReadFile("gov/x.md")
	if !swapped {
		t.Fatal("hook never ran")
	}
	if b != nil {
		t.Fatalf("ReadFile returned bytes through a swapped-in link: %q", b)
	}
	requireReason(t, err, ReasonChanged, "gov/x.md")
}

// TestTree_WalkSubdirSwappedForLinkMidWalk pins WalkRegularFiles' per-
// directory binding: a subdirectory swapped for an in-root link (to
// .git/info) between its Lstat and its OpenRoot is refused, and nothing
// under the link target is yielded.
//
// Mutation check: dropping the os.SameFile(fi, nfi) comparison in walkDir
// yields "gov/exclude" (.git/info/exclude) and returns no error.
func TestTree_WalkSubdirSwappedForLinkMidWalk(t *testing.T) {
	resetHooks(t)
	f := newFixture(t)
	swapped := false
	testHookAfterComponentLstat = func(op, prefix string) {
		if op != "walk" || prefix != "gov" || swapped {
			return
		}
		swapped = true
		if err := os.Rename(f.path("gov"), f.path("gov.bak")); err != nil {
			t.Error(err)
		}
		f.link(t, "gov", ".git/info")
	}
	var got []string
	err := f.tree.WalkRegularFiles("", func(rel string) error {
		got = append(got, rel)
		return nil
	})
	if !swapped {
		t.Fatal("hook never ran")
	}
	for _, rel := range got {
		if strings.HasPrefix(rel, "gov/") {
			t.Errorf("yielded %s through the swapped-in link", rel)
		}
	}
	requireReason(t, err, ReasonChanged, "gov")
}
