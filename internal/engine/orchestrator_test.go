package engine

import (
	"context"
	"testing"

	"github.com/bszymi/spine/internal/actor"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/workflow"
)

// Minimal stubs that satisfy the orchestrator interfaces.

type stubWorkflowResolver struct{}

func (s *stubWorkflowResolver) ResolveWorkflow(_ context.Context, _, _ string) (*workflow.BindingResult, error) {
	return nil, nil
}

func (s *stubWorkflowResolver) ResolveWorkflowForMode(_ context.Context, _, _, _ string) (*workflow.BindingResult, error) {
	return nil, nil
}

type stubRunStore struct{}

func (s *stubRunStore) CreateRun(_ context.Context, _ *domain.Run) error { return nil }
func (s *stubRunStore) GetRun(_ context.Context, _ string) (*domain.Run, error) {
	return nil, nil
}
func (s *stubRunStore) UpdateRunStatus(_ context.Context, _ string, _ domain.RunStatus) error {
	return nil
}
func (s *stubRunStore) TransitionRunStatus(_ context.Context, _ string, _, _ domain.RunStatus) (bool, error) {
	return true, nil
}
func (s *stubRunStore) UpdateCurrentStep(_ context.Context, _, _ string) error { return nil }
func (s *stubRunStore) SetCommitMeta(_ context.Context, _ string, _ map[string]string) error {
	return nil
}
func (s *stubRunStore) CreateStepExecution(_ context.Context, _ *domain.StepExecution) error {
	return nil
}
func (s *stubRunStore) GetStepExecution(_ context.Context, _ string) (*domain.StepExecution, error) {
	return nil, nil
}
func (s *stubRunStore) UpdateStepExecution(_ context.Context, _ *domain.StepExecution) error {
	return nil
}
func (s *stubRunStore) ListStepExecutionsByRun(_ context.Context, _ string) ([]domain.StepExecution, error) {
	return nil, nil
}
func (s *stubRunStore) CreateDivergenceContext(_ context.Context, _ *domain.DivergenceContext) error {
	return nil
}
func (s *stubRunStore) UpdateDivergenceContext(_ context.Context, _ *domain.DivergenceContext) error {
	return nil
}
func (s *stubRunStore) GetDivergenceContext(_ context.Context, _ string) (*domain.DivergenceContext, error) {
	return nil, nil
}
func (s *stubRunStore) CreateBranch(_ context.Context, _ *domain.Branch) error { return nil }
func (s *stubRunStore) UpdateBranch(_ context.Context, _ *domain.Branch) error { return nil }
func (s *stubRunStore) GetBranch(_ context.Context, _ string) (*domain.Branch, error) {
	return nil, domain.NewError(domain.ErrNotFound, "branch not found")
}
func (s *stubRunStore) ListBranchesByDivergence(_ context.Context, _ string) ([]domain.Branch, error) {
	return nil, nil
}

type stubActorAssigner struct{}

func (s *stubActorAssigner) DeliverAssignment(_ context.Context, _ actor.AssignmentRequest) error {
	return nil
}
func (s *stubActorAssigner) ProcessResult(_ context.Context, _ actor.AssignmentRequest, _ actor.AssignmentResult) error {
	return nil
}

type stubArtifactReader struct{}

func (s *stubArtifactReader) Read(_ context.Context, _, _ string) (*domain.Artifact, error) {
	return nil, nil
}

type stubEventEmitter struct{}

func (s *stubEventEmitter) Emit(_ context.Context, _ domain.Event) error { return nil }

type stubGitOperator struct{}

func (s *stubGitOperator) Checkout(_ context.Context, _ string) error { return nil }
func (s *stubGitOperator) Commit(_ context.Context, _ git.CommitOpts) (git.CommitResult, error) {
	return git.CommitResult{}, nil
}
func (s *stubGitOperator) Merge(_ context.Context, _ git.MergeOpts) (git.MergeResult, error) {
	return git.MergeResult{}, nil
}
func (s *stubGitOperator) CreateBranch(_ context.Context, _, _ string) error { return nil }
func (s *stubGitOperator) DeleteBranch(_ context.Context, _ string) error    { return nil }
func (s *stubGitOperator) Diff(_ context.Context, _, _ string) ([]git.FileDiff, error) {
	return nil, nil
}
func (s *stubGitOperator) MergeBase(_ context.Context, a, _ string) (string, error) { return a, nil }
func (s *stubGitOperator) Head(_ context.Context) (string, error)                   { return "abc123", nil }
func (s *stubGitOperator) Push(_ context.Context, _, _ string) error               { return nil }
func (s *stubGitOperator) PushBranch(_ context.Context, _, _ string) error         { return nil }
func (s *stubGitOperator) DeleteRemoteBranch(_ context.Context, _, _ string) error { return nil }
func (s *stubGitOperator) ReadFile(_ context.Context, _, _ string) ([]byte, error) {
	return nil, nil
}
func (s *stubGitOperator) WriteAndStageFile(_ context.Context, _, _ string) error { return nil }

type stubWorkflowLoader struct{}

func (s *stubWorkflowLoader) LoadWorkflow(_ context.Context, _, _ string) (*domain.WorkflowDefinition, error) {
	return &domain.WorkflowDefinition{
		ID:        "wf-stub",
		EntryStep: "start",
		Steps: []domain.StepDefinition{
			{ID: "start", Name: "Start", Outcomes: []domain.OutcomeDefinition{{ID: "done", Name: "Done"}}},
		},
	}, nil
}

type deps struct {
	wf  WorkflowResolver
	st  RunStore
	act ActorAssigner
	art ArtifactReader
	ev  EventEmitter
	g   GitOperator
	wfl WorkflowLoader
}

func validDeps() deps {
	return deps{
		wf:  &stubWorkflowResolver{},
		st:  &stubRunStore{},
		act: &stubActorAssigner{},
		art: &stubArtifactReader{},
		ev:  &stubEventEmitter{},
		g:   &stubGitOperator{},
		wfl: &stubWorkflowLoader{},
	}
}

func TestNew_AllDependencies(t *testing.T) {
	d := validDeps()
	o, err := New(d.wf, d.st, d.act, d.art, d.ev, d.g, d.wfl)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if o == nil {
		t.Fatal("expected non-nil orchestrator")
	}
}

func TestNew_NilWorkflows(t *testing.T) {
	d := validDeps()
	_, err := New(nil, d.st, d.act, d.art, d.ev, d.g, d.wfl)
	if err == nil {
		t.Fatal("expected error for nil workflows")
	}
}

func TestNew_NilStore(t *testing.T) {
	d := validDeps()
	_, err := New(d.wf, nil, d.act, d.art, d.ev, d.g, d.wfl)
	if err == nil {
		t.Fatal("expected error for nil store")
	}
}

func TestNew_NilActors(t *testing.T) {
	d := validDeps()
	_, err := New(d.wf, d.st, nil, d.art, d.ev, d.g, d.wfl)
	if err == nil {
		t.Fatal("expected error for nil actors")
	}
}

func TestNew_NilArtifacts(t *testing.T) {
	d := validDeps()
	_, err := New(d.wf, d.st, d.act, nil, d.ev, d.g, d.wfl)
	if err == nil {
		t.Fatal("expected error for nil artifacts")
	}
}

func TestNew_NilEvents(t *testing.T) {
	d := validDeps()
	_, err := New(d.wf, d.st, d.act, d.art, nil, d.g, d.wfl)
	if err == nil {
		t.Fatal("expected error for nil events")
	}
}

func TestNew_NilGit(t *testing.T) {
	d := validDeps()
	_, err := New(d.wf, d.st, d.act, d.art, d.ev, nil, d.wfl)
	if err == nil {
		t.Fatal("expected error for nil git")
	}
}

func TestNew_NilWorkflowLoader(t *testing.T) {
	d := validDeps()
	_, err := New(d.wf, d.st, d.act, d.art, d.ev, d.g, nil)
	if err == nil {
		t.Fatal("expected error for nil workflow loader")
	}
}

// ── BindingResolver tests ──

type stubWorkflowProvider struct {
	workflows []*domain.WorkflowDefinition
}

func (s *stubWorkflowProvider) ListActiveWorkflows(_ context.Context) ([]*domain.WorkflowDefinition, error) {
	return s.workflows, nil
}

type stubGitClient struct {
	headSHA string
}

func (s *stubGitClient) Clone(_ context.Context, _, _ string) error { return nil }
func (s *stubGitClient) Commit(_ context.Context, _ git.CommitOpts) (git.CommitResult, error) {
	return git.CommitResult{}, nil
}
func (s *stubGitClient) Merge(_ context.Context, _ git.MergeOpts) (git.MergeResult, error) {
	return git.MergeResult{}, nil
}
func (s *stubGitClient) CreateBranch(_ context.Context, _, _ string) error { return nil }
func (s *stubGitClient) DeleteBranch(_ context.Context, _ string) error    { return nil }
func (s *stubGitClient) Diff(_ context.Context, _, _ string) ([]git.FileDiff, error) {
	return nil, nil
}
func (s *stubGitClient) MergeBase(_ context.Context, a, _ string) (string, error) { return a, nil }
func (s *stubGitClient) Log(_ context.Context, _ git.LogOpts) ([]git.CommitInfo, error) {
	return nil, nil
}
func (s *stubGitClient) ReadFile(_ context.Context, _, _ string) ([]byte, error) { return nil, nil }
func (s *stubGitClient) ListFiles(_ context.Context, _, _ string) ([]string, error) {
	return nil, nil
}
func (s *stubGitClient) Head(_ context.Context) (string, error)                  { return s.headSHA, nil }
func (s *stubGitClient) Push(_ context.Context, _, _ string) error               { return nil }
func (s *stubGitClient) PushBranch(_ context.Context, _, _ string) error         { return nil }
func (s *stubGitClient) DeleteRemoteBranch(_ context.Context, _, _ string) error { return nil }

func TestBindingResolver_ResolveWorkflow(t *testing.T) {
	provider := &stubWorkflowProvider{
		workflows: []*domain.WorkflowDefinition{
			{
				ID:        "wf-task",
				Status:    "Active",
				Version:   "1.0.0",
				AppliesTo: []string{"task"},
				CommitSHA: "sha-from-provider",
			},
		},
	}
	gc := &stubGitClient{headSHA: "abc123"}

	resolver := NewBindingResolver(provider, gc)
	result, err := resolver.ResolveWorkflow(context.Background(), "task", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Workflow.ID != "wf-task" {
		t.Fatalf("expected workflow ID wf-task, got %s", result.Workflow.ID)
	}
	if result.CommitSHA != "sha-from-provider" {
		t.Fatalf("expected commit SHA sha-from-provider, got %s", result.CommitSHA)
	}
}

func TestNewBindingResolver_StoresFields(t *testing.T) {
	provider := &stubWorkflowProvider{}
	gc := &stubGitClient{headSHA: "abc"}

	resolver := NewBindingResolver(provider, gc)
	if resolver.provider != provider {
		t.Fatal("expected provider to be stored")
	}
	if resolver.gitClient != gc {
		t.Fatal("expected gitClient to be stored")
	}
}
