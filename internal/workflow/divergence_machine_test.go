package workflow_test

import (
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/workflow"
)

// ── DivergenceContext Valid Transitions ──

func TestDivergencePendingToActive(t *testing.T) {
	r, err := workflow.EvaluateDivergenceTransition(domain.DivergenceStatusPending, workflow.DivergenceTransitionRequest{
		Trigger: workflow.DivergenceTriggerStart,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.ToStatus != domain.DivergenceStatusActive {
		t.Errorf("expected active, got %s", r.ToStatus)
	}
}

func TestDivergenceActiveToConverging(t *testing.T) {
	r, err := workflow.EvaluateDivergenceTransition(domain.DivergenceStatusActive, workflow.DivergenceTransitionRequest{
		Trigger:          workflow.DivergenceTriggerBranchDone,
		EntryPolicy:      domain.EntryPolicyAllTerminal,
		BranchesTotal:    2,
		BranchesTerminal: 2,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.ToStatus != domain.DivergenceStatusConverging {
		t.Errorf("expected converging, got %s", r.ToStatus)
	}
}

func TestDivergenceActiveStaysActive(t *testing.T) {
	r, err := workflow.EvaluateDivergenceTransition(domain.DivergenceStatusActive, workflow.DivergenceTransitionRequest{
		Trigger:          workflow.DivergenceTriggerBranchDone,
		EntryPolicy:      domain.EntryPolicyAllTerminal,
		BranchesTotal:    3,
		BranchesTerminal: 1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.ToStatus != domain.DivergenceStatusActive {
		t.Errorf("expected active, got %s", r.ToStatus)
	}
}

func TestDivergenceActiveToFailedRequireAll(t *testing.T) {
	r, err := workflow.EvaluateDivergenceTransition(domain.DivergenceStatusActive, workflow.DivergenceTransitionRequest{
		Trigger:      workflow.DivergenceTriggerBranchDone,
		Strategy:     domain.ConvergenceRequireAll,
		BranchFailed: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.ToStatus != domain.DivergenceStatusFailed {
		t.Errorf("expected failed, got %s", r.ToStatus)
	}
}

func TestDivergenceConvergingToResolved(t *testing.T) {
	r, err := workflow.EvaluateDivergenceTransition(domain.DivergenceStatusConverging, workflow.DivergenceTransitionRequest{
		Trigger: workflow.DivergenceTriggerEvalDone,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.ToStatus != domain.DivergenceStatusResolved {
		t.Errorf("expected resolved, got %s", r.ToStatus)
	}
}

func TestDivergenceConvergingToFailed(t *testing.T) {
	r, err := workflow.EvaluateDivergenceTransition(domain.DivergenceStatusConverging, workflow.DivergenceTransitionRequest{
		Trigger: workflow.DivergenceTriggerEvalFailed,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.ToStatus != domain.DivergenceStatusFailed {
		t.Errorf("expected failed, got %s", r.ToStatus)
	}
}

// ── Exploratory Divergence ──

func TestDivergenceCreateBranchWindowOpen(t *testing.T) {
	r, err := workflow.EvaluateDivergenceTransition(domain.DivergenceStatusActive, workflow.DivergenceTransitionRequest{
		Trigger:    workflow.DivergenceTriggerCreateBranch,
		WindowOpen: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.ToStatus != domain.DivergenceStatusActive {
		t.Errorf("expected active, got %s", r.ToStatus)
	}
}

func TestDivergenceCreateBranchWindowClosed(t *testing.T) {
	_, err := workflow.EvaluateDivergenceTransition(domain.DivergenceStatusActive, workflow.DivergenceTransitionRequest{
		Trigger:    workflow.DivergenceTriggerCreateBranch,
		WindowOpen: false,
	})
	if err == nil {
		t.Error("expected error for closed window")
	}
}

func TestDivergenceCreateBranchMaxReached(t *testing.T) {
	_, err := workflow.EvaluateDivergenceTransition(domain.DivergenceStatusActive, workflow.DivergenceTransitionRequest{
		Trigger:     workflow.DivergenceTriggerCreateBranch,
		WindowOpen:  true,
		BranchCount: 5,
		MaxBranches: 5,
	})
	if err == nil {
		t.Error("expected error for max branches reached")
	}
}

func TestDivergenceCloseWindow(t *testing.T) {
	r, err := workflow.EvaluateDivergenceTransition(domain.DivergenceStatusActive, workflow.DivergenceTransitionRequest{
		Trigger:    workflow.DivergenceTriggerCloseWindow,
		WindowOpen: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.ToStatus != domain.DivergenceStatusActive {
		t.Errorf("expected active, got %s", r.ToStatus)
	}
}

func TestDivergenceCloseWindowAlreadyClosed(t *testing.T) {
	_, err := workflow.EvaluateDivergenceTransition(domain.DivergenceStatusActive, workflow.DivergenceTransitionRequest{
		Trigger:    workflow.DivergenceTriggerCloseWindow,
		WindowOpen: false,
	})
	if err == nil {
		t.Error("expected error for already closed window")
	}
}

// ── Invalid Transitions ──

func TestDivergenceTerminalStatesImmutable(t *testing.T) {
	terminals := []domain.DivergenceStatus{domain.DivergenceStatusResolved, domain.DivergenceStatusFailed}
	for _, state := range terminals {
		_, err := workflow.EvaluateDivergenceTransition(state, workflow.DivergenceTransitionRequest{
			Trigger: workflow.DivergenceTriggerStart,
		})
		if err == nil {
			t.Errorf("expected error for terminal state %s", state)
		}
	}
}

func TestDivergencePendingRejectsInvalid(t *testing.T) {
	_, err := workflow.EvaluateDivergenceTransition(domain.DivergenceStatusPending, workflow.DivergenceTransitionRequest{
		Trigger: workflow.DivergenceTriggerBranchDone,
	})
	if err == nil {
		t.Error("expected error for invalid trigger on pending")
	}
}

func TestDivergenceConvergingRejectsInvalid(t *testing.T) {
	_, err := workflow.EvaluateDivergenceTransition(domain.DivergenceStatusConverging, workflow.DivergenceTransitionRequest{
		Trigger: workflow.DivergenceTriggerStart,
	})
	if err == nil {
		t.Error("expected error for invalid trigger on converging")
	}
}

// ── Entry Policy Tests ──

func TestEntryPolicyMinCompleted(t *testing.T) {
	r, err := workflow.EvaluateDivergenceTransition(domain.DivergenceStatusActive, workflow.DivergenceTransitionRequest{
		Trigger:          workflow.DivergenceTriggerBranchDone,
		EntryPolicy:      domain.EntryPolicyMinCompleted,
		BranchesTerminal: 2,
		MinBranches:      2,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.ToStatus != domain.DivergenceStatusConverging {
		t.Errorf("expected converging, got %s", r.ToStatus)
	}
}

func TestEntryPolicyMinCompletedNotMet(t *testing.T) {
	r, err := workflow.EvaluateDivergenceTransition(domain.DivergenceStatusActive, workflow.DivergenceTransitionRequest{
		Trigger:          workflow.DivergenceTriggerBranchDone,
		EntryPolicy:      domain.EntryPolicyMinCompleted,
		BranchesTerminal: 1,
		MinBranches:      2,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.ToStatus != domain.DivergenceStatusActive {
		t.Errorf("expected active (policy not met), got %s", r.ToStatus)
	}
}
