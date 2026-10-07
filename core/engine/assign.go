package engine

import (
	"context"
	"fmt"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/workflow"
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
//
// With the zero-value StepEntryPolicy (the default) precondition evaluation
// and actor eligibility are the caller's responsibility, exactly as before
// INIT-023: this method handles only the state-machine transition and the
// exec update, and loads neither the run nor the workflow.
//
// When the installed policy gates binding (any entry check on), the engine
// owns them: it loads the run and the pinned workflow (a step missing from
// it is ErrNotFound, so the bind fails closed) and runs
// checkStepEntryActor and checkStepEntryPreconditions before anything is
// written. The effective allow-list is req.EligibleActorIDs when non-empty,
// otherwise the stored exec.EligibleActorIDs — the list the call leaves in
// place. The assigner may therefore replace a stored list with one that
// admits itself (Owner question 5 default): step.assign is the only writer
// of the list, so this widens nothing a stronger control granted, and
// separation of duties applies whatever list is passed.
func (o *Orchestrator) AssignStep(ctx context.Context, req AssignRequest) (*AssignResult, error) {
	if req.ActorID == "" {
		return nil, domain.NewError(domain.ErrInvalidParams, "actor_id is required")
	}
	if req.RunID == "" || req.StepID == "" {
		return nil, domain.NewError(domain.ErrInvalidParams, "run_id and step_id are required")
	}

	gated := o.entryPolicy.gatesBinding()
	if gated && (o.store == nil || o.wfLoader == nil) {
		// Only reachable for hand-built &Orchestrator{} values: engine.New
		// requires both. Refuse rather than nil-dereference or bind unchecked.
		return nil, domain.NewError(domain.ErrUnavailable, "step-entry policy needs a run store and a workflow loader")
	}

	exec, err := o.findStepExecution(ctx, req.RunID, req.StepID)
	if err != nil {
		return nil, err
	}

	if gated {
		run, err := o.store.GetRun(ctx, req.RunID)
		if err != nil {
			return nil, fmt.Errorf("get run: %w", err)
		}
		wfDef, err := o.wfLoader.LoadWorkflow(ctx, run.WorkflowPath, run.WorkflowVersion)
		if err != nil {
			return nil, fmt.Errorf("load workflow: %w", err)
		}
		stepDef := findStepDef(wfDef, req.StepID)
		if stepDef == nil {
			return nil, domain.NewError(domain.ErrNotFound,
				fmt.Sprintf("step %q not found in workflow %s", req.StepID, wfDef.ID))
		}
		allow := req.EligibleActorIDs
		if len(allow) == 0 {
			allow = exec.EligibleActorIDs
		}
		if err := o.checkStepEntryActor(ctx, entryAssign, run, wfDef, stepDef, exec, req.ActorID, allow); err != nil {
			return nil, err
		}
		if err := o.checkStepEntryPreconditions(ctx, run, stepDef); err != nil {
			return nil, err
		}
	}

	// Recovery for pre-Option-B phantom rows: a step persisted as
	// `assigned` or `in_progress` with an empty `actor_id` is treated
	// as an open slot. /assign binds an actor without invoking the
	// state-machine transition (which rejects step.assign from those
	// states). Both phantom shapes are reachable from old engine
	// behavior — `assigned` from ActivateStep, `in_progress` if
	// SubmitStepResult auto-acknowledged a phantom and the process
	// crashed before completion. Without this, the operator has no
	// recovery path beyond direct DB edits
	// (INIT-020/EPIC-001/TASK-004).
	openSlot := (exec.Status == domain.StepStatusAssigned || exec.Status == domain.StepStatusInProgress) && exec.ActorID == ""
	if !openSlot {
		result, err := workflow.EvaluateStepTransition(exec.Status, workflow.StepTransitionRequest{
			Trigger: workflow.StepTriggerAssign,
		})
		if err != nil {
			return nil, err
		}
		exec.Status = result.ToStatus
	}

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
