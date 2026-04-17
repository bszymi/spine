package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bszymi/spine/internal/actor"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/workflow"
)

// StepResult represents the result submitted by an actor for a step.
type StepResult struct {
	OutcomeID         string
	ArtifactsProduced []string
}

// ActivateStep evaluates preconditions for a step and, if they pass, requests
// actor assignment. If preconditions fail, the step is blocked and the run is paused.
func (o *Orchestrator) ActivateStep(ctx context.Context, runID, stepID string) error {
	log := observe.Logger(ctx)

	run, err := o.store.GetRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("get run: %w", err)
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

	if passed, valResult := o.evaluatePreconditions(ctx, stepDef, run); !passed {
		return o.preparePreconditionFailure(ctx, exec, valResult, runID, stepID)
	}

	exec.ErrorDetail = nil
	stepResult, err := workflow.EvaluateStepTransition(exec.Status, workflow.StepTransitionRequest{
		Trigger: workflow.StepTriggerAssign,
	})
	if err != nil {
		return err
	}
	exec.Status = stepResult.ToStatus

	o.resolveAutoActor(ctx, exec, stepDef, runID, stepID)

	if err := o.store.UpdateStepExecution(ctx, exec); err != nil {
		return fmt.Errorf("update step execution: %w", err)
	}

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

// preparePreconditionFailure persists validation errors (when present) on the
// step execution and returns the appropriate domain error. Callers return the
// result directly; the step stays in waiting because waiting→blocked is not
// a valid transition.
func (o *Orchestrator) preparePreconditionFailure(ctx context.Context, exec *domain.StepExecution, valResult *domain.ValidationResult, runID, stepID string) error {
	log := observe.Logger(ctx)
	if valResult != nil && len(valResult.Errors) > 0 {
		exec.ErrorDetail = &domain.ErrorDetail{
			Classification: domain.FailureValidation,
			Message:        summarizeValidationErrors(valResult.Errors),
			StepID:         stepID,
			Violations:     valResult.Errors,
		}
		if updateErr := o.store.UpdateStepExecution(ctx, exec); updateErr != nil {
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

// resolveAutoActor sets exec.ActorID when the step declares an automated or
// AI-only execution mode and a selector is configured. Missing selections
// are logged but not fatal — the step simply waits.
func (o *Orchestrator) resolveAutoActor(ctx context.Context, exec *domain.StepExecution, stepDef *domain.StepDefinition, runID, stepID string) {
	if stepDef.Execution == nil {
		return
	}
	mode := stepDef.Execution.Mode
	if mode != domain.ExecModeAutomatedOnly && mode != domain.ExecModeAIOnly {
		return
	}
	if o.actorSelector == nil {
		return
	}
	if actorID := o.resolveAutoActorID(ctx, exec, stepDef); actorID != "" {
		exec.ActorID = actorID
		return
	}
	observe.Logger(ctx).Warn("auto-assignment: no eligible actor found, step will wait",
		"run_id", runID, "step_id", stepID, "mode", mode)
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
func (o *Orchestrator) SubmitStepResult(ctx context.Context, executionID string, result StepResult) error {
	exec, err := o.store.GetStepExecution(ctx, executionID)
	if err != nil {
		return fmt.Errorf("get step execution: %w", err)
	}

	run, err := o.store.GetRun(ctx, exec.RunID)
	if err != nil {
		return fmt.Errorf("get run: %w", err)
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

	// If step is assigned, auto-acknowledge to in_progress first.
	if exec.Status == domain.StepStatusAssigned {
		if err := o.autoAcknowledgeStep(ctx, exec, run); err != nil {
			return err
		}
	}

	stepTResult, err := workflow.EvaluateStepTransition(exec.Status, workflow.StepTransitionRequest{
		Trigger:   workflow.StepTriggerSubmit,
		OutcomeID: result.OutcomeID,
	})
	if err != nil {
		return err
	}

	now := time.Now()
	exec.Status = stepTResult.ToStatus
	exec.OutcomeID = result.OutcomeID
	exec.CompletedAt = &now
	if err := o.store.UpdateStepExecution(ctx, exec); err != nil {
		return fmt.Errorf("update step execution: %w", err)
	}

	o.CompleteAssignment(ctx, executionID)

	o.emitEvent(ctx, domain.EventStepCompleted, exec.RunID, run.TraceID,
		fmt.Sprintf("evt-%s-%s-completed", run.TraceID[:12], exec.StepID), nil)

	if exec.StartedAt != nil {
		observe.GlobalMetrics.StepDuration.ObserveDuration(time.Since(*exec.StartedAt))
	}

	return o.routeStepOutcome(ctx, stepDef, outcome, exec, run, wfDef, now)
}

// autoAcknowledgeStep transitions an assigned step to in_progress before the
// submit transition. Extracted so SubmitStepResult's main body stays on the
// happy path; an error here aborts submission.
func (o *Orchestrator) autoAcknowledgeStep(ctx context.Context, exec *domain.StepExecution, run *domain.Run) error {
	ackResult, err := workflow.EvaluateStepTransition(exec.Status, workflow.StepTransitionRequest{
		Trigger: workflow.StepTriggerAcknowledged,
	})
	if err != nil {
		return fmt.Errorf("acknowledge step: %w", err)
	}
	now := time.Now()
	exec.Status = ackResult.ToStatus
	exec.StartedAt = &now
	if err := o.store.UpdateStepExecution(ctx, exec); err != nil {
		return fmt.Errorf("update step execution: %w", err)
	}
	o.emitEvent(ctx, domain.EventStepStarted, exec.RunID, run.TraceID,
		fmt.Sprintf("evt-%s-started", exec.ExecutionID), nil)
	return nil
}

// routeStepOutcome dispatches the step's outcome into one of the three
// post-submit paths: trigger divergence, complete a branch, or advance the
// run (possibly by completing it at the "end" terminal step). SetCommitMeta
// runs when the outcome carries commit metadata.
func (o *Orchestrator) routeStepOutcome(ctx context.Context, stepDef *domain.StepDefinition, outcome *domain.OutcomeDefinition, exec *domain.StepExecution, run *domain.Run, wfDef *domain.WorkflowDefinition, now time.Time) error {
	if stepDef.Diverge != "" && o.divergence != nil {
		return o.startDivergence(ctx, run, wfDef, stepDef, exec)
	}
	if exec.BranchID != "" {
		return o.completeBranchStep(ctx, run, exec, outcome, now)
	}

	nextStepID := outcome.NextStep
	if nextStepID == "" {
		nextStepID = "end"
	}

	hasCommit := len(outcome.Commit) > 0
	if hasCommit {
		if err := o.store.SetCommitMeta(ctx, exec.RunID, outcome.Commit); err != nil {
			return fmt.Errorf("set commit meta: %w", err)
		}
	}
	if nextStepID == "end" {
		if err := o.CompleteRun(ctx, exec.RunID, hasCommit); err != nil {
			return fmt.Errorf("complete run: %w", err)
		}
		return nil
	}
	return o.advanceToNextStep(ctx, exec, nextStepID, "", now)
}

// advanceToNextStep creates the next step execution, updates current_step_id, and activates.
func (o *Orchestrator) advanceToNextStep(ctx context.Context, exec *domain.StepExecution, nextStepID, branchID string, now time.Time) error {
	log := observe.Logger(ctx)

	attempt := o.nextAttempt(ctx, exec.RunID, nextStepID)
	if attempt > domain.MaxReworkCycles {
		log.Warn("rework cycle limit exceeded",
			"run_id", exec.RunID, "step_id", nextStepID, "attempt", attempt, "max", domain.MaxReworkCycles)
		return o.FailRun(ctx, exec.RunID,
			fmt.Sprintf("rework cycle limit exceeded for step %s (attempt %d)", nextStepID, attempt))
	}

	nextExec := &domain.StepExecution{
		ExecutionID: fmt.Sprintf("%s-%s-%d", exec.RunID, nextStepID, attempt),
		RunID:       exec.RunID,
		StepID:      nextStepID,
		BranchID:    branchID,
		Status:      domain.StepStatusWaiting,
		Attempt:     attempt,
		CreatedAt:   now,
	}
	if err := o.store.CreateStepExecution(ctx, nextExec); err != nil {
		return fmt.Errorf("create next step: %w", err)
	}

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
		branchExec := &domain.StepExecution{
			ExecutionID: fmt.Sprintf("%s-%s-%s-1", exec.RunID, branch.BranchID, branch.CurrentStepID),
			RunID:       exec.RunID,
			StepID:      branch.CurrentStepID,
			BranchID:    branch.BranchID,
			Status:      domain.StepStatusWaiting,
			Attempt:     1,
			CreatedAt:   now,
		}
		if err := o.store.CreateStepExecution(ctx, branchExec); err != nil {
			return fmt.Errorf("create branch step %s: %w", branch.BranchID, err)
		}

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
func (o *Orchestrator) completeBranchStep(ctx context.Context, run *domain.Run, exec *domain.StepExecution, outcome *domain.OutcomeDefinition, now time.Time) error {
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
	return o.advanceBranchStep(ctx, exec, nextStepID, exec.BranchID, now)
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

	if err := o.convergence.EvaluateAndCommit(ctx, divCtx, *convDef); err != nil {
		return fmt.Errorf("evaluate and commit convergence: %w", err)
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
		nextExec := &domain.StepExecution{
			ExecutionID: fmt.Sprintf("%s-%s-%d", run.RunID, convergeStepID, attempt),
			RunID:       run.RunID,
			StepID:      convergeStepID,
			Status:      domain.StepStatusWaiting,
			Attempt:     attempt,
			CreatedAt:   now,
		}
		if err := o.store.CreateStepExecution(ctx, nextExec); err != nil {
			return fmt.Errorf("create post-convergence step: %w", err)
		}

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
func (o *Orchestrator) advanceBranchStep(ctx context.Context, exec *domain.StepExecution, nextStepID, branchID string, now time.Time) error {
	log := observe.Logger(ctx)

	attempt := o.nextAttempt(ctx, exec.RunID, nextStepID)
	nextExec := &domain.StepExecution{
		ExecutionID: fmt.Sprintf("%s-%s-%s-%d", exec.RunID, branchID, nextStepID, attempt),
		RunID:       exec.RunID,
		StepID:      nextStepID,
		BranchID:    branchID,
		Status:      domain.StepStatusWaiting,
		Attempt:     attempt,
		CreatedAt:   now,
	}
	if err := o.store.CreateStepExecution(ctx, nextExec); err != nil {
		return fmt.Errorf("create branch step: %w", err)
	}

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

// checkFieldPresent verifies an artifact at config["path"] has a non-empty metadata field.
func (o *Orchestrator) checkFieldPresent(ctx context.Context, config map[string]string, run *domain.Run) bool {
	art, ok := o.loadPreconditionArtifact(ctx, config, run)
	if !ok {
		return false
	}
	return art.Metadata[config["field"]] != ""
}

// checkFieldValue verifies an artifact field matches an expected value.
func (o *Orchestrator) checkFieldValue(ctx context.Context, config map[string]string, run *domain.Run) bool {
	art, ok := o.loadPreconditionArtifact(ctx, config, run)
	if !ok {
		return false
	}
	return art.Metadata[config["field"]] == config["value"]
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
