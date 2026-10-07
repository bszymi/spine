package artifact_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/artifact"
	"github.com/bszymi/spine/testutil"
)

// INIT-023 TASK-011: DiscoverChangesFromDiffs is DiscoverChanges minus
// its Diff call, and every ReadFile failure it skips is recorded.

// readFailGit serves canned diffs and fails ReadFile for chosen paths
// with a chosen error; other paths delegate to a real client.
type readFailGit struct {
	git.GitClient
	fail map[string]error // path -> error
}

func (g *readFailGit) ReadFile(ctx context.Context, ref, path string) ([]byte, error) {
	if err, ok := g.fail[path]; ok {
		return nil, err
	}
	return g.GitClient.ReadFile(ctx, ref, path)
}

func TestDiscoverChangesFromDiffs_MatchesDiscoverChanges(t *testing.T) {
	client, repo := setupDiscoveryRepo(t)
	ctx := context.Background()
	before, _ := client.Head(ctx)
	testutil.WriteFile(t, repo, "governance/guidelines.md", "---\ntype: Governance\ntitle: Guidelines\nstatus: Living Document\n---\n\n# G\n")
	testutil.WriteFile(t, repo, "governance/charter.md", "---\ntype: Governance\ntitle: Charter v2\nstatus: Foundational\n---\n\n# C\n")
	testutil.GitAdd(t, repo, ".", "change")
	after, _ := client.Head(ctx)

	want, err := artifact.DiscoverChanges(ctx, client, before, after)
	if err != nil {
		t.Fatal(err)
	}
	diffs, err := client.Diff(ctx, before, after)
	if err != nil {
		t.Fatal(err)
	}
	got, err := artifact.DiscoverChangesFromDiffs(ctx, client, before, after, diffs)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DiscoverChangesFromDiffs differs from DiscoverChanges:\n got %+v\nwant %+v", got, want)
	}
	if len(got.ReadErrors) != 0 {
		t.Fatalf("healthy reads must record nothing, got %+v", got.ReadErrors)
	}
}

func TestDiscoverChangesFromDiffs_RecordsEveryReadFailure(t *testing.T) {
	client, _ := setupDiscoveryRepo(t)
	ctx := context.Background()
	boom := &git.GitError{Kind: git.ErrKindTransient, Op: "read_file", Message: "repository locked"}
	g := &readFailGit{GitClient: client, fail: map[string]error{
		"a.md": boom, "b.md": boom, "c.md": boom, "d-old.md": boom, "d-new.md": boom,
	}}
	diffs := []git.FileDiff{
		{Path: "a.md", Status: "added"},
		{Path: "b.md", Status: "deleted"},
		{Path: "c.md", Status: "modified"},
		{Path: "d-new.md", OldPath: "d-old.md", Status: "renamed"},
		{Path: "not-markdown.yaml", Status: "added"},
	}
	cs, err := artifact.DiscoverChangesFromDiffs(ctx, g, "from", "to", diffs)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.Created)+len(cs.Modified)+len(cs.Deleted) != 0 {
		t.Fatalf("unreadable paths must still be skipped, got %+v", cs)
	}
	type rec struct{ path, ref string }
	var got []rec
	for _, re := range cs.ReadErrors {
		if !errors.Is(re.Err, boom) || re.Message == "" {
			t.Fatalf("record must carry the error itself: %+v", re)
		}
		got = append(got, rec{re.Path, re.Ref})
	}
	want := []rec{
		{"a.md", "to"},
		{"b.md", "from"},
		{"c.md", "from"}, {"c.md", "to"},
		{"d-old.md", "from"}, {"d-new.md", "to"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadErrors = %+v, want %+v", got, want)
	}
}
