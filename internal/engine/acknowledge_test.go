package engine

import (
	"context"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/domain"
)

func TestAcknowledgeStep_Success(t *testing.T) {
	now := time.Now()
	st := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {RunID: "run-1", TaskPath: "tasks/t.md", TraceID: "trace-123456789"},
		},
	}
	st.createdSteps = []*domain.StepExecution{
		{ExecutionID: "exec-1", RunID: "run-1", StepID: "execute",
			Status: domain.StepStatusAssigned, ActorID: "bot-1", CreatedAt: now},
	}
	orch := &Orchestrator{store: st, events: &stubEventEmitter{}}

	result, err := orch.AcknowledgeStep(context.Background(), AcknowledgeRequest{
		ActorID:     "bot-1",
		ExecutionID: "exec-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != string(domain.StepStatusInProgress) {
		t.Errorf("expected in_progress, got %s", result.Status)
	}
	if result.StartedAt == nil {
		t.Error("expected started_at to be set")
	}

	exec, _ := st.GetStepExecution(context.Background(), "exec-1")
	if exec.Status != domain.StepStatusInProgress {
		t.Errorf("persisted status: expected in_progress, got %s", exec.Status)
	}
}

func TestAcknowledgeStep_WrongActor(t *testing.T) {
	now := time.Now()
	st := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {RunID: "run-1", TraceID: "trace-123456789"},
		},
	}
	st.createdSteps = []*domain.StepExecution{
		{ExecutionID: "exec-1", RunID: "run-1", StepID: "execute",
			Status: domain.StepStatusAssigned, ActorID: "bot-1", CreatedAt: now},
	}
	orch := &Orchestrator{store: st}

	_, err := orch.AcknowledgeStep(context.Background(), AcknowledgeRequest{
		ActorID:     "other-actor",
		ExecutionID: "exec-1",
	})
	if err == nil {
		t.Fatal("expected error for wrong actor")
	}
	se, ok := err.(*domain.SpineError)
	if !ok || se.Code != domain.ErrForbidden {
		t.Errorf("expected ErrForbidden, got %v", err)
	}
}

func TestAcknowledgeStep_NotAssigned(t *testing.T) {
	now := time.Now()
	st := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {RunID: "run-1", TraceID: "trace-123456789"},
		},
	}
	st.createdSteps = []*domain.StepExecution{
		{ExecutionID: "exec-1", RunID: "run-1", StepID: "execute",
			Status: domain.StepStatusInProgress, ActorID: "bot-1", CreatedAt: now},
	}
	orch := &Orchestrator{store: st}

	_, err := orch.AcknowledgeStep(context.Background(), AcknowledgeRequest{
		ActorID:     "bot-1",
		ExecutionID: "exec-1",
	})
	if err == nil {
		t.Fatal("expected error when step is not in assigned state")
	}
	se, ok := err.(*domain.SpineError)
	if !ok || se.Code != domain.ErrConflict {
		t.Errorf("expected ErrConflict, got %v", err)
	}
}

func TestAcknowledgeStep_MissingParams(t *testing.T) {
	orch := &Orchestrator{}

	if _, err := orch.AcknowledgeStep(context.Background(), AcknowledgeRequest{}); err == nil {
		t.Fatal("expected error for missing params")
	}
	if _, err := orch.AcknowledgeStep(context.Background(), AcknowledgeRequest{ActorID: "a"}); err == nil {
		t.Fatal("expected error for missing execution_id")
	}
}
