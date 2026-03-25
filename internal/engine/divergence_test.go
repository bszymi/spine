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

func (m *mockDivergenceHandler) CreateExploratoryBranch(_ context.Context, _ *domain.DivergenceContext, _, _ string) (*domain.Branch, error) {
	return nil, nil
}

func (m *mockDivergenceHandler) CloseWindow(_ context.Context, _ *domain.DivergenceContext) error {
	return nil
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

// ── Convergence mock ──

type mockConvergenceHandler struct {
	policyReady bool
	policyErr   error
	commitErr   error
	called      bool
}

func (m *mockConvergenceHandler) CheckEntryPolicy(_ context.Context, _ *domain.DivergenceContext, _ domain.ConvergenceDefinition) (bool, error) {
	return m.policyReady, m.policyErr
}

func (m *mockConvergenceHandler) EvaluateAndCommit(_ context.Context, _ *domain.DivergenceContext, _ domain.ConvergenceDefinition) error {
	m.called = true
	return m.commitErr
}

func TestCompleteBranchStep_TriggersConvergence(t *testing.T) {
	store, runID := testRunWithStep()
	events := &mockEventEmitter{}
	actors := &mockActorAssigner{}

	wf := &domain.WorkflowDefinition{
		ID:        "wf-test",
		EntryStep: "start",
		Steps: []domain.StepDefinition{
			{ID: "start", Name: "Start", Outcomes: []domain.OutcomeDefinition{{ID: "done", NextStep: "end"}}},
			{ID: "review-a", Name: "Review A", Outcomes: []domain.OutcomeDefinition{{ID: "approve", NextStep: "end"}}},
			{ID: "merge-step", Name: "Merge", Converge: "conv-1", Outcomes: []domain.OutcomeDefinition{{ID: "done", NextStep: "end"}}},
		},
		ConvergencePoints: []domain.ConvergenceDefinition{
			{ID: "conv-1", Strategy: domain.ConvergenceSelectOne, EntryPolicy: domain.EntryPolicyAllTerminal},
		},
	}
	loader := &mockWorkflowLoader{wfDef: wf}

	divCtx := &domain.DivergenceContext{
		DivergenceID:  "div-1",
		RunID:         runID,
		Status:        domain.DivergenceStatusActive,
		ConvergenceID: "conv-1",
	}

	branch := &domain.Branch{
		BranchID: "branch-a", RunID: runID, DivergenceID: "div-1",
		Status: domain.BranchStatusInProgress, CurrentStepID: "review-a",
	}
	store.branches = []*domain.Branch{branch}
	store.divergenceContexts = map[string]*domain.DivergenceContext{"div-1": divCtx}

	convHandler := &mockConvergenceHandler{policyReady: true}

	orch := stepTestOrchestrator(store, events, loader, nil, actors)
	orch.WithConvergence(convHandler)

	// Create branch step.
	branchExec := &domain.StepExecution{
		ExecutionID: runID + "-branch-a-review-a-1",
		RunID:       runID,
		StepID:      "review-a",
		BranchID:    "branch-a",
		Status:      domain.StepStatusInProgress,
		Attempt:     1,
	}
	store.createdSteps = append(store.createdSteps, branchExec)

	err := orch.SubmitStepResult(context.Background(), branchExec.ExecutionID, StepResult{OutcomeID: "approve"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Convergence should have been called.
	if !convHandler.called {
		t.Error("expected convergence EvaluateAndCommit to be called")
	}

	// Post-convergence step should be created.
	found := false
	for _, s := range store.createdSteps {
		if s.StepID == "merge-step" {
			found = true
		}
	}
	if !found {
		t.Error("expected post-convergence step merge-step to be created")
	}
}

func TestCompleteBranchStep_SkipsConvergenceWhenPolicyNotReady(t *testing.T) {
	store, runID := testRunWithStep()
	events := &mockEventEmitter{}

	wf := &domain.WorkflowDefinition{
		ID:        "wf-test",
		EntryStep: "start",
		Steps: []domain.StepDefinition{
			{ID: "start", Name: "Start", Outcomes: []domain.OutcomeDefinition{{ID: "done", NextStep: "end"}}},
			{ID: "review-a", Name: "Review A", Outcomes: []domain.OutcomeDefinition{{ID: "approve", NextStep: "end"}}},
		},
		ConvergencePoints: []domain.ConvergenceDefinition{
			{ID: "conv-1", Strategy: domain.ConvergenceRequireAll, EntryPolicy: domain.EntryPolicyAllTerminal},
		},
	}
	loader := &mockWorkflowLoader{wfDef: wf}

	divCtx := &domain.DivergenceContext{
		DivergenceID: "div-1", RunID: runID, Status: domain.DivergenceStatusActive, ConvergenceID: "conv-1",
	}
	branch := &domain.Branch{
		BranchID: "branch-a", RunID: runID, DivergenceID: "div-1",
		Status: domain.BranchStatusInProgress, CurrentStepID: "review-a",
	}
	store.branches = []*domain.Branch{branch}
	store.divergenceContexts = map[string]*domain.DivergenceContext{"div-1": divCtx}

	convHandler := &mockConvergenceHandler{policyReady: false} // Not ready yet.

	orch := stepTestOrchestrator(store, events, loader, nil, nil)
	orch.WithConvergence(convHandler)

	branchExec := &domain.StepExecution{
		ExecutionID: runID + "-branch-a-review-a-1",
		RunID:       runID, StepID: "review-a", BranchID: "branch-a",
		Status: domain.StepStatusInProgress, Attempt: 1,
	}
	store.createdSteps = append(store.createdSteps, branchExec)

	err := orch.SubmitStepResult(context.Background(), branchExec.ExecutionID, StepResult{OutcomeID: "approve"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Convergence should NOT have been called.
	if convHandler.called {
		t.Error("expected convergence to NOT be called when policy not ready")
	}
}

func TestCompleteBranchStep_AdvancesWithinBranch(t *testing.T) {
	store, runID := testRunWithStep()
	events := &mockEventEmitter{}
	actors := &mockActorAssigner{}

	wf := &domain.WorkflowDefinition{
		ID:        "wf-test",
		EntryStep: "start",
		Steps: []domain.StepDefinition{
			{ID: "start", Name: "Start", Outcomes: []domain.OutcomeDefinition{{ID: "done", NextStep: "end"}}},
			{ID: "review-a", Name: "Review A", Outcomes: []domain.OutcomeDefinition{{ID: "approve", NextStep: "finalize-a"}}},
			{ID: "finalize-a", Name: "Finalize A", Outcomes: []domain.OutcomeDefinition{{ID: "done", NextStep: "end"}}},
		},
	}
	loader := &mockWorkflowLoader{wfDef: wf}

	branch := &domain.Branch{
		BranchID: "branch-a", RunID: runID, DivergenceID: "div-1",
		Status: domain.BranchStatusInProgress, CurrentStepID: "review-a",
	}
	store.branches = []*domain.Branch{branch}

	orch := stepTestOrchestrator(store, events, loader, nil, actors)

	branchExec := &domain.StepExecution{
		ExecutionID: runID + "-branch-a-review-a-1",
		RunID:       runID,
		StepID:      "review-a",
		BranchID:    "branch-a",
		Status:      domain.StepStatusInProgress,
		Attempt:     1,
	}
	store.createdSteps = append(store.createdSteps, branchExec)

	err := orch.SubmitStepResult(context.Background(), branchExec.ExecutionID, StepResult{OutcomeID: "approve"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Branch should advance to finalize-a, not complete.
	if store.branches[0].CurrentStepID != "finalize-a" {
		t.Errorf("expected branch current step finalize-a, got %s", store.branches[0].CurrentStepID)
	}
	if store.branches[0].Status != domain.BranchStatusInProgress {
		t.Errorf("expected branch still in progress, got %s", store.branches[0].Status)
	}

	// New branch step execution should have BranchID set.
	found := false
	for _, s := range store.createdSteps {
		if s.StepID == "finalize-a" && s.BranchID == "branch-a" {
			found = true
		}
	}
	if !found {
		t.Error("expected finalize-a step execution with branch-a BranchID")
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
