package scheduler_test

// INIT-023 TASK-007 AC 26: the scheduler's run-timeout cancel writes the
// run row directly (it never calls engine.CancelRun), so it carries a
// WithRunTerminated hook that closes the run's live executions once the
// cancel write has landed.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/scheduler"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/engine"
)

func timedOutRunStore() *fakeStore {
	st := newFakeStore()
	past := time.Now().Add(-1 * time.Hour)
	st.runs = []domain.Run{{
		RunID: "run-1", TaskPath: "tasks/task.md", Status: domain.RunStatusActive,
		TraceID: "trace-abc123456789", TimeoutAt: &past, CreatedAt: time.Now().Add(-2 * time.Hour),
	}}
	st.stepExecs = []domain.StepExecution{
		{ExecutionID: "run-1-a-1", RunID: "run-1", StepID: "a", Status: domain.StepStatusWaiting, Attempt: 1},
		{ExecutionID: "run-1-b-1", RunID: "run-1", StepID: "b", Status: domain.StepStatusAssigned, ActorID: "worker", Attempt: 1},
	}
	return st
}

// rereadStore answers the timeout handler's re-read with a different run
// status, simulating a run that progressed after the scan snapshot.
type rereadStore struct {
	*fakeStore
	reread domain.RunStatus
}

func (r *rereadStore) GetRun(ctx context.Context, runID string) (*domain.Run, error) {
	run, err := r.fakeStore.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	cp := *run
	cp.Status = r.reread
	return &cp, nil
}

// failingCancelStore refuses the cancel write.
type failingCancelStore struct{ *fakeStore }

func (f *failingCancelStore) UpdateRunStatusAt(context.Context, string, domain.RunStatus, time.Time) error {
	return errors.New("write refused")
}

// Engine dependencies the termination path never reaches.
type (
	unusedResolver  struct{ engine.WorkflowResolver }
	unusedActors    struct{ engine.ActorAssigner }
	unusedArtifacts struct{ engine.ArtifactReader }
	unusedEvents    struct{ engine.EventEmitter }
	unusedGit       struct{ engine.GitOperator }
	unusedLoader    struct{ engine.WorkflowLoader }
)

// Mutation: dropping the fn call from handleRunTimeout fails the first two
// sub-tests.
func TestScheduler_RunTimeoutTerminatesLiveSteps(t *testing.T) {
	ctx := context.Background()

	t.Run("hook fires once after the cancel write", func(t *testing.T) {
		st := timedOutRunStore()
		var calls []string
		hook := func(_ context.Context, runID, message string) {
			if st.runs[0].Status != domain.RunStatusCancelled {
				t.Errorf("hook ran before the run row was cancelled (status %s)", st.runs[0].Status)
			}
			if message != "run cancelled: run timeout" {
				t.Errorf("hook message = %q", message)
			}
			calls = append(calls, runID)
		}
		s := scheduler.New(st, &fakeEventRouter{}, scheduler.WithRunTerminated(hook))
		if err := s.ScanRunTimeouts(ctx); err != nil {
			t.Fatal(err)
		}
		if len(calls) != 1 || calls[0] != "run-1" {
			t.Fatalf("hook calls = %v, want exactly [run-1]", calls)
		}
	})

	t.Run("end to end with the engine's TerminateLiveSteps", func(t *testing.T) {
		st := timedOutRunStore()
		orch, err := engine.New(unusedResolver{}, st, unusedActors{}, unusedArtifacts{}, unusedEvents{}, unusedGit{}, unusedLoader{})
		if err != nil {
			t.Fatal(err)
		}
		orch.WithStepEntryPolicy(engine.StrictStepEntryPolicy())
		s := scheduler.New(st, &fakeEventRouter{}, scheduler.WithRunTerminated(orch.TerminateLiveSteps))
		if err := s.ScanRunTimeouts(ctx); err != nil {
			t.Fatal(err)
		}
		for _, e := range st.stepExecs {
			if e.Status != domain.StepStatusSkipped || e.ErrorDetail == nil ||
				e.ErrorDetail.Classification != domain.FailureRunTerminated || e.CompletedAt == nil {
				t.Fatalf("%s = %s %+v, want skipped/run_terminated with CompletedAt", e.ExecutionID, e.Status, e.ErrorDetail)
			}
		}
	})

	t.Run("legacy policy leaves executions untouched", func(t *testing.T) {
		st := timedOutRunStore()
		orch, err := engine.New(unusedResolver{}, st, unusedActors{}, unusedArtifacts{}, unusedEvents{}, unusedGit{}, unusedLoader{})
		if err != nil {
			t.Fatal(err)
		}
		s := scheduler.New(st, &fakeEventRouter{}, scheduler.WithRunTerminated(orch.TerminateLiveSteps))
		if err := s.ScanRunTimeouts(ctx); err != nil {
			t.Fatal(err)
		}
		if st.runs[0].Status != domain.RunStatusCancelled {
			t.Fatalf("run status = %s, want cancelled", st.runs[0].Status)
		}
		if st.stepExecs[0].Status != domain.StepStatusWaiting || st.stepExecs[1].Status != domain.StepStatusAssigned {
			t.Fatalf("zero-value TerminateLiveSteps must be a no-op, got %s / %s", st.stepExecs[0].Status, st.stepExecs[1].Status)
		}
	})

	for _, status := range []domain.RunStatus{domain.RunStatusCommitting, domain.RunStatusCompleted, domain.RunStatusCancelled} {
		status := status
		t.Run("not called when the run already progressed to "+string(status), func(t *testing.T) {
			st := &rereadStore{fakeStore: timedOutRunStore(), reread: status}
			called := false
			s := scheduler.New(st, &fakeEventRouter{}, scheduler.WithRunTerminated(func(context.Context, string, string) { called = true }))
			if err := s.ScanRunTimeouts(ctx); err != nil {
				t.Fatal(err)
			}
			if called {
				t.Fatal("hook must not fire when the timeout cancel was not applied")
			}
		})
	}
	t.Run("not called when the cancel write fails", func(t *testing.T) {
		st := &failingCancelStore{fakeStore: timedOutRunStore()}
		called := false
		s := scheduler.New(st, &fakeEventRouter{}, scheduler.WithRunTerminated(func(context.Context, string, string) { called = true }))
		_ = s.ScanRunTimeouts(ctx)
		if called {
			t.Fatal("hook must not fire when the cancel write failed")
		}
	})
}
