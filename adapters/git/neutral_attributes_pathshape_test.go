package git_test

// INIT-024 integration A: a renormalisation carries every path's bytes as
// they are — git's `status -z` records, the marker's NUL-terminated record
// and `checkout-index -z --stdin`. Phase 3 found the fingerprints' input
// to `hash-object --stdin-paths` misread a leading quote and a trailing CR
// (and could not carry a newline); phase 4 removed the fingerprints and
// hash-object altogether. For each shape, a converted-only file goes to its
// stored bytes and a really edited one of the same shape is recorded and
// kept, in one call and in a quarantined retry.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/testutil"
)

// rpShapes are the path shapes: each a converted-only name and a really
// edited name of the same shape. The quoted alias names a file that
// unquotes to another tracked file (a.txt, b.txt).
var rpShapes = []struct{ shape, converted, edited string }{
	{"leading quote", "\"quoted.txt", "\"edited.txt"},
	{"quoted alias of another file", "\"a.txt\"", "\"b.txt\""},
	{"backslash", `back\slash.txt`, `edit\ed.txt`},
	{"quoted octal escape", `"a\164xt"`, `"b\164xt"`},
	{"newline", "new\nline.txt", "edit\ned.txt"},
	{"trailing CR", "ends-in-cr\r", "edited\r"},
	{"leading dash", "-n", "-e"},
	{"not UTF-8", "\xff\xfe-latin1.txt", "\xfe\xff-edited.txt"},
}

const (
	rpStored = "the shape\nline\n"
	rpEdited = "the shape\r\nline\r\nreal edit\r\n"
)

// rpConverted is a repository with a.txt, b.txt and the given tracked
// names, all checked out under `* text eol=crlf` and no neutral line yet.
// It returns false when the file system refuses a name (APFS refuses a
// name that is not UTF-8).
func rpConverted(t *testing.T, names ...string) (string, bool) {
	t.Helper()
	repo := testutil.NewTempRepo(t)
	testutil.WriteFile(t, repo, ".gitattributes", "* text eol=crlf\n")
	all := append([]string{"a.txt", "b.txt"}, names...)
	for _, name := range all {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(rpStored), 0o600); err != nil {
			return "", false
		}
	}
	tbGit(t, repo, nil, "add", "-A")
	tbGit(t, repo, nil, "commit", "-q", "-m", "files")
	for _, name := range all {
		if err := os.Remove(filepath.Join(repo, name)); err != nil {
			t.Fatal(err)
		}
	}
	tbGit(t, repo, nil, "checkout", "--", ".")
	for _, name := range all {
		if b, _ := os.ReadFile(filepath.Join(repo, name)); string(b) != "the shape\r\nline\r\n" {
			t.Fatalf("fixture: %q not converted: %q", name, b)
		}
	}
	return repo, true
}

func TestNeutralAttributes_EveryPathShapeSurvivesTheRenormalisation(t *testing.T) {
	ctx := context.Background()
	for _, tc := range rpShapes {
		for _, retry := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/retry=%v", tc.shape, retry), func(t *testing.T) {
				repo, ok := rpConverted(t, tc.converted, tc.edited)
				if !ok {
					if runtime.GOOS == "darwin" {
						t.Skipf("the file system refuses the name (git-floor runs this shape on Linux)")
					}
					t.Fatal("cannot create the shape's files")
				}
				testutil.WriteFile(t, repo, tc.edited, rpEdited)
				rec := testutil.InstallGitRecorder(t)
				c := git.NewCLIClient(repo, git.WithTreeBudget(git.TreeBudget{}))
				if retry {
					// The first call stops after the line; the record must
					// name exactly the edited file, its bytes intact.
					git.SetRenormaliseAfterLineHookForTest(func() error { return errors.New("simulated I/O error") })
					err := c.EnsureNeutralWorkTree(ctx)
					git.SetRenormaliseAfterLineHookForTest(nil)
					if !errors.Is(err, git.ErrRenormalisePending) {
						t.Fatalf("first call: %v; want ErrRenormalisePending", err)
					}
					gd := tbGit(t, repo, nil, "rev-parse", "--absolute-git-dir")
					changes, ok, err := git.ReadRenormaliseRecordForTest(filepath.Join(gd, git.RenormaliseMarkerName))
					if err != nil || !ok || !reflect.DeepEqual(changes, []string{tc.edited}) {
						t.Fatalf("record = %q (ok=%v err=%v); want exactly [%q]", changes, ok, err, tc.edited)
					}
				}
				if err := c.EnsureNeutralWorkTree(ctx); err != nil {
					t.Fatalf("EnsureNeutralWorkTree: %v", err)
				}
				for _, name := range []string{tc.converted, "a.txt", "b.txt"} {
					if b := rnRead(t, repo, name); b != rpStored {
						t.Errorf("%q = %q; want its stored bytes", name, b)
					}
				}
				if b := rnRead(t, repo, tc.edited); b != rpEdited {
					t.Errorf("%q (really edited) = %q; want it kept", tc.edited, b)
				}
				if rnMarked(t, repo) {
					t.Error("the marker is still there")
				}
				for _, call := range rec.Calls(t) {
					if len(call) > 0 && call[0] == "hash-object" {
						t.Fatalf("the renormalisation ran git %q", call)
					}
				}
			})
		}
	}
}

// TestNeutralAttributes_ManyConvertedFilesAreRenormalised: 400 converted
// files with quoted, backslashed names in one checkout-index call.
func TestNeutralAttributes_ManyConvertedFilesAreRenormalised(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewTempRepo(t)
	testutil.WriteFile(t, repo, ".gitattributes", "* text eol=crlf\n")
	var names []string
	for i := 0; i < 400; i++ {
		name := fmt.Sprintf("dir/%s\"quoted\\name-%03d.txt", strings.Repeat("n", 40), i)
		names = append(names, name)
		testutil.WriteFile(t, repo, name, fmt.Sprintf("file %d\nline\n", i))
	}
	tbGit(t, repo, nil, "add", "-A")
	tbGit(t, repo, nil, "commit", "-q", "-m", "files")
	if err := os.RemoveAll(filepath.Join(repo, "dir")); err != nil {
		t.Fatal(err)
	}
	tbGit(t, repo, nil, "checkout", "--", ".")
	if err := git.NewCLIClient(repo, git.WithTreeBudget(git.TreeBudget{})).EnsureNeutralWorkTree(ctx); err != nil {
		t.Fatal(err)
	}
	for i, name := range names {
		if b := rnRead(t, repo, name); b != fmt.Sprintf("file %d\nline\n", i) {
			t.Fatalf("%q = %q; want its stored bytes", name, b)
		}
	}
	if rnMarked(t, repo) {
		t.Error("still marked")
	}
}
