package git_test

// INIT-024 TASK-003 review round 2: the checkout budget's premise is that
// a checkout writes the bytes git stores. NeutralCheckoutAttributes, last
// in $GIT_COMMON_DIR/info/attributes, makes that true whatever the tree's
// own .gitattributes, the host's attribute file or its git config say.
// Fixtures are a few KB each.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/testutil"
)

// ntTree commits files (path -> content) as one tree on top of base and
// returns the commit. Paths may hold any byte but NUL and '/' handling is
// git's (mktree -z, one level per path component via a nested tree).
func ntTree(t *testing.T, repo, base string, files map[string][]byte) string {
	t.Helper()
	type entry struct{ mode, typ, oid, name string }
	dirs := map[string][]entry{}
	var dirOrder []string
	addDir := func(d string) {
		if _, ok := dirs[d]; !ok {
			dirs[d] = nil
			dirOrder = append(dirOrder, d)
		}
	}
	addDir("")
	for p, c := range files {
		oid := tbGit(t, repo, c, "hash-object", "-w", "--no-filters", "--stdin")
		dir, name := "", p
		if i := strings.LastIndexByte(p, '/'); i >= 0 {
			dir, name = p[:i], p[i+1:]
			addDir(dir)
		}
		dirs[dir] = append(dirs[dir], entry{"100644", "blob", oid, name})
	}
	var build func(d string) string
	build = func(d string) string {
		var mk strings.Builder
		for _, e := range dirs[d] {
			fmt.Fprintf(&mk, "%s %s %s\t%s\x00", e.mode, e.typ, e.oid, e.name)
		}
		for _, sub := range dirOrder {
			if sub == "" || sub == d {
				continue
			}
			parent, name := "", sub
			if i := strings.LastIndexByte(sub, '/'); i >= 0 {
				parent, name = sub[:i], sub[i+1:]
			}
			if parent == d {
				fmt.Fprintf(&mk, "040000 tree %s\t%s\x00", build(sub), name)
			}
		}
		return tbGit(t, repo, []byte(mk.String()), "mktree", "-z")
	}
	return tbGit(t, repo, nil, "commit-tree", build(""), "-p", base, "-m", "shape")
}

// writtenBytes sums the sizes, under dir, of every path the commit's tree
// lists: what a checkout of it wrote.
func writtenBytes(t *testing.T, repo, dir, commit string) int64 {
	t.Helper()
	var n int64
	for _, p := range strings.Split(strings.TrimSuffix(tbGit(t, repo, nil, "ls-tree", "-r", "-z", "--name-only", commit), "\x00"), "\x00") {
		fi, err := os.Lstat(filepath.Join(dir, p))
		if err != nil {
			t.Fatalf("%q not checked out: %v", p, err)
		}
		n += fi.Size()
	}
	return n
}

// hostileGlobalConfig points GIT_CONFIG_GLOBAL at a config that would
// re-enable every conversion a checkout or merge can make: an attributes
// file setting ident, text and eol=crlf for everything, core.autocrlf and
// core.eol, a required smudge filter that writes 1 MB, diff3 conflicts.
func hostileGlobalConfig(t *testing.T) {
	t.Helper()
	d := t.TempDir()
	gattr := filepath.Join(d, "gattr")
	if err := os.WriteFile(gattr, []byte("* ident text eol=crlf\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf("[core]\n\tattributesFile = %s\n\tautocrlf = true\n\teol = crlf\n"+
		"[filter \"x\"]\n\tsmudge = \"yes | head -c 1000000\"\n\trequired = true\n"+
		"[merge]\n\tconflictStyle = diff3\n", gattr)
	gc := filepath.Join(d, "gitconfig")
	if err := os.WriteFile(gc, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", gc)
}

// TestNeutralAttributes_EveryShapeWritesStoredBytes (review round 2,
// P1-A/P1-B, and the shapes codex rounds A1-A3 and the reviewers found
// against the attribute scanner this design replaced): under a host config
// that re-enables every conversion, a budgeted worktree (EnterBranch) and a
// budgeted checkout of the main tree each write exactly the bytes the
// measure summed, for every shape — in-tree macros, `[attr]` used as a
// pattern, quoted patterns git fails to unquote, ident, UTF-32, eol=crlf,
// crlf, filter=, a NUL, a CR, a macro named like a built-in, a macro chain
// longer than any depth limit, and attributes past any scan cap. At
// 1a2a8898 the shapes the scanner missed were admitted and written at up
// to 47x their stored size.
func TestNeutralAttributes_EveryShapeWritesStoredBytes(t *testing.T) {
	ctx := context.Background()
	ids := bytes.Repeat([]byte("$Id$\n"), 2000) // 10,000 bytes; 96,000 under ident
	var chain strings.Builder
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&chain, "[attr]m%d m%d\n", i, i+1)
	}
	chain.WriteString("[attr]m12 ident working-tree-encoding=UTF-32\n* m0\n")
	padded := append([]byte("* filter=x ident\n"), bytes.Repeat([]byte("# padding\n"), 110000)...)
	shapes := []struct {
		name  string
		files map[string][]byte
	}{
		{"reviewer combo", map[string][]byte{
			".gitattributes": []byte("[attr]m ident working-tree-encoding=UTF-32\n* m text eol=crlf filter=x conflict-marker-size=100000\n[attr] ident\n\"q\\q ident z=\"\nsub/** crlf\n"),
			"f.txt":          ids, "a": ids, "sub/g.txt": ids}},
		{"ident", map[string][]byte{".gitattributes": []byte("* ident\n"), "f.md": ids}},
		{"[attr] as a pattern", map[string][]byte{".gitattributes": []byte("[attr] ident\n"), "a": ids}},
		{"[attr] as a pattern, tab", map[string][]byte{".gitattributes": []byte("[attr]\tident\n"), "t": ids}},
		{"[attr] pattern with UTF-32", map[string][]byte{".gitattributes": []byte("[attr] ident working-tree-encoding=UTF-32\n"), "a": ids}},
		{"failed C-unquote", map[string][]byte{".gitattributes": []byte("\"x\\q ident y=\"\n"), "\"xq": ids}},
		{"failed C-unquote, glob", map[string][]byte{".gitattributes": []byte("\"\\z* ident y=\"\n"), "\"zz.md": ids}},
		{"NUL ends the line", map[string][]byte{".gitattributes": []byte("* ident\x00hidden\n"), "f.md": ids}},
		{"CR between pattern and attributes", map[string][]byte{".gitattributes": []byte("*\rident\n"), "f.md": ids}},
		{"macro named like a built-in", map[string][]byte{".gitattributes": []byte("[attr]text ident\n* text\n"), "f.md": ids}},
		{"12-macro chain", map[string][]byte{".gitattributes": []byte(chain.String()), "f.md": ids}},
		{"UTF-32", map[string][]byte{".gitattributes": []byte("* working-tree-encoding=UTF-32\n"), "f.md": ids}},
		{"text eol=crlf", map[string][]byte{".gitattributes": []byte("* text eol=crlf\n"), "f.md": ids}},
		{"crlf", map[string][]byte{".gitattributes": []byte("* crlf\n"), "f.md": ids}},
		{"filter with a required host driver", map[string][]byte{".gitattributes": []byte("* filter=x\n"), "f.md": ids}},
		{"attributes past 1 MiB", map[string][]byte{".gitattributes": padded, "f.md": ids}},
		{"no .gitattributes (host config only)", map[string][]byte{"f.md": ids}},
	}
	budget := git.TreeBudget{MaxEntries: 1000, MaxTotalBytes: 4 << 20, MaxPathBytesTotal: 1 << 20}
	for _, sh := range shapes {
		t.Run(sh.name, func(t *testing.T) {
			repo, base, _, _ := tbRepo(t)
			commit := ntTree(t, repo, base, sh.files)
			tbGit(t, repo, nil, "update-ref", "refs/heads/shape", commit)
			hostileGlobalConfig(t)
			b := budget
			st, err := b.Measure(ctx, git.NewCLIClient(repo), commit)
			if err != nil {
				t.Fatalf("measure: %v", err)
			}
			t.Setenv("TMPDIR", t.TempDir())
			scope, err := git.EnterBranchWithLimits(ctx, repo, "shape", nil, git.CheckoutLimits{Budget: &b})
			if err != nil {
				t.Fatalf("worktree refused: %v", err)
			}
			wt := writtenBytes(t, repo, scope.RepoDir, commit)
			scope.Cleanup()
			if wt != st.TotalBytes {
				t.Fatalf("linked worktree wrote %d bytes; the measure summed %d", wt, st.TotalBytes)
			}
			c := git.NewCLIClient(repo, git.WithTreeBudget(b))
			if err := c.Checkout(ctx, "shape"); err != nil {
				t.Fatalf("checkout refused: %v", err)
			}
			if main := writtenBytes(t, repo, repo, commit); main != st.TotalBytes {
				t.Fatalf("main checkout wrote %d bytes; the measure summed %d", main, st.TotalBytes)
			}
		})
	}
}

// TestNeutralAttributes_MergeDriversUnchanged: the neutral line leaves
// `merge` alone, so `binary` (a conflict keeps ours, no markers) and
// `merge=union` (both sides' lines, no conflict) merge exactly as without
// it — the same result tree.
func TestNeutralAttributes_MergeDriversUnchanged(t *testing.T) {
	repo := testutil.NewTempRepo(t)
	testutil.WriteFile(t, repo, ".gitattributes", "*.bin binary\n*.u merge=union\n")
	testutil.WriteFile(t, repo, "a.bin", "base\n")
	testutil.WriteFile(t, repo, "b.u", "base\n")
	tbGit(t, repo, nil, "add", "-A")
	tbGit(t, repo, nil, "commit", "-q", "-m", "base")
	base := tbGit(t, repo, nil, "rev-parse", "HEAD")
	side := func(word string) string {
		return ntTree(t, repo, base, map[string][]byte{
			".gitattributes": []byte("*.bin binary\n*.u merge=union\n"),
			"a.bin":          []byte(word + "\n"),
			"b.u":            []byte("base\n" + word + "\n"),
		})
	}
	ours, theirs := side("ours"), side("theirs")
	mergeTree := func() string {
		cmd := exec.Command("git", "-C", repo, "merge-tree", "--write-tree", "--no-messages", ours, theirs)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
		out, _ := cmd.Output() // exit 1: a.bin conflicts
		first, _, _ := strings.Cut(string(out), "\n")
		return strings.TrimSpace(first)
	}
	plain := mergeTree()
	if err := git.EnsureNeutralCheckoutAttributes(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
	neutral := mergeTree()
	if plain == "" || plain != neutral {
		t.Fatalf("merge result without the line %q, with it %q", plain, neutral)
	}
	if got := tbGit(t, repo, nil, "cat-file", "-p", neutral+":a.bin"); got != "ours" {
		t.Fatalf("binary conflict: a.bin = %q, want ours unmarked", got)
	}
	if got := tbGit(t, repo, nil, "cat-file", "-p", neutral+":b.u"); !strings.Contains(got, "ours") || !strings.Contains(got, "theirs") || strings.Contains(got, "<<<<<<<") {
		t.Fatalf("union merge: b.u = %q", got)
	}
}

// TestNeutralAttributes_EnsureKeepsOperatorLines: the writer keeps every
// other line in order, moves an earlier copy of the line (a CRLF one
// included) to the end, adds a missing final newline, and does not rewrite
// a file that already ends in the line. A link at the path is refused.
func TestNeutralAttributes_EnsureKeepsOperatorLines(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewTempRepo(t)
	path := filepath.Join(repo, ".git", "info", "attributes")
	nl := git.NeutralCheckoutAttributes + "\n"
	for _, tc := range []struct{ have, want string }{
		{"", nl},
		{"*.md diff\n", "*.md diff\n" + nl},
		{"x -text", "x -text\n" + nl},
		{nl + "foo ident\n", "foo ident\n" + nl},
		{"a\n" + git.NeutralCheckoutAttributes + "\r\nb\n", "a\nb\n" + nl},
		{"# operator\n" + nl, "# operator\n" + nl},
	} {
		if tc.have == "" {
			_ = os.Remove(path)
		} else if err := os.WriteFile(path, []byte(tc.have), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := git.EnsureNeutralCheckoutAttributes(ctx, repo); err != nil {
			t.Fatalf("%q: %v", tc.have, err)
		}
		got, _ := os.ReadFile(path)
		if string(got) != tc.want {
			t.Fatalf("%q -> %q, want %q", tc.have, got, tc.want)
		}
	}
	before, _ := os.Stat(path)
	if err := git.EnsureNeutralCheckoutAttributes(ctx, repo); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	if !os.SameFile(before, after) {
		t.Fatal("a file already ending in the line was rewritten")
	}
	if ok, err := git.HasNeutralCheckoutAttributes(ctx, repo); !ok || err != nil {
		t.Fatalf("Has = %v, %v", ok, err)
	}

	link := testutil.NewTempRepo(t)
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(target, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(link, ".git", "info", "attributes")); err != nil {
		t.Fatal(err)
	}
	if err := git.EnsureNeutralCheckoutAttributes(ctx, link); err == nil {
		t.Fatal("wrote through a symbolic link")
	}
	if got, _ := os.ReadFile(target); string(got) != "x\n" {
		t.Fatalf("the link's target changed: %q", got)
	}
}

// TestNeutralAttributes_MeasureFailsClosedWithoutTheLine: a budgeted
// measure refuses a repository whose info/attributes does not END in the
// line — missing, or followed by an operator attribute line — with the
// typed, permanent refusal; a comment or a blank line after it is fine,
// and a linked worktree is measured through the shared common dir.
func TestNeutralAttributes_MeasureFailsClosedWithoutTheLine(t *testing.T) {
	ctx := context.Background()
	repo, _, _, run := tbRepo(t)
	path := filepath.Join(repo, ".git", "info", "attributes")
	nl := git.NeutralCheckoutAttributes + "\n"
	for _, tc := range []struct {
		name, content string
		ok            bool
	}{
		{"missing", "", false},
		{"overridden after it", nl + "* ident\n", false},
		{"comment and blank after it", nl + "# end\n\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.content == "" {
				_ = os.Remove(path)
			} else if err := os.WriteFile(path, []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := git.TreeBudget{}.Measure(ctx, git.NewCLIClient(repo), "main")
			if tc.ok {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			var ge *git.GitError
			if !errors.Is(err, git.ErrNeutralAttributesMissing) || !errors.As(err, &ge) || ge.Kind != git.ErrKindPermanent || ge.Message != git.NeutralAttributesMissingMessage {
				t.Fatalf("err = %v, want the permanent neutral-attributes refusal", err)
			}
			c := git.NewCLIClient(repo, git.WithTreeBudget(git.TreeBudget{}))
			if err := c.Checkout(ctx, run); !errors.Is(err, git.ErrNeutralAttributesMissing) {
				t.Fatalf("budgeted checkout: %v", err)
			}
		})
	}
	if err := os.WriteFile(path, []byte(nl), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", t.TempDir())
	tbGit(t, repo, nil, "branch", "wt", "main")
	scope, err := git.EnterBranchWithLimits(ctx, repo, "wt", nil, git.CheckoutLimits{Budget: &git.TreeBudget{}})
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	defer scope.Cleanup()
	if _, err := git.MeasureTree(ctx, git.NewCLIClient(scope.RepoDir), "HEAD"); err != nil {
		t.Fatalf("measure from a linked worktree: %v", err)
	}
}

// TestNeutralAttributes_BlankRulesAreGits (review round 3, P4): git skips
// only space, tab and CR around attribute fields. A line led by a vertical
// tab or a no-break space is a pattern line to git, not a blank or a
// comment, and the neutral line with a form feed after it is not the
// neutral line (git drops its last attribute as an invalid name). Each such
// file is refused by the measure, and Ensure repairs it by appending the
// exact line.
func TestNeutralAttributes_BlankRulesAreGits(t *testing.T) {
	ctx := context.Background()
	n := git.NeutralCheckoutAttributes
	for _, tc := range []struct{ name, content string }{
		{"VT-led line after the neutral line", n + "\n\v* ident\n"},
		{"NBSP-led line after the neutral line", n + "\n * ident\n"},
		{"VT-prefixed neutral line", "\v" + n + "\n"},
		{"NBSP-prefixed neutral line", " " + n + "\n"},
		{"FF-suffixed neutral line", n + "\f\n"},
		{"VT-led comment after it", n + "\n\v# not a comment\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, _, _, _ := tbRepo(t)
			path := filepath.Join(repo, ".git", "info", "attributes")
			if err := os.WriteFile(path, []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := (git.TreeBudget{}).Measure(ctx, git.NewCLIClient(repo), "main"); !errors.Is(err, git.ErrNeutralAttributesMissing) {
				t.Fatalf("measure: %v, want the neutral-attributes refusal", err)
			}
			if err := git.EnsureNeutralCheckoutAttributes(ctx, repo); err != nil {
				t.Fatal(err)
			}
			if _, err := (git.TreeBudget{}).Measure(ctx, git.NewCLIClient(repo), "main"); err != nil {
				t.Fatalf("after Ensure: %v", err)
			}
		})
	}
	// git's own blanks around the line, and a tab-led comment after it,
	// are fine.
	repo, _, _, _ := tbRepo(t)
	if err := os.WriteFile(filepath.Join(repo, ".git", "info", "attributes"), []byte(" \t"+n+"\r\n\t# end\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ok, err := git.HasNeutralCheckoutAttributes(ctx, repo); !ok || err != nil {
		t.Fatalf("git-blank variants: %v %v", ok, err)
	}
}

// TestNeutralAttributes_EnsureWritesOnlyAtTheTop (review round 3, P4): a
// directory that merely lies inside a repository is refused — its
// repository's info/attributes is not written; a bare repository's own
// directory is accepted.
func TestNeutralAttributes_EnsureWritesOnlyAtTheTop(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewTempRepo(t)
	sub := filepath.Join(repo, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := git.EnsureNeutralCheckoutAttributes(ctx, sub); err == nil {
		t.Fatal("wrote the attributes of a repository the directory merely lies in")
	}
	if _, err := os.Stat(filepath.Join(repo, ".git", "info", "attributes")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("info/attributes was written: %v", err)
	}
	bare := t.TempDir()
	tbGit(t, bare, nil, "init", "-q", "--bare")
	if err := git.EnsureNeutralCheckoutAttributes(ctx, bare); err != nil {
		t.Fatalf("bare: %v", err)
	}
}

// ntConvertedClone is a repository whose files were checked out with
// conversions before the neutral line existed: its .gitattributes (attrs)
// or its own core.autocrlf makes a.txt CRLF on disk. Branch feature
// changes a.txt.
func ntConvertedClone(t *testing.T, attrs string, autocrlf bool) string {
	t.Helper()
	repo := testutil.NewTempRepo(t)
	if attrs != "" {
		testutil.WriteFile(t, repo, ".gitattributes", attrs)
	}
	testutil.WriteFile(t, repo, "a.txt", "one\ntwo\nthree\n")
	tbGit(t, repo, nil, "add", "-A")
	tbGit(t, repo, nil, "commit", "-q", "-m", "files")
	tbGit(t, repo, nil, "checkout", "-q", "-b", "feature")
	testutil.WriteFile(t, repo, "a.txt", "one\ntwo\nthree\nfour\n")
	tbGit(t, repo, nil, "commit", "-q", "-am", "four")
	tbGit(t, repo, nil, "checkout", "-q", "main")
	if autocrlf {
		tbGit(t, repo, nil, "config", "core.autocrlf", "true")
	}
	// Re-check a.txt out under the conversion.
	if err := os.Remove(filepath.Join(repo, "a.txt")); err != nil {
		t.Fatal(err)
	}
	tbGit(t, repo, nil, "checkout", "--", "a.txt")
	if b, _ := os.ReadFile(filepath.Join(repo, "a.txt")); !strings.Contains(string(b), "\r\n") {
		t.Fatalf("fixture: a.txt not converted: %q", b)
	}
	return repo
}

// TestNeutralAttributes_EnsureNeutralWorkTreeRenormalises (review round 3,
// P3): a clone checked out with conversions before the line looks
// modified once the line turns them off, and a merge over it was refused.
// EnsureNeutralWorkTree, when it adds the line, brings the tree to the
// stored bytes (quarantining the tracked state), and the merge succeeds;
// called again it changes nothing.
func TestNeutralAttributes_EnsureNeutralWorkTreeRenormalises(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name     string
		attrs    string
		autocrlf bool
	}{
		{"text eol=crlf", "* text eol=crlf\n", false},
		{"host core.autocrlf", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := ntConvertedClone(t, tc.attrs, tc.autocrlf)
			c := git.NewCLIClient(repo, git.WithTreeBudget(git.TreeBudget{}))
			if err := c.EnsureNeutralWorkTree(ctx); err != nil {
				t.Fatal(err)
			}
			if st := tbGit(t, repo, nil, "status", "--porcelain", "--untracked-files=no"); st != "" {
				t.Fatalf("still modified after the line was added:\n%s", st)
			}
			if _, err := c.Merge(ctx, git.MergeOpts{Source: "feature", Target: "main", Strategy: "merge-commit", Message: "m"}); err != nil {
				t.Fatalf("merge after the upgrade: %v", err)
			}
			head := tbGit(t, repo, nil, "rev-parse", "HEAD")
			if err := c.EnsureNeutralWorkTree(ctx); err != nil {
				t.Fatal(err)
			}
			if got := tbGit(t, repo, nil, "rev-parse", "HEAD"); got != head {
				t.Fatal("a second call moved HEAD")
			}
		})
	}
}

// TestNeutralAttributes_FailedRenormalisationIsRetried (codex review
// round 3; integration A phase 4): a tree whose HEAD is over the client's
// budget used to get the line and a pending renormalisation. Since phase 4
// every check that can fail runs before the line: the call is refused
// with nothing written — no line, no marker, the tree as it was — and so
// is a second one. Once the budget allows HEAD the next call brings the
// tree to the stored bytes and the merge succeeds; after that, no call
// touches the tree again, whatever its budget.
func TestNeutralAttributes_FailedRenormalisationIsRetried(t *testing.T) {
	ctx := context.Background()
	repo := ntConvertedClone(t, "* text eol=crlf\n", false)
	tight := git.NewCLIClient(repo, git.WithTreeBudget(git.TreeBudget{MaxEntries: 1}))
	for i := 0; i < 2; i++ {
		err := tight.EnsureNeutralWorkTree(ctx)
		var tooLarge *git.TreeTooLargeError
		if !errors.As(err, &tooLarge) || errors.Is(err, git.ErrRenormalisePending) {
			t.Fatalf("call %d: want the HEAD-over-budget refusal before the line (not ErrRenormalisePending), got %v", i, err)
		}
		if has, herr := git.HasNeutralCheckoutAttributes(ctx, repo); herr != nil || has {
			t.Fatalf("call %d: the refused call wrote the line (%v, %v)", i, has, herr)
		}
		if _, err := os.Lstat(filepath.Join(repo, ".git", git.RenormaliseMarkerName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("call %d: the refused call left a marker (%v)", i, err)
		}
		if b, _ := os.ReadFile(filepath.Join(repo, "a.txt")); !strings.Contains(string(b), "\r\n") {
			t.Fatalf("call %d: the refused call rewrote a.txt: %q", i, b)
		}
	}
	c := git.NewCLIClient(repo, git.WithTreeBudget(git.TreeBudget{}))
	if err := c.EnsureNeutralWorkTree(ctx); err != nil {
		t.Fatalf("retry once HEAD fits: %v", err)
	}
	if st := tbGit(t, repo, nil, "status", "--porcelain", "--untracked-files=no"); st != "" {
		t.Fatalf("the retry did not renormalise:\n%s", st)
	}
	if _, err := c.Merge(ctx, git.MergeOpts{Source: "feature", Target: "main", Strategy: "merge-commit", Message: "m"}); err != nil {
		t.Fatalf("merge after the retried renormalisation: %v", err)
	}
	// Done: the tree is no longer marked, so even an over-budget client's
	// call is a no-op, and an edit made since is not reset.
	testutil.WriteFile(t, repo, "a.txt", "edited\n")
	if err := tight.EnsureNeutralWorkTree(ctx); err != nil {
		t.Fatalf("a call after the renormalisation: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "a.txt")); string(b) != "edited\n" {
		t.Fatalf("a call after the renormalisation touched the tree: %q", b)
	}
}

// TestNeutralAttributes_SharedTreeRestoreRenormalises (review round 3, P3
// check): on standalone's shared tree — a home branch — the line written
// by the plain writer is enough: the first sequence's restore quarantines
// the converted files and resets them to the stored bytes, and the merge
// inside it succeeds.
func TestNeutralAttributes_SharedTreeRestoreRenormalises(t *testing.T) {
	ctx := context.Background()
	repo := ntConvertedClone(t, "* text eol=crlf\n", false)
	if err := git.EnsureNeutralCheckoutAttributes(ctx, repo); err != nil {
		t.Fatal(err)
	}
	c := git.NewCLIClient(repo, git.WithHomeBranch("main"), git.WithTreeBudget(git.TreeBudget{}))
	err := c.WithWorkingTree(ctx, func(ctx context.Context) error {
		if st := tbGit(t, repo, nil, "status", "--porcelain", "--untracked-files=no"); st != "" {
			return fmt.Errorf("not restored:\n%s", st)
		}
		_, err := c.Merge(ctx, git.MergeOpts{Source: "feature", Target: "main", Strategy: "merge-commit", Message: "m"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
