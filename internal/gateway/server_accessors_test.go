package gateway

import (
	"context"
	"testing"

	"github.com/bszymi/spine/internal/divergence"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/validation"
	"github.com/bszymi/spine/internal/workspace"
)

// stubBranchCreator implements BranchCreator for testing.
type stubBranchCreator struct{}

func (s *stubBranchCreator) CreateExploratoryBranch(_ context.Context, _ *domain.DivergenceContext, _, _ string) (*domain.Branch, error) {
	return nil, nil
}
func (s *stubBranchCreator) CloseWindow(_ context.Context, _ *domain.DivergenceContext) error {
	return nil
}

// stubRunStarter implements RunStarter for testing.
type stubRunStarter struct{ id string }

func (s *stubRunStarter) StartRun(_ context.Context, _ string) (*RunStartResult, error) {
	return &RunStartResult{RunID: s.id}, nil
}

// stubPlanningRunStarter implements PlanningRunStarter for testing.
type stubPlanningRunStarter struct{ id string }

func (s *stubPlanningRunStarter) StartPlanningRun(_ context.Context, _, _ string) (*PlanningRunResult, error) {
	return &PlanningRunResult{RunID: s.id}, nil
}

func TestValidatorFrom_FallsBackToServer(t *testing.T) {
	v := &validation.Engine{}
	s := &Server{validator: v}

	got := s.validatorFrom(context.Background())
	if got != v {
		t.Error("expected server-level validator when no ServiceSet in context")
	}
}

func TestValidatorFrom_PrefersServiceSet(t *testing.T) {
	serverV := &validation.Engine{}
	wsV := &validation.Engine{}

	s := &Server{validator: serverV}
	ctx := context.WithValue(context.Background(), serviceSetContextKey, &workspace.ServiceSet{
		Validator: wsV,
	})

	got := s.validatorFrom(ctx)
	if got != wsV {
		t.Error("expected workspace-scoped validator from ServiceSet")
	}
	if got == serverV {
		t.Error("should not return server-level validator when ServiceSet has one")
	}
}

func TestValidatorFrom_NilServiceSetValidator_FallsBack(t *testing.T) {
	serverV := &validation.Engine{}
	s := &Server{validator: serverV}
	ctx := context.WithValue(context.Background(), serviceSetContextKey, &workspace.ServiceSet{
		Validator: nil,
	})

	got := s.validatorFrom(ctx)
	if got != serverV {
		t.Error("expected fallback to server-level validator when ServiceSet validator is nil")
	}
}

func TestBranchCreatorFrom_FallsBackToServer(t *testing.T) {
	bc := &stubBranchCreator{}
	s := &Server{branchCreator: bc}

	got := s.branchCreatorFrom(context.Background())
	if got != bc {
		t.Error("expected server-level branchCreator when no ServiceSet in context")
	}
}

func TestBranchCreatorFrom_PrefersServiceSet(t *testing.T) {
	serverBC := &stubBranchCreator{}
	wsDiv := &divergence.Service{}

	s := &Server{branchCreator: serverBC}
	ctx := context.WithValue(context.Background(), serviceSetContextKey, &workspace.ServiceSet{
		Divergence: wsDiv,
	})

	got := s.branchCreatorFrom(ctx)
	if got == serverBC {
		t.Error("should not return server-level branchCreator when ServiceSet has Divergence")
	}
	if got != wsDiv {
		t.Error("expected workspace-scoped Divergence as BranchCreator")
	}
}

func TestRunStarterFrom_FallsBackToServer(t *testing.T) {
	rs := &stubRunStarter{id: "server"}
	s := &Server{runStarter: rs}

	got := s.runStarterFrom(context.Background())
	if got != rs {
		t.Error("expected server-level runStarter when no ServiceSet in context")
	}
}

func TestRunStarterFrom_PrefersServiceSet(t *testing.T) {
	serverRS := &stubRunStarter{id: "server"}
	wsRS := &stubRunStarter{id: "workspace"}

	s := &Server{runStarter: serverRS}
	ctx := context.WithValue(context.Background(), serviceSetContextKey, &workspace.ServiceSet{
		RunStarter: wsRS,
	})

	got := s.runStarterFrom(ctx)
	result, _ := got.StartRun(context.Background(), "task")
	if result.RunID != "workspace" {
		t.Errorf("expected workspace RunStarter, got RunID=%q", result.RunID)
	}
}

func TestRunStarterFrom_InvalidType_FallsBack(t *testing.T) {
	serverRS := &stubRunStarter{id: "server"}
	s := &Server{runStarter: serverRS}
	ctx := context.WithValue(context.Background(), serviceSetContextKey, &workspace.ServiceSet{
		RunStarter: "not-a-RunStarter",
	})

	got := s.runStarterFrom(ctx)
	if got != serverRS {
		t.Error("expected fallback to server-level runStarter when type assertion fails")
	}
}

func TestPlanningRunStarterFrom_FallsBackToServer(t *testing.T) {
	ps := &stubPlanningRunStarter{id: "server"}
	s := &Server{planningRunStarter: ps}

	got := s.planningRunStarterFrom(context.Background())
	if got != ps {
		t.Error("expected server-level planningRunStarter when no ServiceSet in context")
	}
}

func TestPlanningRunStarterFrom_PrefersServiceSet(t *testing.T) {
	serverPS := &stubPlanningRunStarter{id: "server"}
	wsPS := &stubPlanningRunStarter{id: "workspace"}

	s := &Server{planningRunStarter: serverPS}
	ctx := context.WithValue(context.Background(), serviceSetContextKey, &workspace.ServiceSet{
		PlanningRunStarter: wsPS,
	})

	got := s.planningRunStarterFrom(ctx)
	result, _ := got.StartPlanningRun(context.Background(), "path", "content")
	if result.RunID != "workspace" {
		t.Errorf("expected workspace PlanningRunStarter, got RunID=%q", result.RunID)
	}
}

func TestAllAccessors_NilServiceSet(t *testing.T) {
	s := &Server{}
	ctx := context.Background()

	if s.validatorFrom(ctx) != nil {
		t.Error("expected nil validator")
	}
	if s.branchCreatorFrom(ctx) != nil {
		t.Error("expected nil branchCreator")
	}
	if s.runStarterFrom(ctx) != nil {
		t.Error("expected nil runStarter")
	}
	if s.planningRunStarterFrom(ctx) != nil {
		t.Error("expected nil planningRunStarter")
	}
}
