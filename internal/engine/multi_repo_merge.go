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
	"github.com/bszymi/spine/internal/repository"
)

// errPendingCodeRepoRetry signals that one or more code repos have a
// non-terminal merge outcome (pending or failed-with-transient class)
// after the loop ran. The caller routes this through retryMerge so
// the run stays in committing state and the scheduler picks it up
// again — without this signal a transient code-repo failure would
// strand its outcome at `failed/network` forever, because the primary
// merge would proceed and complete the run.
var errPendingCodeRepoRetry = errors.New("code repo merge has non-terminal outcomes; retry needed")

// firstPermanentCodeRepoFailure scans the run's recorded outcomes for
// any code repo (non-primary) whose merge ended in a terminal failed
// state. EPIC-005 AC #5 says completed runs require every affected
// repo to merge successfully; this helper returns the first failed
// outcome so MergeRunBranch can fail the run instead of completing
// it. Returns nil when every code repo's outcome is merged, skipped,
// or resolved externally.
//
// A store error is treated conservatively as "cannot confirm clean
// completion" and surfaced to the caller, matching the fail-closed
// posture used in CleanupRunBranch's outcome lookup.
func (o *Orchestrator) firstPermanentCodeRepoFailure(ctx context.Context, runID string) (*domain.RepositoryMergeOutcome, error) {
	outcomes, err := o.store.ListRepositoryMergeOutcomes(ctx, runID)
	if err != nil {
		return nil, err
	}
	for i := range outcomes {
		if outcomes[i].RepositoryID == repository.PrimaryRepositoryID {
			continue
		}
		if outcomes[i].Status == domain.RepositoryMergeStatusFailed {
			cp := outcomes[i]
			return &cp, nil
		}
	}
	return nil, nil
}

// mergeAffectedCodeRepositories runs the per-repo merge for every
// non-primary entry in run.AffectedRepositories. Each attempt produces
// a domain.RepositoryMergeOutcome row that is upserted before the
// next repo is touched, so partial cross-repo state is queryable from
// the moment it exists rather than reconstructed afterwards.
//
// TASK-002 contract:
//   - merge each affected code repo independently before the primary;
//   - record each outcome immediately;
//   - failures in one code repo do not undo prior successful merges
//     (git merges are not transactional anyway, and we never call
//     anything that would revert a landed merge);
//   - the primary merge runs only after every code repo's outcome
//     reaches a terminal state (merged, skipped, resolved-externally,
//     or failed with a permanent class). A non-terminal outcome
//     (failed-transient, pending) returns errPendingCodeRepoRetry so
//     the scheduler retries before the primary ledger advances.
//
// Returns:
//   - nil when every code repo's outcome is terminal or the run has
//     no code repos.
//   - errPendingCodeRepoRetry when at least one code repo has a
//     non-terminal outcome the scheduler should re-attempt.
//   - any other error when the orchestrator cannot even attempt the
//     work (missing wiring, registry lookup failure, store error
//     persisting an outcome row).
func (o *Orchestrator) mergeAffectedCodeRepositories(ctx context.Context, run *domain.Run) error {
	log := observe.Logger(ctx)

	// Fast path: a primary-only run has nothing to do here. Avoids the
	// repoClients/repositories nil checks below for legacy single-repo
	// deployments and every test that stubs only o.git.
	hasCodeRepo := false
	for _, repoID := range run.AffectedRepositories {
		if repoID != "" && repoID != repository.PrimaryRepositoryID {
			hasCodeRepo = true
			break
		}
	}
	if !hasCodeRepo {
		return nil
	}

	if o.repoClients == nil || o.repositories == nil {
		return domain.NewError(domain.ErrPrecondition,
			"multi-repo merge requires WithRepositoryGitClients and WithRepositoryResolver wirings")
	}

	anyNonTerminal := false
	for _, repoID := range run.AffectedRepositories {
		if repoID == "" || repoID == repository.PrimaryRepositoryID {
			continue
		}
		// Retry-safety: a transient primary or later-repo failure puts
		// the run back in committing, which causes the scheduler to
		// re-invoke MergeRunBranch and re-walk this loop. A repo with
		// an already-terminal outcome (merged, skipped, resolved
		// externally, or failed with a permanent class) must NOT be
		// re-attempted: a no-op merge followed by a fresh transient
		// push or auth blip would otherwise overwrite a successful
		// `merged` row with a `failed` row, and CleanupRunBranch would
		// preserve a branch for a repo that already landed.
		//
		// A real lookup error (anything other than ErrNotFound) is
		// fail-closed: a transient store read must NOT silently
		// re-merge a repo that already has a terminal outcome — the
		// scheduler should retry once the store recovers.
		existing, lookupErr := o.store.GetRepositoryMergeOutcome(ctx, run.RunID, repoID)
		if lookupErr != nil && !isNotFoundError(lookupErr) {
			return fmt.Errorf("lookup existing outcome for %q: %w", repoID, lookupErr)
		}
		if existing != nil && existing.IsTerminal() {
			log.Info("code repo merge already terminal, skipping retry",
				"run_id", run.RunID,
				"repository_id", repoID,
				"merge_status", string(existing.Status))
			continue
		}
		outcome, err := o.attemptCodeRepositoryMerge(ctx, run, repoID)
		if err != nil {
			// Non-recoverable per-repo error (e.g. registry no longer
			// knows the repo). Bubble up so the run stays in committing
			// for the scheduler to retry once the registry is fixed.
			// We do not synthesise an outcome row with a placeholder
			// target_branch: the model requires both branches and a
			// fabricated target would lie about what was attempted.
			return fmt.Errorf("merge code repository %q: %w", repoID, err)
		}
		if upsertErr := o.store.UpsertRepositoryMergeOutcome(ctx, &outcome); upsertErr != nil {
			// Failing to persist an outcome is fatal: the AC requires
			// outcome data to land before the primary ledger update,
			// and a missing row would let dashboards mistake "we
			// merged but didn't record it" for "we never tried".
			return fmt.Errorf("record merge outcome for %q: %w", repoID, upsertErr)
		}
		log.Info("code repo merge outcome recorded", outcome.LogFields()...)
		if !outcome.IsTerminal() {
			anyNonTerminal = true
		}
	}
	if anyNonTerminal {
		// Run stays in committing so the scheduler retries; on the
		// next pass terminal outcomes are skipped and only the still-
		// non-terminal repos are re-attempted.
		return errPendingCodeRepoRetry
	}
	return nil
}

// attemptCodeRepositoryMerge does the actual git work for one code
// repository and shapes the result into a RepositoryMergeOutcome.
// It returns the outcome (success or failed-with-class) plus a
// non-nil error only when we could not even start the attempt
// (e.g. the registry no longer knows about the repo). Merge or push
// errors translate into a failed outcome and a nil error so the
// caller can persist the row and move on.
func (o *Orchestrator) attemptCodeRepositoryMerge(ctx context.Context, run *domain.Run, repoID string) (domain.RepositoryMergeOutcome, error) {
	log := observe.Logger(ctx)

	repo, err := o.repositories.Lookup(ctx, repoID)
	if err != nil {
		return domain.RepositoryMergeOutcome{}, err
	}
	if repo.DefaultBranch == "" {
		return domain.RepositoryMergeOutcome{}, domain.NewError(domain.ErrPrecondition,
			fmt.Sprintf("repository %q has no default_branch configured", repoID))
	}
	client, err := o.repoClients.Client(ctx, repoID)
	if err != nil {
		return domain.RepositoryMergeOutcome{}, err
	}

	now := time.Now()
	attempts := o.nextAttemptNumber(ctx, run.RunID, repoID)
	// Status is set in every code path below (merged on success, failed
	// on merge or push error); no initial value here so a forgotten
	// branch would surface as a Validate error rather than silently
	// upserting a row stuck on the wrong status.
	outcome := domain.RepositoryMergeOutcome{
		RunID:           run.RunID,
		RepositoryID:    repoID,
		SourceBranch:    run.BranchName,
		TargetBranch:    repo.DefaultBranch,
		Attempts:        attempts,
		LastAttemptedAt: &now,
	}

	trailers := map[string]string{
		"Run-ID":   run.RunID,
		"Trace-ID": run.TraceID,
	}
	mergeResult, mergeErr := client.Merge(ctx, git.MergeOpts{
		Source:   run.BranchName,
		Target:   repo.DefaultBranch,
		Strategy: "merge-commit",
		Message:  fmt.Sprintf("Merge run %s: %s", run.RunID, run.TaskPath),
		Trailers: trailers,
	})
	if mergeErr != nil {
		// Symmetric with the primary merge path: abort the in-progress
		// merge so the cached clone is not left with MERGE_HEAD and a
		// conflicted index. Without this, the next operation against
		// the same gitpool entry — a retry, a branch cleanup, an
		// unrelated run — fails with "You have not concluded your
		// merge." Best-effort: a failing abort is logged but does not
		// change the outcome we record.
		if _, abortErr := client.Merge(ctx, git.MergeOpts{Source: "--abort", Strategy: "abort"}); abortErr != nil {
			log.Warn("code repo merge abort failed", "repository_id", repoID, "error", abortErr)
		}
		class, detail := classifyMergeFailure(mergeErr)
		outcome.Status = domain.RepositoryMergeStatusFailed
		outcome.FailureClass = class
		outcome.FailureDetail = detail
		log.Warn("code repo merge failed", "repository_id", repoID, "error", mergeErr)
		return outcome, nil
	}

	// Push the merged ref to origin so the remote sees what we just
	// wrote locally. Without this the next run's clone would see a
	// stale default branch and re-attempt the same work. Push errors
	// classify and surface as a failed outcome — the local merge has
	// landed, but as far as collaborators are concerned the repo is
	// not yet updated, which is the failure mode we need recorded.
	if autoPushEnabled() {
		if pushErr := client.Push(ctx, "origin", repo.DefaultBranch); pushErr != nil {
			class, detail := classifyMergeFailure(pushErr)
			outcome.Status = domain.RepositoryMergeStatusFailed
			outcome.FailureClass = class
			outcome.FailureDetail = fmt.Sprintf("local merge succeeded (sha=%s) but push failed: %s",
				mergeResult.SHA, detail)
			log.Warn("code repo push after merge failed",
				"repository_id", repoID,
				"merge_sha", mergeResult.SHA,
				"error", pushErr)
			return outcome, nil
		}
	}

	mergedAt := time.Now()
	outcome.Status = domain.RepositoryMergeStatusMerged
	outcome.MergeCommitSHA = mergeResult.SHA
	outcome.MergedAt = &mergedAt
	return outcome, nil
}

// nextAttemptNumber returns 1 for a brand new (run, repo) pair or one
// more than the previously recorded attempt count. Errors from Get are
// treated as "no prior outcome" so a transient store hiccup never
// causes a regression in the recorded counter.
func (o *Orchestrator) nextAttemptNumber(ctx context.Context, runID, repoID string) int {
	prior, err := o.store.GetRepositoryMergeOutcome(ctx, runID, repoID)
	if err != nil || prior == nil {
		return 1
	}
	return prior.Attempts + 1
}

// isNotFoundError reports whether err is the storage layer's
// "outcome not found" signal. Used to distinguish "this is the first
// attempt for (run, repo)" from a real store read failure that the
// caller must propagate rather than silently treat as absent.
func isNotFoundError(err error) bool {
	var spineErr *domain.SpineError
	if !errors.As(err, &spineErr) {
		return false
	}
	return spineErr.Code == domain.ErrNotFound
}

// classifyMergeFailure maps a git operation error onto the EPIC-005
// failure taxonomy. The translation is intentionally narrow: only the
// well-classified GitError shapes produce a specific failure_class;
// everything else falls back to MergeFailureUnknown so dashboards can
// see "this needs categorising" rather than be silently misclassified.
//
// The transient/permanent split comes from GitError.Kind; the
// fine-grained class comes from the Message string that
// classifyGitError already normalises. Keeping both readings here
// rather than re-deriving from raw stderr avoids drift between the
// git package's classification and the engine's mapping.
func classifyMergeFailure(err error) (domain.MergeFailureClass, string) {
	if err == nil {
		return domain.MergeFailureUnknown, ""
	}

	var gitErr *git.GitError
	if !errors.As(err, &gitErr) {
		return domain.MergeFailureUnknown, err.Error()
	}

	detail := gitErr.Error()

	// Transient first: lock contention and network flakes both map to
	// retryable classes so the scheduler retries automatically.
	if gitErr.IsRetryable() {
		// Lock contention is a "remote unavailable" feel (the resource
		// is busy); network/dns/connection-refused is the canonical
		// network class. Either way the outcome is non-terminal.
		if strings.Contains(gitErr.Message, "lock") {
			return domain.MergeFailureRemoteUnavailable, detail
		}
		return domain.MergeFailureNetwork, detail
	}

	// NotFound (e.g. a ref vanished between run-start and merge) maps
	// to precondition — caller must resync state before retrying.
	if gitErr.Kind == git.ErrKindNotFound {
		return domain.MergeFailurePrecondition, detail
	}

	// Permanent classes: match the substring the git package's
	// classifyGitError normalises into Message.
	switch {
	case strings.Contains(gitErr.Message, "conflict"):
		return domain.MergeFailureConflict, detail
	case strings.Contains(gitErr.Message, "authentication"):
		return domain.MergeFailureAuth, detail
	case strings.Contains(gitErr.Message, "non-fast-forward"),
		strings.Contains(gitErr.Message, "rejected"):
		// A remote rejection on push is most often a branch protection
		// rule or a stale ref. Either way the operator must resolve it
		// — both are permanent for the auto-retry path.
		return domain.MergeFailureBranchProtection, detail
	case strings.Contains(gitErr.Message, "not a git repository"):
		return domain.MergeFailurePrecondition, detail
	}

	return domain.MergeFailureUnknown, detail
}

// recordPrimaryMergeOutcome upserts the (run, primary) outcome row
// after the primary repo merge attempt. Called from MergeRunBranch on
// both the success and failure paths so the row reflects the actual
// outcome — TASK-002 AC: "Primary repo merge records success or
// partial failure."
//
// Parameters:
//   - mergeSHA: the local merge commit SHA when the merge call
//     produced one. May be non-empty on the failed path when the
//     local merge succeeded but a subsequent push failed; the SHA is
//     captured in failure_detail so the recovery path knows what was
//     applied locally even though origin did not advance.
//   - phaseErr: the first non-nil error along the merge → push
//     chain. nil means the entire chain landed.
//   - phase: a short tag identifying which phase produced phaseErr
//     ("" on success, "push" when the merge succeeded but push
//     failed). Used to format failure_detail so dashboards do not
//     have to parse the message string.
//
// Returns the persistence error verbatim so callers can refuse to
// advance the run state without a recorded primary outcome —
// symmetric with the code-repo path, which treats a lost outcome row
// as fatal. The caller is responsible for ordering this call before
// it commits to the next state transition.
func (o *Orchestrator) recordPrimaryMergeOutcome(ctx context.Context, run *domain.Run, mergeSHA string, phaseErr error, phase string) error {
	log := observe.Logger(ctx)
	now := time.Now()
	attempts := o.nextAttemptNumber(ctx, run.RunID, repository.PrimaryRepositoryID)
	outcome := domain.RepositoryMergeOutcome{
		RunID:           run.RunID,
		RepositoryID:    repository.PrimaryRepositoryID,
		SourceBranch:    run.BranchName,
		TargetBranch:    authoritativeBranch,
		Attempts:        attempts,
		LastAttemptedAt: &now,
	}
	if phaseErr != nil {
		class, detail := classifyMergeFailure(phaseErr)
		outcome.Status = domain.RepositoryMergeStatusFailed
		outcome.FailureClass = class
		if phase == "push" && mergeSHA != "" {
			outcome.FailureDetail = fmt.Sprintf("local merge succeeded (sha=%s) but push failed: %s",
				mergeSHA, detail)
		} else {
			outcome.FailureDetail = detail
		}
	} else {
		mergedAt := now
		outcome.Status = domain.RepositoryMergeStatusMerged
		outcome.MergeCommitSHA = mergeSHA
		outcome.MergedAt = &mergedAt
	}
	if err := o.store.UpsertRepositoryMergeOutcome(ctx, &outcome); err != nil {
		return fmt.Errorf("record primary merge outcome: %w", err)
	}
	log.Info("primary merge outcome recorded", outcome.LogFields()...)
	return nil
}
