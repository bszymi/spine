package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/repository"
)

// CleanupRunBranch deletes the local Git branch associated with a
// completed run on every affected repository. Remote branch cleanup
// runs when auto-push is enabled.
//
// Per-repository semantics (EPIC-005 TASK-004):
//   - A repository whose merge outcome is `failed` keeps its run
//     branch — local AND remote — so the operator can resolve the
//     failure against the unmodified source ref. Applies to the
//     primary repo too: if a permanent primary push failure flipped
//     the run to failed, deleting the branch would lose the only ref
//     carrying the merged commits.
//   - A repository with any other terminal outcome (merged, skipped,
//     resolved-externally) gets its branch deleted.
//   - Cleanup errors are recorded as EventRunBranchCleanupFailed and
//     logged best-effort. They never flip a merge outcome to failed
//     and they never block cleanup of other repos.
//   - A primary cleanup error is also returned to the caller so paths
//     like MergeRunBranch can surface it; per-repo (code repo) errors
//     stay best-effort because no caller acts on them.
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

	// preserveAll only governs the code-repo branches: when we cannot
	// read merge outcomes, we keep every code-repo ref but still let
	// the primary follow its own (run-state-derived) cleanup path —
	// otherwise transient store errors would leave the primary branch
	// behind on every successful run.
	preserved, preserveAll := o.preservedRepoBranches(ctx, runID)

	// seq disambiguates EventRunBranchCleanupFailed event IDs when
	// multiple deletes fail in the same nanosecond — the event_log
	// dedupes by event ID, so collapsing two distinct failures would
	// hide one from operators.
	var seq int

	var primaryErr error
	if preserved[repository.PrimaryRepositoryID] {
		log.Info("cleanup: preserving primary run branch",
			"run_id", runID, "repository_id", repository.PrimaryRepositoryID,
			"branch", run.BranchName, "reason", "failed merge outcome")
	} else {
		if err := o.git.DeleteBranch(ctx, run.BranchName); err != nil {
			log.Warn("failed to delete run branch",
				"run_id", runID,
				"branch", run.BranchName,
				"error", err,
			)
			primaryErr = fmt.Errorf("delete branch %s: %w", run.BranchName, err)
			o.recordCleanupFailure(ctx, run, repository.PrimaryRepositoryID, run.BranchName, "local", err, &seq)
		}
		if pushOn {
			if err := o.git.DeleteRemoteBranch(ctx, "origin", run.BranchName); err != nil {
				log.Warn("auto-push: failed to delete remote branch",
					"run_id", runID,
					"branch", run.BranchName,
					"error", err,
				)
				o.recordCleanupFailure(ctx, run, repository.PrimaryRepositoryID, run.BranchName, "remote", err, &seq)
			}
		}
	}

	// Multi-repo cleanup (INIT-014 EPIC-004 TASK-002, refined by
	// EPIC-005 TASK-004). Symmetric with createRunBranches: every
	// non-primary affected repo received the same run branch (and an
	// auto-push remote ref when enabled), so every one of them needs
	// the same delete. Per-repo failures stay best-effort and are
	// recorded as EventRunBranchCleanupFailed; they must not mask the
	// primary cleanup outcome the caller observed.
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
				o.recordCleanupFailure(ctx, run, repoID, run.BranchName, "local", err, &seq)
				continue
			}
			if err := client.DeleteBranch(ctx, run.BranchName); err != nil {
				log.Warn("cleanup: failed to delete run branch",
					"run_id", runID, "repository_id", repoID,
					"branch", run.BranchName, "error", err)
				o.recordCleanupFailure(ctx, run, repoID, run.BranchName, "local", err, &seq)
			}
			if pushOn {
				if err := client.DeleteRemoteBranch(ctx, "origin", run.BranchName); err != nil {
					log.Warn("auto-push: failed to delete remote run branch",
						"run_id", runID, "repository_id", repoID,
						"branch", run.BranchName, "error", err)
					o.recordCleanupFailure(ctx, run, repoID, run.BranchName, "remote", err, &seq)
				}
			}
		}
	}

	if primaryErr != nil {
		return primaryErr
	}
	log.Info("run branch cleanup complete", "run_id", runID, "branch", run.BranchName)
	return nil
}

// preservedRepoBranches returns (preserved-set, preserveAll).
//
// preserved-set: the repository IDs whose merge outcome is `failed`
// and whose run branch must therefore be kept after run completion.
// The set may include the primary repository: a permanent primary
// push failure flips the run to `failed` with primary outcome=failed,
// and the run branch is the only ref carrying the merged commits.
//
// preserveAll: true when ListRepositoryMergeOutcomes errored. In that
// case the caller must preserve every code-repo branch — a transient
// store error at cleanup time would otherwise let CleanupRunBranch
// delete the only ref carrying an unmerged failure's work, which is
// strictly worse than leaving recoverable cruft. The primary follows
// the run-state-derived path (delete unless its own outcome row says
// otherwise) so a store hiccup does not strand primary branches on
// every successful run.
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

// recordCleanupFailure emits EventRunBranchCleanupFailed for a single
// per-repo cleanup error so operators see the failure in the event log
// without the merge outcome being reclassified as failed. seq is bumped
// before emission so each event in the same cleanup pass gets a unique
// ID even when the wall clock has not advanced — the event_log dedupes
// on event ID and would otherwise collapse simultaneous failures.
func (o *Orchestrator) recordCleanupFailure(ctx context.Context, run *domain.Run, repoID, branch, scope string, cleanupErr error, seq *int) {
	*seq++
	payload, _ := json.Marshal(map[string]string{
		"repository_id": repoID,
		"branch":        branch,
		"scope":         scope,
		"error":         cleanupErr.Error(),
	})
	tracePrefix := run.TraceID
	if len(tracePrefix) > 12 {
		tracePrefix = tracePrefix[:12]
	}
	eventID := fmt.Sprintf("evt-%s-cleanup-failed-%d-%d", tracePrefix, time.Now().UnixNano(), *seq)
	o.emitEvent(ctx, domain.EventRunBranchCleanupFailed, run.RunID, run.TraceID, eventID, payload)
}

// RunBranch returns the branch name for a run, or empty if not set.
func (o *Orchestrator) RunBranch(ctx context.Context, runID string) string {
	run, err := o.store.GetRun(ctx, runID)
	if err != nil {
		return ""
	}
	return run.BranchName
}
