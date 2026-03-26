package assert

import (
	"context"
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/scenariotest/harness"
)

// RunStatus asserts that a workflow run has the expected status.
func RunStatus(t *testing.T, db *harness.TestDB, ctx context.Context, runID string, expected domain.RunStatus) {
	t.Helper()
	run, err := db.Store.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("get run %s: %v", runID, err)
	}
	if run.Status != expected {
		t.Errorf("run %s status: got %q, want %q", runID, run.Status, expected)
	}
}

// RunCompleted asserts that a workflow run has completed successfully.
func RunCompleted(t *testing.T, db *harness.TestDB, ctx context.Context, runID string) {
	t.Helper()
	RunStatus(t, db, ctx, runID, domain.RunStatusCompleted)
}

// RunFailed asserts that a workflow run has failed.
func RunFailed(t *testing.T, db *harness.TestDB, ctx context.Context, runID string) {
	t.Helper()
	RunStatus(t, db, ctx, runID, domain.RunStatusFailed)
}

// StepExecutionStatus asserts that a step execution has the expected status.
func StepExecutionStatus(t *testing.T, db *harness.TestDB, ctx context.Context, executionID string, expected domain.StepExecutionStatus) {
	t.Helper()
	exec, err := db.Store.GetStepExecution(ctx, executionID)
	if err != nil {
		t.Fatalf("get step execution %s: %v", executionID, err)
	}
	if exec.Status != expected {
		t.Errorf("step execution %s status: got %q, want %q", executionID, exec.Status, expected)
	}
}

// StepCount asserts the number of step executions for a given run.
func StepCount(t *testing.T, db *harness.TestDB, ctx context.Context, runID string, expected int) {
	t.Helper()
	steps, err := db.Store.ListStepExecutionsByRun(ctx, runID)
	if err != nil {
		t.Fatalf("list steps for run %s: %v", runID, err)
	}
	if len(steps) != expected {
		t.Errorf("run %s step count: got %d, want %d", runID, len(steps), expected)
	}
}

// CurrentStep asserts the current step of a run.
func CurrentStep(t *testing.T, db *harness.TestDB, ctx context.Context, runID, expectedStepID string) {
	t.Helper()
	run, err := db.Store.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("get run %s: %v", runID, err)
	}
	if run.CurrentStepID != expectedStepID {
		t.Errorf("run %s current step: got %q, want %q", runID, run.CurrentStepID, expectedStepID)
	}
}
