package engine

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/domain"
)

// ── In-memory assignment store for testing ──

type memAssignmentStore struct {
	assignments map[string]*domain.Assignment
}

func newMemAssignmentStore() *memAssignmentStore {
	return &memAssignmentStore{assignments: make(map[string]*domain.Assignment)}
}

func (s *memAssignmentStore) CreateAssignment(_ context.Context, a *domain.Assignment) error {
	// Simulate the unique index on (execution_id) WHERE status = 'active'.
	for _, existing := range s.assignments {
		if existing.ExecutionID == a.ExecutionID && existing.Status == domain.AssignmentStatusActive {
			return fmt.Errorf("duplicate active assignment for execution %s", a.ExecutionID)
		}
	}
	s.assignments[a.AssignmentID] = a
	return nil
}

func (s *memAssignmentStore) UpdateAssignmentStatus(_ context.Context, assignmentID string, status domain.AssignmentStatus, respondedAt *time.Time) error {
	a, ok := s.assignments[assignmentID]
	if !ok {
		return domain.NewError(domain.ErrNotFound, "assignment not found")
	}
	a.Status = status
	a.RespondedAt = respondedAt
	return nil
}

func (s *memAssignmentStore) GetAssignment(_ context.Context, assignmentID string) (*domain.Assignment, error) {
	a, ok := s.assignments[assignmentID]
	if !ok {
		return nil, domain.NewError(domain.ErrNotFound, "assignment not found")
	}
	return a, nil
}

func (s *memAssignmentStore) ListExpiredAssignments(_ context.Context, before time.Time) ([]domain.Assignment, error) {
	var result []domain.Assignment
	for _, a := range s.assignments {
		if a.Status == domain.AssignmentStatusActive && a.TimeoutAt != nil && a.TimeoutAt.Before(before) {
			result = append(result, *a)
		}
	}
	return result, nil
}

// ── Tests ──

func TestTrackAssignment(t *testing.T) {
	store := newMemAssignmentStore()
	orch := &Orchestrator{assignments: store}

	orch.TrackAssignment(context.Background(), "a-1", "run-1", "exec-1", "actor-1", 5*time.Minute)

	a, err := store.GetAssignment(context.Background(), "a-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.Status != domain.AssignmentStatusActive {
		t.Errorf("expected active, got %s", a.Status)
	}
	if a.ActorID != "actor-1" {
		t.Errorf("expected actor-1, got %s", a.ActorID)
	}
	if a.TimeoutAt == nil {
		t.Error("expected timeout to be set")
	}
}

func TestTrackAssignment_NoTimeout(t *testing.T) {
	store := newMemAssignmentStore()
	orch := &Orchestrator{assignments: store}

	orch.TrackAssignment(context.Background(), "a-2", "run-1", "exec-1", "actor-1", 0)

	a, _ := store.GetAssignment(context.Background(), "a-2")
	if a.TimeoutAt != nil {
		t.Error("expected no timeout")
	}
}

func TestTrackAssignment_NilStore(t *testing.T) {
	orch := &Orchestrator{} // no assignments store
	// Should not panic.
	orch.TrackAssignment(context.Background(), "a-1", "run-1", "exec-1", "actor-1", 0)
}

func TestCompleteAssignment(t *testing.T) {
	store := newMemAssignmentStore()
	orch := &Orchestrator{assignments: store}

	orch.TrackAssignment(context.Background(), "a-1", "run-1", "exec-1", "actor-1", 0)
	orch.CompleteAssignment(context.Background(), "a-1")

	a, _ := store.GetAssignment(context.Background(), "a-1")
	if a.Status != domain.AssignmentStatusCompleted {
		t.Errorf("expected completed, got %s", a.Status)
	}
	if a.RespondedAt == nil {
		t.Error("expected responded_at to be set")
	}
}

func TestCompleteAssignment_NilStore(t *testing.T) {
	orch := &Orchestrator{}
	orch.CompleteAssignment(context.Background(), "a-1") // should not panic
}

func TestExpireAssignments(t *testing.T) {
	store := newMemAssignmentStore()
	orch := &Orchestrator{assignments: store}

	past := time.Now().Add(-10 * time.Minute)
	future := time.Now().Add(10 * time.Minute)

	// Expired assignment.
	store.assignments["a-1"] = &domain.Assignment{
		AssignmentID: "a-1",
		Status:       domain.AssignmentStatusActive,
		TimeoutAt:    &past,
	}
	// Not expired.
	store.assignments["a-2"] = &domain.Assignment{
		AssignmentID: "a-2",
		Status:       domain.AssignmentStatusActive,
		TimeoutAt:    &future,
	}

	count, err := orch.ExpireAssignments(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 expired, got %d", count)
	}

	a1, _ := store.GetAssignment(context.Background(), "a-1")
	if a1.Status != domain.AssignmentStatusTimedOut {
		t.Errorf("expected timed_out, got %s", a1.Status)
	}
	a2, _ := store.GetAssignment(context.Background(), "a-2")
	if a2.Status != domain.AssignmentStatusActive {
		t.Errorf("expected active, got %s", a2.Status)
	}
}

func TestExpireAssignments_NilStore(t *testing.T) {
	orch := &Orchestrator{}
	count, err := orch.ExpireAssignments(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0, got %d", count)
	}
}
