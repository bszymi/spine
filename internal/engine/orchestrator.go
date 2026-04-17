package engine

import "fmt"

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
