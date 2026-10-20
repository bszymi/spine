package git_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
)

// INIT-023 wave 4 follow-up (TASK-003 residual, found by SMP
// INIT-027/EPIC-004/TASK-008 at ae513dca): ReadFile reads blobs only.
//
// TASK-003 read content with `git show [eoo] <rev>:<path> --`, which
// renders whatever git's revision parser makes of the spec. A gitlink
// whose commit is in the repository came back as that commit with its
// patch, a directory as its listing, and a path holding ".." or a "^"
// suffix as a range or a revision with its parents — so a missing
// "d..main" read as `git log`, and an existing file of that name was
// rendered as the range instead of read.

// blobOnlyRepo builds the fixture: a directory d/, a file literally named
// "d..main" next to it (and a branch main), files named "a^b" and "x@{1}",
// a directory s/ holding s/d/ but no "s/d..main", "s/a^b" or "s/x@{1}", a
// gitlink g.md to a commit that IS in the repository (one that adds a
// file, so `git show` would print a patch), and a gitlink b.md recording
// the id of a blob in the repository.
func blobOnlyRepo(t *testing.T) (dir, head, linked string) {
	t.Helper()
	dir = t.TempDir()
	t3git(t, dir, nil, "init", "-q", "-b", "main")
	for name, body := range map[string]string{
		"d/x.md":     "inside d\n",
		"d..main":    "a file literally named d..main\n",
		"a^b":        "a file literally named a^b\n",
		"x@{1}":      "a file literally named x@{1}\n",
		"s/d/y.md":   "inside s/d\n",
		"payload.md": "---\nid: TASK-9\ntype: Task\ntitle: hidden\nstatus: Completed\n---\n",
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t3git(t, dir, nil, "add", "-A")
	t3git(t, dir, nil, "commit", "-q", "-m", "files")
	linked = t3git(t, dir, nil, "rev-parse", "HEAD")
	payload := t3git(t, dir, nil, "rev-parse", "HEAD:payload.md")
	t3git(t, dir, nil, "update-index", "--add", "--cacheinfo", "160000,"+linked+",g.md")
	t3git(t, dir, nil, "update-index", "--add", "--cacheinfo", "160000,"+payload+",b.md")
	t3git(t, dir, nil, "commit", "-q", "-m", "gitlinks")
	head = t3git(t, dir, nil, "rev-parse", "HEAD")
	return dir, head, linked
}

// Item 1: a gitlink to a commit that exists is refused before anything is
// read — `git show` printed the commit with its patch — as is a gitlink
// recording the id of a blob in the repository (show printed the blob:
// content no diff shows) and a directory (show printed its listing).
func TestReadFile_NonBlobEntriesAreRefusedUnread(t *testing.T) {
	ctx := context.Background()
	dir, head, linked := blobOnlyRepo(t)
	c := git.NewCLIClient(dir)

	// Precondition: what TASK-003's read returned for these paths.
	if out := t3git(t, dir, nil, "show", "--end-of-options", head+":g.md", "--"); !strings.Contains(out, "commit "+linked) || !strings.Contains(out, "+inside d") {
		t.Fatalf("fixture: git show of the gitlink no longer renders its commit with a patch:\n%s", out)
	}

	for _, tc := range []struct{ label, ref, path, typ string }{
		{"gitlink to a commit in the repository", head, "g.md", git.TreeEntryCommit},
		{"gitlink at a branch name", "main", "g.md", git.TreeEntryCommit},
		{"gitlink recording a blob id", head, "b.md", git.TreeEntryCommit},
		{"directory", head, "d", git.TreeEntryTree},
		{"nested directory", head, "s/d", git.TreeEntryTree},
	} {
		got, err := c.ReadFile(ctx, tc.ref, tc.path)
		assertNotBlob(t, tc.label, got, err, tc.ref, tc.path, tc.typ)
	}

	// A file is still read, byte for byte.
	got, err := c.ReadFile(ctx, head, "d/x.md")
	if err != nil || string(got) != "inside d\n" {
		t.Fatalf("ReadFile(d/x.md) = %q, %v", got, err)
	}
}

// Item 2: a path holding ".." inside a component, a "^" suffix or "@{"
// never reaches git's revision parser. A missing one is the miss —
// ErrKindNotFound with "bad revision" text — and an existing file of that
// name reads as itself.
func TestReadFile_RevisionSyntaxInThePathIsLiteral(t *testing.T) {
	ctx := context.Background()
	dir, head, _ := blobOnlyRepo(t)
	c := git.NewCLIClient(dir)

	// Precondition: TASK-003's `git show` read these as revision
	// expressions — a range (s/d is a tree, main a branch) and the
	// gitlink's commit with its parents ("^!") or none ("^@").
	for _, spec := range []string{head + ":s/d..main", head + ":g.md^!"} {
		if out := t3git(t, dir, nil, "show", "--end-of-options", spec, "--"); !strings.Contains(out, "commit ") {
			t.Fatalf("fixture: git show %s no longer renders a revision:\n%s", spec, out)
		}
	}

	for _, p := range []string{"s/d..main", "s/a^b", "s/x@{1}", "g.md^!", "g.md^@", "g.md^-", "d...main", "nope..main"} {
		got, err := c.ReadFile(ctx, head, p)
		var ge *git.GitError
		if !errors.As(err, &ge) || ge.Kind != git.ErrKindNotFound || ge.Op != "read_file" {
			t.Errorf("ReadFile(%q) = %q, %v; want ErrKindNotFound", p, truncateForTest(got), err)
			continue
		}
		if got != nil {
			t.Errorf("ReadFile(%q) returned content %q with its miss", p, truncateForTest(got))
		}
		if want := "fatal: bad revision '" + head + ":" + p + "'"; ge.Message != want {
			t.Errorf("ReadFile(%q): Message = %q, want git's %q", p, ge.Message, want)
		}
	}

	for name, want := range map[string]string{
		"d..main": "a file literally named d..main\n",
		"a^b":     "a file literally named a^b\n",
		"x@{1}":   "a file literally named x@{1}\n",
	} {
		for _, ref := range []string{head, "main", "HEAD"} {
			got, err := c.ReadFile(ctx, ref, name)
			if err != nil || string(got) != want {
				t.Errorf("ReadFile(%s, %q) = %q, %v; want the file's own bytes", ref, name, truncateForTest(got), err)
			}
		}
	}
}

// A path no tree entry can have is a miss without any lookup — git took
// these literally and found nothing ("bad revision"), as it did for a
// file named with a trailing "/".
func TestReadFile_PathsNoEntryCanHaveAreMisses(t *testing.T) {
	ctx := context.Background()
	dir, head, _ := blobOnlyRepo(t)
	c := git.NewCLIClient(dir)
	for _, p := range []string{"", "/", "d/./x.md", "/d/x.md", "d//x.md", "s/../d/x.md", "..", ".", "d/x.md/", "./d/x.md/", "d/x.md\x00"} {
		// ("..": git show read `<rev>:` .. `` as a range and printed a log.)
		if p != "" && p != ".." && p != "d/x.md\x00" {
			if out, ok := gitShow(t, dir, head+":"+p); ok {
				t.Fatalf("fixture: git show %q found %q", p, truncateForTest([]byte(out)))
			}
		}
		got, err := c.ReadFile(ctx, head, p)
		var ge *git.GitError
		if !errors.As(err, &ge) || ge.Kind != git.ErrKindNotFound || got != nil {
			t.Errorf("ReadFile(%q) = %q, %v; want an ErrKindNotFound miss", p, truncateForTest(got), err)
		}
	}
}

// gitShow runs TASK-003's read, `git show [eoo] <spec> --`, and reports
// its stdout and whether it succeeded.
func gitShow(t *testing.T, dir, spec string) (string, bool) {
	t.Helper()
	cmd := exec.Command("git", "show", "--end-of-options", spec, "--")
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err == nil
}

// Codex review (wave 4 follow-up): the path is the one `git show` looked
// up. A "./" or "../" prefix is relative to the repository root, so
// "./d/x.md" still reads d/x.md — byte for byte what show printed — and
// artifact reads and run starts given such a path keep working; a path
// that climbs out of the repository is still git's Permanent "outside
// repository" error; a trailing "/" names only a directory, which is now
// the not-a-file refusal instead of a listing.
func TestReadFile_PathsResolveLikeGitShow(t *testing.T) {
	ctx := context.Background()
	dir, head, _ := blobOnlyRepo(t)
	c := git.NewCLIClient(dir)
	for _, p := range []string{"./d/x.md", "././d/x.md", "./d/../d/x.md", "./d//x.md", "./s/../d/x.md", "./a^b", "./x@{1}"} {
		want, ok := gitShow(t, dir, head+":"+p)
		if !ok {
			t.Fatalf("fixture: git show %q failed", p)
		}
		got, err := c.ReadFile(ctx, head, p)
		if err != nil || string(got) != want {
			t.Errorf("ReadFile(%q) = %q, %v; want git show's %q", p, truncateForTest(got), err, want)
		}
	}
	for _, tc := range []struct{ path, typ string }{
		{"d/", git.TreeEntryTree}, {"s/d/", git.TreeEntryTree}, {"./d/", git.TreeEntryTree},
		{"./", git.TreeEntryTree}, {"./g.md", git.TreeEntryCommit},
	} {
		got, err := c.ReadFile(ctx, head, tc.path)
		assertNotBlob(t, tc.path, got, err, head, tc.path, tc.typ)
	}
	for _, p := range []string{"../d/x.md", ".././d/x.md", "./d/../../x", "./.."} {
		got, err := c.ReadFile(ctx, head, p)
		var ge *git.GitError
		if !errors.As(err, &ge) || ge.Kind != git.ErrKindPermanent || errors.Is(err, git.ErrNotBlob) || got != nil ||
			!strings.Contains(ge.Message, "outside repository") {
			t.Errorf("ReadFile(%q) = %q, %v; want git's Permanent \"outside repository\" refusal", p, truncateForTest(got), err)
		}
	}
	// "./" at a revision that names no tree is a miss.
	if _, err := c.ReadFile(ctx, strings.Repeat("cd", 20), "./"); err == nil {
		t.Fatal("ReadFile(<absent commit>, \"./\") succeeded")
	} else if ge, ok := err.(*git.GitError); !ok || ge.Kind != git.ErrKindNotFound {
		t.Fatalf("ReadFile(<absent commit>, \"./\") = %v, want ErrKindNotFound", err)
	}
}

// A miss is proved, never inferred: when git cannot run at all the answer
// is an error, not ErrKindNotFound (an "absent" file would let a caller
// treat a governed path as free).
func TestReadFile_GitThatCannotRunIsNotAMiss(t *testing.T) {
	ctx := context.Background()
	dir, head, _ := blobOnlyRepo(t)
	c := git.NewCLIClient(dir)
	t.Setenv("PATH", t.TempDir())
	_, err := c.ReadFile(ctx, head, "d/x.md")
	var ge *git.GitError
	if err == nil || (errors.As(err, &ge) && ge.Kind == git.ErrKindNotFound) {
		t.Fatalf("ReadFile with no git binary = %v, want a failure that is not a miss", err)
	}
}
