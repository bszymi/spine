package engine

import "fmt"

// Orchestrator wires the workflow engine, store, actor gateway, artifact service,
// event router, and Git client into a single execution coordinator. It manages
// run lifecycle, step progression, and outcome routing.
type Orchestrator struct {
	workflows   WorkflowResolver
	store       RunStore
	actors      ActorAssigner
	artifacts   ArtifactReader
	events      EventEmitter
	git         GitOperator
	wfLoader    WorkflowLoader
	assignments AssignmentStore // optional, nil if not configured
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
