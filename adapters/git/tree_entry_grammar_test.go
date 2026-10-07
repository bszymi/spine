package git_test

// INIT-023 wave 2 integration: TreeEntryType (TASK-011) follows the
// CLIClient ref grammar (TASK-002). Test names start with RefGrammar so
// the git-floor CI job (git 2.39.5) selects them.

import (
	"context"
	"reflect"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/testutil"
)

// TestRefGrammar_TreeEntryTypeArgvShape pins the argv: the ref is a
// Rev-class argument (exact branch lookup, object id to git) after
// --end-of-options, and the path is the last argument with no "--"
// after it — after --end-of-options ls-tree would take a "--" as a
// second pathspec.
func TestRefGrammar_TreeEntryTypeArgvShape(t *testing.T) {
	c, _, _, _, featOID := grammarRepo(t)
	rec := testutil.InstallGitRecorder(t)
	ctx := context.Background()
	lookup := func(n string) []string { return []string{"show-ref", "--verify", "--hash", "refs/heads/" + n} }
	lsTree := func(rev string) []string {
		return []string{"--literal-pathspecs", "ls-tree", "-z", "--full-tree", "--end-of-options", rev, "f.txt"}
	}
	for _, tc := range []struct {
		name, ref string
		want      [][]string
	}{
		{"branch", "feat", [][]string{lookup("feat"), lsTree(featOID)}},
		{"refs/heads/ branch", "refs/heads/feat", [][]string{lookup("feat"), lsTree(featOID)}},
		{"object id", featOID, [][]string{lsTree(featOID)}},
		{"HEAD", "HEAD", [][]string{lsTree("HEAD")}},
		{"refs/tags/", "refs/tags/t1", [][]string{lsTree("refs/tags/t1")}},
	} {
		rec.Reset(t)
		typ, err := c.TreeEntryType(ctx, tc.ref, "f.txt")
		if err != nil || typ != git.TreeEntryBlob {
			t.Errorf("%s: TreeEntryType = %q, %v; want blob", tc.name, typ, err)
		}
		if got := rec.Calls(t); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s argv:\n got  %q\n want %q", tc.name, got, tc.want)
		}
	}
}

// TestRefGrammar_TreeEntryTypeRefusedBeforeExec: an invalid or
// option-shaped ref is the grammar's permanent ErrInvalidRef refusal and
// never starts git.
func TestRefGrammar_TreeEntryTypeRefusedBeforeExec(t *testing.T) {
	c, _, _, _, _ := grammarRepo(t)
	rec := testutil.InstallGitRecorder(t)
	ctx := context.Background()
	for _, v := range []string{"", "--upload-pack=x", "-f", "--output=/tmp/x", "a\nb", "a..b", "a:b"} {
		rec.Reset(t)
		_, err := c.TreeEntryType(ctx, v, "f.txt")
		assertRefused(t, rec, "TreeEntryType("+v+")", err)
	}
}

// TestRefGrammar_TreeEntryTypeIgnoresShadowingRefs: git resolves a bare
// name refs/<n> first, then refs/tags/<n>, then refs/heads/<n>. A tag or
// a top-level ref named after a branch must not answer for the branch.
// Before the integration fix TreeEntryType handed git the bare name, so
// the tag's tree (which lacks the file) answered "no entry".
func TestRefGrammar_TreeEntryTypeIgnoresShadowingRefs(t *testing.T) {
	c, repo, _, mainOID, _ := grammarRepo(t)
	runGit(t, repo, "switch", "-q", "feat")
	testutil.WriteFile(t, repo, "only-on-feat.md", "x\n")
	testutil.WriteFile(t, repo, "--", "dash-dash\n")
	testutil.WriteFile(t, repo, "-lead.md", "lead\n")
	runGit(t, repo, "add", "--", "only-on-feat.md", "--", "-lead.md")
	runGit(t, repo, "commit", "-qm", "feat files")
	runGit(t, repo, "switch", "-q", "main")
	ctx := context.Background()

	for _, shadow := range []string{"refs/tags/feat", "refs/feat"} {
		runGit(t, repo, "update-ref", shadow, mainOID)
		for _, p := range []string{"only-on-feat.md", "--", "-lead.md"} {
			typ, err := c.TreeEntryType(ctx, "feat", p)
			if err != nil || typ != git.TreeEntryBlob {
				t.Errorf("with %s: TreeEntryType(feat, %q) = %q, %v; want blob (the branch's tree)", shadow, p, typ, err)
			}
		}
		runGit(t, repo, "update-ref", "-d", shadow)
	}

	// A path "--" is a literal path, not a terminator: on main (no such
	// file) it is "no entry", never the listing of another path.
	if typ, err := c.TreeEntryType(ctx, "main", "--"); err != nil || typ != "" {
		t.Errorf("TreeEntryType(main, \"--\") = %q, %v; want no entry", typ, err)
	}
	// A missing branch is not-found, never a DWIM fallback.
	if _, err := c.TreeEntryType(ctx, "refs/heads/nope", "f.txt"); err == nil {
		t.Error("TreeEntryType on a missing refs/heads/ branch must fail")
	}
}
