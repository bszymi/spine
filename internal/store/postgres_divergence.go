package store

import (
	"context"

	"github.com/bszymi/spine/internal/domain"
	"github.com/jackc/pgx/v5"
)

// ── Divergence ──

func (s *PostgresStore) CreateDivergenceContext(ctx context.Context, div *domain.DivergenceContext) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO runtime.divergence_contexts (divergence_id, run_id, status, divergence_mode, divergence_window, min_branches, max_branches, convergence_id, triggered_at, resolved_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		div.DivergenceID, div.RunID, div.Status, div.DivergenceMode, div.DivergenceWindow,
		div.MinBranches, div.MaxBranches, nilIfEmpty(div.ConvergenceID), div.TriggeredAt, div.ResolvedAt,
	)
	return err
}

func (s *PostgresStore) UpdateDivergenceContext(ctx context.Context, div *domain.DivergenceContext) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE runtime.divergence_contexts SET status = $1, divergence_window = $2, min_branches = $3, max_branches = $4, triggered_at = $5, resolved_at = $6
		WHERE divergence_id = $7`,
		div.Status, div.DivergenceWindow, div.MinBranches, div.MaxBranches, div.TriggeredAt, div.ResolvedAt, div.DivergenceID,
	)
	if err != nil {
		return err
	}
	return mustAffect(tag, "divergence context not found")
}

func (s *PostgresStore) GetDivergenceContext(ctx context.Context, divergenceID string) (*domain.DivergenceContext, error) {
	var div domain.DivergenceContext
	var convergenceID *string
	err := s.pool.QueryRow(ctx, `
		SELECT divergence_id, run_id, status, divergence_mode, divergence_window, min_branches, max_branches, convergence_id, triggered_at, resolved_at
		FROM runtime.divergence_contexts WHERE divergence_id = $1`, divergenceID,
	).Scan(&div.DivergenceID, &div.RunID, &div.Status, &div.DivergenceMode, &div.DivergenceWindow,
		&div.MinBranches, &div.MaxBranches, &convergenceID, &div.TriggeredAt, &div.ResolvedAt)
	if err != nil {
		return nil, notFoundOr(err, "divergence context not found")
	}
	if convergenceID != nil {
		div.ConvergenceID = *convergenceID
	}
	return &div, nil
}

func (s *PostgresStore) CreateBranch(ctx context.Context, branch *domain.Branch) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO runtime.branches (branch_id, run_id, divergence_id, status, current_step_id, outcome, artifacts_produced, completed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		branch.BranchID, branch.RunID, branch.DivergenceID, branch.Status,
		nilIfEmpty(branch.CurrentStepID), branch.Outcome, branch.ArtifactsProduced, branch.CompletedAt,
	)
	return err
}

func (s *PostgresStore) UpdateBranch(ctx context.Context, branch *domain.Branch) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE runtime.branches SET status = $1, current_step_id = $2, outcome = $3, artifacts_produced = $4, completed_at = $5
		WHERE branch_id = $6`,
		branch.Status, nilIfEmpty(branch.CurrentStepID), branch.Outcome, branch.ArtifactsProduced,
		branch.CompletedAt, branch.BranchID,
	)
	if err != nil {
		return err
	}
	return mustAffect(tag, "branch not found")
}

func (s *PostgresStore) GetBranch(ctx context.Context, branchID string) (*domain.Branch, error) {
	var branch domain.Branch
	var currentStepID *string
	err := s.pool.QueryRow(ctx, `
		SELECT branch_id, run_id, divergence_id, status, current_step_id, outcome, artifacts_produced, created_at, completed_at
		FROM runtime.branches WHERE branch_id = $1`, branchID,
	).Scan(
		&branch.BranchID, &branch.RunID, &branch.DivergenceID, &branch.Status,
		&currentStepID, &branch.Outcome, &branch.ArtifactsProduced,
		&branch.CreatedAt, &branch.CompletedAt,
	)
	if err != nil {
		return nil, notFoundOr(err, "branch not found")
	}
	if currentStepID != nil {
		branch.CurrentStepID = *currentStepID
	}
	return &branch, nil
}

func (s *PostgresStore) ListBranchesByDivergence(ctx context.Context, divergenceID string) ([]domain.Branch, error) {
	return queryAll(ctx, s.pool, `
		SELECT branch_id, run_id, divergence_id, status, current_step_id, outcome, artifacts_produced, created_at, completed_at
		FROM runtime.branches WHERE divergence_id = $1 ORDER BY created_at`,
		[]any{divergenceID},
		func(row pgx.Rows, b *domain.Branch) error {
			var currentStepID *string
			if err := row.Scan(&b.BranchID, &b.RunID, &b.DivergenceID, &b.Status,
				&currentStepID, &b.Outcome, &b.ArtifactsProduced, &b.CreatedAt, &b.CompletedAt); err != nil {
				return err
			}
			if currentStepID != nil {
				b.CurrentStepID = *currentStepID
			}
			return nil
		},
	)
}
