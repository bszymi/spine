package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/workflow"
)

// AcknowledgeRequest represents a request to acknowledge a step execution.
type AcknowledgeRequest struct {
	ActorID     string
	ExecutionID string
}

// AcknowledgeResult is returned after a successful acknowledge.
type AcknowledgeResult struct {
	ExecutionID string
	StepID      string
	Status      string
	StartedAt   *time.Time
}

// AcknowledgeStep transitions an assigned step execution to in_progress, recording
// the started_at timestamp and emitting EventStepStarted. Only the actor currently
// assigned to the step may acknowledge it.
func (o *Orchestrator) AcknowledgeStep(ctx context.Context, req AcknowledgeRequest) (*AcknowledgeResult, error) {
	if req.ActorID == "" {
		return nil, domain.NewError(domain.ErrInvalidParams, "actor_id is required")
	}
	if req.ExecutionID == "" {
		return nil, domain.NewError(domain.ErrInvalidParams, "execution_id is required")
	}

	exec, err := o.store.GetStepExecution(ctx, req.ExecutionID)
	if err != nil {
		return nil, fmt.Errorf("get step execution: %w", err)
	}

	// Only the assigned actor may acknowledge.
	if exec.ActorID != req.ActorID {
		return nil, domain.NewError(domain.ErrForbidden,
			fmt.Sprintf("actor %q is not assigned to step execution %s", req.ActorID, req.ExecutionID))
	}

	// Idempotency: acknowledging an already-in_progress step is a no-op.
	// The same actor calling acknowledge twice (e.g., after a network retry)
	// should receive the current state rather than an error.
	if exec.Status == domain.StepStatusInProgress {
		return &AcknowledgeResult{
			ExecutionID: exec.ExecutionID,
			StepID:      exec.StepID,
			Status:      string(exec.Status),
			StartedAt:   exec.StartedAt,
		}, nil
	}

	// Validate state transition (returns 409 if not in assigned state).
	if _, err := workflow.EvaluateStepTransition(exec.Status, workflow.StepTransitionRequest{
		Trigger: workflow.StepTriggerAcknowledged,
	}); err != nil {
		return nil, err
	}

	now := time.Now()
	exec.Status = domain.StepStatusInProgress
	exec.StartedAt = &now
	if err := o.store.UpdateStepExecution(ctx, exec); err != nil {
		return nil, fmt.Errorf("update step execution: %w", err)
	}

	run, err := o.store.GetRun(ctx, exec.RunID)
	if err != nil {
		return nil, fmt.Errorf("get run: %w", err)
	}
	o.emitEvent(ctx, domain.EventStepStarted, exec.RunID, run.TraceID,
		fmt.Sprintf("evt-%s-started", exec.ExecutionID), nil)

	return &AcknowledgeResult{
		ExecutionID: exec.ExecutionID,
		StepID:      exec.StepID,
		Status:      string(exec.Status),
		StartedAt:   exec.StartedAt,
	}, nil
}
