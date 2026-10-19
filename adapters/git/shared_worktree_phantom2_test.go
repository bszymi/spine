//go:build unix

package git_test

// INIT-023 TASK-012 review round 2, finding 3 and the AC 11 gap: a case
// collision of a regular file and a symbolic link (a type-change phantom)
// is restored in bounded time and quarantined at most once; more than
// maxPhantoms phantoms are quarantined at every take and never wedge.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/testutil"
)

// swCommitFileLinkCollision commits, on a new branch made from main with
// plumbing, file (a regular file) and link (a symbolic link to target),
// two names that differ only in case.
func swCommitFileLinkCollision(t *testing.T, repo, branch, file, link, target string) {
	t.Helper()
	idx := filepath.Join(t.TempDir(), "index")
	env := append(os.Environ(), "GIT_INDEX_FILE="+idx)
	run := func(stdin string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = env
		if stdin != "" {
			cmd.Stdin = strings.NewReader(stdin)
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("", "read-tree", "main")
	fileBlob := run("UPPER\n", "hash-object", "-w", "--stdin")
	linkBlob := run(target, "hash-object", "-w", "--stdin")
	run("", "update-index", "--add", "--cacheinfo", "100644,"+fileBlob+","+file)
	run("", "update-index", "--add", "--cacheinfo", "120000,"+linkBlob+","+link)
	tree := run("", "write-tree")
	commit := run("collide\n", "-c", "user.name=t", "-c", "user.email=t@example.test", "commit-tree", tree, "-p", "main")
	swGit(t, repo, "branch", branch, commit)
}

// The review's reproduction: a file and a link to a FIFO under two names
// that differ only in case. Whichever the disk keeps, git reports a type
// change no reset removes (both orders, so that in one of them the link
// is what the disk keeps). Every take used to block in `git hash-object`
// on the FIFO until the 30 s restore bound whenever the link won, and
// each one quarantined again (a type change was never a known phantom).
// Now each take is quick and only the first one quarantines.
func TestF9_FileLinkCaseCollisionPhantomIsBounded(t *testing.T) {
	for _, names := range [][2]string{{"A.md", "a.md"}, {"a.md", "A.md"}} {
		t.Run("file "+names[0]+", link "+names[1], func(t *testing.T) {
			ctx := context.Background()
			repo := testutil.NewTempRepo(t)
			if !caseInsensitive(t, repo) {
				t.Skip("needs a case-insensitive filesystem (macOS APFS default, Windows)")
			}
			fifo := filepath.Join(t.TempDir(), "pipe")
			if err := syscall.Mkfifo(fifo, 0o644); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				// Unblock any reader a regression left behind.
				if w, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
					_ = w.Close()
				}
			})
			swCommitFileLinkCollision(t, repo, "collide", names[0], names[1], fifo)
			c := git.NewCLIClient(repo, git.WithHomeBranch("main"))
			start := time.Now()
			if err := c.WithWorkingTree(ctx, func(ctx context.Context) error {
				_, err := c.Merge(ctx, git.MergeOpts{Source: "collide", Target: "main", Strategy: "merge-commit", Message: "collide"})
				return err
			}); err != nil {
				t.Fatalf("merge of the collision into main: %v", err)
			}
			if took := time.Since(start); took > 10*time.Second {
				t.Fatalf("the merge sequence took %s: its exit restore blocked reading through the link", took)
			}
			quarantines := 0
			for i := 0; i < 4; i++ {
				start = time.Now()
				rep, err := c.RestoreWorkingTree(ctx)
				if took := time.Since(start); took > 10*time.Second {
					t.Fatalf("restore %d took %s: blocked reading through the link", i, took)
				}
				if err != nil {
					t.Fatalf("restore %d over the file/link phantom: %v", i, err)
				}
				if rep.QuarantineSHA != "" {
					quarantines++
				}
				ran := false
				start = time.Now()
				if err := c.WithWorkingTree(ctx, func(context.Context) error { ran = true; return nil }); err != nil || !ran {
					t.Fatalf("sequence %d over the file/link phantom: ran=%v err=%v", i, ran, err)
				}
				if took := time.Since(start); took > 10*time.Second {
					t.Fatalf("sequence %d took %s: its restores blocked reading through the link", i, took)
				}
				swStagedClean(t, repo)
			}
			if quarantines > 1 {
				t.Fatalf("%d of 4 restores quarantined the same file/link phantom; want at most the first", quarantines)
			}
		})
	}
}

// More than maxPhantoms (64) phantoms are not remembered: while they show,
// every take quarantines them again (a stash commit, a Warn, an audit
// record), and the tree still never wedges.
func TestF9_PhantomsBeyondTheBoundAreQuarantinedAndNeverWedge(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewTempRepo(t)
	const n = 65
	var paths []string
	for i := 0; i < n; i++ {
		p := fmt.Sprintf("notes/n%02d.md", i)
		paths = append(paths, p)
		testutil.WriteFile(t, repo, p, "line one\r\nline two\r\n")
	}
	swGit(t, repo, "add", "notes")
	swGit(t, repo, "commit", "-q", "-m", "notes with CRLF")
	testutil.WriteFile(t, repo, ".gitattributes", "*.md text eol=lf\n")
	swGit(t, repo, "add", ".gitattributes")
	swGit(t, repo, "commit", "-q", "-m", "attributes")
	c := git.NewCLIClient(repo, git.WithHomeBranch("main"))
	for i := 0; i < 3; i++ {
		for _, p := range paths {
			swStale(t, repo, p)
		}
		rep, err := c.RestoreWorkingTree(ctx)
		if err != nil {
			t.Fatalf("restore %d over %d phantoms: %v", i, n, err)
		}
		// How many the reset leaves visible depends on git's racy-clean
		// window; all n were visible before it, so it quarantined.
		if rep.QuarantineSHA == "" || len(rep.Phantoms) == 0 {
			t.Fatalf("restore %d = quarantine %q, %d phantoms; want a quarantine and the phantoms reported", i, rep.QuarantineSHA, len(rep.Phantoms))
		}
		ran := false
		if err := c.WithWorkingTree(ctx, func(context.Context) error { ran = true; return nil }); err != nil || !ran {
			t.Fatalf("sequence %d over %d phantoms: ran=%v err=%v", i, n, ran, err)
		}
		swStagedClean(t, repo)
	}
}
