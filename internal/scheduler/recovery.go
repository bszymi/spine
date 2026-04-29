package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/event"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/repository"
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
	event.EmitLogged(ctx, s.events, domain.Event{
		EventID: fmt.Sprintf("recovery-%d", time.Now().UnixNano()),
		Type:    domain.EventEngineRecovered,
		Payload: payload,
	})

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

		// Idempotency guard: check if entry step execution already exists.
		execs, err := s.store.ListStepExecutionsByRun(ctx, runs[i].RunID)
		if err != nil {
			log.Error("list step executions failed", "run_id", runs[i].RunID, "error", err)
			continue
		}
		hasEntryStep := false
		for j := range execs {
			if execs[j].StepID == entryStepID {
				hasEntryStep = true
				break
			}
		}

		now := time.Now()
		if err := s.store.WithTx(ctx, func(tx store.Tx) error {
			if err := tx.UpdateRunStatus(ctx, runs[i].RunID, tr.ToStatus); err != nil {
				return err
			}
			if hasEntryStep {
				return nil // entry step already exists, skip creation
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
		// Step completed but run didn't advance — use engine to route to next step.
		if s.stepRecoveryFn != nil {
			log.Info("recovering completed step via engine",
				"run_id", run.RunID, "step_id", exec.StepID, "outcome_id", exec.OutcomeID)
			if err := s.stepRecoveryFn(ctx, exec.ExecutionID); err != nil {
				log.Error("completed step recovery failed", "execution_id", exec.ExecutionID, "error", err)
			}
		} else {
			log.Warn("completed step needs run advancement (no recovery function configured)",
				"run_id", run.RunID, "step_id", exec.StepID, "outcome_id", exec.OutcomeID)
		}

	case domain.StepStatusFailed:
		// Use engine to evaluate retry or fail the run.
		if s.stepRecoveryFn != nil {
			log.Info("recovering failed step via engine",
				"run_id", run.RunID, "step_id", exec.StepID, "attempt", exec.Attempt)
			if err := s.stepRecoveryFn(ctx, exec.ExecutionID); err != nil {
				log.Error("failed step recovery failed", "execution_id", exec.ExecutionID, "error", err)
			}
		} else {
			// Fallback: log assessment without acting.
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
				log.Warn("failed step eligible for retry (no recovery function configured)",
					"run_id", run.RunID, "step_id", exec.StepID, "attempt", exec.Attempt)
			} else {
				log.Warn("failed step not retryable (no recovery function configured)",
					"run_id", run.RunID, "step_id", exec.StepID)
			}
		}

	case domain.StepStatusSkipped:
		// Terminal — no action
	}

	return nil
}

// recoverCommittingRuns retries merge for runs stuck in committing state.
// Uses the CommitRetryFunc when configured; otherwise logs and skips.
func (s *Scheduler) recoverCommittingRuns(ctx context.Context, result *RecoveryResult) error {
	log := observe.Logger(ctx)

	runs, err := s.store.ListRunsByStatus(ctx, domain.RunStatusCommitting)
	if err != nil {
		return err
	}

	for i := range runs {
		result.CommittingFound++

		if s.commitRetryFn == nil {
			log.Warn("committing run found but no retry function configured", "run_id", runs[i].RunID)
			continue
		}

		// Check threshold: only retry if the run has been committing long enough.
		if s.commitThreshold > 0 && time.Since(runs[i].CreatedAt) < s.commitThreshold {
			continue
		}

		log.Info("retrying commit for stuck run", "run_id", runs[i].RunID)
		if err := s.commitRetryFn(ctx, runs[i].RunID); err != nil {
			log.Error("commit retry failed", "run_id", runs[i].RunID, "error", err)
		}
	}
	return nil
}

// retryCommittingRuns is called periodically to retry stuck committing
// runs. EPIC-005 TASK-003: also resume runs in partially-merged state
// — those are blocked on a permanent code-repo failure and need to be
// re-attempted once the operator (or an external resolution loop) has
// resolved the underlying conflict. The CAS-based transition below
// keeps committing-only retries safe: if a parallel actor has already
// moved the run, the second call no-ops.
func (s *Scheduler) retryCommittingRuns(ctx context.Context) {
	s.retryRunsByStatus(ctx, domain.RunStatusCommitting, "")
	s.retryRunsByStatus(ctx, domain.RunStatusPartiallyMerged, workflow.TriggerRetryPartialMerge)
}

// RunRetryCycle drives one pass of the periodic merge-retry loop —
// same code path as the ticker-driven sweep, but callable
// synchronously. Tests use it to drive deterministic assertions
// without relying on ticker timing; an admin endpoint could
// foreseeably do the same to force-resume a partial-merge after a
// manual fix without waiting for the next tick.
func (s *Scheduler) RunRetryCycle(ctx context.Context) {
	if s.commitRetryFn == nil {
		return
	}
	s.retryCommittingRuns(ctx)
}

// retryRunsByStatus drives the commit retry callback over every run
// in the given status. When resumeTrigger is non-empty the run is
// transitioned through the trigger before commitRetryFn fires, so
// MergeRunBranch (which only accepts committing) sees the expected
// state. Transition errors are logged and the run is skipped — the
// next tick will re-attempt.
//
// EPIC-005 TASK-003 retry-flapping guard: when the trigger is the
// partially-merged resume, we skip runs whose merge outcomes still
// carry a permanently-failed code repo. Without this gate every
// scheduler tick would flip the run committing → partially-merged
// in a tight loop — MergeRunBranch's per-repo skip guard would treat
// the failed outcome as terminal and re-park the run on every pass,
// emitting duplicate run_partially_merged events and inflating the
// primary outcome's attempts counter without any progress. An
// operator resolution (which TASK-006 will provide) flips a failed
// outcome to merged / pending / resolved-externally; the next tick
// then sees no failed code-repo and the resume proceeds.
func (s *Scheduler) retryRunsByStatus(ctx context.Context, status domain.RunStatus, resumeTrigger workflow.Trigger) {
	log := observe.Logger(ctx)

	runs, err := s.store.ListRunsByStatus(ctx, status)
	if err != nil {
		log.Error("list runs by status failed", "status", string(status), "error", err)
		return
	}

	for i := range runs {
		runID := runs[i].RunID
		if resumeTrigger != "" {
			if status == domain.RunStatusPartiallyMerged {
				eligible, err := codeRepoOutcomesAllowResume(ctx, s.store, runID)
				if err != nil {
					log.Error("partial-merge resume gate lookup failed",
						"run_id", runID, "error", err)
					continue
				}
				if !eligible {
					// Logged at debug-level intent: an unresolved
					// partial-merge is the expected steady state until
					// an operator acts.
					log.Info("partial-merge resume skipped: failed code repo unchanged",
						"run_id", runID)
					continue
				}
			}

			result, err := workflow.EvaluateRunTransition(status, workflow.TransitionRequest{
				Trigger: resumeTrigger,
			})
			if err != nil {
				log.Error("resume transition lookup failed",
					"run_id", runID, "from_status", string(status),
					"trigger", string(resumeTrigger), "error", err)
				continue
			}
			applied, err := s.store.TransitionRunStatus(ctx, runID, status, result.ToStatus)
			if err != nil {
				log.Error("resume transition failed",
					"run_id", runID, "from_status", string(status),
					"to_status", string(result.ToStatus), "error", err)
				continue
			}
			if !applied {
				// Concurrent actor moved the run; skip this tick.
				continue
			}
			log.Info("resumed run for retry",
				"run_id", runID,
				"from_status", string(status),
				"to_status", string(result.ToStatus))
		}

		log.Info("retrying commit", "run_id", runID, "status", string(status))
		if err := s.commitRetryFn(ctx, runID); err != nil {
			log.Error("commit retry failed", "run_id", runID, "error", err)
		}
	}
}

// codeRepoOutcomesAllowResume reports whether a partially-merged run
// is eligible for an automatic resume. The rule is: every non-primary
// outcome must be in a state that the per-repo merge loop can act on
// — anything other than `failed`. A `failed` outcome (always a
// permanent class on a partially-merged run, since transient
// failures keep the run in committing) means the underlying conflict
// is unresolved and MergeRunBranch's terminal-skip guard would just
// re-park the run.
//
// Store errors are surfaced so the caller can stay fail-closed and
// re-try on the next tick rather than guess.
func codeRepoOutcomesAllowResume(ctx context.Context, st store.Store, runID string) (bool, error) {
	outcomes, err := st.ListRepositoryMergeOutcomes(ctx, runID)
	if err != nil {
		return false, err
	}
	for i := range outcomes {
		if outcomes[i].RepositoryID == repository.PrimaryRepositoryID {
			continue
		}
		if outcomes[i].Status == domain.RepositoryMergeStatusFailed {
			return false, nil
		}
	}
	return true, nil
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
