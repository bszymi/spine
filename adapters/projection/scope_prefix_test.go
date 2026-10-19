package projection

import "testing"

// INIT-023 TASK-017 review round 1: the projection's artifact key and its
// workflow paths agree on one prefix whatever artifacts_dir spelling an
// embedder passes. artifactKey once re-normalised the already-normalised
// dir on every call; the normalisation trims one leading "./" and one
// trailing "/" (after trimming whitespace), so it is not idempotent and
// "foo//", "././foo" or "foo /" were keyed under another prefix than the
// one isWorkflow and WorkflowDirs use — and than before the predicate
// moved to core/artifact (the CHANGELOG's "no behaviour change").
func TestPathScope_OnePrefixForEverySpelling(t *testing.T) {
	for _, in := range []string{"spine", "spine/", " ./spine/ ", "foo//", "././foo", "foo /", " foo/ /"} {
		p := newPathScope(in)
		if p.prefix == "" {
			t.Fatalf("newPathScope(%q) is the root", in)
		}
		if key, ok := p.artifactKey(p.prefix + "initiatives/a.md"); !ok || key != "initiatives/a.md" {
			t.Errorf("newPathScope(%q): artifactKey(%q) = %q,%v; want initiatives/a.md under the scope's own prefix %q",
				in, p.prefix+"initiatives/a.md", key, ok, p.prefix)
		}
		if !p.isWorkflow(p.prefix + "workflows/x.yaml") {
			t.Errorf("newPathScope(%q): %q is not a workflow", in, p.prefix+"workflows/x.yaml")
		}
		dirs := WorkflowDirs(in)
		if len(dirs) != 2 || dirs[1] != p.prefix+"workflows/" {
			t.Errorf("WorkflowDirs(%q) = %v, want the scope's prefix %q", in, dirs, p.prefix)
		}
		// A path outside the scope's own prefix is never an artifact of it.
		for _, other := range []string{"initiatives/a.md", "x" + p.prefix + "a.md"} {
			if key, ok := p.artifactKey(other); ok {
				t.Errorf("newPathScope(%q): artifactKey(%q) = %q, in scope although outside %q", in, other, key, p.prefix)
			}
		}
	}
}
