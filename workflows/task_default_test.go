package workflows_test

import (
	"os"
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/workflow"
)

func TestTaskDefaultWorkflow_Parses(t *testing.T) {
	content, err := os.ReadFile("task-default.yaml")
	if err != nil {
		t.Fatalf("failed to read workflow: %v", err)
	}

	wf, err := workflow.Parse("workflows/task-default.yaml", content)
	if err != nil {
		t.Fatalf("failed to parse workflow: %v", err)
	}

	if wf.ID != "task-default" {
		t.Errorf("expected id task-default, got %s", wf.ID)
	}
	if wf.Status != domain.WorkflowStatusActive {
		t.Errorf("expected status Active, got %s", wf.Status)
	}
	if len(wf.AppliesTo) != 1 || wf.AppliesTo[0] != "Task" {
		t.Errorf("expected applies_to [Task], got %v", wf.AppliesTo)
	}
	if wf.EntryStep != "draft" {
		t.Errorf("expected entry_step draft, got %s", wf.EntryStep)
	}
	if len(wf.Steps) != 4 {
		t.Fatalf("expected 4 steps, got %d", len(wf.Steps))
	}

	// Verify step IDs.
	expectedSteps := []string{"draft", "execute", "review", "commit"}
	for i, expected := range expectedSteps {
		if wf.Steps[i].ID != expected {
			t.Errorf("step %d: expected %s, got %s", i, expected, wf.Steps[i].ID)
		}
	}

	// Verify step types.
	if wf.Steps[0].Type != domain.StepTypeAutomated {
		t.Errorf("draft: expected automated, got %s", wf.Steps[0].Type)
	}
	if wf.Steps[1].Type != domain.StepTypeManual {
		t.Errorf("execute: expected manual, got %s", wf.Steps[1].Type)
	}
	if wf.Steps[2].Type != domain.StepTypeReview {
		t.Errorf("review step: expected review, got %s", wf.Steps[2].Type)
	}

	// Verify review outcomes.
	review := wf.Steps[2]
	if len(review.Outcomes) != 2 {
		t.Fatalf("review: expected 2 outcomes, got %d", len(review.Outcomes))
	}
	if review.Outcomes[0].ID != "accepted" {
		t.Errorf("expected first review outcome accepted, got %s", review.Outcomes[0].ID)
	}
	if review.Outcomes[0].NextStep != "commit" {
		t.Errorf("expected accepted → commit, got %s", review.Outcomes[0].NextStep)
	}
	if review.Outcomes[1].ID != "needs_rework" {
		t.Errorf("expected second review outcome needs_rework, got %s", review.Outcomes[1].ID)
	}
	if review.Outcomes[1].NextStep != "execute" {
		t.Errorf("expected needs_rework → execute, got %s", review.Outcomes[1].NextStep)
	}

	// Verify preconditions exist.
	if len(wf.Steps[0].Preconditions) != 1 {
		t.Errorf("draft: expected 1 precondition, got %d", len(wf.Steps[0].Preconditions))
	}

	// Verify required_outputs on execute step.
	if len(wf.Steps[1].RequiredOutputs) != 1 || wf.Steps[1].RequiredOutputs[0] != "deliverable" {
		t.Errorf("execute: expected required_outputs [deliverable], got %v", wf.Steps[1].RequiredOutputs)
	}

	// Verify commit step has commit effect.
	commit := wf.Steps[3]
	if len(commit.Outcomes) != 1 {
		t.Fatalf("commit: expected 1 outcome, got %d", len(commit.Outcomes))
	}
	if commit.Outcomes[0].NextStep != "end" {
		t.Errorf("expected committed → end, got %s", commit.Outcomes[0].NextStep)
	}
	if commit.Outcomes[0].Commit["status"] != "Completed" {
		t.Errorf("expected commit status=Completed, got %v", commit.Outcomes[0].Commit)
	}

	// Verify retry config on execute.
	if wf.Steps[1].Retry == nil {
		t.Fatal("execute: expected retry config")
	}
	if wf.Steps[1].Retry.Limit != 3 {
		t.Errorf("execute: expected retry limit 3, got %d", wf.Steps[1].Retry.Limit)
	}
}
