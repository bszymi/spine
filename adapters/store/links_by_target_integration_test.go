//go:build integration

package store_test

import (
	"context"
	"sort"
	"testing"

	"github.com/bszymi/spine/adapters/store"
)

// ADR-017 §5: a link target is stored as written in front matter (leading
// slash); callers look it up by projection path (no slash). Both forms of
// the lookup must find both forms of the stored target, and nothing else.
func TestQueryArtifactLinksByTarget_BothPathForms(t *testing.T) {
	s := store.NewTestStore(t)
	ctx := context.Background()
	defer s.CleanupTestData(ctx, t)

	const target = "assumptions/ASM-001-x.md"
	seed := map[string][]store.ArtifactLink{
		"architecture/adr/ADR-001-a.md": {{SourcePath: "architecture/adr/ADR-001-a.md", TargetPath: "/" + target, LinkType: "depends_on"}},
		"architecture/adr/ADR-002-b.md": {{SourcePath: "architecture/adr/ADR-002-b.md", TargetPath: target, LinkType: "depends_on"}},
		"architecture/adr/ADR-003-c.md": {{SourcePath: "architecture/adr/ADR-003-c.md", TargetPath: "/assumptions/ASM-001-x.md.bak", LinkType: "depends_on"}},
		"architecture/adr/ADR-004-d.md": {{SourcePath: "architecture/adr/ADR-004-d.md", TargetPath: "/assumptions/ASM-002-y.md", LinkType: "depends_on"}},
	}
	for src, links := range seed {
		if err := s.UpsertArtifactLinks(ctx, src, links, "c0ffee"); err != nil {
			t.Fatalf("upsert %s: %v", src, err)
		}
	}

	for _, lookup := range []string{target, "/" + target} {
		got, err := s.QueryArtifactLinksByTarget(ctx, lookup)
		if err != nil {
			t.Fatalf("lookup %q: %v", lookup, err)
		}
		var sources []string
		for _, l := range got {
			sources = append(sources, l.SourcePath)
		}
		sort.Strings(sources)
		if len(sources) != 2 || sources[0] != "architecture/adr/ADR-001-a.md" || sources[1] != "architecture/adr/ADR-002-b.md" {
			t.Errorf("lookup %q found %v, want ADR-001 and ADR-002 only", lookup, sources)
		}
	}
}
