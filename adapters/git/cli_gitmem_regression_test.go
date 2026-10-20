package git_test

// INIT-023 TASK-003 AC 18 (design check, 2026-10-06): git's own memory
// is bounded on read commands. Ported from the design-check PoC
// (zz_t003dc_deltabase_test.go), which failed on Spine 61994fe: a
// 3 000 000-byte tip blob stored by stock `git repack` as a delta on a
// 64 MiB base passed the size probe (`cat-file -s` reports the TARGET's
// size), and `git show` then inflated the whole base in the git child —
// 71 MiB RSS for a 3 MB read, in the same cgroup as customer-app. A
// gitlink named *.md that points at an in-repo commit was the other
// probe bypass: `git show` rendered that commit WITH its patch. Read
// commands now run with GIT_ALLOC_LIMIT (4 x the per-blob limit by
// default), and git's refusal is mapped to ErrBlobTooLarge; since the
// wave 4 follow-up ReadFile reads blobs only and refuses a gitlink
// unread (ErrNotBlob).
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
	"fmt"
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

	// gitMemDefaultAlloc is the default client's GIT_ALLOC_LIMIT: 4 x the
	// per-blob limit.
	gitMemDefaultAlloc = 4 * git.DefaultMaxBlobBytes
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
	// GitAllocLimit is BlobTooLargeError.GitAllocLimit; GitAlloc is
	// OutputTooLargeError.GitAlloc (review round 1).
	GitAllocLimit int64 `json:"git_alloc_limit"`
	GitAlloc      bool  `json:"git_alloc"`
	// NotBlob is errors.Is(err, git.ErrNotBlob) (wave 4 follow-up).
	NotBlob bool `json:"not_blob"`
}

// recordErr fills the error fields of a child result.
func (res *gitMemResult) recordErr(err error) {
	if err == nil {
		return
	}
	res.Err = err.Error()
	res.TooLarge = errors.Is(err, git.ErrBlobTooLarge)
	res.OutputLarge = errors.Is(err, git.ErrOutputTooLarge)
	res.NotBlob = errors.Is(err, git.ErrNotBlob)
	var tl *git.BlobTooLargeError
	if errors.As(err, &tl) {
		res.Size = tl.Size
		res.GitAllocLimit = tl.GitAllocLimit
	}
	var ot *git.OutputTooLargeError
	if errors.As(err, &ot) {
		res.GitAlloc = ot.GitAlloc
	}
	var ge *git.GitError
	if errors.As(err, &ge) {
		res.Permanent = ge.Kind == git.ErrKindPermanent && !ge.IsRetryable()
	}
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
	switch os.Getenv("T003_GITMEM_OP") {
	case "diff":
		n, _ := strconv.ParseInt(os.Getenv("T003_GITMEM_ALLOC"), 10, 64)
		c = git.NewCLIClient(repo, git.WithMaxGitAllocBytes(n))
		diffs, err := c.Diff(ctx, os.Getenv("T003_GITMEM_FROM"), os.Getenv("T003_GITMEM_TO"))
		res.Len = len(diffs)
		if err != nil {
			res.Err = err.Error()
		} else if len(diffs) == 1 && diffs[0].Status == "renamed" && diffs[0].OldPath == "a.bin" && diffs[0].Path == "b.bin" {
			res.Match = true
		}
	case "log":
		commits, err := c.Log(ctx, git.LogOpts{Limit: 1})
		res.Len = len(commits)
		res.recordErr(err)
	default:
		data, err := c.ReadFile(ctx, os.Getenv("T003_GITMEM_REF"), os.Getenv("T003_GITMEM_PATH"))
		res.Len = len(data)
		if want := os.Getenv("T003_GITMEM_WANT"); want != "" {
			body, rerr := os.ReadFile(want) //nolint:gosec // G703: a temp file path the parent test wrote
			res.Match = rerr == nil && bytes.Equal(body, data)
		}
		res.recordErr(err)
	}
	res.RSS = childPeakRSS()
	out, _ := json.Marshal(res)
	os.Stdout.Write(append(append([]byte("T003MEM "), out...), '\n'))
}

func runGitMemChild(t *testing.T, env ...string) gitMemResult {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestT003_GitMemChild$", "-test.count=1", "-test.v") //nolint:gosec // G702: re-exec of this test binary
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
	// Review round 1 (P3): the refusal names git's allocation limit, the
	// real cause — the file itself is under the per-blob limit.
	if res.GitAllocLimit != gitMemDefaultAlloc || !strings.Contains(res.Err, "git allocation limit") {
		t.Errorf("%s: GitAllocLimit = %d, error %q; want the %d-byte git allocation limit named as the cause",
			label, res.GitAllocLimit, res.Err, gitMemDefaultAlloc)
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
// 64 MiB blob — `git show` would render the commit with its patch. TASK-003
// had GIT_ALLOC_LIMIT refuse it inside git; since the wave 4 follow-up
// ReadFile types the entry first and refuses the gitlink without reading
// any object behind it (ErrNotBlob).
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
	assertGitMemNotBlob(t, "gitlink", res)
}

// Wave 4 follow-up (SMP INIT-027/EPIC-004/TASK-008, measured at ae513dca):
// a gitlink *.md pointing at an in-repo commit whose patch adds 10^6
// paths through one shared subtree. Every object is tiny — two 1000-entry
// trees and one blob — so the size probe passed (it sized the ~200-byte
// commit) and no single allocation came near GIT_ALLOC_LIMIT, but `git
// show` built the whole diff queue before the output cap could stop it:
// 272 MiB in the git child. ReadFile now refuses the gitlink unread.
func TestGitMem_GitlinkToWideCommitIsRefusedUnread(t *testing.T) {
	dir := t.TempDir()
	t3git(t, dir, nil, "init", "-q", "-b", "main")
	blob := t3git(t, dir, []byte("x\n"), "hash-object", "-w", "--stdin")
	var leaf, mid bytes.Buffer
	for i := 0; i < 1000; i++ {
		fmt.Fprintf(&leaf, "100644 blob %s\tf%04d\n", blob, i)
	}
	leafTree := t3git(t, dir, leaf.Bytes(), "mktree")
	for i := 0; i < 1000; i++ {
		fmt.Fprintf(&mid, "040000 tree %s\td%04d\n", leafTree, i)
	}
	midTree := t3git(t, dir, mid.Bytes(), "mktree")
	root := t3git(t, dir, []byte("040000 tree "+midTree+"\tsub\n"), "mktree")
	wide := t3git(t, dir, nil, "commit-tree", root, "-m", "a million paths")
	t3git(t, dir, nil, "update-index", "--add", "--cacheinfo", "160000,"+wide+",g.md")
	t3git(t, dir, nil, "commit", "-qm", "gitlink to the wide commit")
	res := runGitMemChild(t, "T003_GITMEM_REPO="+dir, "T003_GITMEM_REF=refs/heads/main", "T003_GITMEM_PATH=g.md")
	assertGitMemNotBlob(t, "gitlink to a wide commit", res)
}

// assertGitMemNotBlob checks a child's gitlink read: the not-a-file
// refusal, nothing read, and a git child that never rendered anything.
func assertGitMemNotBlob(t *testing.T, label string, res gitMemResult) {
	t.Helper()
	t.Logf("%s: len=%d err=%q git child peak RSS=%d MiB", label, res.Len, res.Err, res.RSS>>20)
	if !res.NotBlob || !res.Permanent || res.TooLarge || res.OutputLarge || res.Len != 0 {
		t.Errorf("%s: %+v, want the permanent ErrNotBlob refusal and no content", label, res)
	}
	if res.RSS >= 0 && res.RSS >= 32<<20 {
		t.Errorf("%s: git child peak RSS %d MiB, want < 32 MiB", label, res.RSS>>20)
	}
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

// chainRepo builds, with stock `git fast-import` (fastimport.unpackLimit=1
// keeps its pack instead of exploding it into loose objects), a t.md
// whose versions form one linear delta chain: `versions` blobs of
// baseBytes, each a small edit of the one before, then a tipBytes tip
// cut from the last. It checks that the tip sits at depth >= versions and
// that `cat-file -s` sees only the tip, and returns the repo and a file
// holding the tip's bytes.
func chainRepo(t *testing.T, baseBytes, versions, tipBytes int) (string, string) {
	t.Helper()
	dir := t.TempDir()
	t3git(t, dir, nil, "init", "-q", "-b", "main")
	cmd := exec.Command("git", "-c", "fastimport.unpackLimit=1", "fast-import", "--quiet", "--depth=50")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	emit := func(i int, data []byte) {
		fmt.Fprintf(stdin, "commit refs/heads/main\ncommitter t <t@t> %d +0000\ndata 3\nv%02d\nM 100644 inline t.md\ndata %d\n", i, i, len(data))
		stdin.Write(data)
		stdin.Write([]byte("\n"))
	}
	body := gitMemBody(baseBytes)
	for v := 1; v <= versions; v++ {
		copy(body[(v*7919)%(baseBytes-16):], fmt.Sprintf("EDIT%04d", v))
		emit(v, body)
	}
	tip := body[:tipBytes]
	emit(versions+1, tip)
	stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("fast-import: %v\n%s", err, stderr.String())
	}
	if probe := t3git(t, dir, nil, "cat-file", "-s", "refs/heads/main:t.md"); probe != strconv.Itoa(tipBytes) {
		t.Fatalf("cat-file -s = %s, want %d (the probe sees only the target)", probe, tipBytes)
	}
	tipID := t3git(t, dir, nil, "rev-parse", "refs/heads/main:t.md")
	idx, _ := filepath.Glob(filepath.Join(dir, ".git", "objects", "pack", "*.idx"))
	if len(idx) != 1 {
		t.Fatalf("packs: %v", idx)
	}
	depth := -1
	for _, l := range strings.Split(t3git(t, dir, nil, "verify-pack", "-v", idx[0]), "\n") {
		if f := strings.Fields(l); len(f) >= 7 && f[0] == tipID {
			depth, _ = strconv.Atoi(f[5])
		}
	}
	if depth < versions {
		t.Fatalf("the tip is at delta depth %d, want >= %d; the fixture no longer builds a chain", depth, versions)
	}
	want := filepath.Join(t.TempDir(), "want")
	if err := os.WriteFile(want, tip, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, want
}

// Review round 1 (P2), a fifth AC 18 row: GIT_ALLOC_LIMIT checks one
// allocation at a time, and rebuilding an object at the end of a delta
// chain keeps every intermediate base in git's delta-base cache
// (core.deltaBaseCacheLimit, 96 MiB by default). A 3 000 000-byte tip
// on a chain of eight 15 MiB bases — each under the 16 MiB alloc limit,
// so nothing is refused — is a legitimate read, but without a pinned
// cache it took the git child to 127 MiB (134 MB with plain `git show`).
// With the cache pinned, what remains is one chain step (the base being
// patched, the result, the cache): about 3x the alloc limit, 55 MB
// measured.
func TestGitMem_DeltaChainOfSubLimitBasesStaysBounded(t *testing.T) {
	const base = 15 << 20 // under the 16 MiB default alloc limit
	if base >= gitMemDefaultAlloc {
		t.Fatalf("each base must be under the alloc limit (%d)", gitMemDefaultAlloc)
	}
	dir, want := chainRepo(t, base, 8, gitMemTipBytes)
	res := runGitMemChild(t, "T003_GITMEM_REPO="+dir, "T003_GITMEM_REF=refs/heads/main", "T003_GITMEM_PATH=t.md",
		"T003_GITMEM_WANT="+want)
	t.Logf("delta chain: len=%d err=%q git child peak RSS=%d MiB", res.Len, res.Err, res.RSS>>20)
	if res.Err != "" || !res.Match || res.Len != gitMemTipBytes {
		t.Fatalf("delta chain of sub-limit bases: %+v, want the tip, byte-identical (no false refusal)", res)
	}
	// One chain step is about 3 bases; 16 MiB is git's own baseline and
	// the tip. Without the pinned cache: 127 MiB.
	if bound := int64(3*gitMemDefaultAlloc + 16<<20); res.RSS >= 0 && res.RSS >= bound {
		t.Errorf("git child peak RSS %d MiB, want < %d MiB (3 x the alloc limit + 16 MiB)", res.RSS>>20, bound>>20)
	}
}

// Review round 1 (P3): the Log commit-message vector (Decision 9). A
// commit whose message is 64 MiB of one character is a 64 KiB object on
// disk; `git log` builds the whole commit in memory before it prints a
// byte, so without GIT_ALLOC_LIMIT the read reached 72 MB RSS and only
// then hit Spine's stdout ceiling. With it, git refuses the allocation:
// ErrOutputTooLarge with GitAlloc, and the child stays small. This pins
// "log" to the read class by behaviour, not only by the op table.
func TestGitMem_LogCommitMessageIsRefusedInGit(t *testing.T) {
	dir := t.TempDir()
	t3git(t, dir, nil, "init", "-q", "-b", "main")
	msg := filepath.Join(t.TempDir(), "msg")
	if err := os.WriteFile(msg, bytes.Repeat([]byte("m"), gitMemBaseBytes), 0o644); err != nil {
		t.Fatal(err)
	}
	tree := t3git(t, dir, nil, "write-tree")
	commit := t3git(t, dir, nil, "commit-tree", tree, "-F", msg)
	t3git(t, dir, nil, "update-ref", "refs/heads/main", commit)
	res := runGitMemChild(t, "T003_GITMEM_REPO="+dir, "T003_GITMEM_OP=log")
	t.Logf("log: len=%d err=%q git child peak RSS=%d MiB", res.Len, res.Err, res.RSS>>20)
	if !res.OutputLarge || !res.GitAlloc || !res.Permanent || res.TooLarge {
		t.Fatalf("Log of a 64 MiB message: %+v, want a permanent ErrOutputTooLarge with GitAlloc (git refused the allocation)", res)
	}
	if res.RSS >= 0 && res.RSS >= 32<<20 {
		t.Errorf("git child peak RSS %d MiB, want < 32 MiB", res.RSS>>20)
	}
}
