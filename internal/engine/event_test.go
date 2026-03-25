package engine

import (
	"context"
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/workflow"
)

func TestPauseRun_EmitsEvent(t *testing.T) {
	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {RunID: "run-1", Status: domain.RunStatusActive, TraceID: "trace-abc123456789"},
		},
	}
	events := &mockEventEmitter{}
	orch := testOrchestrator(&mockArtifactReader{}, &mockWorkflowResolver{}, store, events)

	err := orch.PauseRun(context.Background(), "run-1", "step blocked")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if store.runs["run-1"].Status != domain.RunStatusPaused {
		t.Errorf("expected paused, got %s", store.runs["run-1"].Status)
	}

	found := false
	for _, e := range events.events {
		if e.Type == domain.EventRunPaused {
			found = true
		}
	}
	if !found {
		t.Error("expected run_paused event")
	}
}

func TestResumeRun_EmitsEvent(t *testing.T) {
	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {RunID: "run-1", Status: domain.RunStatusPaused, TraceID: "trace-abc123456789"},
		},
	}
	events := &mockEventEmitter{}
	orch := testOrchestrator(&mockArtifactReader{}, &mockWorkflowResolver{}, store, events)

	err := orch.ResumeRun(context.Background(), "run-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if store.runs["run-1"].Status != domain.RunStatusActive {
		t.Errorf("expected active, got %s", store.runs["run-1"].Status)
	}

	found := false
	for _, e := range events.events {
		if e.Type == domain.EventRunResumed {
			found = true
		}
	}
	if !found {
		t.Error("expected run_resumed event")
	}
}

func TestSubmitStepResult_EmitsStepStarted(t *testing.T) {
	store, _ := testRunWithStep()
	events := &mockEventEmitter{}
	actors := &mockActorAssigner{}

	wf := &domain.WorkflowDefinition{
		ID:        "wf-test",
		EntryStep: "start",
		Steps: []domain.StepDefinition{
			{ID: "start", Name: "Start", Type: "automated",
				Outcomes: []domain.OutcomeDefinition{{ID: "done", NextStep: "end"}}},
		},
	}
	loader := &mockWorkflowLoader{wfDef: wf}
	orch := stepTestOrchestrator(store, events, loader, nil, actors)

	// Step is assigned — SubmitStepResult auto-acknowledges to in_progress, emitting step_started.
	store.createdSteps[0].Status = domain.StepStatusAssigned

	err := orch.SubmitStepResult(context.Background(), store.createdSteps[0].ExecutionID, StepResult{OutcomeID: "done"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := false
	for _, e := range events.events {
		if e.Type == domain.EventStepStarted {
			found = true
		}
	}
	if !found {
		t.Error("expected step_started event")
	}
}

func TestFailStep_EmitsStepFailed(t *testing.T) {
	store, runID := testRunWithStep()
	events := &mockEventEmitter{}
	wf := &domain.WorkflowDefinition{
		ID:        "wf-test",
		EntryStep: "start",
		Steps: []domain.StepDefinition{
			{ID: "start", Name: "Start"},
		},
	}
	loader := &mockWorkflowLoader{wfDef: wf}
	orch := stepTestOrchestrator(store, events, loader, nil, nil)

	err := orch.FailStep(context.Background(), runID+"-start-1", domain.FailurePermanent, "crash")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := false
	for _, e := range events.events {
		if e.Type == domain.EventStepFailed {
			found = true
		}
	}
	if !found {
		t.Error("expected step_failed event")
	}
}

// Ensure imports are used.
var _ = workflow.TriggerActivate
