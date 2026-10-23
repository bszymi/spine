package workflows_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/workflow"
)

func TestTaskSpikeWorkflow_Parses(t *testing.T) {
	content, err := os.ReadFile("task-spike.yaml")
	if err != nil {
		t.Fatalf("failed to read workflow: %v", err)
	}

	wf, err := workflow.Parse("workflows/task-spike.yaml", content)
	if err != nil {
		t.Fatalf("failed to parse workflow: %v", err)
	}

	if wf.ID != "task-spike" {
		t.Errorf("expected id task-spike, got %s", wf.ID)
	}
	if wf.Status != domain.WorkflowStatusDraft {
		t.Errorf("expected Draft, got %s", wf.Status)
	}
	if len(wf.AppliesTo) != 1 || wf.AppliesTo[0] != "Task" {
		t.Errorf("expected applies_to [Task], got %v", wf.AppliesTo)
	}
	if wf.EntryStep != "investigate" {
		t.Errorf("expected entry_step investigate, got %s", wf.EntryStep)
	}
	if len(wf.Steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(wf.Steps))
	}

	// Verify cyclic routing: review → investigate.
	review := wf.Steps[2]
	if review.Outcomes[1].NextStep != "investigate" {
		t.Errorf("expected needs_more_investigation → investigate, got %s", review.Outcomes[1].NextStep)
	}
}

func TestADRWorkflow_Parses(t *testing.T) {
	content, err := os.ReadFile("adr.yaml")
	if err != nil {
		t.Fatalf("failed to read workflow: %v", err)
	}

	wf, err := workflow.Parse("workflows/adr.yaml", content)
	if err != nil {
		t.Fatalf("failed to parse workflow: %v", err)
	}

	if wf.ID != "adr-review" {
		t.Errorf("expected id adr-review, got %s", wf.ID)
	}
	if len(wf.AppliesTo) != 1 || wf.AppliesTo[0] != "ADR" {
		t.Errorf("expected applies_to [ADR], got %v", wf.AppliesTo)
	}
	if wf.EntryStep != "propose" {
		t.Errorf("expected entry_step propose, got %s", wf.EntryStep)
	}
	if len(wf.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(wf.Steps))
	}

	// Verify evaluate outcomes.
	evaluate := wf.Steps[1]
	if len(evaluate.Outcomes) != 3 {
		t.Fatalf("expected 3 evaluate outcomes, got %d", len(evaluate.Outcomes))
	}
	if evaluate.Outcomes[0].ID != "accepted" {
		t.Errorf("expected accepted, got %s", evaluate.Outcomes[0].ID)
	}
	if evaluate.Outcomes[1].ID != "deprecated" {
		t.Errorf("expected deprecated, got %s", evaluate.Outcomes[1].ID)
	}
	if evaluate.Outcomes[2].NextStep != "propose" {
		t.Errorf("expected needs_revision → propose, got %s", evaluate.Outcomes[2].NextStep)
	}
}

func TestEpicLifecycleWorkflow_Parses(t *testing.T) {
	content, err := os.ReadFile("epic-lifecycle.yaml")
	if err != nil {
		t.Fatalf("failed to read workflow: %v", err)
	}

	wf, err := workflow.Parse("workflows/epic-lifecycle.yaml", content)
	if err != nil {
		t.Fatalf("failed to parse workflow: %v", err)
	}

	if wf.ID != "epic-lifecycle" {
		t.Errorf("expected id epic-lifecycle, got %s", wf.ID)
	}
	if len(wf.AppliesTo) != 1 || wf.AppliesTo[0] != "Epic" {
		t.Errorf("expected applies_to [Epic], got %v", wf.AppliesTo)
	}
	if wf.EntryStep != "plan" {
		t.Errorf("expected entry_step plan, got %s", wf.EntryStep)
	}
	if len(wf.Steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(wf.Steps))
	}

	// Verify no binding conflict: Epic type is unique to this workflow.
	// task-default and task-spike apply to Task, adr applies to ADR.
}

func TestArtifactCreationWorkflow_Parses(t *testing.T) {
	content, err := os.ReadFile("artifact-creation.yaml")
	if err != nil {
		t.Fatalf("failed to read workflow: %v", err)
	}

	wf, err := workflow.Parse("workflows/artifact-creation.yaml", content)
	if err != nil {
		t.Fatalf("failed to parse workflow: %v", err)
	}

	if wf.ID != "artifact-creation" {
		t.Errorf("expected id artifact-creation, got %s", wf.ID)
	}
	if wf.Status != domain.WorkflowStatusActive {
		t.Errorf("expected Active, got %s", wf.Status)
	}
	if wf.Mode != "creation" {
		t.Errorf("expected mode creation, got %s", wf.Mode)
	}
	if len(wf.AppliesTo) != 3 {
		t.Fatalf("expected 3 applies_to types, got %d", len(wf.AppliesTo))
	}
	expectedTypes := map[string]bool{"Initiative": true, "Epic": true, "Task": true}
	for _, at := range wf.AppliesTo {
		if !expectedTypes[at] {
			t.Errorf("unexpected applies_to type: %s", at)
		}
	}
	if wf.EntryStep != "draft" {
		t.Errorf("expected entry_step draft, got %s", wf.EntryStep)
	}
	if len(wf.Steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(wf.Steps))
	}

	// Verify step IDs and transitions.
	draft := wf.Steps[0]
	if draft.ID != "draft" {
		t.Errorf("expected step 0 id draft, got %s", draft.ID)
	}
	if draft.Outcomes[0].NextStep != "validate" {
		t.Errorf("expected draft → validate, got %s", draft.Outcomes[0].NextStep)
	}

	validate := wf.Steps[1]
	if validate.ID != "validate" {
		t.Errorf("expected step 1 id validate, got %s", validate.ID)
	}
	if validate.Type != domain.StepTypeAutomated {
		t.Errorf("expected validate type automated, got %s", validate.Type)
	}
	if len(validate.Outcomes) != 2 {
		t.Fatalf("expected 2 validate outcomes, got %d", len(validate.Outcomes))
	}
	if validate.Outcomes[0].NextStep != "review" {
		t.Errorf("expected valid → review, got %s", validate.Outcomes[0].NextStep)
	}
	if validate.Outcomes[1].NextStep != "draft" {
		t.Errorf("expected invalid → draft, got %s", validate.Outcomes[1].NextStep)
	}

	review := wf.Steps[2]
	if review.ID != "review" {
		t.Errorf("expected step 2 id review, got %s", review.ID)
	}
	if review.Type != domain.StepTypeReview {
		t.Errorf("expected review type review, got %s", review.Type)
	}
	if review.Outcomes[0].ID != "approved" {
		t.Errorf("expected first outcome approved, got %s", review.Outcomes[0].ID)
	}
	if review.Outcomes[0].NextStep != "end" {
		t.Errorf("expected approved → end, got %s", review.Outcomes[0].NextStep)
	}
	if review.Outcomes[0].Commit["status"] != "Pending" {
		t.Errorf("expected approved commit status Pending, got %s", review.Outcomes[0].Commit["status"])
	}
	if review.Outcomes[1].NextStep != "draft" {
		t.Errorf("expected needs_revision → draft, got %s", review.Outcomes[1].NextStep)
	}
}

func TestWorkflowLifecycleWorkflow_Parses(t *testing.T) {
	content, err := os.ReadFile("workflow-lifecycle.yaml")
	if err != nil {
		t.Fatalf("failed to read workflow: %v", err)
	}

	wf, err := workflow.Parse("workflows/workflow-lifecycle.yaml", content)
	if err != nil {
		t.Fatalf("failed to parse workflow: %v", err)
	}

	if wf.ID != "workflow-lifecycle" {
		t.Errorf("expected id workflow-lifecycle, got %s", wf.ID)
	}
	if wf.Status != domain.WorkflowStatusActive {
		t.Errorf("expected Active, got %s", wf.Status)
	}
	if wf.Mode != "creation" {
		t.Errorf("expected mode creation, got %s", wf.Mode)
	}
	if len(wf.AppliesTo) != 1 || wf.AppliesTo[0] != "Workflow" {
		t.Errorf("expected applies_to [Workflow], got %v", wf.AppliesTo)
	}
	if wf.EntryStep != "draft" {
		t.Errorf("expected entry_step draft, got %s", wf.EntryStep)
	}
	if len(wf.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(wf.Steps))
	}

	draft := wf.Steps[0]
	if draft.ID != "draft" || draft.Type != domain.StepTypeManual {
		t.Errorf("expected manual step 'draft', got id=%s type=%s", draft.ID, draft.Type)
	}
	if draft.Outcomes[0].NextStep != "review" {
		t.Errorf("expected draft → review, got %s", draft.Outcomes[0].NextStep)
	}

	review := wf.Steps[1]
	if review.ID != "review" || review.Type != domain.StepTypeReview {
		t.Errorf("expected review step 'review', got id=%s type=%s", review.ID, review.Type)
	}
	if len(review.Outcomes) != 2 {
		t.Fatalf("expected 2 review outcomes, got %d", len(review.Outcomes))
	}
	if review.Outcomes[0].ID != "approved" || review.Outcomes[0].NextStep != "end" {
		t.Errorf("expected approved → end, got id=%s next=%s", review.Outcomes[0].ID, review.Outcomes[0].NextStep)
	}
	if len(review.Outcomes[0].Commit) == 0 {
		t.Error("approved outcome must have a non-empty commit map to trigger merge")
	}
	if review.Outcomes[1].ID != "needs_rework" || review.Outcomes[1].NextStep != "draft" {
		t.Errorf("expected needs_rework → draft, got id=%s next=%s", review.Outcomes[1].ID, review.Outcomes[1].NextStep)
	}

	// Full validation suite must pass.
	result := workflow.Validate(wf)
	if result.Status != "passed" {
		t.Errorf("expected validation passed, got %s with errors %+v", result.Status, result.Errors)
	}
}

func TestNoBindingConflicts(t *testing.T) {
	// Load all workflows and verify no two Active workflows share
	// the same (applies_to type, mode) pair — mode disambiguates
	// execution workflows from creation workflows.
	files := []string{"task-default.yaml", "task-spike.yaml", "adr.yaml", "epic-lifecycle.yaml", "artifact-creation.yaml", "adr-creation.yaml", "document-creation.yaml", "workflow-lifecycle.yaml"}

	// key: "type:mode" → workflow ID
	bindingMap := make(map[string]string)
	for _, f := range files {
		content, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("failed to read %s: %v", f, err)
		}
		wf, err := workflow.Parse("workflows/"+f, content)
		if err != nil {
			t.Fatalf("failed to parse %s: %v", f, err)
		}
		if wf.Status != domain.WorkflowStatusActive {
			continue
		}
		for _, at := range wf.AppliesTo {
			key := at + ":" + wf.Mode
			if existing, ok := bindingMap[key]; ok {
				switch key {
				case "Task:execution":
					// task-default and task-spike deliberately share this
					// binding — work_type selection disambiguates at runtime.
					t.Logf("NOTE: binding %q matched by both %s and %s (disambiguated by work_type)", key, existing, wf.ID)
				default:
					t.Errorf("duplicate binding %q: both %s and %s match; the resolver will return ErrConflict at runtime",
						key, existing, wf.ID)
				}
			}
			bindingMap[key] = wf.ID
		}
	}

	// Execution bindings.
	if id := bindingMap["ADR:execution"]; id != "adr-review" {
		t.Errorf("expected ADR:execution → adr-review, got %s", id)
	}
	if id := bindingMap["Epic:execution"]; id != "epic-lifecycle" {
		t.Errorf("expected Epic:execution → epic-lifecycle, got %s", id)
	}

	// Creation bindings from artifact-creation.yaml.
	if id := bindingMap["Initiative:creation"]; id != "artifact-creation" {
		t.Errorf("expected Initiative:creation → artifact-creation, got %s", id)
	}
	if id := bindingMap["Task:creation"]; id != "artifact-creation" {
		t.Errorf("expected Task:creation → artifact-creation, got %s", id)
	}
	if id := bindingMap["Epic:creation"]; id != "artifact-creation" {
		t.Errorf("expected Epic:creation → artifact-creation, got %s", id)
	}

	// Creation bindings from adr-creation.yaml.
	if id := bindingMap["ADR:creation"]; id != "adr-creation" {
		t.Errorf("expected ADR:creation → adr-creation, got %s", id)
	}

	// Creation bindings from document-creation.yaml.
	if id := bindingMap["Governance:creation"]; id != "document-creation" {
		t.Errorf("expected Governance:creation → document-creation, got %s", id)
	}
	if id := bindingMap["Architecture:creation"]; id != "document-creation" {
		t.Errorf("expected Architecture:creation → document-creation, got %s", id)
	}
	if id := bindingMap["Product:creation"]; id != "document-creation" {
		t.Errorf("expected Product:creation → document-creation, got %s", id)
	}

	// Creation binding from workflow-lifecycle.yaml (ADR-008).
	if id := bindingMap["Workflow:creation"]; id != "workflow-lifecycle" {
		t.Errorf("expected Workflow:creation → workflow-lifecycle, got %s", id)
	}
}

// TestReferenceWorkflows_NoDeferredStatusPreconditions (INIT-023 TASK-007
// AC 15): no shipped workflow may require, on the run's own artifact, a
// status that only an earlier outcome's commit block sets. Commit status is
// applied at merge, so such a precondition can never hold mid-run and, under
// an enforcing step-entry policy, strands every run of the workflow. If a
// future change applies commit status mid-run (TASK-017), revisit
// workflow.DeferredStatusPreconditions; this test will say so.
func TestReferenceWorkflows_NoDeferredStatusPreconditions(t *testing.T) {
	files, err := filepath.Glob("*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no reference workflows found")
	}
	for _, f := range files {
		content, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		wf, err := workflow.Parse("workflows/"+f, content)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		for _, d := range workflow.DeferredStatusPreconditions(wf) {
			t.Errorf("%s: %s", f, d)
		}
	}
}

func TestEpicLifecycleWorkflow_ExecuteHasNoDeferredStatusPrecondition(t *testing.T) {
	content, err := os.ReadFile("epic-lifecycle.yaml")
	if err != nil {
		t.Fatal(err)
	}
	wf, err := workflow.Parse("workflows/epic-lifecycle.yaml", content)
	if err != nil {
		t.Fatal(err)
	}
	if wf.Version != "1.1" {
		t.Errorf("epic-lifecycle version = %q, want 1.1 (execute's unsatisfiable precondition removed)", wf.Version)
	}
	for _, s := range wf.Steps {
		if s.ID == "execute" && len(s.Preconditions) != 0 {
			t.Errorf("epic-lifecycle execute preconditions = %+v, want none", s.Preconditions)
		}
	}
}

// shippedWorkflows reads every shipped definition in this directory.
func shippedWorkflows(t *testing.T) map[string][]byte {
	t.Helper()
	files, err := filepath.Glob("*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no reference workflows found")
	}
	out := make(map[string][]byte, len(files))
	for _, f := range files {
		content, err := os.ReadFile(f) // #nosec G304 -- fixed test directory.
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		out[f] = content
	}
	return out
}

// TestReferenceWorkflows_TimeoutNeverRoutesIntoReview is INIT-024
// TASK-002 AC 6 (review finding N-4). An automated step's timeout is what
// happens when its validator is missing, hangs or is starved: the outcome
// it applies must not carry the run forward into human review as if the
// check had passed. The three creation workflows' validate steps used to
// time out as `valid`; they now time out as `invalid`, back to `draft`.
func TestReferenceWorkflows_TimeoutNeverRoutesIntoReview(t *testing.T) {
	for f, content := range shippedWorkflows(t) {
		wf, err := workflow.Parse("workflows/"+f, content)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		types := make(map[string]domain.StepType, len(wf.Steps))
		for _, s := range wf.Steps {
			types[s.ID] = s.Type
		}
		for _, s := range wf.Steps {
			if s.Type != domain.StepTypeAutomated || s.TimeoutOutcome == "" {
				continue
			}
			found := false
			for _, o := range s.Outcomes {
				if o.ID != s.TimeoutOutcome {
					continue
				}
				found = true
				if types[o.NextStep] == domain.StepTypeReview {
					t.Errorf("%s: automated step %q times out as %q, which routes to review step %q (a timed-out check must not pass)",
						f, s.ID, s.TimeoutOutcome, o.NextStep)
				}
			}
			if !found {
				t.Errorf("%s: automated step %q: timeout_outcome %q names no outcome", f, s.ID, s.TimeoutOutcome)
			}
		}
	}
}

// TestReferenceWorkflows_SingleDocument is INIT-024 TASK-002 AC 7 (review
// finding N-7): every shipped definition is exactly one YAML document —
// not even an empty trailing `---`, which strict Parse would still accept.
func TestReferenceWorkflows_SingleDocument(t *testing.T) {
	for f, content := range shippedWorkflows(t) {
		dec := yaml.NewDecoder(bytes.NewReader(content))
		docs := 0
		for {
			var n yaml.Node
			err := dec.Decode(&n)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatalf("%s: document %d: %v", f, docs+1, err)
			}
			docs++
		}
		if docs != 1 {
			t.Errorf("%s: %d YAML documents, want exactly 1", f, docs)
		}
	}
}
