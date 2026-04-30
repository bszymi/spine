package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/repository"
)

// MergeRecoveryResult is returned by Resolve / Retry so callers can
// surface what happened to the operator without a second round trip.
//
// ReadyToResume reflects the run's resume eligibility AFTER the
// caller's action: true when no code-repo outcome remains in failed
// state. The scheduler still drives the actual resume on its next
// tick — this flag is informational, not a state-change trigger.
//
// BlockingRepositories lists every code repo (not the primary) whose
// outcome is still failed, so the operator can decide which one to
// resolve / retry next. Empty when ReadyToResume is true.
type MergeRecoveryResult struct {
	LedgerCommitSHA      string
	ReadyToResume        bool
	BlockingRepositories []string
}

// ResolveRepositoryMergeExternally records that an operator merged or
// rolled forward a failed per-repo merge outside Spine (EPIC-005
// TASK-006). The outcome row flips to RepositoryMergeStatusResolvedExternally
// with ResolvedBy + ResolutionReason populated; failure metadata is
// cleared so dashboards do not double-count the row as a fresh failure.
//
// targetCommitSHA, when non-empty, is the SHA the operator merged the
// fix to in the upstream code repo. It is captured in the primary-repo
// ledger commit (Target-Commit-SHA trailer) so audit queries can trace
// the run → recovery commit → upstream merge SHA chain end to end.
//
// The run state is intentionally NOT advanced here. A partially-merged
// run resumes through the existing scheduler tick once every code repo
// has reached a terminal non-failed state — the same mechanism TASK-003
// already wires for transient retry. Pulling the resume into this call
// would couple operator action to the merge engine's invocation
// context (gateway request handler) and lose the existing CAS-based
// flapping guard.
//
// Idempotency: calling Resolve on an already-resolved-externally
// outcome is a success no-op for the row but still writes a fresh
// ledger commit so the repeat operator action is queryable from
// primary-repo history. Calling it on a merged outcome is a conflict
// — a successful merge cannot be reclassified as out-of-band.
func (o *Orchestrator) ResolveRepositoryMergeExternally(ctx context.Context, runID, repositoryID, reason, targetCommitSHA string) (*MergeRecoveryResult, error) {
	actor := domain.ActorFromContext(ctx)
	if actor == nil {
		return nil, domain.NewError(domain.ErrUnauthorized,
			"resolve-externally requires an authenticated actor")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, domain.NewError(domain.ErrInvalidParams,
			"resolve-externally requires a non-empty reason")
	}
	if runID == "" || repositoryID == "" {
		return nil, domain.NewError(domain.ErrInvalidParams,
			"run_id and repository_id are required")
	}
	targetCommitSHA = strings.TrimSpace(targetCommitSHA)
	if targetCommitSHA != "" {
		if err := validateSingleLineAuditValue("target_commit_sha", targetCommitSHA); err != nil {
			return nil, err
		}
	}

	run, err := o.store.GetRun(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("get run: %w", err)
	}
	if err := guardPartialMergeRecovery(run, repositoryID); err != nil {
		return nil, err
	}
	existing, err := o.store.GetRepositoryMergeOutcome(ctx, runID, repositoryID)
	if err != nil {
		return nil, fmt.Errorf("get merge outcome: %w", err)
	}

	idempotent := false
	switch existing.Status {
	case domain.RepositoryMergeStatusResolvedExternally:
		// Already resolved by an earlier call — preserve the original
		// audit pair on the row but still write a fresh ledger commit
		// (and event) so the repeat operator action is queryable from
		// primary-repo history.
		idempotent = true
	case domain.RepositoryMergeStatusFailed:
		// Expected case — proceed with the transition.
	case domain.RepositoryMergeStatusMerged:
		return nil, domain.NewError(domain.ErrConflict,
			fmt.Sprintf("repository %s for run %s is already merged; cannot mark resolved-externally",
				repositoryID, runID))
	default:
		return nil, domain.NewError(domain.ErrConflict,
			fmt.Sprintf("repository %s for run %s has status %s; only failed outcomes can be resolved externally",
				repositoryID, runID, existing.Status))
	}

	// Write the primary-repo ledger commit BEFORE the runtime row
	// changes. If the ledger commit fails the failure is loud and the
	// outcome stays at `failed` so the audit invariant holds: the
	// runtime row is not allowed to advance to resolved-externally
	// without a corresponding ledger entry the operator can find via
	// `git log`. A retried call lands a second commit, which is
	// preferable to a silent audit gap.
	ledgerSHA, err := o.writeMergeRecoveryLedgerCommit(ctx, run, repositoryID, mergeRecoveryAction{
		Operation:       "merge_recovery_resolve",
		ActorID:         actor.ActorID,
		Reason:          reason,
		TargetCommitSHA: targetCommitSHA,
	})
	if err != nil {
		return nil, fmt.Errorf("ledger commit: %w", err)
	}

	if !idempotent {
		// Re-read after the ledger commit to detect a concurrent
		// resolve. Two operators racing on the same failed outcome
		// would each pass the initial GetRepositoryMergeOutcome
		// (status=failed) but only the first should own the row's
		// ResolvedBy / ResolutionReason audit pair. The ledger commits
		// from both calls are still preserved in primary-repo history,
		// so the audit trail stays complete; only the runtime row's
		// "first resolver" claim is at risk. A narrow race remains
		// between this re-read and the upsert below, but it is
		// orders of magnitude smaller than the original window across
		// the git Commit / PushBranch round trip.
		fresh, err := o.store.GetRepositoryMergeOutcome(ctx, runID, repositoryID)
		if err != nil {
			return nil, fmt.Errorf("re-read merge outcome: %w", err)
		}
		if fresh.Status != domain.RepositoryMergeStatusFailed {
			observe.Logger(ctx).Info("merge resolve race lost; preserving prior audit pair",
				"run_id", runID, "repository_id", repositoryID,
				"prior_status", string(fresh.Status),
				"prior_resolved_by", fresh.ResolvedBy)
			idempotent = true
		}
	}

	if !idempotent {
		updated := *existing
		updated.Status = domain.RepositoryMergeStatusResolvedExternally
		updated.ResolvedBy = actor.ActorID
		updated.ResolutionReason = reason
		// resolved-externally explicitly means "not merged via Spine" — Validate()
		// rejects MergeCommitSHA / MergedAt / failure fields on this status, so
		// clear them here. FailureClass/FailureDetail came from the prior failed
		// row; MergeCommitSHA/MergedAt are zero on a failed row but cleared
		// defensively in case a future writer populates them on a partial path.
		updated.FailureClass = ""
		updated.FailureDetail = ""
		updated.MergeCommitSHA = ""
		updated.MergedAt = nil
		// LastAttemptedAt is intentionally untouched: it records the last
		// merge attempt, not the last operator action. The audit trail for
		// the operator's resolution lives on the event log; UpdatedAt is
		// bumped by the upsert.

		if err := o.store.UpsertRepositoryMergeOutcome(ctx, &updated); err != nil {
			return nil, fmt.Errorf("upsert merge outcome: %w", err)
		}
	}

	o.emitMergeResolutionEvent(ctx, run, repositoryID, actor.ActorID, reason, ledgerSHA,
		domain.EventRunRepositoryMergeResolved, "resolved")
	observe.Logger(ctx).Info("merge outcome resolved externally",
		"run_id", runID, "repository_id", repositoryID,
		"resolved_by", actor.ActorID,
		"ledger_commit_sha", ledgerSHA)

	return o.computeRecoveryResult(ctx, runID, ledgerSHA), nil
}

// RetryRepositoryMerge clears the failure state on a per-repo outcome
// so the next scheduler tick re-attempts the merge for that repository
// (EPIC-005 TASK-006). The outcome row goes back to
// RepositoryMergeStatusPending with FailureClass / FailureDetail
// cleared; the audit pair (ResolvedBy / ResolutionReason — borrowed for
// "who asked and why") is also cleared because the pending status forbids
// them, while the request itself is recorded as
// EventRunRepositoryMergeRetryRequested for the audit trail.
//
// The Attempts counter is preserved so observability dashboards can still
// see the cumulative effort spent on this (run, repo). MergeRunBranch's
// per-repo loop does not skip pending outcomes, so the next tick picks
// it up; the run's resume gate (codeRepoOutcomesAllowResume) flips
// eligible as soon as no failed code-repo row remains.
//
// Idempotency: Retry on already-pending is a success no-op for the
// row but still writes a fresh ledger commit + event so the operator
// action is queryable. Retry on merged or resolved-externally is a
// conflict — those terminal-success states are not eligible for retry.
//
// The result's ReadyToResume is the operator-facing signal that the
// run will actually be retried by the next scheduler tick. When other
// code repos remain failed, the row flips to pending but the resume
// gate keeps the run parked; the caller surfaces the BlockingRepositories
// list so the operator can continue the recovery batch.
func (o *Orchestrator) RetryRepositoryMerge(ctx context.Context, runID, repositoryID, reason string) (*MergeRecoveryResult, error) {
	actor := domain.ActorFromContext(ctx)
	if actor == nil {
		return nil, domain.NewError(domain.ErrUnauthorized,
			"retry-merge requires an authenticated actor")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, domain.NewError(domain.ErrInvalidParams,
			"retry-merge requires a non-empty reason")
	}
	if runID == "" || repositoryID == "" {
		return nil, domain.NewError(domain.ErrInvalidParams,
			"run_id and repository_id are required")
	}

	run, err := o.store.GetRun(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("get run: %w", err)
	}
	if err := guardPartialMergeRecovery(run, repositoryID); err != nil {
		return nil, err
	}
	existing, err := o.store.GetRepositoryMergeOutcome(ctx, runID, repositoryID)
	if err != nil {
		return nil, fmt.Errorf("get merge outcome: %w", err)
	}

	idempotent := false
	switch existing.Status {
	case domain.RepositoryMergeStatusFailed:
		// Expected: clear the failure and let the scheduler retry.
	case domain.RepositoryMergeStatusPending:
		// Already eligible for retry — success no-op for the row but
		// still write a ledger commit + event so the operator's action
		// is queryable.
		idempotent = true
	case domain.RepositoryMergeStatusMerged:
		return nil, domain.NewError(domain.ErrConflict,
			fmt.Sprintf("repository %s for run %s is already merged; cannot retry",
				repositoryID, runID))
	case domain.RepositoryMergeStatusResolvedExternally:
		return nil, domain.NewError(domain.ErrConflict,
			fmt.Sprintf("repository %s for run %s is resolved-externally; cannot retry",
				repositoryID, runID))
	default:
		return nil, domain.NewError(domain.ErrConflict,
			fmt.Sprintf("repository %s for run %s has status %s; only failed outcomes can be retried",
				repositoryID, runID, existing.Status))
	}

	ledgerSHA, err := o.writeMergeRecoveryLedgerCommit(ctx, run, repositoryID, mergeRecoveryAction{
		Operation: "merge_recovery_retry",
		ActorID:   actor.ActorID,
		Reason:    reason,
	})
	if err != nil {
		return nil, fmt.Errorf("ledger commit: %w", err)
	}

	if !idempotent {
		// Re-read to detect a concurrent retry / resolve that won the
		// race between our initial read and now. If another caller
		// already flipped the row out of `failed`, a blanket upsert
		// here would silently undo their work — for example,
		// overwriting a freshly resolved-externally row back to
		// pending. The ledger commit we just wrote is still a valid
		// audit entry; we just must not regress the runtime state.
		fresh, err := o.store.GetRepositoryMergeOutcome(ctx, runID, repositoryID)
		if err != nil {
			return nil, fmt.Errorf("re-read merge outcome: %w", err)
		}
		if fresh.Status != domain.RepositoryMergeStatusFailed {
			observe.Logger(ctx).Info("merge retry race lost; preserving prior status",
				"run_id", runID, "repository_id", repositoryID,
				"prior_status", string(fresh.Status))
			idempotent = true
		}
	}

	if !idempotent {
		updated := *existing
		updated.Status = domain.RepositoryMergeStatusPending
		updated.FailureClass = ""
		updated.FailureDetail = ""
		updated.MergeCommitSHA = ""
		updated.MergedAt = nil
		// ResolvedBy/ResolutionReason are paired with the resolved-externally
		// status by Validate(); pending must not carry them. The operator's
		// audit trail lives in the event payload below.
		updated.ResolvedBy = ""
		updated.ResolutionReason = ""
		// LastAttemptedAt is intentionally untouched: it records the last
		// merge attempt, not the operator's retry request. The next merge
		// attempt on the scheduler tick will overwrite it; UpdatedAt is
		// bumped by the upsert so the row's recency is still queryable.

		if err := o.store.UpsertRepositoryMergeOutcome(ctx, &updated); err != nil {
			return nil, fmt.Errorf("upsert merge outcome: %w", err)
		}
	}

	o.emitMergeResolutionEvent(ctx, run, repositoryID, actor.ActorID, reason, ledgerSHA,
		domain.EventRunRepositoryMergeRetryRequested, "retry-requested")
	observe.Logger(ctx).Info("merge outcome retry requested",
		"run_id", runID, "repository_id", repositoryID,
		"requested_by", actor.ActorID,
		"ledger_commit_sha", ledgerSHA)

	return o.computeRecoveryResult(ctx, runID, ledgerSHA), nil
}

// computeRecoveryResult inspects the run's outcomes after a recovery
// action and reports whether the scheduler can resume the run on its
// next tick. The scheduler's existing resume gate (codeRepoOutcomesAllowResume)
// requires every code-repo outcome to be non-failed; this helper
// mirrors that rule so the operator-facing response can show "ready to
// resume" or list which repos still block the resume.
//
// A store error during the post-action lookup is logged but not
// propagated: the action itself succeeded, so a cosmetic "couldn't
// list outcomes" failure should not 500 the API. ReadyToResume defaults
// to false in that case so operators see the conservative answer.
func (o *Orchestrator) computeRecoveryResult(ctx context.Context, runID, ledgerSHA string) *MergeRecoveryResult {
	res := &MergeRecoveryResult{LedgerCommitSHA: ledgerSHA}
	outcomes, err := o.store.ListRepositoryMergeOutcomes(ctx, runID)
	if err != nil {
		observe.Logger(ctx).Warn("merge recovery: failed to list outcomes for resume check",
			"run_id", runID, "error", err)
		return res
	}
	for i := range outcomes {
		if outcomes[i].RepositoryID == repository.PrimaryRepositoryID {
			continue
		}
		if outcomes[i].Status == domain.RepositoryMergeStatusFailed {
			res.BlockingRepositories = append(res.BlockingRepositories, outcomes[i].RepositoryID)
		}
	}
	res.ReadyToResume = len(res.BlockingRepositories) == 0
	return res
}

// guardPartialMergeRecovery enforces the scope of the operator
// recovery surface (EPIC-005 TASK-006): the actions only make sense
// for a partially-merged run on a non-primary code repository that
// the run actually targets.
//
//   - Other run statuses (failed, completed, committing, …) cannot be
//     resumed by editing a merge outcome row, so accepting the call
//     would silently rewrite the audit table without unblocking
//     anything — codex review caught this on pass 1.
//   - The primary repository's outcome is set on the merge path that
//     advances the run; rewriting it out of band would lose the only
//     ref carrying the merged commits if the operator confused a
//     "permanent push failure flipped run → failed" with the partial
//     merge recovery flow. The primary's recovery model is "retry the
//     run", not "edit the outcome".
//   - The repository must be one of the run's affected repos so a
//     stale or wrong repository_id from the operator cannot create a
//     new outcome row for an unrelated repo on Upsert.
func guardPartialMergeRecovery(run *domain.Run, repositoryID string) error {
	if run.Status != domain.RunStatusPartiallyMerged {
		return domain.NewError(domain.ErrConflict,
			fmt.Sprintf("run %s is in status %s; merge recovery only applies to partially-merged runs",
				run.RunID, run.Status))
	}
	if repositoryID == repository.PrimaryRepositoryID {
		return domain.NewError(domain.ErrInvalidParams,
			"merge recovery is not available for the primary repository; recover by retrying the run")
	}
	for _, affected := range run.AffectedRepositories {
		if affected == repositoryID {
			return nil
		}
	}
	return domain.NewError(domain.ErrInvalidParams,
		fmt.Sprintf("repository %s is not among run %s affected repositories", repositoryID, run.RunID))
}

// emitMergeResolutionEvent emits a unique-per-call operational event
// for resolve / retry actions. The action tag (e.g. "resolved",
// "retry-requested") plus a unix-nano suffix keeps event IDs distinct
// across rapid repeat calls so the event_log dedupe never collapses
// two distinct operator actions into one row. ledgerSHA is included
// when non-empty so SSE/log-pull consumers can dereference the audit
// commit without an extra git query.
func (o *Orchestrator) emitMergeResolutionEvent(
	ctx context.Context,
	run *domain.Run,
	repositoryID, actorID, reason, ledgerSHA string,
	eventType domain.EventType,
	action string,
) {
	payloadMap := map[string]string{
		"repository_id": repositoryID,
		"reason":        reason,
	}
	switch eventType {
	case domain.EventRunRepositoryMergeResolved:
		payloadMap["resolved_by"] = actorID
	case domain.EventRunRepositoryMergeRetryRequested:
		payloadMap["requested_by"] = actorID
	}
	if ledgerSHA != "" {
		payloadMap["ledger_commit_sha"] = ledgerSHA
	}
	payload, _ := json.Marshal(payloadMap)
	tracePrefix := run.TraceID
	if len(tracePrefix) > 12 {
		tracePrefix = tracePrefix[:12]
	}
	eventID := fmt.Sprintf("evt-%s-%s-%d", tracePrefix, action, time.Now().UnixNano())
	o.emitEvent(ctx, eventType, run.RunID, run.TraceID, eventID, payload)
}

// indentBody prefixes every line with four spaces so the reason is
// visually distinct from any structured trailers below it. Reason
// content with a trailing trailer-like line still cannot be
// misinterpreted as a real trailer because git interpret-trailers
// only parses the LAST paragraph and our trailer block follows a blank
// line — but the indent makes the human-readable separation obvious.
func indentBody(s string) string {
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = "    " + line
	}
	return strings.Join(lines, "\n")
}

// validateSingleLineAuditValue rejects values that contain a newline
// or carriage return. Used to guard fields that go into git trailers
// (Target-Commit-SHA today) — a multi-line trailer value would let an
// operator forge additional trailer lines (e.g. an injected Resolved-By:)
// and corrupt the audit invariant. Operators get a clear 400 instead
// of a silently-corrupt commit.
func validateSingleLineAuditValue(field, v string) error {
	if strings.ContainsAny(v, "\r\n") {
		return domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("%s must be a single line (no carriage returns or newlines)", field))
	}
	return nil
}

// mergeRecoveryAction is the structured input for writing one
// primary-repo ledger commit. Captured here so the helper signature
// stays narrow as more fields accrete.
type mergeRecoveryAction struct {
	Operation       string // "merge_recovery_resolve" | "merge_recovery_retry"
	ActorID         string
	Reason          string
	TargetCommitSHA string // optional — only the resolve path carries one
}

// writeMergeRecoveryLedgerCommit lands an empty commit on the run
// branch of the primary repo so operator merge recovery actions are
// queryable from `git log` (TASK-006 AC: "audit entries are queryable
// from primary-repo history"). The commit lands in `main` once the
// run's primary merge succeeds, since the merge into main carries the
// run branch's full history with it.
//
// The commit goes on `run.BranchName` rather than `main` deliberately:
//
//   - Empty commits on `main` would advance HEAD without affecting the
//     run branch. If the run produced no commits on its branch (rare
//     but legal — e.g. a no-op planning run), the next `git merge
//     --no-ff <run-branch>` from `main` is "Already up to date" and
//     `mergeResult.SHA` returns the ledger commit's SHA instead of a
//     fresh merge commit. recordPrimaryMergeOutcome would then store
//     the ledger SHA as the primary `merge_commit_sha` — codex caught
//     this on pass 3.
//   - Putting the ledger commit on the run branch keeps `main`
//     untouched until the actual merge happens, so the merge always
//     produces a real merge commit whose SHA is the primary outcome.
//   - For runs that never complete (operator gives up), the run
//     branch is preserved by CleanupRunBranch's failure-mode rules,
//     and the ledger commits remain queryable on the preserved branch.
//
// Trailers carry the machine-readable fields (Run-ID, Repository-ID,
// Operation, etc.) so audit queries can grep
// `git log main --grep="Operation: merge_recovery_"` post-completion.
// The working tree is restored to `main` on the way out so concurrent
// engine paths (e.g. the scheduler tick that resumes this run a moment
// later) do not find the working tree on the run branch.
func (o *Orchestrator) writeMergeRecoveryLedgerCommit(
	ctx context.Context,
	run *domain.Run,
	repositoryID string,
	action mergeRecoveryAction,
) (string, error) {
	if run.BranchName == "" {
		// Defensive: every partially-merged run has a branch
		// (createRunBranches set it at run-start), so this should be
		// unreachable. If we ever hit it, surface as a precondition
		// error rather than skip the audit silently.
		return "", domain.NewError(domain.ErrPrecondition,
			fmt.Sprintf("run %s has no branch; cannot write recovery ledger", run.RunID))
	}
	// The checkout restore at the end runs on a separate, bounded
	// context so it survives the request being cancelled mid-flight.
	// Without this, an HTTP client disconnect after the run-branch
	// checkout would leave the shared primary clone on the run
	// branch, confusing every subsequent scheduler tick and operator
	// action that assumes the working tree is on `main`.
	if err := o.git.Checkout(ctx, run.BranchName); err != nil {
		return "", fmt.Errorf("checkout %s: %w", run.BranchName, err)
	}
	defer func() {
		restoreCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := o.git.Checkout(restoreCtx, authoritativeBranch); err != nil {
			observe.Logger(ctx).Warn("failed to restore main checkout after ledger commit",
				"run_id", run.RunID, "error", err)
		}
	}()

	// Trailers are single-line key/value pairs by spec (RFC 822-ish);
	// the free-form operator reason goes in the commit body so a
	// newline-bearing reason cannot forge a Resolved-By trailer.
	// Operator-supplied single-line fields (Target-Commit-SHA) are
	// validated before we get here — see ResolveRepositoryMergeExternally.
	trailers := map[string]string{
		"Operation":     action.Operation,
		"Run-ID":        run.RunID,
		"Trace-ID":      run.TraceID,
		"Repository-ID": repositoryID,
	}
	switch action.Operation {
	case "merge_recovery_resolve":
		trailers["Resolved-By"] = action.ActorID
	case "merge_recovery_retry":
		trailers["Requested-By"] = action.ActorID
	}
	if action.TargetCommitSHA != "" {
		trailers["Target-Commit-SHA"] = action.TargetCommitSHA
	}

	summary := fmt.Sprintf("Resolve external merge for run %s (%s)", run.RunID, repositoryID)
	if action.Operation == "merge_recovery_retry" {
		summary = fmt.Sprintf("Retry merge for run %s (%s)", run.RunID, repositoryID)
	}

	body := "Reason:\n" + indentBody(action.Reason)

	result, err := o.git.Commit(ctx, git.CommitOpts{
		Message:    summary,
		Body:       body,
		Trailers:   trailers,
		AllowEmpty: true,
	})
	if err != nil {
		return "", fmt.Errorf("commit on %s: %w", run.BranchName, err)
	}

	if autoPushEnabled() {
		if err := o.git.PushBranch(ctx, "origin", run.BranchName); err != nil {
			return "", fmt.Errorf("push %s: %w", run.BranchName, err)
		}
	}

	observe.Logger(ctx).Info("merge recovery ledger commit",
		"run_id", run.RunID,
		"repository_id", repositoryID,
		"operation", action.Operation,
		"ledger_commit_sha", result.SHA,
		"actor_id", action.ActorID,
	)
	return result.SHA, nil
}
