package engine

import (
	"context"
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/repository"
)

// merge_recovery_test.go anchors the EPIC-005 TASK-005 acceptance
// criteria end-to-end across the engine merge + recovery surface:
//
//   - Tests show successful repos are not re-merged on retry.
//   - Partial merge state is observable through the audit table.
//   - Failed repo branches are preserved through the recovery loop.
//   - Completed runs have terminal-success outcomes for every affected
//     repo (merged or resolved-externally).
//
// The unit tests in multi_repo_merge_test.go and merge_resolution_test.go
// cover the individual mechanics; this file pins the AC-anchored
// scenarios that combine them — the cross-component proofs the
// initiative needs before EPIC-005 closes.

// TestMergeRecovery_AllReposMergeSuccessfully_AllOutcomesMerged anchors
// AC #4: a completed multi-repo run has a `merged` outcome for every
// affected repository (primary + every code repo). The existing ordering
// test pins call sequence; this test pins the audit-row completeness
// promise the AC reads as.
func TestMergeRecovery_AllReposMergeSuccessfully_AllOutcomesMerged(t *testing.T) {
	const runID = "run-all-merged-1"
	affected := []string{repository.PrimaryRepositoryID, "payments-service", "api-gateway", "auth-service"}

	setup := setupMergeOrderTest(t, runID, "spine/run/run-all-merged-1", affected)

	if err := setup.orch.MergeRunBranch(context.Background(), runID); err != nil {
		t.Fatalf("MergeRunBranch: %v", err)
	}

	if got := setup.store.runs[runID].Status; got != domain.RunStatusCompleted {
		t.Fatalf("run status: got %s, want completed", got)
	}

	// Every affected repo must have a recorded outcome with status=merged.
	// Without this AC pin a future change could let the loop short-circuit
	// after the primary outcome and silently drop a row (e.g. via a
	// mistakenly-extended terminal-skip filter), which would corrupt the
	// dashboards that read from merge_outcomes.
	want := map[string]bool{}
	for _, id := range affected {
		want[id] = false
	}
	for _, o := range setup.store.mergeOutcomes {
		if _, expected := want[o.RepositoryID]; !expected {
			t.Errorf("unexpected outcome row for repo %q", o.RepositoryID)
			continue
		}
		if o.Status != domain.RepositoryMergeStatusMerged {
			t.Errorf("repo %s: got status %s, want merged", o.RepositoryID, o.Status)
		}
		if o.MergeCommitSHA == "" {
			t.Errorf("repo %s: merge_commit_sha must be set on merged outcome", o.RepositoryID)
		}
		want[o.RepositoryID] = true
	}
	for id, seen := range want {
		if !seen {
			t.Errorf("affected repo %q has no outcome row", id)
		}
	}
}

// TestMergeRecovery_PrimaryFailsAfterCodeReposMerged_PreservesCodeOutcomes
// anchors the explicit scenario "Primary repo ledger merge fails after
// code repos merge". The code-repo merge commits have already landed in
// their respective remotes; a primary-side failure must NOT roll back
// or otherwise mutate those success rows. Operators recovering from the
// failure rely on the recorded merge_commit_sha values to know what
// already shipped.
func TestMergeRecovery_PrimaryFailsAfterCodeReposMerged_PreservesCodeOutcomes(t *testing.T) {
	const runID = "run-primary-late-fail-1"
	affected := []string{repository.PrimaryRepositoryID, "payments-service", "api-gateway"}

	setup := setupMergeOrderTest(t, runID, "spine/run/run-primary-late-fail-1", affected)
	setup.repos["payments-service"].mergeSHA = "code-sha-pmt"
	setup.repos["api-gateway"].mergeSHA = "code-sha-api"
	// Primary push fails permanently AFTER the local primary merge has
	// landed. The other half of the failure space (primary merge itself
	// rejected by branch protection) is covered by
	// TestMergeRunBranch_PrimaryPushFailureRecordsFailedOutcome — this
	// test specifically covers the multi-repo case the AC scenario list
	// names.
	setup.primary.pushErr = &git.GitError{
		Kind: git.ErrKindPermanent, Op: "push", Message: "authentication failed",
	}

	if err := setup.orch.MergeRunBranch(context.Background(), runID); err != nil {
		t.Fatalf("MergeRunBranch: %v", err)
	}

	// Code repos ran first (TASK-002 ordering) and both succeeded — their
	// outcome rows must still report merged with the original commit
	// SHAs. A regression where the primary failure swept code rows back
	// to pending or failed would erase the audit trail of what shipped.
	pmt := setup.outcomeForRepo(t, runID, "payments-service")
	if pmt.Status != domain.RepositoryMergeStatusMerged {
		t.Errorf("payments-service: got status %s, want merged", pmt.Status)
	}
	if pmt.MergeCommitSHA != "code-sha-pmt" {
		t.Errorf("payments-service: got SHA %q, want code-sha-pmt (must not be cleared on primary fail)",
			pmt.MergeCommitSHA)
	}
	api := setup.outcomeForRepo(t, runID, "api-gateway")
	if api.Status != domain.RepositoryMergeStatusMerged {
		t.Errorf("api-gateway: got status %s, want merged", api.Status)
	}
	if api.MergeCommitSHA != "code-sha-api" {
		t.Errorf("api-gateway: got SHA %q, want code-sha-api", api.MergeCommitSHA)
	}

	// Primary outcome flips to failed-auth, run flips to failed (auth is
	// permanent). The failure-detail mentions the local merge SHA so
	// recovery can confirm what landed locally even when the push lost.
	primary := setup.outcomeForRepo(t, runID, repository.PrimaryRepositoryID)
	if primary.Status != domain.RepositoryMergeStatusFailed {
		t.Errorf("primary: got status %s, want failed", primary.Status)
	}
	if primary.FailureClass != domain.MergeFailureAuth {
		t.Errorf("primary: got class %s, want auth", primary.FailureClass)
	}
	if got := setup.store.runs[runID].Status; got != domain.RunStatusFailed {
		t.Errorf("run status: got %s, want failed", got)
	}

	// Failed primary keeps its branch — operators need that ref to
	// inspect what landed locally before the push lost. (TASK-004 AC,
	// re-anchored here for the EPIC-005 scenario list.)
	if len(setup.primary.deleted) != 0 {
		t.Errorf("primary: branch deleted on failed push, got %v", setup.primary.deleted)
	}
}

// TestMergeRecovery_PartialMergeRetriedAfterResolveExternally walks the
// full lifecycle the AC names: a multi-repo run with a permanent
// code-repo failure parks in partially-merged → an operator marks the
// failure resolved-externally → the scheduler resume gate flips
// eligible → MergeRunBranch re-walks and the run completes with no
// re-merge of the previously-successful code repo.
func TestMergeRecovery_PartialMergeRetriedAfterResolveExternally(t *testing.T) {
	const runID = "run-resolve-recover-1"
	affected := []string{repository.PrimaryRepositoryID, "payments-service", "api-gateway"}

	setup := setupMergeOrderTest(t, runID, "spine/run/run-resolve-recover-1", affected)
	setup.repos["api-gateway"].mergeErr = &git.GitError{
		Kind: git.ErrKindPermanent, Op: "merge", Message: "merge conflict",
	}

	// 1st pass: payments-service merges, api-gateway fails permanently,
	// primary still merges (TASK-002 ordering keeps the ledger update
	// independent of code-repo state). Run parks in partially-merged.
	if err := setup.orch.MergeRunBranch(context.Background(), runID); err != nil {
		t.Fatalf("MergeRunBranch (1st): %v", err)
	}
	if got := setup.store.runs[runID].Status; got != domain.RunStatusPartiallyMerged {
		t.Fatalf("after 1st pass: got %s, want partially-merged", got)
	}
	pmtMergeCallsBefore := len(setup.repos["payments-service"].mergeCalls)
	if pmtMergeCallsBefore != 1 {
		t.Fatalf("payments-service: setup expected 1 Merge call, got %d", pmtMergeCallsBefore)
	}
	// Snapshot api-gateway's merge call count after the first pass — it
	// includes the failed merge plus the post-failure abort
	// (trackingCodeRepoClient records every Merge invocation, including
	// strategy="abort"). The point of this test is that the second pass
	// adds zero further calls.
	apiMergeCallsBefore := len(setup.repos["api-gateway"].mergeCalls)

	// Operator resolves api-gateway externally. The actor MUST be on the
	// context — the orchestrator refuses anonymous resolution.
	actor := &domain.Actor{ActorID: "actor-op-recover", Role: domain.RoleOperator}
	resolveCtx := domain.WithActor(context.Background(), actor)

	result, err := setup.orch.ResolveRepositoryMergeExternally(
		resolveCtx, runID, "api-gateway",
		"reconciled with upstream PR #123", "deadbeefcafe")
	if err != nil {
		t.Fatalf("ResolveRepositoryMergeExternally: %v", err)
	}
	if !result.ReadyToResume {
		t.Errorf("ResolveRecoveryResult.ReadyToResume: got false, want true (only one failed repo, now resolved)")
	}
	if len(result.BlockingRepositories) != 0 {
		t.Errorf("BlockingRepositories: got %v, want empty", result.BlockingRepositories)
	}

	// The resolved row reflects the operator action.
	api := setup.outcomeForRepo(t, runID, "api-gateway")
	if api.Status != domain.RepositoryMergeStatusResolvedExternally {
		t.Errorf("api-gateway: got status %s, want resolved-externally", api.Status)
	}
	if api.ResolvedBy != actor.ActorID {
		t.Errorf("api-gateway: got resolved_by %q, want %q", api.ResolvedBy, actor.ActorID)
	}

	// Scheduler-equivalent: codeRepoOutcomesAllowResume would now flip
	// eligible (no failed code-repo rows remain). Drive the resume by
	// transitioning back to committing, mirroring what the scheduler's
	// retryRunsByStatus path does on its next tick.
	applied, err := setup.store.TransitionRunStatus(context.Background(), runID,
		domain.RunStatusPartiallyMerged, domain.RunStatusCommitting)
	if err != nil || !applied {
		t.Fatalf("resume transition: applied=%v err=%v", applied, err)
	}

	// 2nd MergeRunBranch pass: terminal-skip guard skips both code repos
	// (payments-service merged, api-gateway resolved-externally), the
	// primary's outcome upserts again (attempts++), and the run flips to
	// completed.
	if err := setup.orch.MergeRunBranch(context.Background(), runID); err != nil {
		t.Fatalf("MergeRunBranch (2nd): %v", err)
	}
	if got := setup.store.runs[runID].Status; got != domain.RunStatusCompleted {
		t.Errorf("after resume: got status %s, want completed", got)
	}

	// AC #1: payments-service was NOT re-merged. A second Merge call here
	// would risk overwriting its merged outcome with a fresh failed row
	// on any transient flake.
	pmtMergeCallsAfter := len(setup.repos["payments-service"].mergeCalls)
	if pmtMergeCallsAfter != pmtMergeCallsBefore {
		t.Errorf("payments-service: got %d merge calls after resume, want %d (terminal-skip violated)",
			pmtMergeCallsAfter, pmtMergeCallsBefore)
	}
	// And the row itself stays merged with attempts unchanged.
	pmt := setup.outcomeForRepo(t, runID, "payments-service")
	if pmt.Status != domain.RepositoryMergeStatusMerged {
		t.Errorf("payments-service: got status %s, want merged", pmt.Status)
	}
	if pmt.Attempts != 1 {
		t.Errorf("payments-service: got attempts=%d, want 1 (no re-attempt)", pmt.Attempts)
	}

	// api-gateway must NOT have been re-merged either — resolved-externally
	// is terminal-skip. Any new merge call would overwrite the audit pair
	// (ResolvedBy / ResolutionReason) the operator just wrote.
	if calls := len(setup.repos["api-gateway"].mergeCalls); calls != apiMergeCallsBefore {
		t.Errorf("api-gateway: got %d merge calls after resume, want %d (terminal-skip violated for resolved-externally)",
			calls, apiMergeCallsBefore)
	}
	// AC #4: every affected repo has a terminal-success outcome at run
	// completion (merged for the two that Spine merged, resolved-externally
	// for the one the operator handled).
	for _, repoID := range affected {
		o := setup.outcomeForRepo(t, runID, repoID)
		if o.Status != domain.RepositoryMergeStatusMerged &&
			o.Status != domain.RepositoryMergeStatusResolvedExternally {
			t.Errorf("repo %s: terminal-success expected at completion, got status %s",
				repoID, o.Status)
		}
	}
}

// TestMergeRecovery_PartialMergeRetriedAfterRetryAPI walks the
// transient-failure recovery path: a code repo failure that the
// operator chooses to retry rather than resolve out-of-band. The
// failed-permanent row is rewritten by RetryRepositoryMerge to
// pending — non-terminal — so the next merge pass re-attempts and
// (with the underlying issue cleared) succeeds. Pins the explicit
// scenario plus AC #1 for the merged repo.
func TestMergeRecovery_PartialMergeRetriedAfterRetryAPI(t *testing.T) {
	const runID = "run-retry-recover-1"
	affected := []string{repository.PrimaryRepositoryID, "payments-service", "api-gateway"}

	setup := setupMergeOrderTest(t, runID, "spine/run/run-retry-recover-1", affected)
	// api-gateway hits a permanent merge conflict — terminal at the
	// outcome layer, so the run parks rather than the scheduler's
	// transient retry kicking in.
	setup.repos["api-gateway"].mergeErr = &git.GitError{
		Kind: git.ErrKindPermanent, Op: "merge", Message: "merge conflict",
	}

	if err := setup.orch.MergeRunBranch(context.Background(), runID); err != nil {
		t.Fatalf("MergeRunBranch (1st): %v", err)
	}
	if got := setup.store.runs[runID].Status; got != domain.RunStatusPartiallyMerged {
		t.Fatalf("after 1st pass: got %s, want partially-merged", got)
	}

	// Operator decides the underlying conflict has been fixed (merged in
	// upstream, resolution committed to main, etc.) and asks to retry
	// rather than mark the row resolved-externally.
	actor := &domain.Actor{ActorID: "actor-op-retry", Role: domain.RoleOperator}
	retryCtx := domain.WithActor(context.Background(), actor)

	result, err := setup.orch.RetryRepositoryMerge(retryCtx, runID, "api-gateway",
		"upstream conflict resolved on main")
	if err != nil {
		t.Fatalf("RetryRepositoryMerge: %v", err)
	}
	if !result.ReadyToResume {
		t.Errorf("ReadyToResume: got false, want true (only one failed repo, now pending)")
	}

	// The Retry path flips the row to pending — distinct from the
	// resolve-externally path's terminal-success state. The next merge
	// pass MUST re-attempt because pending is not terminal.
	api := setup.outcomeForRepo(t, runID, "api-gateway")
	if api.Status != domain.RepositoryMergeStatusPending {
		t.Errorf("api-gateway after Retry: got status %s, want pending", api.Status)
	}
	if api.FailureClass != "" {
		t.Errorf("api-gateway after Retry: got failure_class %s, want cleared", api.FailureClass)
	}

	// Clear the underlying merge error (operator fixed the cause) and
	// drive the scheduler-equivalent resume.
	setup.repos["api-gateway"].mergeErr = nil
	pmtMergeCallsBefore := len(setup.repos["payments-service"].mergeCalls)

	applied, err := setup.store.TransitionRunStatus(context.Background(), runID,
		domain.RunStatusPartiallyMerged, domain.RunStatusCommitting)
	if err != nil || !applied {
		t.Fatalf("resume transition: applied=%v err=%v", applied, err)
	}

	// 2nd pass: payments-service stays terminal-skipped (AC #1), api-gateway
	// re-attempts and succeeds, primary upserts, run completes.
	if err := setup.orch.MergeRunBranch(context.Background(), runID); err != nil {
		t.Fatalf("MergeRunBranch (2nd): %v", err)
	}
	if got := setup.store.runs[runID].Status; got != domain.RunStatusCompleted {
		t.Errorf("after resume: got status %s, want completed", got)
	}

	// AC #1: previously-merged repo not re-attempted.
	if got := len(setup.repos["payments-service"].mergeCalls); got != pmtMergeCallsBefore {
		t.Errorf("payments-service: got %d merge calls after resume, want %d (no re-merge)",
			got, pmtMergeCallsBefore)
	}

	// api-gateway re-attempted exactly once on the resume pass — the
	// initial failure already triggered an abort, so the call count is
	// (initial merge + initial abort + resume merge) = 3.
	if got := len(setup.repos["api-gateway"].mergeCalls); got != 3 {
		t.Errorf("api-gateway: got %d merge calls, want 3 (initial + abort + resume)",
			got)
	}
	api2 := setup.outcomeForRepo(t, runID, "api-gateway")
	if api2.Status != domain.RepositoryMergeStatusMerged {
		t.Errorf("api-gateway after resume: got status %s, want merged", api2.Status)
	}
	if api2.Attempts != 2 {
		t.Errorf("api-gateway after resume: attempts=%d, want 2 (initial + resume)",
			api2.Attempts)
	}
}

// TestMergeRecovery_BlockingRepositoriesSurfacedWhenMultipleFailed pins
// the operator-facing recovery contract: when more than one code repo
// has failed, retrying or resolving one of them must report the others
// as still-blocking so the operator does not assume the run will
// resume. Without this, a recovery batch could mis-trigger a run resume
// (or worse, an operator could assume the run was unblocked) while
// further repos still required action.
func TestMergeRecovery_BlockingRepositoriesSurfacedWhenMultipleFailed(t *testing.T) {
	const runID = "run-blocking-1"
	affected := []string{repository.PrimaryRepositoryID, "payments-service", "api-gateway"}

	setup := setupMergeOrderTest(t, runID, "spine/run/run-blocking-1", affected)
	// Both code repos fail permanently in the same pass.
	setup.repos["payments-service"].mergeErr = &git.GitError{
		Kind: git.ErrKindPermanent, Op: "merge", Message: "merge conflict",
	}
	setup.repos["api-gateway"].mergeErr = &git.GitError{
		Kind: git.ErrKindPermanent, Op: "merge", Message: "merge conflict",
	}

	if err := setup.orch.MergeRunBranch(context.Background(), runID); err != nil {
		t.Fatalf("MergeRunBranch: %v", err)
	}
	if got := setup.store.runs[runID].Status; got != domain.RunStatusPartiallyMerged {
		t.Fatalf("after 1st pass: got %s, want partially-merged", got)
	}

	actor := &domain.Actor{ActorID: "actor-op-multi", Role: domain.RoleOperator}
	ctx := domain.WithActor(context.Background(), actor)

	// Resolve only payments-service. api-gateway is still failed.
	result, err := setup.orch.ResolveRepositoryMergeExternally(
		ctx, runID, "payments-service", "reconciled in payments PR #42", "")
	if err != nil {
		t.Fatalf("ResolveRepositoryMergeExternally: %v", err)
	}

	if result.ReadyToResume {
		t.Errorf("ReadyToResume: got true, want false (api-gateway still blocking)")
	}
	if len(result.BlockingRepositories) != 1 || result.BlockingRepositories[0] != "api-gateway" {
		t.Errorf("BlockingRepositories: got %v, want [api-gateway]", result.BlockingRepositories)
	}

	// Resolve api-gateway. Now no failures remain.
	result2, err := setup.orch.ResolveRepositoryMergeExternally(
		ctx, runID, "api-gateway", "reconciled in api-gateway PR #43", "")
	if err != nil {
		t.Fatalf("ResolveRepositoryMergeExternally (2nd): %v", err)
	}
	if !result2.ReadyToResume {
		t.Errorf("ReadyToResume: got false, want true (all failed repos resolved)")
	}
	if len(result2.BlockingRepositories) != 0 {
		t.Errorf("BlockingRepositories: got %v, want empty", result2.BlockingRepositories)
	}
}
