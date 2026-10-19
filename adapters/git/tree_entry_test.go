package git_test

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/testutil"
)

// absentCommit is a commit id that does not exist in the test repository.
const absentCommit = "0123456789abcdef0123456789abcdef01234567"

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestTreeEntryType types tree entries without reading their objects
// (INIT-023 TASK-011 review round 1). The gitlinks are named so that
// `git show <sha>:<path>`'s stderr — which echoes the path — contains the
// substrings classifyGitError reads as transient ("lock", "timeout",
// "Could not resolve"): the type must come from the tree, not stderr.
func TestTreeEntryType(t *testing.T) {
	client, repo := newTestClient(t)
	ctx := context.Background()

	testutil.WriteFile(t, repo, "docs/a/x.md", "# x\n")
	testutil.WriteFile(t, repo, "docs/st*r.md", "star\n")
	testutil.WriteFile(t, repo, "docs/zażółć.md", "utf-8\n")
	stageFile(t, repo, "docs")
	for _, p := range []string{"docs/blocked.md", "docs/timeout.md", "docs/Could not resolve.md"} {
		gitIn(t, repo, "update-index", "--add", "--cacheinfo", "160000,"+absentCommit+","+p)
	}
	gitIn(t, repo, "commit", "-q", "-m", "fixture")
	head := gitIn(t, repo, "rev-parse", "HEAD")

	// Precondition: `git show` of the gitlink fails, and the
	// stderr-substring classifier would call it transient. (ReadFile no
	// longer runs show since the wave 4 follow-up: it types the entry and
	// refuses a gitlink as ErrNotBlob, Permanent, whatever its name.)
	cmd := exec.CommandContext(ctx, "git", "show", "--end-of-options", head+":docs/blocked.md", "--")
	cmd.Dir = repo
	out, showErr := cmd.CombinedOutput()
	if showErr == nil || git.ClassifyGitErrorForTest(strings.TrimSpace(string(out))).Kind != git.ErrKindTransient {
		t.Fatalf("precondition: git show of a gitlink named blocked.md should be misclassified transient by stderr, got %v: %s", showErr, out)
	}
	_, err := client.ReadFile(ctx, head, "docs/blocked.md")
	var gerr *git.GitError
	if !errors.Is(err, git.ErrNotBlob) || !errors.As(err, &gerr) || gerr.Kind != git.ErrKindPermanent {
		t.Fatalf("ReadFile of a gitlink named blocked.md = %v, want the Permanent ErrNotBlob refusal", err)
	}

	for _, tc := range []struct {
		path string
		want string
	}{
		{"docs/a/x.md", git.TreeEntryBlob},
		{"docs/a", git.TreeEntryTree},
		{"docs/blocked.md", git.TreeEntryCommit},
		{"docs/timeout.md", git.TreeEntryCommit},
		{"docs/Could not resolve.md", git.TreeEntryCommit},
		{"docs/st*r.md", git.TreeEntryBlob},
		{"docs/zażółć.md", git.TreeEntryBlob},
		{"docs/none.md", ""},
		// Pathspec magic and globs stay literal.
		{"docs/*.md", ""},
		{":(glob)docs/**", ""},
		// git normalises `..`; the record it prints is a different path.
		{"docs/a/../a/x.md", ""},
		// A path below a gitlink has no entry of its own.
		{"docs/blocked.md/inner.md", ""},
	} {
		got, err := client.TreeEntryType(ctx, head, tc.path)
		if err != nil {
			t.Errorf("TreeEntryType(%q): %v", tc.path, err)
			continue
		}
		if got != tc.want {
			t.Errorf("TreeEntryType(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// An unanswerable question is an error, never "absent".
func TestTreeEntryType_Errors(t *testing.T) {
	client, _ := newTestClient(t)
	ctx := context.Background()
	for _, tc := range []struct{ name, ref, path string }{
		{"missing tree object", absentCommit, "x.md"},
		{"empty ref", "", "x.md"},
		{"option-like ref", "-x", "x.md"},
		{"empty path", "HEAD", ""},
	} {
		if typ, err := client.TreeEntryType(ctx, tc.ref, tc.path); err == nil {
			t.Errorf("%s: expected an error, got type %q", tc.name, typ)
		}
	}

	// git itself cannot run: the error is returned.
	t.Setenv("PATH", t.TempDir())
	if typ, err := client.TreeEntryType(ctx, "HEAD", "x.md"); err == nil {
		t.Errorf("git unavailable: expected an error, got type %q", typ)
	}
}
