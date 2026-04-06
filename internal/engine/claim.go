package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
)

// ClaimRequest represents a request to claim a step execution.
type ClaimRequest struct {
	ActorID     string
	ExecutionID string
}

// ClaimResult represents the result of a successful claim.
type ClaimResult struct {
	Assignment *domain.Assignment
	StepID     string
	RunID      string
}

// ClaimStep allows an actor to claim a waiting step execution. This is the
// pull-based complement to the push-based DeliverAssignment. The claim is
// atomic — if two actors claim simultaneously, one succeeds and the other
// gets a conflict error.
func (o *Orchestrator) ClaimStep(ctx context.Context, req ClaimRequest) (*ClaimResult, error) {
	log := observe.Logger(ctx)

	if req.ActorID == "" {
		return nil, domain.NewError(domain.ErrInvalidParams, "actor_id is required")
	}
	if req.ExecutionID == "" {
		return nil, domain.NewError(domain.ErrInvalidParams, "execution_id is required")
	}

	// Load the step execution.
	exec, err := o.store.GetStepExecution(ctx, req.ExecutionID)
	if err != nil {
		return nil, fmt.Errorf("get step execution: %w", err)
	}

	// Validate claimable state.
	if exec.Status != domain.StepStatusWaiting {
		return nil, domain.NewError(domain.ErrConflict,
			fmt.Sprintf("step execution %s is in status %q, not claimable (must be waiting)", req.ExecutionID, exec.Status))
	}

	// Load the run for context.
	run, err := o.store.GetRun(ctx, exec.RunID)
	if err != nil {
		return nil, fmt.Errorf("get run: %w", err)
	}

	// Load workflow to get step definition for skill validation.
	wfDef, err := o.wfLoader.LoadWorkflow(ctx, run.WorkflowPath, run.WorkflowVersion)
	if err != nil {
		return nil, fmt.Errorf("load workflow: %w", err)
	}

	stepDef := findStepDef(wfDef, exec.StepID)
	if stepDef == nil {
		return nil, domain.NewError(domain.ErrNotFound,
			fmt.Sprintf("step %q not found in workflow %s", exec.StepID, wfDef.ID))
	}

	// Validate actor eligibility: type and skills.
	if stepDef.Execution != nil {
		actor, err := o.loadActor(ctx, req.ActorID)
		if err != nil {
			return nil, fmt.Errorf("load actor: %w", err)
		}

		// Check actor type.
		if len(stepDef.Execution.EligibleActorTypes) > 0 {
			eligible := false
			for _, at := range stepDef.Execution.EligibleActorTypes {
				if at == string(actor.Type) {
					eligible = true
					break
				}
			}
			if !eligible {
				return nil, domain.NewError(domain.ErrConflict,
					fmt.Sprintf("actor type %q is not eligible for this step (allowed: %v)", actor.Type, stepDef.Execution.EligibleActorTypes))
			}
		}

		// Check required skills.
		if len(stepDef.Execution.RequiredSkills) > 0 && o.blocking != nil {
			type skillChecker interface {
				ListActorSkills(ctx context.Context, actorID string) ([]domain.Skill, error)
			}
			if sc, ok := o.blocking.(skillChecker); ok {
				skills, err := sc.ListActorSkills(ctx, req.ActorID)
				if err != nil {
					return nil, fmt.Errorf("check actor skills: %w", err)
				}
				skillNames := make(map[string]bool, len(skills))
				for _, sk := range skills {
					if sk.Status == domain.SkillStatusActive {
						skillNames[sk.Name] = true
					}
				}
				var missing []string
				for _, req := range stepDef.Execution.RequiredSkills {
					if !skillNames[req] {
						missing = append(missing, req)
					}
				}
				if len(missing) > 0 {
					return nil, domain.NewError(domain.ErrConflict,
						fmt.Sprintf("actor missing required skills: %v", missing))
				}
			}
		}
	}

	now := time.Now()

	// Create assignment record FIRST — the unique index on (execution_id)
	// WHERE status = 'active' ensures only one active assignment per execution.
	// If a concurrent claim already inserted, this fails with a conflict.
	assignment := &domain.Assignment{
		AssignmentID: fmt.Sprintf("claim-%s-%s", req.ExecutionID, req.ActorID),
		RunID:        exec.RunID,
		ExecutionID:  req.ExecutionID,
		ActorID:      req.ActorID,
		Status:       domain.AssignmentStatusActive,
		AssignedAt:   now,
	}
	if o.assignments != nil {
		if err := o.assignments.CreateAssignment(ctx, assignment); err != nil {
			return nil, domain.NewError(domain.ErrConflict,
				fmt.Sprintf("step %s is already claimed (concurrent claim conflict)", req.ExecutionID))
		}
	}

	// Assignment succeeded — now update the step execution.
	exec.Status = domain.StepStatusAssigned
	exec.ActorID = req.ActorID
	exec.StartedAt = &now
	if err := o.store.UpdateStepExecution(ctx, exec); err != nil {
		return nil, fmt.Errorf("update step execution: %w", err)
	}

	// Emit event.
	payload, _ := json.Marshal(map[string]any{
		"step_id":      exec.StepID,
		"actor_id":     req.ActorID,
		"execution_id": req.ExecutionID,
		"claim":        true,
	})
	o.emitEvent(ctx, domain.EventStepAssigned, exec.RunID, run.TraceID,
		fmt.Sprintf("evt-%s-%s-claimed", run.TraceID[:12], exec.StepID), payload)

	log.Info("step claimed", "execution_id", req.ExecutionID, "actor_id", req.ActorID, "step_id", exec.StepID)

	return &ClaimResult{
		Assignment: assignment,
		StepID:     exec.StepID,
		RunID:      exec.RunID,
	}, nil
}

// loadActor loads an actor via the blocking store (which has access to full store).
func (o *Orchestrator) loadActor(ctx context.Context, actorID string) (*domain.Actor, error) {
	if o.blocking == nil {
		return nil, fmt.Errorf("actor lookup requires blocking store")
	}
	type actorLoader interface {
		GetActor(ctx context.Context, actorID string) (*domain.Actor, error)
	}
	loader, ok := o.blocking.(actorLoader)
	if !ok {
		return nil, fmt.Errorf("blocking store does not support actor lookup")
	}
	return loader.GetActor(ctx, actorID)
}
