package engine

import (
	"context"
	"fmt"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/workflow"
)

// AssignRequest represents a manual step assignment — an operator or workflow
// definition selects an actor for a specific step execution. Unlike ClaimStep
// (pull-based by the actor itself) or ActivateStep (engine-driven with
// auto-selection + assignment delivery), AssignStep is the explicit
// third-party override: "this actor is responsible for this step".
type AssignRequest struct {
	RunID            string
	StepID           string
	ActorID          string
	EligibleActorIDs []string
}

// AssignResult is returned on success.
type AssignResult struct {
	Exec *domain.StepExecution
}

// AssignStep assigns an actor to the named step execution under the run.
// Precondition evaluation is the caller's responsibility (the HTTP handler
// owns the precondition model that was in place before extraction); this
// method handles the state-machine transition and the exec update.
func (o *Orchestrator) AssignStep(ctx context.Context, req AssignRequest) (*AssignResult, error) {
	if req.ActorID == "" {
		return nil, domain.NewError(domain.ErrInvalidParams, "actor_id is required")
	}
	if req.RunID == "" || req.StepID == "" {
		return nil, domain.NewError(domain.ErrInvalidParams, "run_id and step_id are required")
	}

	exec, err := o.findStepExecution(ctx, req.RunID, req.StepID)
	if err != nil {
		return nil, err
	}

	result, err := workflow.EvaluateStepTransition(exec.Status, workflow.StepTransitionRequest{
		Trigger: workflow.StepTriggerAssign,
	})
	if err != nil {
		return nil, err
	}

	exec.Status = result.ToStatus
	exec.ActorID = req.ActorID
	if len(req.EligibleActorIDs) > 0 {
		exec.EligibleActorIDs = req.EligibleActorIDs
	}
	if err := o.store.UpdateStepExecution(ctx, exec); err != nil {
		return nil, fmt.Errorf("update step execution: %w", err)
	}

	return &AssignResult{Exec: exec}, nil
}

// LookupStepDef loads the StepDefinition for a given run+step via the
// workflow loader and findStepDef. Returns (nil, nil) when the run or
// workflow cannot be located, matching the behaviour of the gateway's
// prior resolveStepDef (which treated missing references as "no
// precondition context available" rather than an error).
func (o *Orchestrator) LookupStepDef(ctx context.Context, runID, stepID string) (*domain.StepDefinition, *domain.Run) {
	run, err := o.store.GetRun(ctx, runID)
	if err != nil || run == nil || run.WorkflowPath == "" {
		return nil, run
	}
	wfDef, err := o.wfLoader.LoadWorkflow(ctx, run.WorkflowPath, run.WorkflowVersion)
	if err != nil {
		return nil, run
	}
	return findStepDef(wfDef, stepID), run
}
