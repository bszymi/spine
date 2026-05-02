package engine

import (
	"context"
	"testing"

	"github.com/bszymi/spine/internal/domain"
)

// TestAssignStep_FromWaiting_BindsActor exercises the happy-path
// transition: a fresh step in `waiting` is assigned to a concrete actor
// and lands in `assigned` status.
func TestAssignStep_FromWaiting_BindsActor(t *testing.T) {
	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {RunID: "run-1", WorkflowPath: "wf.yaml", WorkflowVersion: "v1", Status: domain.RunStatusActive, TraceID: "trace-1234567890ab"},
		},
	}
	store.createdSteps = []*domain.StepExecution{
		{ExecutionID: "exec-1", RunID: "run-1", StepID: "execute", Status: domain.StepStatusWaiting, Attempt: 1},
	}

	orch := &Orchestrator{store: store, events: &mockEventEmitter{}}

	result, err := orch.AssignStep(context.Background(), AssignRequest{
		RunID:   "run-1",
		StepID:  "execute",
		ActorID: "actor-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Exec.Status != domain.StepStatusAssigned {
		t.Errorf("expected status assigned, got %s", result.Exec.Status)
	}
	if result.Exec.ActorID != "actor-1" {
		t.Errorf("expected actor-1, got %q", result.Exec.ActorID)
	}
}

// TestAssignStep_RecoversPhantomAssignedRow locks in the recovery path
// for in-flight rows persisted by pre-Option-B engines: a step with
// `status=assigned` and `actor_id=""` is treated as an open slot, so
// /assign can bind an operator and unblock the run rather than hitting
// the state machine's "invalid trigger step.assign for assigned step"
// error. Without this, TASK-004's recovery path remains broken for
// existing runs (INIT-020/EPIC-001/TASK-004 codex pass 3).
func TestAssignStep_RecoversPhantomAssignedRow(t *testing.T) {
	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {RunID: "run-1", WorkflowPath: "wf.yaml", WorkflowVersion: "v1", Status: domain.RunStatusActive, TraceID: "trace-1234567890ab"},
		},
	}
	// Phantom: assigned but no actor bound.
	store.createdSteps = []*domain.StepExecution{
		{ExecutionID: "exec-1", RunID: "run-1", StepID: "execute", Status: domain.StepStatusAssigned, ActorID: "", Attempt: 1},
	}

	orch := &Orchestrator{store: store, events: &mockEventEmitter{}}

	result, err := orch.AssignStep(context.Background(), AssignRequest{
		RunID:   "run-1",
		StepID:  "execute",
		ActorID: "operator-1",
	})
	if err != nil {
		t.Fatalf("expected recovery to succeed, got error: %v", err)
	}
	if result.Exec.Status != domain.StepStatusAssigned {
		t.Errorf("expected status to remain assigned, got %s", result.Exec.Status)
	}
	if result.Exec.ActorID != "operator-1" {
		t.Errorf("expected actor bound to operator-1, got %q", result.Exec.ActorID)
	}
}

// TestAssignStep_RecoversPhantomInProgressRow extends the recovery
// guarantee to in_progress rows: if a pre-fix phantom step was auto-
// acknowledged by SubmitStepResult and the process crashed before the
// terminal write, it can be persisted as `in_progress` with no actor.
// /assign must bind an actor without trying to drive the state machine
// (which rejects step.assign from in_progress) so the operator can
// complete the step instead of editing the DB directly.
func TestAssignStep_RecoversPhantomInProgressRow(t *testing.T) {
	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {RunID: "run-1", WorkflowPath: "wf.yaml", WorkflowVersion: "v1", Status: domain.RunStatusActive, TraceID: "trace-1234567890ab"},
		},
	}
	store.createdSteps = []*domain.StepExecution{
		{ExecutionID: "exec-1", RunID: "run-1", StepID: "execute", Status: domain.StepStatusInProgress, ActorID: "", Attempt: 1},
	}

	orch := &Orchestrator{store: store, events: &mockEventEmitter{}}

	result, err := orch.AssignStep(context.Background(), AssignRequest{
		RunID:   "run-1",
		StepID:  "execute",
		ActorID: "operator-1",
	})
	if err != nil {
		t.Fatalf("expected in-progress phantom recovery to succeed, got %v", err)
	}
	if result.Exec.Status != domain.StepStatusInProgress {
		t.Errorf("expected status to remain in_progress, got %s", result.Exec.Status)
	}
	if result.Exec.ActorID != "operator-1" {
		t.Errorf("expected actor bound to operator-1, got %q", result.Exec.ActorID)
	}
}

// TestAssignStep_RejectsAssignedWithExistingActor confirms the open-slot
// recovery is narrow: a step that is `assigned` to a concrete actor
// cannot be reassigned via /assign — the operator must release first.
// Without this guard the recovery shortcut would silently overwrite a
// live assignment.
func TestAssignStep_RejectsAssignedWithExistingActor(t *testing.T) {
	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {RunID: "run-1", WorkflowPath: "wf.yaml", WorkflowVersion: "v1", Status: domain.RunStatusActive, TraceID: "trace-1234567890ab"},
		},
	}
	store.createdSteps = []*domain.StepExecution{
		{ExecutionID: "exec-1", RunID: "run-1", StepID: "execute", Status: domain.StepStatusAssigned, ActorID: "actor-1", Attempt: 1},
	}

	orch := &Orchestrator{store: store, events: &mockEventEmitter{}}

	_, err := orch.AssignStep(context.Background(), AssignRequest{
		RunID:   "run-1",
		StepID:  "execute",
		ActorID: "actor-2",
	})
	if err == nil {
		t.Fatal("expected error when reassigning a step already bound to a different actor")
	}
}
