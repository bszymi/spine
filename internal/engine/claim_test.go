package engine

import (
	"context"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/domain"
)

func TestClaimStep_Success(t *testing.T) {
	now := time.Now()
	st := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {
				RunID: "run-1", TaskPath: "tasks/task-1.md",
				WorkflowPath: "workflows/test.yaml", WorkflowVersion: "abc",
				Status: domain.RunStatusActive, TraceID: "trace-123456789",
			},
		},
	}
	// Seed the step via createdSteps (matches mockRunStore.GetStepExecution)
	st.createdSteps = []*domain.StepExecution{
		{
			ExecutionID: "exec-1", RunID: "run-1", StepID: "execute",
			Status: domain.StepStatusWaiting, CreatedAt: now,
		},
	}

	wfLoader := &claimTestWFLoader{
		wf: &domain.WorkflowDefinition{
			ID: "test", Steps: []domain.StepDefinition{
				{ID: "execute", Name: "Execute", Type: domain.StepTypeManual},
			},
		},
	}

	orch := &Orchestrator{store: st, wfLoader: wfLoader, events: &stubEventEmitter{}}

	result, err := orch.ClaimStep(context.Background(), ClaimRequest{
		ActorID:     "actor-1",
		ExecutionID: "exec-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.StepID != "execute" {
		t.Errorf("expected step_id execute, got %s", result.StepID)
	}
	if result.Assignment.ActorID != "actor-1" {
		t.Errorf("expected actor_id actor-1, got %s", result.Assignment.ActorID)
	}

	// Verify step was updated to assigned
	exec, _ := st.GetStepExecution(context.Background(), "exec-1")
	if exec.Status != domain.StepStatusAssigned {
		t.Errorf("expected step status assigned, got %s", exec.Status)
	}
	if exec.ActorID != "actor-1" {
		t.Errorf("expected actor_id on step, got %s", exec.ActorID)
	}
}

func TestClaimStep_AlreadyAssigned(t *testing.T) {
	now := time.Now()
	st := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {RunID: "run-1", Status: domain.RunStatusActive, TraceID: "trace-123456789"},
		},
	}
	st.createdSteps = []*domain.StepExecution{
		{
			ExecutionID: "exec-1", RunID: "run-1", StepID: "execute",
			Status: domain.StepStatusAssigned, ActorID: "other-actor", CreatedAt: now,
		},
	}

	orch := &Orchestrator{store: st}

	_, err := orch.ClaimStep(context.Background(), ClaimRequest{
		ActorID:     "actor-1",
		ExecutionID: "exec-1",
	})
	if err == nil {
		t.Fatal("expected error for already assigned step")
	}
}

func TestClaimStep_MissingParams(t *testing.T) {
	orch := &Orchestrator{}

	_, err := orch.ClaimStep(context.Background(), ClaimRequest{})
	if err == nil {
		t.Fatal("expected error for missing params")
	}

	_, err = orch.ClaimStep(context.Background(), ClaimRequest{ActorID: "a1"})
	if err == nil {
		t.Fatal("expected error for missing execution_id")
	}
}

// claimTestWFLoader returns a fixed workflow for any path/ref.
type claimTestWFLoader struct {
	wf *domain.WorkflowDefinition
}

func (s *claimTestWFLoader) LoadWorkflow(_ context.Context, _, _ string) (*domain.WorkflowDefinition, error) {
	return s.wf, nil
}
