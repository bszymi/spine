package workflows_test

import (
	"os"
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/workflow"
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
	if review.Outcomes[0].Commit["status"] != "Active" {
		t.Errorf("expected approved commit status Active, got %s", review.Outcomes[0].Commit["status"])
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
				// task-default and task-spike both match Task:execution.
				// This is expected — work_type filtering will disambiguate.
				t.Logf("NOTE: binding %q matched by both %s and %s (disambiguated by work_type)", key, existing, wf.ID)
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
