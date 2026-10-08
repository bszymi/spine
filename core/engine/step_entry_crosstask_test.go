package engine

// INIT-023 wave 2 cross-task review (TASK-007 x TASK-016): separation of
// duties and run-branch push admission must agree on which step types make
// the bound actor an author. Push admission lets the bound actor of every
// step type except review and internal (refadmit.FrozenStepType) push
// commits to the run branch, so every such execution must make its actor a
// run author, or the actor pushes and then reviews their own commits — the
// one-actor F3 chain separation of duties exists to close.

import (
	"context"
	"testing"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/refadmit"
)

// crossTaskWorkflow is execute (manual) → evaluate (evalType) → review,
// the shape of the documented divergence/convergence example (evaluate is
// a convergence point's evaluation_step).
func crossTaskWorkflow(evalType domain.StepType) *domain.WorkflowDefinition {
	return &domain.WorkflowDefinition{
		ID: "sod-cross", EntryStep: "execute",
		Steps: []domain.StepDefinition{
			{ID: "execute", Name: "Execute", Type: domain.StepTypeManual,
				Outcomes: []domain.OutcomeDefinition{{ID: "completed", Name: "Done", NextStep: "evaluate"}}},
			{ID: "evaluate", Name: "Evaluate", Type: evalType,
				Outcomes: []domain.OutcomeDefinition{{ID: "selected", Name: "Selected", NextStep: "review"}}},
			{ID: "review", Name: "Review", Type: domain.StepTypeReview,
				Outcomes: []domain.OutcomeDefinition{{ID: "accepted", Name: "Accepted", NextStep: "end"}}},
		},
	}
}

// crossTaskStepTypes is every declared step type plus the two a projected
// workflow can carry without passing workflow.Validate: none and unknown.
var crossTaskStepTypes = []domain.StepType{
	domain.StepTypeManual, domain.StepTypeAutomated, domain.StepTypeConvergence,
	"", "weird", domain.StepTypeReview, domain.StepTypeInternal,
}

// pushDecision asks TASK-016's evaluator, over the fixture's store and the
// orchestrator's pinned-workflow lookup (the step-definition source
// `spine serve` wires), whether actorID may push the run branch now.
func pushDecision(t *testing.T, f sodFixture, actorID string) refadmit.Decision {
	t.Helper()
	d, err := refadmit.NewEvaluator(f.st).WithStepDefs(f.orch).Decide(context.Background(), refadmit.Request{
		Ref:   "refs/heads/spine/run/run-1",
		Actor: &domain.Actor{ActorID: actorID, Type: domain.ActorTypeHuman, Role: domain.RoleContributor, Status: domain.ActorStatusActive},
	})
	if err != nil {
		t.Fatalf("push decision: %v", err)
	}
	return d
}

// Mutation: restoring the manual/automated-only author predicate fails the
// convergence, "" and "weird" cases of both sub-tests.
func TestStepEntry_SoDAuthorshipCoversEveryPushableStepType(t *testing.T) {
	ctx := context.Background()
	sod := StepEntryPolicy{SeparationOfDuties: true}

	for _, typ := range crossTaskStepTypes {
		typ := typ
		t.Run("bound evaluator of type "+string(typ)+" reviewing", func(t *testing.T) {
			f := newSoDFixture(sod,
				sodExec("run-1-execute-1", "execute", domain.StepStatusCompleted, "carol"),
				sodExec("run-1-evaluate-1", "evaluate", domain.StepStatusInProgress, "rita"))
			f.orch.wfLoader = &claimTestWFLoader{wf: crossTaskWorkflow(typ)}
			f.st.runs["run-1"].CurrentStepID = "evaluate"

			// While bound to evaluate, may rita push the run branch?
			d := pushDecision(t, f, "rita")
			if want := !refadmit.FrozenStepType(typ); d.Allow != want {
				t.Fatalf("push admission for a bound %q step: allow=%v reason=%s, want allow=%v", typ, d.Allow, d.Reason, want)
			}

			// She submits evaluate and the review becomes claimable.
			execByID(f.st, "run-1-evaluate-1").Status = domain.StepStatusCompleted
			f.st.createdSteps = append(f.st.createdSteps, sodExec("run-1-review-1", "review", domain.StepStatusWaiting, ""))

			before := snapshotExecs(f.st)
			_, aErr := f.orch.AssignStep(ctx, AssignRequest{RunID: "run-1", StepID: "review", ActorID: "rita"})
			if aErr == nil {
				e := execByID(f.st, "run-1-review-1")
				e.Status, e.ActorID = domain.StepStatusWaiting, ""
			}
			_, cErr := f.orch.ClaimStep(ctx, ClaimRequest{ActorID: "rita", ExecutionID: "run-1-review-1"})

			if d.Allow {
				// She could have pushed commits: she is an author.
				requireCode(t, aErr, domain.ErrForbidden, "assign of a push-capable evaluator to the review")
				requireCode(t, cErr, domain.ErrForbidden, "claim of a push-capable evaluator to the review")
				requireExecsUnchanged(t, f.st, before, "refused review bind")
				return
			}
			if aErr != nil || cErr != nil {
				t.Fatalf("an actor who could not push (frozen %q step) may review: assign=%v claim=%v", typ, aErr, cErr)
			}
		})
	}

	// The symmetric guard: a live reviewer cannot take a step whose bound
	// actor push admission would let write the run branch.
	for _, typ := range []domain.StepType{domain.StepTypeConvergence, "", "weird"} {
		typ := typ
		t.Run("live reviewer taking a "+string(typ)+" step", func(t *testing.T) {
			f := newSoDFixture(sod,
				sodExec("run-1-execute-1", "execute", domain.StepStatusCompleted, "carol"),
				sodExec("run-1-review-1", "review", domain.StepStatusInProgress, "rita"),
				sodExec("run-1-evaluate-1", "evaluate", domain.StepStatusWaiting, ""))
			f.orch.wfLoader = &claimTestWFLoader{wf: crossTaskWorkflow(typ)}
			before := snapshotExecs(f.st)
			_, aErr := f.orch.AssignStep(ctx, AssignRequest{RunID: "run-1", StepID: "evaluate", ActorID: "rita"})
			requireCode(t, aErr, domain.ErrForbidden, "assign of a live reviewer")
			_, cErr := f.orch.ClaimStep(ctx, ClaimRequest{ActorID: "rita", ExecutionID: "run-1-evaluate-1"})
			requireCode(t, cErr, domain.ErrForbidden, "claim of a live reviewer")
			requireExecsUnchanged(t, f.st, before, "refused bind")
		})
	}
}

// The author set, spelled out: every type but review and internal.
func TestIsAuthorStepType(t *testing.T) {
	want := map[domain.StepType]bool{
		domain.StepTypeManual: true, domain.StepTypeAutomated: true, domain.StepTypeConvergence: true,
		"": true, "weird": true,
		domain.StepTypeReview: false, domain.StepTypeInternal: false, // verify + review stays allowed
	}
	for typ, w := range want {
		if got := isAuthorStepType(typ); got != w {
			t.Errorf("isAuthorStepType(%q) = %v, want %v", typ, got, w)
		}
	}
}
