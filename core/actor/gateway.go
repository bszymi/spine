package actor

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/event"
	"github.com/bszymi/spine/core/observe"
	"github.com/bszymi/spine/core/queue"
)

// Gateway delivers step assignments to actors and processes results.
// Per Actor Model §5.
type Gateway struct {
	store  store.Store
	events event.EventRouter
	queue  queue.Queue
	actors *Service
}

// NewGateway creates a new actor gateway.
func NewGateway(st store.Store, events event.EventRouter, q queue.Queue, actors *Service) *Gateway {
	return &Gateway{
		store:  st,
		events: events,
		queue:  q,
		actors: actors,
	}
}

// DeliverAssignment publishes a step assignment for an actor.
func (g *Gateway) DeliverAssignment(ctx context.Context, req AssignmentRequest) error {
	log := observe.Logger(ctx)

	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal assignment: %w", err)
	}

	// Publish to queue for delivery
	if err := g.queue.Publish(ctx, queue.Entry{
		EntryID:        req.AssignmentID,
		EntryType:      "step_assignment",
		Payload:        payload,
		IdempotencyKey: req.AssignmentID,
		CreatedAt:      time.Now(),
	}); err != nil {
		return fmt.Errorf("publish assignment: %w", err)
	}

	// Emit step_assigned event
	eventPayload, _ := json.Marshal(map[string]any{
		"step_id":   req.StepID,
		"step_name": req.StepName,
		"actor_id":  req.ActorID,
		"step_type": req.StepType,
	})
	event.EmitLogged(ctx, g.events, domain.Event{
		EventID: fmt.Sprintf("assigned-%s", req.AssignmentID),
		Type:    domain.EventStepAssigned,
		RunID:   req.RunID,
		TraceID: req.TraceID,
		ActorID: req.ActorID,
		Payload: eventPayload,
	})

	log.Info("assignment delivered",
		"assignment_id", req.AssignmentID,
		"actor_id", req.ActorID,
		"step_id", req.StepID,
	)
	return nil
}

// stepTransitioner is the store's optional conditional step write (the
// method engine.StepExecutionTransitioner names; core/actor cannot import
// core/engine).
type stepTransitioner interface {
	TransitionStepExecution(ctx context.Context, exec *domain.StepExecution, fromStatus domain.StepExecutionStatus, fromActorID string) (bool, error)
}

// ProcessResult validates and processes a step result from an actor.
func (g *Gateway) ProcessResult(ctx context.Context, req AssignmentRequest, result AssignmentResult) error {
	log := observe.Logger(ctx)

	// Validate result against assignment
	if err := ValidateResult(req, result); err != nil {
		return err
	}

	// Look up step execution by execution ID (which is the assignment ID for the current attempt)
	// The execution_id format is "{run_id}-{step_id}-{attempt}" per the run start handler
	exec, err := g.store.GetStepExecution(ctx, req.AssignmentID)
	if err != nil {
		return fmt.Errorf("get step execution: %w", err)
	}

	// Idempotent: if already completed, no-op
	if exec.Status == domain.StepStatusCompleted {
		log.Info("duplicate result submission (idempotent)", "assignment_id", req.AssignmentID)
		return nil
	}

	// INIT-023 TASK-008 (cheap guards; no production caller in Spine or
	// SMP): a failed or skipped execution is never overwritten to
	// completed, a result is accepted only for a live (assigned or
	// in_progress) execution, and a bound execution only from its own
	// actor.
	if exec.Status != domain.StepStatusAssigned && exec.Status != domain.StepStatusInProgress {
		return domain.NewError(domain.ErrConflict,
			fmt.Sprintf("step execution %s is %s; result not accepted", exec.ExecutionID, exec.Status))
	}
	if exec.ActorID != "" && exec.ActorID != result.ActorID {
		return domain.NewError(domain.ErrForbidden,
			fmt.Sprintf("step execution %s is not assigned to the submitting actor", exec.ExecutionID))
	}
	fromStatus, fromActorID := exec.Status, exec.ActorID

	// Update step execution with result: conditional on the state read
	// above when the store can (the standalone PostgresStore), so a
	// result that lost a race to another writer changes nothing.
	now := time.Now()
	exec.Status = domain.StepStatusCompleted
	exec.OutcomeID = result.OutcomeID
	exec.CompletedAt = &now
	if t, ok := g.store.(stepTransitioner); ok {
		applied, err := t.TransitionStepExecution(ctx, exec, fromStatus, fromActorID)
		if err != nil {
			return fmt.Errorf("update step execution: %w", err)
		}
		if !applied {
			return domain.NewError(domain.ErrConflict,
				fmt.Sprintf("step execution %s changed state concurrently; result not accepted", exec.ExecutionID))
		}
	} else if err := g.store.UpdateStepExecution(ctx, exec); err != nil {
		return fmt.Errorf("update step execution: %w", err)
	}

	// Emit step_completed event
	eventPayload, _ := json.Marshal(map[string]any{
		"step_id":   req.StepID,
		"step_name": req.StepName,
		"outcome":   result.OutcomeID,
		"actor_id":  result.ActorID,
		"summary":   result.Summary,
	})
	event.EmitLogged(ctx, g.events, domain.Event{
		EventID: fmt.Sprintf("completed-%s", req.AssignmentID),
		Type:    domain.EventStepCompleted,
		RunID:   req.RunID,
		TraceID: req.TraceID,
		ActorID: result.ActorID,
		Payload: eventPayload,
	})

	log.Info("result processed",
		"assignment_id", req.AssignmentID,
		"outcome_id", result.OutcomeID,
	)
	return nil
}
