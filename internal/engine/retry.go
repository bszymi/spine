package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/scheduler"
	"github.com/bszymi/spine/internal/workflow"
)

// RetryStep evaluates whether a failed step should be retried based on the
// workflow's retry configuration and the failure classification. If retryable,
// it creates a new step execution with a backoff delay. If not, it fails the run.
func (o *Orchestrator) RetryStep(ctx context.Context, exec *domain.StepExecution) error {
	log := observe.Logger(ctx)

	if exec.Status != domain.StepStatusFailed {
		return domain.NewError(domain.ErrConflict,
			fmt.Sprintf("cannot retry step in %s status", exec.Status))
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

	retryLimit := 0
	backoffType := "exponential"
	if stepDef.Retry != nil {
		retryLimit = stepDef.Retry.Limit
		if stepDef.Retry.Backoff != "" {
			backoffType = stepDef.Retry.Backoff
		}
	}

	classification := domain.FailureTransient
	if exec.ErrorDetail != nil {
		classification = exec.ErrorDetail.Classification
	}

	if !workflow.ShouldRetry(exec.Attempt, retryLimit, classification) {
		log.Info("retry exhausted or not retryable, failing run",
			"run_id", exec.RunID,
			"step_id", exec.StepID,
			"attempt", exec.Attempt,
			"retry_limit", retryLimit,
			"classification", classification,
		)

		o.emitEvent(ctx, domain.EventStepFailed, exec.RunID, run.TraceID,
			fmt.Sprintf("evt-%s-failed", exec.ExecutionID), nil)

		return o.FailRun(ctx, exec.RunID,
			fmt.Sprintf("step %s failed after %d attempt(s): %s",
				exec.StepID, exec.Attempt, classification))
	}

	// Calculate backoff delay.
	delay := scheduler.CalculateBackoff(exec.Attempt, backoffType)
	retryAfter := time.Now().Add(delay)
	nextAttempt := exec.Attempt + 1

	// Create a new step execution for the retry attempt.
	// Preserve eligible_actor_ids so the same actor restriction applies on retry.
	nextExec := &domain.StepExecution{
		ExecutionID:      fmt.Sprintf("%s-%s-%d", exec.RunID, exec.StepID, nextAttempt),
		RunID:            exec.RunID,
		StepID:           exec.StepID,
		BranchID:         exec.BranchID,
		EligibleActorIDs: exec.EligibleActorIDs,
		Status:           domain.StepStatusWaiting,
		Attempt:          nextAttempt,
		RetryAfter:       &retryAfter,
		CreatedAt:        time.Now(),
	}
	if err := o.store.CreateStepExecution(ctx, nextExec); err != nil {
		return fmt.Errorf("create retry execution: %w", err)
	}

	o.emitEvent(ctx, domain.EventRetryAttempted, exec.RunID, run.TraceID,
		fmt.Sprintf("evt-%s-%s-retry-%d", run.TraceID[:12], exec.StepID, nextAttempt), nil)

	log.Info("retry scheduled",
		"run_id", exec.RunID,
		"step_id", exec.StepID,
		"attempt", nextAttempt,
		"retry_after", retryAfter,
		"backoff", backoffType,
	)

	return nil
}
