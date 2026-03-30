package engine

import (
	"context"

	"github.com/bszymi/spine/internal/actor"
	"github.com/bszymi/spine/internal/artifact"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/workflow"
)

// WorkflowResolver resolves the governing workflow for a given artifact type.
type WorkflowResolver interface {
	ResolveWorkflow(ctx context.Context, artifactType, workType string) (*workflow.BindingResult, error)
}

// RunStore provides run and step execution persistence required by the orchestrator.
type RunStore interface {
	// Runs
	CreateRun(ctx context.Context, run *domain.Run) error
	GetRun(ctx context.Context, runID string) (*domain.Run, error)
	UpdateRunStatus(ctx context.Context, runID string, status domain.RunStatus) error
	UpdateCurrentStep(ctx context.Context, runID, stepID string) error

	// Step Executions
	CreateStepExecution(ctx context.Context, exec *domain.StepExecution) error
	GetStepExecution(ctx context.Context, executionID string) (*domain.StepExecution, error)
	UpdateStepExecution(ctx context.Context, exec *domain.StepExecution) error
	ListStepExecutionsByRun(ctx context.Context, runID string) ([]domain.StepExecution, error)

	// Divergence
	CreateDivergenceContext(ctx context.Context, div *domain.DivergenceContext) error
	UpdateDivergenceContext(ctx context.Context, div *domain.DivergenceContext) error
	GetDivergenceContext(ctx context.Context, divergenceID string) (*domain.DivergenceContext, error)
	CreateBranch(ctx context.Context, branch *domain.Branch) error
	UpdateBranch(ctx context.Context, branch *domain.Branch) error
	GetBranch(ctx context.Context, branchID string) (*domain.Branch, error)
	ListBranchesByDivergence(ctx context.Context, divergenceID string) ([]domain.Branch, error)
}

// ActorAssigner assigns work to actors and processes their results.
type ActorAssigner interface {
	DeliverAssignment(ctx context.Context, req actor.AssignmentRequest) error
	ProcessResult(ctx context.Context, req actor.AssignmentRequest, result actor.AssignmentResult) error
}

// ArtifactReader reads artifacts from the repository.
type ArtifactReader interface {
	Read(ctx context.Context, path, ref string) (*domain.Artifact, error)
}

// EventEmitter emits domain events during orchestration.
type EventEmitter interface {
	Emit(ctx context.Context, event domain.Event) error
}

// WorkflowLoader loads a workflow definition from Git at a specific version.
type WorkflowLoader interface {
	LoadWorkflow(ctx context.Context, path, ref string) (*domain.WorkflowDefinition, error)
}

// CrossArtifactValidator runs cross-artifact validation rules against a single artifact.
type CrossArtifactValidator interface {
	Validate(ctx context.Context, artifactPath string) domain.ValidationResult
}

// DivergenceHandler manages divergence lifecycle for the orchestrator.
type DivergenceHandler interface {
	StartDivergence(ctx context.Context, run *domain.Run, divDef domain.DivergenceDefinition, convergenceID string) (*domain.DivergenceContext, error)
	CreateExploratoryBranch(ctx context.Context, divCtx *domain.DivergenceContext, branchID, startStep string) (*domain.Branch, error)
	CloseWindow(ctx context.Context, divCtx *domain.DivergenceContext) error
}

// ConvergenceHandler manages convergence lifecycle for the orchestrator.
type ConvergenceHandler interface {
	CheckEntryPolicy(ctx context.Context, divCtx *domain.DivergenceContext, convDef domain.ConvergenceDefinition) (bool, error)
	EvaluateAndCommit(ctx context.Context, divCtx *domain.DivergenceContext, convDef domain.ConvergenceDefinition) error
}

// DiscussionChecker checks discussion thread state for precondition evaluation.
type DiscussionChecker interface {
	HasOpenThreads(ctx context.Context, anchorType domain.AnchorType, anchorID string) (bool, error)
}

// ArtifactWriter creates artifacts on branches during planning runs.
// Per ADR-006 §2.
type ArtifactWriter interface {
	Create(ctx context.Context, path, content string) (*artifact.WriteResult, error)
}

// GitOperator provides Git operations needed for run-level branching and commits.
type GitOperator interface {
	Commit(ctx context.Context, opts git.CommitOpts) (git.CommitResult, error)
	Merge(ctx context.Context, opts git.MergeOpts) (git.MergeResult, error)
	CreateBranch(ctx context.Context, name, base string) error
	DeleteBranch(ctx context.Context, name string) error
	Head(ctx context.Context) (string, error)
}
