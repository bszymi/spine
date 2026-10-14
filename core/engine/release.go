package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/observe"
)

// ReleaseRequest represents a request to release a step assignment.
type ReleaseRequest struct {
	ActorID      string
	AssignmentID string
	Reason       string
}

// ReleaseStep allows an actor to release a claimed or assigned step execution
// back into the pool. The step transitions back to waiting and the releasing
// actor is excluded from immediate re-assignment.
func (o *Orchestrator) ReleaseStep(ctx context.Context, req ReleaseRequest) error {
	log := observe.Logger(ctx)

	if req.ActorID == "" {
		return domain.NewError(domain.ErrInvalidParams, "actor_id is required")
	}
	if req.AssignmentID == "" {
		return domain.NewError(domain.ErrInvalidParams, "assignment_id is required")
	}

	if o.assignments == nil {
		return domain.NewError(domain.ErrUnavailable, "assignment tracking not configured")
	}

	// Load the assignment.
	assignment, err := o.assignments.GetAssignment(ctx, req.AssignmentID)
	if err != nil {
		return fmt.Errorf("get assignment: %w", err)
	}

	// Validate the actor is the current assignee.
	if assignment.ActorID != req.ActorID {
		return domain.NewError(domain.ErrForbidden,
			fmt.Sprintf("actor %q is not the assignee of assignment %s (assigned to %q)", req.ActorID, req.AssignmentID, assignment.ActorID))
	}

	// Validate assignment is active (not already completed/cancelled/timed_out).
	if assignment.Status != domain.AssignmentStatusActive {
		return domain.NewError(domain.ErrConflict,
			fmt.Sprintf("assignment %s is in status %q, cannot release", req.AssignmentID, assignment.Status))
	}

	// Load the step execution to verify it's not in a terminal state.
	exec, err := o.store.GetStepExecution(ctx, assignment.ExecutionID)
	if err != nil {
		return fmt.Errorf("get step execution: %w", err)
	}
	// The execution itself must still be bound to the releasing actor
	// (INIT-023 TASK-008), not only the assignment row: startup recovery
	// resets assigned→waiting without clearing actor_id or the assignment
	// row, and AssignStep then binds another actor without creating a row,
	// so A's stale active claim would otherwise release B's step. An
	// unbound execution (empty actor) keeps today's behaviour.
	if exec.ActorID != "" && exec.ActorID != req.ActorID {
		return domain.NewError(domain.ErrForbidden,
			fmt.Sprintf("step execution %s is not assigned to actor %q", assignment.ExecutionID, req.ActorID))
	}
	if exec.Status.IsTerminal() {
		return domain.NewError(domain.ErrConflict,
			fmt.Sprintf("step execution %s is in terminal state %q, cannot release", assignment.ExecutionID, exec.Status))
	}
	prior := stateOf(exec)
	original := *exec

	now := time.Now()
	if o.stepTransitioner() == nil {
		// Blind store (no compare-and-swap capability): today's order —
		// the assignment row is cancelled first, then the step is written.
		if err := o.assignments.UpdateAssignmentStatus(ctx, req.AssignmentID, domain.AssignmentStatusCancelled, &now); err != nil {
			return fmt.Errorf("cancel assignment: %w", err)
		}
	}

	// Transition step execution back to waiting.
	exec.Status = domain.StepStatusWaiting
	exec.ActorID = ""
	exec.StartedAt = nil
	if err := o.writeStepExecution(ctx, exec, prior); err != nil {
		return fmt.Errorf("update step execution: %w", err)
	}
	if o.stepTransitioner() != nil {
		// Conditional store: the step write comes first, so a lost race
		// (a submit or bind won) leaves both the execution and the
		// assignment row untouched; the row is cancelled only after the
		// write applied.
		if err := o.assignments.UpdateAssignmentStatus(ctx, req.AssignmentID, domain.AssignmentStatusCancelled, &now); err != nil {
			// Compensate (codex review, round 2): an unbound waiting
			// execution next to a still-active assignment row would block
			// every later claim (the active-assignment unique index), and
			// once another actor is bound the owner guard above stops this
			// actor's retry from cancelling the row. Put the execution back
			// as it was read — conditionally, so a writer that took the
			// released step meanwhile is not overwritten — and report the
			// failure; the caller can retry the release.
			if cerr := o.writeStepExecution(ctx, &original, stateOf(exec)); cerr != nil {
				log.Error("release: assignment not cancelled and the step could not be restored",
					"assignment_id", req.AssignmentID, "execution_id", exec.ExecutionID, "error", err, "restore_error", cerr)
			}
			return fmt.Errorf("cancel assignment: %w", err)
		}
	}

	// Emit release event.
	run, _ := o.store.GetRun(ctx, assignment.RunID)
	traceID := ""
	if run != nil {
		traceID = run.TraceID
	}
	payload, _ := json.Marshal(map[string]any{
		"assignment_id": req.AssignmentID,
		"actor_id":      req.ActorID,
		"execution_id":  assignment.ExecutionID,
		"reason":        req.Reason,
	})
	o.emitEvent(ctx, domain.EventTaskReleased, assignment.RunID, traceID,
		fmt.Sprintf("evt-released-%s", req.AssignmentID), payload)

	log.Info("step released",
		"assignment_id", req.AssignmentID,
		"actor_id", req.ActorID,
		"execution_id", assignment.ExecutionID,
		"reason", req.Reason,
	)

	// Update execution projection.
	if run != nil {
		o.updateExecutionProjection(ctx, run.TaskPath, func(proj *store.ExecutionProjection) {
			proj.AssignedActorID = ""
			proj.AssignmentStatus = "unassigned"
		})
	}

	return nil
}
