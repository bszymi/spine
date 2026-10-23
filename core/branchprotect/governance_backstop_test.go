package branchprotect_test

// INIT-024 TASK-002 (review finding N-5): the decision-time additions to
// the guarded set — GovernancePaths.WithDirs and the structural backstop
// WorkflowDefinitionDirs.

import (
	"reflect"
	"strings"
	"testing"

	"github.com/bszymi/spine/core/branchprotect"
)

func TestWorkflowDefinitionDirs(t *testing.T) {
	for _, tc := range []struct {
		name  string
		paths []string
		want  []string
	}{
		{"root workflows", []string{"workflows/task-default.yaml"}, []string{"workflows"}},
		{"artifacts_dir workflows", []string{"spine/workflows/task-default.yaml"}, []string{"spine/workflows"}},
		{"any depth, .yml", []string{"a/b/c/workflows/x.yml"}, []string{"a/b/c/workflows"}},
		{"case-folded dir and extension", []string{"Docs/WORKFLOWS/x.YML"}, []string{"Docs/WORKFLOWS"}},
		{"KELVIN SIGN", []string{"docs/wor\u212aflows/x.yaml"}, []string{"docs/wor\u212aflows"}},
		{"hidden .yaml leaf", []string{"x/workflows/.yaml"}, []string{"x/workflows"}},
		{"glob characters are literal", []string{"app/[id]/workflows/x.yaml"}, []string{"app/[id]/workflows"}},
		{"leading slash", []string{"/spine/workflows/x.yaml"}, []string{"spine/workflows"}},
		{"C-quoted path decodes", []string{`"sp\303\244ce/workflows/x.yaml"`}, []string{"sp\u00e4ce/workflows"}},
		{"once per folded parent", []string{"d/workflows/a.yaml", "D/Workflows/b.yml", "d/workflows/c.yaml"}, []string{"d/workflows"}},
		{"nested file", []string{"workflows/steps/x.yaml"}, nil},
		{"not yaml", []string{"docs/workflows/README.md", "docs/workflows/x.yaml.bak"}, nil},
		{"file named workflows.yaml", []string{"docs/workflows.yaml"}, nil},
		{"root-level yaml", []string{"workflows.yaml", "x.yaml"}, nil},
		{"lookalike directory", []string{"docs/workflowsx/x.yaml", "docs/xworkflows/x.yaml"}, nil},
		// Match governs these whatever the entries; nothing to add.
		{"already governed", []string{"", "docs/\xff/workflows/x.yaml", `"bad\q/workflows/x.yaml`}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := branchprotect.WorkflowDefinitionDirs(tc.paths); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("WorkflowDefinitionDirs(%q) = %q, want %q", tc.paths, got, tc.want)
			}
		})
	}
}

func TestGovernancePaths_WithDirs(t *testing.T) {
	g := defaultPaths(t)
	w := g.WithDirs("spine/workflows", "/App/[id]/Workflows/", "", "/", "WORKFLOWS")
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
			t.Errorf("WithDirs(...).Match(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
	// g itself is unchanged.
	if strings.Join(g.Entries(), ",") != ".spine,.spine.yaml,workflows" || g.Match("spine/workflows/x.yaml") {
		t.Fatalf("WithDirs changed its receiver: %v", g.Entries())
	}
	// The zero value starts from the defaults, as Match does.
	var zero branchprotect.GovernancePaths
	z := zero.WithDirs("x/workflows")
	if !z.Match("workflows/a.yaml") || !z.Match(".spine.yaml") || !z.Match("x/workflows/a.yaml") {
		t.Fatalf("zero.WithDirs entries = %v, want the defaults plus x/workflows", z.Entries())
	}
	// No dirs: the same entries.
	if !reflect.DeepEqual(g.WithDirs().Entries(), g.Entries()) {
		t.Fatalf("WithDirs() = %v, want %v", g.WithDirs().Entries(), g.Entries())
	}
}
