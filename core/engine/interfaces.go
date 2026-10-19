package engine

import (
	"context"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/repository"
	"github.com/bszymi/spine/core/actor"
	"github.com/bszymi/spine/core/artifact"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/workflow"
)

// WorkflowResolver resolves the governing workflow for a given artifact type.
type WorkflowResolver interface {
	ResolveWorkflow(ctx context.Context, artifactType, workType string) (*workflow.BindingResult, error)
	ResolveWorkflowForMode(ctx context.Context, artifactType, workType, mode string) (*workflow.BindingResult, error)
}

// RunStore provides run and step execution persistence required by the orchestrator.
type RunStore interface {
	// Runs
	CreateRun(ctx context.Context, run *domain.Run) error
	GetRun(ctx context.Context, runID string) (*domain.Run, error)
	UpdateRunStatus(ctx context.Context, runID string, status domain.RunStatus) error
	// TransitionRunStatus atomically updates the run status only if it currently
	// matches fromStatus. Returns true if the transition was applied, false if
	// the run was already in a different state (no error in that case).
	TransitionRunStatus(ctx context.Context, runID string, fromStatus, toStatus domain.RunStatus) (bool, error)
	UpdateCurrentStep(ctx context.Context, runID, stepID string) error
	SetCommitMeta(ctx context.Context, runID string, meta map[string]string) error

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

	// Repository merge outcomes (INIT-014 EPIC-005). One row per
	// (run_id, repository_id); the orchestrator upserts an outcome
	// after each per-repo merge attempt so partial cross-repo states
	// are explicit and queryable rather than inferred from prose.
	UpsertRepositoryMergeOutcome(ctx context.Context, outcome *domain.RepositoryMergeOutcome) error
	GetRepositoryMergeOutcome(ctx context.Context, runID, repositoryID string) (*domain.RepositoryMergeOutcome, error)
	ListRepositoryMergeOutcomes(ctx context.Context, runID string) ([]domain.RepositoryMergeOutcome, error)
}

// ActorAssigner assigns work to actors and processes their results.
type ActorAssigner interface {
	DeliverAssignment(ctx context.Context, req actor.AssignmentRequest) error
	ProcessResult(ctx context.Context, req actor.AssignmentRequest, result actor.AssignmentResult) error
}

// ActorSelector selects an eligible actor based on type, skills, and strategy.
// Used for automatic actor resolution when activating automated or ai-only steps.
type ActorSelector interface {
	SelectActor(ctx context.Context, req actor.SelectionRequest) (*domain.Actor, error)
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

// RepositoryResolver resolves a workspace repository ID to its merged
// catalog/binding view at run-start time, returning the typed errors
// from the repository package (ErrRepositoryNotFound,
// ErrRepositoryUnbound, ErrRepositoryInactive) so the orchestrator can
// classify failures.
//
// This is the single point where the orchestrator consults runtime
// state for repository availability; validate-time catalog existence
// checks live in the validation engine (RE-001) and run earlier.
type RepositoryResolver interface {
	Lookup(ctx context.Context, id string) (*repository.Repository, error)
}

// RepositoryGitClients hands back a git.GitClient for a workspace
// repository ID — the per-repo clone for code repos, the primary
// client for "spine". Required when the orchestrator starts a run
// whose AffectedRepositories include any non-primary repo: branch
// creation must hit each code repo's working tree, not just the
// primary's. Production wires this to *gitpool.Pool, which already
// implements Client(ctx, id) with lazy clone, caching, and credential
// resolution; tests inject a fake.
//
// Without this resolver the orchestrator falls back to primary-only
// branching — backward-compatible with single-repo deployments and
// every test that stubs only o.git.
type RepositoryGitClients interface {
	Client(ctx context.Context, repositoryID string) (git.GitClient, error)
}

// DivergenceHandler manages divergence lifecycle for the orchestrator.
//
// CreateExploratoryBranch takes the divergence's run (INIT-023 TASK-012):
// the new branch forks from its run branch and is refused for any other
// run.
type DivergenceHandler interface {
	StartDivergence(ctx context.Context, run *domain.Run, divDef domain.DivergenceDefinition, convergenceID string) (*domain.DivergenceContext, error)
	CreateExploratoryBranch(ctx context.Context, run *domain.Run, divCtx *domain.DivergenceContext, branchID, startStep string) (*domain.Branch, error)
	CloseWindow(ctx context.Context, divCtx *domain.DivergenceContext) error
}

// ConvergenceHandler manages convergence lifecycle for the orchestrator.
//
// EvaluateAndCommit takes the divergence's run (INIT-023 TASK-012, audit
// F8): the selected branches are merged into run.BranchName — never into
// whatever a working tree has checked out — and a run that is not the
// divergence's, or has no spine/ branch, is refused before anything
// changes.
type ConvergenceHandler interface {
	CheckEntryPolicy(ctx context.Context, divCtx *domain.DivergenceContext, convDef domain.ConvergenceDefinition) (bool, error)
	EvaluateAndCommit(ctx context.Context, run *domain.Run, divCtx *domain.DivergenceContext, convDef domain.ConvergenceDefinition) error
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

// WorkflowWriter creates and updates workflow definitions on branches during
// workflow planning runs (ADR-008). The shape mirrors ArtifactWriter but
// targets YAML workflow files — which are routed through the separate
// workflow.Service (ADR-007) — rather than Markdown artifacts.
// *workflow.Service implements it.
//
// Update was added by INIT-023 TASK-014 for update-capable lifecycle runs
// (StartWorkflowUpdatePlanningRun). Its only implementer in Spine is
// *workflow.Service; SMP neither implements this interface nor calls
// WithWorkflowWriter.
type WorkflowWriter interface {
	Create(ctx context.Context, id, body string) (*workflow.WriteResult, error)
	Update(ctx context.Context, id, body string) (*workflow.WriteResult, error)
}

// CollisionHandler detects and resolves artifact ID collisions during merge.
// Used by planning runs when a merge fails due to a path conflict. It runs
// after the primary merge block has released the working tree, so an
// implementation that changes the shared tree must run its git sequence
// through git.WorkingTreeGuard (INIT-023 TASK-012). Neither product wires
// one today.
type CollisionHandler interface {
	// DetectAndRenumber checks if a merge conflict is an ID collision and,
	// if so, renumbers the artifact on the branch. Returns the new artifact path
	// if renumbered, or empty string if not an ID collision. Max retries limits
	// renumber attempts.
	DetectAndRenumber(ctx context.Context, run *domain.Run, maxRetries int) (newArtifactPath string, err error)
}

// GitOperator provides Git operations needed for run-level branching and commits.
type GitOperator interface {
	Checkout(ctx context.Context, branch string) error
	Commit(ctx context.Context, opts git.CommitOpts) (git.CommitResult, error)
	Merge(ctx context.Context, opts git.MergeOpts) (git.MergeResult, error)
	CreateBranch(ctx context.Context, name, base string) error
	DeleteBranch(ctx context.Context, name string) error
	Diff(ctx context.Context, from, to string) ([]git.FileDiff, error)
	MergeBase(ctx context.Context, a, b string) (string, error)
	Head(ctx context.Context) (string, error)
	Push(ctx context.Context, remote, ref string) error
	PushBranch(ctx context.Context, remote, branch string) error
	DeleteRemoteBranch(ctx context.Context, remote, branch string) error
	ReadFile(ctx context.Context, ref, path string) ([]byte, error)
	WriteAndStageFile(ctx context.Context, path, content string) error
}

// CommitBinder is the optional capability SHA-bound governed merges need
// (INIT-023 TASK-005; audit F2 merge half, P3-37). It is not part of
// GitOperator, so embedders whose operator lacks it keep compiling and
// keep today's behaviour; EnableMergeBinding type-asserts it on the
// GitOperator (and, for code repositories, on each per-repository
// client) and refuses to turn binding on without it.
//
// Every object id is a full lowercase hex id (40 or 64 digits). The
// shared conformance suite in core/engine/enginetest pins the contract
// for any implementation (*git.CLIClient passes it).
type CommitBinder interface {
	// ResolveBranchHead returns the full object id that refs/heads/<branch>
	// points at in the repository merges read from (origin, for
	// remote-backed operators). It never applies DWIM: a refs/tags/<branch>
	// or any other shadow must not answer for the branch, and a missing
	// branch is an error.
	ResolveBranchHead(ctx context.Context, branch string) (string, error)
	// VerifyCommit returns nil only for a full lowercase hex object id that
	// names a commit object available to MergeBound and CommitFilesOnto.
	VerifyCommit(ctx context.Context, sha string) error
	// IsAncestor reports whether ancestor is reachable from descendant (a
	// commit is its own ancestor). A missing object is an error.
	IsAncestor(ctx context.Context, ancestor, descendant string) (bool, error)
	// CommitParents returns the full ids of a commit's parents, in order.
	CommitParents(ctx context.Context, sha string) ([]string, error)
	// CommitFilesOnto creates a commit whose only parent is base and whose
	// tree is base's tree with files replaced. It moves no ref and touches
	// neither HEAD, the index nor a working tree, and it is deterministic:
	// the same base, files and options always give the same commit.
	CommitFilesOnto(ctx context.Context, base string, files map[string]string, opts git.CommitOpts) (git.CommitResult, error)
	// MergeBound merges exactly opts.SourceSHA (required) into opts.Target
	// and reports MergeResult.BaseSHA, the target tip the merge was
	// computed on. A replay of a landed merge is "already up to date"
	// (SHA == BaseSHA).
	MergeBound(ctx context.Context, opts git.MergeOpts) (git.MergeResult, error)
	// PushCommit sets <remote> refs/heads/<branch> to exactly sha with a
	// non-forced push of the object (not of a local ref): the remote
	// accepts it only as a fast-forward or when it already holds sha.
	PushCommit(ctx context.Context, remote, branch, sha string) error
	// ResetBranch takes back a merge the engine refused after MergeBound
	// moved the local target: it moves the local refs/heads/<branch> to
	// `to` only if it is still at expected (compare-and-swap), and a
	// working tree that has the branch checked out ends up clean at to's
	// tree; a branch that is not at expected is left alone and reported as
	// an error. It never touches a remote. A remote-backed implementation
	// whose local branch only caches the remote may instead drop the
	// local branch under the same check, so the next MergeBound re-creates
	// it from the remote (which never received the refused merge). Either
	// way the working tree is left clean (nothing of the refused merge
	// staged or checked out), and the next MergeBound on branch must not
	// compute on expected. The conformance suite accepts both forms.
	ResetBranch(ctx context.Context, branch, to, expected string) error
}
