package engine

import (
	"context"
	"fmt"

	"github.com/bszymi/spine/internal/observe"
)

// CleanupRunBranch deletes the Git branch associated with a completed run.
func (o *Orchestrator) CleanupRunBranch(ctx context.Context, runID string) error {
	run, err := o.store.GetRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("get run: %w", err)
	}

	if run.BranchName == "" {
		return nil // no branch to clean up
	}

	if err := o.git.DeleteBranch(ctx, run.BranchName); err != nil {
		observe.Logger(ctx).Warn("failed to delete run branch",
			"run_id", runID,
			"branch", run.BranchName,
			"error", err,
		)
		return fmt.Errorf("delete branch %s: %w", run.BranchName, err)
	}

	observe.Logger(ctx).Info("run branch cleaned up", "run_id", runID, "branch", run.BranchName)
	return nil
}

// RunBranch returns the branch name for a run, or empty if not set.
func (o *Orchestrator) RunBranch(ctx context.Context, runID string) string {
	run, err := o.store.GetRun(ctx, runID)
	if err != nil {
		return ""
	}
	return run.BranchName
}
