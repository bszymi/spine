package engine

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/repository"
	"github.com/bszymi/spine/internal/workflow"
)

// stubCodeRepoClient is a per-repository git.GitClient stub that
// records branch operations. It implements only the surface the
// run-start path uses (CreateBranch, DeleteBranch, PushBranch,
// DeleteRemoteBranch); the rest of the GitClient methods return zero
// values.
type stubCodeRepoClient struct {
	repoID string

	createCalls       []branchCall
	deleteCalls       []string
	pushCalls         []pushCall
	deleteRemoteCalls []pushCall

	createErr error
	pushErr   error
}

type branchCall struct {
	name string
	base string
}

type pushCall struct {
	remote string
	branch string
}

func (s *stubCodeRepoClient) Clone(_ context.Context, _, _ string) error { return nil }
func (s *stubCodeRepoClient) Commit(_ context.Context, _ git.CommitOpts) (git.CommitResult, error) {
	return git.CommitResult{}, nil
}
func (s *stubCodeRepoClient) Merge(_ context.Context, _ git.MergeOpts) (git.MergeResult, error) {
	return git.MergeResult{}, nil
}
func (s *stubCodeRepoClient) CreateBranch(_ context.Context, name, base string) error {
	s.createCalls = append(s.createCalls, branchCall{name: name, base: base})
	return s.createErr
}
func (s *stubCodeRepoClient) DeleteBranch(_ context.Context, name string) error {
	s.deleteCalls = append(s.deleteCalls, name)
	return nil
}
func (s *stubCodeRepoClient) Diff(_ context.Context, _, _ string) ([]git.FileDiff, error) {
	return nil, nil
}
func (s *stubCodeRepoClient) MergeBase(_ context.Context, a, _ string) (string, error) {
	return a, nil
}
func (s *stubCodeRepoClient) Log(_ context.Context, _ git.LogOpts) ([]git.CommitInfo, error) {
	return nil, nil
}
func (s *stubCodeRepoClient) ReadFile(_ context.Context, _, _ string) ([]byte, error) {
	return nil, nil
}
func (s *stubCodeRepoClient) ListFiles(_ context.Context, _, _ string) ([]string, error) {
	return nil, nil
}
func (s *stubCodeRepoClient) Head(_ context.Context) (string, error)    { return "abc123", nil }
func (s *stubCodeRepoClient) Push(_ context.Context, _, _ string) error { return nil }
func (s *stubCodeRepoClient) PushBranch(_ context.Context, remote, branch string) error {
	s.pushCalls = append(s.pushCalls, pushCall{remote: remote, branch: branch})
	return s.pushErr
}
func (s *stubCodeRepoClient) DeleteRemoteBranch(_ context.Context, remote, branch string) error {
	s.deleteRemoteCalls = append(s.deleteRemoteCalls, pushCall{remote: remote, branch: branch})
	return nil
}

// stubRepoGitClients fakes the gitpool.Pool. Maps repository ID to a
// stubCodeRepoClient so each test can assert per-repo branch traffic.
type stubRepoGitClients struct {
	clients map[string]*stubCodeRepoClient
	err     error
}

func (s *stubRepoGitClients) Client(_ context.Context, repoID string) (git.GitClient, error) {
	if s.err != nil {
		return nil, s.err
	}
	c, ok := s.clients[repoID]
	if !ok {
		return nil, errors.New("test misconfigured: no client for " + repoID)
	}
	return c, nil
}

func newStubRepoGitClients(repoIDs ...string) *stubRepoGitClients {
	clients := make(map[string]*stubCodeRepoClient, len(repoIDs))
	for _, id := range repoIDs {
		clients[id] = &stubCodeRepoClient{repoID: id}
	}
	return &stubRepoGitClients{clients: clients}
}

func multiRepoOrchestrator(t *testing.T, art *domain.Artifact, resolver RepositoryResolver, clients RepositoryGitClients, gitOp GitOperator) (*Orchestrator, *mockRunStore) {
	t.Helper()
	wfRes := &mockWorkflowResolver{
		result: &workflow.BindingResult{
			Workflow: &domain.WorkflowDefinition{
				ID:        "wf-task",
				Path:      "workflows/task.yaml",
				Version:   "1.0.0",
				EntryStep: "start",
				Steps:     []domain.StepDefinition{{ID: "start", Name: "Start"}},
			},
			CommitSHA:    "abc123",
			VersionLabel: "1.0.0",
		},
	}
	store := &mockRunStore{}
	if gitOp == nil {
		gitOp = &trackingGitOperator{}
	}
	orch := &Orchestrator{
		workflows: wfRes,
		store:     store,
		actors:    &stubActorAssigner{},
		artifacts: &mockArtifactReader{artifact: art},
		events:    &mockEventEmitter{},
		git:       gitOp,
		wfLoader:  &stubWorkflowLoader{},
	}
	if resolver != nil {
		orch.WithRepositoryResolver(resolver)
	}
	if clients != nil {
		orch.WithRepositoryGitClients(clients)
	}
	return orch, store
}

func activeRepoLookup(id, defaultBranch string) repoLookup {
	return repoLookup{repo: &repository.Repository{
		ID:            id,
		Status:        "active",
		DefaultBranch: defaultBranch,
	}}
}

// TestStartRun_MultiRepoCreatesBranchPerRepo is the AC anchor:
// "A multi-repo task creates branches in all affected repos."
// "Branch creation uses each repo's default branch."
func TestStartRun_MultiRepoCreatesBranchPerRepo(t *testing.T) {
	art := taskWithRepos([]string{"payments-service", "api-gateway"})
	resolver := newRepoResolver(map[string]repoLookup{
		"payments-service": activeRepoLookup("payments-service", "main"),
		"api-gateway":      activeRepoLookup("api-gateway", "trunk"),
	})
	clients := newStubRepoGitClients("payments-service", "api-gateway")
	orch, store := multiRepoOrchestrator(t, art, resolver, clients, nil)

	result, err := orch.StartRun(context.Background(), art.Path)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	want := []string{domain.PrimaryRepositoryID, "payments-service", "api-gateway"}
	if !reflect.DeepEqual(result.Run.AffectedRepositories, want) {
		t.Errorf("AffectedRepositories: got %v, want %v", result.Run.AffectedRepositories, want)
	}

	branchName := result.Run.BranchName
	pmt := clients.clients["payments-service"]
	if len(pmt.createCalls) != 1 {
		t.Fatalf("payments-service: expected 1 CreateBranch, got %d", len(pmt.createCalls))
	}
	if pmt.createCalls[0] != (branchCall{name: branchName, base: "main"}) {
		t.Errorf("payments-service CreateBranch: got %+v, want {name:%s base:main}", pmt.createCalls[0], branchName)
	}

	api := clients.clients["api-gateway"]
	if len(api.createCalls) != 1 {
		t.Fatalf("api-gateway: expected 1 CreateBranch, got %d", len(api.createCalls))
	}
	if api.createCalls[0] != (branchCall{name: branchName, base: "trunk"}) {
		t.Errorf("api-gateway CreateBranch: got %+v, want {name:%s base:trunk}", api.createCalls[0], branchName)
	}

	// RepositoryBranches stays nil today — the map is reserved for
	// divergent-state tracking (different branch name per repo),
	// which is not what TASK-002 produces. Future recovery / per-repo
	// divergence will populate it.
	if result.Run.RepositoryBranches != nil {
		t.Errorf("RepositoryBranches: got %v, want nil for non-divergent run",
			result.Run.RepositoryBranches)
	}

	if store.createdRun == nil {
		t.Error("expected run persisted after successful multi-repo branching")
	}
}

// TestStartRun_SingleRepoTaskCreatesOneBranch confirms backward
// compatibility: a task with no repositories field still creates only
// the primary branch even when the multi-repo wiring is present.
// AC: "Single-repo tasks still create only one branch."
func TestStartRun_SingleRepoTaskCreatesOneBranch(t *testing.T) {
	art := taskWithRepos(nil)
	resolver := newRepoResolver(map[string]repoLookup{
		"unused": activeRepoLookup("unused", "main"),
	})
	clients := newStubRepoGitClients("unused")
	orch, store := multiRepoOrchestrator(t, art, resolver, clients, nil)

	if _, err := orch.StartRun(context.Background(), art.Path); err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	if c := clients.clients["unused"]; len(c.createCalls) != 0 {
		t.Errorf("expected no per-repo branch traffic for primary-only task, got %v", c.createCalls)
	}
	if store.createdRun == nil {
		t.Fatal("expected run persisted")
	}
	if len(store.createdRun.AffectedRepositories) != 1 {
		t.Errorf("AffectedRepositories: got %v, want primary-only", store.createdRun.AffectedRepositories)
	}
}

// TestStartRun_MultiRepoBranchFailureNamesRepository covers AC:
// "Errors include the failing repository ID."
// And verifies that the run is not persisted on failure (the
// per-repo branch is created BEFORE store.CreateRun, matching the
// existing primary-branch invariant).
func TestStartRun_MultiRepoBranchFailureNamesRepository(t *testing.T) {
	art := taskWithRepos([]string{"payments-service", "api-gateway"})
	resolver := newRepoResolver(map[string]repoLookup{
		"payments-service": activeRepoLookup("payments-service", "main"),
		"api-gateway":      activeRepoLookup("api-gateway", "trunk"),
	})
	clients := newStubRepoGitClients("payments-service", "api-gateway")
	clients.clients["api-gateway"].createErr = errors.New("ref already exists")
	orch, store := multiRepoOrchestrator(t, art, resolver, clients, nil)

	_, err := orch.StartRun(context.Background(), art.Path)
	if err == nil {
		t.Fatal("expected error from failing branch creation")
	}
	if msg := err.Error(); !contains(msg, "api-gateway") {
		t.Errorf("error must name the failing repository, got %q", msg)
	}
	if store.createdRun != nil {
		t.Error("run must not be persisted when multi-repo branch creation fails")
	}

	// Already-created branches are rolled back: payments-service
	// (succeeded) and the primary branch get a DeleteBranch.
	pmt := clients.clients["payments-service"]
	if len(pmt.deleteCalls) != 1 {
		t.Errorf("payments-service: expected 1 rollback DeleteBranch, got %d", len(pmt.deleteCalls))
	}
}

// TestStartRun_MultiRepoMissingDefaultBranchFails ensures a
// misconfigured catalog (binding without default_branch) is reported as
// a precondition failure naming the offending repo, not a Git CLI error.
func TestStartRun_MultiRepoMissingDefaultBranchFails(t *testing.T) {
	art := taskWithRepos([]string{"payments-service"})
	resolver := newRepoResolver(map[string]repoLookup{
		// DefaultBranch left empty.
		"payments-service": {repo: &repository.Repository{ID: "payments-service", Status: "active"}},
	})
	clients := newStubRepoGitClients("payments-service")
	orch, _ := multiRepoOrchestrator(t, art, resolver, clients, nil)

	_, err := orch.StartRun(context.Background(), art.Path)
	if err == nil {
		t.Fatal("expected error for missing default_branch")
	}
	if msg := err.Error(); !contains(msg, "payments-service") || !contains(msg, "default_branch") {
		t.Errorf("error must reference repo and default_branch, got %q", msg)
	}
}

// TestStartRun_AutoPushPushesEachAffectedRepo covers AC:
// "Auto-push behavior applies per affected repo when enabled."
func TestStartRun_AutoPushPushesEachAffectedRepo(t *testing.T) {
	t.Setenv("SPINE_GIT_PUSH_ENABLED", "true")

	art := taskWithRepos([]string{"payments-service"})
	resolver := newRepoResolver(map[string]repoLookup{
		"payments-service": activeRepoLookup("payments-service", "main"),
	})
	clients := newStubRepoGitClients("payments-service")
	orch, _ := multiRepoOrchestrator(t, art, resolver, clients, nil)

	result, err := orch.StartRun(context.Background(), art.Path)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	pmt := clients.clients["payments-service"]
	if len(pmt.pushCalls) != 1 {
		t.Fatalf("payments-service: expected 1 PushBranch, got %d", len(pmt.pushCalls))
	}
	if pmt.pushCalls[0] != (pushCall{remote: "origin", branch: result.Run.BranchName}) {
		t.Errorf("payments-service PushBranch: got %+v, want {remote:origin branch:%s}", pmt.pushCalls[0], result.Run.BranchName)
	}
}

// TestStartRun_AutoPushDisabledSkipsPerRepoPush asserts that a
// SPINE_GIT_PUSH_ENABLED=false override skips push on every affected
// repo, not just the primary — symmetric with the legacy single-repo
// behavior.
func TestStartRun_AutoPushDisabledSkipsPerRepoPush(t *testing.T) {
	t.Setenv("SPINE_GIT_PUSH_ENABLED", "false")

	art := taskWithRepos([]string{"payments-service"})
	resolver := newRepoResolver(map[string]repoLookup{
		"payments-service": activeRepoLookup("payments-service", "main"),
	})
	clients := newStubRepoGitClients("payments-service")
	orch, _ := multiRepoOrchestrator(t, art, resolver, clients, nil)

	if _, err := orch.StartRun(context.Background(), art.Path); err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	if pushes := clients.clients["payments-service"].pushCalls; len(pushes) != 0 {
		t.Errorf("expected no per-repo push when auto-push disabled, got %v", pushes)
	}
}

// TestCleanupRunBranch_DeletesEveryAffectedRepo verifies multi-repo
// runs clean up symmetric with creation: every non-primary affected
// repo gets a DeleteBranch (and DeleteRemoteBranch when auto-push is
// on). Without this, multi-repo runs leak code-repo refs forever.
func TestCleanupRunBranch_DeletesEveryAffectedRepo(t *testing.T) {
	t.Setenv("SPINE_GIT_PUSH_ENABLED", "true")

	resolver := newRepoResolver(map[string]repoLookup{
		"payments-service": activeRepoLookup("payments-service", "main"),
		"api-gateway":      activeRepoLookup("api-gateway", "trunk"),
	})
	clients := newStubRepoGitClients("payments-service", "api-gateway")

	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {
				RunID:                "run-1",
				BranchName:           "spine/run/multi",
				AffectedRepositories: []string{"spine", "payments-service", "api-gateway"},
				PrimaryRepository:    true,
				TraceID:              "trace-1234567890ab",
			},
		},
	}
	gitOp := &cleanupTrackingGitOperator{}
	orch := &Orchestrator{
		workflows: &mockWorkflowResolver{},
		store:     store,
		actors:    &stubActorAssigner{},
		artifacts: &mockArtifactReader{},
		events:    &mockEventEmitter{},
		git:       gitOp,
		wfLoader:  &stubWorkflowLoader{},
	}
	orch.WithRepositoryResolver(resolver)
	orch.WithRepositoryGitClients(clients)

	if err := orch.CleanupRunBranch(context.Background(), "run-1"); err != nil {
		t.Fatalf("CleanupRunBranch: %v", err)
	}

	if !gitOp.localDeleted || !gitOp.remoteDeleted {
		t.Errorf("primary cleanup: local=%v remote=%v", gitOp.localDeleted, gitOp.remoteDeleted)
	}

	pmt := clients.clients["payments-service"]
	if len(pmt.deleteCalls) != 1 || pmt.deleteCalls[0] != "spine/run/multi" {
		t.Errorf("payments-service local delete: got %v", pmt.deleteCalls)
	}

	api := clients.clients["api-gateway"]
	if len(api.deleteCalls) != 1 || api.deleteCalls[0] != "spine/run/multi" {
		t.Errorf("api-gateway local delete: got %v", api.deleteCalls)
	}
}

// cleanupTrackingGitOperator is a primary-repo GitOperator that
// records local and remote branch deletions so the multi-repo cleanup
// test can assert the primary side too.
type cleanupTrackingGitOperator struct {
	stubGitOperator
	localDeleted  bool
	remoteDeleted bool
}

func (g *cleanupTrackingGitOperator) DeleteBranch(_ context.Context, _ string) error {
	g.localDeleted = true
	return nil
}

func (g *cleanupTrackingGitOperator) DeleteRemoteBranch(_ context.Context, _, _ string) error {
	g.remoteDeleted = true
	return nil
}

// rollbackTrackingGitOperator records every CreateBranch / DeleteBranch
// / PushBranch / DeleteRemoteBranch call against the primary repo so
// failure-position tests can assert exactly which refs were created
// and which were torn down. Optional createErr triggers a primary-side
// failure for tests that need it.
type rollbackTrackingGitOperator struct {
	stubGitOperator
	createCalls       []branchCall
	pushCalls         []pushCall
	deleteLocalCalls  []string
	deleteRemoteCalls []pushCall
	deleteLocalErr    error

	createErr error
}

func (g *rollbackTrackingGitOperator) CreateBranch(_ context.Context, name, base string) error {
	g.createCalls = append(g.createCalls, branchCall{name: name, base: base})
	return g.createErr
}

func (g *rollbackTrackingGitOperator) PushBranch(_ context.Context, remote, branch string) error {
	g.pushCalls = append(g.pushCalls, pushCall{remote: remote, branch: branch})
	return nil
}

func (g *rollbackTrackingGitOperator) DeleteBranch(_ context.Context, name string) error {
	g.deleteLocalCalls = append(g.deleteLocalCalls, name)
	return g.deleteLocalErr
}

func (g *rollbackTrackingGitOperator) DeleteRemoteBranch(_ context.Context, remote, branch string) error {
	g.deleteRemoteCalls = append(g.deleteRemoteCalls, pushCall{remote: remote, branch: branch})
	return nil
}

// TestStartRun_MultiRepoFailFirstCodeRepo covers AC: cleanup runs for
// every repo whose branch was already created. With three affected
// repos (spine + A + B) and A's CreateBranch failing, only the primary
// has a local+remote ref to roll back; A is never created, B is never
// touched.
func TestStartRun_MultiRepoFailFirstCodeRepo(t *testing.T) {
	t.Setenv("SPINE_GIT_PUSH_ENABLED", "true")

	art := taskWithRepos([]string{"alpha", "bravo"})
	resolver := newRepoResolver(map[string]repoLookup{
		"alpha": activeRepoLookup("alpha", "main"),
		"bravo": activeRepoLookup("bravo", "main"),
	})
	clients := newStubRepoGitClients("alpha", "bravo")
	clients.clients["alpha"].createErr = errors.New("ref already exists")
	gitOp := &rollbackTrackingGitOperator{}
	orch, store := multiRepoOrchestrator(t, art, resolver, clients, gitOp)

	_, err := orch.StartRun(context.Background(), art.Path)
	if err == nil {
		t.Fatal("expected error from first code repo failure")
	}
	if !contains(err.Error(), "alpha") {
		t.Errorf("error must name the failing repo, got %q", err.Error())
	}
	if store.createdRun != nil {
		t.Error("run must not be persisted when branch creation fails")
	}

	// Primary: created and pushed, then rolled back local + remote.
	if len(gitOp.createCalls) != 1 {
		t.Errorf("primary CreateBranch: got %d calls, want 1", len(gitOp.createCalls))
	}
	if len(gitOp.pushCalls) != 1 {
		t.Errorf("primary PushBranch: got %d calls, want 1", len(gitOp.pushCalls))
	}
	if len(gitOp.deleteLocalCalls) != 1 {
		t.Errorf("primary DeleteBranch: got %d calls, want 1", len(gitOp.deleteLocalCalls))
	}
	if len(gitOp.deleteRemoteCalls) != 1 {
		t.Errorf("primary DeleteRemoteBranch: got %d calls, want 1", len(gitOp.deleteRemoteCalls))
	}

	// Alpha: CreateBranch attempted, failed; never pushed, never rolled back.
	alpha := clients.clients["alpha"]
	if len(alpha.createCalls) != 1 {
		t.Errorf("alpha CreateBranch: got %d, want 1", len(alpha.createCalls))
	}
	if len(alpha.pushCalls) != 0 {
		t.Errorf("alpha PushBranch: got %d, want 0 (create failed)", len(alpha.pushCalls))
	}
	if len(alpha.deleteCalls) != 0 {
		t.Errorf("alpha DeleteBranch: got %d, want 0 (never created)", len(alpha.deleteCalls))
	}
	if len(alpha.deleteRemoteCalls) != 0 {
		t.Errorf("alpha DeleteRemoteBranch: got %d, want 0", len(alpha.deleteRemoteCalls))
	}

	// Bravo: never touched.
	bravo := clients.clients["bravo"]
	if len(bravo.createCalls)+len(bravo.pushCalls)+len(bravo.deleteCalls)+len(bravo.deleteRemoteCalls) != 0 {
		t.Errorf("bravo: expected no traffic, got create=%d push=%d delete=%d deleteRemote=%d",
			len(bravo.createCalls), len(bravo.pushCalls), len(bravo.deleteCalls), len(bravo.deleteRemoteCalls))
	}
}

// TestStartRun_MultiRepoFailMiddleCodeRepo locks in "rollback runs for
// EVERY prior repo, not just the immediately-previous one." With
// spine + A + B + C and B's CreateBranch failing, primary AND A get
// local+remote rollback. B is not rolled back (never created). C is
// never touched.
func TestStartRun_MultiRepoFailMiddleCodeRepo(t *testing.T) {
	t.Setenv("SPINE_GIT_PUSH_ENABLED", "true")

	art := taskWithRepos([]string{"alpha", "bravo", "charlie"})
	resolver := newRepoResolver(map[string]repoLookup{
		"alpha":   activeRepoLookup("alpha", "main"),
		"bravo":   activeRepoLookup("bravo", "main"),
		"charlie": activeRepoLookup("charlie", "main"),
	})
	clients := newStubRepoGitClients("alpha", "bravo", "charlie")
	clients.clients["bravo"].createErr = errors.New("permission denied")
	gitOp := &rollbackTrackingGitOperator{}
	orch, store := multiRepoOrchestrator(t, art, resolver, clients, gitOp)

	_, err := orch.StartRun(context.Background(), art.Path)
	if err == nil {
		t.Fatal("expected error from middle code repo failure")
	}
	if !contains(err.Error(), "bravo") {
		t.Errorf("error must name the failing repo, got %q", err.Error())
	}
	if store.createdRun != nil {
		t.Error("run must not be persisted when branch creation fails")
	}

	// Primary rolled back local+remote.
	if len(gitOp.deleteLocalCalls) != 1 || len(gitOp.deleteRemoteCalls) != 1 {
		t.Errorf("primary rollback: deleteLocal=%d deleteRemote=%d, want 1+1",
			len(gitOp.deleteLocalCalls), len(gitOp.deleteRemoteCalls))
	}

	// Alpha succeeded → rolled back local+remote.
	alpha := clients.clients["alpha"]
	if len(alpha.deleteCalls) != 1 || len(alpha.deleteRemoteCalls) != 1 {
		t.Errorf("alpha rollback: delete=%d deleteRemote=%d, want 1+1",
			len(alpha.deleteCalls), len(alpha.deleteRemoteCalls))
	}

	// Bravo failed during create → not rolled back.
	bravo := clients.clients["bravo"]
	if len(bravo.deleteCalls) != 0 || len(bravo.deleteRemoteCalls) != 0 {
		t.Errorf("bravo rollback: delete=%d deleteRemote=%d, want 0+0",
			len(bravo.deleteCalls), len(bravo.deleteRemoteCalls))
	}

	// Charlie never reached.
	charlie := clients.clients["charlie"]
	if len(charlie.createCalls) != 0 {
		t.Errorf("charlie: expected no CreateBranch, got %d", len(charlie.createCalls))
	}
}

// TestStartRun_MultiRepoFailLastCodeRepo covers the position where
// every prior repo (primary + N-1 code repos) needs full rollback —
// the largest amount of state to clean up.
func TestStartRun_MultiRepoFailLastCodeRepo(t *testing.T) {
	t.Setenv("SPINE_GIT_PUSH_ENABLED", "true")

	art := taskWithRepos([]string{"alpha", "bravo"})
	resolver := newRepoResolver(map[string]repoLookup{
		"alpha": activeRepoLookup("alpha", "main"),
		"bravo": activeRepoLookup("bravo", "main"),
	})
	clients := newStubRepoGitClients("alpha", "bravo")
	clients.clients["bravo"].createErr = errors.New("upstream timeout")
	gitOp := &rollbackTrackingGitOperator{}
	orch, store := multiRepoOrchestrator(t, art, resolver, clients, gitOp)

	_, err := orch.StartRun(context.Background(), art.Path)
	if err == nil {
		t.Fatal("expected error from last code repo failure")
	}
	if !contains(err.Error(), "bravo") {
		t.Errorf("error must name the failing repo, got %q", err.Error())
	}
	if store.createdRun != nil {
		t.Error("run must not be persisted when branch creation fails")
	}

	// Primary + alpha both rolled back local+remote.
	if len(gitOp.deleteLocalCalls) != 1 || len(gitOp.deleteRemoteCalls) != 1 {
		t.Errorf("primary rollback: deleteLocal=%d deleteRemote=%d, want 1+1",
			len(gitOp.deleteLocalCalls), len(gitOp.deleteRemoteCalls))
	}
	alpha := clients.clients["alpha"]
	if len(alpha.deleteCalls) != 1 || len(alpha.deleteRemoteCalls) != 1 {
		t.Errorf("alpha rollback: delete=%d deleteRemote=%d, want 1+1",
			len(alpha.deleteCalls), len(alpha.deleteRemoteCalls))
	}
}

// TestStartRun_StoreFailureRollsBackEverything: when all branches are
// created successfully but the run record itself fails to persist,
// rollback covers every local AND remote ref — symmetric with the
// per-repo failure paths.
func TestStartRun_StoreFailureRollsBackEverything(t *testing.T) {
	t.Setenv("SPINE_GIT_PUSH_ENABLED", "true")

	art := taskWithRepos([]string{"alpha", "bravo"})
	resolver := newRepoResolver(map[string]repoLookup{
		"alpha": activeRepoLookup("alpha", "main"),
		"bravo": activeRepoLookup("bravo", "main"),
	})
	clients := newStubRepoGitClients("alpha", "bravo")
	gitOp := &rollbackTrackingGitOperator{}
	orch, store := multiRepoOrchestrator(t, art, resolver, clients, gitOp)
	store.createRunErr = errors.New("conflict on run_id")

	_, err := orch.StartRun(context.Background(), art.Path)
	if err == nil {
		t.Fatal("expected error from store.CreateRun")
	}
	if store.createdRun != nil {
		t.Error("run must not be retained when CreateRun fails")
	}

	// Primary rolled back local+remote.
	if len(gitOp.deleteLocalCalls) != 1 || len(gitOp.deleteRemoteCalls) != 1 {
		t.Errorf("primary rollback after store failure: deleteLocal=%d deleteRemote=%d, want 1+1",
			len(gitOp.deleteLocalCalls), len(gitOp.deleteRemoteCalls))
	}
	for _, id := range []string{"alpha", "bravo"} {
		c := clients.clients[id]
		if len(c.deleteCalls) != 1 || len(c.deleteRemoteCalls) != 1 {
			t.Errorf("%s rollback after store failure: delete=%d deleteRemote=%d, want 1+1",
				id, len(c.deleteCalls), len(c.deleteRemoteCalls))
		}
	}
}

// TestStartRun_MultiRepoFailWithoutAutoPushSkipsRemoteCleanup pins the
// guard: when auto-push is disabled, no remote refs were created, so
// rollback must never call DeleteRemoteBranch — it would log spurious
// "ref does not exist" warnings against an unrelated remote and hide
// real failures. With the auto-push toggle off, only local cleanup
// should run.
func TestStartRun_MultiRepoFailWithoutAutoPushSkipsRemoteCleanup(t *testing.T) {
	t.Setenv("SPINE_GIT_PUSH_ENABLED", "false")

	art := taskWithRepos([]string{"alpha", "bravo"})
	resolver := newRepoResolver(map[string]repoLookup{
		"alpha": activeRepoLookup("alpha", "main"),
		"bravo": activeRepoLookup("bravo", "main"),
	})
	clients := newStubRepoGitClients("alpha", "bravo")
	clients.clients["bravo"].createErr = errors.New("ref already exists")
	gitOp := &rollbackTrackingGitOperator{}
	orch, _ := multiRepoOrchestrator(t, art, resolver, clients, gitOp)

	if _, err := orch.StartRun(context.Background(), art.Path); err == nil {
		t.Fatal("expected error from bravo failure")
	}

	if len(gitOp.deleteLocalCalls) != 1 {
		t.Errorf("primary local rollback: got %d, want 1", len(gitOp.deleteLocalCalls))
	}
	if len(gitOp.deleteRemoteCalls) != 0 {
		t.Errorf("primary remote rollback: got %d, want 0 (no auto-push, no remote ref)",
			len(gitOp.deleteRemoteCalls))
	}
	if alpha := clients.clients["alpha"]; len(alpha.deleteRemoteCalls) != 0 {
		t.Errorf("alpha remote rollback: got %d, want 0", len(alpha.deleteRemoteCalls))
	}
}

// TestStartRun_RollbackErrorDoesNotMaskOriginalFailure pins AC:
// "Cleanup errors are logged without hiding the original startup
// failure." A DeleteBranch error during rollback must not surface as
// the user-visible error — the original repo-create failure does.
func TestStartRun_RollbackErrorDoesNotMaskOriginalFailure(t *testing.T) {
	t.Setenv("SPINE_GIT_PUSH_ENABLED", "false")

	art := taskWithRepos([]string{"alpha"})
	resolver := newRepoResolver(map[string]repoLookup{
		"alpha": activeRepoLookup("alpha", "main"),
	})
	clients := newStubRepoGitClients("alpha")
	clients.clients["alpha"].createErr = errors.New("upstream timeout")
	gitOp := &rollbackTrackingGitOperator{deleteLocalErr: errors.New("delete failed: branch checked out")}
	orch, _ := multiRepoOrchestrator(t, art, resolver, clients, gitOp)

	_, err := orch.StartRun(context.Background(), art.Path)
	if err == nil {
		t.Fatal("expected error from alpha failure")
	}
	if !contains(err.Error(), "alpha") || !contains(err.Error(), "upstream timeout") {
		t.Errorf("error must name original failure, got %q", err.Error())
	}
	if contains(err.Error(), "delete failed") {
		t.Errorf("rollback error must not mask original failure, got %q", err.Error())
	}
	// The rollback was still attempted — verifies "best-effort, not
	// abort-on-error". The caller observed only the original error.
	if len(gitOp.deleteLocalCalls) != 1 {
		t.Errorf("primary rollback should still be attempted, got %d calls", len(gitOp.deleteLocalCalls))
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
