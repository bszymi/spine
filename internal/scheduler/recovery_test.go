package scheduler_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/scheduler"
	"github.com/bszymi/spine/internal/store"
)

func TestRecoverPendingRuns_IdempotentOnExistingStep(t *testing.T) {
	fs := newFakeStore()
	ev := &fakeEventRouter{}

	// Pending run that already has an entry step (from a previous recovery).
	fs.runs = []domain.Run{
		{RunID: "run-1", Status: domain.RunStatusPending, WorkflowPath: "wf/test.yaml", TraceID: "trace-abc123456789"},
	}
	fs.stepExecs = []domain.StepExecution{
		{ExecutionID: "run-1-start-1", RunID: "run-1", StepID: "start", Status: domain.StepStatusWaiting, Attempt: 1},
	}
	fs.workflows["wf/test.yaml"] = &store.WorkflowProjection{
		Definition: workflowWithStep("start", "", "", 0),
	}

	s := scheduler.New(fs, ev)
	result, err := s.RecoverOnStartup(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should activate run but not create duplicate step.
	if result.PendingActivated != 1 {
		t.Errorf("expected 1 pending activated, got %d", result.PendingActivated)
	}

	// Should still only have 1 step execution (no duplicate).
	stepCount := 0
	for _, e := range fs.stepExecs {
		if e.RunID == "run-1" && e.StepID == "start" {
			stepCount++
		}
	}
	if stepCount != 1 {
		t.Errorf("expected 1 step execution (idempotent), got %d", stepCount)
	}
}

func TestRecoverActiveRuns_CompletedStepCallsEngine(t *testing.T) {
	fs := newFakeStore()
	ev := &fakeEventRouter{}

	fs.runs = []domain.Run{
		{RunID: "run-1", Status: domain.RunStatusActive, CurrentStepID: "start", WorkflowPath: "wf/test.yaml", TraceID: "trace-abc123456789"},
	}
	fs.stepExecs = []domain.StepExecution{
		{ExecutionID: "run-1-start-1", RunID: "run-1", StepID: "start", Status: domain.StepStatusCompleted, Attempt: 1, OutcomeID: "done"},
	}
	fs.workflows["wf/test.yaml"] = &store.WorkflowProjection{
		Definition: workflowWithStep("start", "", "", 0),
	}

	var recoveredExecID string
	recoveryFn := func(_ context.Context, execID string) error {
		recoveredExecID = execID
		return nil
	}

	s := scheduler.New(fs, ev, scheduler.WithStepRecovery(recoveryFn))
	result, err := s.RecoverOnStartup(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.ActiveResumed != 1 {
		t.Errorf("expected 1 active resumed, got %d", result.ActiveResumed)
	}
	if recoveredExecID != "run-1-start-1" {
		t.Errorf("expected recovery function called with run-1-start-1, got %s", recoveredExecID)
	}
}

func TestRecoverActiveRuns_FailedStepCallsEngine(t *testing.T) {
	fs := newFakeStore()
	ev := &fakeEventRouter{}

	fs.runs = []domain.Run{
		{RunID: "run-1", Status: domain.RunStatusActive, CurrentStepID: "start", WorkflowPath: "wf/test.yaml", TraceID: "trace-abc123456789"},
	}
	fs.stepExecs = []domain.StepExecution{
		{ExecutionID: "run-1-start-1", RunID: "run-1", StepID: "start", Status: domain.StepStatusFailed, Attempt: 1,
			ErrorDetail: &domain.ErrorDetail{Classification: domain.FailureTransient, Message: "network error"}},
	}
	fs.workflows["wf/test.yaml"] = &store.WorkflowProjection{
		Definition: workflowWithStep("start", "", "", 3),
	}

	var recoveredExecID string
	recoveryFn := func(_ context.Context, execID string) error {
		recoveredExecID = execID
		return nil
	}

	s := scheduler.New(fs, ev, scheduler.WithStepRecovery(recoveryFn))
	_, err := s.RecoverOnStartup(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if recoveredExecID != "run-1-start-1" {
		t.Errorf("expected recovery function called with run-1-start-1, got %s", recoveredExecID)
	}
}

func TestScanOrphans_FailsStuckRun(t *testing.T) {
	fs := newFakeStore()
	ev := &fakeEventRouter{}

	// Must be older than 3x orphan threshold (default 5min) = 15min.
	old := time.Now().Add(-20 * time.Minute)
	fs.runs = []domain.Run{
		{RunID: "run-1", Status: domain.RunStatusActive, TaskPath: "tasks/task.md", CreatedAt: old, TraceID: "trace-abc123456789"},
	}

	var failedRunID, failReason string
	failFn := func(_ context.Context, runID, reason string) error {
		failedRunID = runID
		failReason = reason
		// Simulate the run being failed.
		for i := range fs.runs {
			if fs.runs[i].RunID == runID {
				fs.runs[i].Status = domain.RunStatusFailed
			}
		}
		return nil
	}

	s := scheduler.New(fs, ev, scheduler.WithRunFail(failFn))
	err := s.ScanOrphans(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if failedRunID != "run-1" {
		t.Errorf("expected run-1 to be failed, got %s", failedRunID)
	}
	if failReason == "" {
		t.Error("expected fail reason")
	}
}

func TestScanOrphans_NoFailWithoutCallback(t *testing.T) {
	fs := newFakeStore()
	ev := &fakeEventRouter{}

	old := time.Now().Add(-10 * time.Minute)
	fs.runs = []domain.Run{
		{RunID: "run-1", Status: domain.RunStatusActive, TaskPath: "tasks/task.md", CreatedAt: old, TraceID: "trace-abc123456789"},
	}

	// No runFailFn configured — should only log, not fail.
	s := scheduler.New(fs, ev)
	err := s.ScanOrphans(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Run should still be active.
	if fs.runs[0].Status != domain.RunStatusActive {
		t.Errorf("expected run to remain active without fail callback, got %s", fs.runs[0].Status)
	}
}

// Ensure workflowWithStep helper uses json for this file.
var _ = json.Marshal
var _ = store.WorkflowProjection{}
