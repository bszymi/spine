package engine

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/bszymi/spine/internal/branchprotect"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/repository"
)

// trackingCodeRepoClient is a per-repository git.GitClient that
// records Merge and Push calls into a shared sequence so ordering
// across repos (and the primary) can be asserted from one place.
// Merge/push errors are injectable per stub.
type trackingCodeRepoClient struct {
	repoID string
	seq    *[]string

	mergeErr error
	pushErr  error
	mergeSHA string

	mergeCalls  []git.MergeOpts
	pushCalls   []pushCall
	deleteCalls []string
}

func (c *trackingCodeRepoClient) Clone(_ context.Context, _, _ string) error { return nil }
func (c *trackingCodeRepoClient) Commit(_ context.Context, _ git.CommitOpts) (git.CommitResult, error) {
	return git.CommitResult{}, nil
}
func (c *trackingCodeRepoClient) Merge(_ context.Context, opts git.MergeOpts) (git.MergeResult, error) {
	c.mergeCalls = append(c.mergeCalls, opts)
	if c.seq != nil {
		*c.seq = append(*c.seq, "merge:"+c.repoID)
	}
	if c.mergeErr != nil {
		return git.MergeResult{}, c.mergeErr
	}
	sha := c.mergeSHA
	if sha == "" {
		sha = "sha-" + c.repoID
	}
	return git.MergeResult{SHA: sha}, nil
}
func (c *trackingCodeRepoClient) CreateBranch(_ context.Context, _, _ string) error { return nil }
func (c *trackingCodeRepoClient) DeleteBranch(_ context.Context, name string) error {
	c.deleteCalls = append(c.deleteCalls, name)
	return nil
}
func (c *trackingCodeRepoClient) Diff(_ context.Context, _, _ string) ([]git.FileDiff, error) {
	return nil, nil
}
func (c *trackingCodeRepoClient) MergeBase(_ context.Context, a, _ string) (string, error) {
	return a, nil
}
func (c *trackingCodeRepoClient) Log(_ context.Context, _ git.LogOpts) ([]git.CommitInfo, error) {
	return nil, nil
}
func (c *trackingCodeRepoClient) ReadFile(_ context.Context, _, _ string) ([]byte, error) {
	return nil, nil
}
func (c *trackingCodeRepoClient) ListFiles(_ context.Context, _, _ string) ([]string, error) {
	return nil, nil
}
func (c *trackingCodeRepoClient) Head(_ context.Context) (string, error) { return "abc123", nil }
func (c *trackingCodeRepoClient) Push(_ context.Context, remote, ref string) error {
	c.pushCalls = append(c.pushCalls, pushCall{remote: remote, branch: ref})
	if c.seq != nil {
		*c.seq = append(*c.seq, "push:"+c.repoID)
	}
	return c.pushErr
}
func (c *trackingCodeRepoClient) PushBranch(_ context.Context, _, _ string) error            { return nil }
func (c *trackingCodeRepoClient) DeleteRemoteBranch(_ context.Context, _, _ string) error { return nil }

// trackingRepoGitClients exposes the per-repo trackingCodeRepoClients
// to the engine via the RepositoryGitClients contract.
type trackingRepoGitClients struct {
	clients map[string]*trackingCodeRepoClient
}

func (t *trackingRepoGitClients) Client(_ context.Context, repoID string) (git.GitClient, error) {
	c, ok := t.clients[repoID]
	if !ok {
		return nil, errors.New("test misconfigured: no client for " + repoID)
	}
	return c, nil
}

// trackingPrimaryGit wraps stubGitOperator with merge/push recording so
// the primary's call slot in the sequence is observable. Override only
// Merge and Push; everything else stays no-op.
type trackingPrimaryGit struct {
	stubGitOperator

	seq      *[]string
	mergeErr error
	pushErr  error
	mergeSHA string

	mergeCalls []git.MergeOpts
	pushCalls  []pushCall
	deleted    []string
}

func (p *trackingPrimaryGit) Merge(_ context.Context, opts git.MergeOpts) (git.MergeResult, error) {
	// The engine also calls Merge for the abort path (Source: "--abort",
	// Strategy: "abort"). Record only real merges so sequence asserts
	// stay readable.
	if opts.Strategy != "abort" {
		p.mergeCalls = append(p.mergeCalls, opts)
		if p.seq != nil {
			*p.seq = append(*p.seq, "merge:"+repository.PrimaryRepositoryID)
		}
	}
	if opts.Strategy == "abort" {
		return git.MergeResult{}, nil
	}
	if p.mergeErr != nil {
		return git.MergeResult{}, p.mergeErr
	}
	sha := p.mergeSHA
	if sha == "" {
		sha = "sha-" + repository.PrimaryRepositoryID
	}
	return git.MergeResult{SHA: sha}, nil
}

func (p *trackingPrimaryGit) Push(_ context.Context, remote, ref string) error {
	p.pushCalls = append(p.pushCalls, pushCall{remote: remote, branch: ref})
	if p.seq != nil {
		*p.seq = append(*p.seq, "push:"+repository.PrimaryRepositoryID)
	}
	return p.pushErr
}

func (p *trackingPrimaryGit) DeleteBranch(_ context.Context, name string) error {
	p.deleted = append(p.deleted, name)
	return nil
}

// mergeOrderTestSetup wires a Committing run with the given affected
// code repos and returns the orchestrator, the run store, the primary
// git tracker, the per-repo client trackers, and the shared sequence
// slice. The run is preloaded into the store so MergeRunBranch finds
// it on GetRun.
type mergeOrderTestSetup struct {
	orch    *Orchestrator
	store   *mockRunStore
	primary *trackingPrimaryGit
	repos   map[string]*trackingCodeRepoClient
	seq     *[]string
}

func setupMergeOrderTest(t *testing.T, runID, branch string, affected []string) *mergeOrderTestSetup {
	t.Helper()
	t.Setenv("SPINE_GIT_PUSH_ENABLED", "true")

	seq := make([]string, 0, 8)
	primary := &trackingPrimaryGit{seq: &seq}

	clients := map[string]*trackingCodeRepoClient{}
	resolverEntries := map[string]repoLookup{}
	for _, id := range affected {
		if id == repository.PrimaryRepositoryID {
			continue
		}
		clients[id] = &trackingCodeRepoClient{repoID: id, seq: &seq}
		resolverEntries[id] = activeRepoLookup(id, "main")
	}

	store := &mockRunStore{
		runs: map[string]*domain.Run{
			runID: {
				RunID:                runID,
				Status:               domain.RunStatusCommitting,
				BranchName:           branch,
				TaskPath:             "tasks/task-001.md",
				TraceID:              "trace-1234567890ab",
				AffectedRepositories: affected,
			},
		},
	}

	orch := &Orchestrator{
		store:    store,
		git:      primary,
		events:   &mockEventEmitter{},
		wfLoader: &stubWorkflowLoader{},
		policy:   branchprotect.NewPermissive(),
	}
	orch.WithRepositoryResolver(newRepoResolver(resolverEntries))
	orch.WithRepositoryGitClients(&trackingRepoGitClients{clients: clients})

	return &mergeOrderTestSetup{
		orch:    orch,
		store:   store,
		primary: primary,
		repos:   clients,
		seq:     &seq,
	}
}

// outcomeForRepo looks up the captured outcome row for (run, repo).
func (s *mergeOrderTestSetup) outcomeForRepo(t *testing.T, runID, repoID string) domain.RepositoryMergeOutcome {
	t.Helper()
	for _, o := range s.store.mergeOutcomes {
		if o.RunID == runID && o.RepositoryID == repoID {
			return o
		}
	}
	t.Fatalf("no outcome recorded for repo %q", repoID)
	return domain.RepositoryMergeOutcome{}
}

// TestMergeRunBranch_CodeRepoFirstOrdering pins the AC: every affected
// code repo merges before the primary, in declared order. Asserts on
// the cross-repo call sequence AND the order outcome rows landed in
// the store.
func TestMergeRunBranch_CodeRepoFirstOrdering(t *testing.T) {
	const runID = "run-order-1"
	const branch = "spine/run/run-order-1"
	affected := []string{repository.PrimaryRepositoryID, "payments-service", "api-gateway"}

	setup := setupMergeOrderTest(t, runID, branch, affected)

	if err := setup.orch.MergeRunBranch(context.Background(), runID); err != nil {
		t.Fatalf("MergeRunBranch: %v", err)
	}

	wantSeq := []string{
		"merge:payments-service",
		"push:payments-service",
		"merge:api-gateway",
		"push:api-gateway",
		"merge:" + repository.PrimaryRepositoryID,
		"push:" + repository.PrimaryRepositoryID,
	}
	if got := *setup.seq; !reflect.DeepEqual(got, wantSeq) {
		t.Errorf("merge call sequence: got %v, want %v", got, wantSeq)
	}

	wantUpsertOrder := []string{"payments-service", "api-gateway", repository.PrimaryRepositoryID}
	gotUpsertOrder := make([]string, 0, len(setup.store.mergeOutcomes))
	for _, o := range setup.store.mergeOutcomes {
		gotUpsertOrder = append(gotUpsertOrder, o.RepositoryID)
	}
	if !reflect.DeepEqual(gotUpsertOrder, wantUpsertOrder) {
		t.Errorf("outcome upsert order: got %v, want %v", gotUpsertOrder, wantUpsertOrder)
	}

	if got := setup.store.runs[runID].Status; got != domain.RunStatusCompleted {
		t.Errorf("run status: got %s, want completed", got)
	}
}

// TestMergeRunBranch_PrimaryRunsAfterCodeOutcomesPersist confirms the
// AC "Code repo outcomes are available before the primary repo
// ledger update": every code repo's outcome row exists in the store
// at the moment the primary Merge is dispatched.
func TestMergeRunBranch_PrimaryRunsAfterCodeOutcomesPersist(t *testing.T) {
	const runID = "run-order-2"
	affected := []string{repository.PrimaryRepositoryID, "payments-service", "api-gateway"}

	setup := setupMergeOrderTest(t, runID, "spine/run/run-order-2", affected)

	// Snapshot the outcome slice the moment the primary's Merge is
	// dispatched, by wrapping the primary git operator. The snapshot
	// proves every code-repo outcome was already persisted before the
	// primary ledger update — the AC's strict ordering, not just a
	// proxy via call sequence.
	var primaryStartSnapshot []domain.RepositoryMergeOutcome
	wrappedPrimary := &snapshotPrimaryGit{
		trackingPrimaryGit: setup.primary,
		store:              setup.store,
		snapshot:           &primaryStartSnapshot,
	}
	setup.orch.git = wrappedPrimary

	if err := setup.orch.MergeRunBranch(context.Background(), runID); err != nil {
		t.Fatalf("MergeRunBranch: %v", err)
	}

	wantRepoIDs := map[string]bool{"payments-service": true, "api-gateway": true}
	gotRepoIDs := map[string]bool{}
	for _, o := range primaryStartSnapshot {
		if o.RunID != runID || o.RepositoryID == repository.PrimaryRepositoryID {
			continue
		}
		gotRepoIDs[o.RepositoryID] = true
	}
	if !reflect.DeepEqual(gotRepoIDs, wantRepoIDs) {
		t.Errorf("at primary merge time: code-repo outcomes present = %v, want %v",
			gotRepoIDs, wantRepoIDs)
	}
}

// snapshotPrimaryGit captures the merge_outcomes table state at the
// moment the primary's Merge is invoked. This lets a test prove that
// every code-repo outcome was already persisted before the primary
// ledger update, satisfying TASK-002 AC #1 directly rather than via a
// proxy ordering check.
type snapshotPrimaryGit struct {
	*trackingPrimaryGit
	store    *mockRunStore
	snapshot *[]domain.RepositoryMergeOutcome
}

func (s *snapshotPrimaryGit) Merge(ctx context.Context, opts git.MergeOpts) (git.MergeResult, error) {
	if opts.Strategy != "abort" {
		cp := make([]domain.RepositoryMergeOutcome, len(s.store.mergeOutcomes))
		copy(cp, s.store.mergeOutcomes)
		*s.snapshot = cp
	}
	return s.trackingPrimaryGit.Merge(ctx, opts)
}

// TestMergeRunBranch_CodeRepoFailureTransitionsToPartiallyMerged
// covers two ACs together. A permanently-failed code repo:
//
//   - does NOT roll back successful prior merges, and the primary
//     repo still merges and records its outcome (TASK-002 ACs);
//   - DOES prevent the run from completing — EPIC-005 AC #5 says
//     completed runs require every affected repo to merge
//     successfully. TASK-003 introduces the non-terminal
//     partially-merged state for this case so the run remains
//     resumable rather than going to flat failed.
func TestMergeRunBranch_CodeRepoFailureTransitionsToPartiallyMerged(t *testing.T) {
	const runID = "run-fail-1"
	affected := []string{repository.PrimaryRepositoryID, "payments-service", "api-gateway"}

	setup := setupMergeOrderTest(t, runID, "spine/run/run-fail-1", affected)
	// Make api-gateway permanently fail (merge conflict).
	setup.repos["api-gateway"].mergeErr = &git.GitError{
		Kind: git.ErrKindPermanent, Op: "merge", Message: "merge conflict",
	}

	if err := setup.orch.MergeRunBranch(context.Background(), runID); err != nil {
		t.Fatalf("MergeRunBranch: %v", err)
	}

	// payments-service merged OK.
	pmt := setup.outcomeForRepo(t, runID, "payments-service")
	if pmt.Status != domain.RepositoryMergeStatusMerged {
		t.Errorf("payments-service: got status %s, want merged", pmt.Status)
	}

	// api-gateway recorded as failed with merge_conflict class.
	api := setup.outcomeForRepo(t, runID, "api-gateway")
	if api.Status != domain.RepositoryMergeStatusFailed {
		t.Errorf("api-gateway: got status %s, want failed", api.Status)
	}
	if api.FailureClass != domain.MergeFailureConflict {
		t.Errorf("api-gateway: got class %s, want %s",
			api.FailureClass, domain.MergeFailureConflict)
	}

	// Primary still merged — code-repo failure must not block the
	// merge attempt itself.
	primary := setup.outcomeForRepo(t, runID, repository.PrimaryRepositoryID)
	if primary.Status != domain.RepositoryMergeStatusMerged {
		t.Errorf("primary: got status %s, want merged", primary.Status)
	}
	if len(setup.primary.mergeCalls) != 1 {
		t.Errorf("primary Merge calls: got %d, want 1", len(setup.primary.mergeCalls))
	}

	// Run is partially-merged (NOT failed): non-terminal state so the
	// scheduler can resume it after operator resolution.
	if got := setup.store.runs[runID].Status; got != domain.RunStatusPartiallyMerged {
		t.Errorf("run status: got %s, want partially-merged (TASK-003)", got)
	}
	if domain.RunStatusPartiallyMerged.IsTerminal() {
		t.Error("partially-merged must be non-terminal")
	}
}

// TestMergeRunBranch_OutcomesCarrySourceTargetAndAttempts verifies the
// outcome rows carry the data dashboards need: source = run branch,
// target = repo default branch, attempts increments per call. This
// also pins MergeCommitSHA + MergedAt on success and FailureClass on
// failure, which Validate would already enforce via Upsert.
func TestMergeRunBranch_OutcomesCarrySourceTargetAndAttempts(t *testing.T) {
	const runID = "run-data-1"
	const branch = "spine/run/run-data-1"
	affected := []string{repository.PrimaryRepositoryID, "payments-service"}

	setup := setupMergeOrderTest(t, runID, branch, affected)
	setup.repos["payments-service"].mergeSHA = "code-sha-abc"
	setup.primary.mergeSHA = "primary-sha-def"

	if err := setup.orch.MergeRunBranch(context.Background(), runID); err != nil {
		t.Fatalf("MergeRunBranch: %v", err)
	}

	pmt := setup.outcomeForRepo(t, runID, "payments-service")
	if pmt.SourceBranch != branch {
		t.Errorf("payments-service source: got %s, want %s", pmt.SourceBranch, branch)
	}
	if pmt.TargetBranch != "main" {
		t.Errorf("payments-service target: got %s, want main", pmt.TargetBranch)
	}
	if pmt.MergeCommitSHA != "code-sha-abc" {
		t.Errorf("payments-service merge_commit_sha: got %s, want code-sha-abc", pmt.MergeCommitSHA)
	}
	if pmt.MergedAt == nil {
		t.Error("payments-service: merged_at must be set on merged status")
	}
	if pmt.Attempts != 1 {
		t.Errorf("payments-service attempts: got %d, want 1", pmt.Attempts)
	}
	if pmt.LastAttemptedAt == nil {
		t.Error("payments-service: last_attempted_at must be set")
	}

	primary := setup.outcomeForRepo(t, runID, repository.PrimaryRepositoryID)
	if primary.SourceBranch != branch {
		t.Errorf("primary source: got %s, want %s", primary.SourceBranch, branch)
	}
	if primary.TargetBranch != authoritativeBranch {
		t.Errorf("primary target: got %s, want %s", primary.TargetBranch, authoritativeBranch)
	}
	if primary.MergeCommitSHA != "primary-sha-def" {
		t.Errorf("primary merge_commit_sha: got %s, want primary-sha-def", primary.MergeCommitSHA)
	}
}

// TestMergeRunBranch_AttemptsIncrementsOnRetry verifies the attempts
// counter survives a retry on the primary outcome. The natural retry
// path for the run state machine is a primary merge transient failure
// (the run stays committing → scheduler re-invokes MergeRunBranch).
// Code-repo failures don't keep the run committing because TASK-002's
// AC says they must not block primary merge — a code-repo retry is
// driven by TASK-006's manual-resolution path, not the run state, so
// pinning the attempts counter via primary retries is the
// general-purpose check.
func TestMergeRunBranch_AttemptsIncrementsOnRetry(t *testing.T) {
	const runID = "run-retry-1"
	affected := []string{repository.PrimaryRepositoryID}

	setup := setupMergeOrderTest(t, runID, "spine/run/run-retry-1", affected)
	// First attempt: primary network fails (transient).
	setup.primary.mergeErr = &git.GitError{
		Kind: git.ErrKindTransient, Op: "merge", Message: "network error",
	}

	if err := setup.orch.MergeRunBranch(context.Background(), runID); err != nil {
		t.Fatalf("MergeRunBranch (1st): %v", err)
	}
	primary1 := setup.outcomeForRepo(t, runID, repository.PrimaryRepositoryID)
	if primary1.Attempts != 1 {
		t.Errorf("after 1st attempt: got attempts=%d, want 1", primary1.Attempts)
	}
	if primary1.FailureClass != domain.MergeFailureNetwork {
		t.Errorf("transient network failure: got class %s, want network",
			primary1.FailureClass)
	}

	// Run remained committing (transient) — clear the error and retry.
	if got := setup.store.runs[runID].Status; got != domain.RunStatusCommitting {
		t.Fatalf("after 1st transient: run status got %s, want committing", got)
	}
	setup.primary.mergeErr = nil

	if err := setup.orch.MergeRunBranch(context.Background(), runID); err != nil {
		t.Fatalf("MergeRunBranch (2nd): %v", err)
	}
	primary2 := setup.outcomeForRepo(t, runID, repository.PrimaryRepositoryID)
	if primary2.Attempts != 2 {
		t.Errorf("after 2nd attempt: got attempts=%d, want 2", primary2.Attempts)
	}
	if primary2.Status != domain.RepositoryMergeStatusMerged {
		t.Errorf("after retry: got status %s, want merged", primary2.Status)
	}
}

// TestMergeRunBranch_PrimaryOnlyRunSkipsCodeRepoMerge confirms the
// fast path: a run with no code repos in AffectedRepositories does
// not touch repoClients.Client, does not call any per-repo Merge, and
// records exactly one outcome row (the primary).
func TestMergeRunBranch_PrimaryOnlyRunSkipsCodeRepoMerge(t *testing.T) {
	const runID = "run-primary-only"
	affected := []string{repository.PrimaryRepositoryID}

	setup := setupMergeOrderTest(t, runID, "spine/run/run-primary-only", affected)

	if err := setup.orch.MergeRunBranch(context.Background(), runID); err != nil {
		t.Fatalf("MergeRunBranch: %v", err)
	}

	if len(setup.repos) != 0 {
		t.Errorf("primary-only run should not allocate code-repo clients, got %v",
			setup.repos)
	}
	if got := *setup.seq; len(got) != 2 || got[0] != "merge:"+repository.PrimaryRepositoryID || got[1] != "push:"+repository.PrimaryRepositoryID {
		t.Errorf("primary-only sequence: got %v, want [merge:spine push:spine]", got)
	}
	if len(setup.store.mergeOutcomes) != 1 {
		t.Fatalf("expected exactly one outcome (primary), got %d", len(setup.store.mergeOutcomes))
	}
	if setup.store.mergeOutcomes[0].RepositoryID != repository.PrimaryRepositoryID {
		t.Errorf("only outcome should be primary, got %s",
			setup.store.mergeOutcomes[0].RepositoryID)
	}
}

// TestMergeRunBranch_NoBranchSkipsBothPaths preserves the existing
// pre-TASK-002 contract: a run with empty BranchName transitions
// directly to completed without touching merge for either primary or
// code repos. Without this guard, the new code-repo loop could
// inadvertently push branches that were never created.
func TestMergeRunBranch_NoBranchSkipsBothPaths(t *testing.T) {
	const runID = "run-no-branch"
	affected := []string{repository.PrimaryRepositoryID, "payments-service"}

	setup := setupMergeOrderTest(t, runID, "" /* no branch */, affected)

	if err := setup.orch.MergeRunBranch(context.Background(), runID); err != nil {
		t.Fatalf("MergeRunBranch: %v", err)
	}

	if got := *setup.seq; len(got) != 0 {
		t.Errorf("no-branch run should not invoke any merge: got %v", got)
	}
	if len(setup.store.mergeOutcomes) != 0 {
		t.Errorf("no-branch run should not record outcomes: got %v",
			setup.store.mergeOutcomes)
	}
	if got := setup.store.runs[runID].Status; got != domain.RunStatusCompleted {
		t.Errorf("no-branch run: got status %s, want completed", got)
	}
}

// TestMergeRunBranch_CodeRepoMissingWiringFailsLoudly enforces the
// fail-closed posture from EPIC-004's branch-creation path: when a
// run declares code repos but the orchestrator was set up without
// repoClients/repositories wiring, the merge must error out instead
// of silently degrading to primary-only. Otherwise dashboards would
// see a successful primary merge for a multi-repo run that never
// actually touched its code repos.
func TestMergeRunBranch_CodeRepoMissingWiringFailsLoudly(t *testing.T) {
	const runID = "run-missing-wiring"
	store := &mockRunStore{
		runs: map[string]*domain.Run{
			runID: {
				RunID:                runID,
				Status:               domain.RunStatusCommitting,
				BranchName:           "spine/run/run-missing-wiring",
				TraceID:              "trace-1234567890ab",
				AffectedRepositories: []string{repository.PrimaryRepositoryID, "payments-service"},
			},
		},
	}
	primary := &trackingPrimaryGit{}
	orch := &Orchestrator{
		store:    store,
		git:      primary,
		events:   &mockEventEmitter{},
		wfLoader: &stubWorkflowLoader{},
		policy:   branchprotect.NewPermissive(),
		// repoClients and repositories deliberately unset.
	}

	err := orch.MergeRunBranch(context.Background(), runID)
	if err == nil {
		t.Fatal("expected error when multi-repo wiring is missing")
	}
	var spineErr *domain.SpineError
	if !errors.As(err, &spineErr) || spineErr.Code != domain.ErrPrecondition {
		t.Errorf("expected ErrPrecondition, got %v", err)
	}

	// Primary merge must NOT have run — code repo failure aborted the
	// chain before the ledger update.
	if len(primary.mergeCalls) != 0 {
		t.Errorf("primary Merge must not run when code-repo wiring is missing, got %d calls",
			len(primary.mergeCalls))
	}
	if len(store.mergeOutcomes) != 0 {
		t.Errorf("no outcomes should be persisted when wiring is missing, got %v",
			store.mergeOutcomes)
	}
}

// TestMergeRunBranch_PartiallyMergedPreservesAllBranches extends the
// partially-merged transition check with the branch-cleanup
// invariant: when the run moves committing → partially-merged
// (TASK-003), no branches are cleaned up — primary or code repo.
// Operators get every ref the conflict resolution needs.
func TestMergeRunBranch_PartiallyMergedPreservesAllBranches(t *testing.T) {
	const runID = "run-preserve-1"
	const branch = "spine/run/run-preserve-1"
	affected := []string{repository.PrimaryRepositoryID, "payments-service", "api-gateway"}

	setup := setupMergeOrderTest(t, runID, branch, affected)
	// api-gateway permanently fails (merge conflict).
	setup.repos["api-gateway"].mergeErr = &git.GitError{
		Kind: git.ErrKindPermanent, Op: "merge", Message: "merge conflict",
	}

	if err := setup.orch.MergeRunBranch(context.Background(), runID); err != nil {
		t.Fatalf("MergeRunBranch: %v", err)
	}

	if got := setup.store.runs[runID].Status; got != domain.RunStatusPartiallyMerged {
		t.Errorf("run status: got %s, want partially-merged", got)
	}

	// payments-service merged successfully but the run is partially-
	// merged (not completed) so completeAfterMerge never ran cleanup.
	pmt := setup.repos["payments-service"]
	if len(pmt.deleteCalls) != 0 {
		t.Errorf("payments-service: expected NO DeleteBranch on partially-merged run, got %v",
			pmt.deleteCalls)
	}

	// api-gateway failed → branch preserved regardless.
	api := setup.repos["api-gateway"]
	if len(api.deleteCalls) != 0 {
		t.Errorf("api-gateway: expected NO DeleteBranch (preserved for recovery), got %v",
			api.deleteCalls)
	}

	// Primary branch also preserved on partially-merged path.
	if len(setup.primary.deleted) != 0 {
		t.Errorf("primary: expected NO DeleteBranch on partially-merged run, got %v",
			setup.primary.deleted)
	}
}

// TestMergeRunBranch_PrimaryPushFailureRecordsFailedOutcome pins the
// other codex pass-1 finding: a primary merge that lands locally but
// fails on push must record outcome=failed (not merged) so dashboards
// see the same status as a code repo with a failed push. The failure
// detail captures the local SHA so recovery can confirm what landed
// locally.
func TestMergeRunBranch_PrimaryPushFailureRecordsFailedOutcome(t *testing.T) {
	const runID = "run-push-fail-1"
	affected := []string{repository.PrimaryRepositoryID}

	setup := setupMergeOrderTest(t, runID, "spine/run/run-push-fail-1", affected)
	setup.primary.mergeSHA = "local-sha-xyz"
	setup.primary.pushErr = &git.GitError{
		Kind: git.ErrKindPermanent, Op: "push", Message: "authentication failed",
	}

	if err := setup.orch.MergeRunBranch(context.Background(), runID); err != nil {
		t.Fatalf("MergeRunBranch: %v", err)
	}

	primary := setup.outcomeForRepo(t, runID, repository.PrimaryRepositoryID)
	if primary.Status != domain.RepositoryMergeStatusFailed {
		t.Errorf("primary on push fail: got status %s, want failed", primary.Status)
	}
	if primary.FailureClass != domain.MergeFailureAuth {
		t.Errorf("primary on push fail: got class %s, want auth", primary.FailureClass)
	}
	// merge_commit_sha must not be set on a failed outcome (Validate
	// enforces it). The local SHA goes into failure_detail instead.
	if primary.MergeCommitSHA != "" {
		t.Errorf("primary on push fail: merge_commit_sha must be empty on failed status, got %q",
			primary.MergeCommitSHA)
	}
	if !contains(primary.FailureDetail, "local-sha-xyz") {
		t.Errorf("primary on push fail: failure_detail should mention local SHA, got %q",
			primary.FailureDetail)
	}

	// Run flips to failed (auth = permanent).
	if got := setup.store.runs[runID].Status; got != domain.RunStatusFailed {
		t.Errorf("run status after permanent push fail: got %s, want failed", got)
	}
}

// TestMergeRunBranch_CodeRepoMergedTerminalSkippedOnRetry pins the
// codex pass-2 finding: when the run goes back to committing for
// retry (e.g. primary transient failure), already-merged code repos
// must NOT be re-attempted. A no-op merge followed by a fresh push
// blip would otherwise overwrite a successful `merged` row with
// `failed`, and CleanupRunBranch would preserve a branch for a repo
// that already landed.
func TestMergeRunBranch_CodeRepoMergedTerminalSkippedOnRetry(t *testing.T) {
	const runID = "run-skip-retry-1"
	affected := []string{repository.PrimaryRepositoryID, "payments-service"}

	setup := setupMergeOrderTest(t, runID, "spine/run/run-skip-retry-1", affected)

	// First pass: payments-service merges; primary fails transiently
	// so the run stays committing.
	setup.primary.mergeErr = &git.GitError{
		Kind: git.ErrKindTransient, Op: "merge", Message: "network error",
	}
	if err := setup.orch.MergeRunBranch(context.Background(), runID); err != nil {
		t.Fatalf("MergeRunBranch (1st): %v", err)
	}
	pmt1 := setup.outcomeForRepo(t, runID, "payments-service")
	if pmt1.Status != domain.RepositoryMergeStatusMerged {
		t.Fatalf("after 1st: payments-service got %s, want merged", pmt1.Status)
	}
	if pmtCalls := setup.repos["payments-service"].mergeCalls; len(pmtCalls) != 1 {
		t.Errorf("after 1st: payments-service Merge calls got %d, want 1", len(pmtCalls))
	}

	// Run is back in committing; clear the primary error and arm a
	// transient push failure on payments-service. If the loop did NOT
	// skip terminal outcomes, it would re-attempt the merge (no-op
	// already-up-to-date) and the push failure would overwrite the
	// merged outcome with failed.
	setup.primary.mergeErr = nil
	setup.repos["payments-service"].pushErr = &git.GitError{
		Kind: git.ErrKindTransient, Op: "push", Message: "network error",
	}

	if err := setup.orch.MergeRunBranch(context.Background(), runID); err != nil {
		t.Fatalf("MergeRunBranch (2nd): %v", err)
	}

	// payments-service must still be merged — no second Merge call,
	// no overwrite of the outcome.
	pmt2 := setup.outcomeForRepo(t, runID, "payments-service")
	if pmt2.Status != domain.RepositoryMergeStatusMerged {
		t.Errorf("after 2nd: payments-service got %s, want merged (terminal outcome must be sticky)",
			pmt2.Status)
	}
	if pmt2.Attempts != 1 {
		t.Errorf("after 2nd: payments-service attempts got %d, want 1 (no re-attempt)",
			pmt2.Attempts)
	}
	if pmtCalls := setup.repos["payments-service"].mergeCalls; len(pmtCalls) != 1 {
		t.Errorf("after 2nd: payments-service Merge calls got %d, want 1 (skip on terminal)",
			len(pmtCalls))
	}
}

// TestMergeRunBranch_PrimaryOutcomePersistFailureKeepsCommitting
// pins the codex pass-2 finding: a store error while recording the
// primary outcome must propagate, leaving the run in committing for
// the scheduler to retry. A logged-and-continued path here would
// advance the run without the audit row, contradicting the AC and
// the symmetric code-repo path.
func TestMergeRunBranch_PrimaryOutcomePersistFailureKeepsCommitting(t *testing.T) {
	const runID = "run-record-fail-1"
	affected := []string{repository.PrimaryRepositoryID}

	setup := setupMergeOrderTest(t, runID, "spine/run/run-record-fail-1", affected)
	// Wrap the existing store with a fault-injecting decorator that
	// fails primary (only) UpsertRepositoryMergeOutcome calls. Code
	// repo upserts and reads pass through.
	failingStore := &recordFailingStore{mockRunStore: setup.store, failFor: repository.PrimaryRepositoryID}
	setup.orch.store = failingStore

	err := setup.orch.MergeRunBranch(context.Background(), runID)
	if err == nil {
		t.Fatal("expected error when primary outcome record fails, got nil")
	}
	if !contains(err.Error(), "record primary merge outcome") {
		t.Errorf("error must name the primary outcome record failure, got %q", err.Error())
	}

	// Run stays in committing — the merge happened locally but the
	// audit row didn't, so the scheduler retries.
	if got := setup.store.runs[runID].Status; got != domain.RunStatusCommitting {
		t.Errorf("run status: got %s, want committing", got)
	}
}

// recordFailingStore wraps mockRunStore and forces an error on
// UpsertRepositoryMergeOutcome for a specific repository ID. All
// other store operations pass through to the underlying mock.
type recordFailingStore struct {
	*mockRunStore
	failFor string
}

func (r *recordFailingStore) UpsertRepositoryMergeOutcome(ctx context.Context, outcome *domain.RepositoryMergeOutcome) error {
	if outcome != nil && outcome.RepositoryID == r.failFor {
		return errors.New("synthetic store failure")
	}
	return r.mockRunStore.UpsertRepositoryMergeOutcome(ctx, outcome)
}

// TestMergeRunBranch_TransientCodeRepoFailureKeepsCommitting pins
// the codex pass-3 finding: a code repo merge that fails with a
// retryable GitError (network, lock) must hold the run in committing
// so the scheduler retries — without this guard the run completes
// and the failed-transient outcome is stranded forever.
func TestMergeRunBranch_TransientCodeRepoFailureKeepsCommitting(t *testing.T) {
	const runID = "run-transient-1"
	affected := []string{repository.PrimaryRepositoryID, "payments-service", "api-gateway"}

	setup := setupMergeOrderTest(t, runID, "spine/run/run-transient-1", affected)
	// payments-service merges; api-gateway hits a network error
	// (transient).
	setup.repos["api-gateway"].mergeErr = &git.GitError{
		Kind: git.ErrKindTransient, Op: "merge", Message: "network error",
	}

	if err := setup.orch.MergeRunBranch(context.Background(), runID); err != nil {
		t.Fatalf("MergeRunBranch (1st): %v", err)
	}

	// Run must NOT complete — it stays in committing for the scheduler
	// to retry the transient code-repo failure.
	if got := setup.store.runs[runID].Status; got != domain.RunStatusCommitting {
		t.Errorf("run status after transient code-repo fail: got %s, want committing", got)
	}
	// Primary must NOT have merged — code repo outcomes are not yet
	// terminal, so the ledger update must wait.
	if len(setup.primary.mergeCalls) != 0 {
		t.Errorf("primary Merge calls: got %d, want 0 (must wait for code repo retry)",
			len(setup.primary.mergeCalls))
	}
	// api-gateway recorded as failed-transient.
	api := setup.outcomeForRepo(t, runID, "api-gateway")
	if api.FailureClass != domain.MergeFailureNetwork {
		t.Errorf("api-gateway: got class %s, want network", api.FailureClass)
	}

	// 2nd pass: clear the transient error. payments-service is skipped
	// (terminal merged), api-gateway retried and succeeds, primary
	// then runs.
	setup.repos["api-gateway"].mergeErr = nil
	if err := setup.orch.MergeRunBranch(context.Background(), runID); err != nil {
		t.Fatalf("MergeRunBranch (2nd): %v", err)
	}
	api2 := setup.outcomeForRepo(t, runID, "api-gateway")
	if api2.Status != domain.RepositoryMergeStatusMerged {
		t.Errorf("api-gateway after retry: got %s, want merged", api2.Status)
	}
	if api2.Attempts != 2 {
		t.Errorf("api-gateway after retry: attempts got %d, want 2", api2.Attempts)
	}
	if got := setup.store.runs[runID].Status; got != domain.RunStatusCompleted {
		t.Errorf("run status after retry: got %s, want completed", got)
	}
}

// TestCleanupRunBranch_OutcomeListErrorPreservesAllCodeRepoBranches
// pins the codex pass-3 finding: when ListRepositoryMergeOutcomes
// errors during cleanup, every code-repo branch must be preserved.
// Deleting them would risk wiping the only ref carrying recoverable
// unmerged work; operators can reconcile by hand once the store is
// healthy again.
func TestCleanupRunBranch_OutcomeListErrorPreservesAllCodeRepoBranches(t *testing.T) {
	t.Setenv("SPINE_GIT_PUSH_ENABLED", "true")
	const runID = "run-cleanup-store-fail"
	const branch = "spine/run/run-cleanup-store-fail"

	primary := &trackingPrimaryGit{}
	clients := map[string]*trackingCodeRepoClient{
		"payments-service": {repoID: "payments-service"},
		"api-gateway":      {repoID: "api-gateway"},
	}
	resolver := newRepoResolver(map[string]repoLookup{
		"payments-service": activeRepoLookup("payments-service", "main"),
		"api-gateway":      activeRepoLookup("api-gateway", "main"),
	})

	listFailing := &listFailingStore{
		mockRunStore: &mockRunStore{
			runs: map[string]*domain.Run{
				runID: {
					RunID:                runID,
					Status:               domain.RunStatusCompleted,
					BranchName:           branch,
					AffectedRepositories: []string{repository.PrimaryRepositoryID, "payments-service", "api-gateway"},
				},
			},
		},
	}

	orch := &Orchestrator{
		store:    listFailing,
		git:      primary,
		events:   &mockEventEmitter{},
		wfLoader: &stubWorkflowLoader{},
		policy:   branchprotect.NewPermissive(),
	}
	orch.WithRepositoryResolver(resolver)
	orch.WithRepositoryGitClients(&trackingRepoGitClients{clients: clients})

	if err := orch.CleanupRunBranch(context.Background(), runID); err != nil {
		t.Fatalf("CleanupRunBranch: %v", err)
	}

	// Primary cleanup still proceeds — it doesn't depend on the
	// outcome list (its merge status is reflected in run state).
	if len(primary.deleted) != 1 {
		t.Errorf("primary cleanup: got %d DeleteBranch calls, want 1", len(primary.deleted))
	}

	// Both code-repo branches preserved — outcome list error means we
	// cannot tell which repos have failed merges, so fail closed.
	if got := clients["payments-service"].deleteCalls; len(got) != 0 {
		t.Errorf("payments-service: expected NO DeleteBranch on outcome-list error, got %v", got)
	}
	if got := clients["api-gateway"].deleteCalls; len(got) != 0 {
		t.Errorf("api-gateway: expected NO DeleteBranch on outcome-list error, got %v", got)
	}
}

// listFailingStore wraps mockRunStore and forces an error on
// ListRepositoryMergeOutcomes. All other operations pass through.
type listFailingStore struct {
	*mockRunStore
}

func (l *listFailingStore) ListRepositoryMergeOutcomes(_ context.Context, _ string) ([]domain.RepositoryMergeOutcome, error) {
	return nil, errors.New("synthetic store list failure")
}

// TestMergeRunBranch_FailedCodeRepoAbortsLeftoverMerge pins the
// codex pass-5 finding: a code repo merge that fails (e.g. with a
// conflict) must call abort against the per-repo client so the
// cached clone does not remain in MERGE_HEAD/conflicted state.
// Without abort the next op against the gitpool entry fails with
// "You have not concluded your merge."
func TestMergeRunBranch_FailedCodeRepoAbortsLeftoverMerge(t *testing.T) {
	const runID = "run-abort-1"
	affected := []string{repository.PrimaryRepositoryID, "payments-service"}

	setup := setupMergeOrderTest(t, runID, "spine/run/run-abort-1", affected)
	setup.repos["payments-service"].mergeErr = &git.GitError{
		Kind: git.ErrKindPermanent, Op: "merge", Message: "merge conflict",
	}

	if err := setup.orch.MergeRunBranch(context.Background(), runID); err != nil {
		t.Fatalf("MergeRunBranch: %v", err)
	}

	// payments-service was called with the merge → expect TWO Merge
	// calls: the actual merge attempt that failed, then the abort
	// (Strategy: "abort").
	pmt := setup.repos["payments-service"]
	if len(pmt.mergeCalls) != 2 {
		t.Fatalf("payments-service Merge calls: got %d, want 2 (merge + abort)",
			len(pmt.mergeCalls))
	}
	if pmt.mergeCalls[0].Strategy != "merge-commit" {
		t.Errorf("first Merge: got strategy %s, want merge-commit",
			pmt.mergeCalls[0].Strategy)
	}
	if pmt.mergeCalls[1].Strategy != "abort" {
		t.Errorf("second Merge: got strategy %s, want abort",
			pmt.mergeCalls[1].Strategy)
	}
}

// TestMergeRunBranch_OutcomeLookupErrorBlocksRetry pins the codex
// pass-5 finding: a non-NotFound error from
// GetRepositoryMergeOutcome must propagate so the run stays in
// committing for the scheduler to retry once the store recovers. A
// silent fall-through would let a transient read error re-merge a
// repo that already has a terminal outcome and overwrite the audit
// row the guard is meant to preserve.
func TestMergeRunBranch_OutcomeLookupErrorBlocksRetry(t *testing.T) {
	const runID = "run-lookup-fail-1"
	affected := []string{repository.PrimaryRepositoryID, "payments-service"}

	setup := setupMergeOrderTest(t, runID, "spine/run/run-lookup-fail-1", affected)

	failing := &lookupFailingStore{mockRunStore: setup.store, failFor: "payments-service"}
	setup.orch.store = failing

	err := setup.orch.MergeRunBranch(context.Background(), runID)
	if err == nil {
		t.Fatal("expected error when GetRepositoryMergeOutcome lookup fails non-NotFound, got nil")
	}
	if !contains(err.Error(), "lookup existing outcome") {
		t.Errorf("error must reference outcome lookup, got %q", err.Error())
	}
	// Run must NOT have completed — it stays committing for retry.
	if got := setup.store.runs[runID].Status; got != domain.RunStatusCommitting {
		t.Errorf("run status: got %s, want committing", got)
	}
	// Primary must NOT have merged.
	if len(setup.primary.mergeCalls) != 0 {
		t.Errorf("primary Merge calls: got %d, want 0", len(setup.primary.mergeCalls))
	}
}

// lookupFailingStore wraps mockRunStore and forces a non-NotFound
// error on GetRepositoryMergeOutcome for a specific repo ID. The
// orchestrator must distinguish this from the legitimate "first
// attempt" not-found case.
type lookupFailingStore struct {
	*mockRunStore
	failFor string
}

func (l *lookupFailingStore) GetRepositoryMergeOutcome(_ context.Context, _, repoID string) (*domain.RepositoryMergeOutcome, error) {
	if repoID == l.failFor {
		return nil, errors.New("synthetic non-NotFound store read failure")
	}
	return nil, domain.NewError(domain.ErrNotFound, "merge outcome not found")
}

// TestMergeRunBranch_PartiallyMergedResumesToCompletion drives the
// full TASK-003 lifecycle: a permanent code-repo failure parks the
// run in partially-merged → operator "resolves" the conflict (the
// test simulates by clearing the merge error and overwriting the
// failed outcome to non-terminal) → MergeRunBranch from committing
// re-walks and completes. Pins:
//   - partially-merged is the entry state from a permanent failure
//   - the run does NOT auto-complete while a code repo is failed
//   - once the failed outcome is reset and the merge error cleared,
//     a fresh MergeRunBranch (after committing transition) re-walks
//     the loop, merges the previously-failed repo, and completes
//     the run
func TestMergeRunBranch_PartiallyMergedResumesToCompletion(t *testing.T) {
	const runID = "run-resume-1"
	affected := []string{repository.PrimaryRepositoryID, "payments-service"}

	setup := setupMergeOrderTest(t, runID, "spine/run/run-resume-1", affected)
	setup.repos["payments-service"].mergeErr = &git.GitError{
		Kind: git.ErrKindPermanent, Op: "merge", Message: "merge conflict",
	}

	if err := setup.orch.MergeRunBranch(context.Background(), runID); err != nil {
		t.Fatalf("MergeRunBranch (1st): %v", err)
	}
	if got := setup.store.runs[runID].Status; got != domain.RunStatusPartiallyMerged {
		t.Fatalf("after 1st: got status %s, want partially-merged", got)
	}

	// Simulate operator resolution: clear the merge error and reset
	// the failed outcome so the loop's terminal-skip guard re-attempts
	// the repo. In production this would be driven by TASK-006's
	// manual-resolution API; for TASK-003 we verify the engine path.
	setup.repos["payments-service"].mergeErr = nil
	for i := range setup.store.mergeOutcomes {
		o := &setup.store.mergeOutcomes[i]
		if o.RunID == runID && o.RepositoryID == "payments-service" {
			o.Status = domain.RepositoryMergeStatusPending
			o.FailureClass = ""
			o.FailureDetail = ""
		}
	}
	// Scheduler resume: partially-merged → committing.
	applied, err := setup.store.TransitionRunStatus(context.Background(), runID,
		domain.RunStatusPartiallyMerged, domain.RunStatusCommitting)
	if err != nil || !applied {
		t.Fatalf("resume transition: applied=%v err=%v", applied, err)
	}

	if err := setup.orch.MergeRunBranch(context.Background(), runID); err != nil {
		t.Fatalf("MergeRunBranch (2nd): %v", err)
	}
	if got := setup.store.runs[runID].Status; got != domain.RunStatusCompleted {
		t.Errorf("after resume: got status %s, want completed", got)
	}

	pmt := setup.outcomeForRepo(t, runID, "payments-service")
	if pmt.Status != domain.RepositoryMergeStatusMerged {
		t.Errorf("payments-service after resume: got %s, want merged", pmt.Status)
	}
}

// TestMergeRunBranch_PartiallyMergedEmitsRunPartiallyMergedEvent
// guarantees the dedicated event is emitted so external subscribers
// (dashboards, runbook automation) can react to the new state without
// polling the runs table.
func TestMergeRunBranch_PartiallyMergedEmitsRunPartiallyMergedEvent(t *testing.T) {
	const runID = "run-event-1"
	affected := []string{repository.PrimaryRepositoryID, "payments-service"}

	setup := setupMergeOrderTest(t, runID, "spine/run/run-event-1", affected)
	setup.repos["payments-service"].mergeErr = &git.GitError{
		Kind: git.ErrKindPermanent, Op: "merge", Message: "merge conflict",
	}

	if err := setup.orch.MergeRunBranch(context.Background(), runID); err != nil {
		t.Fatalf("MergeRunBranch: %v", err)
	}

	emitter, ok := setup.orch.events.(*mockEventEmitter)
	if !ok {
		t.Fatalf("event emitter is not *mockEventEmitter: %T", setup.orch.events)
	}
	found := false
	for _, e := range emitter.events {
		if e.Type == domain.EventRunPartiallyMerged {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected %q event, events=%v",
			domain.EventRunPartiallyMerged, emitter.events)
	}
}

// TestClassifyMergeFailure covers the GitError → MergeFailureClass
// taxonomy directly. The mappings drive retry decisions (TASK-006),
// so a regression here would silently change scheduler behaviour.
func TestClassifyMergeFailure(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantClass domain.MergeFailureClass
	}{
		{
			name:      "nil error → unknown (defensive)",
			err:       nil,
			wantClass: domain.MergeFailureUnknown,
		},
		{
			name:      "non-git error → unknown",
			err:       errors.New("something else"),
			wantClass: domain.MergeFailureUnknown,
		},
		{
			name:      "transient lock → remote_unavailable",
			err:       &git.GitError{Kind: git.ErrKindTransient, Op: "merge", Message: "repository locked"},
			wantClass: domain.MergeFailureRemoteUnavailable,
		},
		{
			name:      "transient network → network",
			err:       &git.GitError{Kind: git.ErrKindTransient, Op: "merge", Message: "network error"},
			wantClass: domain.MergeFailureNetwork,
		},
		{
			name:      "permanent merge conflict → conflict",
			err:       &git.GitError{Kind: git.ErrKindPermanent, Op: "merge", Message: "merge conflict"},
			wantClass: domain.MergeFailureConflict,
		},
		{
			name:      "permanent auth → auth",
			err:       &git.GitError{Kind: git.ErrKindPermanent, Op: "push", Message: "authentication failed"},
			wantClass: domain.MergeFailureAuth,
		},
		{
			name:      "permanent push rejected → branch_protection",
			err:       &git.GitError{Kind: git.ErrKindPermanent, Op: "push", Message: "push rejected"},
			wantClass: domain.MergeFailureBranchProtection,
		},
		{
			name:      "not-found ref → precondition",
			err:       &git.GitError{Kind: git.ErrKindNotFound, Op: "merge", Message: "branch does not exist"},
			wantClass: domain.MergeFailurePrecondition,
		},
		{
			name:      "permanent unknown → unknown (catch-all)",
			err:       &git.GitError{Kind: git.ErrKindPermanent, Op: "merge", Message: "weird stderr"},
			wantClass: domain.MergeFailureUnknown,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			class, _ := classifyMergeFailure(tc.err)
			if class != tc.wantClass {
				t.Errorf("got %s, want %s", class, tc.wantClass)
			}
		})
	}
}
