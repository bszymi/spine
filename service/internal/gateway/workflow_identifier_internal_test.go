package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bszymi/spine/core/workflow"
)

// TestP328_ShippedWorkflowIdentifiersChecked makes the shipped-workflow
// guard (TestP328_ShippedWorkflowsConform) meaningful: every shipped
// workflow decodes through the same decoder the identifier check uses, so
// the check really ran, and each identifier it inspects conforms.
func TestP328_ShippedWorkflowIdentifiersChecked(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "..", "workflows", "*.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no shipped workflows found: %v", err)
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := workflow.ProjectionDefinition(workflowIdentifierCheckPath, raw); err != nil {
			t.Errorf("%s does not decode, so the identifier check would not run: %v", f, err)
			continue
		}
		if bad := workflowIdentifierErrors(string(raw)); len(bad) > 0 {
			t.Errorf("%s: %+v", f, bad)
		}
	}
}

func TestP328_WorkflowIdentifierPattern(t *testing.T) {
	for v, want := range map[string]bool{
		"execute":               true,
		"review_2":              true,
		"0-draft":               true,
		strings.Repeat("a", 64): true,
		strings.Repeat("a", 65): false,
		"":                      false,
		"-lead":                 false,
		"_lead":                 false,
		"Upper":                 false,
		"a.b":                   false,
		"a/b":                   false,
		"a b":                   false,
		"a\nb":                  false,
		"a\n":                   false,
		"ż":                     false,
		"a\ndata: x\n\nid: y":   false,
	} {
		if got := workflowIdentifierPattern.MatchString(v); got != want {
			t.Errorf("%q: got %v want %v", v, got, want)
		}
	}
}

// A long refused identifier is echoed bounded and quoted.
func TestP328_WorkflowIdentifierErrorEchoBounded(t *testing.T) {
	long := strings.Repeat("ż", 200) + "\n"
	body := "id: poc\nname: P\nversion: \"1\"\nstatus: Active\ndescription: d\napplies_to: [Task]\nentry_step: s\n" +
		"steps:\n  - id: \"" + strings.ReplaceAll(long, "\n", `\n`) + "\"\n    name: S\n    type: manual\n" +
		"    outcomes:\n      - id: done\n        name: Done\n        next_step: end\n"
	bad := workflowIdentifierErrors(body)
	if len(bad) != 1 {
		t.Fatalf("errors = %+v, want one", bad)
	}
	if msg := bad[0].Message; len(msg) > 200 || strings.ContainsAny(msg, "\n") {
		t.Errorf("message not bounded or not quoted: %q", msg)
	}
}
