package git_test

// INIT-024 TASK-004 AC 2 (review finding N-6): the streamed listings bound
// what they keep, and still fail closed past it. Also the listing limit's
// relation to the checkout budget (TASK-003 handoff): every tree the
// budget admits lists in full.

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

func wantListingRefusal(t *testing.T, what string, err error, op string, limit int64, perPath bool) {
	t.Helper()
	var ge *git.GitError
	var ol *git.OutputTooLargeError
	if !errors.Is(err, git.ErrOutputTooLarge) || !errors.As(err, &ge) || !errors.As(err, &ol) {
		t.Fatalf("%s: %v, want ErrOutputTooLarge", what, err)
	}
	if ge.Kind != git.ErrKindPermanent || ge.Op != op || ge.Message != "output exceeds read limit" || ge.IsRetryable() {
		t.Errorf("%s: envelope %+v, want permanent %q \"output exceeds read limit\"", what, ge, op)
	}
	if ol.Limit != limit || ol.PerPath != perPath || ol.GitAlloc {
		t.Errorf("%s: cause %+v, want Limit %d PerPath %v", what, ol, limit, perPath)
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

	t.Run("a path over MaxListingPathBytes", func(t *testing.T) {
		at, path := deepPath(t, dir, git.MaxListingPathBytes)
		over, overPath := deepPath(t, dir, git.MaxListingPathBytes+1)
		big := git.NewCLIClient(dir) // the default limit: only the path bound applies
		if got, err := big.ListFiles(ctx, at, ""); err != nil || len(got) != 1 || got[0] != path {
			t.Errorf("ListFiles of a %d-byte path: %d paths, %v; want it listed", len(path), len(got), err)
		}
		_, err := big.ListFiles(ctx, over, "")
		wantListingRefusal(t, "ListFiles(4097-byte path)", err, "list_files", git.MaxListingPathBytes, true)
		_, err = big.ListFiles(ctx, over, "*.md")
		wantListingRefusal(t, "ListFiles(*.md, 4097-byte path)", err, "list_files", git.MaxListingPathBytes, true)
		_, err = big.Diff(ctx, at, over)
		wantListingRefusal(t, "Diff(4097-byte path)", err, "diff", git.MaxListingPathBytes, true)
		// A path the caller drops is read and dropped, whatever its
		// length: it is not kept, so it costs no memory and is no refusal.
		got, err := big.ListFilesFunc(ctx, over, func(p string) bool { return p != overPath })
		if err != nil || len(got) != 0 {
			t.Errorf("ListFilesFunc dropping the 4097-byte path = %q, %v; want no paths and no error", got, err)
		}
		if got, err := big.ListFiles(ctx, over, "*.yaml"); err != nil || len(got) != 0 {
			t.Errorf("ListFiles(*.yaml) over the 4097-byte path = %q, %v; want no paths and no error", got, err)
		}
	})
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
	// The process default budget (`spine serve`) reaches clients too.
	git.SetDefaultCheckoutLimits(git.CheckoutLimits{Budget: &raised})
	t.Cleanup(git.ResetDefaultCheckoutLimitsForTest)
	if got, need := git.NewCLIClient(dir).ListingLimitForTest(), raised.MaxPathBytesTotal+raised.MaxEntries+margin; got < need {
		t.Errorf("listing limit under the process budget = %d, want >= %d", got, need)
	}
}
