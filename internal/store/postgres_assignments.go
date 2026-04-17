package store

import (
	"context"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/jackc/pgx/v5"
)

// ── Assignment Tracking ──

func (s *PostgresStore) CreateAssignment(ctx context.Context, a *domain.Assignment) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO runtime.actor_assignments (assignment_id, run_id, execution_id, actor_id, status, assigned_at, timeout_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		a.AssignmentID, a.RunID, a.ExecutionID, a.ActorID, a.Status, a.AssignedAt, a.TimeoutAt,
	)
	return err
}

func (s *PostgresStore) UpdateAssignmentStatus(ctx context.Context, assignmentID string, status domain.AssignmentStatus, respondedAt *time.Time) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE runtime.actor_assignments SET status = $1, responded_at = $2
		WHERE assignment_id = $3 AND status = 'active'`,
		status, respondedAt, assignmentID,
	)
	if err != nil {
		return err
	}
	return mustAffect(tag, "assignment not found")
}

func (s *PostgresStore) GetAssignment(ctx context.Context, assignmentID string) (*domain.Assignment, error) {
	var a domain.Assignment
	err := s.pool.QueryRow(ctx, `
		SELECT assignment_id, run_id, execution_id, actor_id, status, assigned_at, responded_at, timeout_at
		FROM runtime.actor_assignments WHERE assignment_id = $1`, assignmentID,
	).Scan(&a.AssignmentID, &a.RunID, &a.ExecutionID, &a.ActorID, &a.Status, &a.AssignedAt, &a.RespondedAt, &a.TimeoutAt)
	if err != nil {
		return nil, notFoundOr(err, "assignment not found")
	}
	return &a, nil
}

func (s *PostgresStore) ListAssignmentsByActor(ctx context.Context, actorID string, status *domain.AssignmentStatus) ([]domain.Assignment, error) {
	var rows pgx.Rows
	var err error
	if status != nil {
		rows, err = s.pool.Query(ctx, `
			SELECT assignment_id, run_id, execution_id, actor_id, status, assigned_at, responded_at, timeout_at
			FROM runtime.actor_assignments WHERE actor_id = $1 AND status = $2 ORDER BY assigned_at DESC`, actorID, *status,
		)
	} else {
		rows, err = s.pool.Query(ctx, `
			SELECT assignment_id, run_id, execution_id, actor_id, status, assigned_at, responded_at, timeout_at
			FROM runtime.actor_assignments WHERE actor_id = $1 ORDER BY assigned_at DESC`, actorID,
		)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var assignments []domain.Assignment
	for rows.Next() {
		var a domain.Assignment
		if err := rows.Scan(&a.AssignmentID, &a.RunID, &a.ExecutionID, &a.ActorID, &a.Status, &a.AssignedAt, &a.RespondedAt, &a.TimeoutAt); err != nil {
			return nil, err
		}
		assignments = append(assignments, a)
	}
	return assignments, rows.Err()
}

func (s *PostgresStore) ListExpiredAssignments(ctx context.Context, before time.Time) ([]domain.Assignment, error) {
	return queryAll(ctx, s.pool, `
		SELECT assignment_id, run_id, execution_id, actor_id, status, assigned_at, responded_at, timeout_at
		FROM runtime.actor_assignments WHERE status = 'active' AND timeout_at IS NOT NULL AND timeout_at < $1
		ORDER BY timeout_at`,
		[]any{before},
		func(row pgx.Rows, a *domain.Assignment) error {
			return row.Scan(&a.AssignmentID, &a.RunID, &a.ExecutionID, &a.ActorID, &a.Status, &a.AssignedAt, &a.RespondedAt, &a.TimeoutAt)
		},
	)
}
