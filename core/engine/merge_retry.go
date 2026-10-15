package engine

// Bounded governed-merge retries (INIT-023 TASK-012; audit P3-27).
//
// A transient merge or push failure keeps a run in committing, and the
// scheduler retried committing runs on every tick, forever:
// scheduler.WithCommitRetry stored a retry budget nobody read. A run whose
// push kept failing transiently (network, a stale index.lock reported as
// "repository locked") was retried every minute indefinitely, and
// engine-state-machine.md's "after retries" never happened.
//
// The budget is enforced where the knob lives (the scheduler gates its
// commit-retry pass on scheduler.WithCommitExhausted) and decided here,
// from the existing ledger: repository_merge_outcomes.attempts, no new
// column. AbandonExhaustedMerge ends a committing run whose recorded
// outcome shows the budget spent, routing it exactly like a permanent
// merge failure.
//
// What the bound counts (design-check note): only attempts that reach
// recordPrimaryMergeOutcome, or a code repository's outcome, with a
// transient class. A governance hold returns before the merge and records
// nothing, so it is never counted; a lock wait cut short by the caller's
// context records nothing either (§2.7); outcome-persistence failures are
// a store outage and are not counted.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/observe"
)

// MergeRetriesExhaustedError is the cause recorded when a committing run's
// automatic merge retries are spent (INIT-023 TASK-012, P3-27).
type MergeRetriesExhaustedError struct {
	RepositoryID string
	Attempts     int
	MaxRetries   int
	LastClass    domain.MergeFailureClass
	LastDetail   string
}

func (e *MergeRetriesExhaustedError) Error() string {
	return fmt.Sprintf("merge retries exhausted for repository %s after %d attempts (max_retries=%d); last failure (%s): %s",
		e.RepositoryID, e.Attempts, e.MaxRetries, e.LastClass, e.LastDetail)
}

// retriesExhaustedMarker starts the failure detail of an outcome whose
// retries were abandoned.
const retriesExhaustedMarker = "retries exhausted ("

// AbandonExhaustedMerge ends a committing run whose recorded merge
// outcomes show the retry budget spent. It routes exactly like a permanent
// merge failure: a workflow with a publish step takes merge_failed (back
// to execute, per task-default), a legacy terminal-commit workflow fails
// the run (branch kept). It returns false, and changes nothing, when the
// run is not committing, maxRetries is not positive, or no outcome is
// exhausted.
//
// The exhausted outcome is re-recorded with its attempts, timestamps and
// transient class unchanged (the outcomes CHECK admits no new class, and
// the last failure was transient) and its failure detail prefixed once
// with "retries exhausted (attempts=N, max_retries=M): ". The step the run
// routes from carries domain.FailureMergeRetriesExhausted.
//
// No tree lock: this does no git work, and it routes steps (worktree.go).
// The scheduler calls it (scheduler.CommitExhaustedFunc) before each
// commit retry.
func (o *Orchestrator) AbandonExhaustedMerge(ctx context.Context, runID string, maxRetries int) (bool, error) {
	if maxRetries <= 0 {
		return false, nil
	}
	run, err := o.store.GetRun(ctx, runID)
	if err != nil {
		return false, fmt.Errorf("get run: %w", err)
	}
	if run.Status != domain.RunStatusCommitting {
		return false, nil
	}
	outcomes, err := o.store.ListRepositoryMergeOutcomes(ctx, runID)
	if err != nil {
		return false, fmt.Errorf("list merge outcomes: %w", err)
	}
	ex := domain.FirstExhaustedOutcome(outcomes, maxRetries)
	if ex == nil {
		return false, nil
	}
	cause := &MergeRetriesExhaustedError{
		RepositoryID: ex.RepositoryID,
		Attempts:     ex.Attempts,
		MaxRetries:   maxRetries,
		LastClass:    ex.FailureClass,
		LastDetail:   strings.TrimPrefix(ex.FailureDetail, exhaustedPrefix(ex.Attempts, maxRetries)),
	}
	if !strings.HasPrefix(ex.FailureDetail, retriesExhaustedMarker) {
		marked := *ex
		marked.FailureDetail = exhaustedPrefix(ex.Attempts, maxRetries) + ex.FailureDetail
		if err := o.store.UpsertRepositoryMergeOutcome(ctx, &marked); err != nil {
			return false, fmt.Errorf("record exhausted merge outcome: %w", err)
		}
	}
	observe.AuditLog(ctx, "merge_retries_exhausted",
		"run_id", runID,
		"repository_id", ex.RepositoryID,
		"attempts", ex.Attempts,
		"max_retries", maxRetries,
		"failure_class", string(ex.FailureClass),
	)
	observe.Logger(ctx).Error("governed merge retries exhausted; taking the permanent-failure route",
		"run_id", runID, "repository_id", ex.RepositoryID, "attempts", ex.Attempts,
		"max_retries", maxRetries, "failure_class", string(ex.FailureClass))
	return true, o.handlePermanentMergeFailure(ctx, run, cause)
}

func exhaustedPrefix(attempts, maxRetries int) string {
	return fmt.Sprintf("%sattempts=%d, max_retries=%d): ", retriesExhaustedMarker, attempts, maxRetries)
}

// isRetriesExhausted reports a *MergeRetriesExhaustedError.
func isRetriesExhausted(err error) bool {
	var e *MergeRetriesExhaustedError
	return errors.As(err, &e)
}
