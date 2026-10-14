package engine

// INIT-023 TASK-008, audit P3-17: the engine verifies the submitter.
// TestIngestResult_RefusesNonOwnerSubmitter is the committed form of the
// design-time PoC TestTASK008_PoC_SubmitHasNoSubmitter (HEAD:
// "SubmitRequest has no ActorID"). Mutation: deleting the comparison in
// IngestResult and SubmitStepResult (checkSubmitter) fails AC 1-3 and 5.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"testing"

	"github.com/bszymi/spine/core/actor"
	"github.com/bszymi/spine/core/domain"
)

// AC 1.
func TestIngestResult_RefusesNonOwnerSubmitter(t *testing.T) {
	t.Run("non-owner refused, nothing written", func(t *testing.T) {
		f := newCasFix(t, casWorkflow(), false)
		e := f.addExec("execute", domain.StepStatusInProgress, "actor-a")
		before := f.st.exec(e.ExecutionID)
		_, err := f.orch.IngestResult(context.Background(), SubmitRequest{
			ExecutionID: e.ExecutionID, OutcomeID: "completed", ArtifactsProduced: []string{"deliverable"}, ActorID: "actor-b"})
		requireCode(t, err, domain.ErrForbidden, "submit by actor-b on actor-a's step")
		if after := f.st.exec(e.ExecutionID); !reflect.DeepEqual(after, before) {
			t.Fatalf("a non-owner submit changed the row: %+v -> %+v", before, after)
		}
		if len(f.events(domain.EventStepCompleted)) != 0 {
			t.Fatal("step_completed emitted for a refused submit")
		}
		if f.st.exec(f.runID+"-review-1") != nil {
			t.Fatal("a refused submit routed its outcome")
		}
		if len(f.st.stepUpdates) != 0 {
			t.Fatalf("a refused submit wrote %v", f.st.stepUpdates)
		}
	})
	for _, submitter := range []string{"actor-a", ""} {
		t.Run(fmt.Sprintf("submitter %q completes as today", submitter), func(t *testing.T) {
			f := newCasFix(t, casWorkflow(), false)
			e := f.addExec("execute", domain.StepStatusInProgress, "actor-a")
			resp, err := f.orch.IngestResult(context.Background(), SubmitRequest{
				ExecutionID: e.ExecutionID, OutcomeID: "completed", ArtifactsProduced: []string{"deliverable"}, ActorID: submitter})
			if err != nil || resp.Status != domain.StepStatusCompleted {
				t.Fatalf("IngestResult = %+v, %v; want completed", resp, err)
			}
			if f.st.exec(f.runID+"-review-1") == nil {
				t.Fatal("the outcome was not routed")
			}
		})
	}
}

// AC 2: a non-owner cannot fail the owner's step through the
// missing-outputs path. HEAD: the step is failed with invalid_result.
func TestIngestResult_NonOwnerCannotFailStep(t *testing.T) {
	f := newCasFix(t, casWorkflow(), false)
	e := f.addExec("execute", domain.StepStatusInProgress, "actor-a")
	_, err := f.orch.IngestResult(context.Background(), SubmitRequest{
		ExecutionID: e.ExecutionID, OutcomeID: "completed", ActorID: "actor-b"}) // omits "deliverable"
	requireCode(t, err, domain.ErrForbidden, "non-owner submit omitting a required output")
	if got := f.st.exec(e.ExecutionID); got.Status != domain.StepStatusInProgress || got.ErrorDetail != nil {
		t.Fatalf("execution = %s (%+v), want untouched in_progress", got.Status, got.ErrorDetail)
	}
	if f.st.exec(f.runID+"-execute-2") != nil {
		t.Fatal("a retry execution was created")
	}
	if got := f.st.run(f.runID).Status; got != domain.RunStatusActive {
		t.Fatalf("run = %s, want active", got)
	}
}

// AC 3: parity with the gateway on terminal executions, and the re-read.
func TestSubmitOwnership_TerminalParityAndReread(t *testing.T) {
	ctx := context.Background()
	t.Run("(a) mismatch on a completed execution is forbidden", func(t *testing.T) {
		f := newCasFix(t, casWorkflow(), false)
		e := f.addExec("execute", domain.StepStatusCompleted, "actor-a")
		f.st.mutateExec(e.ExecutionID, func(x *domain.StepExecution) { x.OutcomeID = "completed" })
		_, err := f.orch.IngestResult(ctx, SubmitRequest{ExecutionID: e.ExecutionID, OutcomeID: "completed", ActorID: "actor-b"})
		requireCode(t, err, domain.ErrForbidden, "foreign duplicate submit")
	})
	t.Run("(a) a completed execution with no actor stays idempotent", func(t *testing.T) {
		f := newCasFix(t, casWorkflow(), false)
		e := f.addExec("execute", domain.StepStatusCompleted, "")
		f.st.mutateExec(e.ExecutionID, func(x *domain.StepExecution) { x.OutcomeID = "completed" })
		resp, err := f.orch.IngestResult(ctx, SubmitRequest{ExecutionID: e.ExecutionID, OutcomeID: "completed", ActorID: "actor-b"})
		if err != nil || resp.Status != domain.StepStatusCompleted {
			t.Fatalf("IngestResult = %+v, %v; want the idempotent completed response", resp, err)
		}
	})
	for _, capability := range []bool{false, true} {
		t.Run(fmt.Sprintf("(b) rebind between the reads, owner submits, capability=%v", capability), func(t *testing.T) {
			f := newCasFix(t, casWorkflow(), capability)
			e := f.addExec("execute", domain.StepStatusInProgress, "actor-a")
			f.st.afterGetExec = func(id string, n int) {
				if n == 1 {
					f.st.mutateExec(id, func(x *domain.StepExecution) { x.Status, x.ActorID = domain.StepStatusAssigned, "actor-b" })
				}
			}
			_, err := f.orch.IngestResult(ctx, SubmitRequest{ExecutionID: e.ExecutionID, OutcomeID: "completed",
				ArtifactsProduced: []string{"deliverable"}, ActorID: "actor-a"})
			requireCode(t, err, domain.ErrForbidden, "submit by the previous owner")
			assertRebindKept(t, f, e.ExecutionID)
		})
		// (d): the same rebind with no ActorID — SMP's shape until SMP
		// TASK-011. Mutation: dropping the expect comparison fails it.
		t.Run(fmt.Sprintf("(d) rebind between the reads, no ActorID, capability=%v", capability), func(t *testing.T) {
			f := newCasFix(t, casWorkflow(), capability)
			e := f.addExec("execute", domain.StepStatusInProgress, "actor-a")
			f.st.afterGetExec = func(id string, n int) {
				if n == 1 {
					f.st.mutateExec(id, func(x *domain.StepExecution) { x.Status, x.ActorID = domain.StepStatusAssigned, "actor-b" })
				}
			}
			_, err := f.orch.IngestResult(ctx, SubmitRequest{ExecutionID: e.ExecutionID, OutcomeID: "completed",
				ArtifactsProduced: []string{"deliverable"}})
			requireCode(t, err, domain.ErrConflict, "submit across a rebind")
			if !errors.Is(err, ErrStepStateChanged) {
				t.Fatalf("err = %v, want ErrStepStateChanged", err)
			}
			assertRebindKept(t, f, e.ExecutionID)
		})
	}
	t.Run("(c) the consumer submits as the actor it delivered to", func(t *testing.T) {
		f := newCasFix(t, casWorkflow(), false)
		e := f.addExec("execute", domain.StepStatusInProgress, "actor-b") // rebound while the provider ran
		c := NewConsumer(nil, f.orch)
		req := actor.AssignmentRequest{AssignmentID: e.ExecutionID, RunID: f.runID, StepID: "execute", ActorID: "actor-a"}
		err := c.executeAndSubmit(ctx, discardLogger(), req, staticProvider{outcome: "completed"})
		var se *domain.SpineError
		if !errors.As(err, &se) || se.Code != domain.ErrForbidden {
			t.Fatalf("consumer submit for a rebound step = %v, want forbidden", err)
		}
		if got := f.st.exec(e.ExecutionID); got.Status != domain.StepStatusInProgress {
			t.Fatalf("execution = %s, want untouched", got.Status)
		}
	})
	t.Run("(c) the consumer maps an already-resolved step to nil", func(t *testing.T) {
		f := newCasFix(t, casWorkflow(), false)
		e := f.addExec("execute", domain.StepStatusCompleted, "actor-a")
		f.st.mutateExec(e.ExecutionID, func(x *domain.StepExecution) { x.OutcomeID = "completed" })
		f.addExec("review", domain.StepStatusWaiting, "") // routed
		c := NewConsumer(nil, f.orch)
		req := actor.AssignmentRequest{AssignmentID: e.ExecutionID, RunID: f.runID, StepID: "execute", ActorID: "actor-a"}
		if err := c.executeAndSubmit(ctx, discardLogger(), req, staticProvider{outcome: "completed"}); err != nil {
			t.Fatalf("redelivered assignment for a completed, routed step = %v, want nil", err)
		}
	})
}

func assertRebindKept(t *testing.T, f *casFix, id string) {
	t.Helper()
	got := f.st.exec(id)
	if got.ActorID != "actor-b" || got.Status != domain.StepStatusAssigned {
		t.Fatalf("execution = %s/%q, want the rebind's assigned/actor-b", got.Status, got.ActorID)
	}
	if len(f.st.stepUpdates) != 0 || len(f.st.transitions) != 0 {
		t.Fatalf("writes after a rebind: %v %+v", f.st.stepUpdates, f.st.transitions)
	}
	if len(f.events(domain.EventStepCompleted)) != 0 || f.st.exec(f.runID+"-review-1") != nil {
		t.Fatal("a submit across a rebind was emitted or routed")
	}
}

// AC 5: a non-owner costs no git call — ownership is checked before
// TASK-005's head resolution and before validateRequiredOutputs.
func TestIngestResult_NonOwnerMakesNoGitCall(t *testing.T) {
	f := newCasFix(t, casWorkflow(), false)
	if err := f.orch.EnableMergeBinding(MergeBindingConfig{}); err != nil {
		t.Fatal(err)
	}
	f.git.setHead("spine/run/run-1", sha40('1'))
	e := f.addExec("execute", domain.StepStatusInProgress, "actor-a")
	for _, req := range []SubmitRequest{
		{ExecutionID: e.ExecutionID, OutcomeID: "completed", ActorID: "actor-b", ExpectedHeadSHA: sha40('1'),
			ArtifactsProduced: []string{"deliverable"}},
		{ExecutionID: e.ExecutionID, OutcomeID: "completed", ActorID: "actor-b"}, // missing output
	} {
		_, err := f.orch.IngestResult(context.Background(), req)
		requireCode(t, err, domain.ErrForbidden, "non-owner submit")
	}
	if n := len(f.git.calls); n != 0 {
		t.Fatalf("a non-owner submit made %d git calls: %v", n, f.git.calls)
	}
}

// AC 23: ReleaseStep compares the execution's actor, not only the
// assignment row's. Startup recovery reset assigned→waiting without
// clearing actor_id or the row, and AssignStep then bound actor-b.
func TestReleaseStep_OwnerGuard(t *testing.T) {
	for _, capability := range []bool{false, true} {
		t.Run(fmt.Sprintf("capability=%v", capability), func(t *testing.T) {
			f := newCasFix(t, casWorkflow(), capability)
			e := f.addExec("execute", domain.StepStatusAssigned, "actor-b")
			f.asg.assignments["claim-a"] = &domain.Assignment{AssignmentID: "claim-a", RunID: f.runID,
				ExecutionID: e.ExecutionID, ActorID: "actor-a", Status: domain.AssignmentStatusActive}
			err := f.orch.ReleaseStep(context.Background(), ReleaseRequest{ActorID: "actor-a", AssignmentID: "claim-a"})
			requireCode(t, err, domain.ErrForbidden, "stale claim releasing another actor's step")
			if got := f.st.exec(e.ExecutionID); got.Status != domain.StepStatusAssigned || got.ActorID != "actor-b" {
				t.Fatalf("execution = %s/%q, want assigned/actor-b", got.Status, got.ActorID)
			}
			if got := f.asg.assignments["claim-a"].Status; got != domain.AssignmentStatusActive {
				t.Fatalf("assignment row = %s, want active", got)
			}
		})
	}
	t.Run("the owner still releases", func(t *testing.T) {
		f := newCasFix(t, casWorkflow(), true)
		e := f.addExec("execute", domain.StepStatusAssigned, "actor-a")
		f.asg.assignments["claim-a"] = &domain.Assignment{AssignmentID: "claim-a", RunID: f.runID,
			ExecutionID: e.ExecutionID, ActorID: "actor-a", Status: domain.AssignmentStatusActive}
		if err := f.orch.ReleaseStep(context.Background(), ReleaseRequest{ActorID: "actor-a", AssignmentID: "claim-a"}); err != nil {
			t.Fatalf("ReleaseStep: %v", err)
		}
		if got := f.st.exec(e.ExecutionID); got.Status != domain.StepStatusWaiting || got.ActorID != "" {
			t.Fatalf("execution = %s/%q, want waiting/unbound", got.Status, got.ActorID)
		}
		if got := f.asg.assignments["claim-a"].Status; got != domain.AssignmentStatusCancelled {
			t.Fatalf("assignment row = %s, want cancelled", got)
		}
	})
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type staticProvider struct{ outcome string }

func (staticProvider) CanHandle(domain.ActorType) bool { return true }
func (p staticProvider) Execute(context.Context, actor.AssignmentRequest) (*actor.AssignmentResult, error) {
	return &actor.AssignmentResult{OutcomeID: p.outcome, ArtifactsProduced: []string{"deliverable"}}, nil
}
