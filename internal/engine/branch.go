package engine

import (
	"context"
	"fmt"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/repository"
)

// CleanupRunBranch deletes the local Git branch associated with a
// completed run on every affected repository. Remote branch cleanup
// runs when auto-push is enabled. Failure on the primary repo is
// surfaced (it is the run's authoritative ref); per-repo cleanup
// failures on code repos are logged best-effort so a transient git
// error in one repo cannot mask the rest of the cleanup.
//
// EPIC-005 TASK-002 refinement: code repos whose merge outcome is
// `failed` keep their run branch so an operator can resolve the
// conflict (or rotate creds, etc.) and re-merge from the same source
// ref. Without this guard the code-repo-first merge order would
// quietly destroy the only ref carrying the unmerged work the moment
// the primary repo completes. Branch cleanup per repo (full TASK-004
// scope) refines this further.
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

	// Per-repo merge outcomes drive whether each code repo's branch can
	// be deleted. A repo with a failed outcome keeps its branch — that
	// is the only ref carrying the unmerged work the operator needs to
	// resolve. Pending/Merged/Skipped/ResolvedExternally repos all get
	// cleaned up. preserveAll is set when ListRepositoryMergeOutcomes
	// errored: rather than risk deleting the only ref a recoverable
	// failed-merge has, we err on the side of keeping every code-repo
	// branch and let an operator clean up by hand. The primary cleanup
	// is unaffected — its branch is on a separate working tree and a
	// store hiccup does not change its merge status.
	preserved, preserveAll := o.preservedRepoBranches(ctx, runID)

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
			if preserveAll || preserved[repoID] {
				reason := "failed merge outcome"
				if preserveAll {
					reason = "merge outcome list unavailable"
				}
				log.Info("cleanup: preserving run branch",
					"run_id", runID, "repository_id", repoID,
					"branch", run.BranchName, "reason", reason)
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

// preservedRepoBranches returns (preserved-set, preserveAll).
//
// preserved-set: the repository IDs whose merge outcome is `failed`
// and whose run branch must therefore be kept after run completion.
//
// preserveAll: true when ListRepositoryMergeOutcomes errored. In that
// case the caller must preserve every code-repo branch — a transient
// store error at cleanup time would otherwise let CleanupRunBranch
// delete the only ref carrying an unmerged failure's work, which is
// strictly worse than leaving recoverable cruft. Operators can
// reconcile by hand once the store recovers.
func (o *Orchestrator) preservedRepoBranches(ctx context.Context, runID string) (map[string]bool, bool) {
	preserved := map[string]bool{}
	outcomes, err := o.store.ListRepositoryMergeOutcomes(ctx, runID)
	if err != nil {
		observe.Logger(ctx).Warn("cleanup: failed to list merge outcomes — preserving all code-repo branches",
			"run_id", runID, "error", err)
		return preserved, true
	}
	for _, outcome := range outcomes {
		if outcome.Status == domain.RepositoryMergeStatusFailed {
			preserved[outcome.RepositoryID] = true
		}
	}
	return preserved, false
}

// RunBranch returns the branch name for a run, or empty if not set.
func (o *Orchestrator) RunBranch(ctx context.Context, runID string) string {
	run, err := o.store.GetRun(ctx, runID)
	if err != nil {
		return ""
	}
	return run.BranchName
}
