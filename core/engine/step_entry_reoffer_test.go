package engine

// INIT-023 TASK-014 owner question 7: the claim-takeover residual of audit
// P3-6 is closed by an opt-in step-entry rule — a waiting author step that
// was held before in its run is re-offered only to that prior holder unless
// an operator assigns it (StepEntryPolicy.ReofferToPriorHolder, on in
// StrictStepEntryPolicy, off in the zero value).

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/core/domain"
)

func reofferExec(id, stepID string, attempt int, status domain.StepExecutionStatus, actorID string) *domain.StepExecution {
	return &domain.StepExecution{ExecutionID: id, RunID: "run-1", StepID: stepID, Status: status,
		ActorID: actorID, Attempt: attempt, CreatedAt: time.Now()}
}

// reworkExecs: member-1 executed attempt 1, erin sent it back, attempt 2 of
// execute waits with no actor (what advanceToNextStep creates).
func reworkExecs() []*domain.StepExecution {
	rev := reofferExec("run-1-review-1", "review", 1, domain.StepStatusCompleted, "erin")
	rev.OutcomeID = "needs_rework"
	return []*domain.StepExecution{
		reofferExec("run-1-execute-1", "execute", 1, domain.StepStatusCompleted, "member-1"),
		rev,
		reofferExec("run-1-execute-2", "execute", 2, domain.StepStatusWaiting, ""),
	}
}

func TestStepEntry_ReofferToPriorHolder(t *testing.T) {
	ctx := context.Background()
	on := StepEntryPolicy{ReofferToPriorHolder: true}

	claim := func(t *testing.T, f sodFixture, execID, actorID string) error {
		t.Helper()
		before := snapshotExecs(f.st)
		_, err := f.orch.ClaimStep(ctx, ClaimRequest{ActorID: actorID, ExecutionID: execID})
		if err != nil {
			requireExecsUnchanged(t, f.st, before, "refused claim")
		}
		return err
	}

	t.Run("rework is re-offered to the prior executor only", func(t *testing.T) {
		f := newSoDFixture(on, reworkExecs()...)
		err := claim(t, f, "run-1-execute-2", "mallory")
		requireCode(t, err, domain.ErrForbidden, "takeover of a rework step")
		if strings.Contains(err.Error(), "member-1") {
			t.Fatalf("refusal names the prior holder: %v", err)
		}
		if !strings.Contains(err.Error(), "/runs/run-1/steps/execute/assign") {
			t.Fatalf("refusal should point at the operator assign: %v", err)
		}
		if err := claim(t, f, "run-1-execute-2", "member-1"); err != nil {
			t.Fatalf("prior executor re-claims the rework: %v", err)
		}
	})
	t.Run("the reviewer who sent the work back is not the prior holder", func(t *testing.T) {
		f := newSoDFixture(on, reworkExecs()...)
		requireCode(t, claim(t, f, "run-1-execute-2", "erin"), domain.ErrForbidden, "reviewer takeover of the rework")
	})
	t.Run("an operator assign is never restricted", func(t *testing.T) {
		f := newSoDFixture(on, reworkExecs()...)
		if _, err := f.orch.AssignStep(ctx, AssignRequest{RunID: "run-1", StepID: "execute", ActorID: "member-2"}); err != nil {
			t.Fatalf("operator assign of the rework to another actor: %v", err)
		}
		if e := execByID(f.st, "run-1-execute-2"); e.ActorID != "member-2" {
			t.Fatalf("assign bound %q", e.ActorID)
		}
	})
	t.Run("restart recovery keeps the holder on the waiting row", func(t *testing.T) {
		f := newSoDFixture(on, reofferExec("run-1-execute-1", "execute", 1, domain.StepStatusWaiting, "member-1"))
		requireCode(t, claim(t, f, "run-1-execute-1", "mallory"), domain.ErrForbidden, "takeover after restart recovery")
		if err := claim(t, f, "run-1-execute-1", "member-1"); err != nil {
			t.Fatalf("holder re-claims after restart recovery: %v", err)
		}
	})
	t.Run("a step never held in the run is open to any eligible actor", func(t *testing.T) {
		f := newSoDFixture(on, reofferExec("run-1-execute-1", "execute", 1, domain.StepStatusWaiting, ""))
		if err := claim(t, f, "run-1-execute-1", "member-2"); err != nil {
			t.Fatalf("first claim: %v", err)
		}
	})
	t.Run("a retry of a failed attempt is not re-offered", func(t *testing.T) {
		f := newSoDFixture(on,
			reofferExec("run-1-execute-1", "execute", 1, domain.StepStatusFailed, "member-1"),
			reofferExec("run-1-execute-2", "execute", 2, domain.StepStatusWaiting, ""))
		if err := claim(t, f, "run-1-execute-2", "member-2"); err != nil {
			t.Fatalf("retry claim by another actor: %v", err)
		}
	})
	t.Run("a retry after a failed rework attempt is not re-offered to an older holder", func(t *testing.T) {
		// codex review round 1: attempt 1 completed, rework attempt 2
		// failed, RetryStep created attempt 3. The latest preceding attempt
		// (2) failed, so nobody is re-offered the retry.
		f := newSoDFixture(on,
			reofferExec("run-1-execute-1", "execute", 1, domain.StepStatusCompleted, "member-1"),
			reofferExec("run-1-execute-2", "execute", 2, domain.StepStatusFailed, "member-2"),
			reofferExec("run-1-execute-3", "execute", 3, domain.StepStatusWaiting, ""))
		if err := claim(t, f, "run-1-execute-3", "member-3"); err != nil {
			t.Fatalf("retry claim by a third actor: %v", err)
		}
	})
	t.Run("the latest completed attempt wins", func(t *testing.T) {
		f := newSoDFixture(on,
			reofferExec("run-1-execute-1", "execute", 1, domain.StepStatusCompleted, "member-1"),
			reofferExec("run-1-execute-2", "execute", 2, domain.StepStatusCompleted, "member-2"),
			reofferExec("run-1-execute-3", "execute", 3, domain.StepStatusWaiting, ""))
		requireCode(t, claim(t, f, "run-1-execute-3", "member-1"), domain.ErrForbidden, "an older holder")
		if err := claim(t, f, "run-1-execute-3", "member-2"); err != nil {
			t.Fatalf("latest holder: %v", err)
		}
	})
	t.Run("review steps are not re-offered", func(t *testing.T) {
		f := newSoDFixture(on,
			reofferExec("run-1-review-1", "review", 1, domain.StepStatusCompleted, "erin"),
			reofferExec("run-1-review-2", "review", 2, domain.StepStatusWaiting, ""))
		if err := claim(t, f, "run-1-review-2", "frank"); err != nil {
			t.Fatalf("re-review by another reviewer: %v", err)
		}
	})
	t.Run("another branch's attempts do not count", func(t *testing.T) {
		b1 := reofferExec("run-1-b1-branch-work-1", "branch-work", 1, domain.StepStatusCompleted, "member-1")
		b1.BranchID = "b1"
		b2 := reofferExec("run-1-b2-branch-work-2", "branch-work", 2, domain.StepStatusWaiting, "")
		b2.BranchID = "b2"
		f := newSoDFixture(on, b1, b2)
		if err := claim(t, f, "run-1-b2-branch-work-2", "member-2"); err != nil {
			t.Fatalf("claim on another branch: %v", err)
		}
	})
	t.Run("a released rework attempt is handed off, not re-offered", func(t *testing.T) {
		// codex review round 2: claim → release → claim by another actor
		// on attempt 2. ReleaseStep blanks the row's actor; the cancelled
		// claim row shows the attempt was held and released.
		f := newSoDFixture(on, reworkExecs()...)
		res, err := f.orch.ClaimStep(ctx, ClaimRequest{ActorID: "member-1", ExecutionID: "run-1-execute-2"})
		if err != nil {
			t.Fatalf("prior holder claims the rework: %v", err)
		}
		if err := f.orch.ReleaseStep(ctx, ReleaseRequest{ActorID: "member-1", AssignmentID: res.Assignment.AssignmentID, Reason: "hand-off"}); err != nil {
			t.Fatalf("release: %v", err)
		}
		if e := execByID(f.st, "run-1-execute-2"); e.Status != domain.StepStatusWaiting || e.ActorID != "" {
			t.Fatalf("after release: %s %q", e.Status, e.ActorID)
		}
		if err := claim(t, f, "run-1-execute-2", "member-2"); err != nil {
			t.Fatalf("another actor claims the released rework: %v", err)
		}
	})
	t.Run("an unreadable assignment ledger fails closed", func(t *testing.T) {
		f := newSoDFixture(on, reworkExecs()...)
		f.orch.assignments = failingGetAssignments{f.as}
		requireCode(t, claim(t, f, "run-1-execute-2", "member-2"), domain.ErrUnavailable, "ledger failure")
	})
	t.Run("an unreadable execution list fails closed", func(t *testing.T) {
		f := newSoDFixture(on, reworkExecs()...)
		f.orch.store = &listFailingRunStore{mockRunStore: f.st}
		requireCode(t, claim(t, f, "run-1-execute-2", "member-1"), domain.ErrUnavailable, "list failure")
	})
	t.Run("zero value: anyone eligible claims the rework, as before", func(t *testing.T) {
		f := newSoDFixture(StepEntryPolicy{}, reworkExecs()...)
		if err := claim(t, f, "run-1-execute-2", "mallory"); err != nil {
			t.Fatalf("zero value: %v", err)
		}
	})
}

// failingGetAssignments fails GetAssignment only.
type failingGetAssignments struct{ *memAssignmentStore }

func (f failingGetAssignments) GetAssignment(context.Context, string) (*domain.Assignment, error) {
	return nil, errors.New("ledger down")
}

// listFailingRunStore fails ListStepExecutionsByRun only.
type listFailingRunStore struct{ *mockRunStore }

func (l *listFailingRunStore) ListStepExecutionsByRun(context.Context, string) ([]domain.StepExecution, error) {
	return nil, errors.New("db down")
}

func TestStepEntry_StrictPolicyReoffersToPriorHolder(t *testing.T) {
	p := StrictStepEntryPolicy()
	if !p.ReofferToPriorHolder {
		t.Fatal("StrictStepEntryPolicy (the spine serve default) must enable ReofferToPriorHolder")
	}
	if (StepEntryPolicy{}).ReofferToPriorHolder || !(StepEntryPolicy{ReofferToPriorHolder: true}).gatesBinding() {
		t.Fatal("zero value must be off, and the field must gate binding")
	}
	if (StepEntryPolicy{ReofferToPriorHolder: true}).IsZero() {
		t.Fatal("a policy with only ReofferToPriorHolder is not the zero value")
	}
}

// The candidate listing hides a waiting rework from everyone but its prior
// holder, so a poller does not claim, get 403 and repeat every cycle.
func TestStepEntry_CandidateListingHidesReofferedRework(t *testing.T) {
	ctx := context.Background()
	newOrch := func(p StepEntryPolicy) *Orchestrator {
		st := &mockRunStore{runs: map[string]*domain.Run{"run-1": runFixture("run-1", domain.RunStatusActive)}}
		bs := &stepListingBlockingStore{runs: st.runs}
		for _, e := range reworkExecs() {
			cp := *e
			st.createdSteps = append(st.createdSteps, &cp)
			if !cp.Status.IsTerminal() {
				bs.execs = append(bs.execs, cp)
			}
		}
		o := newStepListingOrch(bs, sodWorkflow())
		o.store = st
		o.WithStepEntryPolicy(p)
		return o
	}
	for _, c := range []struct {
		policy StepEntryPolicy
		actor  string
		want   []string
	}{
		{StepEntryPolicy{ReofferToPriorHolder: true}, "member-1", []string{"run-1-execute-2"}},
		{StepEntryPolicy{ReofferToPriorHolder: true}, "mallory", nil},
		{StepEntryPolicy{}, "mallory", []string{"run-1-execute-2"}},
	} {
		items, err := newOrch(c.policy).ListStepExecutions(ctx, StepExecutionQuery{ActorID: c.actor, Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, it := range items {
			got = append(got, it.ExecutionID)
		}
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Fatalf("policy %+v, actor %s: listing %v, want %v", c.policy, c.actor, got, c.want)
		}
	}
}
