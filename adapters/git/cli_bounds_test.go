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
	"strconv"
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

// AC 5 (I2): every miss is the error the unbounded read gives. The
// expected Kind and Message come from running TASK-003's argv
// (`git show [eoo] <spec> --`) and classifying its stderr, so the blob-only
// read (wave 4 follow-up), which builds a miss's error itself, can never
// change its kind or text. Since INIT-023 TASK-002 every miss is "bad
// revision" (ErrKindNotFound) — including an unknown ref and a path that
// exists only in the working tree, which the design (written on 61994fe,
// before that change) listed as Permanent.
//
// The wave 4 follow-up changed two rows on purpose: a gitlink (here one
// whose commit is absent, which show worded "bad object") and a directory
// (which show listed) are the not-a-file refusal now — Permanent, as the
// dangling gitlink always was.
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
	// A gitlink whose commit is absent.
	t3git(t, dir, nil, "update-index", "--add", "--cacheinfo", "160000,"+strings.Repeat("ab", 20)+",gl.md")
	t3git(t, dir, nil, "commit", "-q", "-m", "gitlink")
	if err := os.WriteFile(filepath.Join(dir, "disk-only.md"), []byte("untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	head := t3git(t, dir, nil, "rev-parse", "HEAD")
	topBlob := t3git(t, dir, nil, "rev-parse", head+":top.md")
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
		{"ref naming a blob", topBlob, "top.md", git.ErrKindNotFound},
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

	// A gitlink and a directory: show failed on the one ("bad object",
	// Permanent) and listed the other; both are the not-a-file refusal.
	if _, want := unbounded(head + ":gl.md"); want == nil || want.Kind != git.ErrKindPermanent {
		t.Fatalf("fixture: show of the dangling gitlink = %v, want a Permanent failure", want)
	}
	if listing, want := unbounded(head + ":docs"); want != nil || !strings.Contains(listing, "a.md") {
		t.Fatalf("fixture: show of the tree path = %q, %v, want its listing", listing, want)
	}
	for _, tc := range []struct{ path, typ string }{{"gl.md", git.TreeEntryCommit}, {"docs", git.TreeEntryTree}} {
		got, err := c.ReadFile(ctx, head, tc.path)
		assertNotBlob(t, tc.path, got, err, head, tc.path, tc.typ)
	}
}

// assertNotBlob checks ReadFile's not-a-file refusal (wave 4 follow-up):
// no content, the direct *git.GitError{Permanent, "read_file",
// NotBlobMessage} wrapping a *git.NotBlobError of the entry's type, and
// never a size refusal or a miss.
func assertNotBlob(t *testing.T, label string, got []byte, err error, ref, path, typ string) {
	t.Helper()
	if got != nil {
		t.Errorf("%s: ReadFile returned %d bytes (%q), want none", label, len(got), truncateForTest(got))
	}
	ge, ok := err.(*git.GitError)
	if !ok {
		t.Fatalf("%s: err = %T %v, want a direct *git.GitError", label, err, err)
	}
	if ge.Kind != git.ErrKindPermanent || ge.IsRetryable() || ge.Op != "read_file" || ge.Message != git.NotBlobMessage {
		t.Errorf("%s: %+v, want {Permanent read_file %q}", label, ge, git.NotBlobMessage)
	}
	var nb *git.NotBlobError
	if !errors.Is(err, git.ErrNotBlob) || !errors.As(err, &nb) {
		t.Fatalf("%s: %v does not match ErrNotBlob / *NotBlobError", label, err)
	}
	if nb.Ref != ref || nb.Path != path || nb.Type != typ {
		t.Errorf("%s: NotBlobError = %+v, want {%q %q %q}", label, nb, ref, path, typ)
	}
	if errors.Is(err, git.ErrBlobTooLarge) || errors.Is(err, git.ErrOutputTooLarge) {
		t.Errorf("%s: %v also matches a size refusal", label, err)
	}
	if len(path) > 4 && strings.Contains(nb.Error(), path) { // a short path is a substring of the fixed text
		t.Errorf("%s: NotBlobError text %q prints the branch-controlled path", label, nb.Error())
	}
}

func truncateForTest(b []byte) string {
	if len(b) > 120 {
		return string(b[:120]) + "…"
	}
	return string(b)
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
	mr, err := c.Merge(ctx, git.MergeOpts{Source: "feature", Target: "main", Strategy: "fast-forward"})
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
	// Review round 1 (P3): the whole table, so a reclassification — "log"
	// moved to diff would silently drop GIT_ALLOC_LIMIT and reopen the
	// commit-message vector — is a visible test change, not only a
	// passing build. TestCommands_RunUnderTheirClassEnvironment pins the
	// same table by behaviour.
	want := map[string]string{
		"read_file": "read", "list_files": "read", "log": "read", "head": "read", "ref-sha": "read",
		"rev-parse": "read", "merge-base": "read", "tree_entry": "read",
		"diff":     "diff",
		"checkout": "mutate", "merge": "mutate", "commit": "mutate", "add": "mutate", "branch": "mutate",
		"config": "mutate", "push": "mutate",
	}
	if got := git.OpClassesForTest(); !reflect.DeepEqual(got, want) {
		t.Errorf("op classes = %v\nwant %v", got, want)
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
		"diff)\n" +
		"  if [ \"$LC_ALL\" = C ]; then echo \"fatal: bad revision 'x'\" >&2; else echo \"Schwerwiegend: ungueltige Revision 'x'\" >&2; fi\n" +
		"  exit 128;;\n" +
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

	// Review round 1 (P3): diff runs in the C locale too (as the
	// CHANGELOG says), so a miss a translated git words differently is
	// still not-found, not a permanent error.
	_, err = git.NewCLIClient(dir).Diff(ctx, commit, commit)
	var ge *git.GitError
	if !errors.As(err, &ge) || ge.Kind != git.ErrKindNotFound {
		t.Fatalf("Diff on a translated host: %v, want ErrKindNotFound (diff must run in the C locale)", err)
	}
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
	mr, err := c.Merge(ctx, git.MergeOpts{Source: "feature", Target: "main"})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if mr.SHA != tip || !mr.FastForward {
		t.Fatalf("Merge = %+v, want SHA %s and FastForward (the result line was truncated away)", mr, tip)
	}
}

// The stderr sink's ring against a reference: for every total length
// around the head/tail boundaries and several chunkings, the kept text
// is the input itself when it fits, otherwise its head, the marker and
// its last bytes.
func TestStderr_RingMatchesReference(t *testing.T) {
	const head = 32 << 10
	keepTail := git.MaxStderrBytesForTest - head - len("\n…[truncated]…\n")
	in := make([]byte, 3*git.MaxStderrBytesForTest)
	for i := range in {
		in[i] = byte('a' + i%26)
	}
	for _, total := range []int{0, 1, head - 1, head, head + 1, head + keepTail - 1, head + keepTail,
		head + keepTail + 1, head + 2*keepTail, head + 2*keepTail + 7, len(in)} {
		for _, chunk := range []int{1, 7, 4096, 32 << 10, keepTail, keepTail + 1, len(in)} {
			got := git.StderrKeptForTest(in[:total], chunk)
			want := string(in[:total])
			if total > head+keepTail {
				want = string(in[:head]) + "\n…[truncated]…\n" + string(in[total-keepTail:total])
			}
			if got != want {
				t.Fatalf("total %d chunk %d: kept %d bytes, want %d (prefix/suffix mismatch)", total, chunk, len(got), len(want))
			}
		}
	}
}

// Codex review round 4: Clone's stderr — which relays the remote's
// sideband — is bounded too. A git that writes 8 MiB to stderr before
// failing costs well under a MiB, and the classification still sees the
// final line.
func TestClone_StderrIsBounded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell wrapper")
	}
	bin := t.TempDir()
	script := "#!/bin/sh\n" +
		"i=0; while [ $i -lt 256 ]; do printf 'remote: %032768d\\n' 0 >&2; i=$((i+1)); done\n" +
		"echo \"fatal: repository 'x' not found\" >&2; exit 128\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil { //nolint:gosec // G306: an executable test wrapper
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	c := git.NewCLIClient(t.TempDir())
	var err error
	alloc := allocDuring(func() {
		err = c.Clone(context.Background(), "https://example.invalid/r.git", filepath.Join(t.TempDir(), "r"))
	})
	var ge *git.GitError
	if !errors.As(err, &ge) || ge.Kind != git.ErrKindNotFound {
		t.Fatalf("Clone = %v, want the final line classified not_found", err)
	}
	if len(ge.Message) > git.MaxStderrBytesForTest {
		t.Fatalf("kept %d bytes of stderr, want <= %d", len(ge.Message), git.MaxStderrBytesForTest)
	}
	if alloc >= 1<<20 {
		t.Fatalf("Clone allocated %d KiB for 8 MiB of stderr; want < 1 MiB", alloc>>10)
	}
}

// writeTemp writes body to a fresh temporary file and returns its path.
func writeTemp(t *testing.T, body []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func assertGitAllocRefusal(t *testing.T, label string, err error, op string, limit int64) {
	t.Helper()
	var ot *git.OutputTooLargeError
	if !errors.As(err, &ot) || ot.Op != op || ot.Limit != limit || !ot.GitAlloc {
		t.Errorf("%s: err = %v (%+v), want OutputTooLargeError{Op:%q Limit:%d GitAlloc:true}", label, err, ot, op, limit)
		return
	}
	if ge, ok := err.(*git.GitError); !ok || ge.Kind != git.ErrKindPermanent {
		t.Errorf("%s: want a permanent *git.GitError, got %T %v", label, err, err)
	}
}

// Review round 1 (P3): every read op runs under GIT_ALLOC_LIMIT, pinned
// by behaviour on real git, not only by the op table. A tree object and
// a commit object each over a 1 MiB alloc limit — both well under the
// stdout ceiling, so only git's allocation limit can refuse them — make
// ls-tree (ListFiles, TreeEntryType), log (Log, HasCommitWithTrailer),
// merge-base and ReadFile's probe refuse with GitAlloc, while the default
// client reads all of them. Moving any of these ops out of the read
// class makes its row succeed and fail the test.
func TestReadCommands_GitAllocLimitBoundsEveryReadOp(t *testing.T) {
	ctx := context.Background()
	const alloc = 1 << 20
	files := map[string][]byte{}
	for i := 0; i < 40000; i++ {
		files[fmt.Sprintf("f-%05d.md", i)] = []byte("x\n")
	}
	repo, commit := blobRepo(t, files) // one root tree of 40 000 entries
	if n, _ := strconv.Atoi(t3git(t, repo, nil, "cat-file", "-s", commit+"^{tree}")); n <= alloc {
		t.Fatalf("root tree is %d bytes, want > %d", n, alloc)
	}
	msg := writeTemp(t, bytes.Repeat([]byte("m"), 2<<20))
	big := t3git(t, repo, nil, "commit-tree", "-p", commit, "-F", msg, commit+"^{tree}")
	t3git(t, repo, nil, "update-ref", "refs/heads/main", big)
	t3git(t, repo, nil, "symbolic-ref", "HEAD", "refs/heads/main")

	small := git.NewCLIClient(repo, git.WithMaxBlobBytes(64<<10), git.WithMaxGitAllocBytes(alloc))
	if _, _, a := small.ReadBoundsForTest(); a != alloc {
		t.Fatalf("alloc limit = %d, want %d", a, alloc)
	}
	def := git.NewCLIClient(repo)
	for _, tc := range []struct {
		label, op string
		run       func(c *git.CLIClient) error
	}{
		{"ListFiles", "list_files", func(c *git.CLIClient) error { _, err := c.ListFiles(ctx, commit, ""); return err }},
		{"TreeEntryType", "tree_entry", func(c *git.CLIClient) error {
			_, err := c.TreeEntryType(ctx, commit, "f-00001.md")
			return err
		}},
		{"Log", "log", func(c *git.CLIClient) error { _, err := c.Log(ctx, git.LogOpts{Limit: 1}); return err }},
		{"HasCommitWithTrailer", "log", func(c *git.CLIClient) error {
			_, _, err := c.HasCommitWithTrailer(ctx, "Run-ID", "r")
			return err
		}},
		{"MergeBase", "merge-base", func(c *git.CLIClient) error { _, err := c.MergeBase(ctx, big, commit); return err }},
	} {
		assertGitAllocRefusal(t, tc.label, tc.run(small), tc.op, alloc)
		if err := tc.run(def); err != nil {
			t.Errorf("%s with the default bounds: %v, want no refusal", tc.label, err)
		}
	}
	// ReadFile's probe resolves the path through the same tree.
	_, err := small.ReadFile(ctx, commit, "f-00001.md")
	var tl *git.BlobTooLargeError
	if !errors.As(err, &tl) || tl.GitAllocLimit != alloc || tl.Size != -1 {
		t.Errorf("ReadFile through an over-limit tree: %v (%+v), want BlobTooLargeError{Size:-1 GitAllocLimit:%d}", err, tl, alloc)
	}
	if body, err := def.ReadFile(ctx, commit, "f-00001.md"); err != nil || string(body) != "x\n" {
		t.Errorf("default ReadFile = %q, %v", body, err)
	}
}

// Review round 1 (P3): every git process the client starts runs under
// exactly its class's environment. A wrapper records each invocation's
// argv and environment; the inherited environment carries a non-C
// locale, no GIT_ALLOC_LIMIT and an unrelated GIT_CONFIG_PARAMETERS
// entry, which every class must keep.
func TestCommands_RunUnderTheirClassEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell wrapper")
	}
	ctx := context.Background()
	dir := t.TempDir()
	t3git(t, dir, nil, "init", "-q", "-b", "main")
	t3git(t, dir, nil, "config", "user.email", "t@t")
	t3git(t, dir, nil, "config", "user.name", "t")
	t3git(t, dir, nil, "config", "maintenance.auto", "false")
	t3git(t, dir, nil, "config", "gc.auto", "0")
	writeFile(t, dir, "f.md", []byte("one\n"))
	t3git(t, dir, nil, "add", "f.md")
	t3git(t, dir, nil, "commit", "-qm", "one")
	t3git(t, dir, nil, "checkout", "-q", "-b", "feature")
	writeFile(t, dir, "g.md", []byte("two\n"))
	t3git(t, dir, nil, "add", "g.md")
	t3git(t, dir, nil, "commit", "-qm", "two")
	t3git(t, dir, nil, "checkout", "-q", "main")
	remote := t.TempDir()
	t3git(t, remote, nil, "init", "-q", "--bare")
	t3git(t, dir, nil, "remote", "add", "origin", remote)

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "env.log")
	script := "#!/bin/sh\n" +
		"{ for a in \"$@\"; do printf '%s\\037' \"$a\"; done\n" +
		"  printf '\\036%s\\036%s\\036%s\\036%s\\n' \"${GIT_ALLOC_LIMIT-unset}\" \"${LC_ALL-unset}\" \"${LANGUAGE-unset}\" \"${GIT_CONFIG_PARAMETERS-unset}\"\n" +
		"} >> \"$T003_ENVLOG\"\n" +
		"exec " + realGit + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil { //nolint:gosec // G306: an executable test wrapper
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("T003_ENVLOG", logPath)
	t.Setenv("LC_ALL", "POSIX") // not C, and never translated
	t.Setenv("LANGUAGE", "t003")
	const inherited = "'t003.inherited=yes'"
	t.Setenv("GIT_CONFIG_PARAMETERS", inherited)
	t.Setenv("GIT_ALLOC_LIMIT", "")
	os.Unsetenv("GIT_ALLOC_LIMIT")

	c := git.NewCLIClient(dir)
	blob, _, alloc := c.ReadBoundsForTest()
	calls := []struct {
		name string
		run  func() error
	}{
		{"ReadFile", func() error { _, err := c.ReadFile(ctx, "main", "f.md"); return err }},
		{"ListFiles", func() error { _, err := c.ListFiles(ctx, "main", ""); return err }},
		{"Log", func() error { _, err := c.Log(ctx, git.LogOpts{Limit: 1}); return err }},
		{"HasCommitWithTrailer", func() error { _, _, err := c.HasCommitWithTrailer(ctx, "Run-ID", "r"); return err }},
		{"Head", func() error { _, err := c.Head(ctx); return err }},
		{"RefSHA", func() error { _, err := c.RefSHA(ctx, "HEAD"); return err }},
		{"MergeBase", func() error { _, err := c.MergeBase(ctx, "main", "feature"); return err }},
		{"TreeEntryType", func() error { _, err := c.TreeEntryType(ctx, "main", "f.md"); return err }},
		{"Diff", func() error { _, err := c.Diff(ctx, "main", "feature"); return err }},
		{"CreateBranch", func() error { return c.CreateBranch(ctx, "n1", "main") }},
		{"Checkout", func() error { return c.Checkout(ctx, "n1") }},
		{"WriteAndStageFile", func() error { return c.WriteAndStageFile(ctx, "h.md", "three\n") }},
		{"Commit", func() error { _, err := c.Commit(ctx, git.CommitOpts{Message: "three"}); return err }},
		{"Merge", func() error { _, err := c.Merge(ctx, git.MergeOpts{Source: "feature", Target: "n1"}); return err }},
		{"Push", func() error { return c.Push(ctx, "origin", "n1") }},
		{"DeleteBranch", func() error {
			if err := c.Checkout(ctx, "main"); err != nil {
				return err
			}
			return c.DeleteBranch(ctx, "n1")
		}},
	}
	for _, call := range calls {
		if err := call.run(); err != nil {
			t.Fatalf("%s: %v", call.name, err)
		}
	}

	cache := fmt.Sprintf("'core.deltaBaseCacheLimit=%d'", blob)
	type env struct{ alloc, lcAll, language, params string }
	readEnv := env{fmt.Sprint(alloc), "C", "", inherited + " " + cache}
	want := map[string]env{
		"cat-file": readEnv, "ls-tree": readEnv, "log": readEnv, "rev-parse": readEnv, "merge-base": readEnv,
		"diff":     {"unset", "C", "", inherited + " " + cache},
		"show-ref": {"unset", "C", "", inherited}, // the exact branch lookup (refgrammar.go)
		"branch":   {"unset", "POSIX", "t003", inherited}, "switch": {"unset", "POSIX", "t003", inherited},
		"add": {"unset", "POSIX", "t003", inherited}, "commit": {"unset", "POSIX", "t003", inherited},
		"merge": {"unset", "POSIX", "t003", inherited}, "push": {"unset", "POSIX", "t003", inherited},
	}
	raw, err := os.ReadFile(logPath) //nolint:gosec // G304: the test's own temp file
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		parts := strings.Split(line, "\x1e")
		if len(parts) != 5 {
			t.Fatalf("unparseable record %q", line)
		}
		sub := ""
		args := strings.Split(strings.TrimSuffix(parts[0], "\x1f"), "\x1f")
		for i := 0; i < len(args); i++ {
			if args[i] == "-c" {
				i++
				continue
			}
			if !strings.HasPrefix(args[i], "-") {
				sub = args[i]
				break
			}
		}
		w, ok := want[sub]
		if !ok {
			// A git child git started itself (local transport, hooks)
			// inherits its parent's environment; only the client's own
			// commands are pinned here.
			if sub == "receive-pack" || sub == "upload-pack" || sub == "pack-objects" || sub == "index-pack" ||
				sub == "unpack-objects" || sub == "maintenance" || sub == "gc" || sub == "rev-list" {
				continue
			}
			t.Errorf("git %q ran but has no expected environment (args %q)", sub, args)
			continue
		}
		seen[sub] = true
		if got := (env{parts[1], parts[2], parts[3], parts[4]}); got != w {
			t.Errorf("git %s (args %q): env %+v, want %+v", sub, args, got, w)
		}
	}
	for sub := range want {
		if !seen[sub] {
			t.Errorf("git %s never ran; the test no longer covers its class", sub)
		}
	}
}

// Review round 1 (P2): the delta-base cache pin is appended to whatever
// GIT_CONFIG_PARAMETERS the process inherited (either quoting form), and
// real git reads it — as the last, winning value — alongside the
// inherited entries.
func TestDeltaBaseCacheEnv(t *testing.T) {
	for _, tc := range []struct{ inherited, want string }{
		{"", "GIT_CONFIG_PARAMETERS='core.deltaBaseCacheLimit=4194304'"},
		{"  ", "GIT_CONFIG_PARAMETERS='core.deltaBaseCacheLimit=4194304'"},
		{"'a.b=c'", "GIT_CONFIG_PARAMETERS='a.b=c' 'core.deltaBaseCacheLimit=4194304'"},
		{"'a.b'='c'", "GIT_CONFIG_PARAMETERS='a.b'='c' 'core.deltaBaseCacheLimit=4194304'"},
	} {
		if got := git.DeltaBaseCacheEnvForTest(tc.inherited, 4<<20); got != tc.want {
			t.Errorf("inherited %q: %q, want %q", tc.inherited, got, tc.want)
		}
	}
	dir := t.TempDir()
	t3git(t, dir, nil, "init", "-q")
	t3git(t, dir, nil, "config", "core.deltaBaseCacheLimit", "999999999") // a repository value never wins
	for _, inherited := range []string{"'t003.x=old-style'", "'t003.x'='new-style'"} {
		if strings.Contains(inherited, "'='") && !gitAtLeast(t, 2, 31) {
			continue // the 'key'='value' form arrived in git 2.31
		}
		cmd := exec.Command("git", "config", "--get-regexp", `^(t003\.x|core\.deltabasecachelimit)$`)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), git.DeltaBaseCacheEnvForTest(inherited, 4<<20))
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git config under %q: %v", inherited, err)
		}
		got := strings.Fields(string(out))
		// --get-regexp prints every value in precedence order, the
		// command-line ones last.
		if len(got) < 4 || got[len(got)-2] != "core.deltabasecachelimit" || got[len(got)-1] != "4194304" ||
			!strings.Contains(string(out), "t003.x") {
			t.Errorf("under %q git sees %q, want the inherited entry kept and the 4194304 pin last", inherited, out)
		}
	}
}

func gitAtLeast(t *testing.T, major, minor int) bool {
	t.Helper()
	m := regexp.MustCompile(`git version (\d+)\.(\d+)`).FindStringSubmatch(t3git(t, t.TempDir(), nil, "version"))
	if m == nil {
		return false
	}
	ma, _ := strconv.Atoi(m[1])
	mi, _ := strconv.Atoi(m[2])
	return ma > major || (ma == major && mi >= minor)
}

// Review round 1 (P3): the stdout sink keeps exactly what fits, grows in
// powers of two (never past the limit), and a text sink hands its buffer
// to String() without a copy — so run() holds a command's output once.
func TestStdoutSink_GrowthAndHandOver(t *testing.T) {
	in := repeatTo(1<<20 + 12345)
	for _, text := range []bool{false, true} {
		for _, chunk := range []int{1 << 10, 7919, 32 << 10, 64 << 10} {
			for _, limit := range []int64{4 << 10, 1 << 20, 3 << 20} {
				label := fmt.Sprintf("text=%v chunk=%d limit=%d", text, chunk, limit)
				drain := git.StdoutSinkForTest(in, chunk, limit, false, text)
				keep := min(int64(len(in)), limit)
				if drain.Kept != string(in[:keep]) || drain.Overflow != (int64(len(in)) > limit) || drain.WriteErr != nil {
					t.Fatalf("%s draining: kept %d bytes, overflow %v, err %v; want %d", label, len(drain.Kept), drain.Overflow, drain.WriteErr, keep)
				}
				fail := git.StdoutSinkForTest(in, chunk, limit, true, text)
				if int64(len(in)) > limit && (fail.WriteErr == nil || !fail.Overflow) {
					t.Fatalf("%s failing: err %v overflow %v, want the limit error", label, fail.WriteErr, fail.Overflow)
				}
				for i, c := range drain.Caps {
					if int64(c) > limit || (int64(c) != limit && c&(c-1) != 0) || c < 512 {
						t.Fatalf("%s: capacity %d after write %d, want a power of two >= 512 or exactly the limit", label, c, i)
					}
				}
				if text && drain.HandOverAllocs != 0 {
					t.Errorf("%s: String() allocated %v times, want 0 (no copy)", label, drain.HandOverAllocs)
				}
			}
		}
	}
}

// Review round 1 (P3): run() returns the output buffer itself. A Log
// whose output is 3.5 MiB costs its buffer's growth (powers of two up
// to 4 MiB: under 8 MiB in all) and nothing more; converting a []byte
// buffer to a string added a full copy (3.5 MiB) on top, and held the
// output twice at its peak.
func TestRun_OutputIsHeldOnce(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t3git(t, dir, nil, "init", "-q", "-b", "main")
	const msgLen = 3<<20 + 512<<10
	tree := t3git(t, dir, nil, "write-tree")
	commit := t3git(t, dir, nil, "commit-tree", tree, "-F", writeTemp(t, bytes.Repeat([]byte("m"), msgLen)))
	t3git(t, dir, nil, "update-ref", "refs/heads/main", commit)
	c := git.NewCLIClient(dir)
	var commits []git.CommitInfo
	var err error
	alloc := allocDuring(func() { commits, err = c.Log(ctx, git.LogOpts{Limit: 1}) })
	if err != nil || len(commits) != 1 || len(commits[0].Message) != msgLen {
		t.Fatalf("Log: %d commits, %v", len(commits), err)
	}
	if bound := uint64(8<<20 + msgLen/2); alloc >= bound {
		t.Errorf("Log allocated %d KiB, want < %d KiB (buffer growth only, no copy of the output)", alloc>>10, bound>>10)
	}
	t.Logf("Log of %d KiB allocated %d KiB", msgLen>>10, alloc>>10)
}
