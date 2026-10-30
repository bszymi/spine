package git_test

// INIT-024 TASK-004 AC 2 (review finding N-6): the streamed listings bound
// what they keep, and still fail closed past it; no single path is held
// to a length limit of its own (review round 1, P3-1: a cap on kept paths
// let one long governed path refuse every listing of the tree). Also the
// listing and diff limits' relation to the checkout budget (TASK-003
// handoff; review round 1, P4-2), the record ceiling and ListFilesUnder
// (P4-6), and the bound on one unfinished record (P4-3).

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/bszymi/spine/adapters/git"
)

const boundsCap = 4 << 10

// bulkRepo commits n empty files with long names (a listing of about 45
// bytes each) and two .md files, on top of an empty seed commit.
func bulkRepo(t *testing.T, n int) (dir, seed, head string) {
	t.Helper()
	dir = t.TempDir()
	t3git(t, dir, nil, "init", "-q", "-b", "main")
	t3git(t, dir, nil, "commit", "-q", "--allow-empty", "-m", "seed")
	seed = t3git(t, dir, nil, "rev-parse", "HEAD")
	blob := t3git(t, dir, []byte{}, "hash-object", "-w", "--stdin")
	md := t3git(t, dir, []byte("# a\n"), "hash-object", "-w", "--stdin")
	var idx strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&idx, "100644 %s\tbulk/d%02d/file-with-a-long-name-%04d.txt\n", blob, i%10, i)
	}
	fmt.Fprintf(&idx, "100644 %s\tdocs/one.md\n100644 %s\tdocs/two.md\n", md, md)
	t3git(t, dir, []byte(idx.String()), "update-index", "--add", "--index-info")
	tree := t3git(t, dir, nil, "write-tree")
	head = t3git(t, dir, nil, "commit-tree", tree, "-p", seed, "-m", "bulk")
	t3git(t, dir, nil, "update-ref", "refs/heads/main", head)
	return dir, seed, head
}

// deepPath commits one file at a path of exactly n bytes (n >= 300,
// ending in ".md"), built with mktree: 255-byte directory names, the last
// one shortened to make the length exact.
func deepPath(t *testing.T, dir string, n int) (commit, path string) {
	t.Helper()
	const file = "x.md"
	var comps []string
	rest := n - len(file)
	for rest > 0 {
		k := min(255, rest-1) // a component and its slash
		if rest-1-k > 0 && rest-1-k < 2 {
			k-- // never leave a 0-byte component
		}
		comps = append(comps, strings.Repeat("d", k))
		rest -= k + 1
	}
	path = strings.Join(append(append([]string{}, comps...), file), "/")
	if len(path) != n {
		t.Fatalf("fixture: built a %d-byte path, want %d", len(path), n)
	}
	blob := t3git(t, dir, []byte("# deep\n"), "hash-object", "-w", "--stdin")
	tree := t3git(t, dir, []byte("100644 blob "+blob+"\t"+file+"\n"), "mktree")
	for i := len(comps) - 1; i >= 0; i-- {
		tree = t3git(t, dir, []byte("040000 tree "+tree+"\t"+comps[i]+"\n"), "mktree")
	}
	return t3git(t, dir, nil, "commit-tree", tree, "-m", fmt.Sprintf("deep %d", n)), path
}

func wantListingRefusal(t *testing.T, what string, err error, op string, limit int64, records bool) {
	t.Helper()
	var ge *git.GitError
	var ol *git.OutputTooLargeError
	if !errors.Is(err, git.ErrOutputTooLarge) || !errors.As(err, &ge) || !errors.As(err, &ol) {
		t.Fatalf("%s: %v, want ErrOutputTooLarge", what, err)
	}
	if ge.Kind != git.ErrKindPermanent || ge.Op != op || ge.Message != "output exceeds read limit" || ge.IsRetryable() {
		t.Errorf("%s: envelope %+v, want permanent %q \"output exceeds read limit\"", what, ge, op)
	}
	if ol.Limit != limit || ol.Records != records || ol.GitAlloc {
		t.Errorf("%s: cause %+v, want Limit %d Records %v", what, ol, limit, records)
	}
}

func TestListFiles_KeptBytesOverLimitRefused(t *testing.T) {
	ctx := context.Background()
	dir, seed, head := bulkRepo(t, 300)
	if raw := t3git(t, dir, nil, "ls-tree", "-r", "--name-only", head); len(raw) < 2*boundsCap {
		t.Fatalf("fixture: the listing is %d bytes, want well over %d", len(raw), boundsCap)
	}
	c := git.NewCLIClient(dir, git.WithMaxOutputBytes(boundsCap))

	t.Run("kept paths over the cap", func(t *testing.T) {
		_, err := c.ListFiles(ctx, head, "")
		wantListingRefusal(t, "ListFiles(all)", err, "list_files", boundsCap, false)
		_, err = c.ListFiles(ctx, head, "bulk/")
		wantListingRefusal(t, "ListFiles(bulk/)", err, "list_files", boundsCap, false)
		_, err = c.ListFilesFunc(ctx, head, nil)
		wantListingRefusal(t, "ListFilesFunc(nil)", err, "list_files", boundsCap, false)
		_, err = c.ListFilesFunc(ctx, head, func(p string) bool { return !strings.HasSuffix(p, ".md") })
		wantListingRefusal(t, "ListFilesFunc(non-.md)", err, "list_files", boundsCap, false)
		_, err = c.Diff(ctx, seed, head)
		wantListingRefusal(t, "Diff", err, "diff", boundsCap, false)
	})

	t.Run("few kept paths from a listing over the cap", func(t *testing.T) {
		want := []string{"docs/one.md", "docs/two.md"}
		if got, err := c.ListFiles(ctx, head, "*.md"); err != nil || strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("ListFiles(*.md) = %q, %v; want %q", got, err, want)
		}
		got, err := c.ListFilesFunc(ctx, head, func(p string) bool { return strings.HasPrefix(p, "docs/") })
		if err != nil || strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("ListFilesFunc(docs/) = %q, %v; want %q", got, err, want)
		}
	})

	// Review round 1 (P3-1): a path longer than 4,096 bytes is listed,
	// kept or dropped, and diffed. At d1f5e6e1 the kept one refused the
	// whole listing and the diff.
	t.Run("a long path is no refusal of its own", func(t *testing.T) {
		at, path := deepPath(t, dir, 4096)
		over, overPath := deepPath(t, dir, 4097)
		long, longPath := deepPath(t, dir, 9000)
		big := git.NewCLIClient(dir)
		for _, tc := range []struct {
			rev, path string
		}{{at, path}, {over, overPath}, {long, longPath}} {
			if got, err := big.ListFiles(ctx, tc.rev, ""); err != nil || len(got) != 1 || got[0] != tc.path {
				t.Errorf("ListFiles of a %d-byte path: %d paths, %v; want it listed", len(tc.path), len(got), err)
			}
			if got, err := big.ListFiles(ctx, tc.rev, "*.md"); err != nil || len(got) != 1 {
				t.Errorf("ListFiles(*.md) of a %d-byte path: %d paths, %v; want it listed", len(tc.path), len(got), err)
			}
			if got, err := big.ListFilesFunc(ctx, tc.rev, func(p string) bool { return p != tc.path }); err != nil || len(got) != 0 {
				t.Errorf("ListFilesFunc dropping a %d-byte path = %q, %v; want no paths, no error", len(tc.path), got, err)
			}
		}
		diffs, err := big.Diff(ctx, at, long)
		found := false
		for _, d := range diffs {
			found = found || d.Path == longPath
		}
		if err != nil || !found {
			t.Errorf("Diff to a 9,000-byte path = %+v, %v; want a record at the 9,000-byte path", len(diffs), err)
		}
		// Only the total binds: the same path over a small cap.
		_, err = git.NewCLIClient(dir, git.WithMaxOutputBytes(boundsCap)).ListFiles(ctx, long, "")
		wantListingRefusal(t, "ListFiles(9,000-byte path, 4 KiB cap)", err, "list_files", boundsCap, false)
	})
}

// TestListing_CarryOfADroppedPathIsBounded (review round 1, P4-3): a path
// the caller drops is carried only up to the room the listing has left
// (never less than 4 KiB), across writes. A dropped path that fits is
// read and dropped; one longer than the room refuses the listing before
// it is complete, so an unfinished record never costs more than the
// limit.
func TestListing_CarryOfADroppedPathIsBounded(t *testing.T) {
	const limit = 16 << 10
	keepMD := func(p string) bool { return strings.HasSuffix(p, ".md") }
	for _, size := range []int{5 << 10, 12 << 10, 20 << 10, 40 << 10} {
		long := strings.Repeat("d", size) + ".txt"
		out := []byte("a.md\x00" + long + "\x00b.md\x00")
		for _, chunk := range []int{1 << 10, 4 << 10, 7 << 10} {
			paths, carry, err := git.ListedForTest(out, chunk, limit, 0, keepMD)
			if size+4 <= limit-5 {
				if err != nil || strings.Join(paths, ",") != "a.md,b.md" {
					t.Errorf("%d-byte dropped path, %d-byte writes: %q, %v; want a.md, b.md", size, chunk, paths, err)
				}
			} else {
				wantListingRefusal(t, fmt.Sprintf("%d-byte dropped path, %d-byte writes", size, chunk), err, "list_files", limit, false)
			}
			if carry > limit {
				t.Errorf("%d-byte dropped path, %d-byte writes: carried %d bytes, over the %d-byte limit", size, chunk, carry, limit)
			}
		}
	}
	// The floor: with the limit all but used up, a record still grows to
	// 4 KiB before it is judged.
	kept := strings.Repeat("k", limit-200) + ".md"
	paths, _, err := git.ListedForTest([]byte(kept+"\x00"+strings.Repeat("x", 3000)+"\x00"), 512, limit, 0, keepMD)
	if err != nil || len(paths) != 1 {
		t.Errorf("a 3,000-byte dropped path after the limit is nearly used: %d paths, %v; want it read and dropped", len(paths), err)
	}
}

// TestListing_RecordCeiling (review round 1, P4-6): a listing stops and
// fails closed past its record ceiling — 4x the checkout budget's entries
// for a tree listing, twice that for a diff — whether the records are
// kept or dropped, so a tree whose shared subtrees expand to millions of
// paths is a bounded walk.
func TestListing_RecordCeiling(t *testing.T) {
	ctx := context.Background()
	dir, seed, head := bulkRepo(t, 300) // 302 files
	// MaxEntries 50: a ceiling of 200 records for a listing, 400 for a diff.
	c := git.NewCLIClient(dir, git.WithTreeBudget(git.TreeBudget{MaxEntries: 50}))
	if got := c.RecordCeilingForTest(); got != 200 {
		t.Fatalf("record ceiling = %d, want 4 x 50", got)
	}
	_, err := c.ListFiles(ctx, head, "*.md")
	wantListingRefusal(t, "ListFiles(*.md) of 302 records, ceiling 200", err, "list_files", 200, true)
	_, err = c.ListFilesFunc(ctx, head, func(string) bool { return false })
	wantListingRefusal(t, "ListFilesFunc(drop all) of 302 records", err, "list_files", 200, true)
	if got, err := c.Diff(ctx, seed, head); err != nil || len(got) != 302 {
		t.Errorf("Diff of 302 changes under a 400-record ceiling = %d, %v; want them all", len(got), err)
	}
	if n, err := c.CountChangedPaths(ctx, seed, head); err != nil || n != 302 {
		t.Errorf("CountChangedPaths under a 400-record ceiling = %d, %v", n, err)
	}
	small := git.NewCLIClient(dir, git.WithTreeBudget(git.TreeBudget{MaxEntries: 20}))
	_, err = small.Diff(ctx, seed, head)
	wantListingRefusal(t, "Diff of 302 changes, ceiling 160", err, "diff", 160, true)
	_, err = small.CountChangedPaths(ctx, seed, head)
	wantListingRefusal(t, "CountChangedPaths of 302 changes, ceiling 160", err, "count_changed_paths", 160, true)
	// A scoped listing walks only its directory: the ceiling counts what git lists.
	if got, err := small.ListFilesUnder(ctx, head, "docs", nil); err != nil || len(got) != 2 {
		t.Errorf("ListFilesUnder(docs) under an 80-record ceiling = %q, %v; want the two docs", got, err)
	}
	// The default: 4 x DefaultTreeMaxEntries.
	if got := git.NewCLIClient(dir).RecordCeilingForTest(); got != 4*git.DefaultTreeMaxEntries {
		t.Errorf("default record ceiling = %d, want %d", got, 4*git.DefaultTreeMaxEntries)
	}
	if n, err := git.CountedForTest([]byte("a\x00b\x00c\x00"), 1, 2); err == nil || n != 0 {
		t.Errorf("counter past a 2-record ceiling: %d, %v; want a refusal", n, err)
	}
}

// TestListFilesUnder_WalksOneDirectory (review round 1, P4-6): the
// directory reaches git as a literal pathspec, only paths under it come
// back, and a directory that is not a clean relative path is never handed
// to git (the whole tree is listed and filtered, same result).
func TestListFilesUnder_WalksOneDirectory(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t3git(t, dir, nil, "init", "-q", "-b", "main")
	blob := t3git(t, dir, []byte("x\n"), "hash-object", "-w", "--stdin")
	var idx strings.Builder
	for _, p := range []string{"docs/a.md", "docs/sub/b.md", "docsx/c.md", "docs*/d.md", "other/e.md", "docs.md"} {
		fmt.Fprintf(&idx, "100644 %s\t%s\n", blob, p)
	}
	t3git(t, dir, []byte(idx.String()), "update-index", "--add", "--index-info")
	tree := t3git(t, dir, nil, "write-tree")
	head := t3git(t, dir, nil, "commit-tree", tree, "-m", "c")
	c := git.NewCLIClient(dir)
	for _, tc := range []struct {
		dir  string
		want string
	}{
		{"docs", "docs/a.md,docs/sub/b.md"},
		{"docs/", "docs/a.md,docs/sub/b.md"},
		{"docs*", "docs*/d.md"}, // literal: not a glob
		{"docs/sub", "docs/sub/b.md"},
		{"", "docs*/d.md,docs.md,docs/a.md,docs/sub/b.md,docsx/c.md,other/e.md"},
		{".", "docs*/d.md,docs.md,docs/a.md,docs/sub/b.md,docsx/c.md,other/e.md"},
		{"nope", ""},
		{"../docs", ""}, // never a pathspec; nothing is under it
		{"./docs", ""},
		{"/docs", ""},
		{"docs//sub", ""},
		{"-docs", ""},
	} {
		got, err := c.ListFilesUnder(ctx, head, tc.dir, nil)
		if err != nil || strings.Join(got, ",") != tc.want {
			t.Errorf("ListFilesUnder(%q) = %q, %v; want %q", tc.dir, got, err, tc.want)
		}
	}
	got, err := c.ListFilesUnder(ctx, head, "docs", func(p string) bool { return strings.Contains(p, "sub") })
	if err != nil || strings.Join(got, ",") != "docs/sub/b.md" {
		t.Errorf("ListFilesUnder(docs, keep sub) = %q, %v", got, err)
	}
	// The capability helper scopes the same way with and without it.
	for _, gc := range []git.FileLister{c, listOnly{c}} {
		got, err := git.ListFilesKeeping(ctx, gc, head, "docs", nil)
		if err != nil || strings.Join(got, ",") != "docs/a.md,docs/sub/b.md" {
			t.Errorf("ListFilesKeeping(%T, docs) = %q, %v", gc, got, err)
		}
	}
}

// listOnly hides every method but ListFiles (an embedder's client without
// the capability).
type listOnly struct{ c *git.CLIClient }

func (l listOnly) ListFiles(ctx context.Context, ref, pattern string) ([]string, error) {
	return l.c.ListFiles(ctx, ref, pattern)
}

// TestListingLimit_AdmitsEveryBudgetedTree pins the TASK-003 handoff: the
// listing limit is at least the checkout budget's path bytes, plus one
// NUL per entry, plus a margin — for the default limit against the
// default budget, and for a client whose budget is raised past it.
func TestListingLimit_AdmitsEveryBudgetedTree(t *testing.T) {
	const margin = 1 << 20
	if need := git.DefaultTreeMaxPathBytesTotal + git.DefaultTreeMaxEntries + margin; git.DefaultMaxListingBytes < need {
		t.Fatalf("DefaultMaxListingBytes = %d, want >= %d (path budget %d + entries %d + margin %d)",
			git.DefaultMaxListingBytes, need, git.DefaultTreeMaxPathBytesTotal, git.DefaultTreeMaxEntries, margin)
	}
	if git.DefaultMaxListingBytes <= git.DefaultMaxOutputBytes {
		t.Fatalf("DefaultMaxListingBytes = %d, want above DefaultMaxOutputBytes %d (a tree near the budget lists to ~16.2 MiB)",
			git.DefaultMaxListingBytes, git.DefaultMaxOutputBytes)
	}
	dir := t.TempDir()
	if got := git.NewCLIClient(dir).ListingLimitForTest(); got != git.DefaultMaxListingBytes {
		t.Errorf("default client's listing limit = %d, want DefaultMaxListingBytes %d", got, git.DefaultMaxListingBytes)
	}
	if got := git.NewCLIClient(dir, git.WithMaxOutputBytes(boundsCap)).ListingLimitForTest(); got != boundsCap {
		t.Errorf("WithMaxOutputBytes(%d) client's listing limit = %d, want the explicit value", boundsCap, got)
	}
	raised := git.TreeBudget{MaxEntries: 1_000_000, MaxPathBytesTotal: 64 << 20}
	for _, c := range []*git.CLIClient{
		git.NewCLIClient(dir, git.WithTreeBudget(raised)),
		git.NewCLIClient(dir, git.WithTreeBudget(raised), git.WithMaxOutputBytes(boundsCap)),
	} {
		if got, need := c.ListingLimitForTest(), raised.MaxPathBytesTotal+raised.MaxEntries; got < need+margin {
			t.Errorf("budgeted client's listing limit = %d, want >= %d + %d", got, need, margin)
		}
	}
	// Review round 1 (P4-2): a diff holds both trees' paths, so its limit
	// is twice what a budgeted tree lists to, plus the margin.
	if need := 2*(git.DefaultTreeMaxPathBytesTotal+git.DefaultTreeMaxEntries) + margin; git.DefaultMaxDiffBytes < need {
		t.Fatalf("DefaultMaxDiffBytes = %d, want >= 2 x (path budget %d + entries %d) + margin %d = %d",
			git.DefaultMaxDiffBytes, git.DefaultTreeMaxPathBytesTotal, git.DefaultTreeMaxEntries, margin, need)
	}
	if got := git.NewCLIClient(dir).DiffLimitForTest(); got != git.DefaultMaxDiffBytes {
		t.Errorf("default client's diff limit = %d, want DefaultMaxDiffBytes %d", got, git.DefaultMaxDiffBytes)
	}
	if got := git.NewCLIClient(dir, git.WithMaxOutputBytes(boundsCap)).DiffLimitForTest(); got != boundsCap {
		t.Errorf("WithMaxOutputBytes(%d) client's diff limit = %d, want the explicit value", boundsCap, got)
	}
	for _, c := range []*git.CLIClient{
		git.NewCLIClient(dir, git.WithTreeBudget(raised)),
		git.NewCLIClient(dir, git.WithTreeBudget(raised), git.WithMaxOutputBytes(boundsCap)),
	} {
		if got, need := c.DiffLimitForTest(), 2*(raised.MaxPathBytesTotal+raised.MaxEntries)+margin; got < need {
			t.Errorf("budgeted client's diff limit = %d, want >= %d", got, need)
		}
	}
	// The process default budget (`spine serve`) reaches clients too.
	git.SetDefaultCheckoutLimits(git.CheckoutLimits{Budget: &raised})
	t.Cleanup(git.ResetDefaultCheckoutLimitsForTest)
	if got, need := git.NewCLIClient(dir).ListingLimitForTest(), raised.MaxPathBytesTotal+raised.MaxEntries+margin; got < need {
		t.Errorf("listing limit under the process budget = %d, want >= %d", got, need)
	}
	if got, need := git.NewCLIClient(dir).DiffLimitForTest(), 2*(raised.MaxPathBytesTotal+raised.MaxEntries)+margin; got < need {
		t.Errorf("diff limit under the process budget = %d, want >= %d", got, need)
	}
}

// TestDiff_HoldsBothTreesWorthOfPaths (review round 1, P4-2): Diff keeps
// up to its own limit — twice what the budget admits for one tree, plus
// the margin — not the tree listing's. With a 4 KiB explicit cap and a
// budget of 1 MiB of path bytes and 50,000 entries the listing limit is
// ~2.05 MiB and the diff
// limit ~3.1 MiB; a diff of ~2.5 MiB of paths is returned in full, while
// the same paths as a tree listing are refused.
func TestDiff_HoldsBothTreesWorthOfPaths(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t3git(t, dir, nil, "init", "-q", "-b", "main")
	t3git(t, dir, nil, "commit", "-q", "--allow-empty", "-m", "seed")
	seed := t3git(t, dir, nil, "rev-parse", "HEAD")
	blob := t3git(t, dir, []byte{}, "hash-object", "-w", "--stdin")
	var idx strings.Builder
	const n = 25_000
	for i := 0; i < n; i++ {
		fmt.Fprintf(&idx, "100644 %s\td%03d/%s-%06d.txt\n", blob, i%500, strings.Repeat("p", 80), i)
	}
	t3git(t, dir, []byte(idx.String()), "update-index", "--add", "--index-info")
	head := t3git(t, dir, nil, "commit-tree", t3git(t, dir, nil, "write-tree"), "-p", seed, "-m", "bulk")
	// An explicit 4 KiB cap, so the budget's floors set both limits.
	c := git.NewCLIClient(dir, git.WithMaxOutputBytes(boundsCap), git.WithTreeBudget(git.TreeBudget{MaxPathBytesTotal: 1 << 20, MaxEntries: 50_000}))
	listing, diff := c.ListingLimitForTest(), c.DiffLimitForTest()
	paths := int64(n * (4 + 80 + 12 + 1))
	if listing >= paths || paths >= diff {
		t.Fatalf("fixture: %d path bytes, listing limit %d, diff limit %d; want between them", paths, listing, diff)
	}
	if got, err := c.Diff(ctx, seed, head); err != nil || len(got) != n {
		t.Fatalf("Diff of %d path bytes under a %d-byte diff limit: %d records, %v; want all %d", paths, diff, len(got), err, n)
	}
	_, err := c.ListFiles(ctx, head, "")
	wantListingRefusal(t, "the same paths as a listing", err, "list_files", listing, false)
}
