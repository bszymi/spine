package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bszymi/spine/core/actor"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/observe"
	"github.com/bszymi/spine/core/workflow"
)

// StepResult represents the result submitted by an actor for a step.
type StepResult struct {
	OutcomeID         string
	ArtifactsProduced []string
	// ExpectedHeadSHA, when set, is the run-branch head the submitter
	// worked on (a full lowercase hex commit id). The submit is refused
	// with 409 (run_branch_moved) unless the head is still there, and
	// with 400 when it is malformed or the step cannot carry one
	// (INIT-023 TASK-005). Optional; it is checked even with merge
	// binding off.
	ExpectedHeadSHA string
	// ActorID is the authenticated submitter (INIT-023 TASK-008, audit
	// P3-17). When non-empty, the submit is refused with ErrForbidden for
	// an execution bound to a different actor — on a terminal execution
	// too — and nothing is written. Empty means the caller enforces
	// ownership itself (the behaviour before TASK-008).
	ActorID string

	// expect is IngestResult's snapshot handoff (§3a): the (status, actor)
	// it validated the submission against. Unexported, so only
	// IngestResult sets it and external keyed literals keep compiling.
	expect *stepState
}

// ActivateStep evaluates preconditions for a step and, if they pass, requests
// actor assignment. If preconditions fail, the step is blocked and the run is paused.
//
// Per INIT-020/EPIC-001/TASK-004 (Option B): the step transitions to `assigned`
// only when the engine can resolve a concrete actor at activation time —
// today, that means modes `automated_only` and `ai_only` with an actor
// selector configured AND a match. Modes that require a human or AI agent
// to opt in (`hybrid`, `human_only`) and steps with no execution config
// remain in `waiting` until an explicit `POST /assign` (or pull-based
// `/claim`) binds an actor. This avoids the prior "phantom assigned"
// state where a step was `status=assigned` with `actor_id=NULL`, which
// blocked recovery via `/assign` (state machine rejected the trigger)
// and silently broke `/submit`.
func (o *Orchestrator) ActivateStep(ctx context.Context, runID, stepID string) error {
	log := observe.Logger(ctx)

	run, err := o.store.GetRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("get run: %w", err)
	}
	// EnforceRunStatus (INIT-023 TASK-007): no activation, and so no
	// DeliverAssignment runner dispatch, for a run that is not active.
	// Engine-internal callers always reach this on an active run
	// (startRunCommon activates first; the publish merge_failed route-back
	// sets active before routing); a cancel that lands between a submit and
	// advanceToNextStep's activation is refused here and leaves an inert
	// waiting execution behind.
	if err := o.requireActiveRun(run, "activated"); err != nil {
		return err
	}

	wfDef, err := o.wfLoader.LoadWorkflow(ctx, run.WorkflowPath, run.WorkflowVersion)
	if err != nil {
		return fmt.Errorf("load workflow: %w", err)
	}

	stepDef := findStepDef(wfDef, stepID)
	if stepDef == nil {
		return domain.NewError(domain.ErrNotFound,
			fmt.Sprintf("step %q not found in workflow %s", stepID, wfDef.ID))
	}

	exec, err := o.findStepExecution(ctx, runID, stepID)
	if err != nil {
		return err
	}
	// Every write below is conditional on the execution still holding the
	// status and actor read here (INIT-023 TASK-008): an activation that
	// lost a race to a concurrent one (or to a claim) writes nothing, binds
	// nobody, delivers no assignment and runs no handler.
	prior := stateOf(exec)

	if passed, valResult := o.evaluatePreconditions(ctx, stepDef, run); !passed {
		return o.preparePreconditionFailure(ctx, exec, prior, valResult, runID, stepID)
	}

	// Internal steps are advanced by the Spine engine itself via a
	// handler registered in engine/handlers.go — no runner dispatch,
	// no actor selection, no assignment record. Route them here before
	// the actor-dispatch code below takes over.
	if stepDef.Type == domain.StepTypeInternal {
		return o.activateInternalStep(ctx, exec, prior, stepDef, run)
	}

	exec.ErrorDetail = nil

	actorID := o.tryResolveAutoActor(ctx, exec, stepDef)
	// SeparationOfDuties: an auto-selected bind holds the run's bind stripe
	// for its check and its write only, like AssignStep and ClaimStep (see
	// sodBindStripes); the actor selection above ran without it. A caller
	// whose context ends while waiting for the stripe gets ErrUnavailable
	// and the step is left as it was. A no-op unless the policy sets
	// SeparationOfDuties.
	unlock := func() {}
	if actorID != "" && o.entryPolicy.SeparationOfDuties {
		u, err := o.lockSoDBind(ctx, runID)
		if err != nil {
			return err
		}
		unlock = u
		defer u()
		// Separation of duties also governs engine auto-selection. A
		// selected run author (or, for an author step, a live reviewer) is
		// never bound: the step stays waiting, exactly as for "no match",
		// and a non-author can claim it. actor.SelectionRequest has no
		// exclusion list, so the engine does not retry with another
		// candidate. A ledger failure fails closed the same way. Check 7
		// runs only here, under the stripe: a refusal is not an error, so
		// there is no check order to keep.
		if err := o.checkSeparationOfDuties(ctx, run, wfDef, stepDef, exec, actorID); err != nil {
			log.Info("auto-selected actor refused by separation of duties; step stays waiting",
				"run_id", runID, "step_id", stepID, "actor_id", actorID, "reason", err.Error())
			actorID = ""
		}
	}
	if actorID == "" {
		unlock()
		// Stay in waiting. Pull-based actors discover via
		// ListStepExecutions and call /claim; operators bind via
		// /assign. Either path moves the step out of waiting.
		if err := o.writeStepExecution(ctx, exec, prior); err != nil {
			return fmt.Errorf("update step execution: %w", err)
		}
		log.Info("step ready for assignment",
			"run_id", runID, "step_id", stepID, "mode", executionModeForLog(stepDef))
		return nil
	}

	exec.ActorID = actorID
	stepResult, err := workflow.EvaluateStepTransition(exec.Status, workflow.StepTransitionRequest{
		Trigger: workflow.StepTriggerAssign,
	})
	if err != nil {
		return err
	}
	exec.Status = stepResult.ToStatus

	// A miss (a concurrent activation or claim bound the step first)
	// returns here: no assignment record, no DeliverAssignment, no
	// step_assigned.
	if err := o.writeStepExecution(ctx, exec, prior); err != nil {
		return fmt.Errorf("update step execution: %w", err)
	}
	// Bound: release the stripe before the assignment record, the runner
	// dispatch and the events.
	unlock()

	o.createAutoAssignmentRecord(ctx, exec, runID)

	assignReq := o.buildAssignmentRequest(ctx, exec, stepDef, run)
	if err := o.actors.DeliverAssignment(ctx, assignReq); err != nil {
		log.Warn("failed to deliver assignment", "step_id", stepID, "error", err)
		failPayload, _ := json.Marshal(map[string]any{
			"step_id": stepID,
			"reason":  err.Error(),
		})
		o.emitEvent(ctx, domain.EventAssignmentFailed, runID, run.TraceID,
			fmt.Sprintf("evt-%s-assign-failed", run.TraceID[:12]), failPayload)
		return fmt.Errorf("deliver assignment: %w", err)
	}

	o.trackStepTimeout(ctx, exec, stepDef, assignReq.ActorID, runID)

	o.emitEvent(ctx, domain.EventStepAssigned, runID, run.TraceID,
		fmt.Sprintf("evt-%s-%s-assigned", run.TraceID[:12], stepID), nil)

	log.Info("step activated", "run_id", runID, "step_id", stepID)
	return nil
}

// executionModeForLog returns a stable string for the step's execution mode,
// or "<unset>" when the step has no execution block. Used in structured
// log records so operators can tell waiting-for-claim apart from
// waiting-because-no-mode-configured at a glance.
func executionModeForLog(stepDef *domain.StepDefinition) string {
	if stepDef == nil || stepDef.Execution == nil || stepDef.Execution.Mode == "" {
		return "<unset>"
	}
	return string(stepDef.Execution.Mode)
}

// preparePreconditionFailure persists validation errors (when present) on the
// step execution and returns the appropriate domain error. Callers return the
// result directly; the step stays in waiting because waiting→blocked is not
// a valid transition.
func (o *Orchestrator) preparePreconditionFailure(ctx context.Context, exec *domain.StepExecution, prior stepState, valResult *domain.ValidationResult, runID, stepID string) error {
	log := observe.Logger(ctx)
	if valResult != nil && len(valResult.Errors) > 0 {
		exec.ErrorDetail = &domain.ErrorDetail{
			Classification: domain.FailureValidation,
			Message:        summarizeValidationErrors(valResult.Errors),
			StepID:         stepID,
			Violations:     valResult.Errors,
		}
		if updateErr := o.writeStepExecution(ctx, exec, prior); updateErr != nil {
			log.Warn("failed to persist validation errors", "error", updateErr)
		}
	}
	log.Info("step preconditions not met", "run_id", runID, "step_id", stepID)
	if valResult != nil {
		return domain.NewErrorWithDetail(domain.ErrPrecondition,
			fmt.Sprintf("preconditions not met for step %s", stepID), valResult)
	}
	return domain.NewError(domain.ErrPrecondition,
		fmt.Sprintf("preconditions not met for step %s", stepID))
}

// tryResolveAutoActor returns the actor ID to bind for an engine-driven
// auto-claim, or "" when the engine should not auto-claim. The caller
// uses the empty result to keep the step in `waiting` so that explicit
// /assign or pull-based /claim drives the next transition.
//
// Auto-claim only fires for `automated_only` and `ai_only` modes — those
// are unambiguously machine-driven, so the engine selecting on the
// actor's behalf is the right behavior. `hybrid`, `human_only`, and
// steps with no execution block require a deliberate actor decision; we
// defer to the assignment API rather than guess.
func (o *Orchestrator) tryResolveAutoActor(ctx context.Context, exec *domain.StepExecution, stepDef *domain.StepDefinition) string {
	if stepDef.Execution == nil {
		return ""
	}
	mode := stepDef.Execution.Mode
	if mode != domain.ExecModeAutomatedOnly && mode != domain.ExecModeAIOnly {
		return ""
	}
	if o.actorSelector == nil {
		return ""
	}
	return o.resolveAutoActorID(ctx, exec, stepDef)
}

// createAutoAssignmentRecord persists an assignment row for an auto-selected
// actor so runners can discover the step via the assignment query surface.
// The call is a no-op when no actor was selected or assignments aren't wired.
func (o *Orchestrator) createAutoAssignmentRecord(ctx context.Context, exec *domain.StepExecution, runID string) {
	if exec.ActorID == "" || o.assignments == nil {
		return
	}
	autoAssignment := &domain.Assignment{
		AssignmentID: fmt.Sprintf("auto-%s-%s", exec.ExecutionID, exec.ActorID),
		RunID:        runID,
		ExecutionID:  exec.ExecutionID,
		ActorID:      exec.ActorID,
		Status:       domain.AssignmentStatusActive,
		AssignedAt:   time.Now(),
	}
	if err := o.assignments.CreateAssignment(ctx, autoAssignment); err != nil {
		observe.Logger(ctx).Warn("auto-assignment: failed to create assignment record", "error", err)
	}
}

// buildAssignmentRequest packages the execution, step, and run context into
// the payload delivered to actor runners.
func (o *Orchestrator) buildAssignmentRequest(ctx context.Context, exec *domain.StepExecution, stepDef *domain.StepDefinition, run *domain.Run) actor.AssignmentRequest {
	outcomeIDs := make([]string, len(stepDef.Outcomes))
	for i, o := range stepDef.Outcomes {
		outcomeIDs[i] = o.ID
	}
	excludeActors := o.unavailableActorsForStep(ctx, run.RunID, exec.StepID)

	// Per ADR-015 *Assignment payload shape*: the resolved RepositoryID
	// is sourced from the StepExecution row (set at activation), the
	// CloneURL is constructed by the workspace-aware builder (always
	// the workspace's git HTTP endpoint, never the runtime binding's
	// external URL), and BranchName comes from the run.
	//
	// TASK-005 extends the payload with WorkspaceID (the same runtime
	// ID baked into CloneURL) and CommitBaseline (the SHA the resolved
	// repo's run branch was cut from, captured by createRunBranches).
	// Both are `omitempty` on AssignmentContext, so legacy runs created
	// before TASK-005 (RepositoryBaselines empty) and tests that don't
	// wire a workspace ID still produce valid payloads.
	repoID := exec.RepositoryID
	if repoID == "" {
		repoID = domain.PrimaryRepositoryID
	}
	// TASK-004 (ADR-018 §6.1): WorkspaceID is sourced from the
	// CloneURLBuilder return rather than from a static field on the
	// Orchestrator. The production builder closes over the env-var
	// workspace ID; tests that don't wire a builder produce empty
	// strings, kept `omitempty` on AssignmentContext.
	var workspaceID, cloneURL string
	if o.cloneURLBuilder != nil {
		workspaceID, cloneURL = o.cloneURLBuilder(repoID)
	}
	baseline := run.RepositoryBaselines[repoID]

	return actor.AssignmentRequest{
		AssignmentID: exec.ExecutionID,
		RunID:        run.RunID,
		TraceID:      run.TraceID,
		StepID:       exec.StepID,
		StepName:     stepDef.Name,
		StepType:     stepDef.Type,
		ActorID:      exec.ActorID,
		Context: actor.AssignmentContext{
			TaskPath:        run.TaskPath,
			WorkflowID:      run.WorkflowID,
			RequiredInputs:  stepDef.RequiredInputs,
			RequiredOutputs: stepDef.RequiredOutputs,
			WorkspaceID:     workspaceID,
			RepositoryID:    repoID,
			CloneURL:        cloneURL,
			BranchName:      run.BranchName,
			CommitBaseline:  baseline,
		},
		Constraints: actor.AssignmentConstraints{
			Timeout:          stepDef.Timeout,
			ExpectedOutcomes: outcomeIDs,
			ExcludeActors:    excludeActors,
		},
	}
}

// trackStepTimeout parses the step's timeout (if set) and registers the
// assignment for polling and expiry.
func (o *Orchestrator) trackStepTimeout(ctx context.Context, exec *domain.StepExecution, stepDef *domain.StepDefinition, actorID, runID string) {
	var timeout time.Duration
	if stepDef.Timeout != "" {
		d, err := time.ParseDuration(stepDef.Timeout)
		if err != nil {
			observe.Logger(ctx).Warn("invalid step timeout duration", "step_id", exec.StepID, "timeout", stepDef.Timeout, "error", err)
		} else {
			timeout = d
		}
	}
	o.TrackAssignment(ctx, exec.ExecutionID, runID, exec.ExecutionID, actorID, timeout)
}

// SubmitStepResult processes an actor's result for a step, validates the outcome,
// routes to the next step, and advances the run.
//
// It is "submit", never "re-route" (INIT-023 TASK-008 §3a). Right after
// its read it checks, in order:
//
//  1. ownership: a non-empty result.ActorID must be the execution's actor
//     (ErrForbidden, even on a terminal execution — the gateway's rule);
//  2. the snapshot handoff: when IngestResult validated the submission
//     against an earlier read (result.expect), the row must still hold
//     that (status, actor); otherwise it moved in between — a rebind, a
//     concurrent submit, the timeout scanner — and nothing is written
//     (ErrConflict wrapping ErrStepStateChanged). This also closes the
//     rebind window for callers that send no ActorID;
//  3. an execution that is already terminal is never written, emitted or
//     routed again (ErrConflict wrapping ErrStepStateChanged): the step
//     state machine accepts step.submit on completed as a no-op, so a
//     compare-and-swap could not see a second submit that re-read the row
//     after the first one completed it. Crash recovery re-routes a
//     completed execution through RecoverCompletedStep instead;
//  4. an execution with no actor bound is a 409, as before.
//
// Every write is conditional on the (status, actor) read (writeStepExecution;
// the terminal write expects the post-acknowledge state), so with a store
// that implements StepExecutionTransitioner a submit that loses a race
// stops before CompleteAssignment, the required-outputs Warn,
// step_completed and routing.
func (o *Orchestrator) SubmitStepResult(ctx context.Context, executionID string, result StepResult) error {
	exec, err := o.store.GetStepExecution(ctx, executionID)
	if err != nil {
		return fmt.Errorf("get step execution: %w", err)
	}

	if err := checkSubmitter(exec, result.ActorID); err != nil {
		return err
	}
	if result.expect != nil && !acknowledgedOrSame(*result.expect, stateOf(exec)) {
		return stepStateChanged(executionID, fmt.Sprintf(
			"it moved from %s to %s since the submission was validated", result.expect.status, exec.Status))
	}
	if exec.Status.IsTerminal() {
		return stepStateChanged(executionID, fmt.Sprintf("it is already %s", exec.Status))
	}

	// Defense-in-depth ownership check: a result can only complete a
	// step that has an actor bound. The IngestResult HTTP path already
	// rejects empty actor_id, but engine-internal callers (e.g. the
	// queue consumer's executeAndSubmit) call this method directly. A
	// pre-Option-B phantom row delivered to the runner before the fix
	// must NOT be silently completed here (INIT-020/EPIC-001/TASK-004).
	if exec.ActorID == "" {
		return domain.NewError(domain.ErrConflict,
			fmt.Sprintf("cannot submit result: step %s has no actor bound; call /assign or /claim before submitting",
				executionID))
	}

	run, err := o.store.GetRun(ctx, exec.RunID)
	if err != nil {
		return fmt.Errorf("get run: %w", err)
	}
	// EnforceRunStatus (INIT-023 TASK-007): the engine funnel for every
	// submit — IngestResult and the queue consumer — never advances a run
	// that is not active.
	if err := o.requireActiveRun(run, "submitted"); err != nil {
		return err
	}

	wfDef, err := o.wfLoader.LoadWorkflow(ctx, run.WorkflowPath, run.WorkflowVersion)
	if err != nil {
		return fmt.Errorf("load workflow: %w", err)
	}

	stepDef := findStepDef(wfDef, exec.StepID)
	if stepDef == nil {
		return domain.NewError(domain.ErrNotFound,
			fmt.Sprintf("step %q not found in workflow", exec.StepID))
	}

	outcome := findOutcome(stepDef, result.OutcomeID)
	if outcome == nil {
		return domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("outcome %q not defined for step %s", result.OutcomeID, exec.StepID))
	}

	// Merge binding (INIT-023 TASK-005): resolve and check the run-branch
	// head before anything about the step changes, so a refused submit
	// (moved head, failed assertion, unresolvable head) leaves the
	// execution exactly as it was. nil with binding off and no assertion.
	hb, err := o.captureHead(ctx, run, exec, stepDef, outcome, wfDef, result.ExpectedHeadSHA)
	if err != nil {
		return err
	}

	// Step-output evidence (INIT-023 TASK-008, P3-19), derived from the
	// capture and the run's previous binding as read above, before this
	// submit's commit_meta write. At most one time-boxed diff; it never
	// fails the submit. With a capture it is persisted in the binding's
	// own commit_meta write below (hb.extra). The derivation has no other
	// side effect: the flag's Warn is logged only once the terminal write
	// below has applied (review round 1), so a submission that is refused
	// or loses its race leaves no audit warning behind.
	change := o.branchChangeAtSubmit(ctx, run, exec, hb)
	unchanged := requiredOutputsUnchanged(stepDef, change)
	if hb != nil {
		key, val, err := evidenceMetaEntry(exec.ExecutionID, change, unchanged)
		if err != nil {
			return fmt.Errorf("encode step output evidence: %w", err)
		}
		hb.extra = map[string]string{key: val}
	}

	// If step is assigned, auto-acknowledge to in_progress first. The
	// terminal write below must then expect the acknowledged state
	// (in_progress, actor), not the (assigned, actor) read above.
	prior := stateOf(exec)
	if exec.Status == domain.StepStatusAssigned {
		if err := o.autoAcknowledgeStep(ctx, exec, prior, run); err != nil {
			// The same actor acknowledging concurrently (its runner's
			// /acknowledge racing the submit) is not a conflict (codex
			// review, round 3): continue from the acknowledged row. Any
			// other move — a rebind, a release, a terminal write — stands.
			if !isStepStateChanged(err) {
				return err
			}
			cur, rerr := o.store.GetStepExecution(ctx, executionID)
			if rerr != nil || !acknowledgedOrSame(prior, stateOf(cur)) || cur.Status != domain.StepStatusInProgress {
				return err
			}
			exec = cur
		}
		prior = stateOf(exec)
	}

	stepTResult, err := workflow.EvaluateStepTransition(exec.Status, workflow.StepTransitionRequest{
		Trigger:   workflow.StepTriggerSubmit,
		OutcomeID: result.OutcomeID,
	})
	if err != nil {
		return err
	}

	// A captured binding is stored BEFORE the execution completes (INIT-023
	// wave 3 integration, codex review). The scheduler's step recovery
	// re-routes a completed execution without capturing again (captureHead
	// skips terminal executions: the head may have moved since), so a
	// binding written only after the completion — lost to a failed
	// SetCommitMeta or a crash in between — left the run with the previous
	// step's binding, or with none, and a gate then compares against
	// nothing and accepts whatever the branch holds. Written here, a
	// completed execution always has its binding; a failed write leaves the
	// execution uncompleted (auto-acknowledged at most), so it can be
	// submitted again. It is the same single commit_meta write
	// routeStepOutcome made (outcome commit map and binding together, plus
	// this execution's evidence key), only earlier; with no capture
	// (binding off, among others) it stays where it was.
	metaRecorded := false
	if hb != nil {
		if err := o.recordStepCommitMeta(ctx, stepDef, outcome, exec, run, wfDef, hb); err != nil {
			return err
		}
		metaRecorded = true
	}

	now := time.Now()
	exec.Status = stepTResult.ToStatus
	exec.OutcomeID = result.OutcomeID
	exec.CompletedAt = &now
	if err := o.writeStepExecution(ctx, exec, prior); err != nil {
		// A lost race (INIT-023 TASK-008) stops here: no CompleteAssignment,
		// no step_completed, no routing. IngestResult answers it with the
		// winner's persisted state.
		return fmt.Errorf("update step execution: %w", err)
	}

	o.CompleteAssignment(ctx, executionID)

	if unchanged {
		warnRequiredOutputsUnchanged(ctx, stepDef, exec, change)
	}
	o.emitStepCompleted(ctx, run, exec,
		baseStepCompletedPayload(stepDef, outcome, exec, now).withEvidence(stepDef, result.ArtifactsProduced, change, unchanged))

	if exec.StartedAt != nil {
		observe.GlobalMetrics.StepDuration.ObserveDuration(time.Since(*exec.StartedAt))
	}

	return o.routeStepOutcome(ctx, stepDef, outcome, exec, run, wfDef, now, hb, metaRecorded)
}

// recoverBranchStep is RecoverCompletedStep for an execution on a
// divergence branch (codex review, round 5), whose routing is
// completeBranchStep: it moves the BRANCH cursor (UpdateBranch) and then
// creates the branch's next execution. The run's cursor must be at this
// branch's divergence and the branch still open (pending or in progress);
// a later execution on the branch means it was routed. Then:
//
//   - an outcome that ends the branch, with the branch cursor still at this
//     step and no later execution: completeBranchStep runs again (branch
//     completed, convergence checked);
//   - otherwise the next execution is claimed like recoverAdvance claims
//     it — created first under a pinned attempt, so of two recoveries one
//     wins the primary key — when the branch cursor is still at this step
//     (nothing of the routing ran; step_completed is re-emitted with
//     branch_change unknown/recovered) or already at the next step (the
//     routing stopped before creating it); only the creator then moves the
//     branch cursor and activates the step;
//   - a successor this recovery created whose branch cursor it did not
//     move (it stopped in between) is finished: cursor moved, activated.
//
// Every branch-cursor move is conditional (moveBranchCursorIfAt, review
// round 1): only while the branch is open and still at this step, and
// only the cursor is written, so a recovery never writes back a stale
// branch snapshot over a branch that moved on or completed; a miss is a
// conflict and nothing is activated.
//
// Anything else is ErrConflict and changes nothing. A branch that was
// marked completed but whose convergence check never ran is not covered
// (KNOWN-LIMITATIONS "Engine State Writes …").
func (o *Orchestrator) recoverBranchStep(ctx context.Context, run *domain.Run, exec *domain.StepExecution) error {
	branch, err := o.store.GetBranch(ctx, exec.BranchID)
	if err != nil {
		return fmt.Errorf("get branch: %w", err)
	}
	if branch.Status != domain.BranchStatusPending && branch.Status != domain.BranchStatusInProgress {
		return domain.NewError(domain.ErrConflict,
			fmt.Sprintf("branch %s is %s; step %s is not re-routed", branch.BranchID, branch.Status, exec.ExecutionID))
	}
	if run.CurrentStepID != "divergence:"+branch.DivergenceID {
		return domain.NewError(domain.ErrConflict,
			fmt.Sprintf("run %s is at %q, not at divergence %s; step %s is not re-routed", run.RunID, run.CurrentStepID, branch.DivergenceID, exec.ExecutionID))
	}
	execs, err := o.store.ListStepExecutionsByRun(ctx, run.RunID)
	if err != nil {
		return fmt.Errorf("list step executions: %w", err)
	}
	var later []*domain.StepExecution
	for i := range execs {
		e := &execs[i]
		if e.ExecutionID != exec.ExecutionID && e.BranchID == exec.BranchID &&
			(e.CreatedAt.After(exec.CreatedAt) || (e.StepID == exec.StepID && e.Attempt > exec.Attempt)) {
			later = append(later, e)
		}
	}
	wfDef, err := o.wfLoader.LoadWorkflow(ctx, run.WorkflowPath, run.WorkflowVersion)
	if err != nil {
		return fmt.Errorf("load workflow: %w", err)
	}
	stepDef := findStepDef(wfDef, exec.StepID)
	if stepDef == nil {
		return domain.NewError(domain.ErrNotFound, fmt.Sprintf("step %q not found in workflow", exec.StepID))
	}
	outcome := findOutcome(stepDef, exec.OutcomeID)
	if outcome == nil {
		return domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("stored outcome %q not defined for step %s", exec.OutcomeID, exec.StepID))
	}
	routed := func(why string) error {
		return domain.NewError(domain.ErrConflict,
			fmt.Sprintf("branch %s: step %s is not re-routed (%s)", branch.BranchID, exec.ExecutionID, why))
	}
	log := observe.Logger(ctx)
	next := outcome.NextStep
	if next == "" || next == "end" {
		// completeBranchStep marks the branch completed and checks
		// convergence; nothing to re-run when the branch moved or a later
		// execution exists.
		if len(later) > 0 || branch.CurrentStepID != exec.StepID {
			return routed("the branch moved on")
		}
		o.emitRecoveredBranchStep(ctx, run, exec, stepDef, outcome)
		return o.completeBranchStep(ctx, run, exec, outcome, time.Now(), wfDef)
	}

	// The successor exists but the branch cursor was not moved: a
	// recovery created it (the claim below) and stopped. Finish it. Two
	// recoveries can both get here; the cursor move is conditional
	// (review round 1, codex), so only one moves it and activates, and
	// neither writes back its stale snapshot of the branch.
	if len(later) == 1 && later[0].StepID == next && later[0].Status == domain.StepStatusWaiting &&
		later[0].ExecutionID == fmt.Sprintf("%s-%s-%s-%d", run.RunID, exec.BranchID, next, later[0].Attempt) &&
		branch.CurrentStepID == exec.StepID {
		moved, err := o.moveBranchCursorIfAt(ctx, branch.BranchID, exec.StepID, next)
		if err != nil {
			return err
		}
		if !moved {
			return routed("the branch moved on while it was being recovered")
		}
		log.Info("recovery: resumed a branch successor whose branch cursor had not moved",
			"run_id", run.RunID, "branch_id", branch.BranchID, "successor", later[0].ExecutionID)
		if err := o.ActivateStep(ctx, run.RunID, next); err != nil {
			log.Warn("branch step activation failed", "step_id", next, "error", err)
		}
		return nil
	}
	if len(later) > 0 {
		return routed("a later execution exists on the branch")
	}
	if branch.CurrentStepID != exec.StepID && branch.CurrentStepID != next {
		return routed(fmt.Sprintf("the branch is at %q", branch.CurrentStepID))
	}

	// Claim the routing by creating the successor under a pinned attempt
	// (see recoverAdvance), then move the branch cursor — only the
	// recovery that created it writes the cursor.
	attempt := 1
	for i := range execs {
		if execs[i].StepID == next && execs[i].Attempt >= attempt {
			attempt = execs[i].Attempt + 1
		}
	}
	nextRepoID := resolveStepRepoIDByStepID(wfDef, next)
	nextExec := &domain.StepExecution{
		ExecutionID:  fmt.Sprintf("%s-%s-%s-%d", run.RunID, exec.BranchID, next, attempt),
		RunID:        run.RunID,
		StepID:       next,
		BranchID:     exec.BranchID,
		RepositoryID: nextRepoID,
		Status:       domain.StepStatusWaiting,
		Attempt:      attempt,
		CreatedAt:    time.Now(),
	}
	if branch.CurrentStepID == exec.StepID {
		o.emitRecoveredBranchStep(ctx, run, exec, stepDef, outcome)
	}
	if err := o.claimSuccessor(ctx, nextExec, exec); err != nil {
		return err
	}
	recordStepRoutingDecision(ctx, run.RunID, next, findStepDef(wfDef, next), nextRepoID)
	if branch.CurrentStepID != next {
		// Conditional, as on the main path (moveCursorIfAt): a branch that
		// moved since the read is not rewound with this snapshot.
		moved, err := o.moveBranchCursorIfAt(ctx, branch.BranchID, exec.StepID, next)
		if err != nil {
			return err
		}
		if !moved {
			log.Warn("recovery: created the branch successor but the branch had moved; not activating it",
				"run_id", run.RunID, "branch_id", branch.BranchID, "successor", nextExec.ExecutionID)
			return routed("the branch moved on while it was being recovered")
		}
	}
	log.Info("branch step progressed (recovery)",
		"run_id", run.RunID, "branch_id", branch.BranchID, "completed_step", exec.StepID, "next_step", next)
	if err := o.ActivateStep(ctx, run.RunID, next); err != nil {
		log.Warn("branch step activation failed", "step_id", next, "error", err)
	}
	return nil
}

// emitRecoveredBranchStep re-emits a recovered branch execution's
// step_completed (same per-execution id; the event log keeps the first).
func (o *Orchestrator) emitRecoveredBranchStep(ctx context.Context, run *domain.Run, exec *domain.StepExecution, stepDef *domain.StepDefinition, outcome *domain.OutcomeDefinition) {
	o.CompleteAssignment(ctx, exec.ExecutionID)
	completedAt := time.Now()
	if exec.CompletedAt != nil {
		completedAt = *exec.CompletedAt
	}
	recovered := BranchChange{RepositoryID: domain.PrimaryRepositoryID, Status: BranchChangeUnknown, Reason: EvidenceReasonRecovered}
	o.emitStepCompleted(ctx, run, exec,
		baseStepCompletedPayload(stepDef, outcome, exec, completedAt).withEvidence(stepDef, nil, recovered, false))
}

// resumeRoutedSuccessor is RecoverCompletedStep's tail when routing
// already created the successor but did not move the run's cursor (see
// there). Anything other than exactly that successor — more than one later
// execution, another step, a divergence branch, a successor that is no
// longer waiting, an id advanceToNextStep would not build, an outcome that
// starts a divergence or ends the run — is refused: the execution was
// routed, and nothing is changed.
func (o *Orchestrator) resumeRoutedSuccessor(ctx context.Context, run *domain.Run, exec *domain.StepExecution, stepDef *domain.StepDefinition, outcome *domain.OutcomeDefinition, later []*domain.StepExecution) error {
	next := outcome.NextStep
	succ := later[0]
	routable := next != "" && next != "end" && (stepDef.Diverge == "" || o.divergence == nil)
	if !routable || len(later) != 1 || succ.StepID != next || succ.BranchID != "" ||
		succ.Status != domain.StepStatusWaiting ||
		succ.ExecutionID != fmt.Sprintf("%s-%s-%d", run.RunID, next, succ.Attempt) {
		return domain.NewError(domain.ErrConflict,
			fmt.Sprintf("run %s already has a later execution (%s); step %s was routed", run.RunID, succ.ExecutionID, exec.ExecutionID))
	}
	log := observe.Logger(ctx)
	// The cursor moves only while the run is still active and at this
	// step (codex review, round 2): a live submit finishing the same
	// routing may already have moved it — and gone further — so a plain
	// write could rewind it to a step that was routed.
	moved, err := o.moveCursorIfAt(ctx, run.RunID, exec.StepID, next)
	if err != nil {
		return err
	}
	if !moved {
		return domain.NewError(domain.ErrConflict,
			fmt.Sprintf("run %s moved on while step %s was being recovered; nothing to resume", run.RunID, exec.ExecutionID))
	}
	log.Info("recovery: routing stopped before the run's cursor moved; resumed the successor",
		"run_id", run.RunID, "execution_id", exec.ExecutionID, "next_step", next, "successor", succ.ExecutionID)
	// Activate only a successor that is still waiting; one a live submit
	// activated (or finished) meanwhile is left to it.
	if cur, err := o.store.GetStepExecution(ctx, succ.ExecutionID); err != nil || cur.Status != domain.StepStatusWaiting {
		return nil
	}
	if err := o.ActivateStep(ctx, run.RunID, next); err != nil {
		// As in advanceToNextStep: the cursor moved; an activation that
		// fails (or loses its compare-and-swap to a live activation) is
		// logged, not returned.
		log.Warn("recovery: successor activation failed", "step_id", next, "error", err)
	}
	return nil
}

// acknowledgedOrSame reports whether cur is the state `was` described, or
// that state acknowledged by the same actor (assigned → in_progress). An
// acknowledgment is the one benign move between a submission's reads and
// its write: the step's owner (or its runner) started it. Any other change
// of status or actor means the execution moved under the submission.
func acknowledgedOrSame(was, cur stepState) bool {
	if cur == was {
		return true
	}
	return was.status == domain.StepStatusAssigned && cur.status == domain.StepStatusInProgress &&
		cur.actorID == was.actorID && was.actorID != ""
}

// checkSubmitter is the engine's own submit-ownership check (INIT-023
// TASK-008, audit P3-17): a non-empty submitter must be the actor the
// execution is bound to. An empty submitter means the caller enforces
// ownership itself (every caller before TASK-008; SMP until SMP TASK-011);
// an execution with no actor is left to the callers' own checks (409 when
// live, the idempotent answer when terminal). The refusal does not name
// the owner.
func checkSubmitter(exec *domain.StepExecution, submitter string) error {
	if submitter == "" || exec.ActorID == "" || exec.ActorID == submitter {
		return nil
	}
	return domain.NewError(domain.ErrForbidden,
		fmt.Sprintf("step execution %s is not assigned to the submitting actor", exec.ExecutionID))
}

// RecoverCompletedStep re-routes a step execution that completed but whose
// run did not advance — the process stopped between the completion write
// and routing (INIT-023 TASK-008 §3a). It is the only engine path that
// routes an already-completed execution, and the crash-recovery callbacks
// of `spine serve` use it instead of SubmitStepResult (which refuses
// terminal executions).
//
// Preconditions, re-checked here rather than trusted from the scheduler:
// the execution is completed; the run is active and its current step is
// the execution's step; and no execution of the run was created after
// this one — otherwise a submit has already routed it. Each refusal is
// ErrConflict and changes nothing.
//
// One later execution is the exception (codex review, round 1): exactly
// the successor this execution's routing creates — the outcome's next
// step on the main path, `waiting`, under the deterministic id
// advanceToNextStep builds. Then routing stopped between creating it and
// moving the run's cursor (a crash; or a live submit that is about to do
// the same two writes), and recovery finishes it instead of refusing:
// the cursor moves to that step and the step is activated, exactly
// advanceToNextStep's tail. Refusing left the run stranded — nothing else
// activates the successor, and an internal publish step is never claimed
// or assigned, so its merge never ran. A live submit racing it writes the
// same cursor, and the second activation of the step is a lost
// compare-and-swap (the store capability `spine serve` always has), so the
// handler and the assignment delivery run once.
//
// It makes no step write and routes the STORED outcome. It emits
// step_completed again under the execution's own event id with
// branch_change unknown/recovered; an event log that already has the
// original keeps it (ON CONFLICT DO NOTHING). Two recoveries of the same
// execution at once may both pass the checks; the routing to a next step
// is claimed by creating the successor first under an attempt pinned from
// the checked listing (recoverAdvance), so exactly one creates it and the
// other gets a conflict (codex review, round 6).
func (o *Orchestrator) RecoverCompletedStep(ctx context.Context, executionID string) error {
	exec, err := o.store.GetStepExecution(ctx, executionID)
	if err != nil {
		return fmt.Errorf("get step execution: %w", err)
	}
	if exec.Status != domain.StepStatusCompleted {
		return domain.NewError(domain.ErrConflict,
			fmt.Sprintf("step execution %s is %s, not completed; nothing to recover", executionID, exec.Status))
	}
	run, err := o.store.GetRun(ctx, exec.RunID)
	if err != nil {
		return fmt.Errorf("get run: %w", err)
	}
	if run.Status != domain.RunStatusActive {
		return domain.NewError(domain.ErrConflict,
			fmt.Sprintf("run %s is %s, not active; step %s is not re-routed", run.RunID, run.Status, executionID))
	}
	if exec.BranchID != "" {
		return o.recoverBranchStep(ctx, run, exec)
	}
	if run.CurrentStepID != exec.StepID {
		return domain.NewError(domain.ErrConflict,
			fmt.Sprintf("run %s is at step %q, not %q; step %s is not re-routed", run.RunID, run.CurrentStepID, exec.StepID, executionID))
	}
	execs, err := o.store.ListStepExecutionsByRun(ctx, run.RunID)
	if err != nil {
		return fmt.Errorf("list step executions: %w", err)
	}
	var later []*domain.StepExecution
	for i := range execs {
		e := &execs[i]
		if e.ExecutionID == exec.ExecutionID {
			continue
		}
		if e.CreatedAt.After(exec.CreatedAt) || (e.StepID == exec.StepID && e.Attempt > exec.Attempt) {
			later = append(later, e)
		}
	}

	wfDef, err := o.wfLoader.LoadWorkflow(ctx, run.WorkflowPath, run.WorkflowVersion)
	if err != nil {
		return fmt.Errorf("load workflow: %w", err)
	}
	stepDef := findStepDef(wfDef, exec.StepID)
	if stepDef == nil {
		return domain.NewError(domain.ErrNotFound,
			fmt.Sprintf("step %q not found in workflow", exec.StepID))
	}
	outcome := findOutcome(stepDef, exec.OutcomeID)
	if outcome == nil {
		return domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("stored outcome %q not defined for step %s", exec.OutcomeID, exec.StepID))
	}
	if len(later) > 0 {
		return o.resumeRoutedSuccessor(ctx, run, exec, stepDef, outcome, later)
	}

	observe.Logger(ctx).Info("re-routing a completed step after recovery",
		"run_id", run.RunID, "execution_id", executionID, "step_id", exec.StepID, "outcome_id", exec.OutcomeID)

	o.CompleteAssignment(ctx, executionID)
	completedAt := time.Now()
	if exec.CompletedAt != nil {
		completedAt = *exec.CompletedAt
	}
	recovered := BranchChange{RepositoryID: domain.PrimaryRepositoryID, Status: BranchChangeUnknown, Reason: EvidenceReasonRecovered}
	o.emitStepCompleted(ctx, run, exec,
		baseStepCompletedPayload(stepDef, outcome, exec, completedAt).withEvidence(stepDef, nil, recovered, false))

	next := outcome.NextStep
	if next == "" || next == "end" || (stepDef.Diverge != "" && o.divergence != nil) {
		// Run completion and divergence start go through the normal
		// routing: CompleteRun is a run compare-and-swap, so a second
		// recovery gets its conflict.
		return o.routeStepOutcome(ctx, stepDef, outcome, exec, run, wfDef, time.Now(), nil, false)
	}
	return o.recoverAdvance(ctx, run, exec, stepDef, outcome, wfDef, execs, next)
}

// recoverAdvance is RecoverCompletedStep's routing to a next step (codex
// review, round 6). advanceToNextStep numbers the next attempt from the
// executions it lists when it runs, so a second recovery that passed the
// same checks a moment after the first one routed would create a second
// attempt of the successor (review-2 beside review-1) and rewind the
// cursor with its plain write. Recovery therefore claims the routing: the
// attempt is pinned from the listing its checks used, the successor is
// created FIRST under advanceToNextStep's deterministic id — so of two
// recoveries exactly one creates it, the other fails on the primary key
// and answers a conflict — and only then is the cursor moved, with the
// conditional cursor move, and the successor activated. A crash between
// the create and the cursor move is what resumeRoutedSuccessor finishes.
func (o *Orchestrator) recoverAdvance(ctx context.Context, run *domain.Run, exec *domain.StepExecution, stepDef *domain.StepDefinition, outcome *domain.OutcomeDefinition, wfDef *domain.WorkflowDefinition, listed []domain.StepExecution, next string) error {
	log := observe.Logger(ctx)
	if err := o.recordStepCommitMeta(ctx, stepDef, outcome, exec, run, wfDef, nil); err != nil {
		return err
	}
	attempt := 1
	for i := range listed {
		if listed[i].StepID == next && listed[i].Attempt >= attempt {
			attempt = listed[i].Attempt + 1
		}
	}
	if attempt > domain.MaxReworkCycles {
		log.Warn("rework cycle limit exceeded",
			"run_id", run.RunID, "step_id", next, "attempt", attempt, "max", domain.MaxReworkCycles)
		return o.FailRun(ctx, run.RunID,
			fmt.Sprintf("rework cycle limit exceeded for step %s (attempt %d)", next, attempt))
	}
	nextRepoID := resolveStepRepoIDByStepID(wfDef, next)
	nextExec := &domain.StepExecution{
		ExecutionID:  fmt.Sprintf("%s-%s-%d", run.RunID, next, attempt),
		RunID:        run.RunID,
		StepID:       next,
		RepositoryID: nextRepoID,
		Status:       domain.StepStatusWaiting,
		Attempt:      attempt,
		CreatedAt:    time.Now(),
	}
	if err := o.claimSuccessor(ctx, nextExec, exec); err != nil {
		return err
	}
	recordStepRoutingDecision(ctx, run.RunID, next, findStepDef(wfDef, next), nextRepoID)
	moved, err := o.moveCursorIfAt(ctx, run.RunID, exec.StepID, next)
	if err != nil {
		return err
	}
	if !moved {
		log.Warn("recovery: created the successor but the run's cursor had moved; not activating it",
			"run_id", run.RunID, "execution_id", exec.ExecutionID, "successor", nextExec.ExecutionID)
		return domain.NewError(domain.ErrConflict,
			fmt.Sprintf("run %s moved on while step %s was being recovered", run.RunID, exec.ExecutionID))
	}
	log.Info("step progressed (recovery)", "run_id", run.RunID, "completed_step", exec.StepID, "next_step", next)
	if err := o.ActivateStep(ctx, run.RunID, next); err != nil {
		log.Warn("next step activation failed", "step_id", next, "error", err)
	}
	return nil
}

// claimSuccessor creates a recovery's successor execution. The create is
// the claim: when it fails because the execution already exists, another
// recovery (or the original routing) created it first, which is a
// conflict with nothing changed; any other failure is returned.
func (o *Orchestrator) claimSuccessor(ctx context.Context, next, from *domain.StepExecution) error {
	if err := o.store.CreateStepExecution(ctx, next); err != nil {
		if _, gerr := o.store.GetStepExecution(ctx, next.ExecutionID); gerr == nil {
			return domain.NewError(domain.ErrConflict,
				fmt.Sprintf("step %s was routed concurrently (%s exists)", from.ExecutionID, next.ExecutionID))
		}
		return fmt.Errorf("create next step: %w", err)
	}
	return nil
}

// autoAcknowledgeStep transitions an assigned step to in_progress before the
// submit transition. Extracted so SubmitStepResult's main body stays on the
// happy path; an error here aborts submission.
func (o *Orchestrator) autoAcknowledgeStep(ctx context.Context, exec *domain.StepExecution, prior stepState, run *domain.Run) error {
	ackResult, err := workflow.EvaluateStepTransition(exec.Status, workflow.StepTransitionRequest{
		Trigger: workflow.StepTriggerAcknowledged,
	})
	if err != nil {
		return fmt.Errorf("acknowledge step: %w", err)
	}
	now := time.Now()
	exec.Status = ackResult.ToStatus
	exec.StartedAt = &now
	if err := o.writeStepExecution(ctx, exec, prior); err != nil {
		return fmt.Errorf("update step execution: %w", err)
	}
	o.emitEvent(ctx, domain.EventStepStarted, exec.RunID, run.TraceID,
		fmt.Sprintf("evt-%s-started", exec.ExecutionID), nil)
	return nil
}

// routeStepOutcome dispatches the step's outcome into one of the three
// post-submit paths: trigger divergence, complete a branch, or advance the
// run (possibly by completing it at the "end" terminal step).
//
// commit_meta (INIT-023 TASK-005): one SetCommitMeta per main-path
// completion, before the dispatch, storing mergeCommitMeta's result —
// the outcome's commit map with every reserved "spine." key stripped
// (binding on or off), plus, with binding on, the reserved binding keys
// carried over and hb written over them. As before, nothing is written for
// a divergence-branch step, and a step that starts a divergence does not
// store its outcome's commit map (only an hb binding, with binding on).
// Binding off, the write happens exactly when it did before: the outcome
// has a commit and the step does not start a divergence. metaRecorded
// means SubmitStepResult already made that write, before the execution
// completed (a captured binding; see there), so it is not repeated.
func (o *Orchestrator) routeStepOutcome(ctx context.Context, stepDef *domain.StepDefinition, outcome *domain.OutcomeDefinition, exec *domain.StepExecution, run *domain.Run, wfDef *domain.WorkflowDefinition, now time.Time, hb *headBinding, metaRecorded bool) error {
	startsDivergence := stepDef.Diverge != "" && o.divergence != nil
	if !metaRecorded {
		if err := o.recordStepCommitMeta(ctx, stepDef, outcome, exec, run, wfDef, hb); err != nil {
			return err
		}
	}
	if startsDivergence {
		return o.startDivergence(ctx, run, wfDef, stepDef, exec)
	}
	if exec.BranchID != "" {
		return o.completeBranchStep(ctx, run, exec, outcome, now, wfDef)
	}

	nextStepID := outcome.NextStep
	if nextStepID == "" {
		nextStepID = "end"
	}

	hasCommit := len(outcome.Commit) > 0
	if nextStepID == "end" {
		if err := o.CompleteRun(ctx, exec.RunID, hasCommit); err != nil {
			return fmt.Errorf("complete run: %w", err)
		}
		return nil
	}
	return o.advanceToNextStep(ctx, exec, nextStepID, "", now, wfDef)
}

// recordStepCommitMeta is a main-path step completion's single
// commit_meta write (see routeStepOutcome): nothing for a
// divergence-branch step; otherwise mergeCommitMeta's result, when it
// says to write.
func (o *Orchestrator) recordStepCommitMeta(ctx context.Context, stepDef *domain.StepDefinition, outcome *domain.OutcomeDefinition, exec *domain.StepExecution, run *domain.Run, wfDef *domain.WorkflowDefinition, hb *headBinding) error {
	if exec.BranchID != "" {
		return nil
	}
	startsDivergence := stepDef.Diverge != "" && o.divergence != nil
	logStrippedReserved(ctx, wfDef.ID, stepDef.ID, outcome.ID, outcome.Commit)
	storeCommit := len(outcome.Commit) > 0 && !startsDivergence
	if meta, write := mergeCommitMeta(run.CommitMeta, outcome.Commit, storeCommit, o.bindingOn, hb); write {
		if err := o.store.SetCommitMeta(ctx, exec.RunID, meta); err != nil {
			return fmt.Errorf("set commit meta: %w", err)
		}
	}
	return nil
}

// advanceToNextStep creates the next step execution, updates current_step_id, and activates.
func (o *Orchestrator) advanceToNextStep(ctx context.Context, exec *domain.StepExecution, nextStepID, branchID string, now time.Time, wfDef *domain.WorkflowDefinition) error {
	log := observe.Logger(ctx)

	attempt := o.nextAttempt(ctx, exec.RunID, nextStepID)
	if attempt > domain.MaxReworkCycles {
		log.Warn("rework cycle limit exceeded",
			"run_id", exec.RunID, "step_id", nextStepID, "attempt", attempt, "max", domain.MaxReworkCycles)
		return o.FailRun(ctx, exec.RunID,
			fmt.Sprintf("rework cycle limit exceeded for step %s (attempt %d)", nextStepID, attempt))
	}

	nextRepoID := resolveStepRepoIDByStepID(wfDef, nextStepID)
	nextExec := &domain.StepExecution{
		ExecutionID:  fmt.Sprintf("%s-%s-%d", exec.RunID, nextStepID, attempt),
		RunID:        exec.RunID,
		StepID:       nextStepID,
		BranchID:     branchID,
		RepositoryID: nextRepoID,
		Status:       domain.StepStatusWaiting,
		Attempt:      attempt,
		CreatedAt:    now,
	}
	if err := o.store.CreateStepExecution(ctx, nextExec); err != nil {
		return fmt.Errorf("create next step: %w", err)
	}
	recordStepRoutingDecision(ctx, exec.RunID, nextStepID, findStepDef(wfDef, nextStepID), nextRepoID)

	if err := o.store.UpdateCurrentStep(ctx, exec.RunID, nextStepID); err != nil {
		return fmt.Errorf("update current step: %w", err)
	}

	log.Info("step progressed",
		"run_id", exec.RunID, "completed_step", exec.StepID, "next_step", nextStepID, "branch_id", branchID)

	if err := o.ActivateStep(ctx, exec.RunID, nextStepID); err != nil {
		log.Warn("next step activation failed", "step_id", nextStepID, "error", err)
	}
	return nil
}

// startDivergence triggers divergence for a step and creates branch step executions.
func (o *Orchestrator) startDivergence(ctx context.Context, run *domain.Run, wfDef *domain.WorkflowDefinition, stepDef *domain.StepDefinition, exec *domain.StepExecution) error {
	log := observe.Logger(ctx)

	// Find the divergence definition in the workflow.
	var divDef *domain.DivergenceDefinition
	for i := range wfDef.DivergencePoints {
		if wfDef.DivergencePoints[i].ID == stepDef.Diverge {
			divDef = &wfDef.DivergencePoints[i]
			break
		}
	}
	if divDef == nil {
		return domain.NewError(domain.ErrNotFound,
			fmt.Sprintf("divergence point %q not found in workflow", stepDef.Diverge))
	}

	// Find the convergence point referenced by this divergence.
	convergenceID := ""
	if stepDef.Converge != "" {
		convergenceID = stepDef.Converge
	}

	divCtx, err := o.divergence.StartDivergence(ctx, run, *divDef, convergenceID)
	if err != nil {
		return fmt.Errorf("start divergence: %w", err)
	}

	// Update run cursor to the divergence context ID so recovery knows
	// the run is in divergence and doesn't try to resume the completed step.
	if err := o.store.UpdateCurrentStep(ctx, exec.RunID, "divergence:"+divCtx.DivergenceID); err != nil {
		return fmt.Errorf("update current step for divergence: %w", err)
	}

	// Create entry step executions for each branch.
	now := time.Now()
	branches, err := o.store.ListBranchesByDivergence(ctx, divCtx.DivergenceID)
	if err != nil {
		return fmt.Errorf("list branches: %w", err)
	}

	for i := range branches {
		branch := &branches[i]
		if branch.CurrentStepID == "" {
			continue
		}
		branchRepoID := resolveStepRepoIDByStepID(wfDef, branch.CurrentStepID)
		branchExec := &domain.StepExecution{
			ExecutionID:  fmt.Sprintf("%s-%s-%s-1", exec.RunID, branch.BranchID, branch.CurrentStepID),
			RunID:        exec.RunID,
			StepID:       branch.CurrentStepID,
			BranchID:     branch.BranchID,
			RepositoryID: branchRepoID,
			Status:       domain.StepStatusWaiting,
			Attempt:      1,
			CreatedAt:    now,
		}
		if err := o.store.CreateStepExecution(ctx, branchExec); err != nil {
			return fmt.Errorf("create branch step %s: %w", branch.BranchID, err)
		}
		recordStepRoutingDecision(ctx, exec.RunID, branch.CurrentStepID, findStepDef(wfDef, branch.CurrentStepID), branchRepoID)

		log.Info("branch step created",
			"run_id", exec.RunID, "branch_id", branch.BranchID, "step_id", branch.CurrentStepID)

		if err := o.ActivateStep(ctx, exec.RunID, branch.CurrentStepID); err != nil {
			log.Warn("branch step activation failed", "branch_id", branch.BranchID, "error", err)
		}
	}

	return nil
}

// completeBranchStep handles step completion within a divergence branch.
// If the outcome is terminal (next_step == "end"), marks the branch as completed.
// Otherwise advances to the next step within the branch and updates branch.CurrentStepID.
func (o *Orchestrator) completeBranchStep(ctx context.Context, run *domain.Run, exec *domain.StepExecution, outcome *domain.OutcomeDefinition, now time.Time, wfDef *domain.WorkflowDefinition) error {
	log := observe.Logger(ctx)

	nextStepID := outcome.NextStep
	if nextStepID == "" {
		nextStepID = "end"
	}

	branch, err := o.store.GetBranch(ctx, exec.BranchID)
	if err != nil {
		log.Warn("failed to get branch", "branch_id", exec.BranchID, "error", err)
		return nil
	}

	if nextStepID == "end" {
		// Branch terminal — mark branch as completed.
		branch.Status = domain.BranchStatusCompleted
		completedAt := now
		branch.CompletedAt = &completedAt
		if err := o.store.UpdateBranch(ctx, branch); err != nil {
			return fmt.Errorf("update branch status: %w", err)
		}

		log.Info("branch completed", "run_id", exec.RunID, "branch_id", exec.BranchID)

		// Check if convergence should trigger now that a branch is done.
		if o.convergence != nil {
			if err := o.tryConvergence(ctx, run, branch.DivergenceID); err != nil {
				log.Warn("convergence check failed", "divergence_id", branch.DivergenceID, "error", err)
			}
		}
		return nil
	}

	// Update branch cursor before advancing.
	branch.CurrentStepID = nextStepID
	if err := o.store.UpdateBranch(ctx, branch); err != nil {
		return fmt.Errorf("update branch current step: %w", err)
	}

	// Non-terminal — advance within the branch (skip run-level current_step_id update).
	return o.advanceBranchStep(ctx, exec, nextStepID, exec.BranchID, now, wfDef)
}

// tryConvergence checks if all branches are ready and triggers convergence evaluation.
func (o *Orchestrator) tryConvergence(ctx context.Context, run *domain.Run, divergenceID string) error {
	log := observe.Logger(ctx)

	divCtx, err := o.store.GetDivergenceContext(ctx, divergenceID)
	if err != nil {
		return fmt.Errorf("get divergence context: %w", err)
	}

	// Load the workflow to find the convergence definition.
	wfDef, err := o.wfLoader.LoadWorkflow(ctx, run.WorkflowPath, run.WorkflowVersion)
	if err != nil {
		return fmt.Errorf("load workflow for convergence: %w", err)
	}

	convDef := findConvergenceForDivergence(wfDef, divergenceID, divCtx)
	if convDef == nil {
		log.Info("no convergence definition found", "divergence_id", divergenceID)
		return nil
	}

	ready, err := o.convergence.CheckEntryPolicy(ctx, divCtx, *convDef)
	if err != nil {
		return fmt.Errorf("check entry policy: %w", err)
	}
	if !ready {
		log.Info("convergence entry policy not yet satisfied", "divergence_id", divergenceID)
		return nil
	}

	log.Info("convergence triggered", "divergence_id", divergenceID, "strategy", convDef.Strategy)

	// Merge binding (INIT-023 TASK-005 §5, DC-6): record the head before
	// the convergence moves it, so the rebind afterwards happens only from
	// the bound head.
	var h0 string
	var h0Err error
	if o.bindingOn && run.BranchName != "" {
		h0, h0Err = o.binder.ResolveBranchHead(ctx, run.BranchName)
	}

	if err := o.convergence.EvaluateAndCommit(ctx, run, divCtx, *convDef); err != nil {
		return fmt.Errorf("evaluate and commit convergence: %w", err)
	}
	if o.bindingOn && run.BranchName != "" {
		o.rebindAfterConvergence(ctx, run.RunID, divergenceID, h0, h0Err)
	}

	// Resume the run after convergence.
	// Prefer evaluation_step from convergence definition; fall back to step with converge field.
	convergeStepID := convDef.EvaluationStep
	if convergeStepID == "" {
		convergeStepID = findConvergeStep(wfDef, convDef.ID)
	}
	if convergeStepID != "" {
		now := time.Now()
		if err := o.store.UpdateCurrentStep(ctx, run.RunID, convergeStepID); err != nil {
			return fmt.Errorf("update current step after convergence: %w", err)
		}

		attempt := o.nextAttempt(ctx, run.RunID, convergeStepID)
		convergeRepoID := resolveStepRepoIDByStepID(wfDef, convergeStepID)
		nextExec := &domain.StepExecution{
			ExecutionID:  fmt.Sprintf("%s-%s-%d", run.RunID, convergeStepID, attempt),
			RunID:        run.RunID,
			StepID:       convergeStepID,
			RepositoryID: convergeRepoID,
			Status:       domain.StepStatusWaiting,
			Attempt:      attempt,
			CreatedAt:    now,
		}
		if err := o.store.CreateStepExecution(ctx, nextExec); err != nil {
			return fmt.Errorf("create post-convergence step: %w", err)
		}
		recordStepRoutingDecision(ctx, run.RunID, convergeStepID, findStepDef(wfDef, convergeStepID), convergeRepoID)

		if err := o.ActivateStep(ctx, run.RunID, convergeStepID); err != nil {
			log.Warn("post-convergence step activation failed", "step_id", convergeStepID, "error", err)
		}
	}

	return nil
}

// findConvergeStep finds the step in the workflow whose converge field references a convergence point.
func findConvergeStep(wfDef *domain.WorkflowDefinition, convergenceID string) string {
	for i := range wfDef.Steps {
		if wfDef.Steps[i].Converge == convergenceID {
			return wfDef.Steps[i].ID
		}
	}
	return ""
}

// findConvergenceForDivergence looks up the convergence definition associated with a divergence.
// Checks ConvergenceID on divCtx first, then scans steps for a converge field referencing
// any convergence point (handles workflows where the divergence step sets diverge but not converge).
func findConvergenceForDivergence(wfDef *domain.WorkflowDefinition, divergenceID string, divCtx *domain.DivergenceContext) *domain.ConvergenceDefinition {
	// Direct lookup via ConvergenceID.
	if divCtx.ConvergenceID != "" {
		for i := range wfDef.ConvergencePoints {
			if wfDef.ConvergencePoints[i].ID == divCtx.ConvergenceID {
				return &wfDef.ConvergencePoints[i]
			}
		}
	}

	// Fallback: if there's only one convergence point, use it.
	if len(wfDef.ConvergencePoints) == 1 {
		return &wfDef.ConvergencePoints[0]
	}
	return nil
}

// advanceBranchStep creates the next step execution within a branch without updating
// run.CurrentStepID (branch steps don't own the run cursor).
func (o *Orchestrator) advanceBranchStep(ctx context.Context, exec *domain.StepExecution, nextStepID, branchID string, now time.Time, wfDef *domain.WorkflowDefinition) error {
	log := observe.Logger(ctx)

	attempt := o.nextAttempt(ctx, exec.RunID, nextStepID)
	nextRepoID := resolveStepRepoIDByStepID(wfDef, nextStepID)
	nextExec := &domain.StepExecution{
		ExecutionID:  fmt.Sprintf("%s-%s-%s-%d", exec.RunID, branchID, nextStepID, attempt),
		RunID:        exec.RunID,
		StepID:       nextStepID,
		BranchID:     branchID,
		RepositoryID: nextRepoID,
		Status:       domain.StepStatusWaiting,
		Attempt:      attempt,
		CreatedAt:    now,
	}
	if err := o.store.CreateStepExecution(ctx, nextExec); err != nil {
		return fmt.Errorf("create branch step: %w", err)
	}
	recordStepRoutingDecision(ctx, exec.RunID, nextStepID, findStepDef(wfDef, nextStepID), nextRepoID)

	log.Info("branch step progressed",
		"run_id", exec.RunID, "branch_id", branchID, "completed_step", exec.StepID, "next_step", nextStepID)

	if err := o.ActivateStep(ctx, exec.RunID, nextStepID); err != nil {
		log.Warn("branch step activation failed", "step_id", nextStepID, "error", err)
	}
	return nil
}

// findStepExecution finds the current (non-terminal) step execution for a run+step.
func (o *Orchestrator) findStepExecution(ctx context.Context, runID, stepID string) (*domain.StepExecution, error) {
	execs, err := o.store.ListStepExecutionsByRun(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("list step executions: %w", err)
	}

	for i := range execs {
		if execs[i].StepID == stepID && !execs[i].Status.IsTerminal() {
			return &execs[i], nil
		}
	}
	return nil, domain.NewError(domain.ErrNotFound,
		fmt.Sprintf("no active execution for step %q in run %s", stepID, runID))
}

// evaluatePreconditions checks all preconditions on a step definition.
// Returns (true, nil) if all pass. On failure, returns (false, result) where
// result is non-nil only for cross_artifact_valid failures (with detailed errors).
func (o *Orchestrator) evaluatePreconditions(ctx context.Context, step *domain.StepDefinition, run *domain.Run) (bool, *domain.ValidationResult) {
	if len(step.Preconditions) == 0 {
		return true, nil
	}

	log := observe.Logger(ctx)

	for _, precond := range step.Preconditions {
		switch precond.Type {
		case "artifact_status":
			if !o.checkArtifactStatus(ctx, precond.Config, run) {
				log.Info("precondition failed", "type", precond.Type, "config", precond.Config)
				return false, nil
			}
		case "field_present":
			if !o.checkFieldPresent(ctx, precond.Config, run) {
				log.Info("precondition failed", "type", precond.Type, "config", precond.Config)
				return false, nil
			}
		case "field_value":
			if !o.checkFieldValue(ctx, precond.Config, run) {
				log.Info("precondition failed", "type", precond.Type, "config", precond.Config)
				return false, nil
			}
		case "links_exist":
			if !o.checkLinksExist(ctx, precond.Config, run) {
				log.Info("precondition failed", "type", precond.Type, "config", precond.Config)
				return false, nil
			}
		case "discussions_resolved":
			if !o.checkDiscussionsResolved(ctx, precond.Config, run) {
				log.Info("precondition failed", "type", precond.Type, "config", precond.Config)
				return false, nil
			}
		case "cross_artifact_valid":
			if o.validator == nil {
				if o.entryPolicy.EnforcePreconditions {
					// Strict evaluation (INIT-023 TASK-007, Owner question 4):
					// a check that cannot run does not pass.
					log.Info("cross_artifact_valid precondition failed closed (no validator configured)")
					return false, nil
				}
				log.Info("cross_artifact_valid precondition skipped (no validator configured)")
				continue
			}
			artifactPath := run.TaskPath
			if p := precond.Config["artifact_path"]; p != "" {
				artifactPath = p
			}
			result := o.validator.Validate(ctx, artifactPath)
			// Log warnings but don't block on them.
			for i := range result.Warnings {
				log.Warn("validation warning",
					"rule_id", result.Warnings[i].RuleID,
					"message", result.Warnings[i].Message,
					"artifact_path", result.Warnings[i].ArtifactPath,
				)
			}
			if result.Status == "failed" {
				log.Info("cross_artifact_valid precondition failed",
					"artifact_path", artifactPath,
					"error_count", len(result.Errors),
				)
				return false, &result
			}
		default:
			if o.entryPolicy.EnforcePreconditions {
				// Strict evaluation: an unknown type — including the
				// documented-but-unimplemented `custom` — fails closed.
				log.Info("unknown precondition type failed closed", "type", precond.Type)
				return false, nil
			}
			// Unknown precondition types are skipped (forward-compatible).
			log.Info("unknown precondition type skipped", "type", precond.Type)
		}
	}
	return true, nil
}

// summarizeValidationErrors produces a human-readable summary of validation errors.
func summarizeValidationErrors(errs []domain.ValidationError) string {
	if len(errs) == 0 {
		return "validation failed"
	}
	if len(errs) == 1 {
		return errs[0].Message
	}
	var msgs []string
	for i := range errs {
		msgs = append(msgs, errs[i].Message)
	}
	return fmt.Sprintf("%d validation errors: %s", len(errs), strings.Join(msgs, "; "))
}

// resolveReadRef returns the Git ref to use when reading artifacts for precondition evaluation.
// Planning runs read from the run branch (where the artifact exists); standard runs read from HEAD.
func resolveReadRef(run *domain.Run) string {
	if run.Mode == domain.RunModePlanning && run.BranchName != "" {
		return run.BranchName
	}
	return "HEAD"
}

// loadPreconditionArtifact reads the artifact referenced by config["path"]
// (falling back to run.TaskPath) at the run's current read ref. Returns
// (nil, false) if the read fails — callers treat that as a failed check.
func (o *Orchestrator) loadPreconditionArtifact(ctx context.Context, config map[string]string, run *domain.Run) (*domain.Artifact, bool) {
	path := config["path"]
	if path == "" {
		path = run.TaskPath
	}
	art, err := o.artifacts.Read(ctx, path, resolveReadRef(run))
	if err != nil {
		return nil, false
	}
	return art, true
}

// checkArtifactStatus verifies an artifact at config["path"] has config["status"].
func (o *Orchestrator) checkArtifactStatus(ctx context.Context, config map[string]string, run *domain.Run) bool {
	art, ok := o.loadPreconditionArtifact(ctx, config, run)
	if !ok {
		return false
	}
	return string(art.Status) == config["status"]
}

// checkFieldPresent verifies an artifact at config["path"] has a non-empty
// front-matter field (see preconditionField for how id/type/title/status are
// read).
func (o *Orchestrator) checkFieldPresent(ctx context.Context, config map[string]string, run *domain.Run) bool {
	art, ok := o.loadPreconditionArtifact(ctx, config, run)
	if !ok {
		return false
	}
	return o.preconditionField(art, config["field"]) != ""
}

// checkFieldValue verifies an artifact field matches an expected value (see
// preconditionField for how id/type/title/status are read).
func (o *Orchestrator) checkFieldValue(ctx context.Context, config map[string]string, run *domain.Run) bool {
	art, ok := o.loadPreconditionArtifact(ctx, config, run)
	if !ok {
		return false
	}
	return o.preconditionField(art, config["field"]) == config["value"]
}

// checkLinksExist verifies an artifact has at least one link of the given type.
func (o *Orchestrator) checkLinksExist(ctx context.Context, config map[string]string, run *domain.Run) bool {
	art, ok := o.loadPreconditionArtifact(ctx, config, run)
	if !ok {
		return false
	}
	linkType := domain.LinkType(config["link_type"])
	for _, link := range art.Links {
		if link.Type == linkType {
			return true
		}
	}
	return false
}

// checkDiscussionsResolved verifies no open discussion threads exist on the artifact.
// Config supports optional "anchor_type" (defaults to "artifact") and "path" (defaults to run.TaskPath).
func (o *Orchestrator) checkDiscussionsResolved(ctx context.Context, config map[string]string, run *domain.Run) bool {
	if o.discussions == nil {
		if o.entryPolicy.EnforcePreconditions {
			// Strict evaluation (INIT-023 TASK-007): fail closed.
			observe.Logger(ctx).Info("discussions_resolved precondition failed closed (no discussion checker configured)")
			return false
		}
		observe.Logger(ctx).Info("discussions_resolved precondition skipped (no discussion checker configured)")
		return true
	}

	anchorType := domain.AnchorType(config["anchor_type"])
	if anchorType == "" {
		anchorType = domain.AnchorTypeArtifact
	}
	anchorID := config["path"]
	if anchorID == "" {
		if anchorType == domain.AnchorTypeRun {
			anchorID = run.RunID
		} else {
			anchorID = run.TaskPath
		}
	}

	hasOpen, err := o.discussions.HasOpenThreads(ctx, anchorType, anchorID)
	if err != nil {
		observe.Logger(ctx).Warn("discussions_resolved precondition check failed", "error", err)
		return false
	}
	return !hasOpen
}

// nextAttempt returns the next attempt number for a step in a run.
// This handles cyclic workflows where a step may be visited multiple times.
func (o *Orchestrator) nextAttempt(ctx context.Context, runID, stepID string) int {
	execs, err := o.store.ListStepExecutionsByRun(ctx, runID)
	if err != nil {
		return 1
	}
	highest := 0
	for i := range execs {
		if execs[i].StepID == stepID && execs[i].Attempt > highest {
			highest = execs[i].Attempt
		}
	}
	return highest + 1
}

// unavailableActorsForStep returns actor IDs from prior failed executions
// classified as actor_unavailable. Used to exclude them from reassignment.
func (o *Orchestrator) unavailableActorsForStep(ctx context.Context, runID, stepID string) []string {
	execs, err := o.store.ListStepExecutionsByRun(ctx, runID)
	if err != nil {
		observe.Logger(ctx).Warn("failed to list step executions for actor exclusion", "run_id", runID, "error", err)
		return nil
	}
	var excluded []string
	for i := range execs {
		e := &execs[i]
		if e.StepID == stepID && e.Status == domain.StepStatusFailed &&
			e.ErrorDetail != nil && e.ErrorDetail.Classification == domain.FailureActorUnavailable &&
			e.ActorID != "" {
			excluded = append(excluded, e.ActorID)
		}
	}
	return excluded
}

// resolveAutoActorID selects an actor for an automated or ai-only step.
//
// Case 1: eligible_actor_ids is set on the execution — validates and picks the
// first listed actor.
// Case 2: no eligible_actor_ids — selects any active actor matching the step's
// eligible_actor_types (falling back to the mode-implied type if unset).
// Returns "" if no eligible actor is found (graceful degradation).
func (o *Orchestrator) resolveAutoActorID(ctx context.Context, exec *domain.StepExecution, stepDef *domain.StepDefinition) string {
	log := observe.Logger(ctx)

	eligibleTypes := stepDef.Execution.EligibleActorTypes
	if len(eligibleTypes) == 0 {
		// Infer from mode when not explicitly configured.
		switch stepDef.Execution.Mode {
		case domain.ExecModeAutomatedOnly:
			eligibleTypes = []string{string(domain.ActorTypeAutomated)}
		case domain.ExecModeAIOnly:
			eligibleTypes = []string{string(domain.ActorTypeAIAgent)}
		}
	}

	// Case 1: explicit actor list on the execution.
	if len(exec.EligibleActorIDs) > 0 {
		selected, err := o.actorSelector.SelectActor(ctx, actor.SelectionRequest{
			Strategy:           actor.StrategyExplicit,
			ExplicitActorID:    exec.EligibleActorIDs[0],
			EligibleActorTypes: eligibleTypes,
			RequiredSkills:     stepDef.Execution.RequiredSkills,
		})
		if err != nil {
			log.Warn("auto-assignment: explicit actor not eligible",
				"actor_id", exec.EligibleActorIDs[0], "error", err)
			return ""
		}
		return selected.ActorID
	}

	// Case 2: pick any eligible actor by type.
	selected, err := o.actorSelector.SelectActor(ctx, actor.SelectionRequest{
		Strategy:           actor.StrategyAnyEligible,
		EligibleActorTypes: eligibleTypes,
		RequiredSkills:     stepDef.Execution.RequiredSkills,
	})
	if err != nil {
		log.Warn("auto-assignment: no eligible actor of required type",
			"eligible_types", eligibleTypes, "error", err)
		return ""
	}
	return selected.ActorID
}

// findOutcome looks up an outcome by ID within a step definition.
func findOutcome(step *domain.StepDefinition, outcomeID string) *domain.OutcomeDefinition {
	for i := range step.Outcomes {
		if step.Outcomes[i].ID == outcomeID {
			return &step.Outcomes[i]
		}
	}
	return nil
}
