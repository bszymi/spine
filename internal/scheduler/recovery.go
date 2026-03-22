package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/store"
	"github.com/bszymi/spine/internal/workflow"
)

// RecoveryResult tracks what the recovery sequence did.
type RecoveryResult struct {
	PendingActivated int
	ActiveResumed    int
	CommittingFound  int
	StepsRecovered   int
}

// RecoverOnStartup performs crash recovery per Engine State Machine §2.4 and §3.5.
// Called once at engine startup, before the scheduler polling loops begin.
func (s *Scheduler) RecoverOnStartup(ctx context.Context) (*RecoveryResult, error) {
	ctx = observe.WithComponent(ctx, "recovery")
	log := observe.Logger(ctx)
	log.Info("starting crash recovery")

	result := &RecoveryResult{}

	if err := s.recoverPendingRuns(ctx, result); err != nil {
		return result, fmt.Errorf("recover pending runs: %w", err)
	}

	if err := s.recoverActiveRuns(ctx, result); err != nil {
		return result, fmt.Errorf("recover active runs: %w", err)
	}

	if err := s.recoverCommittingRuns(ctx, result); err != nil {
		return result, fmt.Errorf("recover committing runs: %w", err)
	}

	observe.GlobalMetrics.RecoveriesExecuted.Inc()
	log.Info("crash recovery complete",
		"pending_activated", result.PendingActivated,
		"active_resumed", result.ActiveResumed,
		"committing_found", result.CommittingFound,
		"steps_recovered", result.StepsRecovered,
	)

	payload, _ := json.Marshal(map[string]int{
		"pending_activated": result.PendingActivated,
		"active_resumed":    result.ActiveResumed,
		"committing_found":  result.CommittingFound,
		"steps_recovered":   result.StepsRecovered,
	})
	if err := s.events.Emit(ctx, domain.Event{
		EventID:   fmt.Sprintf("recovery-%d", time.Now().UnixNano()),
		Type:      domain.EventEngineRecovered,
		Timestamp: time.Now(),
		Payload:   payload,
	}); err != nil {
		log.Warn("failed to emit engine_recovered event", "error", err)
	}

	return result, nil
}

// recoverPendingRuns activates runs stuck in pending state.
// Per §2.2: pending→active requires creating the entry StepExecution and setting current_step_id.
func (s *Scheduler) recoverPendingRuns(ctx context.Context, result *RecoveryResult) error {
	log := observe.Logger(ctx)

	runs, err := s.store.ListRunsByStatus(ctx, domain.RunStatusPending)
	if err != nil {
		return err
	}

	for i := range runs {
		tr, err := workflow.EvaluateRunTransition(runs[i].Status, workflow.TransitionRequest{
			Trigger: workflow.TriggerActivate,
		})
		if err != nil {
			log.Error("cannot activate pending run", "run_id", runs[i].RunID, "error", err)
			continue
		}

		// Look up the workflow's entry step to create the initial StepExecution.
		entryStepID, err := s.lookupEntryStep(ctx, &runs[i])
		if err != nil {
			log.Error("lookup entry step failed", "run_id", runs[i].RunID, "error", err)
			continue
		}

		now := time.Now()
		if err := s.store.WithTx(ctx, func(tx store.Tx) error {
			if err := tx.UpdateRunStatus(ctx, runs[i].RunID, tr.ToStatus); err != nil {
				return err
			}
			return tx.CreateStepExecution(ctx, &domain.StepExecution{
				ExecutionID: fmt.Sprintf("%s-%s-1", runs[i].RunID, entryStepID),
				RunID:       runs[i].RunID,
				StepID:      entryStepID,
				Status:      domain.StepStatusWaiting,
				Attempt:     1,
				CreatedAt:   now,
			})
		}); err != nil {
			log.Error("activate pending run failed", "run_id", runs[i].RunID, "error", err)
			continue
		}

		result.PendingActivated++
		log.Info("recovered pending run", "run_id", runs[i].RunID, "entry_step", entryStepID)
	}
	return nil
}

// recoverActiveRuns inspects each active run's current step and applies recovery.
func (s *Scheduler) recoverActiveRuns(ctx context.Context, result *RecoveryResult) error {
	log := observe.Logger(ctx)

	runs, err := s.store.ListRunsByStatus(ctx, domain.RunStatusActive)
	if err != nil {
		return err
	}

	for i := range runs {
		if runs[i].CurrentStepID == "" {
			log.Warn("active run has no current step", "run_id", runs[i].RunID)
			continue
		}

		execs, err := s.store.ListStepExecutionsByRun(ctx, runs[i].RunID)
		if err != nil {
			log.Error("list step executions failed", "run_id", runs[i].RunID, "error", err)
			continue
		}

		currentExec := findCurrentExecution(execs, runs[i].CurrentStepID)
		if currentExec == nil {
			log.Warn("no execution found for current step", "run_id", runs[i].RunID, "step_id", runs[i].CurrentStepID)
			continue
		}

		if err := s.recoverStep(ctx, &runs[i], currentExec); err != nil {
			log.Error("step recovery failed", "run_id", runs[i].RunID, "step_id", currentExec.StepID, "error", err)
			continue
		}

		result.ActiveResumed++
		result.StepsRecovered++
	}
	return nil
}

// recoverStep applies recovery logic based on the step's persisted state.
// Per Engine State Machine §3.5.
func (s *Scheduler) recoverStep(ctx context.Context, run *domain.Run, exec *domain.StepExecution) error {
	log := observe.Logger(ctx)

	switch exec.Status {
	case domain.StepStatusWaiting, domain.StepStatusAssigned:
		// Re-attempt assignment: reset to waiting
		// Actor availability check deferred to Actor Gateway (EPIC-003)
		if exec.Status == domain.StepStatusAssigned {
			result, err := workflow.EvaluateStepTransition(exec.Status, workflow.StepTransitionRequest{
				Trigger: workflow.StepTriggerActorUnavail,
			})
			if err != nil {
				return fmt.Errorf("reset assigned to waiting: %w", err)
			}
			exec.Status = result.ToStatus
			if err := s.store.UpdateStepExecution(ctx, exec); err != nil {
				return fmt.Errorf("update step execution: %w", err)
			}
		}
		log.Info("step ready for reassignment", "run_id", run.RunID, "step_id", exec.StepID, "status", exec.Status)

	case domain.StepStatusInProgress:
		// No actor gateway to check for pending results.
		// Leave as-is; the timeout scanner will catch expired steps.
		log.Info("in-progress step left for timeout scanner", "run_id", run.RunID, "step_id", exec.StepID)

	case domain.StepStatusBlocked:
		// Remain blocked; future convergence logic will resolve.
		log.Info("blocked step unchanged", "run_id", run.RunID, "step_id", exec.StepID)

	case domain.StepStatusCompleted:
		// Step completed but run didn't advance — requires engine orchestrator to
		// look up the outcome's next_step and create the next StepExecution.
		// Recovery identifies the situation; the engine run loop will process it.
		log.Warn("completed step needs run advancement (requires engine orchestrator)",
			"run_id", run.RunID, "step_id", exec.StepID, "outcome_id", exec.OutcomeID)

	case domain.StepStatusFailed:
		// Check retry eligibility and log the assessment.
		// Creating a new StepExecution for retry or transitioning the run to failed
		// requires the engine orchestrator — recovery identifies the situation.
		stepDef, err := s.lookupStepDefinition(ctx, exec)
		if err != nil {
			return fmt.Errorf("lookup step definition: %w", err)
		}
		retryLimit := 0
		if stepDef != nil && stepDef.Retry != nil {
			retryLimit = stepDef.Retry.Limit
		}
		classification := domain.FailureTransient
		if exec.ErrorDetail != nil {
			classification = exec.ErrorDetail.Classification
		}
		if workflow.ShouldRetry(exec.Attempt, retryLimit, classification) {
			log.Warn("failed step eligible for retry (requires engine orchestrator)",
				"run_id", run.RunID, "step_id", exec.StepID, "attempt", exec.Attempt)
		} else {
			log.Warn("failed step not retryable, run should be failed (requires engine orchestrator)",
				"run_id", run.RunID, "step_id", exec.StepID)
		}

	case domain.StepStatusSkipped:
		// Terminal — no action
	}

	return nil
}

// recoverCommittingRuns handles runs stuck in committing state.
func (s *Scheduler) recoverCommittingRuns(ctx context.Context, result *RecoveryResult) error {
	log := observe.Logger(ctx)

	runs, err := s.store.ListRunsByStatus(ctx, domain.RunStatusCommitting)
	if err != nil {
		return err
	}

	for i := range runs {
		// Git commit retry service not yet implemented.
		// Leave in committing state; will be handled when Git integration is complete.
		log.Warn("committing run requires Git commit retry (not yet implemented)", "run_id", runs[i].RunID)
		result.CommittingFound++
	}
	return nil
}

// findCurrentExecution returns the most recent execution for the given step ID.
func findCurrentExecution(execs []domain.StepExecution, stepID string) *domain.StepExecution {
	var latest *domain.StepExecution
	for i := range execs {
		if execs[i].StepID == stepID {
			if latest == nil || execs[i].Attempt > latest.Attempt {
				latest = &execs[i]
			}
		}
	}
	return latest
}

// lookupEntryStep returns the workflow's entry_step ID for a given run.
func (s *Scheduler) lookupEntryStep(ctx context.Context, run *domain.Run) (string, error) {
	proj, err := s.store.GetWorkflowProjection(ctx, run.WorkflowPath)
	if err != nil {
		return "", fmt.Errorf("get workflow projection: %w", err)
	}

	var wfDef domain.WorkflowDefinition
	if err := json.Unmarshal(proj.Definition, &wfDef); err != nil {
		return "", fmt.Errorf("unmarshal workflow definition: %w", err)
	}

	if wfDef.EntryStep == "" {
		return "", fmt.Errorf("workflow %s has no entry_step", run.WorkflowPath)
	}
	return wfDef.EntryStep, nil
}
