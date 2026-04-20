package engine

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/bszymi/spine/internal/branchprotect"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/workflow"
)

// authoritativeBranch is the destination of every governed merge the
// Orchestrator performs today. Centralised here so the branch-protection
// request is built consistently with the git.MergeOpts target below — if
// either changes, they change together.
const authoritativeBranch = "main"

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
		return err
	}

	// Apply commit metadata (e.g., rewrite artifact status) on the branch
	// before merging so the updated file lands on main.
	if newStatus := run.CommitMeta["status"]; newStatus != "" {
		if err := o.applyCommitStatus(ctx, run, newStatus); err != nil {
			log.Warn("failed to apply commit status, proceeding with merge",
				"run_id", runID, "error", err)
		}
	}

	// Perform the merge into the authoritative branch explicitly.
	trailers := map[string]string{
		"Run-ID":   runID,
		"Trace-ID": run.TraceID,
	}

	mergeResult, err := o.git.Merge(ctx, git.MergeOpts{
		Source:   run.BranchName,
		Target:   authoritativeBranch,
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

		// For planning runs with a collision handler, check if this is an
		// ID collision and attempt to renumber before failing.
		if run.Mode == domain.RunModePlanning && o.collision != nil {
			if newPath, renErr := o.collision.DetectAndRenumber(ctx, run, 2); renErr == nil && newPath != "" {
				log.Info("artifact renumbered after ID collision, retrying merge",
					"run_id", runID, "old_path", run.TaskPath, "new_path", newPath)
				run.TaskPath = newPath
				return o.retryMerge(ctx, run)
			}
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
			// Classify push error: auth failures are permanent (don't retry),
			// transient errors (network) stay in committing for scheduler retry.
			var gitErr *git.GitError
			if errors.As(err, &gitErr) && !gitErr.IsRetryable() {
				log.Error("auto-push: permanent push failure (auth or rejected), failing run",
					"run_id", runID, "error", err)
				return o.failRunOnMergeError(ctx, run, err)
			}
			log.Warn("auto-push: transient push failure, staying in committing for retry",
				"run_id", runID, "error", err)
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

	observe.Logger(ctx).Info("run completed after merge", "run_id", run.RunID)
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

// applyCommitStatus rewrites the artifact file's frontmatter status on the
// run branch before merging. This ensures the merged file on main reflects
// the workflow outcome's commit.status (e.g., Draft → Pending).
func (o *Orchestrator) applyCommitStatus(ctx context.Context, run *domain.Run, newStatus string) error {
	log := observe.Logger(ctx)

	// Read the artifact file from the branch.
	content, err := o.git.ReadFile(ctx, run.BranchName, run.TaskPath)
	if err != nil {
		return fmt.Errorf("read artifact on branch %s: %w", run.BranchName, err)
	}

	// Rewrite the status field in the front matter.
	original := string(content)
	if !strings.HasPrefix(original, "---") {
		return fmt.Errorf("artifact %s has no front matter", run.TaskPath)
	}
	endIdx := strings.Index(original[3:], "---")
	if endIdx == -1 {
		return fmt.Errorf("artifact %s has unclosed front matter", run.TaskPath)
	}
	endIdx += 3

	frontMatter := original[:endIdx]
	rest := original[endIdx:]
	updated := statusFieldRegexp.ReplaceAllString(frontMatter, "status: "+newStatus) + rest

	if updated == original {
		log.Info("artifact status already matches, no rewrite needed",
			"run_id", run.RunID, "status", newStatus)
		return nil
	}

	// Checkout the branch, write the updated file, commit, then return to main.
	if err := o.git.Checkout(ctx, run.BranchName); err != nil {
		return fmt.Errorf("checkout branch: %w", err)
	}

	if err := o.git.WriteAndStageFile(ctx, run.TaskPath, updated); err != nil {
		_ = o.git.Checkout(ctx, "main")
		return fmt.Errorf("write updated artifact: %w", err)
	}

	if _, err := o.git.Commit(ctx, git.CommitOpts{
		Message: fmt.Sprintf("Update artifact status to %s", newStatus),
		Trailers: map[string]string{
			"Run-ID":   run.RunID,
			"Trace-ID": run.TraceID,
		},
	}); err != nil {
		_ = o.git.Checkout(ctx, "main")
		return fmt.Errorf("commit status update: %w", err)
	}

	if err := o.git.Checkout(ctx, "main"); err != nil {
		return fmt.Errorf("checkout main after status update: %w", err)
	}

	log.Info("artifact status updated on branch",
		"run_id", run.RunID, "path", run.TaskPath, "status", newStatus)
	return nil
}
