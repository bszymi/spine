package gateway_test

// INIT-023 TASK-015 (audit P3-28): the standalone gateway refuses workflow
// bodies whose step, divergence-point, divergence-branch or
// convergence-point IDs fall outside ^[a-z0-9][a-z0-9_-]{0,63}$ (AC 3).
// The body is the workflow from the audit PoC
// (poc/delivery-scheduler/poc_stepid_test.go, step ID
// "a\ndata: {...}\n\nid: x"), which core workflow validation accepts.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bszymi/spine/core/domain"
)

const pocWorkflowYAML = "id: poc\nname: PoC\nversion: \"1.0\"\nstatus: Active\ndescription: d\napplies_to: [Task]\n" +
	"entry_step: \"a\\ndata: {\\\"type\\\":\\\"run_completed\\\"}\\n\\nid: x\"\n" +
	"steps:\n  - id: \"a\\ndata: {\\\"type\\\":\\\"run_completed\\\"}\\n\\nid: x\"\n    name: A\n    type: manual\n" +
	"    outcomes:\n      - id: done\n        name: Done\n        next_step: end\n"

func pocWorkflowJSON(t *testing.T, withID bool) string {
	t.Helper()
	m := map[string]string{"body": pocWorkflowYAML}
	if withID {
		m["id"] = "poc"
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// decodeIdentifierErrors reads the 422 body and returns the field names of
// the identifier errors in its detail.
func decodeIdentifierErrors(t *testing.T, resp *http.Response) []string {
	t.Helper()
	var out struct {
		Errors []struct {
			Code   string                   `json:"code"`
			Detail []domain.ValidationError `json:"detail"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	var fields []string
	for _, e := range out.Errors {
		for _, d := range e.Detail {
			if strings.Contains(d.Message, "\n") {
				t.Errorf("error message carries a raw newline: %q", d.Message)
			}
			fields = append(fields, d.Field)
		}
	}
	return fields
}

func TestP328_WorkflowCreate_OperatorDirectCommit_RefusesUnsafeStepID(t *testing.T) {
	svc := newFakeWorkflowSvc()
	ts, tok := newWorkflowServer(t, svc, domain.RoleOperator)
	defer ts.Close()
	resp := doWorkflowJSON(t, http.MethodPost, ts.URL+"/api/v1/workflows", tok, pocWorkflowJSON(t, true))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	if fields := decodeIdentifierErrors(t, resp); len(fields) != 1 || fields[0] != "steps[0].id" {
		t.Errorf("identifier errors = %v, want [steps[0].id]", fields)
	}
	if svc.lastCreateID != "" {
		t.Fatalf("workflow service must not be called; got create id %q", svc.lastCreateID)
	}
}

func TestP328_WorkflowCreate_ReviewerPlanningRun_RefusesUnsafeStepID(t *testing.T) {
	svc := newFakeWorkflowSvc()
	planner := &fakeWFPlanningStarter{}
	ts, tok := newWorkflowServerWithPlanning(t, svc, planner, domain.RoleReviewer)
	defer ts.Close()
	resp := doWorkflowJSON(t, http.MethodPost, ts.URL+"/api/v1/workflows", tok, pocWorkflowJSON(t, true))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	if planner.lastID != "" {
		t.Fatalf("planning run must not start; got %q", planner.lastID)
	}
	if svc.lastCreateID != "" {
		t.Fatalf("workflow service must not be called; got create id %q", svc.lastCreateID)
	}
}

func TestP328_WorkflowUpdate_RefusesUnsafeStepID(t *testing.T) {
	svc := newFakeWorkflowSvc()
	svc.stored["poc"] = &domain.WorkflowDefinition{ID: "poc", Version: "1.0.0", Path: "workflows/poc.yaml"}
	ts, tok := newWorkflowServer(t, svc, domain.RoleOperator)
	defer ts.Close()
	resp := doWorkflowJSON(t, http.MethodPut, ts.URL+"/api/v1/workflows/poc", tok, pocWorkflowJSON(t, false))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	if svc.lastUpdateID != "" {
		t.Fatalf("workflow service must not be called; got update id %q", svc.lastUpdateID)
	}
}

// identifierRuleBody has a well-formed step ID and refused divergence,
// branch and convergence IDs (upper case, a space, a dot).
const identifierRuleBody = "id: poc\nname: P\nversion: \"1\"\nstatus: Active\ndescription: d\napplies_to: [Task]\nentry_step: s\n" +
	"steps:\n  - id: s\n    name: S\n    type: manual\n    diverge: \"Dv\"\n    outcomes:\n      - id: done\n        name: Done\n        next_step: end\n" +
	"divergence_points:\n  - id: \"Dv\"\n    mode: structured\n    branches:\n      - id: \"b r\"\n        name: B\n        start_step: s\n" +
	"convergence_points:\n  - id: \"c.1\"\n    strategy: select_one\n    entry_policy: all_branches_terminal\n    evaluation_step: s\n"

func TestP328_WorkflowValidate_ReportsUnsafeIdentifiers(t *testing.T) {
	svc := newFakeWorkflowSvc()
	ts, tok := newWorkflowServer(t, svc, domain.RoleReviewer)
	defer ts.Close()
	b, err := json.Marshal(map[string]string{"body": identifierRuleBody})
	if err != nil {
		t.Fatal(err)
	}
	resp := doWorkflowJSON(t, http.MethodPost, ts.URL+"/api/v1/workflows/poc/validate", tok, string(b))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		Status string                   `json:"status"`
		Errors []domain.ValidationError `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Status != "failed" {
		t.Fatalf("status = %q, want failed", out.Status)
	}
	fields := map[string]bool{}
	for _, e := range out.Errors {
		fields[e.Field] = true
	}
	for _, f := range []string{"divergence_points[0].id", "divergence_points[0].branches[0].id", "convergence_points[0].id"} {
		if !fields[f] {
			t.Errorf("missing identifier error for %s; got %+v", f, out.Errors)
		}
	}
	if fields["steps[0].id"] {
		t.Errorf("the well-formed step id was reported: %+v", out.Errors)
	}

	// The same body is refused by POST, with the same three fields.
	b, err = json.Marshal(map[string]string{"id": "poc", "body": identifierRuleBody})
	if err != nil {
		t.Fatal(err)
	}
	ts2, tok2 := newWorkflowServer(t, newFakeWorkflowSvc(), domain.RoleOperator)
	defer ts2.Close()
	resp2 := doWorkflowJSON(t, http.MethodPost, ts2.URL+"/api/v1/workflows", tok2, string(b))
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("POST status = %d, want 422", resp2.StatusCode)
	}
	if got := decodeIdentifierErrors(t, resp2); len(got) != 3 {
		t.Errorf("POST identifier errors = %v, want the three fields", got)
	}
}

// A body that does not parse is left to the workflow service: the check
// adds nothing and the service's own contract answers.
func TestP328_WorkflowCreate_UnparsableBodyLeftToService(t *testing.T) {
	svc := newFakeWorkflowSvc()
	ts, tok := newWorkflowServer(t, svc, domain.RoleOperator)
	defer ts.Close()
	b, _ := json.Marshal(map[string]string{"id": "x", "body": "id: x\nnot_a_field: [\n"})
	resp := doWorkflowJSON(t, http.MethodPost, ts.URL+"/api/v1/workflows", tok, string(b))
	defer resp.Body.Close()
	if svc.lastCreateID != "x" {
		t.Fatalf("an unparsable body must reach the service; status %d", resp.StatusCode)
	}
}

// Guard (passes before and after): every workflow shipped in this
// repository is accepted by POST /workflows.
func TestP328_ShippedWorkflowsConform(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "..", "workflows", "*.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no shipped workflows found: %v", err)
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		svc := newFakeWorkflowSvc()
		ts, tok := newWorkflowServer(t, svc, domain.RoleOperator)
		id := strings.TrimSuffix(filepath.Base(f), ".yaml")
		b, _ := json.Marshal(map[string]string{"id": id, "body": string(raw)})
		resp := doWorkflowJSON(t, http.MethodPost, ts.URL+"/api/v1/workflows", tok, string(b))
		resp.Body.Close()
		ts.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Errorf("%s: status %d, want 201 (shipped workflow refused)", f, resp.StatusCode)
		}
	}
}
