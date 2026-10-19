package engine

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/artifact"
	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/observe"
	"github.com/bszymi/spine/core/workflow"
)

// authoritativeBranch is the destination of every governed merge the
// Orchestrator performs today. Centralised here so the branch-protection
// request is built consistently with the git.MergeOpts target below — if
// either changes, they change together.
const authoritativeBranch = "main"

// AuthoritativeBranch exports the governed-merge target so wiring that
// must follow the same branch — the projection's
// WithAuthoritativeBranch (INIT-023 TASK-011, audit P3-22) — cannot
// drift from it. It is a short branch name, not a qualified ref.
const AuthoritativeBranch = authoritativeBranch

// MergeRunBranch merges the run's branch to the authoritative branch (main)
// and transitions the run from committing to completed. If the merge fails,
// the run transitions to failed (permanent) or stays in committing (transient).
//
// The body delegates to four phase helpers — preflightCheck loads the run
// and runs branch-protection / commit-status; mergeCodeRepos drives the
// per-repo loop; mergePrimary performs the primary merge + push; and
// verifyAndComplete handles the post-merge code-repo completeness check
// and the committing → completed transition. Each helper signals "done,
// caller should return" through its first return so MergeRunBranch
// itself stays a flat orchestrator and every retry / permanent-fail /
// success path remains in the same order as before extraction.
//
// With merge binding on (EnableMergeBinding, INIT-023 TASK-005)
// resolveMergeSource runs after the preflight: it reads the run's frozen
// binding (failing closed without one), applies the status cascade AT
// the bound commit, and every later phase merges exactly that commit.
// With binding off it returns the zero value and nothing changes.
//
// With the status lifecycle or the governance-path guard on, an attempt
// first settles a merge an earlier attempt of the run may have left on the
// local main unverified (settleUnverifiedMerge, INIT-023 wave 4 review).
//
// With the governance-path guard on (WithGovernanceGuard, INIT-023
// TASK-006) governanceGate runs next: a merge that changes governance
// paths without an operator approval of the bound commit is held (the
// error wraps ErrGovernanceApprovalRequired), and mergePrimaryBound
// asserts after the merge that it changed nothing else. The whole attempt
// then runs under the run's governance stripe, so an operator decision on
// the run waits for it (governanceRunStripes).
func (o *Orchestrator) MergeRunBranch(ctx context.Context, runID string) error {
	// An exhaustion abandonment of this run in progress (AbandonExhaustedMerge,
	// INIT-023 TASK-012) is routing it: this attempt stands down, nothing
	// is recorded. Never the case for an embedder that does not wire the
	// scheduler's exhaustion handler. Registered before the governance
	// stripe below (INIT-023 wave 4 integration), so an attempt waiting
	// for an operator decision on the run already counts as in flight and
	// no abandonment routes the run while it waits.
	end, ok := mergeAttemptGate.beginAttempt(runID)
	if !ok {
		observe.Logger(ctx).Info("merge retries of this run are being abandoned; this merge attempt stands down", "run_id", runID)
		return nil
	}
	defer end()
	ctx, unlock, err := o.lockGovernanceRunIfGuarded(ctx, runID)
	if err != nil {
		return err
	}
	defer unlock()
	run, done, err := o.preflightCheck(ctx, runID)
	if err != nil || done {
		return err
	}
	// A merge an earlier attempt left on the local main without its
	// post-merge checks (the process died in the block, or a take-back
	// failed) is taken back before anything reads main (INIT-023 wave 4
	// review; merge_inflight.go). Only with V1 or the guard on.
	if err := o.settleUnverifiedMerge(ctx, run); err != nil {
		return err
	}
	src, done, err := o.resolveMergeSource(ctx, run)
	if err != nil || done {
		return err
	}
	// Governance-path guard (ADR-016; INIT-023 TASK-006): after the
	// binding is validated and the merge source built, before any code
	// repository or the primary merges. A held merge returns here with
	// the run still committing.
	if done, err := o.governanceGate(ctx, run, &src); err != nil || done {
		return err
	}
	if retried, err := o.mergeCodeRepos(ctx, run); err != nil || retried {
		return err
	}
	if done, err := o.mergePrimary(ctx, run, &src); err != nil || done {
		return err
	}
	return o.verifyAndComplete(ctx, run, src)
}

// preflightCheck loads the run, validates its state, runs branch protection,
// and applies commit-status metadata on the run branch.
//
// Returns:
//   - (run, false, nil)         — ready for the code-repo loop.
//   - (nil, true, nil) / (nil, true, err) — empty-branch fast path: the
//     run was driven directly to completed via completeAfterMerge; the
//     caller returns the wrapped err (nil on success).
//   - (nil, false, err)         — fatal preflight error (GetRun failed,
//     run not in committing state, branch-protection denied).
//
// applyCommitStatus failures are logged and swallowed — exactly as in the
// pre-extraction code — because the merge itself is the contract: a
// failed status rewrite must not block the merge. That holds only with the
// status lifecycle off: with it on (INIT-023 TASK-017) binding is
// required — the combination with binding off is refused here as a
// configuration error — and the lifecycle judges the bound commit in
// resolveMergeSource, failing closed.
func (o *Orchestrator) preflightCheck(ctx context.Context, runID string) (*domain.Run, bool, error) {
	log := observe.Logger(ctx)

	run, err := o.store.GetRun(ctx, runID)
	if err != nil {
		return nil, false, fmt.Errorf("get run: %w", err)
	}

	if run.Status != domain.RunStatusCommitting {
		return nil, false, domain.NewError(domain.ErrConflict,
			fmt.Sprintf("run %s is in %s state, expected committing", runID, run.Status))
	}

	if run.BranchName == "" {
		// No branch to merge — transition directly to completed.
		return nil, true, o.completeAfterMerge(ctx, run, false)
	}

	// Branch-protection check (ADR-009 §3). Runs before any ref-advancing
	// work — including applyCommitStatus, which writes to the run branch —
	// so a nil policy or a denied governed merge does not leave half-
	// applied commits on the branch. OpGovernedMerge is allowed
	// unconditionally by the real evaluator (rules do not gate it), so
	// under normal operation this is a no-op at the decision level. The
	// check still runs so (a) a nil policy is caught as a configuration
	// error rather than a silent bypass, and (b) a future evaluator that
	// does want to gate governed merges (e.g. to reject merges from a
	// contributor on a branch touching .spine/branch-protection.yaml —
	// see ADR-009 §1 run-branch-slippage) has a single site to plug into.
	// Rule-source errors for governed merges do NOT surface here: the
	// evaluator short-circuits before loading rules, by design.
	if err := o.checkBranchProtectMerge(ctx, run); err != nil {
		return nil, false, err
	}

	// The status lifecycle judges the bound commit (INIT-023 TASK-017
	// DC-2): without binding the tip could move between the judgement and
	// the merge, so the combination is a configuration error, refused
	// before anything is written (like a nil policy). The run stays in
	// committing; no outcome is recorded.
	if o.lifecycle != nil && !o.bindingOn {
		return nil, false, domain.NewError(domain.ErrUnavailable,
			"engine: the status lifecycle requires merge binding (EnableMergeBinding); refusing to merge")
	}

	// Apply commit metadata (e.g., rewrite artifact status) on the branch
	// before merging so the updated file lands on main. Under merge
	// binding the cascade runs at the bound commit instead
	// (resolveMergeSource → applyCommitStatusAt) and never touches the
	// run branch.
	//
	// The cascade is one git-only block on the shared working tree
	// (INIT-023 TASK-012, worktree.go): checkout run → write → commit →
	// checkout main can no longer interleave with another run's merge, so
	// its status commit never lands on main outside the governed merge.
	// The block re-checks the run status first: a second entry for a run
	// another entry already completed stands down here.
	if newStatus := run.CommitMeta["status"]; newStatus != "" && !o.bindingOn {
		err := o.withTree(ctx, func(ctx context.Context) error {
			if err := o.requireStillCommitting(ctx, run.RunID); err != nil {
				return err
			}
			return o.applyCommitStatus(ctx, run, newStatus)
		})
		if done, ret := blockExit(err); done {
			return nil, true, ret
		}
		if err != nil {
			log.Warn("failed to apply commit status, proceeding with merge",
				"run_id", runID, "error", err)
		}
	}

	return run, false, nil
}

// mergeCodeRepos drives the per-repo merge loop for every affected
// non-primary repository (INIT-014 EPIC-005 TASK-002). Code-repo-first
// ordering is preserved: every affected repo merges and records its
// outcome before the primary ledger update advances. This intentionally
// runs after applyCommitStatus — the cascade only touches files in the
// primary repo, so its placement does not gate code-repo merges, but
// keeping the existing primary-prep step before any per-repo work
// preserves the ordering operators already debug against. A non-nil
// error from mergeAffectedCodeRepositories means the orchestrator could
// not even attempt the chain (missing wiring, registry lookup failure);
// per-repo merge failures are recorded as failed outcomes and never
// bubble up.
//
// Returns (true, nil) when the run was placed on the transient retry
// track (caller returns nil), (false, err) on hard error, (false, nil)
// on success.
func (o *Orchestrator) mergeCodeRepos(ctx context.Context, run *domain.Run) (bool, error) {
	if err := o.mergeAffectedCodeRepositories(ctx, run); err != nil {
		// A non-terminal code-repo outcome (transient failure, pending)
		// routes through retryMerge so the run stays in committing and
		// the scheduler picks it up again. Anything else is treated as
		// a hard error and bubbles up.
		if errors.Is(err, errPendingCodeRepoRetry) {
			observe.Logger(ctx).Warn("code repo merge has non-terminal outcomes, retrying",
				"run_id", run.RunID, "branch", run.BranchName)
			return true, o.retryMerge(ctx, run)
		}
		return false, err
	}
	return false, nil
}

// mergePrimary performs the primary merge into the authoritative branch
// and pushes it (when auto-push is enabled). The retry / renumber /
// permanent-fail decisions on merge and push errors live here so the
// outer MergeRunBranch keeps a single orchestration shape.
//
// Returns:
//   - (false, recErr) — outcome-record persistence failure. The caller
//     returns this error so the run stays in committing for retry,
//     matching the symmetric treatment in the code-repo path.
//   - (true, nil) / (true, err) — the run was placed on a retry track
//     (transient failure, ID-collision renumber) or routed through
//     handlePermanentMergeFailure; caller returns the wrapped err.
//   - (false, nil) — merge AND push (when enabled) both succeeded and
//     the primary outcome row is recorded; caller advances to
//     verifyAndComplete.
//
// With merge binding on, mergePrimaryBound merges exactly src.sha instead.
func (o *Orchestrator) mergePrimary(ctx context.Context, run *domain.Run, src *mergeSource) (bool, error) {
	if o.bindingOn {
		return o.mergePrimaryBound(ctx, run, src)
	}
	log := observe.Logger(ctx)

	// Perform the merge into the authoritative branch explicitly.
	trailers := map[string]string{
		"Run-ID":   run.RunID,
		"Trace-ID": run.TraceID,
	}

	// The merge, its abort and the push are one git-only block on the
	// shared working tree (INIT-023 TASK-012, worktree.go). It takes the
	// tree fresh, so its entry restore puts a clean main under the merge;
	// it re-checks the run status first; and it holds the tree across the
	// push, so no other writer moves main between the merge and the push.
	// Outcome recording, classification and routing run after it.
	pushOn := autoPushEnabled()
	var mergeResult git.MergeResult
	var mergeErr, pushErr error
	blockErr := o.withTree(ctx, func(ctx context.Context) error {
		if err := o.requireStillCommitting(ctx, run.RunID); err != nil {
			return err
		}
		mergeResult, mergeErr = o.git.Merge(ctx, git.MergeOpts{
			Source:   run.BranchName,
			Target:   authoritativeBranch,
			Strategy: "merge-commit",
			Message:  fmt.Sprintf("Merge run %s: %s", run.RunID, run.TaskPath),
			Trailers: trailers,
		})
		if mergeErr != nil {
			// Abort early, inside the block, so the tree is clean for
			// whatever holds it next.
			o.abortMerge(ctx)
			return nil
		}
		if pushOn {
			pushErr = o.git.Push(ctx, "origin", "main")
		}
		return nil
	})
	if done, ret := blockExit(blockErr); done {
		return true, ret
	}
	if blockErr != nil {
		mergeErr = blockMergeError(blockErr)
	}

	// On merge failure: the only retry-relevant signal is mergeErr.
	if mergeErr != nil {
		// Record the failed outcome before any state transition so the
		// audit row is in place. A persistence failure here is fatal:
		// returning the error keeps the run in committing for retry,
		// matching the symmetric treatment in the code-repo path. The
		// classifier-driven retry/fail logic below only runs when the
		// row is safely recorded.
		if recErr := o.recordPrimaryMergeOutcome(ctx, run, "", mergeErr, ""); recErr != nil {
			return false, recErr
		}

		var gitErr *git.GitError
		if errors.As(mergeErr, &gitErr) && gitErr.IsRetryable() {
			log.Warn("transient merge failure, will retry",
				"run_id", run.RunID, "branch", run.BranchName, "error", mergeErr)
			return true, o.retryMerge(ctx, run)
		}

		// For planning runs with a collision handler, check if this is an
		// ID collision and attempt to renumber before failing.
		if run.Mode == domain.RunModePlanning && o.collision != nil {
			if newPath, renErr := o.collision.DetectAndRenumber(ctx, run, 2); renErr == nil && newPath != "" {
				log.Info("artifact renumbered after ID collision, retrying merge",
					"run_id", run.RunID, "old_path", run.TaskPath, "new_path", newPath)
				run.TaskPath = newPath
				return true, o.retryMerge(ctx, run)
			}
		}

		log.Error("permanent merge failure",
			"run_id", run.RunID, "branch", run.BranchName, "error", mergeErr)
		return true, o.handlePermanentMergeFailure(ctx, run, mergeErr)
	}

	log.Info("run branch merged",
		"run_id", run.RunID,
		"branch", run.BranchName,
		"merge_sha", mergeResult.SHA,
		"fast_forward", mergeResult.FastForward,
	)

	// The push of the authoritative branch ran in the block. On failure
	// record the primary outcome as failed before the run state
	// transition: the local merge happened (mergeResult.SHA is known) but
	// origin/main has not advanced, so dashboards must see the same
	// "failed" status they would for a code repo whose push did not land.
	// The local SHA is captured in failure_detail so the recovery path can
	// confirm what was applied locally.
	if pushOn {
		if pushErr != nil {
			if recErr := o.recordPrimaryMergeOutcome(ctx, run, mergeResult.SHA, pushErr, "push"); recErr != nil {
				return false, recErr
			}

			// Classify push error: auth failures are permanent (don't retry),
			// transient errors (network) stay in committing for scheduler retry.
			var gitErr *git.GitError
			if errors.As(pushErr, &gitErr) && !gitErr.IsRetryable() {
				log.Error("auto-push: permanent push failure (auth or rejected), failing run",
					"run_id", run.RunID, "error", pushErr)
				return true, o.handlePermanentMergeFailure(ctx, run, pushErr)
			}
			log.Warn("auto-push: transient push failure, staying in committing for retry",
				"run_id", run.RunID, "error", pushErr)
			return true, o.retryMerge(ctx, run)
		}
	}

	// Merge AND push (when enabled) succeeded — record the merged
	// outcome before the run-state transition so dashboards see a
	// fully populated row at the moment the run completes. A
	// persistence failure here is fatal: returning the error keeps
	// the run in committing so the scheduler retries, and a no-op
	// merge on the second attempt re-records cleanly.
	if recErr := o.recordPrimaryMergeOutcome(ctx, run, mergeResult.SHA, nil, ""); recErr != nil {
		return false, recErr
	}
	return false, nil
}

// verifyAndComplete enforces the EPIC-005 AC #5 rule (every affected
// repository must merge before the run completes), then transitions the
// run to its terminal state. The primary merge has already landed and
// its outcome is recorded by mergePrimary; this helper checks the
// per-repo outcomes for permanent failures and routes accordingly:
//
//   - Lookup error  → propagate so the run stays in committing and the
//     scheduler retries when the store recovers (fail-closed).
//   - Permanent code-repo failure → transitionToPartiallyMerged: the
//     run is blocked until an operator resolves the failed code repo,
//     but the work that has merged stays preserved and the scheduler
//     keeps the run resumable so a retry after manual resolution
//     promotes it back to committing (per TASK-003).
//   - All clean   → completeAfterMerge with branch cleanup, then
//     advancePublishStepIfAny so workflows with a publish step (type:
//     internal, handler: merge) advance via the "published" outcome.
//     No-op on workflows that model commit via terminal-outcome
//     metadata — the merge has landed either way.
//
// Under merge binding a run branch whose tip held commits the merge left
// out (src.excludedTip), or whose tip could not be judged
// (src.tipUnknown), is kept for inspection instead of being cleaned up
// (owner question 3).
func (o *Orchestrator) verifyAndComplete(ctx context.Context, run *domain.Run, src mergeSource) error {
	log := observe.Logger(ctx)

	if failed, listErr := o.firstPermanentCodeRepoFailure(ctx, run.RunID); listErr != nil {
		// Fail-closed: surface the lookup error so the run stays in
		// committing and the scheduler retries when the store
		// recovers, rather than guess at completeness.
		return fmt.Errorf("verify code repo merge completion: %w", listErr)
	} else if failed != nil {
		log.Error("code repo merge permanently failed; transitioning run to partially-merged",
			"run_id", run.RunID,
			"repository_id", failed.RepositoryID,
			"failure_class", string(failed.FailureClass),
			"failure_detail", failed.FailureDetail,
		)
		return o.transitionToPartiallyMerged(ctx, run, failed)
	}

	// Transition committing → completed (with branch cleanup, unless a
	// bound merge left commits on a run branch out: the primary's, or any
	// code repository's — decided here, not from this attempt alone,
	// because a code repository may have merged in an earlier attempt).
	cleanup := true
	if o.bindingOn {
		cleanup = src.excludedTip == "" && !src.tipUnknown && !o.codeRepoHasExcludedCommits(ctx, run)
	}
	if err := o.completeAfterMerge(ctx, run, cleanup); err != nil {
		return err
	}
	return o.advancePublishStepIfAny(ctx, run, "published")
}

// handlePermanentMergeFailure routes a permanent merge error through the
// workflow's publish step if one exists: the step's merge_failed outcome
// sends the run back to execute (per task-default.yaml) so the actor can
// retry. When the workflow has no publish step (the legacy pattern used
// by artifact-creation, adr, etc.) the run is failed outright — same
// behaviour as before TASK-015.
func (o *Orchestrator) handlePermanentMergeFailure(ctx context.Context, run *domain.Run, mergeErr error) error {
	wfDef, wfErr := o.wfLoader.LoadWorkflow(ctx, run.WorkflowPath, run.WorkflowVersion)
	if wfErr != nil {
		// Fall back to the run-failed path if we can't load the workflow.
		return o.failRunOnMergeError(ctx, run, mergeErr)
	}
	stepExec, _ := o.findActiveMergeStep(ctx, run, wfDef)
	if stepExec == nil {
		return o.failRunOnMergeError(ctx, run, mergeErr)
	}
	o.recordGitConflictOnStep(ctx, run, mergeErr)
	return o.advancePublishStepIfAny(ctx, run, "merge_failed")
}

// abortMerge cleans up a failed merge so the repo is not left dirty. It
// runs inside the merge block (INIT-023 TASK-012): the abort re-enters the
// tree the merge holds.
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
// Uses TransitionRunStatus (compare-and-swap) to prevent duplicate
// run_completed events when concurrent MergeRunBranch calls race.
func (o *Orchestrator) completeAfterMerge(ctx context.Context, run *domain.Run, cleanupBranch bool) error {
	result, err := workflow.EvaluateRunTransition(run.Status, workflow.TransitionRequest{
		Trigger: workflow.TriggerGitCommitSucceeded,
	})
	if err != nil {
		return err
	}

	// Atomically transition only if the run is still in the expected state.
	// A concurrent MergeRunBranch may have already completed this run.
	applied, err := o.store.TransitionRunStatus(ctx, run.RunID, run.Status, result.ToStatus)
	if err != nil {
		return fmt.Errorf("update run status: %w", err)
	}
	if !applied {
		observe.Logger(ctx).Info("run already transitioned, skipping duplicate completion",
			"run_id", run.RunID)
		return nil
	}

	o.emitEvent(ctx, domain.EventRunCompleted, run.RunID, run.TraceID,
		fmt.Sprintf("evt-%s-completed", run.TraceID[:12]), nil)

	log := observe.Logger(ctx)
	log.Info("run completed after merge", "run_id", run.RunID)
	if run.StartedAt != nil {
		observe.GlobalMetrics.RunDuration.ObserveDuration(time.Since(*run.StartedAt))
	}

	// Clean up the branch only if the main push succeeded (or auto-push is off).
	// When main push fails, the remote run branch is the only ref containing
	// the merged commits — preserve it for collaborators.
	if cleanupBranch {
		if err := o.CleanupRunBranch(ctx, run.RunID); err != nil {
			log.Warn("cleanup_run_branch failed",
				"run_id", run.RunID,
				"branch", run.BranchName,
				"error", err,
			)
		}
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

// transitionToPartiallyMerged moves a run from committing to
// partially-merged when the primary repo merge has landed but at least
// one affected code repo ended in a permanent-failed state. The
// branches involved (both the merged and the failed) stay preserved
// — CleanupRunBranch keys off the per-repo outcomes for that — so an
// operator can resolve the conflict against the unmodified source
// ref. The scheduler resumes the run after operator action; the
// per-repo loop's already-terminal skip guard prevents already-merged
// repos from being re-attempted.
func (o *Orchestrator) transitionToPartiallyMerged(ctx context.Context, run *domain.Run, failedOutcome *domain.RepositoryMergeOutcome) error {
	result, err := workflow.EvaluateRunTransition(run.Status, workflow.TransitionRequest{
		Trigger: workflow.TriggerCodeRepoPartialFailure,
	})
	if err != nil {
		return err
	}

	applied, err := o.store.TransitionRunStatus(ctx, run.RunID, run.Status, result.ToStatus)
	if err != nil {
		return fmt.Errorf("update run status: %w", err)
	}
	if !applied {
		observe.Logger(ctx).Info("run already transitioned, skipping duplicate partial-merge transition",
			"run_id", run.RunID)
		return nil
	}

	// Surface the partial-merge classification on the last completed
	// step so step-level dashboards and HTTP detail responses agree
	// with the per-repo outcomes table on what is blocking the run.
	stepErr := fmt.Errorf("code repo %q merge failed (class=%s): %s",
		failedOutcome.RepositoryID, failedOutcome.FailureClass, failedOutcome.FailureDetail)
	o.recordGitConflictOnStep(ctx, run, stepErr)

	// Each retry of a partially-merged run can re-enter this state if
	// the operator's resolution failed; using a unique suffix per
	// transition keeps the delivery queue and event log from
	// deduping later occurrences against the first one. Same pattern
	// as the run paused/resumed events.
	o.emitEvent(ctx, domain.EventRunPartiallyMerged, run.RunID, run.TraceID,
		fmt.Sprintf("evt-%s-partially-merged-%d", run.TraceID[:12], time.Now().UnixMilli()), nil)
	return nil
}

// failRunOnMergeError transitions a run to failed due to a permanent merge error.
// Persists git_conflict classification on the last completed step for visibility.
//
// The run write is a compare-and-swap from the status the merge read
// (INIT-023 TASK-008): when another path already moved the run (a
// concurrent merge attempt completed it, an operator acted), the miss is
// logged and nil returned, exactly like completeAfterMerge — no run_failed,
// no executions closed, and no merge-failure detail written onto a run
// that another path finished (the git_conflict detail is recorded only
// after the transition applied).
func (o *Orchestrator) failRunOnMergeError(ctx context.Context, run *domain.Run, mergeErr error) error {
	log := observe.Logger(ctx)

	result, err := workflow.EvaluateRunTransition(run.Status, workflow.TransitionRequest{
		Trigger: workflow.TriggerGitCommitFailedPerm,
	})
	if err != nil {
		return err
	}

	applied, err := o.store.TransitionRunStatus(ctx, run.RunID, run.Status, result.ToStatus)
	if err != nil {
		return fmt.Errorf("update run status: %w", err)
	}
	if !applied {
		log.Info("run already transitioned, skipping permanent merge failure",
			"run_id", run.RunID, "error", mergeErr)
		return nil
	}

	// Persist git_conflict detail on the last completed step so actors
	// can see why the run failed via the step execution record.
	o.recordGitConflictOnStep(ctx, run, mergeErr)
	// TerminateStepsOnCancel (INIT-023 TASK-007): the publish step (and any
	// other live execution) closes with the run.
	o.terminateLiveSteps(ctx, run.RunID, "run failed: merge failed permanently")

	o.emitEvent(ctx, domain.EventRunFailed, run.RunID, run.TraceID,
		fmt.Sprintf("evt-%s-failed", run.TraceID[:12]), nil)

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

	// Classify based on error type: spent retry budgets get
	// merge_retries_exhausted (INIT-023 TASK-012, P3-27), a status-lifecycle
	// refusal gets status_lifecycle_violation (INIT-023 TASK-017), merge
	// conflicts get git_conflict, other permanent merge errors get
	// permanent_error.
	classification := domain.FailurePermanent
	var gitErr *git.GitError
	var lifecycleErr *StatusLifecycleError
	switch {
	case isRetriesExhausted(mergeErr):
		classification = domain.FailureMergeRetriesExhausted
	case errors.As(mergeErr, &lifecycleErr):
		classification = domain.FailureStatusLifecycle
	case errors.As(mergeErr, &gitErr) && strings.Contains(gitErr.Message, "conflict"):
		classification = domain.FailureGitConflict
	}

	prior := stateOf(lastCompleted)
	lastCompleted.ErrorDetail = &domain.ErrorDetail{
		Classification: classification,
		Message:        fmt.Sprintf("merge failed: %s", mergeErr.Error()),
		StepID:         lastCompleted.StepID,
	}
	if updateErr := o.writeStepExecution(ctx, lastCompleted, prior); updateErr != nil {
		observe.Logger(ctx).Warn("failed to record git conflict on step", "error", updateErr)
	}
}

// checkBranchProtectMerge consults the installed branch-protection
// policy before MergeRunBranch advances the authoritative branch
// (ADR-009 §3). The operation is classified as OpGovernedMerge — the
// real policy allows those unconditionally (rules do not gate governed
// merges). The check still runs so a nil policy is caught as a
// configuration error rather than a silent bypass, and so any future
// evaluator that does want to gate merges has a single site to plug in.
//
// Note: rule-source failures for OpGovernedMerge do not surface from
// the real policy — it short-circuits before loading rules. Fail-closed
// on source errors is enforced on the DirectWrite path (artifact
// service); this path's fail-closed property is limited to "nil policy".
//
// The actor on the request is whichever actor is bound to ctx (may be
// nil on scheduler recovery paths — OpGovernedMerge does not consult
// Actor.Role, so a zero actor is fine). RunID comes from the Run being
// merged; TraceID from the Run's trace so override/audit events
// correlate with the request that started the run.
func (o *Orchestrator) checkBranchProtectMerge(ctx context.Context, run *domain.Run) error {
	if o.policy == nil {
		return domain.NewError(domain.ErrUnavailable,
			"engine: branch-protection policy not configured (production must call WithBranchProtectPolicy)")
	}

	var actor domain.Actor
	if a := domain.ActorFromContext(ctx); a != nil {
		actor = *a
	}

	req := branchprotect.Request{
		Branch:  authoritativeBranch,
		Kind:    branchprotect.OpGovernedMerge,
		Actor:   actor,
		RunID:   run.RunID,
		TraceID: run.TraceID,
	}

	decision, reasons, err := o.policy.Evaluate(ctx, req)
	if err != nil {
		observe.Logger(ctx).Error("branch-protection evaluation failed on merge",
			"run_id", run.RunID,
			"branch", authoritativeBranch,
			"error", err.Error(),
		)
		return domain.NewError(domain.ErrInternal,
			fmt.Sprintf("branch-protection evaluation failed: %v", err))
	}

	if decision == branchprotect.DecisionDeny {
		observe.Logger(ctx).Warn("branch-protection denied governed merge",
			"run_id", run.RunID,
			"branch", authoritativeBranch,
			"reasons", denyReasonCodes(reasons),
		)
		return domain.NewError(domain.ErrForbidden,
			firstMergeDenyMessage(reasons, run.RunID))
	}

	return nil
}

func denyReasonCodes(reasons []branchprotect.Reason) []string {
	out := make([]string, len(reasons))
	for i, r := range reasons {
		out[i] = string(r.Code)
	}
	return out
}

func firstMergeDenyMessage(reasons []branchprotect.Reason, runID string) string {
	if len(reasons) == 0 {
		return fmt.Sprintf("governed merge for run %s denied by branch protection", runID)
	}
	return reasons[0].Message
}

// statusFieldRegexp matches the status line in YAML front matter.
var statusFieldRegexp = regexp.MustCompile(`(?m)^status:\s*.*$`)

// applyCommitStatus rewrites frontmatter status on every artifact file added
// on the run branch vs the authoritative branch, and lands all rewrites in a
// single commit on the branch. This cascade is required by TASK-016: a
// planning run that adds a parent artifact plus child artifacts via
// POST /artifacts/add (or direct file writes on the branch) should get one
// operator review = one status transition for the whole planned package.
// Without the cascade, children are merged to main as Draft while only
// run.TaskPath gets rewritten.
//
// Enumeration is best-effort: if Diff fails, the cascade degrades to the
// original single-artifact behaviour on run.TaskPath so the primary contract
// is preserved.
//
// It must run inside the cascade block (preflightCheck, INIT-023
// TASK-012): its checkout → write → commit → checkout is a working-tree
// sequence, and ctx must be the block's.
func (o *Orchestrator) applyCommitStatus(ctx context.Context, run *domain.Run, newStatus string) error {
	log := observe.Logger(ctx)

	paths := o.cascadePaths(ctx, run)

	updates, err := o.collectStatusRewrites(ctx, run, run.BranchName, "on branch "+run.BranchName, paths, newStatus)
	if err != nil {
		return err
	}

	if len(updates) == 0 {
		log.Info("artifact statuses already match, no cascade commit needed",
			"run_id", run.RunID, "status", newStatus)
		return nil
	}

	if err := o.git.Checkout(ctx, run.BranchName); err != nil {
		return fmt.Errorf("checkout branch: %w", err)
	}
	for path, content := range updates {
		if err := o.git.WriteAndStageFile(ctx, path, content); err != nil {
			_ = o.git.Checkout(ctx, authoritativeBranch)
			return fmt.Errorf("write updated artifact %s: %w", path, err)
		}
	}
	msg := statusCascadeMessage(newStatus, len(updates))
	if _, err := o.git.Commit(ctx, git.CommitOpts{
		Message: msg,
		Trailers: map[string]string{
			"Run-ID":   run.RunID,
			"Trace-ID": run.TraceID,
		},
	}); err != nil {
		_ = o.git.Checkout(ctx, authoritativeBranch)
		return fmt.Errorf("commit status update: %w", err)
	}
	if err := o.git.Checkout(ctx, authoritativeBranch); err != nil {
		return fmt.Errorf("checkout main after status update: %w", err)
	}

	for path := range updates {
		log.Info("artifact status updated on branch",
			"run_id", run.RunID, "artifact_path", path, "new_status", newStatus)
	}
	return nil
}

// collectStatusRewrites reads each cascade path at ref and returns the
// files whose front-matter status must become newStatus (path → new
// content). The primary artifact (run.TaskPath) must be readable and have
// front matter; other paths are skipped when unreadable, when they are
// not Spine artifacts, or when their front matter is malformed. where
// describes ref in the primary read error ("on branch <b>", "at bound
// commit <sha>").
func (o *Orchestrator) collectStatusRewrites(ctx context.Context, run *domain.Run, ref, where string, paths []string, newStatus string) (map[string]string, error) {
	log := observe.Logger(ctx)
	updates := make(map[string]string, len(paths))
	for _, path := range paths {
		content, err := o.git.ReadFile(ctx, ref, path)
		if err != nil {
			if path == run.TaskPath {
				return nil, fmt.Errorf("read artifact %s: %w", where, err)
			}
			log.Warn("skip cascade path, read failed",
				"run_id", run.RunID, "artifact_path", path, "error", err)
			continue
		}
		// For non-primary paths, require the file to parse as a Spine
		// artifact before rewriting. This prevents commit.status from
		// clobbering the `status:` field on incidental Markdown files
		// (e.g., deliverables with their own YAML frontmatter) that a
		// run happens to add on the branch.
		if path != run.TaskPath && !artifact.IsArtifact(content) {
			continue
		}
		original := string(content)
		updated, err := rewriteStatusField(original, newStatus, path)
		if err != nil {
			if path == run.TaskPath {
				return nil, err
			}
			continue
		}
		if updated == original {
			continue
		}
		updates[path] = updated
	}
	return updates, nil
}

// cascadePaths returns run.TaskPath plus any additional .md files the run
// added on its branch since it diverged from the authoritative branch. The
// diff is scoped to the merge-base, not to the current tip of main — if
// main has advanced while the run was in-flight (e.g. another run deleted
// an unrelated artifact), diffing against main's tip would misreport that
// deletion as "added on the run branch" and the cascade would resurrect
// (or conflict on) the deletion at merge time.
//
// On any Diff/MergeBase failure the cascade degrades to just the primary
// path so applyCommitStatus still meets its original single-artifact
// contract.
func (o *Orchestrator) cascadePaths(ctx context.Context, run *domain.Run) []string {
	paths := []string{run.TaskPath}
	base, err := o.git.MergeBase(ctx, authoritativeBranch, run.BranchName)
	if err != nil || base == "" {
		observe.Logger(ctx).Warn("merge-base for status cascade failed, falling back to primary only",
			"run_id", run.RunID, "error", err)
		return paths
	}
	diffs, err := o.git.Diff(ctx, base, run.BranchName)
	if err != nil {
		observe.Logger(ctx).Warn("diff for status cascade failed, falling back to primary only",
			"run_id", run.RunID, "error", err)
		return paths
	}
	return appendAddedMarkdown(paths, run.TaskPath, diffs)
}

// appendAddedMarkdown appends every added .md path in diffs to paths,
// once, skipping taskPath (already first).
//
// A path discovery refuses (domain.UnsafeRepositoryPath: a control
// character, U+2028/U+2029, a bidi control, invalid UTF-8) is skipped too
// (wave 3 review, codex): it is never a projected artifact, and since
// TASK-003's NUL-delimited Diff lists it raw instead of C-quoted, it was
// read and rewritten, and the cascade commit then failed as a whole on
// it — CommitFilesOnto refuses a control byte in a path — so a run that
// added one such file merged with its task's status never updated. Before
// TASK-003 git's quoted listing made its read fail, which skipped it.
func appendAddedMarkdown(paths []string, taskPath string, diffs []git.FileDiff) []string {
	seen := map[string]struct{}{taskPath: {}}
	for _, d := range diffs {
		if d.Status != "added" {
			continue
		}
		if !strings.HasSuffix(d.Path, ".md") {
			continue
		}
		if domain.UnsafeRepositoryPath(d.Path) {
			continue
		}
		if _, ok := seen[d.Path]; ok {
			continue
		}
		seen[d.Path] = struct{}{}
		paths = append(paths, d.Path)
	}
	return paths
}

// rewriteStatusField replaces the status line in YAML frontmatter. Returns
// the original content unchanged when the status already matches. Returns
// an error when the file has no (or an unclosed) frontmatter block — the
// caller decides whether that is fatal (primary) or skippable (cascade).
func rewriteStatusField(original, newStatus, path string) (string, error) {
	if !strings.HasPrefix(original, "---") {
		return "", fmt.Errorf("artifact %s has no front matter", path)
	}
	endIdx := strings.Index(original[3:], "---")
	if endIdx == -1 {
		return "", fmt.Errorf("artifact %s has unclosed front matter", path)
	}
	endIdx += 3
	frontMatter := original[:endIdx]
	rest := original[endIdx:]
	return statusFieldRegexp.ReplaceAllString(frontMatter, "status: "+newStatus) + rest, nil
}
