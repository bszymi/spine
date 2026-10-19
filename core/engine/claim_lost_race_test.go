package engine

// INIT-023 TASK-008 review round 2 (codex): a claim that loses its
// conditional step write leaves no assignment row the step-entry policy
// would read as a binding.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bszymi/spine/core/domain"
)

// DeleteAssignment makes memAssignmentStore the engine's optional
// AssignmentRowDeleter, like *store.PostgresStore: the row goes only while
// it still names the execution and the actor.
func (s *memAssignmentStore) DeleteAssignment(_ context.Context, assignmentID, executionID, actorID string) error {
	if a, ok := s.assignments[assignmentID]; ok && a.ExecutionID == executionID && a.ActorID == actorID {
		delete(s.assignments, assignmentID)
	}
	return nil
}

var _ AssignmentRowDeleter = (*memAssignmentStore)(nil)

// undeletableAssignments keeps the separation-of-duties ledger but offers
// no AssignmentRowDeleter: the fallback an embedder's own store gets.
type undeletableAssignments struct{ inner *memAssignmentStore }

func (u undeletableAssignments) CreateAssignment(ctx context.Context, a *domain.Assignment) error {
	return u.inner.CreateAssignment(ctx, a)
}
func (u undeletableAssignments) UpdateAssignmentStatus(ctx context.Context, id string, st domain.AssignmentStatus, at *time.Time) error {
	return u.inner.UpdateAssignmentStatus(ctx, id, st, at)
}
func (u undeletableAssignments) GetAssignment(ctx context.Context, id string) (*domain.Assignment, error) {
	return u.inner.GetAssignment(ctx, id)
}
func (u undeletableAssignments) ListExpiredAssignments(ctx context.Context, before time.Time) ([]domain.Assignment, error) {
	return u.inner.ListExpiredAssignments(ctx, before)
}
func (u undeletableAssignments) ListAssignmentsByActor(ctx context.Context, actorID string, st *domain.AssignmentStatus) ([]domain.Assignment, error) {
	return u.inner.ListAssignmentsByActor(ctx, actorID, st)
}

// failingDeleteAssignments offers the deleter, but every delete fails.
type failingDeleteAssignments struct{ *memAssignmentStore }

func (failingDeleteAssignments) DeleteAssignment(context.Context, string, string, string) error {
	return errors.New("assignment store unreachable")
}

// TestClaimStep_LostRaceLeavesNoLedgerRow: ClaimStep creates its
// claim-<execution>-<actor> row before the conditional step write. When
// that write loses, the claim never bound its actor, so its row must not
// survive where the step-entry policy reads bindings:
//
//   - the separation-of-duties author ledger (runAuthor counts claim rows
//     in every status) must not make the lost claimant a run author;
//   - the prior-holder re-offer (releasedSince reads a
//     claim-<execution>-<holder> row in any status as "held and
//     released") must not lose its protection to the holder's own lost
//     claim, and the holder must be able to claim again.
//
// Mutation: cancelling the row instead of deleting it (the code before
// review round 2) fails the first two sub-tests.
func TestClaimStep_LostRaceLeavesNoLedgerRow(t *testing.T) {
	ctx := context.Background()

	// loseExecuteClaim: actor-a's claim of the waiting execute step loses
	// to an operator assign of actor-x (which writes no assignment row);
	// then actor-x completes execute and the review step waits.
	loseExecuteClaim := func(t *testing.T, f *casFix) (execID, reviewID string) {
		t.Helper()
		e := f.addExec("execute", domain.StepStatusWaiting, "")
		f.st.afterGetExec = func(id string, n int) {
			if id == e.ExecutionID && n == 1 {
				f.st.mutateExec(id, func(x *domain.StepExecution) {
					x.Status, x.ActorID = domain.StepStatusAssigned, "actor-x"
				})
			}
		}
		_, err := f.orch.ClaimStep(ctx, ClaimRequest{ActorID: "actor-a", ExecutionID: e.ExecutionID})
		if !errors.Is(err, ErrStepStateChanged) {
			t.Fatalf("lost claim: err = %v, want ErrStepStateChanged", err)
		}
		f.st.afterGetExec = nil
		if got := f.st.exec(e.ExecutionID).ActorID; got != "actor-x" {
			t.Fatalf("lost claim overwrote the assign: %q", got)
		}
		if len(f.events(domain.EventStepAssigned)) != 0 {
			t.Fatal("a lost claim emitted step_assigned")
		}
		done := time.Now()
		f.st.mutateExec(e.ExecutionID, func(x *domain.StepExecution) {
			x.Status, x.OutcomeID, x.CompletedAt = domain.StepStatusCompleted, "completed", &done
		})
		r := f.addExec("review", domain.StepStatusWaiting, "")
		return e.ExecutionID, r.ExecutionID
	}
	claimRow := func(f *casFix, execID, actorID string) *domain.Assignment {
		return f.asg.assignments["claim-"+execID+"-"+actorID]
	}

	t.Run("separation of duties: the lost claimant is no run author", func(t *testing.T) {
		f := newCasFix(t, casWorkflow(), true)
		f.orch.WithStepEntryPolicy(StepEntryPolicy{SeparationOfDuties: true})
		execID, reviewID := loseExecuteClaim(t, f)
		if row := claimRow(f, execID, "actor-a"); row != nil {
			t.Fatalf("the lost claim's assignment row survived: %+v", row)
		}
		// Control: the actor that did execute is refused on the review.
		_, err := f.orch.ClaimStep(ctx, ClaimRequest{ActorID: "actor-x", ExecutionID: reviewID})
		requireCode(t, err, domain.ErrForbidden, "the executing actor's review claim")
		if _, err := f.orch.ClaimStep(ctx, ClaimRequest{ActorID: "actor-a", ExecutionID: reviewID}); err != nil {
			t.Fatalf("the lost claimant was refused as a reviewer of a run it never wrote: %v", err)
		}
		if got := f.st.exec(reviewID).ActorID; got != "actor-a" {
			t.Fatalf("review bound to %q, want actor-a", got)
		}
	})

	t.Run("prior-holder re-offer survives the holder's lost claim", func(t *testing.T) {
		f := newCasFix(t, casWorkflow(), true)
		f.orch.WithStepEntryPolicy(StepEntryPolicy{ReofferToPriorHolder: true})
		f.addExec("execute", domain.StepStatusCompleted, "actor-p")
		f.addExec("review", domain.StepStatusCompleted, "actor-r")
		rework := f.addExec("execute", domain.StepStatusWaiting, "") // re-offered to actor-p
		f.st.afterGetExec = func(id string, n int) {
			if id == rework.ExecutionID && n == 1 { // an activation opened the slot
				f.st.mutateExec(id, func(x *domain.StepExecution) {
					x.Status, x.ActorID = domain.StepStatusAssigned, ""
				})
			}
		}
		_, err := f.orch.ClaimStep(ctx, ClaimRequest{ActorID: "actor-p", ExecutionID: rework.ExecutionID})
		if !errors.Is(err, ErrStepStateChanged) {
			t.Fatalf("the holder's lost claim: err = %v, want ErrStepStateChanged", err)
		}
		f.st.afterGetExec = nil
		if row := claimRow(f, rework.ExecutionID, "actor-p"); row != nil {
			t.Fatalf("the lost claim's assignment row survived: %+v", row)
		}
		_, err = f.orch.ClaimStep(ctx, ClaimRequest{ActorID: "actor-q", ExecutionID: rework.ExecutionID})
		requireCode(t, err, domain.ErrForbidden, "another actor's claim of the re-offered rework")
		if got := f.st.exec(rework.ExecutionID); got.Status != domain.StepStatusAssigned || got.ActorID != "" {
			t.Fatalf("rework = (%s, %q) after the refused claim, want the open slot", got.Status, got.ActorID)
		}
		if _, err := f.orch.ClaimStep(ctx, ClaimRequest{ActorID: "actor-p", ExecutionID: rework.ExecutionID}); err != nil {
			t.Fatalf("the prior holder's next claim: %v", err)
		}
		row := claimRow(f, rework.ExecutionID, "actor-p")
		if row == nil || row.Status != domain.AssignmentStatusActive || f.st.exec(rework.ExecutionID).ActorID != "actor-p" {
			t.Fatalf("after the holder's claim: row %+v, actor %q", row, f.st.exec(rework.ExecutionID).ActorID)
		}
	})

	// Documented residual (KNOWN-LIMITATIONS "Engine State Writes …"): an
	// assignment store without the port keeps the row, cancelled, so it
	// blocks no claim; the lost claimant then reads as a run author.
	t.Run("without the port the row is cancelled", func(t *testing.T) {
		f := newCasFix(t, casWorkflow(), true)
		f.orch.WithStepEntryPolicy(StepEntryPolicy{SeparationOfDuties: true})
		f.orch.assignments = undeletableAssignments{inner: f.asg}
		execID, reviewID := loseExecuteClaim(t, f)
		row := claimRow(f, execID, "actor-a")
		if row == nil || row.Status != domain.AssignmentStatusCancelled || row.RespondedAt == nil {
			t.Fatalf("the lost claim's row = %+v, want cancelled with responded_at", row)
		}
		_, err := f.orch.ClaimStep(ctx, ClaimRequest{ActorID: "actor-a", ExecutionID: reviewID})
		requireCode(t, err, domain.ErrForbidden, "the lost claimant's review claim without the port (residual)")
	})

	t.Run("a failing delete falls back to cancelling the row", func(t *testing.T) {
		f := newCasFix(t, casWorkflow(), true)
		f.orch.assignments = failingDeleteAssignments{f.asg}
		execID, _ := loseExecuteClaim(t, f)
		row := claimRow(f, execID, "actor-a")
		if row == nil || row.Status != domain.AssignmentStatusCancelled {
			t.Fatalf("the lost claim's row = %+v, want cancelled (never left active)", row)
		}
	})
}
