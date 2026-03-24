package engine

import (
	"context"
	"fmt"
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

	// Load the workflow definition to get step details.
	wfDef, err := o.wfLoader.LoadWorkflow(ctx, run.WorkflowPath, run.WorkflowVersion)
	if err != nil {
		return fmt.Errorf("load workflow: %w", err)
	}

	stepDef := findStepDef(wfDef, stepID)
	if stepDef == nil {
		return domain.NewError(domain.ErrNotFound,
			fmt.Sprintf("step %q not found in workflow %s", stepID, wfDef.ID))
	}

	// Find the current step execution.
	exec, err := o.findStepExecution(ctx, runID, stepID)
	if err != nil {
		return err
	}

	// Evaluate preconditions. If they fail, the step stays in waiting and
	// the caller receives a precondition error. The step is not transitioned
	// because waiting→blocked is not valid in the step state machine.
	if !o.evaluatePreconditions(ctx, stepDef, run) {
		log.Info("step preconditions not met", "run_id", runID, "step_id", stepID)
		return domain.NewError(domain.ErrPrecondition,
			fmt.Sprintf("preconditions not met for step %s", stepID))
	}

	// Preconditions pass — transition to assigned via step.assign.
	stepResult, err := workflow.EvaluateStepTransition(exec.Status, workflow.StepTransitionRequest{
		Trigger: workflow.StepTriggerAssign,
	})
	if err != nil {
		return err
	}
	exec.Status = stepResult.ToStatus
	if err := o.store.UpdateStepExecution(ctx, exec); err != nil {
		return fmt.Errorf("update step execution: %w", err)
	}

	// Request actor assignment.
	outcomeIDs := make([]string, len(stepDef.Outcomes))
	for i, o := range stepDef.Outcomes {
		outcomeIDs[i] = o.ID
	}

	assignReq := actor.AssignmentRequest{
		AssignmentID: exec.ExecutionID,
		RunID:        runID,
		TraceID:      run.TraceID,
		StepID:       stepID,
		StepName:     stepDef.Name,
		StepType:     stepDef.Type,
		Context: actor.AssignmentContext{
			TaskPath:        run.TaskPath,
			WorkflowID:      run.WorkflowID,
			RequiredInputs:  stepDef.RequiredInputs,
			RequiredOutputs: stepDef.RequiredOutputs,
		},
		Constraints: actor.AssignmentConstraints{
			Timeout:          stepDef.Timeout,
			ExpectedOutcomes: outcomeIDs,
		},
	}

	if err := o.actors.DeliverAssignment(ctx, assignReq); err != nil {
		log.Warn("failed to deliver assignment", "step_id", stepID, "error", err)
		if err := o.events.Emit(ctx, domain.Event{
			EventID:   fmt.Sprintf("evt-%s-assign-failed", run.TraceID[:12]),
			Type:      domain.EventAssignmentFailed,
			Timestamp: time.Now(),
			RunID:     runID,
			TraceID:   run.TraceID,
		}); err != nil {
			log.Warn("failed to emit event", "event_type", domain.EventAssignmentFailed, "error", err)
		}
		return fmt.Errorf("deliver assignment: %w", err)
	}

	if err := o.events.Emit(ctx, domain.Event{
		EventID:   fmt.Sprintf("evt-%s-%s-assigned", run.TraceID[:12], stepID),
		Type:      domain.EventStepAssigned,
		Timestamp: time.Now(),
		RunID:     runID,
		TraceID:   run.TraceID,
	}); err != nil {
		log.Warn("failed to emit event", "event_type", domain.EventStepAssigned, "error", err)
	}

	log.Info("step activated", "run_id", runID, "step_id", stepID)
	return nil
}

// SubmitStepResult processes an actor's result for a step, validates the outcome,
// routes to the next step, and advances the run.
func (o *Orchestrator) SubmitStepResult(ctx context.Context, executionID string, result StepResult) error {
	log := observe.Logger(ctx)

	exec, err := o.store.GetStepExecution(ctx, executionID)
	if err != nil {
		return fmt.Errorf("get step execution: %w", err)
	}

	run, err := o.store.GetRun(ctx, exec.RunID)
	if err != nil {
		return fmt.Errorf("get run: %w", err)
	}

	// Load the workflow to validate the outcome.
	wfDef, err := o.wfLoader.LoadWorkflow(ctx, run.WorkflowPath, run.WorkflowVersion)
	if err != nil {
		return fmt.Errorf("load workflow: %w", err)
	}

	stepDef := findStepDef(wfDef, exec.StepID)
	if stepDef == nil {
		return domain.NewError(domain.ErrNotFound,
			fmt.Sprintf("step %q not found in workflow", exec.StepID))
	}

	// Validate outcome ID is defined in the step.
	outcome := findOutcome(stepDef, result.OutcomeID)
	if outcome == nil {
		return domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("outcome %q not defined for step %s", result.OutcomeID, exec.StepID))
	}

	// If step is assigned, auto-acknowledge to in_progress first.
	if exec.Status == domain.StepStatusAssigned {
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
	}

	// Submit: transition to completed.
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

	if err := o.events.Emit(ctx, domain.Event{
		EventID:   fmt.Sprintf("evt-%s-%s-completed", run.TraceID[:12], exec.StepID),
		Type:      domain.EventStepCompleted,
		Timestamp: now,
		RunID:     exec.RunID,
		TraceID:   run.TraceID,
	}); err != nil {
		log.Warn("failed to emit event", "event_type", domain.EventStepCompleted, "error", err)
	}

	// Determine next step from outcome.
	nextStepID := outcome.NextStep
	if nextStepID == "" {
		nextStepID = "end"
	}

	// Route: advance the run.
	hasCommit := len(outcome.Commit) > 0
	if nextStepID == "end" {
		// Terminal — complete the run.
		if err := o.CompleteRun(ctx, exec.RunID, hasCommit); err != nil {
			return fmt.Errorf("complete run: %w", err)
		}
	} else {
		// Non-terminal — create next step, update current_step_id, and activate.
		nextExec := &domain.StepExecution{
			ExecutionID: fmt.Sprintf("%s-%s-1", exec.RunID, nextStepID),
			RunID:       exec.RunID,
			StepID:      nextStepID,
			Status:      domain.StepStatusWaiting,
			Attempt:     1,
			CreatedAt:   now,
		}
		if err := o.store.CreateStepExecution(ctx, nextExec); err != nil {
			return fmt.Errorf("create next step: %w", err)
		}

		// Persist current_step_id so the scheduler can resume after restart.
		if err := o.store.UpdateCurrentStep(ctx, exec.RunID, nextStepID); err != nil {
			return fmt.Errorf("update current step: %w", err)
		}

		log.Info("step progressed",
			"run_id", exec.RunID,
			"completed_step", exec.StepID,
			"next_step", nextStepID,
		)

		// Activate the next step (deliver actor assignment).
		if err := o.ActivateStep(ctx, exec.RunID, nextStepID); err != nil {
			log.Warn("next step activation failed", "step_id", nextStepID, "error", err)
		}
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
// Returns true if all pass, false if any fail.
func (o *Orchestrator) evaluatePreconditions(ctx context.Context, step *domain.StepDefinition, run *domain.Run) bool {
	if len(step.Preconditions) == 0 {
		return true
	}

	log := observe.Logger(ctx)

	for _, precond := range step.Preconditions {
		switch precond.Type {
		case "artifact_status":
			if !o.checkArtifactStatus(ctx, precond.Config, run) {
				log.Info("precondition failed", "type", precond.Type, "config", precond.Config)
				return false
			}
		case "field_present":
			if !o.checkFieldPresent(ctx, precond.Config, run) {
				log.Info("precondition failed", "type", precond.Type, "config", precond.Config)
				return false
			}
		case "field_value":
			if !o.checkFieldValue(ctx, precond.Config, run) {
				log.Info("precondition failed", "type", precond.Type, "config", precond.Config)
				return false
			}
		case "links_exist":
			if !o.checkLinksExist(ctx, precond.Config, run) {
				log.Info("precondition failed", "type", precond.Type, "config", precond.Config)
				return false
			}
		default:
			// Unknown precondition types are skipped (forward-compatible).
			log.Info("unknown precondition type skipped", "type", precond.Type)
		}
	}
	return true
}

// checkArtifactStatus verifies an artifact at config["path"] has config["status"].
func (o *Orchestrator) checkArtifactStatus(ctx context.Context, config map[string]string, run *domain.Run) bool {
	path := config["path"]
	if path == "" {
		path = run.TaskPath
	}
	expectedStatus := config["status"]

	art, err := o.artifacts.Read(ctx, path, "HEAD")
	if err != nil {
		return false
	}
	return string(art.Status) == expectedStatus
}

// checkFieldPresent verifies an artifact at config["path"] has a non-empty metadata field.
func (o *Orchestrator) checkFieldPresent(ctx context.Context, config map[string]string, run *domain.Run) bool {
	path := config["path"]
	if path == "" {
		path = run.TaskPath
	}
	field := config["field"]

	art, err := o.artifacts.Read(ctx, path, "HEAD")
	if err != nil {
		return false
	}
	return art.Metadata[field] != ""
}

// checkFieldValue verifies an artifact field matches an expected value.
func (o *Orchestrator) checkFieldValue(ctx context.Context, config map[string]string, run *domain.Run) bool {
	path := config["path"]
	if path == "" {
		path = run.TaskPath
	}
	field := config["field"]
	expected := config["value"]

	art, err := o.artifacts.Read(ctx, path, "HEAD")
	if err != nil {
		return false
	}
	return art.Metadata[field] == expected
}

// checkLinksExist verifies an artifact has at least one link of the given type.
func (o *Orchestrator) checkLinksExist(ctx context.Context, config map[string]string, run *domain.Run) bool {
	path := config["path"]
	if path == "" {
		path = run.TaskPath
	}
	linkType := config["link_type"]

	art, err := o.artifacts.Read(ctx, path, "HEAD")
	if err != nil {
		return false
	}
	for _, link := range art.Links {
		if link.Type == domain.LinkType(linkType) {
			return true
		}
	}
	return false
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
