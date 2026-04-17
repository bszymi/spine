package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/event"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/workflow"
)

// ScanTimeouts checks all active step executions for timeout expiry.
// Per Engine State Machine §6.3.
func (s *Scheduler) ScanTimeouts(ctx context.Context) error {
	log := observe.Logger(ctx)
	observe.GlobalMetrics.SchedulerScans.Inc()

	execs, err := s.store.ListActiveStepExecutions(ctx)
	if err != nil {
		return fmt.Errorf("list active step executions: %w", err)
	}

	for i := range execs {
		exec := &execs[i]

		// Use StartedAt if available, otherwise CreatedAt.
		// Waiting/assigned steps don't have StartedAt but can still time out per §3.2.
		refTime := exec.CreatedAt
		if exec.StartedAt != nil {
			refTime = *exec.StartedAt
		}

		stepDef, err := s.lookupStepDefinition(ctx, exec)
		if err != nil {
			log.Error("lookup step definition failed", "execution_id", exec.ExecutionID, "error", err)
			continue
		}
		if stepDef == nil || stepDef.Timeout == "" {
			continue // no timeout configured
		}

		timeout, err := time.ParseDuration(stepDef.Timeout)
		if err != nil {
			log.Error("invalid timeout duration", "step_id", exec.StepID, "timeout", stepDef.Timeout, "error", err)
			continue
		}

		if time.Since(refTime) <= timeout {
			continue // not yet expired
		}

		if err := s.handleStepTimeout(ctx, exec, stepDef); err != nil {
			log.Error("handle step timeout failed", "execution_id", exec.ExecutionID, "error", err)
		}
	}

	return nil
}

func (s *Scheduler) handleStepTimeout(ctx context.Context, exec *domain.StepExecution, stepDef *domain.StepDefinition) error {
	log := observe.Logger(ctx)

	hasTimeoutOutcome := stepDef.TimeoutOutcome != ""
	result, err := workflow.EvaluateStepTransition(exec.Status, workflow.StepTransitionRequest{
		Trigger:           workflow.StepTriggerTimeout,
		HasTimeoutOutcome: hasTimeoutOutcome,
	})
	if err != nil {
		return fmt.Errorf("evaluate step transition: %w", err)
	}

	now := time.Now()
	exec.Status = result.ToStatus
	exec.CompletedAt = &now
	if !hasTimeoutOutcome {
		exec.ErrorDetail = &domain.ErrorDetail{
			Classification: domain.FailureTimeout,
			Message:        fmt.Sprintf("step timed out after %s", stepDef.Timeout),
			StepID:         exec.StepID,
		}
	} else {
		exec.OutcomeID = stepDef.TimeoutOutcome
	}

	if err := s.store.UpdateStepExecution(ctx, exec); err != nil {
		return fmt.Errorf("update step execution: %w", err)
	}

	observe.GlobalMetrics.TimeoutsDetected.Inc()
	log.Info("step timed out",
		"execution_id", exec.ExecutionID,
		"step_id", exec.StepID,
		"run_id", exec.RunID,
		"to_status", result.ToStatus,
	)

	payload, _ := json.Marshal(map[string]string{
		"step_id":      exec.StepID,
		"execution_id": exec.ExecutionID,
		"timeout":      stepDef.Timeout,
	})
	event.EmitLogged(ctx, s.events, domain.Event{
		EventID:   fmt.Sprintf("timeout-%s", exec.ExecutionID),
		Type:      domain.EventStepTimeout,
		Timestamp: now,
		RunID:     exec.RunID,
		Payload:   payload,
	})

	return nil
}

// lookupStepDefinition loads the workflow definition from the projection store
// and returns the StepDefinition for the given step execution.
func (s *Scheduler) lookupStepDefinition(ctx context.Context, exec *domain.StepExecution) (*domain.StepDefinition, error) {
	run, err := s.store.GetRun(ctx, exec.RunID)
	if err != nil {
		return nil, fmt.Errorf("get run: %w", err)
	}

	proj, err := s.store.GetWorkflowProjection(ctx, run.WorkflowPath)
	if err != nil {
		return nil, fmt.Errorf("get workflow projection: %w", err)
	}

	var wfDef domain.WorkflowDefinition
	if err := json.Unmarshal(proj.Definition, &wfDef); err != nil {
		return nil, fmt.Errorf("unmarshal workflow definition: %w", err)
	}

	for i := range wfDef.Steps {
		if wfDef.Steps[i].ID == exec.StepID {
			return &wfDef.Steps[i], nil
		}
	}
	return nil, nil // step not found in definition
}
