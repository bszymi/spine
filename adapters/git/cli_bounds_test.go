package git_test

// INIT-023 TASK-003 (audit F4, P3-9): bounded reads and NUL-delimited
// listings in the git CLI client. Tests that measure
// runtime.MemStats.TotalAlloc do not run in parallel.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/yamlsafe"
)

// allocDuring returns the bytes the Go heap allocated while f ran.
func allocDuring(f func()) uint64 {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// blobRepo is a repository whose refs/heads/spine/run/x commit holds the
// given files (path → content), written straight into the object store
// (loose objects; no working-tree copy). It returns the repo and the
// commit id.
func blobRepo(t *testing.T, files map[string][]byte) (string, string) {
	t.Helper()
	dir := t.TempDir()
	t3git(t, dir, nil, "init", "-q", "-b", "main")
	t3git(t, dir, nil, "commit", "-q", "--allow-empty", "-m", "seed")
	var index bytes.Buffer
	ids := map[string]string{} // content -> blob id: one hash-object per distinct body
	for p, body := range files {
		blob, ok := ids[string(body)]
		if !ok {
			blob = t3git(t, dir, body, "hash-object", "-w", "--stdin")
			ids[string(body)] = blob
		}
		fmt.Fprintf(&index, "100644 %s 0\t%s\n", blob, p)
	}
	env := append(os.Environ(), "GIT_INDEX_FILE="+filepath.Join(dir, ".git", "t003-index"))
	cmd := exec.Command("git", "update-index", "--add", "--index-info")
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = &index
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("update-index: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "write-tree")
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("write-tree: %v", err)
	}
	commit := t3git(t, dir, nil, "commit-tree", strings.TrimSpace(string(out)), "-p", "main", "-m", "blobs")
	t3git(t, dir, nil, "update-ref", "refs/heads/spine/run/x", commit)
	return dir, commit
}

func repeatTo(n int64) []byte {
	line := []byte("lorem ipsum dolor sit amet\n")
	b := bytes.Repeat(line, int(n)/len(line)+1)
	return b[:n]
}

// assertBlobTooLarge checks the full refusal contract (AC 2).
func assertBlobTooLarge(t *testing.T, label string, err error, ref, path string, size, limit int64) {
	t.Helper()
	if !errors.Is(err, git.ErrBlobTooLarge) {
		t.Fatalf("%s: err = %v, want errors.Is(err, git.ErrBlobTooLarge)", label, err)
	}
	var tl *git.BlobTooLargeError
	if !errors.As(err, &tl) {
		t.Fatalf("%s: errors.As(*git.BlobTooLargeError) failed for %v", label, err)
	}
	if tl.Size != size || tl.Limit != limit || tl.Ref != ref || tl.Path != path {
		t.Errorf("%s: BlobTooLargeError = %+v, want {Ref:%q Path:%q Size:%d Limit:%d}", label, *tl, ref, path, size, limit)
	}
	ge, ok := err.(*git.GitError)
	if !ok {
		t.Fatalf("%s: err is %T, want an unwrapped *git.GitError", label, err)
	}
	if ge.Kind != git.ErrKindPermanent || ge.IsRetryable() {
		t.Errorf("%s: Kind = %q retryable=%v, want permanent, not retryable", label, ge.Kind, ge.IsRetryable())
	}
	if errors.Is(err, git.ErrOutputTooLarge) {
		t.Errorf("%s: a blob refusal must not also match ErrOutputTooLarge", label)
	}
	if !strings.Contains(err.Error(), "read limit") || strings.Contains(err.Error(), "corrupt") {
		t.Errorf("%s: error text %q, want a read-limit message and no corruption report", label, err)
	}
}

// AC 2: an oversized blob is refused by the size probe, before any of
// its content is read.
func TestReadFile_OversizedBlobIsRefusedBeforeReading(t *testing.T) {
	ctx := context.Background()
	justOver := repeatTo(git.DefaultMaxBlobBytes + 1)
	eight := repeatTo(8 << 20)
	dir, _ := blobRepo(t, map[string][]byte{"over.md": justOver, "eight.md": eight})
	c := git.NewCLIClient(dir)
	const ref = "refs/heads/spine/run/x"
	for _, tc := range []struct {
		path string
		size int64
	}{
		{"over.md", int64(len(justOver))},
		{"eight.md", int64(len(eight))},
	} {
		var err error
		alloc := allocDuring(func() { _, err = c.ReadFile(ctx, ref, tc.path) })
		assertBlobTooLarge(t, tc.path, err, ref, tc.path, tc.size, git.DefaultMaxBlobBytes)
		if alloc >= 1<<20 {
			t.Errorf("%s: ReadFile allocated %d KiB; want < 1 MiB (refused before reading)", tc.path, alloc>>10)
		}
	}
}

// AC 3: an at-limit blob is returned byte-identical through one
// pre-sized buffer (no growth, no string round trip); a 0-byte blob is a
// non-nil empty slice.
func TestReadFile_AtLimitIsOnePreSizedBuffer(t *testing.T) {
	ctx := context.Background()
	const limit = 1 << 20
	body := repeatTo(limit)
	dir, commit := blobRepo(t, map[string][]byte{"at.md": body, "empty.md": {}})
	c := git.NewCLIClient(dir, git.WithMaxBlobBytes(limit))

	var got []byte
	var err error
	alloc := allocDuring(func() { got, err = c.ReadFile(ctx, commit, "at.md") })
	if err != nil {
		t.Fatalf("ReadFile at the limit: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("ReadFile returned %d bytes, not the %d-byte blob", len(got), len(body))
	}
	if alloc > limit+limit/4 {
		t.Errorf("ReadFile of a %d-byte blob allocated %d KiB; want <= 1.25 MiB (one pre-sized buffer)", limit, alloc>>10)
	}

	empty, err := c.ReadFile(ctx, commit, "empty.md")
	if err != nil {
		t.Fatalf("ReadFile of a 0-byte blob: %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("0-byte blob = %#v, want a non-nil empty slice", empty)
	}
}

// AC 4: the read's own cap is the second line of defence. With a probe
// that under-reports, an 8 MiB blob is still refused, with Size -1, and
// the read stops at the cap.
func TestReadFile_ReadCapIsTheSecondLineOfDefence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const limit = 1 << 20
	dir, commit := blobRepo(t, map[string][]byte{"big.md": repeatTo(8 << 20)})
	c := git.NewCLIClient(dir, git.WithMaxBlobBytes(limit))
	c.SetBlobSizeForTest(func(context.Context, string) (int64, error) { return 1, nil })

	var err error
	alloc := allocDuring(func() { _, err = c.ReadFile(ctx, commit, "big.md") })
	assertBlobTooLarge(t, "lying probe", err, commit, "big.md", -1, limit)
	if alloc >= 4<<20 {
		t.Errorf("ReadFile allocated %d KiB past a 1 MiB cap; want < 4 MiB", alloc>>10)
	}
	if ctx.Err() != nil {
		t.Fatal("ReadFile returned only at the deadline: the git child was not stopped at the cap")
	}
}

// AC 5 (I2): every error is the one the unbounded read gives. The
// expected Kind and Message come from running today's argv
// (`git show [eoo] <spec> --`) and classifying its stderr, so the probe
// can never change a miss's kind or text. Since INIT-023 TASK-002 every
// miss is "bad revision" (ErrKindNotFound) — including an unknown ref and
// a path that exists only in the working tree, which the design (written
// on 61994fe, before that change) listed as Permanent.
func TestReadFile_ErrorKindParity(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t3git(t, dir, nil, "init", "-q", "-b", "main")
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"docs/a.md", "docs/b.md", "top.md"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x "+f+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t3git(t, dir, nil, "add", "-A")
	t3git(t, dir, nil, "commit", "-q", "-m", "one")
	// A gitlink whose commit is absent: cat-file -s and git show word it
	// differently; the show's error wins.
	t3git(t, dir, nil, "update-index", "--add", "--cacheinfo", "160000,"+strings.Repeat("ab", 20)+",gl.md")
	t3git(t, dir, nil, "commit", "-q", "-m", "gitlink")
	if err := os.WriteFile(filepath.Join(dir, "disk-only.md"), []byte("untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	head := t3git(t, dir, nil, "rev-parse", "HEAD")
	c := git.NewCLIClient(dir)

	unbounded := func(spec string) (string, *git.GitError) {
		cmd := exec.Command("git", "show", "--end-of-options", spec, "--")
		cmd.Dir = dir
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		if err := cmd.Run(); err != nil {
			return "", git.ClassifyGitErrorForTest(strings.TrimSpace(errb.String()))
		}
		return out.String(), nil
	}

	for _, tc := range []struct {
		label, ref, path string
		wantKind         git.GitErrorKind
	}{
		{"missing path", "HEAD", "nope.md", git.ErrKindNotFound},
		{"missing nested path", "HEAD", "docs/nope/x.md", git.ErrKindNotFound},
		{"path through a blob", "HEAD", "top.md/x", git.ErrKindNotFound},
		{"unknown ref", "badref", "top.md", git.ErrKindNotFound},
		{"unknown ref at an object id", strings.Repeat("cd", 20), "top.md", git.ErrKindNotFound},
		{"present on disk, not at the ref", head, "disk-only.md", git.ErrKindNotFound},
		{"gitlink to an absent commit", head, "gl.md", git.ErrKindPermanent},
	} {
		_, err := c.ReadFile(ctx, tc.ref, tc.path)
		ge, ok := err.(*git.GitError)
		if !ok {
			t.Errorf("%s: err = %T %v, want a direct *git.GitError", tc.label, err, err)
			continue
		}
		_, want := unbounded(tc.ref + ":" + tc.path)
		if want == nil {
			t.Fatalf("%s: the unbounded read succeeded; fixture is wrong", tc.label)
		}
		if ge.Kind != tc.wantKind || ge.Kind != want.Kind {
			t.Errorf("%s: Kind = %q, want %q (unbounded read: %q)", tc.label, ge.Kind, tc.wantKind, want.Kind)
		}
		if ge.Message != want.Message {
			t.Errorf("%s: Message = %q, want the unbounded read's %q", tc.label, ge.Message, want.Message)
		}
		if ge.Op != "read_file" || errors.Is(err, git.ErrBlobTooLarge) {
			t.Errorf("%s: Op = %q, size refusal = %v", tc.label, ge.Op, errors.Is(err, git.ErrBlobTooLarge))
		}
	}

	// A tree path keeps today's `git show` listing, byte for byte.
	got, err := c.ReadFile(ctx, head, "docs")
	if err != nil {
		t.Fatalf("ReadFile of a tree path: %v", err)
	}
	want, gerr := unbounded(head + ":docs")
	if gerr != nil {
		t.Fatal(gerr)
	}
	if string(got) != want {
		t.Fatalf("tree path output:\n got %q\nwant %q", got, want)
	}
}

// AC 6: every other read command fails past the per-command ceiling;
// the defaults leave ordinary repositories alone.
func TestReadCommands_OutputCeiling(t *testing.T) {
	ctx := context.Background()
	files := map[string][]byte{}
	for i := 0; i < 300; i++ {
		files[fmt.Sprintf("initiatives/INIT-001/notes/file-%03d.md", i)] = []byte("x\n")
	}
	repo, commit := blobRepo(t, files)
	t3git(t, repo, nil, "update-ref", "refs/heads/main", commit)
	// A commit whose message is 8 KiB.
	msg := strings.Repeat("m", 8<<10)
	big := t3git(t, repo, nil, "commit-tree", commit+"^{tree}", "-p", commit, "-m", msg)
	t3git(t, repo, nil, "update-ref", "refs/heads/main", big)
	t3git(t, repo, nil, "symbolic-ref", "HEAD", "refs/heads/main")

	small := git.NewCLIClient(repo, git.WithMaxOutputBytes(4<<10))
	_, err := small.ListFiles(ctx, big, "")
	assertOutputTooLarge(t, "ListFiles", err, "list_files", 4<<10)
	_, err = small.Log(ctx, git.LogOpts{Limit: 1})
	assertOutputTooLarge(t, "Log", err, "log", 4<<10)

	def := git.NewCLIClient(repo)
	listed, err := def.ListFiles(ctx, big, "")
	if err != nil || len(listed) != 300 {
		t.Fatalf("default ListFiles: %d files, %v", len(listed), err)
	}
	commits, err := def.Log(ctx, git.LogOpts{Limit: 1})
	if err != nil || len(commits) != 1 || len(commits[0].Message) != len(msg) {
		t.Fatalf("default Log: %+v, %v", commits, err)
	}
}

func assertOutputTooLarge(t *testing.T, label string, err error, op string, limit int64) {
	t.Helper()
	if !errors.Is(err, git.ErrOutputTooLarge) {
		t.Fatalf("%s: err = %v, want errors.Is(err, git.ErrOutputTooLarge)", label, err)
	}
	var tl *git.OutputTooLargeError
	if !errors.As(err, &tl) || tl.Op != op || tl.Limit != limit || tl.GitAlloc {
		t.Fatalf("%s: OutputTooLargeError = %+v, want {Op:%q Limit:%d GitAlloc:false}", label, tl, op, limit)
	}
	ge, ok := err.(*git.GitError)
	if !ok || ge.Kind != git.ErrKindPermanent || ge.IsRetryable() {
		t.Fatalf("%s: want a permanent *git.GitError, got %T %v", label, err, err)
	}
	if errors.Is(err, git.ErrBlobTooLarge) {
		t.Errorf("%s: an output refusal must not match ErrBlobTooLarge", label)
	}
}

// AC 6 (design-check note): the kept stderr is bounded and keeps its
// tail, where the deciding line usually is.
func TestStderr_KeepsHeadAndTail(t *testing.T) {
	var in bytes.Buffer
	for in.Len() < 1<<20 {
		in.WriteString("warning: CRLF will be replaced by LF in docs/notes-0001.md.\n")
	}
	const last = "fatal: Unable to create '/r/.git/index.lock': File exists."
	in.WriteString(last + "\n")
	for _, chunk := range []int{0, 1000, 4096, 1} {
		kept := git.StderrKeptForTest(in.Bytes(), chunk)
		if len(kept) > git.MaxStderrBytesForTest {
			t.Fatalf("chunk %d: kept %d bytes of stderr, want <= %d", chunk, len(kept), git.MaxStderrBytesForTest)
		}
		if !strings.Contains(kept, last) {
			t.Fatalf("chunk %d: the final fatal line was dropped", chunk)
		}
		if !strings.HasPrefix(kept, "warning: CRLF") || !strings.Contains(kept, "[truncated]") {
			t.Fatalf("chunk %d: want the head and a truncation marker", chunk)
		}
		if k := git.ClassifyGitErrorForTest(strings.TrimSpace(kept)).Kind; k != git.ErrKindTransient {
			t.Fatalf("chunk %d: classified %q, want transient (the lock line)", chunk, k)
		}
	}
	// Short stderr is kept verbatim.
	if got := git.StderrKeptForTest([]byte(last+"\n"), 0); got != last+"\n" {
		t.Fatalf("short stderr = %q", got)
	}
}

// AC 7: the defaults and options.
func TestReadBounds_Defaults(t *testing.T) {
	// Spine's API refuses artifact writes over 512 KiB
	// (service/internal/gateway/handlers_helpers.go maxArtifactContentSize,
	// not importable here): anything the API accepts must be readable.
	if git.DefaultMaxBlobBytes < 512<<10 {
		t.Errorf("DefaultMaxBlobBytes = %d, below the 512 KiB API write cap", git.DefaultMaxBlobBytes)
	}
	if git.DefaultMaxBlobBytes < yamlsafe.MaxBytes {
		t.Errorf("DefaultMaxBlobBytes = %d, below yamlsafe.MaxBytes %d", git.DefaultMaxBlobBytes, yamlsafe.MaxBytes)
	}
	if git.DefaultMaxOutputBytes < git.DefaultMaxBlobBytes {
		t.Errorf("DefaultMaxOutputBytes %d < DefaultMaxBlobBytes %d", git.DefaultMaxOutputBytes, git.DefaultMaxBlobBytes)
	}
	type bounds struct{ blob, out, alloc int64 }
	get := func(c *git.CLIClient) bounds {
		b, o, a := c.ReadBoundsForTest()
		return bounds{b, o, a}
	}
	def := bounds{git.DefaultMaxBlobBytes, git.DefaultMaxOutputBytes, 4 * git.DefaultMaxBlobBytes}
	for _, tc := range []struct {
		label string
		c     *git.CLIClient
		want  bounds
	}{
		{"no options", git.NewCLIClient("."), def},
		{"WithMaxBlobBytes(0)", git.NewCLIClient(".", git.WithMaxBlobBytes(0)), def},
		{"WithMaxBlobBytes(-1)", git.NewCLIClient(".", git.WithMaxBlobBytes(-1)), def},
		{"WithMaxOutputBytes(0)", git.NewCLIClient(".", git.WithMaxOutputBytes(0)), def},
		{"WithMaxGitAllocBytes(-1)", git.NewCLIClient(".", git.WithMaxGitAllocBytes(-1)), def},
		{"zero-value client", &git.CLIClient{}, def},
		{"alloc follows the blob limit", git.NewCLIClient(".", git.WithMaxBlobBytes(1<<20)),
			bounds{1 << 20, git.DefaultMaxOutputBytes, 4 << 20}},
		{"alloc follows the blob limit in any option order",
			git.NewCLIClient(".", git.WithMaxGitAllocBytes(0), git.WithMaxBlobBytes(1<<20)),
			bounds{1 << 20, git.DefaultMaxOutputBytes, 4 << 20}},
		{"explicit alloc", git.NewCLIClient(".", git.WithMaxGitAllocBytes(64<<20)),
			bounds{git.DefaultMaxBlobBytes, git.DefaultMaxOutputBytes, 64 << 20}},
		{"too-small alloc is raised to blob+1", git.NewCLIClient(".", git.WithMaxBlobBytes(1<<20), git.WithMaxGitAllocBytes(1000)),
			bounds{1 << 20, git.DefaultMaxOutputBytes, 1<<20 + 1}},
		{"too-small explicit alloc with the default blob limit", git.NewCLIClient(".", git.WithMaxGitAllocBytes(1000)),
			bounds{git.DefaultMaxBlobBytes, git.DefaultMaxOutputBytes, git.DefaultMaxBlobBytes + 1}},
		// git's own fixed allocations (524 256 bytes in ls-tree, log,
		// merge-base, diff) need a floor: 4 x 64 KiB would break them.
		{"alloc floor under a small blob limit", git.NewCLIClient(".", git.WithMaxBlobBytes(64<<10)),
			bounds{64 << 10, git.DefaultMaxOutputBytes, 1 << 20}},
		{"alloc floor for an explicit value", git.NewCLIClient(".", git.WithMaxBlobBytes(1000), git.WithMaxGitAllocBytes(2000)),
			bounds{1000, git.DefaultMaxOutputBytes, 1 << 20}},
		{"output floor", git.NewCLIClient(".", git.WithMaxOutputBytes(1)),
			bounds{git.DefaultMaxBlobBytes, 4096, 4 * git.DefaultMaxBlobBytes}},
		{"explicit output", git.NewCLIClient(".", git.WithMaxOutputBytes(4096)),
			bounds{git.DefaultMaxBlobBytes, 4096, 4 * git.DefaultMaxBlobBytes}},
	} {
		if got := get(tc.c); got != tc.want {
			t.Errorf("%s: bounds = %+v, want %+v", tc.label, got, tc.want)
		}
	}
}

// A small per-blob limit leaves every read command working: the alloc
// limit's floor covers git's own fixed allocations.
func TestReadBounds_SmallBlobLimitKeepsReadsWorking(t *testing.T) {
	ctx := context.Background()
	dir, commit := blobRepo(t, map[string][]byte{"a.md": []byte("a\n"), "b/c.md": []byte("c\n")})
	c := git.NewCLIClient(dir, git.WithMaxBlobBytes(1<<10))
	if files, err := c.ListFiles(ctx, commit, ""); err != nil || len(files) != 2 {
		t.Fatalf("ListFiles: %q, %v", files, err)
	}
	parent := t3git(t, dir, nil, "rev-parse", commit+"~1")
	if diffs, err := c.Diff(ctx, parent, commit); err != nil || len(diffs) != 2 {
		t.Fatalf("Diff: %+v, %v", diffs, err)
	}
	if _, err := c.MergeBase(ctx, parent, commit); err != nil {
		t.Fatalf("MergeBase: %v", err)
	}
	t3git(t, dir, nil, "update-ref", "refs/heads/main", commit)
	t3git(t, dir, nil, "symbolic-ref", "HEAD", "refs/heads/main")
	if _, err := c.Log(ctx, git.LogOpts{Limit: 2}); err != nil {
		t.Fatalf("Log: %v", err)
	}
	if got, err := c.ReadFile(ctx, commit, "b/c.md"); err != nil || string(got) != "c\n" {
		t.Fatalf("ReadFile: %q, %v", got, err)
	}
}

// AC 8: raw paths through ListFiles and Diff — renames, copies and type
// changes included.
func TestDiff_RawRenameCopyAndTypeChange(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t3git(t, dir, nil, "init", "-q", "-b", "main")
	write := func(p, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, p), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	long := strings.Repeat("a stable line of text\n", 40)
	write("orig.md", long)
	write("src.md", long+"copy source\n")
	write("link-or-file", "plain\n")
	t3git(t, dir, nil, "add", "-A")
	t3git(t, dir, nil, "commit", "-q", "-m", "one")
	from := t3git(t, dir, nil, "rev-parse", "HEAD")

	t3git(t, dir, nil, "mv", "orig.md", "re\nnamed.md")
	write("copy of src.md", long+"copy source\n")
	write("src.md", long+"copy source, edited\n")
	if err := os.Remove(filepath.Join(dir, "link-or-file")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("orig.md", filepath.Join(dir, "link-or-file")); err != nil {
		t.Fatal(err)
	}
	t3git(t, dir, nil, "add", "-A")
	t3git(t, dir, nil, "commit", "-q", "-m", "two")
	to := t3git(t, dir, nil, "rev-parse", "HEAD")

	byPath := func(diffs []git.FileDiff) map[string]git.FileDiff {
		m := map[string]git.FileDiff{}
		for _, d := range diffs {
			m[d.Path] = d
		}
		return m
	}

	c := git.NewCLIClient(dir)
	diffs, err := c.Diff(ctx, from, to)
	if err != nil {
		t.Fatal(err)
	}
	got := byPath(diffs)
	if d := got["re\nnamed.md"]; d.Status != "renamed" || d.OldPath != "orig.md" {
		t.Errorf("rename = %+v, want renamed orig.md -> \"re\\nnamed.md\"", d)
	}
	if d := got["link-or-file"]; d.Status != "modified" {
		t.Errorf("type change = %+v, want modified", d)
	}

	// With diff.renames=copies a copy is an addition at the destination
	// (before -z the default case reported the SOURCE as modified).
	t3git(t, dir, nil, "config", "diff.renames", "copies")
	diffs, err = c.Diff(ctx, from, to)
	if err != nil {
		t.Fatal(err)
	}
	got = byPath(diffs)
	if len(diffs) != 4 || len(got) != 4 {
		t.Errorf("with copies: %d records over %d paths, want 4 distinct (C, T, R, M): %+v", len(diffs), len(got), diffs)
	}
	if d := got["copy of src.md"]; d.Status != "added" || d.OldPath != "" {
		t.Errorf("copy = %+v, want added at the destination", d)
	}
	if d := got["src.md"]; d.Status != "modified" {
		t.Errorf("copy source = %+v, want its own modification only", d)
	}
}

// AC 8: the -z parser is strict — no partial list on a truncated or
// malformed stream, and no path in the error.
func TestParseNameStatusZ(t *testing.T) {
	ok := []struct {
		in   string
		want []git.FileDiff
	}{
		{"", nil},
		{"A\x00a.md\x00", []git.FileDiff{{Path: "a.md", Status: "added"}}},
		{"M\x00 lead.md\x00D\x00\"q.md\x00T\x00t\x00", []git.FileDiff{
			{Path: " lead.md", Status: "modified"}, {Path: "\"q.md", Status: "deleted"}, {Path: "t", Status: "modified"}}},
		{"R100\x00old\x00new\nline\x00", []git.FileDiff{{OldPath: "old", Path: "new\nline", Status: "renamed"}}},
		{"C075\x00src\x00dst\x00", []git.FileDiff{{Path: "dst", Status: "added"}}},
		{"X\x00odd\x00", []git.FileDiff{{Path: "odd", Status: "modified"}}},
	}
	for _, tc := range ok {
		got, err := git.ParseNameStatusZForTest(tc.in)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("parse %q = %+v, %v; want %+v", tc.in, got, err, tc.want)
		}
	}
	for _, in := range []string{
		"R100\x00only-one\x00",
		"A\x00",
		"A\x00a.md",                // unterminated
		"A\x00a.md\x00M\x00",       // second record missing its path
		"A\x00\x00",                // empty path
		"R100\x00\x00b\x00",        // empty source
		"a\x00x.md\x00",            // lower-case status
		"R1x\x00a\x00b\x00",        // non-numeric score
		"\x00x.md\x00",             // empty status
		"A\x00secret-path.md\x00M", // trailing garbage
	} {
		got, err := git.ParseNameStatusZForTest(in)
		if err == nil || got != nil {
			t.Errorf("parse %q = %+v, %v; want an error and no records", in, got, err)
			continue
		}
		if strings.Contains(err.Error(), "secret-path") || strings.Contains(err.Error(), "only-one") {
			t.Errorf("parse error echoes a path: %v", err)
		}
	}
}

// AC 19: mutating commands never fail on output size. git applies the
// side effect before it prints, so a failing ceiling would report a
// fast-forward that had already moved main as failed.
func TestMutatingCommands_DrainOutput(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t3git(t, dir, nil, "init", "-q", "-b", "main")
	t3git(t, dir, nil, "config", "user.email", "t@t")
	t3git(t, dir, nil, "config", "user.name", "t")
	t3git(t, dir, nil, "commit", "-q", "--allow-empty", "-m", "seed")
	t3git(t, dir, nil, "checkout", "-q", "-b", "feature")
	for i := 0; i < 300; i++ {
		p := filepath.Join(dir, fmt.Sprintf("added-file-with-a-long-name-%03d.md", i))
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t3git(t, dir, nil, "add", "-A")
	c := git.NewCLIClient(dir, git.WithMaxOutputBytes(4<<10))

	// Commit of 300 staged files: its summary alone is over 4 KiB.
	res, err := c.Commit(ctx, git.CommitOpts{Message: "add 300 files", Author: git.Author{Name: "t", Email: "t@t"}})
	if err != nil {
		t.Fatalf("Commit with a 4 KiB output ceiling: %v", err)
	}
	tip := t3git(t, dir, nil, "rev-parse", "refs/heads/feature")
	if res.SHA != tip {
		t.Fatalf("Commit SHA = %s, want %s", res.SHA, tip)
	}

	t3git(t, dir, nil, "checkout", "-q", "main")
	mr, err := c.Merge(ctx, git.MergeOpts{Source: "feature", Strategy: "fast-forward"})
	mainNow := t3git(t, dir, nil, "rev-parse", "refs/heads/main")
	if mainNow != tip {
		t.Fatalf("refs/heads/main = %s, want the fast-forwarded tip %s", mainNow, tip)
	}
	if err != nil {
		t.Fatalf("Merge failed although refs/heads/main moved to %s: %v", mainNow, err)
	}
	if mr.SHA != tip || !mr.FastForward {
		t.Fatalf("Merge = %+v, want SHA %s, FastForward", mr, tip)
	}
}

// The alloc-limit matcher accepts only git's own refusal line for the
// limit this client set.
func TestGitAllocRefusalMatcher(t *testing.T) {
	const limit = 16777216
	for _, tc := range []struct {
		stderr string
		want   bool
	}{
		{"fatal: attempting to allocate 67108864 over limit 16777216", true},
		{"error: attempting to allocate 20971495 over limit 16777216\nfatal: packed object 6efb (stored in .git/objects/pack/p.pack) is corrupt", true},
		{"error: attempting to allocate 1 over limit 16777216\r", true},
		{"fatal: attempting to allocate 67108864 over limit 4194304", false},
		{"fatal: path 'attempting to allocate 1 over limit 16777216' does not exist in 'HEAD'", false},
		{"warning: x\nfatal: attempting to allocate 1 over limit 16777216 trailing", false},
		{"", false},
	} {
		if got := git.IsGitAllocRefusalForTest(tc.stderr, limit); got != tc.want {
			t.Errorf("match %q = %v, want %v", tc.stderr, got, tc.want)
		}
	}
}

// Every op name the client runs a command under has a declared output
// class: an unlisted read op would lose GIT_ALLOC_LIMIT, and an unlisted
// mutating op would fail on output size after its side effect.
func TestEveryOpHasAnOutputClass(t *testing.T) {
	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	call := regexp.MustCompile(`\.run(?:Bounded)?\(ctx, "([^"]+)"`)
	seen := 0
	for _, f := range sources {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range call.FindAllStringSubmatch(string(src), -1) {
			seen++
			if !git.OpClassDeclaredForTest(m[1]) {
				t.Errorf("%s: op %q has no entry in opClasses", f, m[1])
			}
		}
	}
	if seen < 20 {
		t.Fatalf("found only %d run calls; the pattern no longer matches the source", seen)
	}
}

// Codex review round 1 (P2): git translates the "fatal: " / "error: "
// prefix of its alloc refusal, so read commands run in the C locale. A
// git wrapper that answers like a translated git unless LC_ALL=C (and
// echoes the limit it was given) must still yield ErrBlobTooLarge.
func TestReadCommands_RunInTheCLocale(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell wrapper")
	}
	ctx := context.Background()
	dir, commit := blobRepo(t, map[string][]byte{"t.md": []byte("small\n")})
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := "#!/bin/sh\n" +
		"case \"$1\" in cat-file|show)\n" +
		"  if [ \"$LC_ALL\" = C ]; then p=fatal; else p=Schwerwiegend; fi\n" +
		"  echo \"$p: attempting to allocate 99999999 over limit $GIT_ALLOC_LIMIT\" >&2; exit 128;;\n" +
		"esac\n" +
		"exec " + realGit + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil { //nolint:gosec // G306: an executable test wrapper
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("LC_ALL", "de_DE.UTF-8")
	t.Setenv("LANGUAGE", "de")

	_, err = git.NewCLIClient(dir).ReadFile(ctx, commit, "t.md")
	assertBlobTooLarge(t, "translated host", err, commit, "t.md", -1, git.DefaultMaxBlobBytes)
}

// Codex review round 3 (P2): Merge reads its result from the head of
// `git merge`'s drained output, and every commit and merge ends with a
// `rev-parse HEAD`, so the per-command limit is floored at 4 KiB: even
// WithMaxOutputBytes(1) reports a fast-forward as one.
func TestMerge_TinyOutputLimitKeepsTheResult(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t3git(t, dir, nil, "init", "-q", "-b", "main")
	t3git(t, dir, nil, "config", "user.email", "t@t")
	t3git(t, dir, nil, "config", "user.name", "t")
	t3git(t, dir, nil, "commit", "-q", "--allow-empty", "-m", "seed")
	t3git(t, dir, nil, "checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(dir, "f.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t3git(t, dir, nil, "add", "-A")
	t3git(t, dir, nil, "commit", "-q", "-m", "one file")
	tip := t3git(t, dir, nil, "rev-parse", "HEAD")
	t3git(t, dir, nil, "checkout", "-q", "main")

	c := git.NewCLIClient(dir, git.WithMaxOutputBytes(1))
	mr, err := c.Merge(ctx, git.MergeOpts{Source: "feature"})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if mr.SHA != tip || !mr.FastForward {
		t.Fatalf("Merge = %+v, want SHA %s and FastForward (the result line was truncated away)", mr, tip)
	}
}
