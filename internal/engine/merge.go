package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/workflow"
)

// MergeRunBranch merges the run's branch to the authoritative branch (main)
// and transitions the run from committing to completed. If the merge fails,
// the run transitions to failed with error detail.
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
		return o.completeAfterMerge(ctx, run)
	}

	// Perform the merge.
	trailers := map[string]string{
		"Run-ID":   runID,
		"Trace-ID": run.TraceID,
	}

	mergeResult, err := o.git.Merge(ctx, git.MergeOpts{
		Source:   run.BranchName,
		Strategy: "merge-commit",
		Message:  fmt.Sprintf("Merge run %s: %s", runID, run.TaskPath),
		Trailers: trailers,
	})

	if err != nil {
		// Merge failed — classify and fail the run.
		log.Error("merge failed", "run_id", runID, "branch", run.BranchName, "error", err)
		return o.failRunOnMergeError(ctx, run, err)
	}

	log.Info("run branch merged",
		"run_id", runID,
		"branch", run.BranchName,
		"merge_sha", mergeResult.SHA,
		"fast_forward", mergeResult.FastForward,
	)

	// Transition committing → completed.
	return o.completeAfterMerge(ctx, run)
}

// completeAfterMerge transitions a run from committing to completed
// via the git.commit_succeeded trigger.
func (o *Orchestrator) completeAfterMerge(ctx context.Context, run *domain.Run) error {
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

	// Clean up the branch.
	_ = o.CleanupRunBranch(ctx, run.RunID)
	return nil
}

// failRunOnMergeError transitions a run to failed due to a merge error.
func (o *Orchestrator) failRunOnMergeError(ctx context.Context, run *domain.Run, mergeErr error) error {
	result, err := workflow.EvaluateRunTransition(run.Status, workflow.TransitionRequest{
		Trigger: workflow.TriggerGitCommitFailedPerm,
	})
	if err != nil {
		return err
	}

	if err := o.store.UpdateRunStatus(ctx, run.RunID, result.ToStatus); err != nil {
		return fmt.Errorf("update run status: %w", err)
	}

	log := observe.Logger(ctx)
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
