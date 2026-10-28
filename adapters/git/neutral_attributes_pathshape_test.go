package git_test

// INIT-024 integration A (codex, phase 3): the renormalisation's
// fingerprints passed every path to `hash-object --stdin-paths`, one per
// line. git C-unquotes a line that starts with `"` and drops a CR before
// the newline, so a tracked name with a leading quote failed the
// renormalisation ("line is badly quoted") or was hashed as another file,
// a name ending in CR could not be opened, and a name with a newline was
// never fingerprinted. Every shape is now passed as an argument.

import (
	"context"
	"crypto/sha1" //nolint:gosec // G505: git's own object id, not a security hash
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/testutil"
)

// blobID is git's SHA-1 object id of b as a blob.
func blobID(b []byte) string {
	h := sha1.New() //nolint:gosec // G401: git's own object id
	fmt.Fprintf(h, "blob %d\x00", len(b))
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}

// rpConverted is a repository with a.txt and a tracked file named name,
// both checked out under `* text eol=crlf` and no neutral line yet. It
// returns false when the file system refuses the name (APFS refuses a
// name that is not UTF-8).
func rpConverted(t *testing.T, name string) (string, bool) {
	t.Helper()
	repo := testutil.NewTempRepo(t)
	testutil.WriteFile(t, repo, ".gitattributes", "* text eol=crlf\n")
	testutil.WriteFile(t, repo, "a.txt", "the alias target\nline\n")
	if err := os.WriteFile(filepath.Join(repo, name), []byte("the shape\nline\n"), 0o600); err != nil {
		return "", false
	}
	tbGit(t, repo, nil, "add", "-A")
	tbGit(t, repo, nil, "commit", "-q", "-m", "files")
	for _, f := range []string{"a.txt", name} {
		if err := os.Remove(filepath.Join(repo, f)); err != nil {
			t.Fatal(err)
		}
	}
	tbGit(t, repo, nil, "checkout", "--", ".")
	if b, _ := os.ReadFile(filepath.Join(repo, name)); string(b) != "the shape\r\nline\r\n" {
		t.Fatalf("fixture: %q not converted: %q", name, b)
	}
	return repo, true
}

func TestNeutralAttributes_EveryPathShapeIsFingerprinted(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct{ shape, name string }{
		{"leading quote", "\"quoted.txt"},
		{"quoted alias of another file", "\"a.txt\""},
		{"backslash", `back\slash.txt`},
		{"backslash escape inside quotes", `"a\164xt"`},
		{"newline", "new\nline.txt"},
		{"trailing CR", "ends-in-cr\r"},
		{"leading dash", "-n"},
		{"not UTF-8", "\xff\xfe-latin1.txt"},
	} {
		t.Run(tc.shape, func(t *testing.T) {
			repo, ok := rpConverted(t, tc.name)
			if !ok {
				if runtime.GOOS == "darwin" {
					t.Skipf("the file system refuses the name %q (git-floor runs this shape on Linux)", tc.name)
				}
				t.Fatalf("cannot create %q", tc.name)
			}
			onDisk, err := os.ReadFile(filepath.Join(repo, tc.name))
			if err != nil {
				t.Fatal(err)
			}

			// A first call whose budget HEAD is over: it records, writes the
			// line, proves the conversion-only files, then is refused.
			tight := git.NewCLIClient(repo, git.WithTreeBudget(git.TreeBudget{MaxEntries: 1}))
			err = tight.EnsureNeutralWorkTree(ctx)
			var tooLarge *git.TreeTooLargeError
			if !errors.As(err, &tooLarge) || !errors.Is(err, git.ErrRenormalisePending) {
				t.Fatalf("first call: %v; want the HEAD-over-budget refusal as ErrRenormalisePending (the proof is taken before it)", err)
			}
			gd := tbGit(t, repo, nil, "rev-parse", "--absolute-git-dir")
			_, proof, ok, err := git.ReadRenormaliseRecordForTest(filepath.Join(gd, git.RenormaliseMarkerName))
			if err != nil || !ok || proof == nil {
				t.Fatalf("marker after the first call: ok=%v proof=%v err=%v; want a proven record", ok, proof, err)
			}
			if got, want := proof[tc.name], "blob "+blobID(onDisk); got != want {
				t.Fatalf("fingerprint of %q = %q; want %q (its own bytes)", tc.name, got, want)
			}

			// The retry, with a budget HEAD fits, rewrites every proven file.
			if err := git.NewCLIClient(repo, git.WithTreeBudget(git.TreeBudget{})).EnsureNeutralWorkTree(ctx); err != nil {
				t.Fatalf("retry: %v", err)
			}
			if b := rnRead(t, repo, tc.name); b != "the shape\nline\n" {
				t.Errorf("%q after the retry = %q; want its stored bytes", tc.name, b)
			}
			if b := rnRead(t, repo, "a.txt"); b != "the alias target\nline\n" {
				t.Errorf("a.txt after the retry = %q; want its stored bytes", b)
			}
			if rnMarked(t, repo) {
				t.Error("the marker is still there after the retry")
			}
		})
	}
}

// winEscapeArg is Go's syscall.EscapeArg on Windows (how os/exec quotes an
// argument for CreateProcess), copied so the bound can be checked on any
// platform.
func winEscapeArg(s string) string {
	if s == "" {
		return `""`
	}
	needsBackslash, hasSpace := false, false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"', '\\':
			needsBackslash = true
		case ' ', '\t':
			hasSpace = true
		}
	}
	if !needsBackslash && !hasSpace {
		return s
	}
	if !needsBackslash {
		return `"` + s + `"`
	}
	var b []byte
	if hasSpace {
		b = append(b, '"')
	}
	slashes := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		default:
			slashes = 0
		case '\\':
			slashes++
		case '"':
			for ; slashes > 0; slashes-- {
				b = append(b, '\\')
			}
			b = append(b, '\\')
		}
		b = append(b, c)
	}
	if hasSpace {
		for ; slashes > 0; slashes-- {
			b = append(b, '\\')
		}
		b = append(b, '"')
	}
	return string(b)
}

// TestNeutralAttributes_FingerprintBatchesFitWindows (codex round 2): a
// fingerprint batch's command line, quoted as on Windows, stays within
// CreateProcess's 32,767 UTF-16 units — for the worst quoting shapes,
// long names and names that are not ASCII — and the batches keep every
// path once, in order.
func TestNeutralAttributes_FingerprintBatchesFitWindows(t *testing.T) {
	const gitExe = `C:\Program Files\Git\cmd\git.exe` // a long but ordinary git path
	shapes := []string{
		strings.Repeat(`\"`, 40) + " x",                   // every quote after a backslash, with a space
		strings.Repeat(`\`, 79) + " ",                     // trailing backslashes doubled before the closing quote
		"dir/" + strings.Repeat("é", 60) + ".txt",         // two UTF-8 bytes, one UTF-16 unit each
		"dir/" + strings.Repeat("\U0001F600", 30) + ".md", // four bytes, two units each
		"plain/file.txt",
		strings.Repeat("long-", 2000), // one path past half the limit
	}
	var paths []string
	for i := 0; i < 3000; i++ {
		paths = append(paths, fmt.Sprintf("%s-%d", shapes[i%len(shapes)], i))
	}
	batches := git.FingerprintBatchesForTest(paths)
	if len(batches) < 2 {
		t.Fatalf("%d batch(es) for %d paths; want several", len(batches), len(paths))
	}
	var back []string
	for i, batch := range batches {
		if len(batch) == 0 {
			t.Fatalf("batch %d is empty", i)
		}
		line := winEscapeArg(gitExe) + " hash-object --"
		for _, p := range batch {
			line += " " + winEscapeArg(p)
		}
		if n := len(utf16.Encode([]rune(line))); n > 32767 {
			t.Fatalf("batch %d (%d paths): its Windows command line is %d UTF-16 units; CreateProcess takes at most 32767", i, len(batch), n)
		}
		back = append(back, batch...)
	}
	if !reflect.DeepEqual(back, paths) {
		t.Fatal("the batches do not hold every path once, in order")
	}
}

// TestNeutralAttributes_ManyConvertedFilesAreFingerprintedInBatches: end
// to end, more converted files than one batch takes are proven and then
// brought to their stored bytes.
func TestNeutralAttributes_ManyConvertedFilesAreFingerprintedInBatches(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewTempRepo(t)
	testutil.WriteFile(t, repo, ".gitattributes", "* text eol=crlf\n")
	var names []string
	for i := 0; i < 400; i++ {
		name := fmt.Sprintf("dir/%s\"quoted\\name-%03d.txt", strings.Repeat("n", 40), i)
		names = append(names, name)
		testutil.WriteFile(t, repo, name, fmt.Sprintf("file %d\nline\n", i))
	}
	if n := len(git.FingerprintBatchesForTest(names)); n < 2 {
		t.Fatalf("fixture: %d batch(es); want several", n)
	}
	tbGit(t, repo, nil, "add", "-A")
	tbGit(t, repo, nil, "commit", "-q", "-m", "files")
	if err := os.RemoveAll(filepath.Join(repo, "dir")); err != nil {
		t.Fatal(err)
	}
	tbGit(t, repo, nil, "checkout", "--", ".")
	err := git.NewCLIClient(repo, git.WithTreeBudget(git.TreeBudget{MaxEntries: 1})).EnsureNeutralWorkTree(ctx)
	if !errors.Is(err, git.ErrRenormalisePending) {
		t.Fatalf("first call: %v; want ErrRenormalisePending", err)
	}
	gd := tbGit(t, repo, nil, "rev-parse", "--absolute-git-dir")
	_, proof, ok, err := git.ReadRenormaliseRecordForTest(filepath.Join(gd, git.RenormaliseMarkerName))
	if err != nil || !ok || len(proof) != len(names) {
		t.Fatalf("proof after the first call: ok=%v err=%v n=%d; want %d", ok, err, len(proof), len(names))
	}
	if err := git.NewCLIClient(repo, git.WithTreeBudget(git.TreeBudget{})).EnsureNeutralWorkTree(ctx); err != nil {
		t.Fatalf("retry: %v", err)
	}
	for i, name := range names {
		if b := rnRead(t, repo, name); b != fmt.Sprintf("file %d\nline\n", i) {
			t.Fatalf("%q = %q; want its stored bytes", name, b)
		}
	}
}
