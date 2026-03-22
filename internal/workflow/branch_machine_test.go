package workflow_test

import (
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/workflow"
)

func TestBranchPendingToInProgress(t *testing.T) {
	r, err := workflow.EvaluateBranchTransition(domain.BranchStatusPending, workflow.BranchTransitionRequest{
		Trigger: workflow.BranchTriggerStart,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.ToStatus != domain.BranchStatusInProgress {
		t.Errorf("expected in_progress, got %s", r.ToStatus)
	}
}

func TestBranchInProgressStaysWithNextStep(t *testing.T) {
	r, err := workflow.EvaluateBranchTransition(domain.BranchStatusInProgress, workflow.BranchTransitionRequest{
		Trigger:     workflow.BranchTriggerStepDone,
		HasNextStep: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.ToStatus != domain.BranchStatusInProgress {
		t.Errorf("expected in_progress, got %s", r.ToStatus)
	}
}

func TestBranchInProgressToCompleted(t *testing.T) {
	r, err := workflow.EvaluateBranchTransition(domain.BranchStatusInProgress, workflow.BranchTransitionRequest{
		Trigger:     workflow.BranchTriggerStepDone,
		HasNextStep: false,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.ToStatus != domain.BranchStatusCompleted {
		t.Errorf("expected completed, got %s", r.ToStatus)
	}
}

func TestBranchInProgressToFailed(t *testing.T) {
	r, err := workflow.EvaluateBranchTransition(domain.BranchStatusInProgress, workflow.BranchTransitionRequest{
		Trigger: workflow.BranchTriggerStepFailed,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.ToStatus != domain.BranchStatusFailed {
		t.Errorf("expected failed, got %s", r.ToStatus)
	}
}

func TestBranchTerminalStatesImmutable(t *testing.T) {
	terminals := []domain.BranchStatus{domain.BranchStatusCompleted, domain.BranchStatusFailed}
	for _, state := range terminals {
		_, err := workflow.EvaluateBranchTransition(state, workflow.BranchTransitionRequest{
			Trigger: workflow.BranchTriggerStart,
		})
		if err == nil {
			t.Errorf("expected error for terminal state %s", state)
		}
	}
}

func TestBranchPendingRejectsInvalid(t *testing.T) {
	_, err := workflow.EvaluateBranchTransition(domain.BranchStatusPending, workflow.BranchTransitionRequest{
		Trigger: workflow.BranchTriggerStepDone,
	})
	if err == nil {
		t.Error("expected error for invalid trigger on pending")
	}
}

func TestBranchInProgressRejectsInvalid(t *testing.T) {
	_, err := workflow.EvaluateBranchTransition(domain.BranchStatusInProgress, workflow.BranchTransitionRequest{
		Trigger: workflow.BranchTriggerStart,
	})
	if err == nil {
		t.Error("expected error for invalid trigger on in_progress")
	}
}
