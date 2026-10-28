package git_test

// INIT-024 TASK-004 review round 1 (P4-3): the end-of-stream checks run in
// ListFiles, Diff and CountChangedPaths themselves — runListing's verdict
// on output that ends inside a record, and Diff's check for a record cut
// short — not only in the collectors the shims feed. A fake `git` on PATH
// prints crafted output for the listing command (every argument is an
// object id, so no lookup runs), and exits 0 as git would.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
)

const fakeOID = "0123456789abcdef0123456789abcdef01234567"

// installFakeGit puts a `git` on PATH that prints the file $FAKE_GIT_OUT.
func installFakeGit(t *testing.T) func(out string) {
	t.Helper()
	bin := t.TempDir()
	script := "#!/bin/sh\ncat \"$FAKE_GIT_OUT\"\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil { //nolint:gosec // G306: the fake git must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	outFile := filepath.Join(t.TempDir(), "out")
	t.Setenv("FAKE_GIT_OUT", outFile)
	return func(out string) {
		if err := os.WriteFile(outFile, []byte(out), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestListings_EndOfStreamThroughTheRealRunner(t *testing.T) {
	ctx := context.Background()
	set := installFakeGit(t)
	c := git.NewCLIClient(t.TempDir())
	wantMessage := func(label string, err error, msg string) {
		t.Helper()
		var ge *git.GitError
		if !errors.As(err, &ge) || ge.Kind != git.ErrKindPermanent || !strings.Contains(ge.Error(), msg) {
			t.Errorf("%s: %v, want a permanent %q", label, err, msg)
		}
	}

	set("a.md\x00b.md\x00")
	if got, err := c.ListFiles(ctx, fakeOID, ""); err != nil || !reflect.DeepEqual(got, []string{"a.md", "b.md"}) {
		t.Fatalf("control ListFiles = %q, %v", got, err)
	}
	set("a.md\x00b.md")
	_, err := c.ListFiles(ctx, fakeOID, "")
	wantMessage("ListFiles of output ending inside a record", err, "unterminated `git ls-tree -z` output")
	_, err = c.ListFilesFunc(ctx, fakeOID, func(p string) bool { return false })
	wantMessage("ListFilesFunc (dropping every path) of the same", err, "unterminated `git ls-tree -z` output")

	set("M\x00a.md\x00")
	if got, err := c.Diff(ctx, fakeOID, fakeOID); err != nil || len(got) != 1 {
		t.Fatalf("control Diff = %+v, %v", got, err)
	}
	set("M\x00a.md\x00R100\x00old\x00")
	_, err = c.Diff(ctx, fakeOID, fakeOID)
	wantMessage("Diff of a last record cut short", err, "unparseable diff output")
	if err != nil && !strings.Contains(errors.Unwrap(err).Error(), "missing a path") {
		t.Errorf("Diff of a last record cut short: %v, want \"missing a path\"", err)
	}
	set("M\x00a.md\x00M\x00b")
	_, err = c.Diff(ctx, fakeOID, fakeOID)
	wantMessage("Diff of output ending inside a record", err, "unparseable diff output")

	set("a\x00b\x00")
	if n, err := c.CountChangedPaths(ctx, fakeOID, fakeOID); err != nil || n != 2 {
		t.Fatalf("control CountChangedPaths = %d, %v", n, err)
	}
	set("a\x00b")
	_, err = c.CountChangedPaths(ctx, fakeOID, fakeOID)
	wantMessage("CountChangedPaths of output ending inside a record", err, "unterminated `git diff -z` output")
}
