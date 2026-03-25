package engine

import (
	"context"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
)

// AssignmentStore provides assignment persistence for the orchestrator.
type AssignmentStore interface {
	CreateAssignment(ctx context.Context, a *domain.Assignment) error
	UpdateAssignmentStatus(ctx context.Context, assignmentID string, status domain.AssignmentStatus, respondedAt *time.Time) error
	GetAssignment(ctx context.Context, assignmentID string) (*domain.Assignment, error)
	ListExpiredAssignments(ctx context.Context, before time.Time) ([]domain.Assignment, error)
}

// TrackAssignment creates an assignment record when a step is activated.
func (o *Orchestrator) TrackAssignment(ctx context.Context, assignmentID, runID, executionID, actorID string, timeout time.Duration) {
	if o.assignments == nil {
		return
	}

	now := time.Now()
	a := &domain.Assignment{
		AssignmentID: assignmentID,
		RunID:        runID,
		ExecutionID:  executionID,
		ActorID:      actorID,
		Status:       domain.AssignmentStatusActive,
		AssignedAt:   now,
	}
	if timeout > 0 {
		t := now.Add(timeout)
		a.TimeoutAt = &t
	}

	if err := o.assignments.CreateAssignment(ctx, a); err != nil {
		observe.Logger(ctx).Warn("failed to track assignment", "assignment_id", assignmentID, "error", err)
	}
}

// CompleteAssignment marks an assignment as completed when a result is submitted.
func (o *Orchestrator) CompleteAssignment(ctx context.Context, assignmentID string) {
	if o.assignments == nil {
		return
	}

	now := time.Now()
	if err := o.assignments.UpdateAssignmentStatus(ctx, assignmentID, domain.AssignmentStatusCompleted, &now); err != nil {
		observe.Logger(ctx).Warn("failed to complete assignment", "assignment_id", assignmentID, "error", err)
	}
}

// ExpireAssignments marks timed-out assignments as expired.
func (o *Orchestrator) ExpireAssignments(ctx context.Context) (int, error) {
	if o.assignments == nil {
		return 0, nil
	}

	expired, err := o.assignments.ListExpiredAssignments(ctx, time.Now())
	if err != nil {
		return 0, err
	}

	log := observe.Logger(ctx)
	count := 0
	for i := range expired {
		now := time.Now()
		if err := o.assignments.UpdateAssignmentStatus(ctx, expired[i].AssignmentID, domain.AssignmentStatusTimedOut, &now); err != nil {
			log.Warn("failed to expire assignment", "assignment_id", expired[i].AssignmentID, "error", err)
			continue
		}
		count++
		log.Info("assignment expired", "assignment_id", expired[i].AssignmentID, "actor_id", expired[i].ActorID)
	}
	return count, nil
}
