package engine

import (
	"context"
	"testing"

	"github.com/bszymi/spine/internal/domain"
)

// ── Divergence mock ──

type mockDivergenceHandler struct {
	divCtx *domain.DivergenceContext
	err    error
}

func (m *mockDivergenceHandler) StartDivergence(_ context.Context, _ *domain.Run, _ domain.DivergenceDefinition, _ string) (*domain.DivergenceContext, error) {
	return m.divCtx, m.err
}

func TestSubmitStepResult_TriggersDivergence(t *testing.T) {
	store, runID := testRunWithStep()
	events := &mockEventEmitter{}
	actors := &mockActorAssigner{}

	wf := &domain.WorkflowDefinition{
		ID:        "wf-test",
		EntryStep: "start",
		Steps: []domain.StepDefinition{
			{
				ID:      "start",
				Name:    "Start",
				Type:    "automated",
				Diverge: "div-1",
				Outcomes: []domain.OutcomeDefinition{
					{ID: "done", NextStep: "end"},
				},
			},
		},
		DivergencePoints: []domain.DivergenceDefinition{
			{
				ID:   "div-1",
				Mode: domain.DivergenceModeStructured,
				Branches: []domain.BranchDefinition{
					{ID: "branch-a", Name: "Branch A", StartStep: "review-a"},
					{ID: "branch-b", Name: "Branch B", StartStep: "review-b"},
				},
			},
		},
	}
	loader := &mockWorkflowLoader{wfDef: wf}

	divCtx := &domain.DivergenceContext{
		DivergenceID: runID + "-div-div-1",
		RunID:        runID,
		Status:       domain.DivergenceStatusActive,
	}
	divHandler := &mockDivergenceHandler{divCtx: divCtx}

	orch := stepTestOrchestrator(store, events, loader, nil, actors)
	orch.WithDivergence(divHandler)

	// Add branches to store so startDivergence can list them.
	store.branches = []*domain.Branch{
		{BranchID: "branch-a", RunID: runID, DivergenceID: divCtx.DivergenceID, Status: domain.BranchStatusInProgress, CurrentStepID: "review-a"},
		{BranchID: "branch-b", RunID: runID, DivergenceID: divCtx.DivergenceID, Status: domain.BranchStatusInProgress, CurrentStepID: "review-b"},
	}

	// Transition step to assigned, then in_progress, then submit.
	store.createdSteps[0].Status = domain.StepStatusInProgress

	err := orch.SubmitStepResult(context.Background(), store.createdSteps[0].ExecutionID, StepResult{
		OutcomeID: "done",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have created branch step executions.
	branchSteps := 0
	for _, s := range store.createdSteps {
		if s.BranchID != "" {
			branchSteps++
		}
	}
	if branchSteps < 2 {
		t.Errorf("expected at least 2 branch step executions, got %d", branchSteps)
	}
}

func TestSubmitStepResult_NoDivergenceWithoutHandler(t *testing.T) {
	store, _ := testRunWithStep()
	events := &mockEventEmitter{}
	actors := &mockActorAssigner{}

	wf := &domain.WorkflowDefinition{
		ID:        "wf-test",
		EntryStep: "start",
		Steps: []domain.StepDefinition{
			{
				ID:      "start",
				Name:    "Start",
				Type:    "automated",
				Diverge: "div-1",
				Outcomes: []domain.OutcomeDefinition{
					{ID: "done", NextStep: "review"},
				},
			},
			{ID: "review", Name: "Review", Outcomes: []domain.OutcomeDefinition{{ID: "approve", NextStep: "end"}}},
		},
	}
	loader := &mockWorkflowLoader{wfDef: wf}

	// No divergence handler — should fall through to normal routing.
	orch := stepTestOrchestrator(store, events, loader, nil, actors)

	store.createdSteps[0].Status = domain.StepStatusInProgress

	err := orch.SubmitStepResult(context.Background(), store.createdSteps[0].ExecutionID, StepResult{
		OutcomeID: "done",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have progressed to next step normally (no divergence).
	found := false
	for _, s := range store.createdSteps {
		if s.StepID == "review" {
			found = true
		}
	}
	if !found {
		t.Error("expected normal progression to review step")
	}
}

func TestCompleteBranchStep_MarksBranchCompleted(t *testing.T) {
	store, runID := testRunWithStep()
	events := &mockEventEmitter{}

	wf := &domain.WorkflowDefinition{
		ID:        "wf-test",
		EntryStep: "start",
		Steps: []domain.StepDefinition{
			{ID: "start", Name: "Start", Outcomes: []domain.OutcomeDefinition{{ID: "done", NextStep: "end"}}},
			{ID: "review-a", Name: "Review A", Outcomes: []domain.OutcomeDefinition{{ID: "approve", NextStep: "end"}}},
		},
	}
	loader := &mockWorkflowLoader{wfDef: wf}

	branch := &domain.Branch{
		BranchID: "branch-a", RunID: runID, DivergenceID: "div-1",
		Status: domain.BranchStatusInProgress, CurrentStepID: "review-a",
	}
	store.branches = []*domain.Branch{branch}

	orch := stepTestOrchestrator(store, events, loader, nil, nil)

	// Create a branch step execution.
	branchExec := &domain.StepExecution{
		ExecutionID: runID + "-branch-a-review-a-1",
		RunID:       runID,
		StepID:      "review-a",
		BranchID:    "branch-a",
		Status:      domain.StepStatusInProgress,
		Attempt:     1,
	}
	store.createdSteps = append(store.createdSteps, branchExec)

	err := orch.SubmitStepResult(context.Background(), branchExec.ExecutionID, StepResult{
		OutcomeID: "approve",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Branch should be marked as completed.
	if store.branches[0].Status != domain.BranchStatusCompleted {
		t.Errorf("expected branch completed, got %s", store.branches[0].Status)
	}
	if store.branches[0].CompletedAt == nil {
		t.Error("expected branch completed_at to be set")
	}
}
