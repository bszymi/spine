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

type stubRunStore struct{}

func (s *stubRunStore) CreateRun(_ context.Context, _ *domain.Run) error                  { return nil }
func (s *stubRunStore) GetRun(_ context.Context, _ string) (*domain.Run, error)           { return nil, nil }
func (s *stubRunStore) UpdateRunStatus(_ context.Context, _ string, _ domain.RunStatus) error {
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
func (s *stubRunStore) CreateBranch(_ context.Context, _ *domain.Branch) error  { return nil }
func (s *stubRunStore) UpdateBranch(_ context.Context, _ *domain.Branch) error  { return nil }
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

func (s *stubGitOperator) Commit(_ context.Context, _ git.CommitOpts) (git.CommitResult, error) {
	return git.CommitResult{}, nil
}
func (s *stubGitOperator) Merge(_ context.Context, _ git.MergeOpts) (git.MergeResult, error) {
	return git.MergeResult{}, nil
}
func (s *stubGitOperator) CreateBranch(_ context.Context, _, _ string) error { return nil }
func (s *stubGitOperator) DeleteBranch(_ context.Context, _ string) error    { return nil }
func (s *stubGitOperator) Head(_ context.Context) (string, error)            { return "abc123", nil }

func validDeps() (WorkflowResolver, RunStore, ActorAssigner, ArtifactReader, EventEmitter, GitOperator) {
	return &stubWorkflowResolver{}, &stubRunStore{}, &stubActorAssigner{},
		&stubArtifactReader{}, &stubEventEmitter{}, &stubGitOperator{}
}

func TestNew_AllDependencies(t *testing.T) {
	o, err := New(validDeps())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if o == nil {
		t.Fatal("expected non-nil orchestrator")
	}
}

func TestNew_NilWorkflows(t *testing.T) {
	_, st, act, art, ev, g := validDeps()
	_, err := New(nil, st, act, art, ev, g)
	if err == nil {
		t.Fatal("expected error for nil workflows")
	}
}

func TestNew_NilStore(t *testing.T) {
	wf, _, act, art, ev, g := validDeps()
	_, err := New(wf, nil, act, art, ev, g)
	if err == nil {
		t.Fatal("expected error for nil store")
	}
}

func TestNew_NilActors(t *testing.T) {
	wf, st, _, art, ev, g := validDeps()
	_, err := New(wf, st, nil, art, ev, g)
	if err == nil {
		t.Fatal("expected error for nil actors")
	}
}

func TestNew_NilArtifacts(t *testing.T) {
	wf, st, act, _, ev, g := validDeps()
	_, err := New(wf, st, act, nil, ev, g)
	if err == nil {
		t.Fatal("expected error for nil artifacts")
	}
}

func TestNew_NilEvents(t *testing.T) {
	wf, st, act, art, _, g := validDeps()
	_, err := New(wf, st, act, art, nil, g)
	if err == nil {
		t.Fatal("expected error for nil events")
	}
}

func TestNew_NilGit(t *testing.T) {
	wf, st, act, art, ev, _ := validDeps()
	_, err := New(wf, st, act, art, ev, nil)
	if err == nil {
		t.Fatal("expected error for nil git")
	}
}
