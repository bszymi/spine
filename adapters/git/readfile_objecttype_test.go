package git_test

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
)

// INIT-023 wave 4 follow-up, adversarial review of the follow-up (P1,
// P2): a file entry's MODE does not make the object it records a blob.
// `git update-index --cacheinfo 100644,<id>,<path>` (or mktree, or any
// tool that writes trees) records whatever id it is given, and a push
// with the default receive config checks only that the object exists.
// ReadFile typed the entry from its mode (ls-tree) and then read the id
// with `git cat-file blob <oid>`, which DEREFERENCES to a blob:
//
//   - an annotated tag whose target is a blob was peeled, so the read
//     returned that blob's bytes cleanly — content no diff shows (git diff
//     shows the tag object's own text: "object <id>\ntype blob\ntag …") —
//     and a reviewed workflow or the branch-protection rules could be
//     replaced invisibly (P1);
//   - a tree, a commit, or a tag whose target is one of those made cat-file
//     exit 128 "bad file", a plain Permanent error that the projection then
//     classified as infrastructure (TreeEntryType says "blob" from the
//     mode), holding the sync until someone rewrote the file — a plantable
//     wedge (P2).
//
// ReadFile now checks the recorded object's own type (`git cat-file -t
// <oid>`) before it reads or sizes anything, and refuses every type but
// blob as the not-a-file refusal with FileEntry set.

// objectTypeRepo commits a regular file and then, in a second commit,
// file entries (100644, 100755 and the symlink mode 120000, which ls-tree
// also prints as "blob") recording objects that are not blobs. It returns
// the repository, both commits and the hidden body the peeled tags point
// at.
func objectTypeRepo(t *testing.T) (dir, c1, c2, hidden string) {
	t.Helper()
	dir = t.TempDir()
	t3git(t, dir, nil, "init", "-q", "-b", "main")
	hidden = "id: hidden\nsteps: []\n"
	t3git(t, dir, nil, "update-index", "--add", "--cacheinfo",
		"100644,"+t3git(t, dir, []byte("visible\n"), "hash-object", "-w", "--stdin")+",visible.md")
	c1 = t3git(t, dir, nil, "commit-tree", "-m", "c1", t3git(t, dir, nil, "write-tree"))
	t3git(t, dir, nil, "update-ref", "refs/heads/main", c1)

	blob := t3git(t, dir, []byte(hidden), "hash-object", "-w", "--stdin")
	tree := t3git(t, dir, nil, "rev-parse", c1+"^{tree}")
	mktag := func(name, target, typ string) string {
		body := fmt.Sprintf("object %s\ntype %s\ntag %s\ntagger t <t@t> 0 +0000\n\nm\n", target, typ, name)
		return t3git(t, dir, []byte(body), "mktag")
	}
	tagBlob := mktag("v1", blob, "blob")
	entries := []struct{ mode, id, path string }{
		{"100644", tagBlob, "tag.md"},
		{"100755", tagBlob, "tag-exec.sh"},
		{"120000", tagBlob, "tag-link"},
		{"100644", mktag("v2", tagBlob, "tag"), "tag-of-tag.md"},
		{"100644", mktag("v3", tree, "tree"), "tag-tree.md"},
		{"100644", mktag("v4", c1, "commit"), "tag-commit.md"},
		{"100644", tree, "tree.md"},
		{"100644", c1, "commit.md"},
		{"100644", tree, "d/nested-tree.md"},
	}
	for _, e := range entries {
		t3git(t, dir, nil, "update-index", "--add", "--cacheinfo", e.mode+","+e.id+","+e.path)
	}
	c2 = t3git(t, dir, nil, "commit-tree", "-p", c1, "-m", "c2", t3git(t, dir, nil, "write-tree"))
	t3git(t, dir, nil, "update-ref", "refs/heads/main", c2)
	return dir, c1, c2, hidden
}

func TestReadFile_FileEntryRecordingANonBlobObjectIsRefused(t *testing.T) {
	ctx := context.Background()
	dir, c1, c2, hidden := objectTypeRepo(t)
	c := git.NewCLIClient(dir)

	// Preconditions: what the follow-up's `cat-file blob <oid>` did with
	// these entries, and what a reviewer sees.
	tagID := t3git(t, dir, nil, "rev-parse", c2+":tag.md")
	if got := t3git(t, dir, nil, "cat-file", "blob", tagID); got+"\n" != hidden {
		t.Fatalf("fixture: cat-file blob of the tag no longer peels to the hidden body: %q", got)
	}
	if d := t3git(t, dir, nil, "diff", c1, c2, "--", "tag.md"); !strings.Contains(d, "+object ") || strings.Contains(d, "hidden") {
		t.Fatalf("fixture: the diff must show the tag object's text, not the hidden body:\n%s", d)
	}
	if out, ok := gitCatFileBlob(t, dir, t3git(t, dir, nil, "rev-parse", c2+":tree.md")); ok {
		t.Fatalf("fixture: cat-file blob of a tree id succeeded: %q", out)
	}

	for _, tc := range []struct{ path, typ string }{
		{"tag.md", git.ObjectTag},
		{"tag-exec.sh", git.ObjectTag},
		{"tag-link", git.ObjectTag},
		{"tag-of-tag.md", git.ObjectTag},
		{"tag-tree.md", git.ObjectTag},
		{"tag-commit.md", git.ObjectTag},
		{"tree.md", git.TreeEntryTree},
		{"commit.md", git.TreeEntryCommit},
		{"d/nested-tree.md", git.TreeEntryTree},
	} {
		for _, ref := range []string{c2, "main", "HEAD"} {
			// The mode says file: TreeEntryType, which reads only trees,
			// still answers "blob" (the projection's pre-read typing
			// cannot see the object); ReadFile is the check.
			if typ, err := c.TreeEntryType(ctx, ref, tc.path); err != nil || typ != git.TreeEntryBlob {
				t.Fatalf("fixture: TreeEntryType(%s, %s) = %q, %v; want blob (from the mode)", ref, tc.path, typ, err)
			}
			got, err := c.ReadFile(ctx, ref, tc.path)
			label := ref + ":" + tc.path
			assertNotBlobObject(t, label, got, err, ref, tc.path, tc.typ, true)
			if strings.Contains(string(got), "hidden") {
				t.Fatalf("%s: the peeled tag's hidden body was read", label)
			}
		}
	}

	// A regular file is still read, byte for byte.
	if got, err := c.ReadFile(ctx, c2, "visible.md"); err != nil || string(got) != "visible\n" {
		t.Fatalf("ReadFile(visible.md) = %q, %v", got, err)
	}
}

// gitCatFileBlob runs the follow-up's content read, `git cat-file blob
// <oid>`, and reports its stdout and whether it succeeded.
func gitCatFileBlob(t *testing.T, dir, oid string) (string, bool) {
	t.Helper()
	cmd := exec.Command("git", "cat-file", "blob", oid)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err == nil
}
