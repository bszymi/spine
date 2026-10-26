package git_test

// INIT-024 TASK-003: the adversarial review of the checkout budget —
// ways around it that the AC tests do not cover. Fixtures stay tiny.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/testutil"
)

// tbAttrCommit commits a tree of one .gitattributes holding attrs and
// one file of content, on top of base.
func tbAttrCommit(t *testing.T, repo, base, attrs string, content []byte) string {
	t.Helper()
	a := tbGit(t, repo, []byte(attrs), "hash-object", "-w", "--stdin")
	f := tbGit(t, repo, content, "hash-object", "-w", "--stdin")
	tree := tbGit(t, repo, []byte("100644 blob "+a+"\t.gitattributes\n100644 blob "+f+"\tf.md\n"), "mktree")
	return tbGit(t, repo, nil, "commit-tree", tree, "-p", base, "-m", "attrs")
}

// TestTreeBudget_AttributeExpansionIsCounted: `ident` (and text/eol,
// working-tree-encoding) rewrite content on checkout, so a tree whose
// stored bytes fit the budget can write ~12x (ident) or more. The measure
// multiplies by the worst case its .gitattributes allow: such a tree is
// refused by EnterBranch before anything is written, and the same content
// without the attribute is admitted. A macro that sets ident is caught by
// the same scan.
func TestTreeBudget_AttributeExpansionIsCounted(t *testing.T) {
	ctx := context.Background()
	repo, base, _, _ := tbRepo(t)
	content := bytes.Repeat([]byte("$Id$"), 4000) // 16,000 bytes; ~188 KB under ident
	budget := git.TreeBudget{MaxEntries: 100, MaxTotalBytes: 64 << 10, MaxPathBytesTotal: 1 << 20}

	for _, tc := range []struct {
		name, attrs string
		refused     bool
	}{
		{"ident", "* ident\n", true},
		{"macro", "[attr]stamp ident\n*.md stamp\n", true},
		{"nul-terminated", "* ident\x00hidden\n", true},
		{"built-in-named macro", "[attr]text ident\n* text\n", true},
		{"encoding", "*.md working-tree-encoding=UTF-32\n", true},
		{"eol only (2x fits)", "* text eol=crlf\n", false},
		{"no expanding attribute", "*.png binary\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := tbAttrCommit(t, repo, base, tc.attrs, content)
			branch := "attr-" + strings.NewReplacer(" ", "-", "(", "", ")", "").Replace(tc.name)
			tbGit(t, repo, nil, "update-ref", "refs/heads/"+branch, c)
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)
			b := budget
			scope, err := git.EnterBranchWithLimits(ctx, repo, branch, nil, git.CheckoutLimits{Budget: &b})
			if !tc.refused {
				if err != nil {
					t.Fatalf("admissible tree refused: %v", err)
				}
				scope.Cleanup()
				return
			}
			if err == nil {
				n, _ := duBytes(scope.RepoDir)
				scope.Cleanup()
				t.Fatalf("admitted a tree that wrote %d bytes", n)
			}
			tl := requireTreeTooLarge(t, err, git.TreeLimitTotalBytes)
			if tl.Expansion <= 2 {
				t.Fatalf("refusal expansion = %v, want the attribute factor", tl.Expansion)
			}
			if names := dirEntries(t, tmp); len(names) != 0 {
				t.Fatalf("TMPDIR = %v", names)
			}
		})
	}

	// The factor is a bound: what git really writes for ident is under it.
	c := tbAttrCommit(t, repo, base, "* ident\n", content)
	tbGit(t, repo, nil, "update-ref", "refs/heads/attr-measure", c)
	st, err := git.MeasureTree(ctx, git.NewCLIClient(repo), "attr-measure")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", t.TempDir())
	scope, err := git.EnterBranch(ctx, repo, "attr-measure", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer scope.Cleanup()
	written, _ := duBytes(scope.RepoDir)
	if fi, err := os.Lstat(filepath.Join(scope.RepoDir, ".git")); err == nil && fi.Mode().IsRegular() {
		written -= fi.Size() // the worktree's "gitdir:" file is not tree content
	}
	if float64(written) > float64(st.TotalBytes)*st.Expansion || float64(written) < 10*float64(st.TotalBytes)/2 {
		t.Fatalf("wrote %d bytes; measured %d x %.2f", written, st.TotalBytes, st.Expansion)
	}
}

// TestTreeBudget_AttributeFilesTheScanCannotReadAreWorstCase: the scan
// reads every .gitattributes file in one batch (review round 1: no jump
// to the worst case at 65 files). Only past its total cap of attribute
// text, or for a symbolic link named .gitattributes (older git reads
// through it), is the worst case assumed — and the reason says so.
func TestTreeBudget_AttributeFilesTheScanCannotReadAreWorstCase(t *testing.T) {
	ctx := context.Background()
	repo, base, _, _ := tbRepo(t)
	worst := git.WorstAttributeExpansionForTest(40)
	harmless := tbGit(t, repo, []byte("*.png binary\n"), "hash-object", "-w", "--stdin")
	// Two files of 600,000 bytes each: 1.2 MB of attribute text.
	big1 := tbGit(t, repo, append([]byte("*.png binary\n"), bytes.Repeat([]byte("# padding\n"), 60000)...), "hash-object", "-w", "--stdin")
	big2 := tbGit(t, repo, append([]byte("*.jpg binary\n"), bytes.Repeat([]byte("# padding\n"), 60000)...), "hash-object", "-w", "--stdin")
	file := tbGit(t, repo, []byte("data\n"), "hash-object", "-w", "--stdin")

	var mk strings.Builder
	for i := 0; i < 70; i++ {
		sub := tbGit(t, repo, []byte("100644 blob "+harmless+"\t.gitattributes\n"), "mktree")
		fmt.Fprintf(&mk, "040000 tree %s\td%03d\n", sub, i)
	}
	many := tbGit(t, repo, []byte(mk.String()), "mktree")
	sub := tbGit(t, repo, []byte("100644 blob "+big2+"\t.gitattributes\n"), "mktree")
	overCap := tbGit(t, repo, []byte("100644 blob "+big1+"\t.gitattributes\n040000 tree "+sub+"\tsub\n100644 blob "+file+"\tf\n"), "mktree")
	link := tbGit(t, repo, []byte("120000 blob "+file+"\t.GitAttributes\n"), "mktree")

	for _, tc := range []struct {
		name, tree string
		want       float64
		reason     string
	}{
		{"70 harmless files are read", many, 1, ""},
		{"over the text cap", overCap, worst, "exceed the 1048576 bytes the scan reads"},
		{"symbolic link", link, worst, "symbolic link named .gitattributes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			commit := tbGit(t, repo, nil, "commit-tree", tc.tree, "-p", base, "-m", tc.name)
			st, err := git.MeasureTree(ctx, git.NewCLIClient(repo), commit)
			if err != nil {
				t.Fatal(err)
			}
			if st.Expansion != tc.want || !strings.Contains(st.ExpansionReason, tc.reason) {
				t.Fatalf("expansion = %v (%q), want %v (%q)", st.Expansion, st.ExpansionReason, tc.want, tc.reason)
			}
		})
	}
	if worst < 90 {
		t.Fatalf("worst-case factor %v is below ident x crlf x encoding", worst)
	}
}

// TestTreeBudget_AttributeParsing (review round 1, P2): attributes are
// parsed, not substring-matched — comments, unset (-attr) and unspecified
// (!attr) attributes, attr=false-style values, patterns and other names
// that merely contain a word expand nothing; whole set attributes,
// [attr] macros (recursively, with cycles cut) and quoted patterns do.
func TestTreeBudget_AttributeParsing(t *testing.T) {
	ident := 47.0 / 4
	for _, tc := range []struct {
		text string
		want float64
	}{
		{"# this file is not accidentally normalised\n*.bin -text\n", 1},
		{"* text=auto\n", 2},
		{"* text\n", 2},
		{"* eol=crlf\n", 2},
		{"* eol=lf\n", 1},
		{"* crlf=input\n", 1},
		{"* crlf\n", 2},
		{"* text=false\n", 1},
		{"*.c ident\n", ident},
		{"* -ident !text -crlf\n", 1},
		{"* ident=false\n", 1},
		{"identity/*.md binary\n", 1},
		{"*.x binary\n", 1},
		{"   # indented comment ident\n", 1},
		{"\"a quoted ident path\" ident\n", ident},
		{"\"a quoted path\" -ident\n", 1},
		{"[attr]stamp ident\n*.md stamp\n", ident},
		{"[attr]stamp ident\n*.md -stamp\n", 1},
		{"[attr]a b\n[attr]b ident\n* a\n", ident},
		{"[attr]a b\n[attr]b a\n* a\n", 1},
		{"[attr]unused ident\n", 1},
		{"* working-tree-encoding=UTF-8\n", 1},
		{"* working-tree-encoding=utf8\n", 1},
		{"* working-tree-encoding=UTF-16\n", 8},
		{"* text eol=crlf ident\r\n", 2 * ident},
		{"* filter=lfs diff=lfs merge=lfs -text\n", 1},
		{"* ident\x00hidden\n", ident},       // a NUL ends the line, as in git (codex review)
		{"*.x binary\x00\n* ident\n", ident}, // and the next line is still read
		{"* text\x00 ident\n", 2},
		{"[attr]text ident\n* text\n", 2 * ident},  // a macro named like a built-in (codex review)
		{"[attr]ident filter=x\n* ident\n", ident}, // its filter is found too (below)
		{"*\rident\n", ident},                      // CR is a blank between pattern and attributes
		{"\"unterminated ident\n", ident},          // an unclosed quote falls back to a blank split
		{"\"a b\" -ident\n", 1},
	} {
		if got := git.AttributeExpansionForTest(tc.text, 40); got != tc.want {
			t.Errorf("%q: factor %v, want %v", tc.text, got, tc.want)
		}
	}
	if got := git.AttributeFiltersForTest("* filter=lfs\n*.x -filter\n"); len(got) != 1 || got[0] != "lfs" {
		t.Errorf("filters = %v, want [lfs]", got)
	}
	if got := git.AttributeFiltersForTest("[attr]text filter=x\n* text\n"); len(got) != 1 || got[0] != "x" {
		t.Errorf("a filter behind a built-in-named macro: %v, want [x]", got)
	}
}

// TestTreeBudget_ReviewReproductionsAreAdmitted (review round 1, P2): the
// two trees the substring heuristic refused — 65 directories each with
// `* text=auto` (taken as the 188x worst case past 64 files), and a
// comment containing "accidentally" (read as `ident`) beside `*.bin -text`
// — are measured at their real factors (2 and 1) and admitted.
func TestTreeBudget_ReviewReproductionsAreAdmitted(t *testing.T) {
	ctx := context.Background()
	repo, base, _, _ := tbRepo(t)
	budget := git.TreeBudget{MaxEntries: 1000, MaxTotalBytes: 64 << 10, MaxPathBytesTotal: 1 << 20}
	data := tbGit(t, repo, bytes.Repeat([]byte("y"), 20<<10), "hash-object", "-w", "--stdin")
	auto := tbGit(t, repo, []byte("* text=auto\n"), "hash-object", "-w", "--stdin")
	var mk strings.Builder
	for i := 0; i < 65; i++ {
		sub := tbGit(t, repo, []byte("100644 blob "+auto+"\t.gitattributes\n"), "mktree")
		fmt.Fprintf(&mk, "040000 tree %s\td%03d\n", sub, i)
	}
	mk.WriteString("100644 blob " + data + "\tdata.bin\n")
	sixtyFive := tbGit(t, repo, []byte(mk.String()), "mktree")
	comment := tbGit(t, repo, []byte("# binaries are not accidentally normalised\n*.bin -text\n"), "hash-object", "-w", "--stdin")
	commented := tbGit(t, repo, []byte("100644 blob "+comment+"\t.gitattributes\n100644 blob "+data+"\tdata.bin\n"), "mktree")
	for _, tc := range []struct {
		name, tree string
		want       float64
	}{
		{"65 dirs of text=auto", sixtyFive, 2},
		{"accidentally", commented, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			commit := tbGit(t, repo, nil, "commit-tree", tc.tree, "-p", base, "-m", tc.name)
			st, err := budget.Measure(ctx, git.NewCLIClient(repo), commit)
			if err != nil {
				t.Fatalf("refused: %v", err)
			}
			if st.Expansion != tc.want {
				t.Fatalf("expansion = %v (%s), want %v", st.Expansion, st.ExpansionReason, tc.want)
			}
		})
	}
}

// TestTreeBudget_RefusalNamesTheFactor (review round 1, P2): a refusal
// applied with an expansion says the stored size, the factor and which
// attribute it comes from, so an operator is not told to raise the limit
// 188-fold for a tree that stores a fraction of it.
func TestTreeBudget_RefusalNamesTheFactor(t *testing.T) {
	ctx := context.Background()
	repo, base, _, _ := tbRepo(t)
	c := tbAttrCommit(t, repo, base, "* ident\n", bytes.Repeat([]byte("$Id$"), 4000))
	err := git.TreeBudget{MaxTotalBytes: 64 << 10}.Check(ctx, git.NewCLIClient(repo), c)
	tl := requireTreeTooLarge(t, err, git.TreeLimitTotalBytes)
	if tl.Stored != 16008 || tl.Expansion != 47.0/4 || !strings.Contains(tl.Reason, "ident") {
		t.Fatalf("refusal = %+v", tl)
	}
	for _, want := range []string{"16008 bytes stored", "x11.75: ident x11.75", "65536-byte"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q lacks %q", err, want)
		}
	}
}

// TestTreeBudget_FilterDriverIsUnbounded (review round 1): a filter=<name>
// whose smudge driver the host's git config defines writes whatever the
// driver produces, so the tree is refused at any limit and the refusal
// says why; the same attribute with no driver configured is inert.
func TestTreeBudget_FilterDriverIsUnbounded(t *testing.T) {
	ctx := context.Background()
	repo, base, _, _ := tbRepo(t)
	c := tbAttrCommit(t, repo, base, "* filter=fetch\n", []byte("pointer\n"))
	if err := (git.TreeBudget{}).Check(ctx, git.NewCLIClient(repo), c); err != nil {
		t.Fatalf("no driver configured: %v", err)
	}
	tbGit(t, repo, nil, "config", "filter.fetch.smudge", "cat")
	err := (git.TreeBudget{}).Check(ctx, git.NewCLIClient(repo), c)
	tl := requireTreeTooLarge(t, err, git.TreeLimitTotalBytes)
	if !math.IsInf(tl.Expansion, 1) || !strings.Contains(err.Error(), `filter="fetch"`) || !strings.Contains(err.Error(), "cannot admit it at any setting") {
		t.Fatalf("refusal: %v (%+v)", err, tl)
	}
}

// TestTreeBudget_InPlaceRestoreIsBounded: on a home-branch tree (the
// shared standalone tree) the entry restore of an in-place write scope
// switches back to the home branch; an over-budget home branch is
// refused before the switch writes it.
func TestTreeBudget_InPlaceRestoreIsBounded(t *testing.T) {
	ctx := context.Background()
	repo, base, bomb, _ := tbRepo(t)
	tbGit(t, repo, nil, "branch", "elsewhere", base)
	tbGit(t, repo, nil, "checkout", "-q", "elsewhere")
	tbGit(t, repo, nil, "update-ref", "refs/heads/main", bomb)
	_ = git.NewCLIClient(repo, git.WithHomeBranch("main")) // registers the home branch
	_, err := git.EnterBranchWithLimits(ctx, repo, "", nil, git.CheckoutLimits{Budget: &smallBudget})
	var tre *git.TreeRestoreError
	if !errors.As(err, &tre) || !errors.Is(err, git.ErrTreeTooLarge) {
		t.Fatalf("in-place entry over an over-budget home branch: %v", err)
	}
	if got := tbGit(t, repo, nil, "symbolic-ref", "HEAD"); got != "refs/heads/elsewhere" {
		t.Fatalf("HEAD = %s", got)
	}
	if _, err := os.Stat(filepath.Join(repo, "docs")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the home branch's bomb was written: %v", err)
	}
}

// TestTreeBudget_ResetBranchIsBounded: the take-back's `read-tree -m -u`
// is measured before the ref moves; an over-budget `to` leaves the
// branch, index and tree where they were.
func TestTreeBudget_ResetBranchIsBounded(t *testing.T) {
	ctx := context.Background()
	repo, _, bomb, _ := tbRepo(t)
	// main: bomb, then a commit that removes docs/ (what is checked out).
	tbGit(t, repo, nil, "update-ref", "refs/heads/main", bomb)
	tbGit(t, repo, nil, "read-tree", "--reset", "main")
	clean := tbGit(t, repo, nil, "commit-tree", tbGit(t, repo, nil, "rev-parse", bomb+"^{tree}"), "-p", bomb, "-m", "same")
	readme := tbGit(t, repo, []byte("hello\n"), "hash-object", "-w", "--stdin")
	small := tbGit(t, repo, []byte("100644 blob "+readme+"\tREADME.md\n"), "mktree")
	m := tbGit(t, repo, nil, "commit-tree", small, "-p", clean, "-m", "drop docs")
	tbGit(t, repo, nil, "update-ref", "refs/heads/main", m)
	tbGit(t, repo, nil, "reset", "-q", "--hard", "main")
	if _, err := os.Stat(filepath.Join(repo, "docs")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fixture: docs/ checked out: %v", err)
	}

	c := git.NewCLIClient(repo, git.WithTreeBudget(smallBudget))
	err := c.ResetBranch(ctx, "main", bomb, m)
	requireTreeTooLarge(t, err, git.TreeLimitTotalBytes)
	if got := tbGit(t, repo, nil, "rev-parse", "refs/heads/main"); got != m {
		t.Fatalf("main moved to %s", got)
	}
	if _, err := os.Stat(filepath.Join(repo, "docs")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the take-back wrote the bomb: %v", err)
	}
}

// TestTreeBudget_RepeatedSmallPushesAreMeasuredWhole: each commit adds a
// little, and each is within any per-push delta — but every measure is of
// the whole tree, so the tip that crosses the budget is refused.
func TestTreeBudget_RepeatedSmallPushesAreMeasuredWhole(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewTempRepo(t)
	c := git.NewCLIClient(repo, git.WithTreeBudget(git.TreeBudget{MaxEntries: 25, MaxTotalBytes: 1 << 30, MaxPathBytesTotal: 1 << 30}))
	for i := 0; i < 30; i++ {
		testutil.WriteFile(t, repo, fmt.Sprintf("f%02d.md", i), "x\n")
		tbGit(t, repo, nil, "add", "-A")
		tbGit(t, repo, nil, "commit", "-q", "-m", "one more")
		err := c.Checkout(ctx, "main")
		if i < 24 && err != nil {
			t.Fatalf("commit %d (%d entries) refused: %v", i, i+1, err)
		}
		if i >= 25 {
			requireTreeTooLarge(t, err, git.TreeLimitEntries)
		}
	}
}

// TestTreeBudget_GitlinksAndSymlinksCount: entries that write no file
// content (gitlinks become empty directories, symbolic links tiny files)
// still count against the entry budget.
func TestTreeBudget_GitlinksAndSymlinksCount(t *testing.T) {
	ctx := context.Background()
	repo, base, _, _ := tbRepo(t)
	target := tbGit(t, repo, []byte("t"), "hash-object", "-w", "--stdin")
	var mk strings.Builder
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&mk, "160000 commit %s\tsub%03d\n120000 blob %s\tlink%03d\n", base, i, target, i)
	}
	tree := tbGit(t, repo, []byte(mk.String()), "mktree")
	commit := tbGit(t, repo, nil, "commit-tree", tree, "-p", base, "-m", "links")
	st, err := git.TreeBudget{MaxEntries: 100}.Measure(ctx, git.NewCLIClient(repo), commit)
	requireTreeTooLarge(t, err, git.TreeLimitEntries)
	if st.Gitlinks == 0 || st.Blobs == 0 {
		t.Fatalf("stats = %+v", st)
	}
}

// TestClone_RefusesOverBudgetTree: a clone checks out its remote's
// default branch, which nothing has measured (a code repository is
// whatever its remote holds). With a budget the clone is made with
// --no-checkout, measured, and written only within the budget; over it,
// the clone is refused and removed, so the pool never later takes a
// half-made clone for a usable one. A clone within the budget, and an
// empty repository, still work.
func TestClone_RefusesOverBudgetTree(t *testing.T) {
	ctx := context.Background()
	src, base, bomb, _ := tbRepo(t)
	c := git.NewCLIClient("", git.WithTreeBudget(smallBudget))

	tbGit(t, src, nil, "update-ref", "refs/heads/main", bomb)
	dst := filepath.Join(t.TempDir(), "clone")
	requireTreeTooLarge(t, c.Clone(ctx, "file://"+src, dst), git.TreeLimitTotalBytes)
	if _, err := os.Stat(dst); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the refused clone was left behind: %v", err)
	}
	// Into an existing empty directory: emptied again, not removed.
	empty := t.TempDir()
	requireTreeTooLarge(t, c.Clone(ctx, "file://"+src, empty), git.TreeLimitTotalBytes)
	if names := dirEntries(t, empty); len(names) != 0 {
		t.Fatalf("the refused clone left %v", names)
	}

	tbGit(t, src, nil, "update-ref", "refs/heads/main", base)
	ok := filepath.Join(t.TempDir(), "ok")
	if err := c.Clone(ctx, "file://"+src, ok); err != nil {
		t.Fatalf("clone within the budget: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(ok, "README.md")); err != nil || string(b) != "hello\n" {
		t.Fatalf("clone within the budget not checked out: %q, %v", b, err)
	}
	if st := tbGit(t, ok, nil, "status", "--porcelain"); st != "" {
		t.Fatalf("clone within the budget is dirty:\n%s", st)
	}

	bare := t.TempDir()
	tbGit(t, bare, nil, "init", "-q", "--bare")
	emptyClone := filepath.Join(t.TempDir(), "empty")
	if err := c.Clone(ctx, "file://"+bare, emptyClone); err != nil {
		t.Fatalf("clone of an empty repository: %v", err)
	}
}

// TestClone_CheckoutKeepsTheCloneEnvironment (codex round 1, P2): the
// separate `reset --hard` that writes a budgeted clone runs with the
// clone's own environment. A client without ambient credentials keeps
// the host's global git config — here a smudge filter that leaves a
// marker — out of the checkout, as the single `git clone` did; a client
// with ambient credentials still gets it (the control proves the test
// can see the filter run).
func TestClone_CheckoutKeepsTheCloneEnvironment(t *testing.T) {
	ctx := context.Background()
	src := testutil.NewTempRepo(t)
	testutil.WriteFile(t, src, ".gitattributes", "*.md filter=leak\n")
	testutil.WriteFile(t, src, "a.md", "content\n")
	tbGit(t, src, nil, "add", "-A")
	tbGit(t, src, nil, "commit", "-q", "-m", "filtered")
	marker := filepath.Join(t.TempDir(), "smudged")
	global := filepath.Join(t.TempDir(), "gitconfig")
	// Quoted: an unquoted ";" starts a comment in a git config file.
	cfg := fmt.Sprintf("[filter \"leak\"]\n\tsmudge = \"touch '%s'; cat\"\n\tclean = cat\n", marker)
	if err := os.WriteFile(global, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)

	isolated := git.NewCLIClient("", git.WithTreeBudget(smallBudget), git.WithoutAmbientCredentials())
	if err := isolated.Clone(ctx, "file://"+src, filepath.Join(t.TempDir(), "iso")); err != nil {
		t.Fatalf("isolated clone: %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the host's smudge filter ran in an isolated clone's checkout: %v", err)
	}

	// The control: a deadline-only client also splits the clone, and its
	// reset runs with the host environment, so the filter runs. (A budgeted
	// client with ambient credentials refuses such a tree outright: the
	// host defines a smudge driver for it — TestTreeBudget_FilterDriverIsUnbounded.)
	ambient := git.NewCLIClient("", git.WithMutateTimeout(time.Minute))
	amb := filepath.Join(t.TempDir(), "amb")
	if err := ambient.Clone(ctx, "file://"+src, amb); err != nil {
		t.Fatalf("ambient clone: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(amb, "a.md")); err != nil || string(b) != "content\n" {
		t.Fatalf("ambient clone a.md = %q, %v", b, err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("control: the host's smudge filter did not run for an ambient clone: %v", err)
	}
}

// TestClone_MutateTimeoutBoundsTheCheckout (codex round 1, P2): a client
// with only a mutate deadline also clones --no-checkout and writes the
// tree under the deadline — a slow checkout is stopped and the clone
// removed — while the network part of the clone stays undeadlined.
func TestClone_MutateTimeoutBoundsTheCheckout(t *testing.T) {
	ctx := context.Background()
	src := testutil.NewTempRepo(t)
	testutil.WriteFile(t, src, ".gitattributes", "*.slow filter=slow\n")
	testutil.WriteFile(t, src, "b.slow", "slow\n")
	tbGit(t, src, nil, "add", "-A")
	tbGit(t, src, nil, "commit", "-q", "-m", "slow")
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, []byte("[filter \"slow\"]\n\tsmudge = \"exec 2>/dev/null; sleep 5; cat\"\n\tclean = cat\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	c := git.NewCLIClient("", git.WithMutateTimeout(300*time.Millisecond))
	dst := filepath.Join(t.TempDir(), "clone")
	start := time.Now()
	err := c.Clone(ctx, "file://"+src, dst)
	var mt *git.MutateTimeoutError
	if !errors.As(err, &mt) || time.Since(start) > 4*time.Second {
		t.Fatalf("Clone = %v after %s, want a *MutateTimeoutError well before the filter ends", err, time.Since(start))
	}
	if _, err := os.Stat(dst); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the stopped clone was left behind: %v", err)
	}
}

// TestClone_FailedCheckoutLeavesTheDirectoryEmpty (codex round 2, P2):
// cloning into an existing empty directory, a checkout that fails half
// way — here a required smudge filter that fails on the second file —
// leaves the directory empty again, not holding the files the reset had
// already written, so a retry can clone into it.
func TestClone_FailedCheckoutLeavesTheDirectoryEmpty(t *testing.T) {
	ctx := context.Background()
	src := testutil.NewTempRepo(t)
	testutil.WriteFile(t, src, ".gitattributes", "z.md filter=broken\n")
	testutil.WriteFile(t, src, "a.md", "first\n")
	testutil.WriteFile(t, src, "z.md", "last\n")
	tbGit(t, src, nil, "add", "-A")
	tbGit(t, src, nil, "commit", "-q", "-m", "files")
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, []byte("[filter \"broken\"]\n\tsmudge = false\n\tclean = cat\n\trequired = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	dst := t.TempDir()
	c := git.NewCLIClient("", git.WithTreeBudget(smallBudget))
	if err := c.Clone(ctx, "file://"+src, dst); err == nil {
		t.Fatal("a clone whose checkout failed reported success")
	}
	if names := dirEntries(t, dst); len(names) != 0 {
		t.Fatalf("the failed clone left %v in the directory", names)
	}
}

// TestTreeBudget_RestoreMeasuresHEADBeforeItsReset (codex round 3, P2):
// the shared tree's restore resets HEAD's tree whenever the index or the
// tree differs from it, and HEAD's ref can move under the tree without a
// checkout (here an update-ref of the checked-out home branch to the
// bomb). The reset is measured first: refused, nothing is written.
func TestTreeBudget_RestoreMeasuresHEADBeforeItsReset(t *testing.T) {
	ctx := context.Background()
	repo, _, bomb, _ := tbRepo(t)
	c := git.NewCLIClient(repo, git.WithHomeBranch("main"), git.WithTreeBudget(smallBudget))
	tbGit(t, repo, nil, "update-ref", "refs/heads/main", bomb)
	_, err := c.RestoreWorkingTree(ctx)
	if !errors.Is(err, git.ErrTreeTooLarge) {
		t.Fatalf("restore over an over-budget HEAD: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "docs")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the restore wrote the bomb: %v", err)
	}
}

// TestTreeBudget_TreeObjectOverTheAllocLimitSaysWhatItIs (review round 1,
// P3): one tree object larger than GIT_ALLOC_LIMIT (here one directory of
// 10,000 long names, ~1.3 MB, against the 1 MiB floor a 64 KiB per-blob
// limit gives) cannot be listed at all. The refusal says so, and that the
// tree limits are not the knob.
func TestTreeBudget_TreeObjectOverTheAllocLimitSaysWhatItIs(t *testing.T) {
	ctx := context.Background()
	repo, base, _, _ := tbRepo(t)
	blob := tbGit(t, repo, []byte("x"), "hash-object", "-w", "--stdin")
	var mk strings.Builder
	long := strings.Repeat("n", 90)
	for i := 0; i < 10000; i++ {
		fmt.Fprintf(&mk, "100644 blob %s\t%s%05d\n", blob, long, i)
	}
	tree := tbGit(t, repo, []byte(mk.String()), "mktree")
	commit := tbGit(t, repo, nil, "commit-tree", tree, "-p", base, "-m", "wide")
	c := git.NewCLIClient(repo, git.WithMaxBlobBytes(64<<10))
	_, err := git.TreeBudget{MaxEntries: 1 << 40, MaxTotalBytes: 1 << 40, MaxPathBytesTotal: 1 << 40}.Measure(ctx, c, commit)
	var ge *git.GitError
	if !errors.As(err, &ge) || ge.Message != git.TreeObjectTooLargeMessage || !errors.Is(err, git.ErrOutputTooLarge) || errors.Is(err, git.ErrTreeTooLarge) {
		t.Fatalf("err = %v, want the tree-object refusal", err)
	}
	if !strings.Contains(err.Error(), "raising the tree limits does not help") {
		t.Fatalf("message: %v", err)
	}
}

// TestTreeBudget_MacrosAreExpandedOnceAndFully (codex review after round
// 1, P1 x2): a self-referencing macro that fans out 20 ways costs linear
// work (it used to be ~20^8 visits, unbounded by any timeout), and a macro
// chain of any length is followed to its end (a 12-macro chain ending in
// `ident` used to be cut at depth 8 and measured at 1x).
func TestTreeBudget_MacrosAreExpandedOnceAndFully(t *testing.T) {
	loop := "[attr]loop" + strings.Repeat(" loop", 20) + "\n*.md loop\n"
	done := make(chan float64, 1)
	go func() { done <- git.AttributeExpansionForTest(loop, 40) }()
	select {
	case f := <-done:
		if f != 1 {
			t.Fatalf("fan-out loop: factor %v, want 1", f)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a fan-out macro loop did not finish in 5 s")
	}
	var chain strings.Builder
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&chain, "[attr]m%d m%d\n", i, i+1)
	}
	chain.WriteString("[attr]m12 ident\n*.md m0\n")
	if f := git.AttributeExpansionForTest(chain.String(), 40); f != 47.0/4 {
		t.Fatalf("12-macro chain to ident: factor %v, want 11.75", f)
	}
	var fchain strings.Builder
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&fchain, "[attr]f%d f%d\n", i, i+1)
	}
	fchain.WriteString("[attr]f12 filter=huge\n* f0\n")
	if got := git.AttributeFiltersForTest(fchain.String()); len(got) != 1 || got[0] != "huge" {
		t.Fatalf("12-macro chain to a filter: %v", got)
	}
}

// TestTreeBudget_UnscannedAttributesWithAFilterDriverAreRefused (codex
// review after round 1, P1): attributes past the scan's cap could name any
// filter; when this host defines a smudge driver, the worst-case factor is
// no bound, so such a tree is refused at any limit.
func TestTreeBudget_UnscannedAttributesWithAFilterDriverAreRefused(t *testing.T) {
	ctx := context.Background()
	repo, base, _, _ := tbRepo(t)
	big := tbGit(t, repo, append([]byte("*.md filter=huge\n"), bytes.Repeat([]byte("# padding\n"), 110000)...), "hash-object", "-w", "--stdin")
	file := tbGit(t, repo, []byte("p\n"), "hash-object", "-w", "--stdin")
	tree := tbGit(t, repo, []byte("100644 blob "+big+"\t.gitattributes\n100644 blob "+file+"\ta.md\n"), "mktree")
	commit := tbGit(t, repo, nil, "commit-tree", tree, "-p", base, "-m", "padded")
	st, err := git.MeasureTree(ctx, git.NewCLIClient(repo), commit)
	if err != nil || st.Expansion != git.WorstAttributeExpansionForTest(40) {
		t.Fatalf("no driver configured: %+v, %v", st, err)
	}
	tbGit(t, repo, nil, "config", "filter.huge.smudge", "cat")
	err = (git.TreeBudget{}).Check(ctx, git.NewCLIClient(repo), commit)
	tl := requireTreeTooLarge(t, err, git.TreeLimitTotalBytes)
	if !math.IsInf(tl.Expansion, 1) || !strings.Contains(err.Error(), "smudge filter drivers") {
		t.Fatalf("refusal: %v", err)
	}
}
