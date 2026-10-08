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

// INIT-023 TASK-007 review round 1: the run-scoped separation-of-duties
// ledger read returns one actor's rows of one run, in every status, newest
// first — and nothing from the actor's other runs or other actors.
func TestListAssignmentsByRunAndActor(t *testing.T) {
	s := store.NewTestStore(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	runA, runB := "run-t007-ledger-a-"+suffix, "run-t007-ledger-b-"+suffix
	carol, dave := "carol-"+suffix, "dave-"+suffix
	now := time.Now().UTC().Truncate(time.Microsecond)

	t.Cleanup(func() {
		for _, r := range []string{runA, runB} {
			_ = s.ExecRaw(ctx, `DELETE FROM runtime.actor_assignments WHERE run_id = $1`, r)
			_ = s.ExecRaw(ctx, `DELETE FROM runtime.step_executions WHERE run_id = $1`, r)
			_ = s.ExecRaw(ctx, `DELETE FROM runtime.runs WHERE run_id = $1`, r)
		}
	})

	for _, r := range []string{runA, runB} {
		if err := s.CreateRun(ctx, &domain.Run{RunID: r, TaskPath: "tasks/t.md", WorkflowPath: "workflows/w.yaml",
			WorkflowID: "w", WorkflowVersion: "v1", Status: domain.RunStatusActive, TraceID: "trace-" + r, CreatedAt: now}); err != nil {
			t.Fatalf("create run %s: %v", r, err)
		}
		if err := s.CreateStepExecution(ctx, &domain.StepExecution{ExecutionID: r + "-execute-1", RunID: r, StepID: "execute",
			Status: domain.StepStatusWaiting, Attempt: 1, CreatedAt: now}); err != nil {
			t.Fatalf("create step execution in %s: %v", r, err)
		}
	}
	mk := func(id, runID, actorID string, status domain.AssignmentStatus, at time.Time) {
		t.Helper()
		if err := s.CreateAssignment(ctx, &domain.Assignment{AssignmentID: id + "-" + suffix, RunID: runID,
			ExecutionID: runID + "-execute-1", ActorID: actorID, Status: status, AssignedAt: at}); err != nil {
			t.Fatalf("create assignment %s: %v", id, err)
		}
	}
	mk("a-carol-cancelled", runA, carol, domain.AssignmentStatusCancelled, now)
	mk("a-carol-completed", runA, carol, domain.AssignmentStatusCompleted, now.Add(time.Minute))
	mk("a-dave-active", runA, dave, domain.AssignmentStatusActive, now.Add(2*time.Minute))
	mk("b-carol-active", runB, carol, domain.AssignmentStatusActive, now.Add(3*time.Minute))

	rows, err := s.ListAssignmentsByRunAndActor(ctx, runA, carol)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	want := []string{"a-carol-completed-" + suffix, "a-carol-cancelled-" + suffix}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows (%+v), want %v", len(rows), rows, want)
	}
	for i := range rows {
		if rows[i].AssignmentID != want[i] || rows[i].RunID != runA || rows[i].ActorID != carol {
			t.Fatalf("row %d = %+v, want %s in %s for %s (newest first)", i, rows[i], want[i], runA, carol)
		}
	}

	rows, err = s.ListAssignmentsByRunAndActor(ctx, runB, dave)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("dave holds nothing in %s, got %+v", runB, rows)
	}
}
