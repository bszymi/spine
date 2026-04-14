package engine

import (
	"context"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/actor"
	"github.com/bszymi/spine/internal/domain"
)

// mockActorSelector is a simple fake ActorSelector for tests.
type mockActorSelector struct {
	actor *domain.Actor
	err   error
	calls []actor.SelectionRequest
}

func (m *mockActorSelector) SelectActor(_ context.Context, req actor.SelectionRequest) (*domain.Actor, error) {
	m.calls = append(m.calls, req)
	return m.actor, m.err
}

func automatedOnlyWF(stepID string, eligibleTypes []string) *domain.WorkflowDefinition {
	return &domain.WorkflowDefinition{
		ID:        "wf-auto",
		EntryStep: stepID,
		Steps: []domain.StepDefinition{
			{
				ID:   stepID,
				Name: "Auto Step",
				Type: domain.StepTypeAutomated,
				Execution: &domain.ExecutionConfig{
					Mode:               domain.ExecModeAutomatedOnly,
					EligibleActorTypes: eligibleTypes,
				},
				Outcomes: []domain.OutcomeDefinition{{ID: "done"}},
			},
		},
	}
}

func automatedOnlyOrch(st *mockRunStore, sel *mockActorSelector, assn AssignmentStore) *Orchestrator {
	orch := stepTestOrchestrator(st, &mockEventEmitter{}, &mockWorkflowLoader{wfDef: automatedOnlyWF("run", []string{"automated_system"})}, nil, nil)
	orch.actorSelector = sel
	orch.assignments = assn
	return orch
}

// TestActivateStep_AutoAssign_ByType verifies that automated_only steps are
// assigned to an actor of the matching type when eligible_actor_ids is empty.
func TestActivateStep_AutoAssign_ByType(t *testing.T) {
	now := time.Now()
	st := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {RunID: "run-1", TaskPath: "tasks/t.md", WorkflowPath: "wf.yaml", WorkflowVersion: "v1", TraceID: "trace-123456789", Status: domain.RunStatusActive},
		},
	}
	st.createdSteps = []*domain.StepExecution{
		{ExecutionID: "exec-1", RunID: "run-1", StepID: "run", Status: domain.StepStatusWaiting, CreatedAt: now},
	}

	sel := &mockActorSelector{
		actor: &domain.Actor{ActorID: "bot-1", Type: domain.ActorTypeAutomated, Status: domain.ActorStatusActive},
	}
	as := newMemAssignmentStore()

	orch := automatedOnlyOrch(st, sel, as)

	if err := orch.ActivateStep(context.Background(), "run-1", "run"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The step execution should have actor_id set.
	exec, _ := st.GetStepExecution(context.Background(), "exec-1")
	if exec.ActorID != "bot-1" {
		t.Errorf("expected actor_id bot-1, got %q", exec.ActorID)
	}
	if exec.Status != domain.StepStatusAssigned {
		t.Errorf("expected assigned status, got %s", exec.Status)
	}

	// An assignment record should have been created.
	if len(as.assignments) == 0 {
		t.Error("expected assignment record to be created")
	}

	// The selector should have been called with the correct type filter.
	if len(sel.calls) == 0 {
		t.Fatal("expected SelectActor to be called")
	}
	if !containsStr(sel.calls[0].EligibleActorTypes, "automated_system") {
		t.Errorf("expected eligible type automated_system, got %v", sel.calls[0].EligibleActorTypes)
	}
}

// TestActivateStep_AutoAssign_ByEligibleActorIDs verifies that when
// eligible_actor_ids is set on the execution, the first listed actor is used.
func TestActivateStep_AutoAssign_ByEligibleActorIDs(t *testing.T) {
	now := time.Now()
	st := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {RunID: "run-1", TaskPath: "tasks/t.md", WorkflowPath: "wf.yaml", WorkflowVersion: "v1", TraceID: "trace-123456789", Status: domain.RunStatusActive},
		},
	}
	st.createdSteps = []*domain.StepExecution{
		{ExecutionID: "exec-1", RunID: "run-1", StepID: "run", Status: domain.StepStatusWaiting,
			EligibleActorIDs: []string{"runner-specific"}, CreatedAt: now},
	}

	sel := &mockActorSelector{
		actor: &domain.Actor{ActorID: "runner-specific", Type: domain.ActorTypeAutomated, Status: domain.ActorStatusActive},
	}
	as := newMemAssignmentStore()

	orch := automatedOnlyOrch(st, sel, as)

	if err := orch.ActivateStep(context.Background(), "run-1", "run"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	exec, _ := st.GetStepExecution(context.Background(), "exec-1")
	if exec.ActorID != "runner-specific" {
		t.Errorf("expected actor_id runner-specific, got %q", exec.ActorID)
	}

	// Selector should have been called with explicit strategy.
	if sel.calls[0].Strategy != actor.StrategyExplicit {
		t.Errorf("expected explicit strategy, got %s", sel.calls[0].Strategy)
	}
	if sel.calls[0].ExplicitActorID != "runner-specific" {
		t.Errorf("expected explicit actor runner-specific, got %s", sel.calls[0].ExplicitActorID)
	}
}

// TestActivateStep_AutoAssign_NoActorFound verifies graceful degradation when
// no eligible actor of the required type exists — step is still assigned with
// actor_id left empty so a runner can claim it manually later.
func TestActivateStep_AutoAssign_NoActorFound(t *testing.T) {
	now := time.Now()
	st := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {RunID: "run-1", TaskPath: "tasks/t.md", WorkflowPath: "wf.yaml", WorkflowVersion: "v1", TraceID: "trace-123456789", Status: domain.RunStatusActive},
		},
	}
	st.createdSteps = []*domain.StepExecution{
		{ExecutionID: "exec-1", RunID: "run-1", StepID: "run", Status: domain.StepStatusWaiting, CreatedAt: now},
	}

	sel := &mockActorSelector{
		err: domain.NewError(domain.ErrNotFound, "no eligible actor found"),
	}

	orch := automatedOnlyOrch(st, sel, nil)

	// ActivateStep should succeed — graceful degradation, not an error.
	if err := orch.ActivateStep(context.Background(), "run-1", "run"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	exec, _ := st.GetStepExecution(context.Background(), "exec-1")
	if exec.ActorID != "" {
		t.Errorf("expected empty actor_id on graceful degradation, got %q", exec.ActorID)
	}
	if exec.Status != domain.StepStatusAssigned {
		t.Errorf("expected step to still be assigned, got %s", exec.Status)
	}
}

// TestActivateStep_AutoAssign_HumanStep verifies that human-only steps are
// NOT auto-assigned (selector is not called).
func TestActivateStep_AutoAssign_HumanStep(t *testing.T) {
	now := time.Now()
	st := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {RunID: "run-1", TaskPath: "tasks/t.md", WorkflowPath: "wf.yaml", WorkflowVersion: "v1", TraceID: "trace-123456789", Status: domain.RunStatusActive},
		},
	}
	st.createdSteps = []*domain.StepExecution{
		{ExecutionID: "exec-1", RunID: "run-1", StepID: "review", Status: domain.StepStatusWaiting, CreatedAt: now},
	}

	humanWF := &domain.WorkflowDefinition{
		ID: "wf-human",
		Steps: []domain.StepDefinition{
			{ID: "review", Name: "Review", Type: domain.StepTypeReview,
				Execution: &domain.ExecutionConfig{Mode: domain.ExecModeHumanOnly},
				Outcomes:  []domain.OutcomeDefinition{{ID: "approved"}}},
		},
	}

	sel := &mockActorSelector{
		actor: &domain.Actor{ActorID: "bot-1", Type: domain.ActorTypeAutomated},
	}

	orch := stepTestOrchestrator(st, &mockEventEmitter{}, &mockWorkflowLoader{wfDef: humanWF}, nil, nil)
	orch.actorSelector = sel

	if err := orch.ActivateStep(context.Background(), "run-1", "review"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Selector must NOT have been called for human-only steps.
	if len(sel.calls) > 0 {
		t.Error("SelectActor should not be called for human-only steps")
	}

	exec, _ := st.GetStepExecution(context.Background(), "exec-1")
	if exec.ActorID != "" {
		t.Errorf("human step should not have actor_id auto-set, got %q", exec.ActorID)
	}
}

// TestActivateStep_AutoAssign_TypeInferredFromMode verifies that when
// eligible_actor_types is empty, the type is inferred from the execution mode.
func TestActivateStep_AutoAssign_TypeInferredFromMode(t *testing.T) {
	now := time.Now()
	st := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {RunID: "run-1", TaskPath: "tasks/t.md", WorkflowPath: "wf.yaml", WorkflowVersion: "v1", TraceID: "trace-123456789", Status: domain.RunStatusActive},
		},
	}
	st.createdSteps = []*domain.StepExecution{
		{ExecutionID: "exec-1", RunID: "run-1", StepID: "ai", Status: domain.StepStatusWaiting, CreatedAt: now},
	}

	aiWF := &domain.WorkflowDefinition{
		ID: "wf-ai",
		Steps: []domain.StepDefinition{
			{ID: "ai", Name: "AI Step", Type: domain.StepTypeAutomated,
				Execution: &domain.ExecutionConfig{Mode: domain.ExecModeAIOnly /* no EligibleActorTypes */},
				Outcomes:  []domain.OutcomeDefinition{{ID: "done"}}},
		},
	}

	sel := &mockActorSelector{
		actor: &domain.Actor{ActorID: "agent-1", Type: domain.ActorTypeAIAgent, Status: domain.ActorStatusActive},
	}

	orch := stepTestOrchestrator(st, &mockEventEmitter{}, &mockWorkflowLoader{wfDef: aiWF}, nil, nil)
	orch.actorSelector = sel

	if err := orch.ActivateStep(context.Background(), "run-1", "ai"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Type should be inferred as ai_agent from ExecModeAIOnly.
	if len(sel.calls) == 0 {
		t.Fatal("expected SelectActor to be called")
	}
	if !containsStr(sel.calls[0].EligibleActorTypes, "ai_agent") {
		t.Errorf("expected inferred type ai_agent, got %v", sel.calls[0].EligibleActorTypes)
	}

	exec, _ := st.GetStepExecution(context.Background(), "exec-1")
	if exec.ActorID != "agent-1" {
		t.Errorf("expected actor_id agent-1, got %q", exec.ActorID)
	}
}
