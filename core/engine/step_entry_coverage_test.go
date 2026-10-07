package engine

// INIT-023 TASK-007 review round 1 — coverage for step-entry branches no
// test pinned: each test names the mutation the reviewers showed survived
// and that now turns it red.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/core/domain"
)

// ── Finding: ClaimStep's existence + active-status requirement ──

// TestStepEntry_ClaimRequiresLiveActor: under EnforceEligibility ClaimStep
// gains check 4 (Design §6) — the gap SMP works around at executor_gates.go.
// ClaimStep's own checks look no actor up on a step without an execution
// block and never read the status, so check 4 is the only gate in these
// rows.
// Mutations: `op == entryAssign && a.Status != active` fails the suspended
// and deactivated rows; `p.EnforceEligibility && op == entryAssign` fails
// every row.
func TestStepEntry_ClaimRequiresLiveActor(t *testing.T) {
	ctx := context.Background()
	wf := &domain.WorkflowDefinition{ID: "wf", EntryStep: "plain", Steps: []domain.StepDefinition{
		{ID: "plain", Name: "Plain", Type: domain.StepTypeManual,
			Outcomes: []domain.OutcomeDefinition{{ID: "done", Name: "Done", NextStep: "end"}}},
		{ID: "typed", Name: "Typed", Type: domain.StepTypeManual,
			Execution: &domain.ExecutionConfig{Mode: domain.ExecModeHybrid},
			Outcomes:  []domain.OutcomeDefinition{{ID: "done", Name: "Done", NextStep: "end"}}},
	}}
	setup := func(p StepEntryPolicy, stepID string) (*mockRunStore, *memAssignmentStore, *Orchestrator) {
		st := &mockRunStore{runs: map[string]*domain.Run{"run-1": runFixture("run-1", domain.RunStatusActive)}}
		st.createdSteps = []*domain.StepExecution{sodExec("run-1-"+stepID+"-1", stepID, domain.StepStatusWaiting, "")}
		bs := newClaimBlockingStore()
		bs.actors["sam-suspended"] = &domain.Actor{ActorID: "sam-suspended", Type: domain.ActorTypeHuman, Status: domain.ActorStatusSuspended}
		bs.actors["dee-deactivated"] = &domain.Actor{ActorID: "dee-deactivated", Type: domain.ActorTypeHuman, Status: domain.ActorStatusDeactivated}
		as := newMemAssignmentStore()
		o := &Orchestrator{store: st, wfLoader: &claimTestWFLoader{wf: wf}, events: &mockEventEmitter{},
			git: &stubGitOperator{}, actors: &stubActorAssigner{}, blocking: bs, artifacts: &stubArtifactReader{},
			assignments: as}
		o.WithStepEntryPolicy(p)
		return st, as, o
	}
	rows := []struct {
		name, step, actor string
		want              domain.ErrorCode
		msg               string
	}{
		{"suspended actor, step without an execution block", "plain", "sam-suspended", domain.ErrForbidden, "suspended"},
		{"suspended actor, step with an execution block", "typed", "sam-suspended", domain.ErrForbidden, "suspended"},
		{"deactivated actor", "plain", "dee-deactivated", domain.ErrForbidden, "deactivated"},
		{"unknown actor, step without an execution block", "plain", "no-such-actor", domain.ErrNotFound, "no-such-actor"},
	}
	for _, r := range rows {
		r := r
		t.Run(r.name, func(t *testing.T) {
			st, as, o := setup(StepEntryPolicy{EnforceEligibility: true}, r.step)
			before := snapshotExecs(st)
			_, err := o.ClaimStep(ctx, ClaimRequest{ActorID: r.actor, ExecutionID: "run-1-" + r.step + "-1"})
			requireCode(t, err, r.want, "ClaimStep("+r.actor+")")
			if !strings.Contains(err.Error(), r.msg) {
				t.Fatalf("refusal %q must mention %q", err.Error(), r.msg)
			}
			requireExecsUnchanged(t, st, before, "refused claim")
			if len(as.assignments) != 0 {
				t.Fatalf("a refused claim must not create an assignment row")
			}
		})
	}
	t.Run("zero value lets them claim a step without an execution block, as today", func(t *testing.T) {
		for _, actor := range []string{"sam-suspended", "no-such-actor"} {
			_, _, o := setup(StepEntryPolicy{}, "plain")
			if _, err := o.ClaimStep(ctx, ClaimRequest{ActorID: actor, ExecutionID: "run-1-plain-1"}); err != nil {
				t.Fatalf("zero value: ClaimStep(%s) = %v, want the pre-INIT-023 success", actor, err)
			}
		}
	})
}

// ── Finding: RequireSkillsOnAssign fails closed (Decision 10) ──

// noSkillsBlocking is a blocking store that cannot list skills.
type noSkillsBlocking struct{ *fakeBlockingStore }

// failingSkillsBlocking lists skills with an error.
type failingSkillsBlocking struct{ *claimBlockingStore }

func (failingSkillsBlocking) ListActorSkills(context.Context, string) ([]domain.Skill, error) {
	return nil, errors.New("skill directory unreachable")
}

// TestStepEntry_RequireSkillsOnAssignFailsClosed: a switch that asks for
// skill enforcement must not silently do nothing — no skill lookup wired,
// or a failing one, answers 503 with nothing written (unlike ClaimStep's
// silent skip, which is left alone).
// Mutation: turning either branch of requireSkillsOnAssign into
// `return nil` fails the matching row.
func TestStepEntry_RequireSkillsOnAssignFailsClosed(t *testing.T) {
	ctx := context.Background()
	wf := &domain.WorkflowDefinition{ID: "wf", EntryStep: "skilled", Steps: []domain.StepDefinition{{
		ID: "skilled", Name: "Skilled", Type: domain.StepTypeManual,
		Execution: &domain.ExecutionConfig{Mode: domain.ExecModeHybrid, RequiredSkills: []string{"review"}},
		Outcomes:  []domain.OutcomeDefinition{{ID: "done", Name: "Done", NextStep: "end"}},
	}}}
	withSkill := newClaimBlockingStore()
	withSkill.skills["alice"] = skillSet("review")
	failing := failingSkillsBlocking{claimBlockingStore: newClaimBlockingStore()}
	rows := []struct {
		name     string
		blocking BlockingStore
		want     domain.ErrorCode
		msg      string
	}{
		{"no skill lookup wired", noSkillsBlocking{newFakeBlockingStore()}, domain.ErrUnavailable, "no skill lookup"},
		{"skill lookup fails", failing, domain.ErrUnavailable, "skill lookup failed"},
		{"skill held (control)", withSkill, "", ""},
	}
	for _, r := range rows {
		r := r
		t.Run(r.name, func(t *testing.T) {
			st := &writeCountingRunStore{mockRunStore: &mockRunStore{runs: map[string]*domain.Run{"run-1": runFixture("run-1", domain.RunStatusActive)}}}
			st.createdSteps = []*domain.StepExecution{sodExec("run-1-skilled-1", "skilled", domain.StepStatusWaiting, "")}
			o := &Orchestrator{store: st, wfLoader: &claimTestWFLoader{wf: wf}, events: &mockEventEmitter{},
				git: &stubGitOperator{}, actors: &stubActorAssigner{}, blocking: r.blocking, artifacts: &stubArtifactReader{}}
			o.WithStepEntryPolicy(StepEntryPolicy{RequireSkillsOnAssign: true})
			before := snapshotExecs(st.mockRunStore)
			_, err := o.AssignStep(ctx, AssignRequest{RunID: "run-1", StepID: "skilled", ActorID: "alice"})
			if r.want == "" {
				if err != nil {
					t.Fatalf("control: %v", err)
				}
				return
			}
			requireCode(t, err, r.want, "AssignStep with "+r.name)
			if !strings.Contains(err.Error(), r.msg) {
				t.Fatalf("refusal %q must mention %q", err.Error(), r.msg)
			}
			requireExecsUnchanged(t, st.mockRunStore, before, "refused assign")
			if st.stepWrites != 0 {
				t.Fatalf("refused assign wrote %d executions", st.stepWrites)
			}
		})
	}
}

// ── Finding: SoD fail-closed anomalies (Design §7, Deviation 5) ──

// TestStepEntry_SoDFailClosedAnomalies pins the fail-closed branches of the
// author ledger and the symmetric guard that cannot occur with a pinned
// workflow but must never fail open.
// Mutations: `sd == nil || …` in runAuthor's step filter fails the first
// row; dropping the unknown-execution ledger branch fails the second;
// `sd != nil && sd.Type == review` in the symmetric guard fails the third.
func TestStepEntry_SoDFailClosedAnomalies(t *testing.T) {
	sod := StepEntryPolicy{SeparationOfDuties: true}
	ctx := context.Background()
	bindBoth := func(t *testing.T, f sodFixture, stepID, execID, actorID string) []error {
		t.Helper()
		before := snapshotExecs(f.st)
		_, aErr := f.orch.AssignStep(ctx, AssignRequest{RunID: "run-1", StepID: stepID, ActorID: actorID})
		_, cErr := f.orch.ClaimStep(ctx, ClaimRequest{ActorID: actorID, ExecutionID: execID})
		requireExecsUnchanged(t, f.st, before, "refused binds")
		return []error{aErr, cErr}
	}

	t.Run("execution of a step missing from the pinned workflow counts as authorship", func(t *testing.T) {
		f := newSoDFixture(sod,
			sodExec("run-1-ghost-step-1", "ghost-step", domain.StepStatusCompleted, "ghost-author"),
			sodExec("run-1-review-1", "review", domain.StepStatusWaiting, ""))
		for _, err := range bindBoth(t, f, "review", "run-1-review-1", "ghost-author") {
			requireCode(t, err, domain.ErrForbidden, "review bind of an unknown-step executor")
			if !strings.Contains(err.Error(), `"ghost-step"`) {
				t.Fatalf("refusal %q must name the unknown step", err.Error())
			}
		}
	})
	t.Run("ledger row of an execution the run does not list counts as authorship", func(t *testing.T) {
		f := newSoDFixture(sod,
			sodExec("run-1-execute-1", "execute", domain.StepStatusCompleted, "member-1"),
			sodExec("run-1-review-1", "review", domain.StepStatusWaiting, ""))
		f.as.assignments["claim-run-1-vanished-1-phantom"] = &domain.Assignment{AssignmentID: "claim-run-1-vanished-1-phantom",
			RunID: "run-1", ExecutionID: "run-1-vanished-1", ActorID: "phantom",
			Status: domain.AssignmentStatusCompleted, AssignedAt: time.Now()}
		for _, err := range bindBoth(t, f, "review", "run-1-review-1", "phantom") {
			requireCode(t, err, domain.ErrForbidden, "review bind of an actor with an orphan ledger row")
			if !strings.Contains(err.Error(), "run-1-vanished-1") {
				t.Fatalf("refusal %q must name the unknown execution", err.Error())
			}
		}
		// Control: the same row in another run is not authorship here.
		f = newSoDFixture(sod,
			sodExec("run-1-execute-1", "execute", domain.StepStatusCompleted, "member-1"),
			sodExec("run-1-review-1", "review", domain.StepStatusWaiting, ""))
		f.as.assignments["claim-run-2-vanished-1-phantom"] = &domain.Assignment{AssignmentID: "claim-run-2-vanished-1-phantom",
			RunID: "run-2", ExecutionID: "run-2-vanished-1", ActorID: "phantom",
			Status: domain.AssignmentStatusCompleted, AssignedAt: time.Now()}
		if _, err := f.orch.AssignStep(ctx, AssignRequest{RunID: "run-1", StepID: "review", ActorID: "phantom"}); err != nil {
			t.Fatalf("another run's ledger row must not count: %v", err)
		}
	})
	t.Run("live execution of a step missing from the pinned workflow blocks author steps like a review", func(t *testing.T) {
		f := newSoDFixture(sod,
			sodExec("run-1-execute-1", "execute", domain.StepStatusCompleted, "member-1"),
			sodExec("run-1-ghost-step-1", "ghost-step", domain.StepStatusInProgress, "erin"),
			&domain.StepExecution{ExecutionID: "run-1-b1-branch-work-1", RunID: "run-1", StepID: "branch-work",
				BranchID: "b1", Status: domain.StepStatusWaiting, Attempt: 1, CreatedAt: time.Now()})
		for _, err := range bindBoth(t, f, "branch-work", "run-1-b1-branch-work-1", "erin") {
			requireCode(t, err, domain.ErrForbidden, "author-step bind of an actor holding an unknown-step execution")
			if !strings.Contains(err.Error(), `"ghost-step"`) {
				t.Fatalf("refusal %q must name the unknown step", err.Error())
			}
		}
	})
}
