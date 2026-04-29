package engine

import (
	"fmt"

	"github.com/bszymi/spine/internal/branchprotect"
)

// Orchestrator wires the workflow engine, store, actor gateway, artifact service,
// event router, and Git client into a single execution coordinator. It manages
// run lifecycle, step progression, and outcome routing.
type Orchestrator struct {
	workflows      WorkflowResolver
	store          RunStore
	actors         ActorAssigner
	artifacts      ArtifactReader
	events         EventEmitter
	git            GitOperator
	wfLoader       WorkflowLoader
	assignments    AssignmentStore        // optional, nil if not configured
	actorSelector  ActorSelector          // optional, nil if not configured
	validator      CrossArtifactValidator // optional, nil if not configured
	discussions    DiscussionChecker      // optional, nil if not configured
	divergence     DivergenceHandler      // optional, nil if not configured
	convergence    ConvergenceHandler     // optional, nil if not configured
	artifactWriter ArtifactWriter         // optional, required for planning runs
	workflowWriter WorkflowWriter         // optional, required for workflow planning runs (ADR-008)
	blocking       BlockingStore          // optional, nil if not configured
	collision      CollisionHandler       // optional, nil if not configured
	repositories   RepositoryResolver     // optional, gates run start on repository availability
	repoClients    RepositoryGitClients   // optional, resolves per-repo git clients for multi-repo branch creation
	policy         branchprotect.Policy   // branch-protection guard for MergeRunBranch (ADR-009 §3)
}

// New creates an Orchestrator with all required dependencies.
func New(
	workflows WorkflowResolver,
	store RunStore,
	actors ActorAssigner,
	artifacts ArtifactReader,
	events EventEmitter,
	gitOp GitOperator,
	wfLoader WorkflowLoader,
) (*Orchestrator, error) {
	if workflows == nil {
		return nil, fmt.Errorf("engine: workflows resolver is required")
	}
	if store == nil {
		return nil, fmt.Errorf("engine: run store is required")
	}
	if actors == nil {
		return nil, fmt.Errorf("engine: actor assigner is required")
	}
	if artifacts == nil {
		return nil, fmt.Errorf("engine: artifact reader is required")
	}
	if events == nil {
		return nil, fmt.Errorf("engine: event emitter is required")
	}
	if gitOp == nil {
		return nil, fmt.Errorf("engine: git operator is required")
	}
	if wfLoader == nil {
		return nil, fmt.Errorf("engine: workflow loader is required")
	}

	return &Orchestrator{
		workflows: workflows,
		store:     store,
		actors:    actors,
		artifacts: artifacts,
		events:    events,
		git:       gitOp,
		wfLoader:  wfLoader,
	}, nil
}

// WithAssignmentStore enables assignment tracking on the orchestrator.
func (o *Orchestrator) WithAssignmentStore(s AssignmentStore) {
	o.assignments = s
}

// WithActorSelector enables automatic actor resolution for automated/ai-only steps.
func (o *Orchestrator) WithActorSelector(s ActorSelector) {
	o.actorSelector = s
}

// WithValidator enables cross-artifact validation for step preconditions.
func (o *Orchestrator) WithValidator(v CrossArtifactValidator) {
	o.validator = v
}

// WithDiscussions enables discussion-based preconditions.
func (o *Orchestrator) WithDiscussions(d DiscussionChecker) {
	o.discussions = d
}

// WithDivergence enables divergence handling for workflow branching.
func (o *Orchestrator) WithDivergence(d DivergenceHandler) {
	o.divergence = d
}

// WithConvergence enables convergence handling for branch merging.
func (o *Orchestrator) WithConvergence(c ConvergenceHandler) {
	o.convergence = c
}

// WithArtifactWriter enables artifact creation for planning runs.
func (o *Orchestrator) WithArtifactWriter(w ArtifactWriter) {
	o.artifactWriter = w
}

// WithWorkflowWriter enables workflow-definition writes for workflow planning
// runs (ADR-008). Required only for callers that use StartWorkflowPlanningRun.
func (o *Orchestrator) WithWorkflowWriter(w WorkflowWriter) {
	o.workflowWriter = w
}

// WithBlockingStore enables dependency blocking detection.
func (o *Orchestrator) WithBlockingStore(b BlockingStore) {
	o.blocking = b
}

// WithCollisionHandler enables artifact ID collision detection and renumbering during merge.
func (o *Orchestrator) WithCollisionHandler(c CollisionHandler) {
	o.collision = c
}

// WithRepositoryResolver enables run-start preconditions that gate the
// run on every declared repository being resolvable to an active
// runtime binding (INIT-014 EPIC-002 TASK-004). When nil, the
// preconditions are skipped and StartRun behaves as before — useful
// for tests and for production paths that haven't yet wired the
// registry. The check fires after blocking and before the run branch
// is created so a failed precondition leaves no orphan branch.
func (o *Orchestrator) WithRepositoryResolver(r RepositoryResolver) {
	o.repositories = r
}

// WithRepositoryGitClients enables multi-repository branch creation at
// run start (INIT-014 EPIC-004 TASK-002). When a run's
// AffectedRepositories includes any non-primary repo, the orchestrator
// uses this resolver to fetch each code repo's git.GitClient and
// create the same run branch there from that repo's default branch.
// Without this resolver, multi-repo runs fail at start with a
// precondition error so a partial wiring never silently degrades to
// primary-only execution; primary-only runs are unaffected.
func (o *Orchestrator) WithRepositoryGitClients(c RepositoryGitClients) {
	o.repoClients = c
}

// WithBranchProtectPolicy installs the branch-protection policy consulted by
// MergeRunBranch before advancing the authoritative ref (ADR-009 §3). A nil
// policy is fail-closed at the merge boundary — MergeRunBranch refuses to
// merge until one is wired. Production wires the projection-backed policy
// in cmd/spine; tests install branchprotect.NewPermissive() (or a specific
// rule source) to match the behaviour they want to exercise.
//
// spine/* branches (run branches, divergence branches, scheduler-managed
// refs) are out of scope of user-authored rules by construction (ADR-009 §3),
// so there is no guard on the run-branch-creation / divergence paths; the
// only authoritative ref the Orchestrator advances is main-via-merge.
func (o *Orchestrator) WithBranchProtectPolicy(p branchprotect.Policy) {
	o.policy = p
}
