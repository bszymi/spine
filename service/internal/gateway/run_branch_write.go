package gateway

import (
	"context"
	"fmt"
	"strings"

	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/observe"
	"github.com/bszymi/spine/core/refadmit"
)

// divergenceCursorPrefix is the run-level cursor the engine sets while a
// run is diverged ("divergence:<id>", core/engine startDivergence). No
// run-level step execution carries it as a StepID.
const divergenceCursorPrefix = "divergence:"

// authorizeRunBranchWrite is the single gate for every API write that lands
// on a run branch: write_context on POST /artifacts, PUT /artifacts/*,
// POST /workflows and PUT /workflows/{id}, and POST /artifacts/add
// (INIT-023 TASK-014, audit P3-6). Before it, resolveWriteContext checked
// only that the run was active and had a branch (and, for standard runs,
// that the caller echoed the run's task_path — readable by anyone through
// GET /runs/{id}), so any contributor could commit into a colleague's run
// branch, even while it sat in review; the governed merge then published
// the injection under the trusted "Merge run …" commit.
//
// Rules, in order:
//
//  1. No run-level cursor — an empty CurrentStepID (legacy rows) or the
//     diverged-run cursor "divergence:<id>". No run-level execution can be
//     bound, so the run branch is operator-only (409 otherwise), the same
//     rule push admission applies (TASK-016). A nil actor passes: it
//     exists only in dev mode, where authorize admits everything.
//  2. Binding (I5), for an authenticated actor below operator. This is
//     TASK-016's push predicate, called rather than repeated, so the push
//     path and the API path are one rule: core/refadmit Decide on
//     refs/heads/<run branch> admits only the actor bound to the latest
//     run-level execution of the run's current step while it is assigned
//     or in progress on an active run, and — for that bound actor — applies
//     the review freeze (refadmit.FrozenStepType). A refusal is explained
//     from the run's executions (bindingRefusal): 409 while the step is
//     waiting or unassigned (claim it first), 409 with no open execution,
//     403 for anyone else — never naming the holder. A run whose branch is
//     not an engine run branch (refadmit does not classify it as one, or
//     resolves it to another run) is operator-only (fail closed).
//  3. Review freeze (I6, amended), for operators and the dev-mode nil
//     actor: while the current step's declared type is review or internal
//     (refadmit.FrozenStepType), every API write is refused (409), so what
//     the reviewer judges is what the governed merge carries and nothing
//     lands between approval and the merge (task-default's internal
//     `publish`). Owner question 1 (default): operators are NOT exempt on
//     the API path; they recover through needs_rework, cancel, or a push
//     (TASK-016 exempts operators on the push path). The type is judged
//     from the step definition, never from the step id, and an
//     unresolvable definition refuses (fail closed).
//
// The step definition comes from s.assignerFor: the orchestrator's
// LookupStepDef (the run's pinned workflow version) when one is wired,
// else the store fallback (the current projection; a degenerate
// configuration with no run engine). No assigner answers 503.
//
// A time-of-check gap remains (documented): the run and its executions are
// read once, before the write. Only the bound actor passes rule 2, so the
// gap is that actor racing their own step.submit with a write; TASK-005's
// reviewed-SHA merge binding is the backstop.
func (s *Server) authorizeRunBranchWrite(ctx context.Context, run *domain.Run) error {
	actor := actorFromContext(ctx)
	operator := actor != nil && actor.Role.HasAtLeast(domain.RoleOperator)

	if run.CurrentStepID == "" || strings.HasPrefix(run.CurrentStepID, divergenceCursorPrefix) {
		if operator || actor == nil {
			return nil
		}
		return domain.NewErrorWithDetail(domain.ErrConflict,
			fmt.Sprintf("run %s has no run-level current step; its branch accepts writes only from operators", run.RunID),
			map[string]string{"run_id": run.RunID})
	}

	assigner := s.assignerFor(ctx)
	if assigner == nil {
		return domain.NewError(domain.ErrUnavailable,
			"run-branch writes need the run's step definitions, and no step assigner is configured")
	}

	if actor != nil && !operator {
		return s.authorizeBoundWriter(ctx, run, actor, assigner)
	}

	def, _ := assigner.LookupStepDef(ctx, run.RunID, run.CurrentStepID)
	if def == nil {
		return unresolvedStepError(run)
	}
	if refadmit.FrozenStepType(def.Type) {
		return frozenStepError(run, def.Type)
	}
	return nil
}

// authorizeBoundWriter is rule 2 (and, for the bound actor, rule 3) through
// core/refadmit.
func (s *Server) authorizeBoundWriter(ctx context.Context, run *domain.Run, actor *domain.Actor, assigner StepAssigner) error {
	st := s.storeFrom(ctx)
	if st == nil {
		return domain.NewError(domain.ErrUnavailable, "store not configured")
	}
	defs := &recordingStepDefs{inner: assigner}
	dec, err := refadmit.NewEvaluator(st).WithStepDefs(defs).Decide(ctx, refadmit.Request{
		Ref:   "refs/heads/" + run.BranchName,
		Actor: actor,
	})
	if err != nil {
		observe.Logger(ctx).Error("run-branch write authorization failed",
			"run_id", run.RunID, "actor_id", actor.ActorID, "error", err)
		return domain.NewError(domain.ErrInternal, "run-branch write authorization failed")
	}
	if dec.Allow {
		if dec.Reason == refadmit.ReasonBoundActor && dec.Class == refadmit.ClassRunBranch && dec.RunID == run.RunID {
			return nil
		}
		// Admitted for another reason — an ordinary branch name, which
		// push admission defers to branch protection — or resolved to a
		// different run: not an engine run branch this predicate can bind.
		return notEngineRunBranchError(run)
	}

	observe.Logger(ctx).Info("run-branch write refused",
		"run_id", run.RunID, "actor_id", actor.ActorID, "class", string(dec.Class), "reason", string(dec.Reason))
	switch dec.Reason {
	case refadmit.ReasonReviewFrozen:
		t := domain.StepType("")
		if defs.last != nil {
			t = defs.last.Type
		}
		return frozenStepError(run, t)
	case refadmit.ReasonStepUnresolved:
		return unresolvedStepError(run)
	case refadmit.ReasonResolverUnavailable:
		return domain.NewError(domain.ErrUnavailable,
			"run-branch writes need the run's step definitions, and none can be resolved on this server")
	case refadmit.ReasonOperatorRequired, refadmit.ReasonUnresolved, refadmit.ReasonInvalidRef, refadmit.ReasonShadowingRef:
		return notEngineRunBranchError(run)
	}
	if dec.Class != refadmit.ClassRunBranch || dec.RunID != run.RunID {
		return notEngineRunBranchError(run)
	}
	return bindingRefusal(ctx, st, run, actor)
}

// bindingRefusal explains a not_bound decision from the latest run-level
// execution of the current step (the execution refadmit judged; store
// order is created_at, latest attempt wins). It never names the holder.
func bindingRefusal(ctx context.Context, st store.Store, run *domain.Run, actor *domain.Actor) error {
	execs, err := st.ListStepExecutionsByRun(ctx, run.RunID)
	if err != nil {
		observe.Logger(ctx).Error("run-branch write authorization: list step executions failed",
			"run_id", run.RunID, "error", err)
		return domain.NewError(domain.ErrInternal, "run-branch write authorization failed")
	}
	var latest *domain.StepExecution
	for i := range execs {
		if execs[i].StepID == run.CurrentStepID && execs[i].BranchID == "" {
			latest = &execs[i]
		}
	}
	switch {
	case latest == nil || !openExecution(latest.Status):
		return domain.NewErrorWithDetail(domain.ErrConflict,
			fmt.Sprintf("run %s has no open execution for its current step %q; its branch accepts no writes", run.RunID, run.CurrentStepID),
			map[string]string{"run_id": run.RunID, "step_id": run.CurrentStepID})
	case latest.ActorID == "" || latest.Status == domain.StepStatusWaiting:
		return domain.NewErrorWithDetail(domain.ErrConflict,
			fmt.Sprintf("current step %q of run %s is not assigned; claim it (POST /execution/claim) or have an operator assign it (POST /runs/%s/steps/%s/assign) before writing to the run branch",
				run.CurrentStepID, run.RunID, run.RunID, run.CurrentStepID),
			map[string]string{"run_id": run.RunID, "step_id": run.CurrentStepID, "execution_id": latest.ExecutionID})
	}
	return domain.NewErrorWithDetail(domain.ErrForbidden,
		fmt.Sprintf("the branch of run %s accepts writes only from the actor assigned to its current step %q, or an operator", run.RunID, run.CurrentStepID),
		map[string]string{"run_id": run.RunID, "step_id": run.CurrentStepID})
}

func openExecution(s domain.StepExecutionStatus) bool {
	return s == domain.StepStatusWaiting || s == domain.StepStatusAssigned || s == domain.StepStatusInProgress
}

func frozenStepError(run *domain.Run, t domain.StepType) error {
	what := "review"
	if t == domain.StepTypeInternal {
		what = "engine-internal"
	}
	return domain.NewErrorWithDetail(domain.ErrConflict,
		fmt.Sprintf("run %s is in %s step %q; its branch is frozen until the step completes (a rework outcome returns it to an editable step)",
			run.RunID, what, run.CurrentStepID),
		map[string]string{"run_id": run.RunID, "step_id": run.CurrentStepID, "step_type": string(t)})
}

func unresolvedStepError(run *domain.Run) error {
	return domain.NewErrorWithDetail(domain.ErrConflict,
		fmt.Sprintf("cannot resolve the definition of step %q of run %s; run-branch writes are refused", run.CurrentStepID, run.RunID),
		map[string]string{"run_id": run.RunID, "step_id": run.CurrentStepID})
}

func notEngineRunBranchError(run *domain.Run) error {
	return domain.NewErrorWithDetail(domain.ErrConflict,
		fmt.Sprintf("run %s's branch is not an engine run branch; API writes to it are accepted only from operators", run.RunID),
		map[string]string{"run_id": run.RunID})
}

// recordingStepDefs passes refadmit's review-freeze lookup through to the
// gateway's step assigner and keeps the last definition, so a freeze
// refusal can report the step type without a second lookup.
type recordingStepDefs struct {
	inner StepAssigner
	last  *domain.StepDefinition
}

func (r *recordingStepDefs) LookupStepDef(ctx context.Context, runID, stepID string) (*domain.StepDefinition, *domain.Run) {
	def, run := r.inner.LookupStepDef(ctx, runID, stepID)
	r.last = def
	return def, run
}
