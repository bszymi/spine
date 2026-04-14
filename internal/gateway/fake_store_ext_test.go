package gateway_test

import (
	"context"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/store"
)

// Additional fakeStore methods for handlers not covered in gateway_test.go.

func (f *fakeStore) ListAssignmentsByActor(_ context.Context, actorID string, status *domain.AssignmentStatus) ([]domain.Assignment, error) {
	if f.listAssignmentsErr != nil {
		return nil, f.listAssignmentsErr
	}
	var result []domain.Assignment
	for _, a := range f.assignments {
		if a.ActorID != actorID {
			continue
		}
		if status != nil && a.Status != *status {
			continue
		}
		result = append(result, a)
	}
	return result, nil
}

func (f *fakeStore) QueryExecutionProjections(_ context.Context, _ store.ExecutionProjectionQuery) ([]store.ExecutionProjection, error) {
	if f.execProjErr != nil {
		return nil, f.execProjErr
	}
	return f.execProjs, nil
}

func (f *fakeStore) UpsertExecutionProjection(_ context.Context, _ *store.ExecutionProjection) error {
	return nil
}

func (f *fakeStore) GetExecutionProjection(_ context.Context, _ string) (*store.ExecutionProjection, error) {
	return nil, domain.NewError(domain.ErrNotFound, "not found")
}

func (f *fakeStore) DeleteExecutionProjection(_ context.Context, _ string) error {
	return nil
}

func (f *fakeStore) GetDivergenceContext(_ context.Context, divID string) (*domain.DivergenceContext, error) {
	if f.divContexts != nil {
		if dc, ok := f.divContexts[divID]; ok {
			return dc, nil
		}
	}
	return nil, domain.NewError(domain.ErrNotFound, "divergence not found")
}
