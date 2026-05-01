package gateway

import (
	"context"
	"testing"

	"github.com/bszymi/spine/internal/delivery"
	"github.com/bszymi/spine/internal/divergence"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/engine"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/gitpool"
	"github.com/bszymi/spine/internal/repository"
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
	ctx := context.WithValue(context.Background(), serviceSetKey{}, &workspace.ServiceSet{
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
	ctx := context.WithValue(context.Background(), serviceSetKey{}, &workspace.ServiceSet{
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
	ctx := context.WithValue(context.Background(), serviceSetKey{}, &workspace.ServiceSet{
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
	ctx := context.WithValue(context.Background(), serviceSetKey{}, &workspace.ServiceSet{
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
	ctx := context.WithValue(context.Background(), serviceSetKey{}, &workspace.ServiceSet{
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
	ctx := context.WithValue(context.Background(), serviceSetKey{}, &workspace.ServiceSet{
		PlanningRunStarter: wsPS,
	})

	got := s.planningRunStarterFrom(ctx)
	result, _ := got.StartPlanningRun(context.Background(), "path", "content")
	if result.RunID != "workspace" {
		t.Errorf("expected workspace PlanningRunStarter, got RunID=%q", result.RunID)
	}
}

// gitPoolStubResolver is the minimal Resolver shape gitpool.New
// requires; we never invoke it in these tests, only construct a Pool
// to exercise field-copy and accessor wiring.
type gitPoolStubResolver struct{}

func (gitPoolStubResolver) Lookup(_ context.Context, _ string) (*repository.Repository, error) {
	return nil, nil
}
func (gitPoolStubResolver) ListActive(_ context.Context) ([]repository.Repository, error) {
	return nil, nil
}

func newPoolForAccessorTest(t *testing.T) *gitpool.Pool {
	t.Helper()
	primary := git.NewCLIClient(t.TempDir())
	p, err := gitpool.New(primary, gitPoolStubResolver{}, gitpool.NewCLIClientFactory())
	if err != nil {
		t.Fatalf("gitpool.New: %v", err)
	}
	return p
}

func TestNewServer_CopiesGitPoolFromConfig(t *testing.T) {
	// The server's gitPool field is the only handle handlers have on
	// the workspace pool; if NewServer dropped it, every per-repo
	// resolution would silently fall back to nil.
	pool := newPoolForAccessorTest(t)
	srv := NewServer("", ServerConfig{GitPool: pool})

	got := srv.gitPoolFrom(context.Background())
	if got != pool {
		t.Error("expected gitPool from config to be reachable via accessor")
	}
}

func TestGitPoolFrom_PrefersServiceSet(t *testing.T) {
	serverPool := newPoolForAccessorTest(t)
	wsPool := newPoolForAccessorTest(t)

	s := &Server{gitPool: serverPool}
	ctx := context.WithValue(context.Background(), serviceSetKey{}, &workspace.ServiceSet{
		GitPool: wsPool,
	})

	got := s.gitPoolFrom(ctx)
	if got != wsPool {
		t.Error("expected workspace-scoped GitPool from ServiceSet")
	}
}

func TestGitPoolFrom_NilServiceSetGitPool_FallsBack(t *testing.T) {
	serverPool := newPoolForAccessorTest(t)
	s := &Server{gitPool: serverPool}
	ctx := context.WithValue(context.Background(), serviceSetKey{}, &workspace.ServiceSet{
		GitPool: nil,
	})

	got := s.gitPoolFrom(ctx)
	if got != serverPool {
		t.Error("expected fallback to server-level gitPool when ServiceSet pool is nil")
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
	if s.stepAcknowledgerFrom(ctx) != nil {
		t.Error("expected nil stepAcknowledger")
	}
	if s.candidateFinderFrom(ctx) != nil {
		t.Error("expected nil candidateFinder")
	}
	if s.stepClaimerFrom(ctx) != nil {
		t.Error("expected nil stepClaimer")
	}
	if s.stepReleaserFrom(ctx) != nil {
		t.Error("expected nil stepReleaser")
	}
	if s.stepExecutionListerFrom(ctx) != nil {
		t.Error("expected nil stepExecutionLister")
	}
}

// Stubs and tests for the lifecycle-handler accessors that close the
// platform-binding nil-deref gap (INIT-020 EPIC-001 TASK-002). Each
// resolver follows the precedent of resultHandlerFrom: prefer the
// workspace ServiceSet when populated, fall back to the server-level
// field, return nil when neither is wired so handlers can degrade to
// 503 instead of panicking on a nil orchestrator.

type stubStepAcknowledger struct{ id string }

func (s *stubStepAcknowledger) AcknowledgeStep(_ context.Context, _ engine.AcknowledgeRequest) (*engine.AcknowledgeResult, error) {
	return &engine.AcknowledgeResult{ExecutionID: s.id}, nil
}

type stubCandidateFinder struct{ id string }

func (s *stubCandidateFinder) FindExecutionCandidates(_ context.Context, _ engine.ExecutionCandidateFilter) ([]engine.ExecutionCandidate, error) {
	return []engine.ExecutionCandidate{{TaskPath: s.id}}, nil
}

type stubStepClaimer struct{ id string }

func (s *stubStepClaimer) ClaimStep(_ context.Context, _ engine.ClaimRequest) (*engine.ClaimResult, error) {
	return &engine.ClaimResult{RunID: s.id}, nil
}

type stubStepReleaser struct{ id string }

func (s *stubStepReleaser) ReleaseStep(_ context.Context, _ engine.ReleaseRequest) error {
	return nil
}

type stubStepExecutionLister struct{ id string }

func (s *stubStepExecutionLister) ListStepExecutions(_ context.Context, _ engine.StepExecutionQuery) ([]engine.StepExecutionItem, error) {
	return []engine.StepExecutionItem{{ExecutionID: s.id}}, nil
}

func TestStepAcknowledgerFrom_FallsBackToServer(t *testing.T) {
	srv := &stubStepAcknowledger{id: "server"}
	s := &Server{stepAcknowledger: srv}
	if got := s.stepAcknowledgerFrom(context.Background()); got != srv {
		t.Error("expected server-level stepAcknowledger when no ServiceSet in context")
	}
}

func TestStepAcknowledgerFrom_PrefersServiceSet(t *testing.T) {
	serverA := &stubStepAcknowledger{id: "server"}
	wsA := &stubStepAcknowledger{id: "workspace"}
	s := &Server{stepAcknowledger: serverA}
	ctx := context.WithValue(context.Background(), serviceSetKey{}, &workspace.ServiceSet{
		StepAcknowledger: wsA,
	})
	got := s.stepAcknowledgerFrom(ctx)
	result, _ := got.AcknowledgeStep(context.Background(), engine.AcknowledgeRequest{})
	if result.ExecutionID != "workspace" {
		t.Errorf("expected workspace StepAcknowledger, got ExecutionID=%q", result.ExecutionID)
	}
}

func TestStepAcknowledgerFrom_InvalidType_FallsBack(t *testing.T) {
	srv := &stubStepAcknowledger{id: "server"}
	s := &Server{stepAcknowledger: srv}
	ctx := context.WithValue(context.Background(), serviceSetKey{}, &workspace.ServiceSet{
		StepAcknowledger: "not-a-StepAcknowledger",
	})
	if got := s.stepAcknowledgerFrom(ctx); got != srv {
		t.Error("expected fallback to server-level when type assertion fails")
	}
}

// Platform-binding regression: with no top-level field and a populated
// ServiceSet, the resolver must dispatch to the workspace handler. This
// is the exact shape of the bug TASK-002 fixes — handleStepAcknowledge
// reading s.stepAcknowledger directly nil-derefed in production.
func TestStepAcknowledgerFrom_PlatformBinding_NoTopLevel_ResolvesFromServiceSet(t *testing.T) {
	wsA := &stubStepAcknowledger{id: "workspace"}
	s := &Server{} // no top-level
	ctx := context.WithValue(context.Background(), serviceSetKey{}, &workspace.ServiceSet{
		StepAcknowledger: wsA,
	})
	got := s.stepAcknowledgerFrom(ctx)
	if got == nil {
		t.Fatal("expected workspace stepAcknowledger; resolver returned nil — handler would 503 in platform-binding mode")
	}
	result, _ := got.AcknowledgeStep(context.Background(), engine.AcknowledgeRequest{})
	if result.ExecutionID != "workspace" {
		t.Errorf("expected workspace dispatch, got ExecutionID=%q", result.ExecutionID)
	}
}

func TestCandidateFinderFrom_FallsBackToServer(t *testing.T) {
	cf := &stubCandidateFinder{id: "server"}
	s := &Server{candidateFinder: cf}
	if got := s.candidateFinderFrom(context.Background()); got != cf {
		t.Error("expected server-level candidateFinder when no ServiceSet in context")
	}
}

func TestCandidateFinderFrom_PrefersServiceSet(t *testing.T) {
	serverCF := &stubCandidateFinder{id: "server"}
	wsCF := &stubCandidateFinder{id: "workspace"}
	s := &Server{candidateFinder: serverCF}
	ctx := context.WithValue(context.Background(), serviceSetKey{}, &workspace.ServiceSet{
		CandidateFinder: wsCF,
	})
	got := s.candidateFinderFrom(ctx)
	result, _ := got.FindExecutionCandidates(context.Background(), engine.ExecutionCandidateFilter{})
	if len(result) != 1 || result[0].TaskPath != "workspace" {
		t.Errorf("expected workspace CandidateFinder, got %+v", result)
	}
}

func TestCandidateFinderFrom_InvalidType_FallsBack(t *testing.T) {
	cf := &stubCandidateFinder{id: "server"}
	s := &Server{candidateFinder: cf}
	ctx := context.WithValue(context.Background(), serviceSetKey{}, &workspace.ServiceSet{
		CandidateFinder: "not-a-CandidateFinder",
	})
	if got := s.candidateFinderFrom(ctx); got != cf {
		t.Error("expected fallback to server-level when type assertion fails")
	}
}

func TestStepClaimerFrom_FallsBackToServer(t *testing.T) {
	cl := &stubStepClaimer{id: "server"}
	s := &Server{stepClaimer: cl}
	if got := s.stepClaimerFrom(context.Background()); got != cl {
		t.Error("expected server-level stepClaimer when no ServiceSet in context")
	}
}

func TestStepClaimerFrom_PrefersServiceSet(t *testing.T) {
	serverCL := &stubStepClaimer{id: "server"}
	wsCL := &stubStepClaimer{id: "workspace"}
	s := &Server{stepClaimer: serverCL}
	ctx := context.WithValue(context.Background(), serviceSetKey{}, &workspace.ServiceSet{
		StepClaimer: wsCL,
	})
	got := s.stepClaimerFrom(ctx)
	result, _ := got.ClaimStep(context.Background(), engine.ClaimRequest{})
	if result.RunID != "workspace" {
		t.Errorf("expected workspace StepClaimer, got RunID=%q", result.RunID)
	}
}

func TestStepClaimerFrom_InvalidType_FallsBack(t *testing.T) {
	cl := &stubStepClaimer{id: "server"}
	s := &Server{stepClaimer: cl}
	ctx := context.WithValue(context.Background(), serviceSetKey{}, &workspace.ServiceSet{
		StepClaimer: "not-a-StepClaimer",
	})
	if got := s.stepClaimerFrom(ctx); got != cl {
		t.Error("expected fallback to server-level when type assertion fails")
	}
}

func TestStepReleaserFrom_FallsBackToServer(t *testing.T) {
	rl := &stubStepReleaser{id: "server"}
	s := &Server{stepReleaser: rl}
	if got := s.stepReleaserFrom(context.Background()); got != rl {
		t.Error("expected server-level stepReleaser when no ServiceSet in context")
	}
}

func TestStepReleaserFrom_PrefersServiceSet(t *testing.T) {
	serverRL := &stubStepReleaser{id: "server"}
	wsRL := &stubStepReleaser{id: "workspace"}
	s := &Server{stepReleaser: serverRL}
	ctx := context.WithValue(context.Background(), serviceSetKey{}, &workspace.ServiceSet{
		StepReleaser: wsRL,
	})
	if got := s.stepReleaserFrom(ctx); got != wsRL {
		t.Error("expected workspace-scoped StepReleaser")
	}
}

func TestStepReleaserFrom_InvalidType_FallsBack(t *testing.T) {
	rl := &stubStepReleaser{id: "server"}
	s := &Server{stepReleaser: rl}
	ctx := context.WithValue(context.Background(), serviceSetKey{}, &workspace.ServiceSet{
		StepReleaser: "not-a-StepReleaser",
	})
	if got := s.stepReleaserFrom(ctx); got != rl {
		t.Error("expected fallback to server-level when type assertion fails")
	}
}

func TestStepExecutionListerFrom_FallsBackToServer(t *testing.T) {
	ll := &stubStepExecutionLister{id: "server"}
	s := &Server{stepExecutionLister: ll}
	if got := s.stepExecutionListerFrom(context.Background()); got != ll {
		t.Error("expected server-level stepExecutionLister when no ServiceSet in context")
	}
}

func TestStepExecutionListerFrom_PrefersServiceSet(t *testing.T) {
	serverLL := &stubStepExecutionLister{id: "server"}
	wsLL := &stubStepExecutionLister{id: "workspace"}
	s := &Server{stepExecutionLister: serverLL}
	ctx := context.WithValue(context.Background(), serviceSetKey{}, &workspace.ServiceSet{
		StepExecutionLister: wsLL,
	})
	got := s.stepExecutionListerFrom(ctx)
	steps, _ := got.ListStepExecutions(context.Background(), engine.StepExecutionQuery{})
	if len(steps) != 1 || steps[0].ExecutionID != "workspace" {
		t.Errorf("expected workspace StepExecutionLister, got %+v", steps)
	}
}

func TestStepExecutionListerFrom_InvalidType_FallsBack(t *testing.T) {
	ll := &stubStepExecutionLister{id: "server"}
	s := &Server{stepExecutionLister: ll}
	ctx := context.WithValue(context.Background(), serviceSetKey{}, &workspace.ServiceSet{
		StepExecutionLister: "not-a-StepExecutionLister",
	})
	if got := s.stepExecutionListerFrom(ctx); got != ll {
		t.Error("expected fallback to server-level when type assertion fails")
	}
}

// EventBroadcaster resolver — TASK-003 SSE in platform-binding mode.
// Same shape as the lifecycle-handler resolvers: prefer ServiceSet,
// fall back to server, type-assert the any slot.

func TestEventBroadcasterFrom_FallsBackToServer(t *testing.T) {
	srv := delivery.NewEventBroadcaster()
	s := &Server{eventBroadcaster: srv}
	if got := s.eventBroadcasterFrom(context.Background()); got != srv {
		t.Error("expected server-level eventBroadcaster when no ServiceSet in context")
	}
}

func TestEventBroadcasterFrom_PrefersServiceSet(t *testing.T) {
	serverB := delivery.NewEventBroadcaster()
	wsB := delivery.NewEventBroadcaster()
	s := &Server{eventBroadcaster: serverB}
	ctx := context.WithValue(context.Background(), serviceSetKey{}, &workspace.ServiceSet{
		EventBroadcaster: wsB,
	})
	if got := s.eventBroadcasterFrom(ctx); got != wsB {
		t.Error("expected workspace-scoped EventBroadcaster from ServiceSet")
	}
}

func TestEventBroadcasterFrom_InvalidType_FallsBack(t *testing.T) {
	srv := delivery.NewEventBroadcaster()
	s := &Server{eventBroadcaster: srv}
	ctx := context.WithValue(context.Background(), serviceSetKey{}, &workspace.ServiceSet{
		EventBroadcaster: "not-a-broadcaster",
	})
	if got := s.eventBroadcasterFrom(ctx); got != srv {
		t.Error("expected fallback to server-level when type assertion fails")
	}
}

// Platform-binding regression: nil top-level + populated ServiceSet
// must dispatch to the workspace broadcaster. Without this resolver,
// /api/v1/events/stream returned 503 in platform-binding mode even
// after TASK-003 wired per-workspace delivery.
func TestEventBroadcasterFrom_PlatformBinding_NoTopLevel_ResolvesFromServiceSet(t *testing.T) {
	wsB := delivery.NewEventBroadcaster()
	s := &Server{}
	ctx := context.WithValue(context.Background(), serviceSetKey{}, &workspace.ServiceSet{
		EventBroadcaster: wsB,
	})
	got := s.eventBroadcasterFrom(ctx)
	if got == nil {
		t.Fatal("expected workspace EventBroadcaster; resolver returned nil — SSE would 503 in platform-binding")
	}
	if got != wsB {
		t.Error("expected the workspace's broadcaster to be returned")
	}
}
