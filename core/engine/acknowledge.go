package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/workflow"
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
	prior := stateOf(exec) // the write below is conditional on it (INIT-023 TASK-008)

	if res, done, err := o.acknowledgeAnswer(ctx, req, exec); done {
		return res, err
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
	if err := o.writeStepExecution(ctx, exec, prior); err != nil {
		if isStepStateChanged(err) {
			return o.lostAcknowledgeRace(ctx, req, fmt.Errorf("update step execution: %w", err))
		}
		return nil, fmt.Errorf("update step execution: %w", err)
	}

	run, err := o.store.GetRun(ctx, exec.RunID)
	if err != nil {
		return nil, fmt.Errorf("get run: %w", err)
	}
	o.emitEvent(ctx, domain.EventStepStarted, exec.RunID, run.TraceID,
		fmt.Sprintf("evt-%s-started", exec.ExecutionID), nil)

	return acknowledged(exec), nil
}

// acknowledgeAnswer is AcknowledgeStep's answer that needs no write, for
// the execution as read: done is true when (res, err) is the answer — the
// ownership refusal, TASK-007's run-status refusal, or the idempotent
// in_progress result — and false when the step must be transitioned.
func (o *Orchestrator) acknowledgeAnswer(ctx context.Context, req AcknowledgeRequest, exec *domain.StepExecution) (*AcknowledgeResult, bool, error) {
	// Only the assigned actor may acknowledge.
	if exec.ActorID != req.ActorID {
		return nil, true, domain.NewError(domain.ErrForbidden,
			fmt.Sprintf("actor %q is not assigned to step execution %s", req.ActorID, req.ExecutionID))
	}

	// EnforceRunStatus (INIT-023 TASK-007): no work starts on a run that is
	// not active. Checked ahead of the idempotent in_progress answer too, so
	// a runner re-acknowledging on a dead run is told to stop rather than
	// that it may carry on. The run is loaded only when the flag is on.
	if o.entryPolicy.EnforceRunStatus {
		run, err := o.store.GetRun(ctx, exec.RunID)
		if err != nil {
			return nil, true, fmt.Errorf("get run: %w", err)
		}
		if err := o.requireActiveRun(run, "acknowledged"); err != nil {
			return nil, true, err
		}
	}

	// Idempotency: acknowledging an already-in_progress step is a no-op.
	// The same actor calling acknowledge twice (e.g., after a network retry)
	// should receive the current state rather than an error.
	if exec.Status == domain.StepStatusInProgress {
		return acknowledged(exec), true, nil
	}
	return nil, false, nil
}

// lostAcknowledgeRace answers an acknowledgment whose conditional write
// missed (INIT-023 TASK-008 review round 1; only with the step capability —
// a blind store never misses). Decision D9's parity rule, as for a submit
// (acknowledgedOrSame, lostSubmitRace): losing to the same actor's
// identical transition — a concurrent retry of this acknowledge, or the
// actor's own submit auto-acknowledging — is not a conflict, because the
// sequential retry of it gets the idempotent in_progress answer; so this
// one gets it too, judged on the re-read row exactly as a fresh call
// would be (TASK-007's run-status refusal included), and emits no second
// step_started (the winner did). Any other move — released, rebound,
// completed, failed, terminated — keeps the conflict: the acknowledgment
// was not applied and the step is not the caller's to start.
func (o *Orchestrator) lostAcknowledgeRace(ctx context.Context, req AcknowledgeRequest, raceErr error) (*AcknowledgeResult, error) {
	cur, err := o.store.GetStepExecution(ctx, req.ExecutionID)
	if err != nil {
		return nil, fmt.Errorf("re-read step execution: %w", err)
	}
	if cur.Status != domain.StepStatusInProgress || cur.ActorID != req.ActorID {
		return nil, raceErr
	}
	res, _, err := o.acknowledgeAnswer(ctx, req, cur)
	return res, err
}

func acknowledged(exec *domain.StepExecution) *AcknowledgeResult {
	return &AcknowledgeResult{
		ExecutionID: exec.ExecutionID,
		StepID:      exec.StepID,
		Status:      string(exec.Status),
		StartedAt:   exec.StartedAt,
	}
}
