package workflow_test

import (
	"testing"

	"github.com/bszymi/spine/core/workflow"
)

// INIT-023 TASK-014 §3.5 / AC 17: the canonical workflow-path test behind
// every artifact-surface guard.
func TestInWorkflowsDir(t *testing.T) {
	cases := map[string]bool{
		// workflow paths, every spelling
		"workflows/x.yaml":               true,
		"/workflows/x.yaml":              true,
		"./workflows/x.yaml":             true,
		"governance/../workflows/x.yaml": true,
		"a/b/../../workflows/x.yaml":     true,
		"../workflows/x.yaml":            true,
		"workflows//x.yaml":              true,
		"workflows":                      true,
		"workflows/":                     true,
		"Workflows/x.yaml":               true,
		"WORKFLOWS/x.yaml":               true,
		"worKflows/x.yaml":               true, // KELVIN SIGN folds to k
		// not workflow paths
		"workflows-guide.md":    false,
		"docs/workflows/x.md":   false,
		"governance/charter.md": false,
		"":                      false,
		"/":                     false,
		"workflowsx/a.yaml":     false,
		"initiatives/workflows": false,
		"workflows.yaml":        false,
	}
	for p, want := range cases {
		if got := workflow.InWorkflowsDir(p); got != want {
			t.Errorf("InWorkflowsDir(%q) = %v, want %v", p, got, want)
		}
	}
}
