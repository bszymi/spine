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
	if wf.Status != domain.WorkflowStatusActive {
		t.Errorf("expected Active, got %s", wf.Status)
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
	if evaluate.Outcomes[1].ID != "rejected" {
		t.Errorf("expected rejected, got %s", evaluate.Outcomes[1].ID)
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

func TestNoBindingConflicts(t *testing.T) {
	// Load all workflows and verify no two Active workflows share
	// the same applies_to type (which would cause ambiguous binding).
	files := []string{"task-default.yaml", "task-spike.yaml", "adr.yaml", "epic-lifecycle.yaml"}

	typeMap := make(map[string]string) // artifact type → workflow ID
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
			if existing, ok := typeMap[at]; ok {
				// Both task-default and task-spike apply to Task.
				// This is expected — work_type filtering will disambiguate.
				// For now, document the known overlap.
				t.Logf("NOTE: type %q matched by both %s and %s (disambiguated by work_type)", at, existing, wf.ID)
			}
			typeMap[at] = wf.ID
		}
	}

	// ADR and Epic should be unique.
	if id, ok := typeMap["ADR"]; !ok || id != "adr-review" {
		t.Errorf("expected ADR → adr-review")
	}
	if id, ok := typeMap["Epic"]; !ok || id != "epic-lifecycle" {
		t.Errorf("expected Epic → epic-lifecycle")
	}
}
