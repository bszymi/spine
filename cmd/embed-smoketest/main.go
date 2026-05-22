// Command embed-smoketest proves the Spine Core library surface stands
// alone: it imports core/... packages, constructs a complete Engine
// with in-binary stub port implementations, and exits 0 on success.
//
// The point of this binary is the *import graph*, not the runtime
// behaviour. If `go build ./cmd/embed-smoketest` succeeds and the
// running binary prints "engine constructed", the library form of
// Spine Core is genuinely usable from an external consumer — no
// service/, no adapters/postgres, no real Git, no real actor runner
// required.
//
// SMP will write its own port implementations against its real
// backends. This binary is the Spine repo's structural CI test that
// the surface SMP would import is buildable from the Core side alone.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/bszymi/spine/core/actor"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/engine"
	"github.com/bszymi/spine/core/workflow"

	// NOTE: importing adapters/git here exposes a known TASK-004
	// follow-up: core/engine's GitOperator interface signature
	// references types from adapters/git (CommitOpts, MergeOpts,
	// MergeResult, FileDiff, CommitResult). Per ADR-017, port
	// interface signatures must name only core/ types — the git
	// operation DTOs need to move into core/ (e.g. core/git/ or
	// core/ports/) with adapters/git keeping only the
	// implementation. Until that relocation lands, an external
	// consumer of the Core library must transitively pull
	// adapters/git for the type names. The smoke test surfaces
	// rather than hides this leak.
	gitops "github.com/bszymi/spine/adapters/git"
)

// errSmoketestNotFound is the sentinel stubRunStore.GetRun returns so
// the end-to-end CancelRun exercise can assert that the orchestrator →
// store → response path was actually walked — not just that some error
// happened earlier in construction or argument validation.
var errSmoketestNotFound = errors.New("smoketest: run not found in stub store")

// --- Stub port implementations ----------------------------------------
//
// One stub per required engine dependency. All operations are no-ops
// or trivial returns; the smoke test does not need real behaviour
// behind any port. A real consumer replaces each with an
// implementation against their actual backend.

type stubWorkflowResolver struct{}

func (stubWorkflowResolver) ResolveWorkflow(_ context.Context, _, _ string) (*workflow.BindingResult, error) {
	return nil, fmt.Errorf("smoketest: no workflow resolution wired")
}

func (stubWorkflowResolver) ResolveWorkflowForMode(_ context.Context, _, _, _ string) (*workflow.BindingResult, error) {
	return nil, fmt.Errorf("smoketest: no workflow resolution wired")
}

type stubRunStore struct{}

func (stubRunStore) CreateRun(_ context.Context, _ *domain.Run) error { return nil }
func (stubRunStore) GetRun(_ context.Context, _ string) (*domain.Run, error) {
	return nil, errSmoketestNotFound
}
func (stubRunStore) UpdateRunStatus(_ context.Context, _ string, _ domain.RunStatus) error {
	return nil
}
func (stubRunStore) TransitionRunStatus(_ context.Context, _ string, _, _ domain.RunStatus) (bool, error) {
	return true, nil
}
func (stubRunStore) UpdateCurrentStep(_ context.Context, _, _ string) error { return nil }
func (stubRunStore) SetCommitMeta(_ context.Context, _ string, _ map[string]string) error {
	return nil
}
func (stubRunStore) CreateStepExecution(_ context.Context, _ *domain.StepExecution) error {
	return nil
}
func (stubRunStore) GetStepExecution(_ context.Context, _ string) (*domain.StepExecution, error) {
	return nil, fmt.Errorf("step execution not found")
}
func (stubRunStore) UpdateStepExecution(_ context.Context, _ *domain.StepExecution) error {
	return nil
}
func (stubRunStore) ListStepExecutionsByRun(_ context.Context, _ string) ([]domain.StepExecution, error) {
	return nil, nil
}
func (stubRunStore) CreateDivergenceContext(_ context.Context, _ *domain.DivergenceContext) error {
	return nil
}
func (stubRunStore) UpdateDivergenceContext(_ context.Context, _ *domain.DivergenceContext) error {
	return nil
}
func (stubRunStore) GetDivergenceContext(_ context.Context, _ string) (*domain.DivergenceContext, error) {
	return nil, fmt.Errorf("divergence not found")
}
func (stubRunStore) CreateBranch(_ context.Context, _ *domain.Branch) error { return nil }
func (stubRunStore) UpdateBranch(_ context.Context, _ *domain.Branch) error { return nil }
func (stubRunStore) GetBranch(_ context.Context, _ string) (*domain.Branch, error) {
	return nil, fmt.Errorf("branch not found")
}
func (stubRunStore) ListBranchesByDivergence(_ context.Context, _ string) ([]domain.Branch, error) {
	return nil, nil
}
func (stubRunStore) UpsertRepositoryMergeOutcome(_ context.Context, _ *domain.RepositoryMergeOutcome) error {
	return nil
}
func (stubRunStore) GetRepositoryMergeOutcome(_ context.Context, _, _ string) (*domain.RepositoryMergeOutcome, error) {
	return nil, fmt.Errorf("merge outcome not found")
}
func (stubRunStore) ListRepositoryMergeOutcomes(_ context.Context, _ string) ([]domain.RepositoryMergeOutcome, error) {
	return nil, nil
}

type stubActorAssigner struct{}

func (stubActorAssigner) DeliverAssignment(_ context.Context, _ actor.AssignmentRequest) error {
	return nil
}
func (stubActorAssigner) ProcessResult(_ context.Context, _ actor.AssignmentRequest, _ actor.AssignmentResult) error {
	return nil
}

type stubArtifactReader struct{}

func (stubArtifactReader) Read(_ context.Context, _, _ string) (*domain.Artifact, error) {
	return nil, fmt.Errorf("artifact not found")
}

type stubEventEmitter struct{}

func (stubEventEmitter) Emit(_ context.Context, _ domain.Event) error { return nil }

type stubGitOperator struct{}

func (stubGitOperator) Checkout(_ context.Context, _ string) error { return nil }
func (stubGitOperator) Commit(_ context.Context, _ gitops.CommitOpts) (gitops.CommitResult, error) {
	return gitops.CommitResult{}, nil
}
func (stubGitOperator) Merge(_ context.Context, _ gitops.MergeOpts) (gitops.MergeResult, error) {
	return gitops.MergeResult{}, nil
}
func (stubGitOperator) CreateBranch(_ context.Context, _, _ string) error { return nil }
func (stubGitOperator) DeleteBranch(_ context.Context, _ string) error    { return nil }
func (stubGitOperator) Diff(_ context.Context, _, _ string) ([]gitops.FileDiff, error) {
	return nil, nil
}
func (stubGitOperator) MergeBase(_ context.Context, a, _ string) (string, error) { return a, nil }
func (stubGitOperator) Head(_ context.Context) (string, error)                   { return "smoketest", nil }
func (stubGitOperator) Push(_ context.Context, _, _ string) error                { return nil }
func (stubGitOperator) PushBranch(_ context.Context, _, _ string) error          { return nil }
func (stubGitOperator) DeleteRemoteBranch(_ context.Context, _, _ string) error  { return nil }
func (stubGitOperator) ReadFile(_ context.Context, _, _ string) ([]byte, error)  { return nil, nil }
func (stubGitOperator) WriteAndStageFile(_ context.Context, _, _ string) error   { return nil }

type stubWorkflowLoader struct{}

func (stubWorkflowLoader) LoadWorkflow(_ context.Context, _, _ string) (*domain.WorkflowDefinition, error) {
	return nil, fmt.Errorf("workflow definition not found")
}

// --- main -------------------------------------------------------------

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "embed-smoketest: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("embed-smoketest: engine constructed and exercised; library surface is sound.")
}

func run() error {
	// Construct the engine from the published Spine Core surface.
	//
	// TASK-004 follow-up: engine.New takes a positional argument list
	// today (one parameter per required port). ADR-018 §7 specifies a
	// single core.NewEngine(core.Dependencies{...}) constructor;
	// migration to that shape lands when the public core.Engine
	// interface is carved out (EPIC-003 TASK-002 / TASK-004 follow-up).
	o, err := engine.New(
		stubWorkflowResolver{},
		stubRunStore{},
		stubActorAssigner{},
		stubArtifactReader{},
		stubEventEmitter{},
		stubGitOperator{},
		stubWorkflowLoader{},
	)
	if err != nil {
		return fmt.Errorf("engine construction: %w", err)
	}
	if o == nil {
		return fmt.Errorf("engine.New returned nil without error")
	}

	// Exercise an end-to-end path through the constructed engine so
	// the smoke test proves more than "the constructor returns". The
	// stub store's GetRun returns errSmoketestNotFound; CancelRun's
	// first call is store.GetRun, and the engine wraps and returns
	// that error. errors.Is against the sentinel proves that the
	// orchestrator → store → response path was actually walked —
	// any earlier engine-side failure would not unwrap to our
	// sentinel.
	ctx := context.Background()
	err = o.CancelRun(ctx, "smoketest-run-id-that-does-not-exist")
	if err == nil {
		return fmt.Errorf("expected CancelRun to fail against stub store, got nil error")
	}
	if !errors.Is(err, errSmoketestNotFound) {
		return fmt.Errorf("CancelRun returned %v; expected error to wrap errSmoketestNotFound — orchestrator → store path may not be wired", err)
	}

	return nil
}
