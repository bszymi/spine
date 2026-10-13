package git_test

// INIT-023 TASK-003 AC 18 (design check, 2026-10-06): git's own memory
// is bounded on read commands. Ported from the design-check PoC
// (zz_t003dc_deltabase_test.go), which failed on Spine 61994fe: a
// 3 000 000-byte tip blob stored by stock `git repack` as a delta on a
// 64 MiB base passed the size probe (`cat-file -s` reports the TARGET's
// size), and `git show` then inflated the whole base in the git child —
// 71 MiB RSS for a 3 MB read, in the same cgroup as customer-app. A
// gitlink named *.md that points at an in-repo commit is the other
// probe bypass: `git show` renders that commit WITH its patch. Read
// commands now run with GIT_ALLOC_LIMIT (4 x the per-blob limit by
// default), and git's refusal is mapped to ErrBlobTooLarge.
//
// The git child's peak RSS is measured in a re-exec of this test binary
// (RUSAGE_CHILDREN), so the fixture's own git commands — a repack of a
// 64 MiB blob — do not count. On platforms without RUSAGE_CHILDREN only
// the error assertions run.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
)

const (
	gitMemBaseBytes = 64 << 20  // far over any per-blob or alloc limit
	gitMemTipBytes  = 3_000_000 // under the 4 MiB default; >= base/32, so pack-objects deltifies it
)

// gitMemResult is what the child reports.
type gitMemResult struct {
	Len         int    `json:"len"`
	RSS         int64  `json:"rss"`
	Err         string `json:"err"`
	TooLarge    bool   `json:"too_large"`
	OutputLarge bool   `json:"output_large"`
	Size        int64  `json:"size"`
	Permanent   bool   `json:"permanent"`
	Match       bool   `json:"match"`
}

// TestT003_GitMemChild is the re-exec entry point; it is a no-op in a
// normal run.
func TestT003_GitMemChild(t *testing.T) {
	repo := os.Getenv("T003_GITMEM_REPO")
	if repo == "" {
		t.Skip("child process only")
	}
	var opts []git.CLIOption
	if v := os.Getenv("T003_GITMEM_BLOB"); v != "" {
		n, _ := strconv.ParseInt(v, 10, 64)
		opts = append(opts, git.WithMaxBlobBytes(n))
	}
	ctx := context.Background()
	c := git.NewCLIClient(repo, opts...)
	var res gitMemResult
	if os.Getenv("T003_GITMEM_OP") == "diff" {
		n, _ := strconv.ParseInt(os.Getenv("T003_GITMEM_ALLOC"), 10, 64)
		c = git.NewCLIClient(repo, git.WithMaxGitAllocBytes(n))
		diffs, err := c.Diff(ctx, os.Getenv("T003_GITMEM_FROM"), os.Getenv("T003_GITMEM_TO"))
		res.Len = len(diffs)
		if err != nil {
			res.Err = err.Error()
		} else if len(diffs) == 1 && diffs[0].Status == "renamed" && diffs[0].OldPath == "a.bin" && diffs[0].Path == "b.bin" {
			res.Match = true
		}
	} else {
		data, err := c.ReadFile(ctx, os.Getenv("T003_GITMEM_REF"), os.Getenv("T003_GITMEM_PATH"))
		res.Len = len(data)
		if want := os.Getenv("T003_GITMEM_WANT"); want != "" {
			body, rerr := os.ReadFile(want)
			res.Match = rerr == nil && bytes.Equal(body, data)
		}
		if err != nil {
			res.Err = err.Error()
			res.TooLarge = errors.Is(err, git.ErrBlobTooLarge)
			res.OutputLarge = errors.Is(err, git.ErrOutputTooLarge)
			var tl *git.BlobTooLargeError
			if errors.As(err, &tl) {
				res.Size = tl.Size
			}
			var ge *git.GitError
			if errors.As(err, &ge) {
				res.Permanent = ge.Kind == git.ErrKindPermanent && !ge.IsRetryable()
			}
		}
	}
	res.RSS = childPeakRSS()
	out, _ := json.Marshal(res)
	os.Stdout.Write(append(append([]byte("T003MEM "), out...), '\n'))
}

func runGitMemChild(t *testing.T, env ...string) gitMemResult {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestT003_GitMemChild$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), env...)
	out, _ := cmd.CombinedOutput()
	for _, l := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(l, "T003MEM "); ok {
			var res gitMemResult
			if err := json.Unmarshal([]byte(rest), &res); err != nil {
				t.Fatalf("child result %q: %v", rest, err)
			}
			return res
		}
	}
	t.Fatalf("child produced no result:\n%s", out)
	return gitMemResult{}
}

func gitMemBody(n int) []byte {
	body := append([]byte("---\nid: TASK-1\ntype: Task\ntitle: t\nstatus: Pending\n---\n"),
		bytes.Repeat([]byte("lorem ipsum dolor sit amet\n"), n/27+1)...)
	return body[:n]
}

func writeFile(t *testing.T, dir, rel string, body []byte) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// deltaRepo commits a baseBytes t.md, then its first tipBytes, repacks
// with stock settings and checks the tip is stored as a delta.
func deltaRepo(t *testing.T, baseBytes, tipBytes int) (string, string) {
	t.Helper()
	dir := t.TempDir()
	t3git(t, dir, nil, "init", "-q", "-b", "main")
	body := gitMemBody(baseBytes)
	writeFile(t, dir, "t.md", body)
	t3git(t, dir, nil, "add", "t.md")
	t3git(t, dir, nil, "commit", "-qm", "big")
	writeFile(t, dir, "t.md", body[:tipBytes])
	t3git(t, dir, nil, "commit", "-qam", "trimmed")
	t3git(t, dir, nil, "repack", "-adf", "-q", "--window=10", "--depth=10")
	tip := t3git(t, dir, nil, "rev-parse", "HEAD:t.md")
	idx, _ := filepath.Glob(filepath.Join(dir, ".git", "objects", "pack", "*.idx"))
	if len(idx) != 1 {
		t.Fatalf("packs: %v", idx)
	}
	vp := t3git(t, dir, nil, "verify-pack", "-v", idx[0])
	deltified := false
	for _, l := range strings.Split(vp, "\n") {
		if strings.HasPrefix(l, tip+" ") && len(strings.Fields(l)) >= 7 {
			deltified = true
		}
	}
	if !deltified {
		t.Fatalf("stock repack did not store the %d-byte tip as a delta on the %d-byte base; the fixture no longer exercises the bypass", tipBytes, baseBytes)
	}
	want := filepath.Join(t.TempDir(), "want")
	if err := os.WriteFile(want, body[:tipBytes], 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, want
}

func assertGitMemRefused(t *testing.T, label string, res gitMemResult) {
	t.Helper()
	t.Logf("%s: len=%d err=%q git child peak RSS=%d MiB", label, res.Len, res.Err, res.RSS>>20)
	if !res.TooLarge || res.Size != -1 || !res.Permanent || res.OutputLarge {
		t.Errorf("%s: %+v, want a permanent ErrBlobTooLarge with Size -1 (and not ErrOutputTooLarge)", label, res)
	}
	if !strings.Contains(res.Err, "read limit") || strings.Contains(res.Err, "corrupt") {
		t.Errorf("%s: error %q, want a read-limit message, never git's corruption text", label, res.Err)
	}
	if res.RSS >= 0 && res.RSS >= 32<<20 {
		t.Errorf("%s: git child peak RSS %d MiB, want < 32 MiB", label, res.RSS>>20)
	}
}

// AC 18 (1): a small tip deltified on a large base.
func TestGitMem_DeltaBaseIsRefusedInGit(t *testing.T) {
	dir, _ := deltaRepo(t, gitMemBaseBytes, gitMemTipBytes)
	if probe := t3git(t, dir, nil, "cat-file", "-s", "refs/heads/main:t.md"); probe != strconv.Itoa(gitMemTipBytes) {
		t.Fatalf("cat-file -s = %s, want %d (the probe sees only the target)", probe, gitMemTipBytes)
	}
	res := runGitMemChild(t, "T003_GITMEM_REPO="+dir, "T003_GITMEM_REF=refs/heads/main", "T003_GITMEM_PATH=t.md")
	assertGitMemRefused(t, "delta base", res)
}

// AC 18 (2): a gitlink *.md pointing at an in-repo commit that adds a
// 64 MiB blob — `git show` would render the commit with its patch.
func TestGitMem_GitlinkToCommitIsRefusedInGit(t *testing.T) {
	dir := t.TempDir()
	t3git(t, dir, nil, "init", "-q", "-b", "main")
	t3git(t, dir, nil, "commit", "-q", "--allow-empty", "-m", "seed")
	t3git(t, dir, nil, "checkout", "-q", "-b", "side")
	writeFile(t, dir, "big.bin", gitMemBody(gitMemBaseBytes))
	t3git(t, dir, nil, "add", "big.bin")
	t3git(t, dir, nil, "commit", "-qm", "big")
	side := t3git(t, dir, nil, "rev-parse", "HEAD")
	t3git(t, dir, nil, "checkout", "-q", "main")
	t3git(t, dir, nil, "update-index", "--add", "--cacheinfo", "160000,"+side+",g.md")
	t3git(t, dir, nil, "commit", "-qm", "gitlink")
	res := runGitMemChild(t, "T003_GITMEM_REPO="+dir, "T003_GITMEM_REF=refs/heads/main", "T003_GITMEM_PATH=g.md")
	assertGitMemRefused(t, "gitlink", res)
}

// AC 18 (3): no false refusal within the bounds.
func TestGitMem_NoFalseRefusal(t *testing.T) {
	t.Run("packed undeltified blob at the limit", func(t *testing.T) {
		const limit = 4 << 20
		dir := t.TempDir()
		t3git(t, dir, nil, "init", "-q", "-b", "main")
		body := gitMemBody(limit)
		writeFile(t, dir, "at.md", body)
		t3git(t, dir, nil, "add", "at.md")
		t3git(t, dir, nil, "commit", "-qm", "at limit")
		t3git(t, dir, nil, "repack", "-adf", "-q")
		want := filepath.Join(t.TempDir(), "want")
		if err := os.WriteFile(want, body, 0o644); err != nil {
			t.Fatal(err)
		}
		res := runGitMemChild(t, "T003_GITMEM_REPO="+dir, "T003_GITMEM_REF=refs/heads/main", "T003_GITMEM_PATH=at.md",
			"T003_GITMEM_BLOB="+strconv.Itoa(limit), "T003_GITMEM_WANT="+want)
		if res.Err != "" || !res.Match || res.Len != limit {
			t.Fatalf("at-limit packed blob: %+v, want the %d bytes, byte-identical", res, limit)
		}
	})
	t.Run("3 MiB tip deltified on a 12 MiB base", func(t *testing.T) {
		dir, want := deltaRepo(t, 12<<20, 3<<20)
		res := runGitMemChild(t, "T003_GITMEM_REPO="+dir, "T003_GITMEM_REF=refs/heads/main", "T003_GITMEM_PATH=t.md",
			"T003_GITMEM_WANT="+want)
		if res.Err != "" || !res.Match || res.Len != 3<<20 {
			t.Fatalf("delta within 4x the limit: %+v, want the tip, byte-identical", res)
		}
	})
}

// AC 18 (4): `diff` runs without GIT_ALLOC_LIMIT, so a legitimate large
// rename already on main does not fail Diff (and wedge IncrementalSync).
func TestGitMem_DiffIsExempt(t *testing.T) {
	dir := t.TempDir()
	t3git(t, dir, nil, "init", "-q", "-b", "main")
	body := gitMemBody(20 << 20)
	writeFile(t, dir, "a.bin", body)
	t3git(t, dir, nil, "add", "a.bin")
	t3git(t, dir, nil, "commit", "-qm", "large file")
	from := t3git(t, dir, nil, "rev-parse", "HEAD")
	if err := os.Remove(filepath.Join(dir, "a.bin")); err != nil {
		t.Fatal(err)
	}
	changed := append([]byte(nil), body...)
	copy(changed[len(changed)/2:], "ONE CHANGED LINE\n")
	writeFile(t, dir, "b.bin", changed)
	t3git(t, dir, nil, "add", "-A")
	t3git(t, dir, nil, "commit", "-qm", "rename with one change")
	to := t3git(t, dir, nil, "rev-parse", "HEAD")
	res := runGitMemChild(t, "T003_GITMEM_REPO="+dir, "T003_GITMEM_OP=diff",
		"T003_GITMEM_FROM="+from, "T003_GITMEM_TO="+to, "T003_GITMEM_ALLOC="+strconv.Itoa(16<<20))
	if res.Err != "" || !res.Match {
		t.Fatalf("Diff under a 16 MiB alloc limit: %+v, want one renamed a.bin -> b.bin", res)
	}
}
