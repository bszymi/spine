package engine

// INIT-023 TASK-008 review round 1: a call that loses its compare-and-swap
// answers exactly as the sequential call after the winner would (Decision
// D9) — the two lostSubmitRace branches no test reached, AcknowledgeStep
// losing to the same actor's identical transition — and the queue consumer
// never routes one completion twice in-process. Every test names the
// mutation that turns it red.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bszymi/spine/core/actor"
	"github.com/bszymi/spine/core/domain"
)

// lostSubmitRace's run-terminated and ownership branches, through
// IngestResult with the step capability. Each concurrent writer lands
// after SubmitStepResult has its snapshot, so the terminal write misses
// and the answer is lostSubmitRace's.
func TestIngestResult_LostRaceAnswersLikeTheSequentialCall(t *testing.T) {
	ctx := context.Background()
	submit := func(f *casFix, id string) (*IngestResponse, error) {
		return f.orch.IngestResult(ctx, SubmitRequest{ExecutionID: id, OutcomeID: "completed",
			ArtifactsProduced: []string{"deliverable"}, ActorID: "actor-a"})
	}

	// TASK-007's 409 contract on the race path: a CancelRun (strict
	// step-entry policy) commits after SubmitStepResult's run read — the
	// run is still active there — and terminates the execution. The
	// sequential late submit gets runTerminatedConflict; so must this one,
	// not 200 {status: skipped}, which tells a late runner its work was
	// accepted. Mutation: deleting the run-terminated branch in
	// lostSubmitRace fails it.
	t.Run("run terminated by a cancel: TASK-007's 409", func(t *testing.T) {
		f := newCasFix(t, casWorkflow(), true)
		f.orch.entryPolicy = StepEntryPolicy{EnforceRunStatus: true, TerminateStepsOnCancel: true}
		e := f.addExec("execute", domain.StepStatusInProgress, "actor-a")
		f.st.afterGetRun = func(_ string, n int) {
			if n == 2 { // SubmitStepResult's GetRun (IngestResult's is the first)
				if err := f.orch.CancelRun(ctx, f.runID); err != nil {
					t.Errorf("CancelRun: %v", err)
				}
			}
		}
		resp, err := submit(f, e.ExecutionID)
		requireCode(t, err, domain.ErrConflict, "a submit that lost to a terminating cancel")
		if errors.Is(err, ErrStepStateChanged) || !strings.Contains(err.Error(), "terminated with the run") {
			t.Fatalf("err = %v, want TASK-007's run-terminated conflict", err)
		}
		if resp != nil {
			t.Fatalf("response %+v alongside the refusal", resp)
		}
		if got := f.st.exec(e.ExecutionID); !isRunTerminated(got) {
			t.Fatalf("execution = %s, want skipped/run_terminated", got.Status)
		}
		if f.st.exec(f.runID+"-review-1") != nil || len(f.events(domain.EventStepCompleted)) != 0 {
			t.Fatal("the losing submit routed or emitted")
		}
	})

	// The ownership re-check after the re-read: the step is released,
	// rebound to actor-b and completed by actor-b between SubmitStepResult's
	// read and its terminal write. actor-a learns nothing about it — 403,
	// as the sequential submit gets — not 200 with actor-b's outcome.
	// Mutation: deleting checkSubmitter in lostSubmitRace fails it.
	t.Run("rebound and completed by another actor: 403", func(t *testing.T) {
		f := newCasFix(t, casWorkflow(), true)
		e := f.addExec("execute", domain.StepStatusInProgress, "actor-a")
		f.st.afterGetExec = func(id string, n int) {
			if n == 2 { // SubmitStepResult has its snapshot (IngestResult's read is the first)
				f.st.mutateExec(id, func(x *domain.StepExecution) {
					done := time.Now()
					x.ActorID, x.Status, x.OutcomeID, x.CompletedAt = "actor-b", domain.StepStatusCompleted, "completed", &done
				})
			}
		}
		resp, err := submit(f, e.ExecutionID)
		requireCode(t, err, domain.ErrForbidden, "a non-owner after the race")
		if resp != nil || strings.Contains(err.Error(), "actor-b") {
			t.Fatalf("the refusal disclosed the execution: resp %+v, err %v", resp, err)
		}
		if got := f.st.exec(e.ExecutionID); got.ActorID != "actor-b" || got.Status != domain.StepStatusCompleted {
			t.Fatalf("execution = %s/%q, want actor-b's completion untouched", got.Status, got.ActorID)
		}
		if len(f.events(domain.EventStepCompleted)) != 0 {
			t.Fatal("the losing submit emitted step_completed")
		}
	})
}

// AcknowledgeStep losing its compare-and-swap (with the capability) to the
// same actor's identical transition — a concurrent retry of the same
// acknowledge, or the actor's own submit auto-acknowledging — answers like
// the sequential retry: the idempotent in_progress result, judged on the
// re-read row (TASK-007's run check included), with no second
// step_started. Any other move keeps the conflict (the released row is
// TestStepWriters_LostRaceHasNoSideEffects/AcknowledgeStep). Mutation:
// returning the race error unconditionally fails the first; skipping the
// run check on the race path fails the second.
func TestAcknowledgeStep_LostRaceToTheSameActor(t *testing.T) {
	ctx := context.Background()
	winnerStarted := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	ackBy := func(f *casFix, id string) {
		f.st.mutateExec(id, func(x *domain.StepExecution) {
			s := winnerStarted
			x.Status, x.StartedAt = domain.StepStatusInProgress, &s
		})
	}

	t.Run("a concurrent duplicate: the idempotent in_progress answer", func(t *testing.T) {
		f := newCasFix(t, casWorkflow(), true)
		e := f.addExec("execute", domain.StepStatusAssigned, "actor-a")
		f.st.afterGetExec = func(id string, n int) {
			if n == 1 { // this call has its (assigned, actor-a) snapshot
				ackBy(f, id)
			}
		}
		res, err := f.orch.AcknowledgeStep(ctx, AcknowledgeRequest{ActorID: "actor-a", ExecutionID: e.ExecutionID})
		if err != nil {
			t.Fatalf("lost to the same actor's acknowledge: %v, want the idempotent answer", err)
		}
		if res.Status != string(domain.StepStatusInProgress) || res.StartedAt == nil || !res.StartedAt.Equal(winnerStarted) {
			t.Fatalf("result = %+v, want in_progress with the winner's started_at", res)
		}
		if len(f.st.transitions) != 1 || f.st.transitions[0].applied {
			t.Fatalf("conditional writes = %+v, want exactly one, missed", f.st.transitions)
		}
		if n := len(f.events(domain.EventStepStarted)); n != 0 {
			t.Fatalf("%d step_started from the losing acknowledge", n)
		}
	})

	t.Run("EnforceRunStatus, the run cancelled meanwhile: TASK-007's refusal", func(t *testing.T) {
		f := newCasFix(t, casWorkflow(), true)
		f.orch.entryPolicy = StepEntryPolicy{EnforceRunStatus: true}
		e := f.addExec("execute", domain.StepStatusAssigned, "actor-a")
		f.st.afterGetRun = func(_ string, n int) {
			if n == 1 { // this call checked an active run; the owner's ack and a cancel land now
				ackBy(f, e.ExecutionID)
				f.st.setRunStatus(f.runID, domain.RunStatusCancelled)
			}
		}
		res, err := f.orch.AcknowledgeStep(ctx, AcknowledgeRequest{ActorID: "actor-a", ExecutionID: e.ExecutionID})
		requireCode(t, err, domain.ErrConflict, "a re-acknowledge on a dead run")
		if errors.Is(err, ErrStepStateChanged) || !strings.Contains(err.Error(), "active run") || res != nil {
			t.Fatalf("res %+v, err %v; want the run-status refusal a sequential re-acknowledge gets", res, err)
		}
	})

	t.Run("lost to the actor's own completed submit: conflict", func(t *testing.T) {
		f := newCasFix(t, casWorkflow(), true)
		e := f.addExec("execute", domain.StepStatusAssigned, "actor-a")
		f.st.afterGetExec = func(id string, n int) {
			if n == 1 {
				f.st.mutateExec(id, func(x *domain.StepExecution) {
					done := time.Now()
					x.Status, x.OutcomeID, x.CompletedAt = domain.StepStatusCompleted, "completed", &done
				})
			}
		}
		res, err := f.orch.AcknowledgeStep(ctx, AcknowledgeRequest{ActorID: "actor-a", ExecutionID: e.ExecutionID})
		if !errors.Is(err, ErrStepStateChanged) || res != nil {
			t.Fatalf("res %+v, err %v; want ErrStepStateChanged", res, err)
		}
		if got := f.st.exec(e.ExecutionID).Status; got != domain.StepStatusCompleted {
			t.Fatalf("execution = %s, want completed", got)
		}
	})
}

// onCompleted runs fn once, right after the first applied conditional
// write that completes an execution: the window between a winning
// submit's terminal write and its routing.
type onCompleted struct {
	condStore
	once sync.Once
	fn   func()
}

func (s *onCompleted) TransitionStepExecution(ctx context.Context, e *domain.StepExecution, from domain.StepExecutionStatus, fromActor string) (bool, error) {
	applied, err := s.condStore.TransitionStepExecution(ctx, e, from, fromActor)
	if applied && err == nil && e.Status == domain.StepStatusCompleted {
		s.once.Do(s.fn)
	}
	return applied, err
}

// Two deliveries of one assignment overlap in one consumer.
//
// The winner's window: the second runs while the first is between its
// terminal write and its routing. Its submit is refused (terminal) and it
// must leave the routing to the first — RecoverCompletedStep cannot tell
// a live winner from a crash, so it would create review-1 and the
// winner's advanceToNextStep review-2. One completion, one successor; a
// redelivery after the winner finished recovers nothing (routed).
//
// Two losers: the execution was completed elsewhere and its routing never
// ran; both deliveries are refused, and the last one out of
// SubmitStepResult recovers it — exactly once.
//
// Mutations: dropping the in-flight check in executeAndSubmit fails the
// first (two review executions); checking before leaving fails
// TestConsumer_RedeliveryFinishesIncompleteRouting (a lone redelivery sees
// itself and never recovers).
func TestConsumer_OverlappingDeliveriesRouteOnce(t *testing.T) {
	ctx := context.Background()
	reviews := func(f *casFix) []string {
		var ids []string
		for _, x := range f.st.list(f.runID) {
			if x.StepID == "review" {
				ids = append(ids, x.ExecutionID)
			}
		}
		return ids
	}
	routedOnce := func(t *testing.T, f *casFix) {
		t.Helper()
		if got := reviews(f); len(got) != 1 || got[0] != f.runID+"-review-1" {
			t.Fatalf("review executions = %v, want exactly [%s-review-1]: one completion routed twice", got, f.runID)
		}
		if got := f.st.run(f.runID).CurrentStepID; got != "review" {
			t.Fatalf("run cursor = %q, want review", got)
		}
	}
	reqFor := func(f *casFix, id string) actor.AssignmentRequest {
		return actor.AssignmentRequest{AssignmentID: id, RunID: f.runID, StepID: "execute", ActorID: "actor-a"}
	}

	t.Run("a redelivery inside the winner's window leaves the routing to it", func(t *testing.T) {
		f := newCasFix(t, casWorkflow(), true)
		e := f.addExec("execute", domain.StepStatusInProgress, "actor-a")
		req := reqFor(f, e.ExecutionID)
		c := NewConsumer(nil, f.orch)
		var second error
		ran := false
		hs := &onCompleted{condStore: condStore{f.st}}
		hs.fn = func() {
			ran = true
			second = c.executeAndSubmit(ctx, discardLogger(), req, staticProvider{outcome: "completed"})
		}
		f.orch.store = hs

		if err := c.executeAndSubmit(ctx, discardLogger(), req, staticProvider{outcome: "completed"}); err != nil {
			t.Fatalf("first delivery: %v", err)
		}
		if !ran || second != nil {
			t.Fatalf("second delivery ran=%v err=%v; want it to run inside the window and answer nil", ran, second)
		}
		routedOnce(t, f)
		if err := c.executeAndSubmit(ctx, discardLogger(), req, staticProvider{outcome: "completed"}); err != nil {
			t.Fatalf("a later redelivery: %v", err)
		}
		routedOnce(t, f)
		if c.submittingNow(req.AssignmentID) {
			t.Fatal("the in-flight count leaked")
		}
	})

	t.Run("two refused deliveries: the last one out recovers, once", func(t *testing.T) {
		f := newCasFix(t, casWorkflow(), true)
		e := f.addExec("execute", domain.StepStatusCompleted, "actor-a") // completed elsewhere, never routed
		f.st.mutateExec(e.ExecutionID, func(x *domain.StepExecution) { x.OutcomeID = "completed" })
		req := reqFor(f, e.ExecutionID)
		c := NewConsumer(nil, f.orch)
		var second error
		ran := false
		f.st.afterGetExec = func(id string, n int) {
			if n == 1 { // the first delivery is inside SubmitStepResult
				ran = true
				second = c.executeAndSubmit(ctx, discardLogger(), req, staticProvider{outcome: "completed"})
			}
		}
		if err := c.executeAndSubmit(ctx, discardLogger(), req, staticProvider{outcome: "completed"}); err != nil {
			t.Fatalf("first delivery: %v", err)
		}
		if !ran || second != nil {
			t.Fatalf("second delivery ran=%v err=%v", ran, second)
		}
		routedOnce(t, f)
	})
}
