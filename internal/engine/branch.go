package engine

import (
	"context"
	"fmt"

	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/repository"
)

// CleanupRunBranch deletes the local Git branch associated with a
// completed run on every affected repository. Remote branch cleanup
// runs when auto-push is enabled. Failure on the primary repo is
// surfaced (it is the run's authoritative ref); per-repo cleanup
// failures on code repos are logged best-effort so a transient git
// error in one repo cannot mask the rest of the cleanup.
func (o *Orchestrator) CleanupRunBranch(ctx context.Context, runID string) error {
	run, err := o.store.GetRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("get run: %w", err)
	}

	if run.BranchName == "" {
		return nil // no branch to clean up
	}

	log := observe.Logger(ctx)
	pushOn := autoPushEnabled()

	// Hold onto the primary cleanup error but keep going — the code
	// repos are independent working trees, so a primary-side failure
	// (e.g. branch is checked out) must not leak refs in every code
	// repo too. The primary error is returned at the end.
	var primaryErr error
	if err := o.git.DeleteBranch(ctx, run.BranchName); err != nil {
		log.Warn("failed to delete run branch",
			"run_id", runID,
			"branch", run.BranchName,
			"error", err,
		)
		primaryErr = fmt.Errorf("delete branch %s: %w", run.BranchName, err)
	}

	// Delete the remote branch as well (best-effort — it may already be gone).
	if pushOn {
		if err := o.git.DeleteRemoteBranch(ctx, "origin", run.BranchName); err != nil {
			log.Warn("auto-push: failed to delete remote branch",
				"run_id", runID,
				"branch", run.BranchName,
				"error", err,
			)
		}
	}

	// Multi-repo cleanup (INIT-014 EPIC-004 TASK-002). Symmetric with
	// createRunBranches: every non-primary affected repo received the
	// same run branch (and an auto-push remote ref when enabled), so
	// every one of them needs the same delete. Best-effort per repo —
	// a code-repo cleanup failure must not mask the primary cleanup
	// success the caller already observed.
	if o.repoClients != nil {
		for _, repoID := range run.AffectedRepositories {
			if repoID == "" || repoID == repository.PrimaryRepositoryID {
				continue
			}
			client, err := o.repoClients.Client(ctx, repoID)
			if err != nil {
				log.Warn("cleanup: failed to resolve code repo client",
					"run_id", runID, "repository_id", repoID,
					"branch", run.BranchName, "error", err)
				continue
			}
			if err := client.DeleteBranch(ctx, run.BranchName); err != nil {
				log.Warn("cleanup: failed to delete run branch",
					"run_id", runID, "repository_id", repoID,
					"branch", run.BranchName, "error", err)
			}
			if pushOn {
				if err := client.DeleteRemoteBranch(ctx, "origin", run.BranchName); err != nil {
					log.Warn("auto-push: failed to delete remote run branch",
						"run_id", runID, "repository_id", repoID,
						"branch", run.BranchName, "error", err)
				}
			}
		}
	}

	if primaryErr != nil {
		return primaryErr
	}
	log.Info("run branch cleaned up", "run_id", runID, "branch", run.BranchName)
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
