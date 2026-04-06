package engine

import (
	"context"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/domain"
)

func TestReleaseStep_Success(t *testing.T) {
	now := time.Now()
	st := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {RunID: "run-1", TraceID: "trace-123456789", Status: domain.RunStatusActive},
		},
	}
	st.createdSteps = []*domain.StepExecution{
		{ExecutionID: "exec-1", RunID: "run-1", StepID: "execute",
			Status: domain.StepStatusAssigned, ActorID: "actor-1", CreatedAt: now},
	}
	as := newMemAssignmentStore()
	as.assignments["assign-1"] = &domain.Assignment{
		AssignmentID: "assign-1", RunID: "run-1", ExecutionID: "exec-1",
		ActorID: "actor-1", Status: domain.AssignmentStatusActive, AssignedAt: now,
	}

	orch := &Orchestrator{store: st, assignments: as, events: &stubEventEmitter{}}

	err := orch.ReleaseStep(context.Background(), ReleaseRequest{
		ActorID:      "actor-1",
		AssignmentID: "assign-1",
		Reason:       "cannot complete",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify assignment was cancelled.
	a := as.assignments["assign-1"]
	if a.Status != domain.AssignmentStatusCancelled {
		t.Errorf("expected assignment cancelled, got %s", a.Status)
	}

	// Verify step went back to waiting.
	exec, _ := st.GetStepExecution(context.Background(), "exec-1")
	if exec.Status != domain.StepStatusWaiting {
		t.Errorf("expected step waiting, got %s", exec.Status)
	}
	if exec.ActorID != "" {
		t.Errorf("expected actor cleared, got %s", exec.ActorID)
	}
}

func TestReleaseStep_WrongActor(t *testing.T) {
	now := time.Now()
	as := newMemAssignmentStore()
	as.assignments["assign-1"] = &domain.Assignment{
		AssignmentID: "assign-1", RunID: "run-1", ExecutionID: "exec-1",
		ActorID: "actor-1", Status: domain.AssignmentStatusActive, AssignedAt: now,
	}

	orch := &Orchestrator{assignments: as}

	err := orch.ReleaseStep(context.Background(), ReleaseRequest{
		ActorID:      "actor-2",
		AssignmentID: "assign-1",
	})
	if err == nil {
		t.Fatal("expected error for wrong actor")
	}
}

func TestReleaseStep_TerminalStep(t *testing.T) {
	now := time.Now()
	st := &mockRunStore{}
	st.createdSteps = []*domain.StepExecution{
		{ExecutionID: "exec-1", RunID: "run-1", StepID: "execute",
			Status: domain.StepStatusCompleted, CreatedAt: now},
	}
	as := newMemAssignmentStore()
	as.assignments["assign-1"] = &domain.Assignment{
		AssignmentID: "assign-1", RunID: "run-1", ExecutionID: "exec-1",
		ActorID: "actor-1", Status: domain.AssignmentStatusActive, AssignedAt: now,
	}

	orch := &Orchestrator{store: st, assignments: as}

	err := orch.ReleaseStep(context.Background(), ReleaseRequest{
		ActorID:      "actor-1",
		AssignmentID: "assign-1",
	})
	if err == nil {
		t.Fatal("expected error for terminal step")
	}
}

func TestReleaseStep_MissingParams(t *testing.T) {
	orch := &Orchestrator{assignments: newMemAssignmentStore()}

	err := orch.ReleaseStep(context.Background(), ReleaseRequest{})
	if err == nil {
		t.Fatal("expected error for missing params")
	}
}
