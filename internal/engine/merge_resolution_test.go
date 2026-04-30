package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/repository"
)

// recordingGitOperator captures Commit calls so tests can assert on
// ledger commit trailers and message. All other GitOperator methods
// delegate to the no-op stub.
type recordingGitOperator struct {
	stubGitOperator
	commits     []git.CommitOpts
	commitErr   error
	commitSHA   string
	pushedRefs  []string
	checkedOut  []string
}

func (r *recordingGitOperator) Checkout(_ context.Context, branch string) error {
	r.checkedOut = append(r.checkedOut, branch)
	return nil
}

func (r *recordingGitOperator) Commit(_ context.Context, opts git.CommitOpts) (git.CommitResult, error) {
	r.commits = append(r.commits, opts)
	if r.commitErr != nil {
		return git.CommitResult{}, r.commitErr
	}
	sha := r.commitSHA
	if sha == "" {
		sha = "ledger-sha-default"
	}
	return git.CommitResult{SHA: sha}, nil
}

func (r *recordingGitOperator) Push(_ context.Context, _, ref string) error {
	r.pushedRefs = append(r.pushedRefs, ref)
	return nil
}

// resolutionFixture sets up the minimum store state both Resolve and
// Retry tests need: one run, plus seeded outcome rows passed in by the
// caller. The actor is wired into the returned context so the engine
// can read it for the audit trail.
type resolutionFixture struct {
	orch    *Orchestrator
	store   *mockRunStore
	events  *mockEventEmitter
	ctx     context.Context
	run     *domain.Run
	actorID string
}

func newResolutionFixture(t *testing.T, outcomes ...domain.RepositoryMergeOutcome) *resolutionFixture {
	t.Helper()
	run := &domain.Run{
		RunID:                "run-1",
		TraceID:              "trace-abcdefghijkl",
		Status:               domain.RunStatusPartiallyMerged,
		BranchName:           "spine-run-1",
		AffectedRepositories: []string{repository.PrimaryRepositoryID, "payments-service"},
	}
	store := &mockRunStore{
		runs:          map[string]*domain.Run{run.RunID: run},
		mergeOutcomes: append([]domain.RepositoryMergeOutcome{}, outcomes...),
	}
	events := &mockEventEmitter{}
	orch := testOrchestrator(nil, nil, store, events)

	actor := &domain.Actor{
		ActorID: "actor-operator",
		Role:    domain.RoleOperator,
	}
	ctx := domain.WithActor(context.Background(), actor)

	return &resolutionFixture{
		orch:    orch,
		store:   store,
		events:  events,
		ctx:     ctx,
		run:     run,
		actorID: actor.ActorID,
	}
}

func failedOutcome(repoID string) domain.RepositoryMergeOutcome {
	now := time.Now().Add(-time.Minute)
	return domain.RepositoryMergeOutcome{
		RunID:           "run-1",
		RepositoryID:    repoID,
		Status:          domain.RepositoryMergeStatusFailed,
		SourceBranch:    "spine-run-1",
		TargetBranch:    "main",
		FailureClass:    domain.MergeFailureConflict,
		FailureDetail:   "merge conflict in app.go",
		Attempts:        2,
		LastAttemptedAt: &now,
	}
}

func mergedOutcome(repoID, sha string) domain.RepositoryMergeOutcome {
	mergedAt := time.Now().Add(-time.Minute)
	return domain.RepositoryMergeOutcome{
		RunID:          "run-1",
		RepositoryID:   repoID,
		Status:         domain.RepositoryMergeStatusMerged,
		SourceBranch:   "spine-run-1",
		TargetBranch:   "main",
		MergeCommitSHA: sha,
		MergedAt:       &mergedAt,
		Attempts:       1,
	}
}

// ── ResolveRepositoryMergeExternally ──

func TestResolveRepositoryMergeExternally_FailedToResolved(t *testing.T) {
	fix := newResolutionFixture(t, failedOutcome("payments-service"))

	if _, err := fix.orch.ResolveRepositoryMergeExternally(fix.ctx, "run-1", "payments-service", "manual force-merge in upstream", ""); err != nil {
		t.Fatalf("ResolveRepositoryMergeExternally: %v", err)
	}

	got, err := fix.store.GetRepositoryMergeOutcome(context.Background(), "run-1", "payments-service")
	if err != nil {
		t.Fatalf("get outcome: %v", err)
	}
	if got.Status != domain.RepositoryMergeStatusResolvedExternally {
		t.Fatalf("expected status %s, got %s", domain.RepositoryMergeStatusResolvedExternally, got.Status)
	}
	if got.ResolvedBy != fix.actorID {
		t.Fatalf("expected resolved_by=%s, got %s", fix.actorID, got.ResolvedBy)
	}
	if got.ResolutionReason != "manual force-merge in upstream" {
		t.Fatalf("unexpected reason: %q", got.ResolutionReason)
	}
	if got.FailureClass != "" || got.FailureDetail != "" {
		t.Fatalf("expected failure fields cleared, got class=%s detail=%q", got.FailureClass, got.FailureDetail)
	}
	if got.Attempts != 2 {
		t.Fatalf("expected attempts preserved at 2, got %d", got.Attempts)
	}

	if len(fix.events.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(fix.events.events))
	}
	ev := fix.events.events[0]
	if ev.Type != domain.EventRunRepositoryMergeResolved {
		t.Fatalf("unexpected event type: %s", ev.Type)
	}
	if !strings.Contains(string(ev.Payload), "manual force-merge") {
		t.Fatalf("payload missing reason: %s", ev.Payload)
	}
	if !strings.Contains(string(ev.Payload), fix.actorID) {
		t.Fatalf("payload missing resolved_by: %s", ev.Payload)
	}
}

func TestResolveRepositoryMergeExternally_AlreadyResolvedIsIdempotent(t *testing.T) {
	original := failedOutcome("payments-service")
	original.Status = domain.RepositoryMergeStatusResolvedExternally
	original.ResolvedBy = "first-actor"
	original.ResolutionReason = "first reason"
	original.FailureClass = ""
	original.FailureDetail = ""

	fix := newResolutionFixture(t, original)

	if _, err := fix.orch.ResolveRepositoryMergeExternally(fix.ctx, "run-1", "payments-service", "second reason", ""); err != nil {
		t.Fatalf("expected idempotent success, got %v", err)
	}

	got, err := fix.store.GetRepositoryMergeOutcome(context.Background(), "run-1", "payments-service")
	if err != nil {
		t.Fatalf("get outcome: %v", err)
	}
	// Original audit pair must be preserved on idempotent success — we
	// should not overwrite the first operator's resolution reason.
	if got.ResolvedBy != "first-actor" || got.ResolutionReason != "first reason" {
		t.Fatalf("expected original audit preserved, got resolved_by=%q reason=%q",
			got.ResolvedBy, got.ResolutionReason)
	}
	if len(fix.events.events) != 1 {
		t.Fatalf("expected 1 event for the repeat call, got %d", len(fix.events.events))
	}
}

func TestResolveRepositoryMergeExternally_RejectsMerged(t *testing.T) {
	fix := newResolutionFixture(t, mergedOutcome("payments-service", "sha-merged"))

	_, err := fix.orch.ResolveRepositoryMergeExternally(fix.ctx, "run-1", "payments-service", "irrelevant", "")
	if err == nil {
		t.Fatal("expected error when resolving an already-merged outcome")
	}
	if !isConflictError(err) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestResolveRepositoryMergeExternally_RequiresActor(t *testing.T) {
	fix := newResolutionFixture(t, failedOutcome("payments-service"))

	_, err := fix.orch.ResolveRepositoryMergeExternally(context.Background(), "run-1", "payments-service", "reason", "")
	if err == nil {
		t.Fatal("expected error when no actor in context")
	}
	if !isUnauthorizedError(err) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

func TestResolveRepositoryMergeExternally_RequiresReason(t *testing.T) {
	fix := newResolutionFixture(t, failedOutcome("payments-service"))

	_, err := fix.orch.ResolveRepositoryMergeExternally(fix.ctx, "run-1", "payments-service", "   ", "")
	if err == nil {
		t.Fatal("expected error when reason is empty/whitespace")
	}
	if !isInvalidParamsError(err) {
		t.Fatalf("expected ErrInvalidParams, got %v", err)
	}
}

func TestResolveRepositoryMergeExternally_OutcomeNotFound(t *testing.T) {
	fix := newResolutionFixture(t)

	_, err := fix.orch.ResolveRepositoryMergeExternally(fix.ctx, "run-1", "missing-repo", "reason", "")
	if err == nil {
		t.Fatal("expected error when outcome row missing")
	}
}

// ── RetryRepositoryMerge ──

func TestRetryRepositoryMerge_FailedToPending(t *testing.T) {
	fix := newResolutionFixture(t, failedOutcome("payments-service"))

	if _, err := fix.orch.RetryRepositoryMerge(fix.ctx, "run-1", "payments-service", "fixed upstream"); err != nil {
		t.Fatalf("RetryRepositoryMerge: %v", err)
	}

	got, err := fix.store.GetRepositoryMergeOutcome(context.Background(), "run-1", "payments-service")
	if err != nil {
		t.Fatalf("get outcome: %v", err)
	}
	if got.Status != domain.RepositoryMergeStatusPending {
		t.Fatalf("expected status pending, got %s", got.Status)
	}
	if got.FailureClass != "" || got.FailureDetail != "" {
		t.Fatalf("expected failure fields cleared, got class=%s detail=%q", got.FailureClass, got.FailureDetail)
	}
	if got.ResolvedBy != "" || got.ResolutionReason != "" {
		t.Fatalf("expected resolution fields cleared on pending, got resolved_by=%q reason=%q",
			got.ResolvedBy, got.ResolutionReason)
	}
	if got.Attempts != 2 {
		t.Fatalf("expected attempts preserved at 2, got %d", got.Attempts)
	}

	if len(fix.events.events) != 1 {
		t.Fatalf("expected 1 retry event, got %d", len(fix.events.events))
	}
	ev := fix.events.events[0]
	if ev.Type != domain.EventRunRepositoryMergeRetryRequested {
		t.Fatalf("unexpected event type: %s", ev.Type)
	}
	if !strings.Contains(string(ev.Payload), "fixed upstream") {
		t.Fatalf("payload missing reason: %s", ev.Payload)
	}
	if !strings.Contains(string(ev.Payload), fix.actorID) {
		t.Fatalf("payload missing requested_by: %s", ev.Payload)
	}
}

func TestRetryRepositoryMerge_PendingIsIdempotent(t *testing.T) {
	pending := domain.RepositoryMergeOutcome{
		RunID:        "run-1",
		RepositoryID: "payments-service",
		Status:       domain.RepositoryMergeStatusPending,
		SourceBranch: "spine-run-1",
		TargetBranch: "main",
		Attempts:     0,
	}
	fix := newResolutionFixture(t, pending)

	if _, err := fix.orch.RetryRepositoryMerge(fix.ctx, "run-1", "payments-service", "redundant retry"); err != nil {
		t.Fatalf("expected idempotent success, got %v", err)
	}
	if len(fix.events.events) != 1 {
		t.Fatalf("expected 1 event even on no-op retry, got %d", len(fix.events.events))
	}
}

func TestRetryRepositoryMerge_RejectsMerged(t *testing.T) {
	fix := newResolutionFixture(t, mergedOutcome("payments-service", "sha"))

	_, err := fix.orch.RetryRepositoryMerge(fix.ctx, "run-1", "payments-service", "irrelevant")
	if err == nil {
		t.Fatal("expected error when retrying an already-merged outcome")
	}
	if !isConflictError(err) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestRetryRepositoryMerge_RejectsResolvedExternally(t *testing.T) {
	resolved := failedOutcome("payments-service")
	resolved.Status = domain.RepositoryMergeStatusResolvedExternally
	resolved.ResolvedBy = "someone"
	resolved.ResolutionReason = "manual"
	resolved.FailureClass = ""
	resolved.FailureDetail = ""

	fix := newResolutionFixture(t, resolved)

	_, err := fix.orch.RetryRepositoryMerge(fix.ctx, "run-1", "payments-service", "reason")
	if err == nil {
		t.Fatal("expected error when retrying a resolved-externally outcome")
	}
	if !isConflictError(err) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestRetryRepositoryMerge_RequiresActor(t *testing.T) {
	fix := newResolutionFixture(t, failedOutcome("payments-service"))

	_, err := fix.orch.RetryRepositoryMerge(context.Background(), "run-1", "payments-service", "reason")
	if err == nil {
		t.Fatal("expected error when no actor in context")
	}
	if !isUnauthorizedError(err) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

func TestRetryRepositoryMerge_RequiresReason(t *testing.T) {
	fix := newResolutionFixture(t, failedOutcome("payments-service"))

	_, err := fix.orch.RetryRepositoryMerge(fix.ctx, "run-1", "payments-service", "")
	if err == nil {
		t.Fatal("expected error when reason is empty")
	}
	if !isInvalidParamsError(err) {
		t.Fatalf("expected ErrInvalidParams, got %v", err)
	}
}

// ── ledger commit ──

func newResolutionFixtureWithGit(t *testing.T, gitOp *recordingGitOperator, outcomes ...domain.RepositoryMergeOutcome) *resolutionFixture {
	t.Helper()
	fix := newResolutionFixture(t, outcomes...)
	fix.orch.git = gitOp
	return fix
}

func TestResolveRepositoryMergeExternally_WritesLedgerCommit(t *testing.T) {
	gitOp := &recordingGitOperator{commitSHA: "ledger-abc"}
	fix := newResolutionFixtureWithGit(t, gitOp, failedOutcome("payments-service"))

	if _, err := fix.orch.ResolveRepositoryMergeExternally(fix.ctx, "run-1", "payments-service",
		"manual force-merge", "upstream-sha-7777"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if len(gitOp.commits) != 1 {
		t.Fatalf("expected 1 ledger commit, got %d", len(gitOp.commits))
	}
	got := gitOp.commits[0]
	if !got.AllowEmpty {
		t.Fatalf("expected AllowEmpty on ledger commit")
	}
	if !strings.Contains(got.Message, "run-1") || !strings.Contains(got.Message, "payments-service") {
		t.Fatalf("ledger message missing run/repo identifiers: %q", got.Message)
	}
	if got.Trailers["Operation"] != "merge_recovery_resolve" {
		t.Fatalf("expected Operation=merge_recovery_resolve, got %q", got.Trailers["Operation"])
	}
	if got.Trailers["Resolved-By"] != fix.actorID {
		t.Fatalf("expected Resolved-By=%s, got %q", fix.actorID, got.Trailers["Resolved-By"])
	}
	if got.Trailers["Target-Commit-SHA"] != "upstream-sha-7777" {
		t.Fatalf("expected Target-Commit-SHA in trailers, got %q", got.Trailers["Target-Commit-SHA"])
	}
	// Reason lives in the body, not the trailers — protects against
	// trailer-injection via newline-bearing operator input.
	if _, ok := got.Trailers["Resolution-Reason"]; ok {
		t.Fatalf("Reason must not be a trailer (injection risk); trailers=%+v", got.Trailers)
	}
	if !strings.Contains(got.Body, "manual force-merge") {
		t.Fatalf("body missing reason: %q", got.Body)
	}

	// Ledger SHA is also surfaced in the operational event payload so
	// SSE/log-pull consumers do not need a separate git query.
	if len(fix.events.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(fix.events.events))
	}
	if !strings.Contains(string(fix.events.events[0].Payload), "ledger-abc") {
		t.Fatalf("event payload missing ledger sha: %s", fix.events.events[0].Payload)
	}
}

func TestRetryRepositoryMerge_WritesLedgerCommit(t *testing.T) {
	gitOp := &recordingGitOperator{commitSHA: "ledger-retry"}
	fix := newResolutionFixtureWithGit(t, gitOp, failedOutcome("payments-service"))

	if _, err := fix.orch.RetryRepositoryMerge(fix.ctx, "run-1", "payments-service", "transient flake"); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if len(gitOp.commits) != 1 {
		t.Fatalf("expected 1 ledger commit, got %d", len(gitOp.commits))
	}
	got := gitOp.commits[0]
	if got.Trailers["Operation"] != "merge_recovery_retry" {
		t.Fatalf("expected Operation=merge_recovery_retry, got %q", got.Trailers["Operation"])
	}
	if got.Trailers["Requested-By"] != fix.actorID {
		t.Fatalf("expected Requested-By=%s, got %q", fix.actorID, got.Trailers["Requested-By"])
	}
	if _, ok := got.Trailers["Target-Commit-SHA"]; ok {
		t.Fatalf("retry path must not include Target-Commit-SHA: %+v", got.Trailers)
	}
}

func TestResolveRepositoryMergeExternally_LedgerCommitFailureRollsBack(t *testing.T) {
	gitOp := &recordingGitOperator{commitErr: errors.New("disk full")}
	fix := newResolutionFixtureWithGit(t, gitOp, failedOutcome("payments-service"))

	_, err := fix.orch.ResolveRepositoryMergeExternally(fix.ctx, "run-1", "payments-service", "reason", "")
	if err == nil {
		t.Fatal("expected ledger commit failure to surface")
	}

	// Outcome row must remain `failed` — the audit invariant says we
	// never advance the runtime row to resolved-externally without a
	// matching ledger entry.
	got, _ := fix.store.GetRepositoryMergeOutcome(context.Background(), "run-1", "payments-service")
	if got.Status != domain.RepositoryMergeStatusFailed {
		t.Fatalf("outcome rolled forward despite ledger failure: status=%s", got.Status)
	}
	if len(fix.events.events) != 0 {
		t.Fatalf("expected no events on ledger failure, got %d", len(fix.events.events))
	}
}

func TestResolveRepositoryMergeExternally_AlreadyResolvedStillWritesLedgerCommit(t *testing.T) {
	resolved := failedOutcome("payments-service")
	resolved.Status = domain.RepositoryMergeStatusResolvedExternally
	resolved.ResolvedBy = "first-actor"
	resolved.ResolutionReason = "first reason"
	resolved.FailureClass = ""
	resolved.FailureDetail = ""

	gitOp := &recordingGitOperator{}
	fix := newResolutionFixtureWithGit(t, gitOp, resolved)

	if _, err := fix.orch.ResolveRepositoryMergeExternally(fix.ctx, "run-1", "payments-service", "second reason", ""); err != nil {
		t.Fatalf("expected idempotent success, got %v", err)
	}
	// Even on the idempotent path, the operator's repeat action lands
	// in the primary-repo history so the audit trail explains why a
	// resolved-externally row was touched again.
	if len(gitOp.commits) != 1 {
		t.Fatalf("expected ledger commit on idempotent resolve, got %d", len(gitOp.commits))
	}
}

// ── concurrent-action race ──

func TestResolveRepositoryMergeExternally_ConcurrentResolverWinsRace(t *testing.T) {
	original := failedOutcome("payments-service")
	gitOp := &raceGitOperator{
		recordingGitOperator: recordingGitOperator{commitSHA: "ledger-late"},
	}
	fix := newResolutionFixtureWithGit(t, &gitOp.recordingGitOperator, original)
	gitOp.store = fix.store
	fix.orch.git = gitOp

	// Simulate the racing operator landing their resolve while ours
	// is between "read failed" and "upsert resolved". The recording
	// git operator's onCommit hook flips the row to resolved-externally
	// with a different actor right after our ledger commit lands.
	gitOp.onCommit = func() {
		row, _ := fix.store.GetRepositoryMergeOutcome(context.Background(), "run-1", "payments-service")
		row.Status = domain.RepositoryMergeStatusResolvedExternally
		row.ResolvedBy = "first-actor"
		row.ResolutionReason = "first reason"
		row.FailureClass = ""
		row.FailureDetail = ""
		_ = fix.store.UpsertRepositoryMergeOutcome(context.Background(), row)
	}

	if _, err := fix.orch.ResolveRepositoryMergeExternally(fix.ctx, "run-1", "payments-service",
		"second reason", ""); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	got, _ := fix.store.GetRepositoryMergeOutcome(context.Background(), "run-1", "payments-service")
	if got.ResolvedBy != "first-actor" || got.ResolutionReason != "first reason" {
		t.Fatalf("expected first resolver's audit pair preserved, got resolved_by=%q reason=%q",
			got.ResolvedBy, got.ResolutionReason)
	}
}

// raceGitOperator runs an arbitrary hook between the Commit
// returning and the caller's next store operation, so tests can
// simulate a concurrent actor mutating the row mid-flow.
type raceGitOperator struct {
	recordingGitOperator
	store    *mockRunStore
	onCommit func()
}

func (r *raceGitOperator) Commit(ctx context.Context, opts git.CommitOpts) (git.CommitResult, error) {
	res, err := r.recordingGitOperator.Commit(ctx, opts)
	if r.onCommit != nil {
		r.onCommit()
	}
	return res, err
}

// ── audit injection guards ──

func TestResolveRepositoryMergeExternally_RejectsNewlineInTargetSHA(t *testing.T) {
	gitOp := &recordingGitOperator{}
	fix := newResolutionFixtureWithGit(t, gitOp, failedOutcome("payments-service"))

	// Newline lets a malicious target_commit_sha forge a synthetic
	// trailer like Resolved-By: someone-else. Engine must reject the
	// input rather than write it through.
	_, err := fix.orch.ResolveRepositoryMergeExternally(fix.ctx, "run-1", "payments-service",
		"reason", "abc123\nResolved-By: attacker")
	if err == nil {
		t.Fatal("expected ErrInvalidParams when target_commit_sha contains a newline")
	}
	if !isInvalidParamsError(err) {
		t.Fatalf("expected ErrInvalidParams, got %v", err)
	}
	if len(gitOp.commits) != 0 {
		t.Fatalf("no ledger commit should have been written, got %d", len(gitOp.commits))
	}
}

func TestResolveRepositoryMergeExternally_NewlineReasonStaysInBody(t *testing.T) {
	gitOp := &recordingGitOperator{commitSHA: "ledger-multiline"}
	fix := newResolutionFixtureWithGit(t, gitOp, failedOutcome("payments-service"))

	// Multi-line reasons are legitimate operator input (paste a paragraph).
	// They must be accepted but kept out of the trailer block so they
	// cannot inject synthetic trailers.
	multiLine := "Force-merged upstream after\nResolved-By: pretend-attacker\nbecause CI was wedged."

	res, err := fix.orch.ResolveRepositoryMergeExternally(fix.ctx, "run-1", "payments-service", multiLine, "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil result")
	}
	if len(gitOp.commits) != 1 {
		t.Fatalf("expected 1 ledger commit, got %d", len(gitOp.commits))
	}
	got := gitOp.commits[0]
	if got.Trailers["Resolved-By"] != fix.actorID {
		t.Fatalf("Resolved-By must be the authenticated actor, not the body content: got %q",
			got.Trailers["Resolved-By"])
	}
	if _, ok := got.Trailers["Resolution-Reason"]; ok {
		t.Fatalf("reason leaked into trailers: %+v", got.Trailers)
	}
	if !strings.Contains(got.Body, "pretend-attacker") {
		t.Fatalf("multi-line body should preserve full reason text: %q", got.Body)
	}
}

// ── resume readiness signal ──

func TestRetryRepositoryMerge_ResumeReadyWhenSoleFailure(t *testing.T) {
	gitOp := &recordingGitOperator{commitSHA: "ledger-1"}
	fix := newResolutionFixtureWithGit(t, gitOp,
		failedOutcome("payments-service"),
		mergedOutcome("auth-service", "auth-merged-sha"),
	)

	res, err := fix.orch.RetryRepositoryMerge(fix.ctx, "run-1", "payments-service", "retest")
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil result")
	}
	if !res.ReadyToResume {
		t.Fatalf("expected ReadyToResume=true once the only failure is retried; blocking=%v", res.BlockingRepositories)
	}
	if len(res.BlockingRepositories) != 0 {
		t.Fatalf("expected no blocking repositories, got %v", res.BlockingRepositories)
	}
	if res.LedgerCommitSHA != "ledger-1" {
		t.Fatalf("expected ledger SHA propagated, got %q", res.LedgerCommitSHA)
	}
}

func TestRetryRepositoryMerge_ResumeBlockedByOtherFailedRepos(t *testing.T) {
	gitOp := &recordingGitOperator{commitSHA: "ledger-2"}
	fix := newResolutionFixtureWithGit(t, gitOp,
		failedOutcome("payments-service"),
		failedOutcome("api-gateway"),
	)
	fix.run.AffectedRepositories = []string{repository.PrimaryRepositoryID, "payments-service", "api-gateway"}

	res, err := fix.orch.RetryRepositoryMerge(fix.ctx, "run-1", "payments-service", "retest")
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil result")
	}
	// Per-repo retry must surface the still-failed repos so the
	// operator does not silently believe the run will resume.
	if res.ReadyToResume {
		t.Fatalf("expected ReadyToResume=false while api-gateway still failed; blocking=%v", res.BlockingRepositories)
	}
	if len(res.BlockingRepositories) != 1 || res.BlockingRepositories[0] != "api-gateway" {
		t.Fatalf("expected BlockingRepositories=[api-gateway], got %v", res.BlockingRepositories)
	}
}

func TestResolveRepositoryMergeExternally_ResumeReadyWhenSoleFailure(t *testing.T) {
	gitOp := &recordingGitOperator{commitSHA: "ledger-3"}
	fix := newResolutionFixtureWithGit(t, gitOp,
		failedOutcome("payments-service"),
		mergedOutcome("auth-service", "auth-sha"),
	)
	fix.run.AffectedRepositories = []string{repository.PrimaryRepositoryID, "payments-service", "auth-service"}

	res, err := fix.orch.ResolveRepositoryMergeExternally(fix.ctx, "run-1", "payments-service", "manual fix", "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !res.ReadyToResume {
		t.Fatalf("expected ReadyToResume=true; blocking=%v", res.BlockingRepositories)
	}
}

// ── guardPartialMergeRecovery (run/repo scope) ──

func TestResolveRepositoryMergeExternally_RejectsNonPartialRun(t *testing.T) {
	fix := newResolutionFixture(t, failedOutcome("payments-service"))
	fix.run.Status = domain.RunStatusFailed

	_, err := fix.orch.ResolveRepositoryMergeExternally(fix.ctx, "run-1", "payments-service", "reason", "")
	if err == nil {
		t.Fatal("expected error when run is not partially-merged")
	}
	if !isConflictError(err) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
	// Outcome must remain failed — the audit table cannot be quietly
	// rewritten for a run that has no recovery path.
	got, _ := fix.store.GetRepositoryMergeOutcome(context.Background(), "run-1", "payments-service")
	if got.Status != domain.RepositoryMergeStatusFailed {
		t.Fatalf("outcome status was rewritten to %s; want failed", got.Status)
	}
}

func TestResolveRepositoryMergeExternally_RejectsPrimaryRepository(t *testing.T) {
	primary := failedOutcome(repository.PrimaryRepositoryID)
	fix := newResolutionFixture(t, primary)

	_, err := fix.orch.ResolveRepositoryMergeExternally(fix.ctx, "run-1", repository.PrimaryRepositoryID, "reason", "")
	if err == nil {
		t.Fatal("expected error when targeting primary repository")
	}
	if !isInvalidParamsError(err) {
		t.Fatalf("expected ErrInvalidParams, got %v", err)
	}
}

func TestResolveRepositoryMergeExternally_RejectsUnaffectedRepository(t *testing.T) {
	fix := newResolutionFixture(t, failedOutcome("payments-service"))

	_, err := fix.orch.ResolveRepositoryMergeExternally(fix.ctx, "run-1", "wandering-repo", "reason", "")
	if err == nil {
		t.Fatal("expected error when repository is not in run.AffectedRepositories")
	}
	if !isInvalidParamsError(err) {
		t.Fatalf("expected ErrInvalidParams, got %v", err)
	}
}

func TestRetryRepositoryMerge_RejectsNonPartialRun(t *testing.T) {
	fix := newResolutionFixture(t, failedOutcome("payments-service"))
	fix.run.Status = domain.RunStatusCompleted

	_, err := fix.orch.RetryRepositoryMerge(fix.ctx, "run-1", "payments-service", "reason")
	if err == nil {
		t.Fatal("expected error when run is not partially-merged")
	}
	if !isConflictError(err) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestRetryRepositoryMerge_RejectsPrimaryRepository(t *testing.T) {
	primary := failedOutcome(repository.PrimaryRepositoryID)
	fix := newResolutionFixture(t, primary)

	_, err := fix.orch.RetryRepositoryMerge(fix.ctx, "run-1", repository.PrimaryRepositoryID, "reason")
	if err == nil {
		t.Fatal("expected error when targeting primary repository")
	}
	if !isInvalidParamsError(err) {
		t.Fatalf("expected ErrInvalidParams, got %v", err)
	}
}

// ── helpers ──

func isConflictError(err error) bool {
	return matchSpineErr(err, domain.ErrConflict)
}

func isUnauthorizedError(err error) bool {
	return matchSpineErr(err, domain.ErrUnauthorized)
}

func isInvalidParamsError(err error) bool {
	return matchSpineErr(err, domain.ErrInvalidParams)
}

func matchSpineErr(err error, code domain.ErrorCode) bool {
	if err == nil {
		return false
	}
	se, ok := err.(*domain.SpineError)
	return ok && se.Code == code
}
