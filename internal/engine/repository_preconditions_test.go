package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/repository"
	"github.com/bszymi/spine/internal/workflow"
)

// stubRepositoryResolver fakes the registry. lookups maps repository
// IDs to either an active Repository (success) or a sentinel error.
type stubRepositoryResolver struct {
	lookups map[string]repoLookup
}

type repoLookup struct {
	repo *repository.Repository
	err  error
}

func (s *stubRepositoryResolver) Lookup(_ context.Context, id string) (*repository.Repository, error) {
	if r, ok := s.lookups[id]; ok {
		return r.repo, r.err
	}
	return nil, errors.New("test misconfigured: no entry for " + id)
}

func newRepoResolver(entries map[string]repoLookup) *stubRepositoryResolver {
	return &stubRepositoryResolver{lookups: entries}
}

func taskWithRepos(repos []string) *domain.Artifact {
	return &domain.Artifact{
		Type:         "task",
		Path:         "tasks/my-task.md",
		Repositories: repos,
	}
}

func startRunOrchestrator(t *testing.T, art *domain.Artifact, resolver RepositoryResolver) (*Orchestrator, *mockRunStore, *trackingGitOperator) {
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
	git := &trackingGitOperator{}
	orch := &Orchestrator{
		workflows: wfRes,
		store:     store,
		actors:    &stubActorAssigner{},
		artifacts: &mockArtifactReader{artifact: art},
		events:    &mockEventEmitter{},
		git:       git,
		wfLoader:  &stubWorkflowLoader{},
	}
	if resolver != nil {
		orch.WithRepositoryResolver(resolver)
	}
	// When the task declares non-primary repos, the orchestrator
	// requires per-repo client wiring as well (TASK-002). Tests that
	// stub a resolver but want to keep the run-start path on the
	// happy line need a stub client too — wire one for every active
	// lookup the resolver knows about.
	if resolver != nil {
		if stub, ok := resolverWithLookups(resolver); ok {
			clients := newStubRepoGitClients()
			for id, lookup := range stub.lookups {
				if lookup.repo != nil && id != "spine" {
					clients.clients[id] = &stubCodeRepoClient{repoID: id}
				}
			}
			if len(clients.clients) > 0 {
				orch.WithRepositoryGitClients(clients)
			}
		}
	}
	return orch, store, git
}

// resolverWithLookups unwraps a resolver to its underlying
// stubRepositoryResolver if possible. Recordingresolver embeds the
// stub, so this returns the inner map without re-listing entries.
func resolverWithLookups(r RepositoryResolver) (*stubRepositoryResolver, bool) {
	switch v := r.(type) {
	case *stubRepositoryResolver:
		return v, true
	case *recordingResolver:
		return &v.stubRepositoryResolver, true
	}
	return nil, false
}

// trackingGitOperator records branch-creation calls so tests can
// assert that a precondition failure short-circuited before Git was
// touched.
type trackingGitOperator struct {
	stubGitOperator
	createBranchCalled bool
}

func (g *trackingGitOperator) CreateBranch(_ context.Context, _, _ string) error {
	g.createBranchCalled = true
	return nil
}

func TestRepoPrecondition_NoResolverPrimaryOnlyStillStarts(t *testing.T) {
	// No resolver wired and no code repos declared — the legacy
	// primary-only run path still works without any registry wiring.
	art := taskWithRepos(nil)
	orch, store, git := startRunOrchestrator(t, art, nil)

	_, err := orch.StartRun(context.Background(), art.Path)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if !git.createBranchCalled {
		t.Error("expected branch creation when resolver is nil")
	}
	if store.createdRun == nil {
		t.Error("expected run created when resolver is nil")
	}
}

// TestRepoPrecondition_NoResolverWithCodeReposFails locks in the
// TASK-002 invariant: a task that declares code repos cannot start
// without per-repo wiring. Silently degrading to primary-only would
// persist the run with phantom AffectedRepositories — branches
// listed but never created — and break downstream cleanup.
func TestRepoPrecondition_NoResolverWithCodeReposFails(t *testing.T) {
	art := taskWithRepos([]string{"payments-service"})
	orch, store, git := startRunOrchestrator(t, art, nil)

	_, err := orch.StartRun(context.Background(), art.Path)
	if err == nil {
		t.Fatal("expected error when code repos declared without per-repo wiring")
	}
	if git.createBranchCalled {
		t.Error("primary branch must not be created when wiring is missing")
	}
	if store.createdRun != nil {
		t.Error("run must not be persisted when wiring is missing")
	}
}

func TestRepoPrecondition_MissingReposStartsPrimaryOnly(t *testing.T) {
	// The task declares no repositories — the primary-only run must
	// start without consulting the registry at all (we wire a resolver
	// that would fail to prove it isn't called).
	art := taskWithRepos(nil)
	resolver := newRepoResolver(map[string]repoLookup{
		"unused": {err: errors.New("must not be called")},
	})
	orch, store, git := startRunOrchestrator(t, art, resolver)

	_, err := orch.StartRun(context.Background(), art.Path)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if !git.createBranchCalled {
		t.Error("expected branch creation for primary-only run")
	}
	if store.createdRun == nil {
		t.Error("expected run created for primary-only run")
	}
}

func TestRepoPrecondition_AllActiveSucceeds(t *testing.T) {
	art := taskWithRepos([]string{"spine", "payments-service"})
	resolver := newRepoResolver(map[string]repoLookup{
		"spine":            {repo: &repository.Repository{ID: "spine", Status: "active", DefaultBranch: "main"}},
		"payments-service": {repo: &repository.Repository{ID: "payments-service", Status: "active", DefaultBranch: "main"}},
	})
	orch, store, git := startRunOrchestrator(t, art, resolver)

	_, err := orch.StartRun(context.Background(), art.Path)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if !git.createBranchCalled {
		t.Error("expected branch creation when all repos resolve active")
	}
	if store.createdRun == nil {
		t.Error("expected run created when all repos resolve active")
	}
}

func TestRepoPrecondition_NotFoundFailsBeforeBranch(t *testing.T) {
	art := taskWithRepos([]string{"ghost-service"})
	notFound := domain.NewErrorWithCause(domain.ErrNotFound, "no", repository.ErrRepositoryNotFound)
	resolver := newRepoResolver(map[string]repoLookup{
		"ghost-service": {err: notFound},
	})
	orch, store, git := startRunOrchestrator(t, art, resolver)

	_, err := orch.StartRun(context.Background(), art.Path)
	if err == nil {
		t.Fatal("expected error for unknown repository")
	}
	assertPreconditionFailure(t, err, "ghost-service", repoPreconditionNotFound)
	if !errors.Is(err, repository.ErrRepositoryNotFound) {
		t.Error("error must wrap ErrRepositoryNotFound")
	}
	if git.createBranchCalled {
		t.Error("branch must not be created when precondition fails")
	}
	if store.createdRun != nil {
		t.Error("run must not be persisted when precondition fails")
	}
}

func TestRepoPrecondition_UnboundFailsBeforeBranch(t *testing.T) {
	art := taskWithRepos([]string{"payments-service"})
	unbound := domain.NewErrorWithCause(domain.ErrPrecondition, "no", repository.ErrRepositoryUnbound)
	resolver := newRepoResolver(map[string]repoLookup{
		"payments-service": {err: unbound},
	})
	orch, _, git := startRunOrchestrator(t, art, resolver)

	_, err := orch.StartRun(context.Background(), art.Path)
	if err == nil {
		t.Fatal("expected error for unbound repository")
	}
	assertPreconditionFailure(t, err, "payments-service", repoPreconditionUnbound)
	if !errors.Is(err, repository.ErrRepositoryUnbound) {
		t.Error("error must wrap ErrRepositoryUnbound")
	}
	if git.createBranchCalled {
		t.Error("branch must not be created when precondition fails")
	}
}

func TestRepoPrecondition_InactiveFailsBeforeBranch(t *testing.T) {
	art := taskWithRepos([]string{"payments-service"})
	inactive := domain.NewErrorWithCause(domain.ErrPrecondition, "no", repository.ErrRepositoryInactive)
	resolver := newRepoResolver(map[string]repoLookup{
		"payments-service": {err: inactive},
	})
	orch, _, git := startRunOrchestrator(t, art, resolver)

	_, err := orch.StartRun(context.Background(), art.Path)
	if err == nil {
		t.Fatal("expected error for inactive repository")
	}
	assertPreconditionFailure(t, err, "payments-service", repoPreconditionInactive)
	if !errors.Is(err, repository.ErrRepositoryInactive) {
		t.Error("error must wrap ErrRepositoryInactive")
	}
	if git.createBranchCalled {
		t.Error("branch must not be created when precondition fails")
	}
}

func TestRepoPrecondition_InternalErrorCategorized(t *testing.T) {
	// A non-sentinel error (e.g. clone or credential resolution failure
	// surfaced from a deeper provider) is reported with category
	// "internal" so callers can distinguish it from the typed sentinels.
	art := taskWithRepos([]string{"payments-service"})
	internal := errors.New("clone failed: connection refused")
	resolver := newRepoResolver(map[string]repoLookup{
		"payments-service": {err: internal},
	})
	orch, _, _ := startRunOrchestrator(t, art, resolver)

	_, err := orch.StartRun(context.Background(), art.Path)
	if err == nil {
		t.Fatal("expected error from underlying provider")
	}
	assertPreconditionFailure(t, err, "payments-service", repoPreconditionInternal)
}

// recordingResolver wraps stubRepositoryResolver to capture the
// sequence of Lookup calls. The order matters — the precondition
// check is documented to walk the artifact's repositories slice in
// order, and dashboards/log readers depend on that ordering.
type recordingResolver struct {
	stubRepositoryResolver
	calls []string
}

func (r *recordingResolver) Lookup(ctx context.Context, id string) (*repository.Repository, error) {
	r.calls = append(r.calls, id)
	return r.stubRepositoryResolver.Lookup(ctx, id)
}

// TestRepoPrecondition_MultiRepoScenarioStartsRun is the TASK-005
// "scenario test starts a run for a valid multi-repo task" anchor.
// It pins three guarantees in one place:
//   - every declared repository is consulted, in declaration order,
//   - the run is persisted (no precondition short-circuit),
//   - the run branch is created (no Git-side abort).
func TestRepoPrecondition_MultiRepoScenarioStartsRun(t *testing.T) {
	declared := []string{"spine", "payments-service", "api-gateway"}
	art := taskWithRepos(declared)
	resolver := &recordingResolver{
		stubRepositoryResolver: stubRepositoryResolver{
			lookups: map[string]repoLookup{
				"spine":            {repo: &repository.Repository{ID: "spine", Status: "active", DefaultBranch: "main"}},
				"payments-service": {repo: &repository.Repository{ID: "payments-service", Status: "active", DefaultBranch: "main"}},
				"api-gateway":      {repo: &repository.Repository{ID: "api-gateway", Status: "active", DefaultBranch: "main"}},
			},
		},
	}
	orch, store, git := startRunOrchestrator(t, art, resolver)

	if _, err := orch.StartRun(context.Background(), art.Path); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	// The precondition check walks every declared repository up front,
	// in declaration order. Branch creation (TASK-002) re-looks up
	// each non-primary repo for its default_branch, so additional
	// lookups appear after the prefix; the prefix is what the
	// dashboard contract relies on.
	if len(resolver.calls) < len(declared) || !equalSlice(resolver.calls[:len(declared)], declared) {
		t.Errorf("Lookup precondition prefix: got %v, want prefix %v", resolver.calls, declared)
	}
	if !git.createBranchCalled {
		t.Error("expected branch creation for valid multi-repo run")
	}
	if store.createdRun == nil {
		t.Error("expected run created for valid multi-repo task")
	}
}

func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRepoPrecondition_ShortCircuitsOnFirstFailure(t *testing.T) {
	// The check stops at the first failing ID so the error names that
	// repo (not a later one). Order of the slice matters for this test.
	art := taskWithRepos([]string{"payments-service", "api-gateway"})
	resolver := newRepoResolver(map[string]repoLookup{
		"payments-service": {repo: &repository.Repository{ID: "payments-service", Status: "active"}},
		"api-gateway":      {err: domain.NewErrorWithCause(domain.ErrNotFound, "no", repository.ErrRepositoryNotFound)},
	})
	orch, _, _ := startRunOrchestrator(t, art, resolver)

	_, err := orch.StartRun(context.Background(), art.Path)
	if err == nil {
		t.Fatal("expected error for unknown api-gateway")
	}
	assertPreconditionFailure(t, err, "api-gateway", repoPreconditionNotFound)
}

func assertPreconditionFailure(t *testing.T, err error, wantID, wantCategory string) {
	t.Helper()
	var spineErr *domain.SpineError
	if !errors.As(err, &spineErr) {
		t.Fatalf("expected SpineError, got %T: %v", err, err)
	}
	if spineErr.Code != domain.ErrPrecondition {
		t.Errorf("expected ErrPrecondition, got %s", spineErr.Code)
	}
	failure, ok := spineErr.Detail.(RepositoryPreconditionFailure)
	if !ok {
		t.Fatalf("expected detail RepositoryPreconditionFailure, got %T", spineErr.Detail)
	}
	if failure.RepositoryID != wantID {
		t.Errorf("RepositoryID: got %q, want %q", failure.RepositoryID, wantID)
	}
	if failure.Category != wantCategory {
		t.Errorf("Category: got %q, want %q", failure.Category, wantCategory)
	}
}
