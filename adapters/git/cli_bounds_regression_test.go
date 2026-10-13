package git_test

// INIT-023 TASK-003 AC 1: the design-phase PoCs for audit F4 (unbounded
// ReadFile; skeptic-bigblob, verify-r1-9 phase A) and P3-9 (C-quoted and
// whitespace-trimmed ListFiles / Diff paths), committed with their
// assertions unchanged. Each failed on Spine 61994fe as recorded in the
// task artifact (49 MiB TotalAlloc for an 8 MiB blob; quoted and trimmed
// paths) and passes with the bounded, NUL-delimited client.

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
)

func t3git(t *testing.T, dir string, stdin []byte, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null")
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, errb.String())
	}
	return strings.TrimSpace(out.String())
}

// 8 MiB of compressible text: comfortably over any proposed default cap
// (4 MiB), small enough for CI.
const t3BlobBytes = 8 << 20

func t3BigBlobRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	t3git(t, dir, nil, "init", "-q", "-b", "main")
	t3git(t, dir, nil, "commit", "-q", "--allow-empty", "-m", "seed")
	body := append([]byte("---\nid: TASK-9\ntype: Task\ntitle: big\nstatus: Pending\n---\n"),
		bytes.Repeat([]byte("lorem ipsum dolor sit amet\n"), t3BlobBytes/27+1)...)
	blob := t3git(t, dir, body, "hash-object", "-w", "--stdin")
	sub := t3git(t, dir, []byte("100644 blob "+blob+"\tbig.md\n"), "mktree")
	root := t3git(t, dir, []byte("040000 tree "+sub+"\tinitiatives\n"), "mktree")
	commit := t3git(t, dir, nil, "commit-tree", root, "-p", "main", "-m", "big")
	t3git(t, dir, nil, "update-ref", "refs/heads/spine/run/x", commit)
	return dir, commit
}

func TestT003_ReadFileOversizedBlobIsRefusedWithoutBuffering(t *testing.T) {
	dir, _ := t3BigBlobRepo(t)
	c := git.NewCLIClient(dir)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	data, err := c.ReadFile(context.Background(), "refs/heads/spine/run/x", "initiatives/big.md")
	runtime.ReadMemStats(&after)
	alloc := after.TotalAlloc - before.TotalAlloc
	t.Logf("ReadFile: len=%d err=%v TotalAlloc=%d KiB", len(data), err, alloc>>10)
	if err == nil {
		t.Errorf("ReadFile of a %d-byte blob returned %d bytes and no error; want a too-large refusal", t3BlobBytes, len(data))
	}
	if alloc > 1<<20 {
		t.Errorf("ReadFile allocated %d KiB for an over-limit blob; want < 1 MiB (refused before reading content)", alloc>>10)
	}
}

// Paths git C-quotes without -z, plus a leading-space path that
// strings.TrimSpace(output) mangles today (it sorts first in ls-tree).
var t3TrickyPaths = []string{
	" lead.md",
	`"quoted.md`,
	"r\xc3\xa9view.md",
	"tab\tfile.md",
	"new\nline.md",
	"plain.md",
}

func t3TrickyRepo(t *testing.T) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	t3git(t, dir, nil, "init", "-q", "-b", "main")
	t3git(t, dir, nil, "commit", "-q", "--allow-empty", "-m", "seed")
	from := t3git(t, dir, nil, "rev-parse", "HEAD")
	for _, p := range t3TrickyPaths {
		if err := os.WriteFile(filepath.Join(dir, p), []byte("x "+p+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t3git(t, dir, nil, "add", "-A")
	t3git(t, dir, nil, "commit", "-q", "-m", "tricky")
	to := t3git(t, dir, nil, "rev-parse", "HEAD")
	return dir, from, to
}

func t3sorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func TestT003_ListFilesReturnsRawPaths(t *testing.T) {
	dir, _, to := t3TrickyRepo(t)
	c := git.NewCLIClient(dir)
	files, err := c.ListFiles(context.Background(), to, "")
	if err != nil {
		t.Fatal(err)
	}
	got, want := t3sorted(files), t3sorted(t3TrickyPaths)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("ListFiles paths:\n got %q\nwant %q", got, want)
	}
}

func TestT003_DiffReturnsRawPaths(t *testing.T) {
	dir, from, to := t3TrickyRepo(t)
	c := git.NewCLIClient(dir)
	diffs, err := c.Diff(context.Background(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range diffs {
		if d.Status != "added" {
			t.Errorf("status for %q = %q, want added", d.Path, d.Status)
		}
		got = append(got, d.Path)
	}
	if strings.Join(t3sorted(got), "\x00") != strings.Join(t3sorted(t3TrickyPaths), "\x00") {
		t.Errorf("Diff paths:\n got %q\nwant %q", t3sorted(got), t3sorted(t3TrickyPaths))
	}
}
