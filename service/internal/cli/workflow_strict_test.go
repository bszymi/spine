package cli_test

// INIT-024 TASK-002 (review round 1): `spine workflow list/show/resolve`
// accept a workflow file exactly as the server's projection does. They
// used to decode leniently and read only the first YAML document, so a
// file with a second document was listed Active and resolved as a binding
// while the server quarantined it.

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bszymi/spine/service/internal/cli"
)

const strictTestWorkflow = `id: wf-two-docs
name: Two Documents
version: "1.0"
status: Active
applies_to: [Task]
entry_step: execute
steps:
  - id: execute
    name: Execute
    type: manual
    outcomes:
      - id: done
        next_step: end
`

// captureStdout runs fn with os.Stdout redirected and returns what it
// wrote.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan []byte)
	go func() {
		b, _ := io.ReadAll(r)
		done <- b
	}()
	ferr := fn()
	_ = w.Close()
	os.Stdout = old
	return string(<-done), ferr
}

func strictRepo(t *testing.T) string {
	t.Helper()
	dir := setupWorkflowRepo(t)
	write := func(rel, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("workflows/two-docs.yaml", strictTestWorkflow+"---\nid: other\n")
	write("workflows/empty-trailer.yaml", strings.Replace(strictTestWorkflow, "wf-two-docs", "wf-empty-trailer", 1)+"---\n")
	write("initiatives/task.md", "---\nid: TASK-001\ntype: Task\ntitle: T\nstatus: Pending\n---\n# T\n")
	return dir
}

func TestWorkflowCLI_SecondDocumentRefusedLikeTheServer(t *testing.T) {
	dir := strictRepo(t)

	out, err := captureStdout(t, func() error { return cli.ListWorkflows(dir, cli.FormatJSON) })
	if err != nil {
		t.Fatalf("ListWorkflows: %v", err)
	}
	var listed []cli.WorkflowSummary
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatalf("decode list output %q: %v", out, err)
	}
	ids := map[string]bool{}
	for _, wf := range listed {
		ids[wf.ID] = true
	}
	if ids["wf-two-docs"] {
		t.Fatalf("list shows the two-document workflow: %+v", listed)
	}
	for _, want := range []string{"wf-task-default", "wf-adr", "wf-empty-trailer"} {
		if !ids[want] {
			t.Fatalf("list lacks %s: %+v", want, listed)
		}
	}

	if err := cli.ShowWorkflow(dir, "workflows/two-docs.yaml", cli.FormatJSON); err == nil ||
		!strings.Contains(err.Error(), "unexpected second document") {
		t.Fatalf("ShowWorkflow(two-docs) = %v, want the second-document refusal", err)
	}

	out, err = captureStdout(t, func() error { return cli.ResolveWorkflow(dir, "initiatives/task.md", cli.FormatJSON) })
	if err != nil {
		t.Fatalf("ResolveWorkflow: %v", err)
	}
	var resolved struct {
		Bindings []cli.WorkflowSummary `json:"bindings"`
	}
	if err := json.NewDecoder(bytes.NewReader([]byte(out))).Decode(&resolved); err != nil {
		t.Fatalf("decode resolve output %q: %v", out, err)
	}
	for _, b := range resolved.Bindings {
		if b.ID == "wf-two-docs" {
			t.Fatalf("resolve binds the two-document workflow: %+v", resolved.Bindings)
		}
	}
	if len(resolved.Bindings) != 2 {
		t.Fatalf("bindings = %+v, want wf-task-default and wf-empty-trailer", resolved.Bindings)
	}
}

// The detail still carries everything it showed before.
func TestWorkflowCLI_ShowDetail(t *testing.T) {
	dir := setupWorkflowRepo(t)
	out, err := captureStdout(t, func() error { return cli.ShowWorkflow(dir, "workflows/task-default.yaml", cli.FormatJSON) })
	if err != nil {
		t.Fatalf("ShowWorkflow: %v", err)
	}
	var d cli.WorkflowDetail
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if d.ID != "wf-task-default" || d.Version != "1.0.0" || d.Status != "Active" || d.Mode != "execution" ||
		d.Path != "workflows/task-default.yaml" || d.EntryStep != "draft" || len(d.Steps) != 2 ||
		d.Steps[1].Type != "review" || d.Steps[1].Timeout != "48h" || strings.Join(d.Steps[1].Outcomes, ",") != "approve,reject" {
		t.Fatalf("detail = %+v", d)
	}
}
