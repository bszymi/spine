package engine

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/branchprotect"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/repository"
)

// cleanupPrimaryGit is a primary-side GitOperator with injectable
// delete errors. It records each DeleteBranch and DeleteRemoteBranch
// call so cleanup tests can assert exactly which refs were touched.
// Defaults are no-error so callers opt in per test.
type cleanupPrimaryGit struct {
	stubGitOperator

	deleteCalls       []string
	deleteRemoteCalls []pushCall

	deleteErr       error
	deleteRemoteErr error
}

func (g *cleanupPrimaryGit) DeleteBranch(_ context.Context, name string) error {
	g.deleteCalls = append(g.deleteCalls, name)
	return g.deleteErr
}

func (g *cleanupPrimaryGit) DeleteRemoteBranch(_ context.Context, remote, branch string) error {
	g.deleteRemoteCalls = append(g.deleteRemoteCalls, pushCall{remote: remote, branch: branch})
	return g.deleteRemoteErr
}

// cleanupOutcome is a small helper that builds a per-repo merge
// outcome row in the shape the engine writes (status-conditional
// fields satisfied so Validate would pass).
func cleanupOutcome(runID, repoID string, status domain.RepositoryMergeStatus) domain.RepositoryMergeOutcome {
	now := time.Now().UTC()
	o := domain.RepositoryMergeOutcome{
		RunID:        runID,
		RepositoryID: repoID,
		Status:       status,
		SourceBranch: "spine/run/" + runID,
		TargetBranch: "main",
		Attempts:     1,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	switch status {
	case domain.RepositoryMergeStatusMerged:
		o.MergeCommitSHA = "merge-sha-" + repoID
		o.MergedAt = &now
	case domain.RepositoryMergeStatusFailed:
		o.FailureClass = domain.MergeFailureConflict
		o.FailureDetail = "merge conflict on " + repoID
	}
	return o
}

// cleanupOrchestrator wires the orchestrator with primary git, code
// repo clients, and a preloaded run. Returns the orchestrator, the
// primary git tracker, the repo clients map, the store (for outcome
// writes), and the event emitter so tests can assert on emissions.
type cleanupHarness struct {
	orch    *Orchestrator
	primary *cleanupPrimaryGit
	clients map[string]*stubCodeRepoClient
	store   *mockRunStore
	events  *mockEventEmitter
	branch  string
}

func newCleanupHarness(t *testing.T, runID, branch string, codeRepos []string) *cleanupHarness {
	t.Helper()

	primary := &cleanupPrimaryGit{}
	clients := map[string]*stubCodeRepoClient{}
	resolverEntries := map[string]repoLookup{}
	affected := []string{repository.PrimaryRepositoryID}
	for _, id := range codeRepos {
		clients[id] = &stubCodeRepoClient{repoID: id}
		resolverEntries[id] = activeRepoLookup(id, "main")
		affected = append(affected, id)
	}

	store := &mockRunStore{
		runs: map[string]*domain.Run{
			runID: {
				RunID:                runID,
				Status:               domain.RunStatusCompleted,
				BranchName:           branch,
				TraceID:              "trace-1234567890ab",
				AffectedRepositories: affected,
			},
		},
	}
	events := &mockEventEmitter{}
	orch := &Orchestrator{
		store:    store,
		git:      primary,
		events:   events,
		wfLoader: &stubWorkflowLoader{},
		policy:   branchprotect.NewPermissive(),
	}
	orch.WithRepositoryResolver(newRepoResolver(resolverEntries))
	orch.WithRepositoryGitClients(&stubRepoGitClients{clients: clients})

	return &cleanupHarness{
		orch:    orch,
		primary: primary,
		clients: clients,
		store:   store,
		events:  events,
		branch:  branch,
	}
}

// findCleanupEvents extracts every EventRunBranchCleanupFailed payload
// from the recorded emissions.
func (h *cleanupHarness) findCleanupEvents(t *testing.T) []map[string]string {
	t.Helper()
	out := []map[string]string{}
	for _, e := range h.events.events {
		if e.Type != domain.EventRunBranchCleanupFailed {
			continue
		}
		var payload map[string]string
		if err := json.Unmarshal(e.Payload, &payload); err != nil {
			t.Fatalf("unmarshal cleanup payload: %v", err)
		}
		payload["__event_id"] = e.EventID
		out = append(out, payload)
	}
	return out
}

// TestCleanupRunBranch_MixedOutcomesDeletesSuccessAndPreservesFailure
// is the AC anchor for TASK-004: in a single cleanup pass with one
// merged repo and one failed repo, the merged repo's branch is
// deleted (local + remote) and the failed repo's branch is preserved.
// The primary (merged) is also deleted. No cleanup events are emitted
// when every delete succeeds.
func TestCleanupRunBranch_MixedOutcomesDeletesSuccessAndPreservesFailure(t *testing.T) {
	t.Setenv("SPINE_GIT_PUSH_ENABLED", "true")
	const runID = "run-mix-1"
	h := newCleanupHarness(t, runID, "spine/run/run-mix-1", []string{"payments-service", "api-gateway"})

	// payments-service merged, api-gateway failed.
	h.store.mergeOutcomes = []domain.RepositoryMergeOutcome{
		cleanupOutcome(runID, repository.PrimaryRepositoryID, domain.RepositoryMergeStatusMerged),
		cleanupOutcome(runID, "payments-service", domain.RepositoryMergeStatusMerged),
		cleanupOutcome(runID, "api-gateway", domain.RepositoryMergeStatusFailed),
	}

	if err := h.orch.CleanupRunBranch(context.Background(), runID); err != nil {
		t.Fatalf("CleanupRunBranch: %v", err)
	}

	// Primary deleted local + remote.
	if len(h.primary.deleteCalls) != 1 || h.primary.deleteCalls[0] != h.branch {
		t.Errorf("primary local delete: got %v, want [%s]", h.primary.deleteCalls, h.branch)
	}
	if len(h.primary.deleteRemoteCalls) != 1 {
		t.Errorf("primary remote delete: got %d, want 1", len(h.primary.deleteRemoteCalls))
	}

	// payments-service (merged) deleted local + remote.
	pmt := h.clients["payments-service"]
	if len(pmt.deleteCalls) != 1 || pmt.deleteCalls[0] != h.branch {
		t.Errorf("payments-service local delete: got %v, want [%s]", pmt.deleteCalls, h.branch)
	}
	if len(pmt.deleteRemoteCalls) != 1 {
		t.Errorf("payments-service remote delete: got %d, want 1", len(pmt.deleteRemoteCalls))
	}

	// api-gateway (failed) preserved local + remote.
	api := h.clients["api-gateway"]
	if len(api.deleteCalls) != 0 {
		t.Errorf("api-gateway local delete: got %v, want [] (preserved on failed outcome)", api.deleteCalls)
	}
	if len(api.deleteRemoteCalls) != 0 {
		t.Errorf("api-gateway remote delete: got %d, want 0 (preserved)", len(api.deleteRemoteCalls))
	}

	// No cleanup-failed events when every delete succeeded.
	if events := h.findCleanupEvents(t); len(events) != 0 {
		t.Errorf("expected no cleanup-failed events, got %v", events)
	}
}

// TestCleanupRunBranch_PrimaryFailedOutcomePreservesPrimary pins the
// new TASK-004 behavior: a primary outcome of `failed` (e.g. permanent
// push auth failure) preserves the primary's run branch — it is the
// only ref carrying the merged commits the operator needs to recover.
func TestCleanupRunBranch_PrimaryFailedOutcomePreservesPrimary(t *testing.T) {
	t.Setenv("SPINE_GIT_PUSH_ENABLED", "true")
	const runID = "run-primary-fail-1"
	h := newCleanupHarness(t, runID, "spine/run/run-primary-fail-1", []string{"payments-service"})

	h.store.mergeOutcomes = []domain.RepositoryMergeOutcome{
		cleanupOutcome(runID, repository.PrimaryRepositoryID, domain.RepositoryMergeStatusFailed),
		cleanupOutcome(runID, "payments-service", domain.RepositoryMergeStatusMerged),
	}

	if err := h.orch.CleanupRunBranch(context.Background(), runID); err != nil {
		t.Fatalf("CleanupRunBranch: %v", err)
	}

	if len(h.primary.deleteCalls) != 0 {
		t.Errorf("primary local delete: got %v, want [] (preserved on failed primary outcome)", h.primary.deleteCalls)
	}
	if len(h.primary.deleteRemoteCalls) != 0 {
		t.Errorf("primary remote delete: got %d, want 0 (preserved)", len(h.primary.deleteRemoteCalls))
	}

	// payments-service (merged) still cleaned up — independent.
	pmt := h.clients["payments-service"]
	if len(pmt.deleteCalls) != 1 {
		t.Errorf("payments-service: got %d local deletes, want 1", len(pmt.deleteCalls))
	}
	if len(pmt.deleteRemoteCalls) != 1 {
		t.Errorf("payments-service: got %d remote deletes, want 1", len(pmt.deleteRemoteCalls))
	}
}

// TestCleanupRunBranch_PerRepoErrorEmitsEventAndContinues pins the
// AC: "Cleanup errors are recorded without marking a merge as failed."
// A DeleteBranch error in one repo emits EventRunBranchCleanupFailed,
// is logged, AND does not block cleanup of other repos. The merge
// outcome row stays untouched.
func TestCleanupRunBranch_PerRepoErrorEmitsEventAndContinues(t *testing.T) {
	t.Setenv("SPINE_GIT_PUSH_ENABLED", "true")
	const runID = "run-fault-1"
	h := newCleanupHarness(t, runID, "spine/run/run-fault-1", []string{"payments-service", "api-gateway"})

	// payments-service local delete fails; api-gateway is clean.
	h.clients["payments-service"].deleteErr = errors.New("ref locked: branch checked out")

	h.store.mergeOutcomes = []domain.RepositoryMergeOutcome{
		cleanupOutcome(runID, repository.PrimaryRepositoryID, domain.RepositoryMergeStatusMerged),
		cleanupOutcome(runID, "payments-service", domain.RepositoryMergeStatusMerged),
		cleanupOutcome(runID, "api-gateway", domain.RepositoryMergeStatusMerged),
	}

	if err := h.orch.CleanupRunBranch(context.Background(), runID); err != nil {
		t.Fatalf("CleanupRunBranch must not surface code-repo cleanup errors, got %v", err)
	}

	// Remote delete still attempted on payments-service even though
	// local failed — the per-repo loop must not short-circuit out of
	// the remote step on a local error.
	pmt := h.clients["payments-service"]
	if len(pmt.deleteCalls) != 1 {
		t.Errorf("payments-service: expected 1 local delete attempt, got %d", len(pmt.deleteCalls))
	}
	if len(pmt.deleteRemoteCalls) != 1 {
		t.Errorf("payments-service: expected 1 remote delete attempt despite local failure, got %d",
			len(pmt.deleteRemoteCalls))
	}

	// api-gateway (next repo in the loop) was not blocked by payments-
	// service's failure.
	api := h.clients["api-gateway"]
	if len(api.deleteCalls) != 1 || len(api.deleteRemoteCalls) != 1 {
		t.Errorf("api-gateway: expected 1+1 deletes despite earlier failure, got %d+%d",
			len(api.deleteCalls), len(api.deleteRemoteCalls))
	}

	// Exactly one cleanup-failed event for payments-service local.
	events := h.findCleanupEvents(t)
	if len(events) != 1 {
		t.Fatalf("expected 1 cleanup-failed event, got %d: %v", len(events), events)
	}
	got := events[0]
	if got["repository_id"] != "payments-service" {
		t.Errorf("event repository_id: got %q, want payments-service", got["repository_id"])
	}
	if got["scope"] != "local" {
		t.Errorf("event scope: got %q, want local", got["scope"])
	}
	if got["branch"] != h.branch {
		t.Errorf("event branch: got %q, want %s", got["branch"], h.branch)
	}
	if got["error"] == "" {
		t.Errorf("event error: must be non-empty")
	}

	// Merge outcome rows untouched — cleanup errors must not flip a
	// merge to failed (the AC).
	for _, o := range h.store.mergeOutcomes {
		if o.Status != domain.RepositoryMergeStatusMerged {
			t.Errorf("outcome for %s: got status %s, want merged (cleanup error must not change merge status)",
				o.RepositoryID, o.Status)
		}
	}
}

// TestCleanupRunBranch_PerRepoRemoteErrorEmitsEvent confirms remote
// cleanup errors are reported with scope="remote" and the loop
// continues to subsequent repos.
func TestCleanupRunBranch_PerRepoRemoteErrorEmitsEvent(t *testing.T) {
	t.Setenv("SPINE_GIT_PUSH_ENABLED", "true")
	const runID = "run-remote-fault-1"
	h := newCleanupHarness(t, runID, "spine/run/run-remote-fault-1", []string{"payments-service"})

	h.clients["payments-service"].deleteRemoteErr = errors.New("network timeout")
	h.store.mergeOutcomes = []domain.RepositoryMergeOutcome{
		cleanupOutcome(runID, repository.PrimaryRepositoryID, domain.RepositoryMergeStatusMerged),
		cleanupOutcome(runID, "payments-service", domain.RepositoryMergeStatusMerged),
	}

	if err := h.orch.CleanupRunBranch(context.Background(), runID); err != nil {
		t.Fatalf("CleanupRunBranch: %v", err)
	}

	events := h.findCleanupEvents(t)
	if len(events) != 1 {
		t.Fatalf("expected 1 cleanup-failed event, got %d", len(events))
	}
	if events[0]["repository_id"] != "payments-service" || events[0]["scope"] != "remote" {
		t.Errorf("event: got %v, want repository_id=payments-service scope=remote", events[0])
	}
}

// TestCleanupRunBranch_PrimaryDeleteErrorRecordsAndReturns pins that
// a primary-side delete error is BOTH recorded as an event AND
// returned to the caller (so MergeRunBranch / FailRun can surface it
// in their logs). The per-repo loop still runs.
func TestCleanupRunBranch_PrimaryDeleteErrorRecordsAndReturns(t *testing.T) {
	t.Setenv("SPINE_GIT_PUSH_ENABLED", "true")
	const runID = "run-primary-fault-1"
	h := newCleanupHarness(t, runID, "spine/run/run-primary-fault-1", []string{"payments-service"})

	h.primary.deleteErr = errors.New("delete failed: branch checked out")
	h.store.mergeOutcomes = []domain.RepositoryMergeOutcome{
		cleanupOutcome(runID, repository.PrimaryRepositoryID, domain.RepositoryMergeStatusMerged),
		cleanupOutcome(runID, "payments-service", domain.RepositoryMergeStatusMerged),
	}

	err := h.orch.CleanupRunBranch(context.Background(), runID)
	if err == nil {
		t.Fatal("expected primary cleanup error to be returned, got nil")
	}
	if !contains(err.Error(), "delete failed") {
		t.Errorf("returned error must wrap primary cleanup error, got %q", err.Error())
	}

	// payments-service still cleaned up despite primary failure.
	pmt := h.clients["payments-service"]
	if len(pmt.deleteCalls) != 1 {
		t.Errorf("payments-service: expected 1 local delete despite primary failure, got %d",
			len(pmt.deleteCalls))
	}

	events := h.findCleanupEvents(t)
	if len(events) != 1 {
		t.Fatalf("expected 1 cleanup-failed event for primary, got %d", len(events))
	}
	if events[0]["repository_id"] != repository.PrimaryRepositoryID {
		t.Errorf("event repository_id: got %q, want %s",
			events[0]["repository_id"], repository.PrimaryRepositoryID)
	}
	if events[0]["scope"] != "local" {
		t.Errorf("event scope: got %q, want local", events[0]["scope"])
	}
}

// TestCleanupRunBranch_MultipleFailuresGetUniqueEventIDs guards
// against an event-ID collision when multiple deletes fail in the
// same nanosecond. The event log dedupes by event ID, so collapsed
// IDs would silently lose cleanup-failure observations.
func TestCleanupRunBranch_MultipleFailuresGetUniqueEventIDs(t *testing.T) {
	t.Setenv("SPINE_GIT_PUSH_ENABLED", "true")
	const runID = "run-many-fault-1"
	h := newCleanupHarness(t, runID, "spine/run/run-many-fault-1", []string{"payments-service", "api-gateway"})

	// Primary local + both code repo locals all fail.
	h.primary.deleteErr = errors.New("primary delete failed")
	h.clients["payments-service"].deleteErr = errors.New("pmt delete failed")
	h.clients["api-gateway"].deleteErr = errors.New("api delete failed")

	h.store.mergeOutcomes = []domain.RepositoryMergeOutcome{
		cleanupOutcome(runID, repository.PrimaryRepositoryID, domain.RepositoryMergeStatusMerged),
		cleanupOutcome(runID, "payments-service", domain.RepositoryMergeStatusMerged),
		cleanupOutcome(runID, "api-gateway", domain.RepositoryMergeStatusMerged),
	}

	_ = h.orch.CleanupRunBranch(context.Background(), runID)

	events := h.findCleanupEvents(t)
	if len(events) != 3 {
		t.Fatalf("expected 3 cleanup-failed events (primary + 2 code repos), got %d", len(events))
	}
	seen := map[string]bool{}
	for _, e := range events {
		id := e["__event_id"]
		if id == "" {
			t.Errorf("event missing event_id")
		}
		if seen[id] {
			t.Errorf("duplicate event ID %q — collisions would dedupe in event_log", id)
		}
		seen[id] = true
	}
}
