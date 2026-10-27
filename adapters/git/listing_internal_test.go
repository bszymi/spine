package git

// INIT-024 TASK-004 AC 3 (review finding N-6): the streamed listings
// return exactly what the buffered ones did. The reference
// implementations below are ListFiles' and parseNameStatusZ's bodies at
// a560c210 (INIT-023 TASK-003), unchanged, applied to the same git
// output; the streamed collectors are also fed in every chunk size, so a
// record split at any byte boundary parses the same.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// bufferedListFiles is ListFiles' parse at a560c210 over `output`.
func bufferedListFiles(output, pattern string) ([]string, error) {
	if output == "" {
		return nil, nil
	}
	if !strings.HasSuffix(output, "\x00") {
		return nil, &GitError{Kind: ErrKindPermanent, Op: "list_files", Message: "unterminated `git ls-tree -z` output"}
	}
	var files []string
	for rest := output; rest != ""; {
		var p string
		p, rest, _ = strings.Cut(rest, "\x00")
		if p == "" {
			return nil, &GitError{Kind: ErrKindPermanent, Op: "list_files", Message: "empty path in `git ls-tree -z` output"}
		}
		if pattern == "" || matchSimpleGlob(pattern, p) {
			files = append(files, p)
		}
	}
	return files, nil
}

// bufferedNameStatusZ is parseNameStatusZ at a560c210.
func bufferedNameStatusZ(out string) ([]FileDiff, error) {
	if out == "" {
		return nil, nil
	}
	if !strings.HasSuffix(out, "\x00") {
		return nil, fmt.Errorf("%w: unterminated record", errMalformedDiff)
	}
	rest := out
	field := func() (string, bool) {
		if rest == "" {
			return "", false
		}
		f, tail, _ := strings.Cut(rest, "\x00")
		rest = tail
		return f, true
	}
	var diffs []FileDiff
	for rest != "" {
		status, _ := field()
		if !validDiffStatus(status) {
			return nil, fmt.Errorf("%w: unexpected status field at record %d", errMalformedDiff, len(diffs)+1)
		}
		paths := 1
		if status[0] == 'R' || status[0] == 'C' {
			paths = 2
		}
		p1, ok := field()
		p2 := ""
		if ok && paths == 2 {
			p2, ok = field()
		}
		if !ok {
			return nil, fmt.Errorf("%w: record %d is missing a path", errMalformedDiff, len(diffs)+1)
		}
		if p1 == "" || (paths == 2 && p2 == "") {
			return nil, fmt.Errorf("%w: record %d has an empty path", errMalformedDiff, len(diffs)+1)
		}
		var fd FileDiff
		switch status[0] {
		case 'A':
			fd = FileDiff{Path: p1, Status: "added"}
		case 'D':
			fd = FileDiff{Path: p1, Status: "deleted"}
		case 'R':
			fd = FileDiff{OldPath: p1, Path: p2, Status: "renamed"}
		case 'C':
			fd = FileDiff{Path: p2, Status: "added"}
		default:
			fd = FileDiff{Path: p1, Status: "modified"}
		}
		diffs = append(diffs, fd)
	}
	return diffs, nil
}

// streamedListFiles feeds output to ListFiles' collector in chunk-sized
// writes, with no listing limit.
func streamedListFiles(output []byte, chunk int, pattern string) ([]string, *GitError) {
	var keep func(string) bool
	if pattern != "" {
		keep = func(p string) bool { return matchSimpleGlob(pattern, p) }
	}
	s := newPathCollector("list_files", math.MaxInt64, keep)
	feedForTest(&s.nulRecords, output, chunk)
	if err := s.refusal(); err != nil {
		return nil, err
	}
	if s.unterminated() {
		return nil, unterminatedListing()
	}
	return s.paths, nil
}

func listingGit(t *testing.T, dir string, stdin []byte, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, errb.String())
	}
	return out.String()
}

// The -z corpus INIT-023 TASK-003 pinned (paths git C-quotes without -z,
// a leading-space path that sorts first) plus nesting, a trailing space
// and the shapes every pattern form matches.
var listingCorpus = []string{
	" lead.md",
	`"quoted.md`,
	"r\xc3\xa9view.md",
	"tab\tfile.md",
	"new\nline.md",
	"plain.md",
	"trail.md ",
	"dir/a.md",
	"dir/b.txt",
	"dir/sub/c.md",
	"workflows/task.yaml",
	"x/dir/y.md",
	"\xe6\x97\xa5\xe6\x9c\xac/\xe8\xaa\x9e.md",
}

func TestListFiles_StreamedEqualsBuffered(t *testing.T) {
	dir := t.TempDir()
	listingGit(t, dir, nil, "init", "-q", "-b", "main")
	for _, p := range listingCorpus {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x "+p+"\n"), 0o644); err != nil { //nolint:gosec // G703: test-authored corpus under t.TempDir()
			t.Fatal(err)
		}
	}
	listingGit(t, dir, nil, "add", "-A")
	listingGit(t, dir, nil, "commit", "-q", "-m", "corpus")
	raw := listingGit(t, dir, nil, "ls-tree", "-r", "--name-only", "-z", "--end-of-options", "HEAD")
	c := NewCLIClient(dir)
	ctx := context.Background()

	for _, pattern := range []string{"", "*", "*.md", "dir/*", "dir", ".md", "workflows/", "nomatch"} {
		want, wantErr := bufferedListFiles(raw, pattern)
		if wantErr != nil {
			t.Fatalf("reference ListFiles(%q): %v", pattern, wantErr)
		}
		got, err := c.ListFiles(ctx, "HEAD", pattern)
		if err != nil {
			t.Fatalf("ListFiles(%q): %v", pattern, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("ListFiles(%q):\n got  %q\n want %q", pattern, got, want)
		}
		for chunk := 1; chunk <= len(raw); chunk++ {
			got, gerr := streamedListFiles([]byte(raw), chunk, pattern)
			if gerr != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("streamed ListFiles(%q) in %d-byte writes: %q, %v; want %q", pattern, chunk, got, gerr, want)
			}
		}
	}
	// ListFilesFunc with the pattern's predicate is ListFiles.
	got, err := c.ListFilesFunc(ctx, "HEAD", func(p string) bool { return matchSimpleGlob("*.md", p) })
	if want, _ := bufferedListFiles(raw, "*.md"); err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("ListFilesFunc(*.md) = %q, %v; want %q", got, err, want)
	}
	if got, err := c.ListFilesFunc(ctx, "HEAD", nil); err != nil || !reflect.DeepEqual(got, mustBuffered(t, raw)) {
		t.Errorf("ListFilesFunc(nil) = %q, %v; want every path", got, err)
	}

	// Malformed streams: the same refusal as the buffered parse.
	for _, out := range []string{"a.md", "a.md\x00b.md", "a.md\x00\x00b.md\x00", "\x00"} {
		_, wantErr := bufferedListFiles(out, "")
		for chunk := 1; chunk <= len(out); chunk++ {
			_, gerr := streamedListFiles([]byte(out), chunk, "")
			if gerr == nil || wantErr == nil || gerr.Error() != wantErr.Error() {
				t.Errorf("malformed %q in %d-byte writes: %v, want %v", out, chunk, gerr, wantErr)
			}
		}
	}
}

func mustBuffered(t *testing.T, raw string) []string {
	t.Helper()
	files, err := bufferedListFiles(raw, "")
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// TestDiff_StreamedEqualsBuffered is AC 3 for Diff: a real rename, copy,
// type change, add, delete and modify over the corpus, and the malformed
// shapes TestParseNameStatusZ pins, in every chunk size.
func TestDiff_StreamedEqualsBuffered(t *testing.T) {
	dir := t.TempDir()
	listingGit(t, dir, nil, "init", "-q", "-b", "main")
	write := func(p, body string) {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil { //nolint:gosec // G703: test-authored corpus under t.TempDir()
			t.Fatal(err)
		}
	}
	long := strings.Repeat("a line long enough to make renames detectable\n", 40)
	for _, p := range listingCorpus {
		write(p, long+p+"\n")
	}
	write("gone.md", long+"gone\n")
	write("typed.md", "t\n")
	listingGit(t, dir, nil, "add", "-A")
	listingGit(t, dir, nil, "commit", "-q", "-m", "from")
	from := strings.TrimSpace(listingGit(t, dir, nil, "rev-parse", "HEAD"))
	if err := os.MkdirAll(filepath.Join(dir, "moved"), 0o755); err != nil {
		t.Fatal(err)
	}
	listingGit(t, dir, nil, "mv", "plain.md", "moved/r\xc3\xa9named.md")
	write("dir/a.md", long+"changed\n")
	listingGit(t, dir, nil, "rm", "-q", "gone.md")
	write("added new.md", "new\n")
	if err := os.Remove(filepath.Join(dir, "typed.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("plain-target", filepath.Join(dir, "typed.md")); err != nil {
		t.Fatal(err)
	}
	listingGit(t, dir, nil, "add", "-A")
	listingGit(t, dir, nil, "commit", "-q", "-m", "to")
	to := strings.TrimSpace(listingGit(t, dir, nil, "rev-parse", "HEAD"))

	c := NewCLIClient(dir)
	for _, args := range [][]string{
		{"diff", "--name-status", "-z", "--ignore-submodules=none", "--end-of-options", from, to, "--"},
		{"-c", "diff.renames=copies", "diff", "--name-status", "-z", "-C", "-C", "--end-of-options", from, to, "--"},
	} {
		raw := listingGit(t, dir, nil, args...)
		want, err := bufferedNameStatusZ(raw)
		if err != nil {
			t.Fatalf("reference parse of %v: %v", args, err)
		}
		if len(want) < 5 {
			t.Fatalf("fixture %v: only %d records", args, len(want))
		}
		for chunk := 1; chunk <= len(raw); chunk++ {
			got, gerr := ParseNameStatusZChunkedForTest(raw, chunk)
			if gerr != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("streamed parse of %v in %d-byte writes: %+v, %v; want %+v", args, chunk, got, gerr, want)
			}
		}
	}
	raw := listingGit(t, dir, nil, "diff", "--name-status", "-z", "--ignore-submodules=none", "--end-of-options", from, to, "--")
	want, _ := bufferedNameStatusZ(raw)
	got, err := c.Diff(context.Background(), from, to)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("Diff = %+v, %v; want %+v", got, err, want)
	}

	for _, out := range []string{
		"M\x00a.md", "Q!\x00a.md\x00", "\x00a.md\x00", "R100\x00a.md\x00", "M\x00\x00", "R100\x00\x00b\x00",
		"R100\x00a\x00\x00", "M\x00a.md\x00R100\x00a\x00", "C\x00a\x00b\x00M", "A\x00",
	} {
		_, wantErr := bufferedNameStatusZ(out)
		for chunk := 1; chunk <= len(out); chunk++ {
			_, gerr := ParseNameStatusZChunkedForTest(out, chunk)
			if gerr == nil || wantErr == nil || gerr.Error() != wantErr.Error() || !errors.Is(gerr, errMalformedDiff) {
				t.Errorf("malformed %q in %d-byte writes: %v, want %v", out, chunk, gerr, wantErr)
			}
		}
	}
}

// TestNULRecords_CarryIsBoundedByTheOwner pins the shared reader's
// contract for its two owners: a record that spans writes is held only
// while the owner's pending bound admits it, and the first refusal stops
// the reader for good.
func TestNULRecords_CarryIsBoundedByTheOwner(t *testing.T) {
	var seen []string
	stopped := 0
	s := &nulRecords{
		pending: func(n int64) error {
			if n > 8 {
				return errors.New("too long")
			}
			return nil
		},
		record: func(rec []byte) error { seen = append(seen, string(rec)); return nil },
		stop:   func() { stopped++ },
	}
	for _, w := range []string{"ab", "c\x00de", "f\x00", "0123", "45678"} {
		_, _ = s.Write([]byte(w))
	}
	if !reflect.DeepEqual(seen, []string{"abc", "def"}) || stopped != 1 || !s.stopped {
		t.Fatalf("seen %q, stopped %d (%v); want abc, def and one stop at the 9-byte record", seen, stopped, s.stopped)
	}
	if n, err := s.Write([]byte("x\x00")); n != 0 || !errors.Is(err, errStopListing) {
		t.Fatalf("Write after the stop = %d, %v; want 0, errStopListing", n, err)
	}
	if len(s.carry) > 8 {
		t.Fatalf("carried %d bytes past the bound", len(s.carry))
	}
}
