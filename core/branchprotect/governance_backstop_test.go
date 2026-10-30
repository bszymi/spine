package branchprotect_test

// INIT-024 TASK-002 (review finding N-5): the decision-time additions to
// the guarded set — GovernancePaths.WithLiteralEntries and the structural
// backstop WorkflowDefinitionPaths.

import (
	"reflect"
	"strings"
	"testing"

	"github.com/bszymi/spine/core/branchprotect"
)

func TestWorkflowDefinitionPaths(t *testing.T) {
	for _, tc := range []struct {
		name  string
		paths []string
		want  []string
	}{
		{"root workflows", []string{"workflows/task-default.yaml"}, []string{"workflows/task-default.yaml"}},
		{"artifacts_dir workflows", []string{"spine/workflows/task-default.yaml"}, []string{"spine/workflows/task-default.yaml"}},
		{"any depth, .yml", []string{"a/b/c/workflows/x.yml"}, []string{"a/b/c/workflows/x.yml"}},
		{"case-folded dir and extension", []string{"Docs/WORKFLOWS/x.YML"}, []string{"Docs/WORKFLOWS/x.YML"}},
		{"KELVIN SIGN", []string{"docs/wor\u212aflows/x.yaml"}, []string{"docs/wor\u212aflows/x.yaml"}},
		{"hidden .yaml leaf", []string{"x/workflows/.yaml"}, []string{"x/workflows/.yaml"}},
		{"glob characters are literal", []string{"app/[id]/workflows/x.yaml"}, []string{"app/[id]/workflows/x.yaml"}},
		{"leading slash", []string{"/spine/workflows/x.yaml"}, []string{"spine/workflows/x.yaml"}},
		{"C-quoted path decodes", []string{`"sp\303\244ce/workflows/x.yaml"`}, []string{"sp\u00e4ce/workflows/x.yaml"}},
		{"once per folded path", []string{"d/workflows/a.yaml", "D/Workflows/A.YAML", "d/workflows/b.yml"}, []string{"d/workflows/a.yaml", "d/workflows/b.yml"}},
		{"only the definitions of a directory", []string{".github/workflows/ci.yml", ".github/workflows/scripts/build.sh", ".github/workflows/README.md", ".github/workflows/matrix/linux.json"}, []string{".github/workflows/ci.yml"}},
		{"nested file", []string{"workflows/steps/x.yaml"}, nil},
		{"not yaml", []string{"docs/workflows/README.md", "docs/workflows/x.yaml.bak"}, nil},
		{"file named workflows.yaml", []string{"docs/workflows.yaml"}, nil},
		{"root-level yaml", []string{"workflows.yaml", "x.yaml"}, nil},
		{"lookalike directory", []string{"docs/workflowsx/x.yaml", "docs/xworkflows/x.yaml"}, nil},
		// Match governs these whatever the entries; nothing to add.
		{"already governed", []string{"", "docs/\xff/workflows/x.yaml", `"bad\q/workflows/x.yaml`}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := branchprotect.WorkflowDefinitionPaths(tc.paths); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("WorkflowDefinitionPaths(%q) = %q, want %q", tc.paths, got, tc.want)
			}
		})
	}
}

// TestWorkflowDefinitionPaths_GovernsOnlyTheFiles: the backstop's entries
// are the definition files themselves, so a merge that also changes a
// script, a README or a nested file beside them governs only the files.
func TestWorkflowDefinitionPaths_GovernsOnlyTheFiles(t *testing.T) {
	changed := []string{
		".github/workflows/ci.yml",
		".github/workflows/scripts/build.sh",
		".github/workflows/README.md",
		".github/workflows/matrix/linux.json",
		".github/workflows/nested/x.yaml",
	}
	c := &branchprotect.GovernanceCheck{
		Paths:        defaultPaths(t).WithLiteralEntries(branchprotect.WorkflowDefinitionPaths(changed)...),
		ChangedPaths: changed,
	}
	if got := c.Touched(); !reflect.DeepEqual(got, []string{".github/workflows/ci.yml"}) {
		t.Fatalf("Touched() = %q, want only .github/workflows/ci.yml", got)
	}
}

func TestGovernancePaths_WithLiteralEntries(t *testing.T) {
	g := defaultPaths(t)
	w := g.WithLiteralEntries("spine/workflows", "/App/[id]/Workflows/", "", "/", "WORKFLOWS")
	if got, want := strings.Join(w.Entries(), ","), ".spine,.spine.yaml,app/[id]/workflows,spine/workflows,workflows"; got != want {
		t.Fatalf("entries = %q, want %q (folded, trimmed, deduplicated, empty skipped)", got, want)
	}
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"spine/workflows/steps/x.yaml", true}, // a prefix entry covers nested files
		{"SPINE/Workflows/x.yaml", true},
		{"app/[id]/workflows/x.yaml", true}, // literal, never a pattern
		{"app/i/workflows/x.yaml", false},
		{".spine/x", true}, // the configured set is kept
		{"spine/docs/a.md", false},
	} {
		if got := w.Match(tc.path); got != tc.want {
			t.Errorf("WithLiteralEntries(...).Match(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
	// g itself is unchanged.
	if strings.Join(g.Entries(), ",") != ".spine,.spine.yaml,workflows" || g.Match("spine/workflows/x.yaml") {
		t.Fatalf("WithLiteralEntries changed its receiver: %v", g.Entries())
	}
	// The zero value starts from the defaults, as Match does.
	var zero branchprotect.GovernancePaths
	z := zero.WithLiteralEntries("x/workflows")
	if !z.Match("workflows/a.yaml") || !z.Match(".spine.yaml") || !z.Match("x/workflows/a.yaml") {
		t.Fatalf("zero.WithLiteralEntries entries = %v, want the defaults plus x/workflows", z.Entries())
	}
	// No dirs: the same entries.
	if !reflect.DeepEqual(g.WithLiteralEntries().Entries(), g.Entries()) {
		t.Fatalf("WithLiteralEntries() = %v, want %v", g.WithLiteralEntries().Entries(), g.Entries())
	}
}
