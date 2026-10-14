package scheduler_test

// INIT-023 TASK-008 AC 12 (scheduler half): the timeout scanner, the
// run-timeout cancel and startup recovery write conditionally when the
// store can (the standalone PostgresStore), so a writer that moved the row
// after the scan is never overwritten.

import (
	"context"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/scheduler"
	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/observe"
)

// casStore adds the conditional writes PostgresStore has to fakeStore.
type casStore struct {
	*fakeStore
	afterListActive func()
	afterListRuns   func()
	afterListExecs  func()
	stepCAS         int
	runCAS          int
}

func (c *casStore) ListActiveStepExecutions(ctx context.Context) ([]domain.StepExecution, error) {
	out, err := c.fakeStore.ListActiveStepExecutions(ctx)
	if c.afterListActive != nil {
		c.afterListActive()
	}
	return out, err
}

func (c *casStore) ListRunsByStatus(ctx context.Context, st domain.RunStatus) ([]domain.Run, error) {
	out, err := c.fakeStore.ListRunsByStatus(ctx, st)
	if c.afterListRuns != nil {
		c.afterListRuns()
	}
	return out, err
}

func (c *casStore) ListStepExecutionsByRun(ctx context.Context, runID string) ([]domain.StepExecution, error) {
	out, err := c.fakeStore.ListStepExecutionsByRun(ctx, runID)
	if c.afterListExecs != nil {
		c.afterListExecs()
	}
	return out, err
}

func (c *casStore) TransitionStepExecution(_ context.Context, e *domain.StepExecution, from domain.StepExecutionStatus, fromActor string) (bool, error) {
	c.stepCAS++
	for i := range c.stepExecs {
		if c.stepExecs[i].ExecutionID == e.ExecutionID {
			if c.stepExecs[i].Status != from || c.stepExecs[i].ActorID != fromActor {
				return false, nil
			}
			c.stepExecs[i] = *e
			c.updatedSteps[e.ExecutionID] = e
			return true, nil
		}
	}
	return false, domain.NewError(domain.ErrNotFound, "step execution not found")
}

func (c *casStore) TransitionRunStatusAt(_ context.Context, runID string, from, to domain.RunStatus, at time.Time) (bool, error) {
	c.runCAS++
	for i := range c.runs {
		if c.runs[i].RunID == runID {
			if c.runs[i].Status != from {
				return false, nil
			}
			c.runs[i].Status = to
			c.runs[i].CompletedAt = &at
			c.updatedRuns[runID] = to
			return true, nil
		}
	}
	return false, nil
}

func (c *casStore) WithTx(ctx context.Context, fn func(tx store.Tx) error) error {
	return c.fakeStore.WithTx(ctx, fn)
}

func TestScanTimeouts_SubmitWinsTheRace(t *testing.T) {
	started := time.Now().Add(-2 * time.Hour)
	fs := newFakeStore()
	fs.runs = []domain.Run{{RunID: "r1", Status: domain.RunStatusActive, WorkflowPath: "wf/test.yaml"}}
	fs.stepExecs = []domain.StepExecution{{ExecutionID: "e1", RunID: "r1", StepID: "step1",
		Status: domain.StepStatusInProgress, ActorID: "actor-a", StartedAt: &started}}
	fs.workflows["wf/test.yaml"] = &store.WorkflowProjection{Definition: workflowWithStep("step1", "1h", "", 0)}
	cs := &casStore{fakeStore: fs}
	cs.afterListActive = func() { // the owner's submit completes the step now
		fs.stepExecs[0].Status = domain.StepStatusCompleted
		fs.stepExecs[0].OutcomeID = "done"
	}
	events := &fakeEventRouter{}
	before := observe.GlobalMetrics.TimeoutsDetected.Value()

	if err := scheduler.New(cs, events).ScanTimeouts(context.Background()); err != nil {
		t.Fatalf("ScanTimeouts: %v", err)
	}
	if got := fs.stepExecs[0].Status; got != domain.StepStatusCompleted {
		t.Fatalf("a completed step was overwritten to %s by the timeout", got)
	}
	if cs.stepCAS != 1 {
		t.Fatalf("%d conditional writes, want 1", cs.stepCAS)
	}
	if len(events.events) != 0 {
		t.Fatalf("step_timeout emitted for a step a submit completed: %+v", events.events)
	}
	if got := observe.GlobalMetrics.TimeoutsDetected.Value(); got != before {
		t.Fatalf("TimeoutsDetected moved %d -> %d", before, got)
	}
}

func TestScanTimeouts_ConditionalWriteApplies(t *testing.T) {
	started := time.Now().Add(-2 * time.Hour)
	fs := newFakeStore()
	fs.runs = []domain.Run{{RunID: "r1", Status: domain.RunStatusActive, WorkflowPath: "wf/test.yaml"}}
	fs.stepExecs = []domain.StepExecution{{ExecutionID: "e1", RunID: "r1", StepID: "step1",
		Status: domain.StepStatusInProgress, ActorID: "actor-a", StartedAt: &started}}
	fs.workflows["wf/test.yaml"] = &store.WorkflowProjection{Definition: workflowWithStep("step1", "1h", "", 0)}
	cs := &casStore{fakeStore: fs}
	events := &fakeEventRouter{}
	if err := scheduler.New(cs, events).ScanTimeouts(context.Background()); err != nil {
		t.Fatalf("ScanTimeouts: %v", err)
	}
	if got := fs.stepExecs[0].Status; got != domain.StepStatusFailed {
		t.Fatalf("status = %s, want failed", got)
	}
	if len(events.events) != 1 || events.events[0].Type != domain.EventStepTimeout {
		t.Fatalf("events = %+v, want one step_timeout", events.events)
	}
}

func TestScanRunTimeouts_RunMovedToCommittingIsNotCancelled(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	fs := newFakeStore()
	fs.runs = []domain.Run{{RunID: "r1", Status: domain.RunStatusActive, TimeoutAt: &past, TraceID: "trace-abc123456789"}}
	// The re-read sees active; the merge start lands right after it.
	rs := &rereadThenMoveStore{casStore: &casStore{fakeStore: fs}, moveTo: domain.RunStatusCommitting}
	events := &fakeEventRouter{}
	hookCalls := 0
	sched := scheduler.New(rs, events, scheduler.WithRunTerminated(func(context.Context, string, string) { hookCalls++ }))
	if err := sched.ScanRunTimeouts(context.Background()); err != nil {
		t.Fatalf("ScanRunTimeouts: %v", err)
	}
	if got := fs.runs[0].Status; got != domain.RunStatusCommitting {
		t.Fatalf("status = %s, want committing (not cancelled)", got)
	}
	if rs.runCAS != 1 {
		t.Fatalf("%d conditional run writes, want 1", rs.runCAS)
	}
	if len(events.events) != 0 || hookCalls != 0 {
		t.Fatalf("a run-timeout that did not apply emitted %d events / fired the hook %d times", len(events.events), hookCalls)
	}
}

// rereadThenMoveStore moves the run to moveTo right after the timeout
// handler's re-read.
type rereadThenMoveStore struct {
	*casStore
	moveTo domain.RunStatus
	moved  bool
}

func (r *rereadThenMoveStore) GetRun(ctx context.Context, runID string) (*domain.Run, error) {
	run, err := r.casStore.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	cp := *run
	if !r.moved {
		r.moved = true
		run.Status = r.moveTo
	}
	return &cp, nil
}

func TestRecoverPendingRuns_MissSkipsTheRun(t *testing.T) {
	fs := newFakeStore()
	fs.runs = []domain.Run{{RunID: "r1", Status: domain.RunStatusPending, WorkflowPath: "wf/test.yaml"}}
	fs.workflows["wf/test.yaml"] = &store.WorkflowProjection{Definition: workflowWithStep("step1", "1h", "", 0)}
	cs := &casStore{fakeStore: fs}
	listed := false
	cs.afterListRuns = func() {
		if !listed { // the pending listing is taken; StartRun activates the run now
			listed = true
			fs.runs[0].Status = domain.RunStatusCancelled
		}
	}
	result, err := scheduler.New(cs, &fakeEventRouter{}).RecoverOnStartup(context.Background())
	if err != nil {
		t.Fatalf("RecoverOnStartup: %v", err)
	}
	if result.PendingActivated != 0 {
		t.Fatalf("PendingActivated = %d, want 0", result.PendingActivated)
	}
	if got := fs.runs[0].Status; got != domain.RunStatusCancelled {
		t.Fatalf("status = %s, want the concurrent writer's cancelled", got)
	}
	if len(fs.stepExecs) != 0 {
		t.Fatalf("an entry step was created for a run recovery did not activate: %+v", fs.stepExecs)
	}
}

func TestRecoverAssignedStep_MissSkips(t *testing.T) {
	fs := newFakeStore()
	fs.runs = []domain.Run{{RunID: "r1", Status: domain.RunStatusActive, CurrentStepID: "step1", WorkflowPath: "wf/test.yaml"}}
	fs.stepExecs = []domain.StepExecution{{ExecutionID: "e1", RunID: "r1", StepID: "step1",
		Status: domain.StepStatusAssigned, ActorID: "actor-a"}}
	cs := &casStore{fakeStore: fs}
	cs.afterListExecs = func() { // a live replica's runner acknowledges now
		fs.stepExecs[0].Status = domain.StepStatusInProgress
	}
	if _, err := scheduler.New(cs, &fakeEventRouter{}).RecoverOnStartup(context.Background()); err != nil {
		t.Fatalf("RecoverOnStartup: %v", err)
	}
	if got := fs.stepExecs[0].Status; got != domain.StepStatusInProgress {
		t.Fatalf("status = %s, want the acknowledged in_progress", got)
	}
	if cs.stepCAS == 0 {
		t.Fatal("recovery did not write conditionally")
	}
}
