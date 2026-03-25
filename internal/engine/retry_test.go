package engine

import (
	"context"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/domain"
)

func testRetryWorkflow() *domain.WorkflowDefinition {
	return &domain.WorkflowDefinition{
		ID:        "wf-test",
		EntryStep: "start",
		Steps: []domain.StepDefinition{{
			ID:   "start",
			Name: "Start",
			Type: "automated",
			Retry: &domain.RetryConfig{
				Limit:   3,
				Backoff: "exponential",
			},
			Outcomes: []domain.OutcomeDefinition{{
				ID:       "done",
				NextStep: "end",
			}},
		}},
	}
}

func failedExecution(runID, stepID string, attempt int, classification domain.FailureClassification) *domain.StepExecution {
	return &domain.StepExecution{
		ExecutionID: runID + "-" + stepID + "-1",
		RunID:       runID,
		StepID:      stepID,
		Status:      domain.StepStatusFailed,
		Attempt:     attempt,
		ErrorDetail: &domain.ErrorDetail{
			Classification: classification,
			Message:        "transient error",
			StepID:         stepID,
		},
		CreatedAt: time.Now(),
	}
}

func TestRetryStep_SchedulesRetry(t *testing.T) {
	store, runID := testRunWithStep()
	events := &mockEventEmitter{}
	wf := testRetryWorkflow()
	loader := &mockWorkflowLoader{wfDef: wf}

	orch := stepTestOrchestrator(store, events, loader, nil, nil)

	exec := failedExecution(runID, "start", 1, domain.FailureTransient)
	store.createdSteps[0] = exec

	err := orch.RetryStep(context.Background(), exec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have created a new step execution for retry.
	if len(store.createdSteps) < 2 {
		t.Fatal("expected retry execution to be created")
	}
	retry := store.createdSteps[len(store.createdSteps)-1]
	if retry.Attempt != 2 {
		t.Errorf("expected attempt 2, got %d", retry.Attempt)
	}
	if retry.Status != domain.StepStatusWaiting {
		t.Errorf("expected waiting status, got %s", retry.Status)
	}
	if retry.RetryAfter == nil {
		t.Error("expected retry_after to be set")
	}
	if retry.RetryAfter != nil && retry.RetryAfter.Before(time.Now()) {
		t.Error("expected retry_after to be in the future")
	}

	// Should have emitted retry event.
	if len(events.events) == 0 {
		t.Error("expected retry_attempted event")
	} else if events.events[len(events.events)-1].Type != domain.EventRetryAttempted {
		t.Errorf("expected retry_attempted event, got %s", events.events[len(events.events)-1].Type)
	}
}

func TestRetryStep_ExhaustedFailsRun(t *testing.T) {
	store, runID := testRunWithStep()
	events := &mockEventEmitter{}
	wf := testRetryWorkflow()
	loader := &mockWorkflowLoader{wfDef: wf}

	orch := stepTestOrchestrator(store, events, loader, nil, nil)

	// Attempt 3 with limit 3 — exhausted.
	exec := failedExecution(runID, "start", 3, domain.FailureTransient)
	store.createdSteps[0] = exec

	err := orch.RetryStep(context.Background(), exec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Run should be failed.
	if store.runs[runID].Status != domain.RunStatusFailed {
		t.Errorf("expected run to be failed, got %s", store.runs[runID].Status)
	}
}

func TestRetryStep_PermanentFailureNoRetry(t *testing.T) {
	store, runID := testRunWithStep()
	events := &mockEventEmitter{}
	wf := testRetryWorkflow()
	loader := &mockWorkflowLoader{wfDef: wf}

	orch := stepTestOrchestrator(store, events, loader, nil, nil)

	// Permanent failure — should not retry even on attempt 1.
	exec := failedExecution(runID, "start", 1, domain.FailurePermanent)
	store.createdSteps[0] = exec

	err := orch.RetryStep(context.Background(), exec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Run should be failed (no retry for permanent errors).
	if store.runs[runID].Status != domain.RunStatusFailed {
		t.Errorf("expected run to be failed, got %s", store.runs[runID].Status)
	}
}

func TestRetryStep_NoRetryConfig(t *testing.T) {
	store, runID := testRunWithStep()
	events := &mockEventEmitter{}
	wf := testRetryWorkflow()
	wf.Steps[0].Retry = nil // No retry config.
	loader := &mockWorkflowLoader{wfDef: wf}

	orch := stepTestOrchestrator(store, events, loader, nil, nil)

	exec := failedExecution(runID, "start", 1, domain.FailureTransient)
	store.createdSteps[0] = exec

	err := orch.RetryStep(context.Background(), exec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Run should be failed immediately (no retry config).
	if store.runs[runID].Status != domain.RunStatusFailed {
		t.Errorf("expected run to be failed, got %s", store.runs[runID].Status)
	}
}

func TestRetryStep_LinearBackoff(t *testing.T) {
	store, runID := testRunWithStep()
	events := &mockEventEmitter{}
	wf := testRetryWorkflow()
	wf.Steps[0].Retry.Backoff = "linear"
	loader := &mockWorkflowLoader{wfDef: wf}

	orch := stepTestOrchestrator(store, events, loader, nil, nil)

	exec := failedExecution(runID, "start", 1, domain.FailureTransient)
	store.createdSteps[0] = exec

	err := orch.RetryStep(context.Background(), exec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have created retry execution.
	if len(store.createdSteps) < 2 {
		t.Fatal("expected retry execution to be created")
	}
	retry := store.createdSteps[len(store.createdSteps)-1]
	if retry.RetryAfter == nil {
		t.Error("expected retry_after to be set")
	}
}

func TestRetryStep_WrongStatusRejects(t *testing.T) {
	store, runID := testRunWithStep()
	events := &mockEventEmitter{}
	wf := testRetryWorkflow()
	loader := &mockWorkflowLoader{wfDef: wf}

	orch := stepTestOrchestrator(store, events, loader, nil, nil)

	// Step is in_progress, not failed.
	exec := &domain.StepExecution{
		ExecutionID: runID + "-start-1",
		RunID:       runID,
		StepID:      "start",
		Status:      domain.StepStatusInProgress,
		Attempt:     1,
	}

	err := orch.RetryStep(context.Background(), exec)
	if err == nil {
		t.Fatal("expected error for non-failed step")
	}
}

func TestRetryStep_AttemptCountIncremented(t *testing.T) {
	store, runID := testRunWithStep()
	events := &mockEventEmitter{}
	wf := testRetryWorkflow()
	loader := &mockWorkflowLoader{wfDef: wf}

	orch := stepTestOrchestrator(store, events, loader, nil, nil)

	// Attempt 2 → should create attempt 3.
	exec := failedExecution(runID, "start", 2, domain.FailureTransient)
	store.createdSteps[0] = exec

	err := orch.RetryStep(context.Background(), exec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	retry := store.createdSteps[len(store.createdSteps)-1]
	if retry.Attempt != 3 {
		t.Errorf("expected attempt 3, got %d", retry.Attempt)
	}
	if retry.ExecutionID != runID+"-start-3" {
		t.Errorf("expected execution ID %s-start-3, got %s", runID, retry.ExecutionID)
	}
}
