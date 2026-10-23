package branchprotect

// INIT-024 TASK-002, codex round 1: Match looks entries up by the path
// and its "/"-ancestors instead of scanning them. These tests pin that the
// lookup decides exactly what the scan decided, and that an evaluation is
// no longer quadratic in the number of derived entries.

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/core/domain"
)

// scanMatch is Match before the index: the same fail-closed prelude, then
// a scan of the folded entries.
func scanMatch(entries []string, p string) bool {
	if p == "" || domain.UnsafeRepositoryPath(p) {
		return true
	}
	if strings.HasPrefix(p, `"`) {
		dec, ok := unquoteGitPath(p)
		if !ok || dec == "" || domain.UnsafeRepositoryPath(dec) {
			return true
		}
		p = dec
	}
	q := foldPath(strings.TrimLeft(p, "/"))
	for _, e := range entries {
		if q == e || strings.HasPrefix(q, e+"/") {
			return true
		}
	}
	return false
}

func TestGovernancePaths_IndexedMatchEqualsScan(t *testing.T) {
	g, err := NewGovernancePaths([]string{".spine/", ".spine.yaml", "workflows/", "Spine/Workflows/**", "a/b"})
	if err != nil {
		t.Fatal(err)
	}
	g = g.WithLiteralEntries("x//workflows", "app/[id]/workflows", "wor\u212aflows/deep/workflows")
	fixed := []string{
		"", "/", "//", "a", "a/", "a/b", "a/b/", "a/bc", "a//b", "a/b/c", "A/B/c", "ab/c",
		"workflows", "workflows/", "workflowsx", "Workflows/a.yaml", "/workflows/a.yaml", "//workflows/a",
		"spine/workflows", "spine/workflowsx/a", "SPINE/WORKFLOWS/x", "spine/workflow",
		".spine", ".spine.yaml", ".spine.yaml/x", ".spine.yamlx", ".\u017fpine/x",
		"x//workflows/a.yaml", "x/workflows/a.yaml", "app/[id]/workflows/a", "app/[iD]/WORKFLOWS",
		"workflows/deep/workflows/x", "docs/a.md", `"a/b\303\244"`, `"docs/x"`, `"bad`, "a\x01",
	}
	for _, p := range fixed {
		if got, want := g.Match(p), scanMatch(g.entries, p); got != want {
			t.Errorf("Match(%q) = %v, the scan says %v", p, got, want)
		}
	}
	// Random paths over an alphabet that exercises separators, case and
	// the non-ASCII fold partners (deterministic seed).
	alphabet := []string{"a", "A", "b", "/", "/", ".", "k", "K", "\u212a", "s", "\u017f", "workflows", "spine", "[", "x", `"`, `\`}
	r := rand.New(rand.NewSource(20261023)) //nolint:gosec // G404: deterministic test corpus
	for i := 0; i < 20000; i++ {
		var b strings.Builder
		for n := r.Intn(8); n >= 0; n-- {
			b.WriteString(alphabet[r.Intn(len(alphabet))])
		}
		p := b.String()
		if got, want := g.Match(p), scanMatch(g.entries, p); got != want {
			t.Fatalf("Match(%q) = %v, the scan says %v", p, got, want)
		}
	}
}

// TestGovernanceCheck_TouchedLinearInDerivedEntries: a run that changes a
// YAML file in each of many "workflows" directories adds one entry per
// file; evaluating it must stay fast. With the scan, 80,000
// such paths took about 40 s on an idle machine (each Match walked every entry).
func TestGovernanceCheck_TouchedLinearInDerivedEntries(t *testing.T) {
	const n = 80000
	changed := make([]string, n)
	for i := range changed {
		changed[i] = fmt.Sprintf("d%06d/workflows/x.yaml", i)
	}
	start := time.Now()
	c := &GovernanceCheck{Paths: defaultGovernancePaths().WithLiteralEntries(WorkflowDefinitionPaths(changed)...), ChangedPaths: changed}
	touched := c.Touched()
	elapsed := time.Since(start)
	if len(touched) != n {
		t.Fatalf("touched %d paths, want %d", len(touched), n)
	}
	// A generous bound (a loaded or -race run is far slower than an idle
	// one): the indexed lookup takes about 0.1 s here, the scan about 40 s.
	if elapsed > 10*time.Second {
		t.Fatalf("Touched over %d derived entries took %s", n, elapsed)
	}
	t.Logf("%d paths, %d entries: %s", n, len(c.Paths.Entries()), elapsed)
}
