package gateway

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/engine"
	"github.com/bszymi/spine/internal/store"
	"github.com/bszymi/spine/internal/workflow"
)

// storeStepAssigner is the gateway's built-in StepAssigner fallback. It's
// used when no engine orchestrator is wired (single-workspace test setups,
// operator-only deployments) so handleStepAssign has a working path. It
// performs the same state-machine transition the orchestrator's AssignStep
// does, against the request-scoped store.
type storeStepAssigner struct {
	st store.Store
}

// assignerFor picks a StepAssigner for the current request. Priority:
// 1. The configured orchestrator-backed assigner (engine-side).
// 2. A per-request fallback backed by the resolved store.
// Returns nil only when neither an assigner nor a store is available.
func (s *Server) assignerFor(ctx context.Context) StepAssigner {
	if s.stepAssigner != nil {
		return s.stepAssigner
	}
	st := s.storeFrom(ctx)
	if st == nil {
		return nil
	}
	return &storeStepAssigner{st: st}
}

func (a *storeStepAssigner) AssignStep(ctx context.Context, req engine.AssignRequest) (*engine.AssignResult, error) {
	if req.ActorID == "" {
		return nil, domain.NewError(domain.ErrInvalidParams, "actor_id is required")
	}
	if req.RunID == "" || req.StepID == "" {
		return nil, domain.NewError(domain.ErrInvalidParams, "run_id and step_id are required")
	}

	execs, err := a.st.ListStepExecutionsByRun(ctx, req.RunID)
	if err != nil {
		return nil, err
	}
	// Walk all executions keeping the LAST match: store orders by
	// created_at, so after a retry the terminal first attempt sorts
	// before the new waiting attempt. Picking the latest match matches
	// the original handler's behaviour and avoids returning a conflict
	// error for retried steps.
	var exec *domain.StepExecution
	for i := range execs {
		if execs[i].StepID == req.StepID {
			exec = &execs[i]
		}
	}
	if exec == nil {
		return nil, domain.NewError(domain.ErrNotFound, "step execution not found")
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
	if err := a.st.UpdateStepExecution(ctx, exec); err != nil {
		return nil, fmt.Errorf("update step execution: %w", err)
	}
	return &engine.AssignResult{Exec: exec}, nil
}

func (a *storeStepAssigner) LookupStepDef(ctx context.Context, runID, stepID string) (*domain.StepDefinition, *domain.Run) {
	run, err := a.st.GetRun(ctx, runID)
	if err != nil || run == nil || run.WorkflowPath == "" {
		return nil, run
	}
	proj, err := a.st.GetWorkflowProjection(ctx, run.WorkflowPath)
	if err != nil {
		return nil, run
	}
	var wfDef domain.WorkflowDefinition
	if err := json.Unmarshal(proj.Definition, &wfDef); err != nil {
		return nil, run
	}
	for i := range wfDef.Steps {
		if wfDef.Steps[i].ID == stepID {
			return &wfDef.Steps[i], run
		}
	}
	return nil, run
}
