package workflow

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/bszymi/spine/internal/domain"
)

// ProjectionStore provides the subset of store operations needed by the provider.
type ProjectionStore interface {
	ListActiveWorkflowProjections(ctx context.Context) ([]WorkflowProjection, error)
}

// WorkflowProjection mirrors store.WorkflowProjection to avoid circular imports.
type WorkflowProjection struct {
	WorkflowPath string
	WorkflowID   string
	Name         string
	Version      string
	Status       string
	AppliesTo    []byte // JSONB
	Definition   []byte // JSONB
	SourceCommit string
}

// ProjectionWorkflowProvider implements WorkflowProvider using workflow projections.
type ProjectionWorkflowProvider struct {
	store ProjectionStore
}

// NewProjectionWorkflowProvider creates a provider backed by workflow projections.
func NewProjectionWorkflowProvider(store ProjectionStore) *ProjectionWorkflowProvider {
	return &ProjectionWorkflowProvider{store: store}
}

// NewProjectionProviderFromListFn creates a provider from a function that lists
// active workflow projections. This avoids circular imports between store and
// workflow packages by accepting a closure that adapts the store's return type.
func NewProjectionProviderFromListFn(listFn func(ctx context.Context) ([]WorkflowProjection, error)) *ProjectionWorkflowProvider {
	return &ProjectionWorkflowProvider{store: &fnAdapter{listFn: listFn}}
}

type fnAdapter struct {
	listFn func(ctx context.Context) ([]WorkflowProjection, error)
}

func (a *fnAdapter) ListActiveWorkflowProjections(ctx context.Context) ([]WorkflowProjection, error) {
	return a.listFn(ctx)
}

func (p *ProjectionWorkflowProvider) ListActiveWorkflows(ctx context.Context) ([]*domain.WorkflowDefinition, error) {
	projections, err := p.store.ListActiveWorkflowProjections(ctx)
	if err != nil {
		return nil, fmt.Errorf("list active workflow projections: %w", err)
	}

	var workflows []*domain.WorkflowDefinition
	for i := range projections {
		wf, err := projectionToDefinition(&projections[i])
		if err != nil {
			continue // skip unparseable projections
		}
		workflows = append(workflows, wf)
	}
	return workflows, nil
}

func projectionToDefinition(proj *WorkflowProjection) (*domain.WorkflowDefinition, error) {
	var wf domain.WorkflowDefinition
	if err := json.Unmarshal(proj.Definition, &wf); err != nil {
		return nil, fmt.Errorf("unmarshal workflow %s: %w", proj.WorkflowPath, err)
	}
	wf.Path = proj.WorkflowPath
	wf.CommitSHA = proj.SourceCommit
	return &wf, nil
}
