package git_test

// INIT-024 TASK-004 AC 8 (review finding N-12): CountChangedPaths counts
// the paths two commits differ in without loading a blob. Output evidence
// diffs a commit pair the run-branch author chooses, and Diff — `git diff
// --name-status` with rename detection, exempt from GIT_ALLOC_LIMIT —
// loads both blobs of every rename candidate: a rename pair of two large
// blobs took the git child to several hundred MB. The counter runs
// `git diff --name-only -z --no-renames` as a read command, so
// GIT_ALLOC_LIMIT and the pinned delta-base cache apply, and with no
// rename detection no blob is read at all.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
)

func countBody(n int, seed string) []byte {
	line := []byte("a line of ordinary text in a large tracked file, " + seed + "\n")
	return bytes.Repeat(line, n/len(line)+1)[:n]
}

func TestCountChangedPaths_UnderAllocLimit(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t3git(t, dir, nil, "init", "-q", "-b", "main")
	const size = 8 << 20
	writeFile(t, dir, "a.bin", countBody(size, "x"))
	writeFile(t, dir, "keep.md", []byte("keep\n"))
	t3git(t, dir, nil, "add", "-A")
	t3git(t, dir, nil, "commit", "-qm", "large file")
	from := t3git(t, dir, nil, "rev-parse", "HEAD")
	if err := os.Remove(filepath.Join(dir, "a.bin")); err != nil {
		t.Fatal(err)
	}
	changed := countBody(size, "x")
	copy(changed[size/2:], "ONE CHANGED LINE\n")
	writeFile(t, dir, "b.bin", changed)
	writeFile(t, dir, "keep.md", []byte("kept, edited\n"))
	t3git(t, dir, nil, "add", "-A")
	t3git(t, dir, nil, "commit", "-qm", "rename with one change")
	to := t3git(t, dir, nil, "rev-parse", "HEAD")

	const blobLimit = 1 << 20
	c := git.NewCLIClient(dir, git.WithMaxBlobBytes(blobLimit), git.WithMaxGitAllocBytes(blobLimit))
	_, _, alloc := c.ReadBoundsForTest()
	var envs []string
	var ops []string
	c.SetEnvHookForTest(func(op string, env []string) { ops = append(ops, op); envs = append(envs, strings.Join(env, "\x00")) })

	// The counter: a delete and an add (renames count as both), plus the
	// edit — and no error under a 1 MiB allocation limit with 8 MiB blobs.
	n, err := c.CountChangedPaths(ctx, from, to)
	if err != nil || n != 3 {
		t.Fatalf("CountChangedPaths = %d, %v; want 3 (a.bin, b.bin, keep.md) and no error", n, err)
	}
	if len(ops) != 1 || ops[0] != "count_changed_paths" {
		t.Fatalf("listing ops = %v, want one count_changed_paths", ops)
	}
	if !strings.Contains(envs[0], "GIT_ALLOC_LIMIT="+strconv.FormatInt(alloc, 10)) ||
		!strings.Contains(envs[0], "core.deltaBaseCacheLimit=") {
		t.Fatalf("the count did not run as a read command (no GIT_ALLOC_LIMIT=%d / pinned cache)", alloc)
	}
	if got := git.OpClassesForTest()["count_changed_paths"]; got != "read" {
		t.Fatalf("count_changed_paths is class %q, want read", got)
	}

	// Controls. The same pair under the same limit WITH rename detection
	// fails in git: the blobs are what a rename check loads, so the
	// count's success is the absence of that load.
	cmd := exec.Command("git", "diff", "--name-only", "-z", "-M", "--end-of-options", from, to, "--")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_ALLOC_LIMIT="+strconv.FormatInt(alloc, 10), "LC_ALL=C")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil || !strings.Contains(stderr.String(), "over limit") {
		t.Fatalf("control: rename detection under GIT_ALLOC_LIMIT=%d: %v %q, want git's allocation refusal", alloc, err, stderr.String())
	}
	// Diff (opDiff, no allocation limit) still detects the rename.
	ops = nil
	diffs, err := c.Diff(ctx, from, to)
	if err != nil || len(diffs) != 2 {
		t.Fatalf("Diff = %+v, %v; want the rename and the edit", diffs, err)
	}
	if strings.Contains(envs[len(envs)-1], "GIT_ALLOC_LIMIT=") {
		t.Fatal("Diff ran with GIT_ALLOC_LIMIT (it is exempt)")
	}

	// Grammar and resolution as Diff's.
	var ge *git.GitError
	if _, err := c.CountChangedPaths(ctx, "--output=x", to); !errors.Is(err, git.ErrInvalidRef) {
		t.Errorf("an option-shaped ref: %v, want ErrInvalidRef", err)
	}
	if _, err := c.CountChangedPaths(ctx, "spine/run/gone", to); !errors.As(err, &ge) || ge.Kind != git.ErrKindNotFound {
		t.Errorf("a missing run branch: %v, want ErrKindNotFound", err)
	}
	if n, err := c.CountChangedPaths(ctx, "main", "main"); err != nil || n != 0 {
		t.Errorf("CountChangedPaths(main, main) = %d, %v; want 0", n, err)
	}
}

// The counter keeps nothing: it counts records in any chunking and
// refuses an empty or unterminated record.
func TestCountChangedPaths_StreamContract(t *testing.T) {
	out := []byte("a.md\x00dir/b c.md\x00r\xc3\xa9.md\x00" + strings.Repeat("x", 10000) + "\x00")
	for chunk := 1; chunk <= 64; chunk++ {
		if n, err := git.CountedForTest(out, chunk); err != nil || n != 4 {
			t.Fatalf("%d-byte writes: %d, %v; want 4", chunk, n, err)
		}
	}
	for _, bad := range []string{"\x00", "a\x00\x00", "a\x00b"} {
		if _, err := git.CountedForTest([]byte(bad), 1); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
	if n, err := git.CountedForTest(nil, 1); err != nil || n != 0 {
		t.Errorf("empty output: %d, %v", n, err)
	}
}

