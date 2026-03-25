package engine

import (
	"context"
	"testing"

	"github.com/bszymi/spine/internal/domain"
)

func TestUnavailableActorsForStep_CollectsFailedActors(t *testing.T) {
	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {RunID: "run-1", Status: domain.RunStatusActive, TraceID: "trace-abc123456789"},
		},
		createdSteps: []*domain.StepExecution{
			{ExecutionID: "run-1-start-1", RunID: "run-1", StepID: "start", Status: domain.StepStatusFailed,
				ActorID: "actor-a", ErrorDetail: &domain.ErrorDetail{Classification: domain.FailureActorUnavailable}},
			{ExecutionID: "run-1-start-2", RunID: "run-1", StepID: "start", Status: domain.StepStatusFailed,
				ActorID: "actor-b", ErrorDetail: &domain.ErrorDetail{Classification: domain.FailureActorUnavailable}},
			{ExecutionID: "run-1-start-3", RunID: "run-1", StepID: "start", Status: domain.StepStatusWaiting,
				Attempt: 3},
		},
	}
	orch := &Orchestrator{store: store}

	excluded := orch.unavailableActorsForStep(context.Background(), "run-1", "start")
	if len(excluded) != 2 {
		t.Fatalf("expected 2 excluded actors, got %d", len(excluded))
	}
	if excluded[0] != "actor-a" || excluded[1] != "actor-b" {
		t.Errorf("expected [actor-a, actor-b], got %v", excluded)
	}
}

func TestUnavailableActorsForStep_IgnesOtherClassifications(t *testing.T) {
	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {RunID: "run-1", Status: domain.RunStatusActive, TraceID: "trace-abc123456789"},
		},
		createdSteps: []*domain.StepExecution{
			{ExecutionID: "run-1-start-1", RunID: "run-1", StepID: "start", Status: domain.StepStatusFailed,
				ActorID: "actor-a", ErrorDetail: &domain.ErrorDetail{Classification: domain.FailureTransient}},
		},
	}
	orch := &Orchestrator{store: store}

	excluded := orch.unavailableActorsForStep(context.Background(), "run-1", "start")
	if len(excluded) != 0 {
		t.Errorf("expected 0 excluded actors for transient failure, got %d", len(excluded))
	}
}

func TestUnavailableActorsForStep_EmptyWhenNoFailures(t *testing.T) {
	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {RunID: "run-1", Status: domain.RunStatusActive, TraceID: "trace-abc123456789"},
		},
		createdSteps: []*domain.StepExecution{
			{ExecutionID: "run-1-start-1", RunID: "run-1", StepID: "start", Status: domain.StepStatusWaiting},
		},
	}
	orch := &Orchestrator{store: store}

	excluded := orch.unavailableActorsForStep(context.Background(), "run-1", "start")
	if len(excluded) != 0 {
		t.Errorf("expected 0 excluded actors, got %d", len(excluded))
	}
}

func TestFailStep_Public(t *testing.T) {
	store, runID := testRunWithStep()
	events := &mockEventEmitter{}
	wf := testRetryWorkflow()
	wf.Steps[0].Retry = nil // no retry — should fail the run
	loader := &mockWorkflowLoader{wfDef: wf}

	orch := stepTestOrchestrator(store, events, loader, nil, nil)

	err := orch.FailStep(context.Background(), runID+"-start-1", domain.FailurePermanent, "actor crashed")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if store.createdSteps[0].Status != domain.StepStatusFailed {
		t.Errorf("expected failed, got %s", store.createdSteps[0].Status)
	}
	if store.createdSteps[0].ErrorDetail == nil {
		t.Fatal("expected error detail")
	}
	if store.createdSteps[0].ErrorDetail.Classification != domain.FailurePermanent {
		t.Errorf("expected permanent_error, got %s", store.createdSteps[0].ErrorDetail.Classification)
	}
}

func TestFailStep_SkipsTerminal(t *testing.T) {
	store, runID := testRunWithStep()
	events := &mockEventEmitter{}
	loader := &mockWorkflowLoader{wfDef: testRetryWorkflow()}

	orch := stepTestOrchestrator(store, events, loader, nil, nil)

	// Set step to already completed.
	store.createdSteps[0].Status = domain.StepStatusCompleted

	err := orch.FailStep(context.Background(), runID+"-start-1", domain.FailurePermanent, "actor crashed")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should remain completed — not overwritten.
	if store.createdSteps[0].Status != domain.StepStatusCompleted {
		t.Errorf("expected completed (unchanged), got %s", store.createdSteps[0].Status)
	}
}
