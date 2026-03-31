package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/workflow"
)

// MergeRunBranch merges the run's branch to the authoritative branch (main)
// and transitions the run from committing to completed. If the merge fails,
// the run transitions to failed (permanent) or stays in committing (transient).
func (o *Orchestrator) MergeRunBranch(ctx context.Context, runID string) error {
	log := observe.Logger(ctx)

	run, err := o.store.GetRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("get run: %w", err)
	}

	if run.Status != domain.RunStatusCommitting {
		return domain.NewError(domain.ErrConflict,
			fmt.Sprintf("run %s is in %s state, expected committing", runID, run.Status))
	}

	if run.BranchName == "" {
		// No branch to merge — transition directly to completed.
		return o.completeAfterMerge(ctx, run, false)
	}

	// Perform the merge into the authoritative branch explicitly.
	trailers := map[string]string{
		"Run-ID":   runID,
		"Trace-ID": run.TraceID,
	}

	mergeResult, err := o.git.Merge(ctx, git.MergeOpts{
		Source:   run.BranchName,
		Target:   "main",
		Strategy: "merge-commit",
		Message:  fmt.Sprintf("Merge run %s: %s", runID, run.TaskPath),
		Trailers: trailers,
	})

	if err != nil {
		// Abort any in-progress merge to leave the repo clean.
		o.abortMerge(ctx)

		// Classify: transient errors stay in committing for retry,
		// permanent errors fail the run.
		var gitErr *git.GitError
		if errors.As(err, &gitErr) && gitErr.IsRetryable() {
			log.Warn("transient merge failure, will retry",
				"run_id", runID, "branch", run.BranchName, "error", err)
			return o.retryMerge(ctx, run)
		}

		log.Error("permanent merge failure",
			"run_id", runID, "branch", run.BranchName, "error", err)
		return o.failRunOnMergeError(ctx, run, err)
	}

	log.Info("run branch merged",
		"run_id", runID,
		"branch", run.BranchName,
		"merge_sha", mergeResult.SHA,
		"fast_forward", mergeResult.FastForward,
	)

	// Push the authoritative branch to origin after a successful merge.
	if autoPushEnabled() {
		if err := o.git.Push(ctx, "origin", "main"); err != nil {
			log.Warn("auto-push: failed to push main after merge, staying in committing for retry",
				"run_id", runID, "error", err)
			// Stay in committing so the scheduler retries the merge+push cycle.
			return o.retryMerge(ctx, run)
		}
	}

	// Transition committing → completed (with branch cleanup).
	return o.completeAfterMerge(ctx, run, true)
}

// abortMerge cleans up a failed merge so the repo is not left dirty.
func (o *Orchestrator) abortMerge(ctx context.Context) {
	// git merge --abort to clean up conflicted state.
	// This is best-effort — if it fails, the repo may need manual cleanup.
	if _, err := o.git.Merge(ctx, git.MergeOpts{
		Source:   "--abort",
		Strategy: "abort",
	}); err != nil {
		observe.Logger(ctx).Warn("failed to abort merge", "error", err)
	}
}

// completeAfterMerge transitions a run from committing to completed
// via the git.commit_succeeded trigger. When cleanupBranch is true,
// the run branch is cleaned up (local + remote).
func (o *Orchestrator) completeAfterMerge(ctx context.Context, run *domain.Run, cleanupBranch bool) error {
	result, err := workflow.EvaluateRunTransition(run.Status, workflow.TransitionRequest{
		Trigger: workflow.TriggerGitCommitSucceeded,
	})
	if err != nil {
		return err
	}

	if err := o.store.UpdateRunStatus(ctx, run.RunID, result.ToStatus); err != nil {
		return fmt.Errorf("update run status: %w", err)
	}

	log := observe.Logger(ctx)
	if err := o.events.Emit(ctx, domain.Event{
		EventID:   fmt.Sprintf("evt-%s-completed", run.TraceID[:12]),
		Type:      domain.EventRunCompleted,
		Timestamp: time.Now(),
		RunID:     run.RunID,
		TraceID:   run.TraceID,
	}); err != nil {
		log.Warn("failed to emit event", "event_type", domain.EventRunCompleted, "error", err)
	}

	log.Info("run completed after merge", "run_id", run.RunID)
	if run.StartedAt != nil {
		observe.GlobalMetrics.RunDuration.ObserveDuration(time.Since(*run.StartedAt))
	}

	// Clean up the branch only if the main push succeeded (or auto-push is off).
	// When main push fails, the remote run branch is the only ref containing
	// the merged commits — preserve it for collaborators.
	if cleanupBranch {
		_ = o.CleanupRunBranch(ctx, run.RunID)
	}
	return nil
}

// retryMerge keeps the run in committing state for transient failures
// via the git.commit_failed_transient trigger.
func (o *Orchestrator) retryMerge(ctx context.Context, run *domain.Run) error {
	_, err := workflow.EvaluateRunTransition(run.Status, workflow.TransitionRequest{
		Trigger: workflow.TriggerGitCommitFailedTrans,
	})
	if err != nil {
		return err
	}
	// Status stays committing — scheduler will retry.
	return nil
}

// failRunOnMergeError transitions a run to failed due to a permanent merge error.
// Persists git_conflict classification on the last completed step for visibility.
func (o *Orchestrator) failRunOnMergeError(ctx context.Context, run *domain.Run, mergeErr error) error {
	log := observe.Logger(ctx)

	// Persist git_conflict detail on the last completed step so actors
	// can see why the run failed via the step execution record.
	o.recordGitConflictOnStep(ctx, run, mergeErr)

	result, err := workflow.EvaluateRunTransition(run.Status, workflow.TransitionRequest{
		Trigger: workflow.TriggerGitCommitFailedPerm,
	})
	if err != nil {
		return err
	}

	if err := o.store.UpdateRunStatus(ctx, run.RunID, result.ToStatus); err != nil {
		return fmt.Errorf("update run status: %w", err)
	}

	if err := o.events.Emit(ctx, domain.Event{
		EventID:   fmt.Sprintf("evt-%s-failed", run.TraceID[:12]),
		Type:      domain.EventRunFailed,
		Timestamp: time.Now(),
		RunID:     run.RunID,
		TraceID:   run.TraceID,
	}); err != nil {
		log.Warn("failed to emit event", "event_type", domain.EventRunFailed, "error", err)
	}

	log.Info("run failed on merge", "run_id", run.RunID, "error", mergeErr)
	// Branch preserved for debugging — not cleaned up on failure.
	return nil
}

// recordGitConflictOnStep finds the last completed step execution for a run
// and attaches git_conflict error detail so actors can see the merge failure.
func (o *Orchestrator) recordGitConflictOnStep(ctx context.Context, run *domain.Run, mergeErr error) {
	log := observe.Logger(ctx)

	execs, err := o.store.ListStepExecutionsByRun(ctx, run.RunID)
	if err != nil {
		log.Warn("failed to list step executions for git conflict recording", "run_id", run.RunID, "error", err)
		return
	}
	// Find the last completed step (the one whose output triggered the merge).
	var lastCompleted *domain.StepExecution
	for i := range execs {
		if execs[i].Status == domain.StepStatusCompleted {
			lastCompleted = &execs[i]
		}
	}
	if lastCompleted == nil {
		return
	}

	// Classify based on error type: merge conflicts get git_conflict,
	// other permanent merge errors get permanent_error.
	classification := domain.FailurePermanent
	var gitErr *git.GitError
	if errors.As(mergeErr, &gitErr) && strings.Contains(gitErr.Message, "conflict") {
		classification = domain.FailureGitConflict
	}

	lastCompleted.ErrorDetail = &domain.ErrorDetail{
		Classification: classification,
		Message:        fmt.Sprintf("merge failed: %s", mergeErr.Error()),
		StepID:         lastCompleted.StepID,
	}
	if updateErr := o.store.UpdateStepExecution(ctx, lastCompleted); updateErr != nil {
		observe.Logger(ctx).Warn("failed to record git conflict on step", "error", updateErr)
	}
}
