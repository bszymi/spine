//go:build integration

package store_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/domain"
)

// INIT-023 TASK-008 AC 8: the conditional step-execution write and the
// conditional run-timeout write.

func casFixture(t *testing.T) (*store.PostgresStore, context.Context, string) {
	t.Helper()
	s := store.NewTestStore(t)
	ctx := context.Background()
	runID := fmt.Sprintf("run-t008-cas-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = s.ExecRaw(ctx, `DELETE FROM runtime.step_executions WHERE run_id = $1`, runID)
		_ = s.ExecRaw(ctx, `DELETE FROM runtime.runs WHERE run_id = $1`, runID)
	})
	if err := s.CreateRun(ctx, &domain.Run{RunID: runID, TaskPath: "tasks/t.md", WorkflowPath: "workflows/w.yaml",
		WorkflowID: "w", WorkflowVersion: "v1", Status: domain.RunStatusActive, TraceID: "trace-" + runID,
		CreatedAt: time.Now().UTC().Truncate(time.Microsecond)}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	return s, ctx, runID
}

func casExec(t *testing.T, s *store.PostgresStore, ctx context.Context, runID, stepID string, st domain.StepExecutionStatus, actorID string) *domain.StepExecution {
	t.Helper()
	e := &domain.StepExecution{
		ExecutionID: runID + "-" + stepID + "-1", RunID: runID, StepID: stepID,
		Status: st, ActorID: actorID, Attempt: 1, CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
	}
	if err := s.CreateStepExecution(ctx, e); err != nil {
		t.Fatalf("create step execution: %v", err)
	}
	return e
}

// utcExec returns a copy of e with every timestamp in UTC, so rows read
// back from PostgreSQL (session time zone) compare with values built here.
func utcExec(e domain.StepExecution) domain.StepExecution {
	utc := func(p *time.Time) *time.Time {
		if p == nil {
			return nil
		}
		u := p.UTC()
		return &u
	}
	e.CreatedAt = e.CreatedAt.UTC()
	e.StartedAt, e.CompletedAt, e.RetryAfter = utc(e.StartedAt), utc(e.CompletedAt), utc(e.RetryAfter)
	return e
}

func TestTransitionStepExecution_AppliesOnMatch(t *testing.T) {
	s, ctx, runID := casFixture(t)
	e := casExec(t, s, ctx, runID, "execute", domain.StepStatusInProgress, "actor-a")

	started := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	completed := time.Now().UTC().Truncate(time.Microsecond)
	retry := completed.Add(time.Hour)
	want := *e
	want.Status = domain.StepStatusCompleted
	want.OutcomeID = "done"
	want.StartedAt = &started
	want.CompletedAt = &completed
	want.RetryAfter = &retry
	want.ErrorDetail = &domain.ErrorDetail{Classification: domain.FailurePermanent, Message: "m", StepID: "execute"}
	want.EligibleActorIDs = []string{"actor-a", "actor-c"}
	want.ActorID = "actor-c" // every column of the update set is written

	applied, err := s.TransitionStepExecution(ctx, &want, domain.StepStatusInProgress, "actor-a")
	if err != nil || !applied {
		t.Fatalf("TransitionStepExecution = (%v, %v), want (true, nil)", applied, err)
	}
	got, err := s.GetStepExecution(ctx, e.ExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(utcExec(*got), utcExec(want)) {
		t.Fatalf("row after the conditional write:\n got %+v\nwant %+v", *got, want)
	}

	// The blind update writes the same column set: the same values through
	// UpdateStepExecution read back identically.
	e2 := casExec(t, s, ctx, runID, "review", domain.StepStatusInProgress, "actor-a")
	want2 := want
	want2.ExecutionID, want2.StepID, want2.CreatedAt = e2.ExecutionID, e2.StepID, e2.CreatedAt
	if err := s.UpdateStepExecution(ctx, &want2); err != nil {
		t.Fatal(err)
	}
	got2, err := s.GetStepExecution(ctx, e2.ExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(utcExec(*got2), utcExec(want2)) {
		t.Fatalf("UpdateStepExecution and TransitionStepExecution write different columns:\n got %+v\nwant %+v", *got2, want2)
	}
}

func TestTransitionStepExecution_MissesOnMismatch(t *testing.T) {
	s, ctx, runID := casFixture(t)
	bound := casExec(t, s, ctx, runID, "execute", domain.StepStatusAssigned, "actor-a")
	unbound := casExec(t, s, ctx, runID, "review", domain.StepStatusWaiting, "")

	for _, tc := range []struct {
		name   string
		exec   *domain.StepExecution
		status domain.StepExecutionStatus
		actor  string
	}{
		{"status mismatch", bound, domain.StepStatusInProgress, "actor-a"},
		{"actor mismatch", bound, domain.StepStatusAssigned, "actor-b"},
		{"empty expectation vs bound actor", bound, domain.StepStatusAssigned, ""},
		{"actor expectation vs NULL actor", unbound, domain.StepStatusWaiting, "actor-a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, err := s.GetStepExecution(ctx, tc.exec.ExecutionID)
			if err != nil {
				t.Fatal(err)
			}
			w := *before
			w.Status = domain.StepStatusCompleted
			w.OutcomeID = "stale"
			w.ActorID = "actor-x"
			applied, err := s.TransitionStepExecution(ctx, &w, tc.status, tc.actor)
			if err != nil || applied {
				t.Fatalf("TransitionStepExecution = (%v, %v), want (false, nil)", applied, err)
			}
			after, err := s.GetStepExecution(ctx, tc.exec.ExecutionID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("a missed write changed the row: %+v -> %+v", before, after)
			}
		})
	}

	// NULL and '' both mean "no actor": a row whose actor_id is '' (an
	// embedder fixture writing the column directly) matches "".
	if err := s.ExecRaw(ctx, `UPDATE runtime.step_executions SET actor_id = '' WHERE execution_id = $1`, unbound.ExecutionID); err != nil {
		t.Fatal(err)
	}
	w := *unbound
	w.Status = domain.StepStatusAssigned
	w.ActorID = "actor-a"
	if applied, err := s.TransitionStepExecution(ctx, &w, domain.StepStatusWaiting, ""); err != nil || !applied {
		t.Fatalf("'' actor vs empty expectation = (%v, %v), want (true, nil)", applied, err)
	}
}

func TestTransitionStepExecution_MissingRow(t *testing.T) {
	s, ctx, runID := casFixture(t)
	applied, err := s.TransitionStepExecution(ctx, &domain.StepExecution{
		ExecutionID: runID + "-ghost-1", RunID: runID, StepID: "ghost", Status: domain.StepStatusCompleted,
	}, domain.StepStatusInProgress, "actor-a")
	var se *domain.SpineError
	if applied || !errors.As(err, &se) || se.Code != domain.ErrNotFound {
		t.Fatalf("missing row = (%v, %v), want (false, ErrNotFound)", applied, err)
	}
}

func TestTransitionStepExecution_OneOfTwoRacersApplies(t *testing.T) {
	s, ctx, runID := casFixture(t)
	for round := 0; round < 20; round++ {
		stepID := fmt.Sprintf("race%d", round)
		e := casExec(t, s, ctx, runID, stepID, domain.StepStatusInProgress, "actor-a")
		var wg sync.WaitGroup
		results := make([]bool, 2)
		errs := make([]error, 2)
		for i := range results {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				w := *e
				w.Status = domain.StepStatusCompleted
				w.OutcomeID = fmt.Sprintf("outcome-%d", i)
				results[i], errs[i] = s.TransitionStepExecution(ctx, &w, domain.StepStatusInProgress, "actor-a")
			}(i)
		}
		wg.Wait()
		if errs[0] != nil || errs[1] != nil {
			t.Fatalf("round %d: errors %v / %v", round, errs[0], errs[1])
		}
		if results[0] == results[1] {
			t.Fatalf("round %d: applied = %v / %v, want exactly one", round, results[0], results[1])
		}
		got, err := s.GetStepExecution(ctx, e.ExecutionID)
		if err != nil {
			t.Fatal(err)
		}
		winner := 0
		if results[1] {
			winner = 1
		}
		if got.OutcomeID != fmt.Sprintf("outcome-%d", winner) {
			t.Fatalf("round %d: stored outcome %q, want the winner's outcome-%d", round, got.OutcomeID, winner)
		}
	}
}

func TestTransitionRunStatusAt(t *testing.T) {
	s, ctx, runID := casFixture(t)
	at := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)

	// A miss: the run is not in the expected status. Nothing changes.
	applied, err := s.TransitionRunStatusAt(ctx, runID, domain.RunStatusCommitting, domain.RunStatusCancelled, at)
	if err != nil || applied {
		t.Fatalf("miss = (%v, %v), want (false, nil)", applied, err)
	}
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != domain.RunStatusActive || run.CompletedAt != nil {
		t.Fatalf("a missed write changed the run: status=%s completed_at=%v", run.Status, run.CompletedAt)
	}

	// A hit stamps the supplied completed_at.
	applied, err = s.TransitionRunStatusAt(ctx, runID, domain.RunStatusActive, domain.RunStatusCancelled, at)
	if err != nil || !applied {
		t.Fatalf("hit = (%v, %v), want (true, nil)", applied, err)
	}
	run, err = s.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != domain.RunStatusCancelled || run.CompletedAt == nil || !run.CompletedAt.Equal(at) {
		t.Fatalf("after the hit: status=%s completed_at=%v, want cancelled at %v", run.Status, run.CompletedAt, at)
	}

	// A missing run is a miss, not an error (TransitionRunStatus's contract).
	if applied, err := s.TransitionRunStatusAt(ctx, runID+"-ghost", domain.RunStatusActive, domain.RunStatusCancelled, at); err != nil || applied {
		t.Fatalf("missing run = (%v, %v), want (false, nil)", applied, err)
	}
}
