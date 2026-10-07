//go:build integration

package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/domain"
)

// INIT-023 TASK-007 AC 17: closing a terminated execution's active
// assignment rows.
func TestCancelActiveAssignmentsForExecution(t *testing.T) {
	s := store.NewTestStore(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	runID := "run-t007-cancel-" + suffix
	now := time.Now().UTC().Truncate(time.Microsecond)

	t.Cleanup(func() {
		_ = s.ExecRaw(ctx, `DELETE FROM runtime.actor_assignments WHERE run_id = $1`, runID)
		_ = s.ExecRaw(ctx, `DELETE FROM runtime.step_executions WHERE run_id = $1`, runID)
		_ = s.ExecRaw(ctx, `DELETE FROM runtime.runs WHERE run_id = $1`, runID)
	})

	if err := s.CreateRun(ctx, &domain.Run{RunID: runID, TaskPath: "tasks/t.md", WorkflowPath: "workflows/w.yaml",
		WorkflowID: "w", WorkflowVersion: "v1", Status: domain.RunStatusActive, TraceID: "trace-" + suffix, CreatedAt: now}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	execA, execB := runID+"-a-1", runID+"-b-1"
	for stepID, id := range map[string]string{"a": execA, "b": execB} {
		if err := s.CreateStepExecution(ctx, &domain.StepExecution{ExecutionID: id, RunID: runID, StepID: stepID,
			Status: domain.StepStatusAssigned, ActorID: "worker", Attempt: 1, CreatedAt: now}); err != nil {
			t.Fatalf("create step execution %s: %v", id, err)
		}
	}
	mk := func(id, execID string, status domain.AssignmentStatus) {
		t.Helper()
		if err := s.CreateAssignment(ctx, &domain.Assignment{AssignmentID: id, RunID: runID, ExecutionID: execID,
			ActorID: "worker", Status: status, AssignedAt: now}); err != nil {
			t.Fatalf("create assignment %s: %v", id, err)
		}
	}
	mk("asg-active-"+suffix, execA, domain.AssignmentStatusActive)
	mk("asg-done-"+suffix, execA, domain.AssignmentStatusCompleted)
	mk("asg-other-"+suffix, execB, domain.AssignmentStatusActive)

	at := now.Add(time.Minute)
	n, err := s.CancelActiveAssignmentsForExecution(ctx, execA, at)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if n != 1 {
		t.Fatalf("first call cancelled %d rows, want 1", n)
	}

	active, err := s.GetAssignment(ctx, "asg-active-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	if active.Status != domain.AssignmentStatusCancelled || active.RespondedAt == nil || !active.RespondedAt.Equal(at) {
		t.Fatalf("active row = %s responded_at=%v, want cancelled at %v", active.Status, active.RespondedAt, at)
	}
	done, err := s.GetAssignment(ctx, "asg-done-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != domain.AssignmentStatusCompleted || done.RespondedAt != nil {
		t.Fatalf("completed row changed: %s responded_at=%v", done.Status, done.RespondedAt)
	}
	other, err := s.GetAssignment(ctx, "asg-other-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	if other.Status != domain.AssignmentStatusActive {
		t.Fatalf("another execution's row changed: %s", other.Status)
	}

	n, err = s.CancelActiveAssignmentsForExecution(ctx, execA, at.Add(time.Minute))
	if err != nil {
		t.Fatalf("second cancel: %v", err)
	}
	if n != 0 {
		t.Fatalf("second call cancelled %d rows, want 0", n)
	}
}
