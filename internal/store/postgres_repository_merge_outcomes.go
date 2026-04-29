package store

import (
	"context"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/jackc/pgx/v5"
)

// ── Repository Merge Outcomes (INIT-014 EPIC-005 TASK-001) ──
//
// One row per (run_id, repository_id). Persistence mirrors the
// domain.RepositoryMergeOutcome shape; the upsert path is keyed on
// the composite primary key so callers can write the same outcome
// repeatedly (e.g. on retry) without first deleting the existing row.
// Read-side helpers expose the per-run lookup that EPIC-005 dashboards
// need; per-repo lookup is rare today but cheap to add later because
// the primary-key index already covers it.

const repositoryMergeOutcomeColumns = `
	run_id, repository_id, status, source_branch, target_branch,
	merge_commit_sha, ledger_commit_sha, failure_class, failure_detail,
	resolved_by, resolution_reason, attempts,
	created_at, updated_at, merged_at, last_attempted_at`

// UpsertRepositoryMergeOutcome inserts or replaces the outcome row
// keyed on (run_id, repository_id). Validation runs in the domain
// layer first so a malformed outcome is rejected with a clean
// SpineError rather than a Postgres CHECK violation.
func (s *PostgresStore) UpsertRepositoryMergeOutcome(ctx context.Context, outcome *domain.RepositoryMergeOutcome) error {
	if outcome == nil {
		return domain.NewError(domain.ErrInvalidParams, "merge outcome required")
	}
	if err := outcome.Validate(); err != nil {
		return err
	}

	// CreatedAt: zero value defers to the column default on INSERT. On
	// UPDATE we never overwrite the original creation timestamp, so the
	// ON CONFLICT clause omits created_at.
	var createdAt *time.Time
	if !outcome.CreatedAt.IsZero() {
		createdAt = &outcome.CreatedAt
	}

	_, err := s.pool.Exec(ctx, `
		INSERT INTO runtime.repository_merge_outcomes (
			run_id, repository_id, status, source_branch, target_branch,
			merge_commit_sha, ledger_commit_sha, failure_class, failure_detail,
			resolved_by, resolution_reason, attempts,
			created_at, updated_at, merged_at, last_attempted_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9,
			$10, $11, $12,
			COALESCE($13, now()), now(), $14, $15
		)
		ON CONFLICT (run_id, repository_id) DO UPDATE SET
			status            = EXCLUDED.status,
			source_branch     = EXCLUDED.source_branch,
			target_branch     = EXCLUDED.target_branch,
			merge_commit_sha  = EXCLUDED.merge_commit_sha,
			ledger_commit_sha = EXCLUDED.ledger_commit_sha,
			failure_class     = EXCLUDED.failure_class,
			failure_detail    = EXCLUDED.failure_detail,
			resolved_by       = EXCLUDED.resolved_by,
			resolution_reason = EXCLUDED.resolution_reason,
			attempts          = EXCLUDED.attempts,
			updated_at        = now(),
			merged_at         = EXCLUDED.merged_at,
			last_attempted_at = EXCLUDED.last_attempted_at`,
		outcome.RunID,
		outcome.RepositoryID,
		string(outcome.Status),
		outcome.SourceBranch,
		outcome.TargetBranch,
		nilIfEmpty(outcome.MergeCommitSHA),
		nilIfEmpty(outcome.LedgerCommitSHA),
		nilIfEmpty(string(outcome.FailureClass)),
		nilIfEmpty(outcome.FailureDetail),
		nilIfEmpty(outcome.ResolvedBy),
		nilIfEmpty(outcome.ResolutionReason),
		outcome.Attempts,
		createdAt,
		outcome.MergedAt,
		outcome.LastAttemptedAt,
	)
	return err
}

// GetRepositoryMergeOutcome returns one outcome by (run_id, repository_id).
func (s *PostgresStore) GetRepositoryMergeOutcome(ctx context.Context, runID, repositoryID string) (*domain.RepositoryMergeOutcome, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+repositoryMergeOutcomeColumns+`
		 FROM runtime.repository_merge_outcomes
		 WHERE run_id = $1 AND repository_id = $2`,
		runID, repositoryID,
	)
	out, err := scanRepositoryMergeOutcome(row)
	if err != nil {
		return nil, notFoundOr(err, "merge outcome not found")
	}
	return &out, nil
}

// ListRepositoryMergeOutcomes returns every outcome for the run, ordered
// by repository_id for stable dashboards. Returns an empty slice (not
// nil) when the run has no recorded outcomes yet.
func (s *PostgresStore) ListRepositoryMergeOutcomes(ctx context.Context, runID string) ([]domain.RepositoryMergeOutcome, error) {
	out, err := queryAll(ctx, s.pool,
		`SELECT `+repositoryMergeOutcomeColumns+`
		 FROM runtime.repository_merge_outcomes
		 WHERE run_id = $1
		 ORDER BY repository_id`,
		[]any{runID},
		func(rows pgx.Rows, dst *domain.RepositoryMergeOutcome) error {
			o, err := scanRepositoryMergeOutcome(rows)
			if err != nil {
				return err
			}
			*dst = o
			return nil
		},
	)
	if err != nil {
		return nil, err
	}
	if out == nil {
		return []domain.RepositoryMergeOutcome{}, nil
	}
	return out, nil
}

func scanRepositoryMergeOutcome(scanner interface{ Scan(dest ...any) error }) (domain.RepositoryMergeOutcome, error) {
	var o domain.RepositoryMergeOutcome
	var (
		status                                                                                  string
		mergeCommitSHA, ledgerCommitSHA, failureClass, failureDetail, resolvedBy, resolutionReason *string
	)
	err := scanner.Scan(
		&o.RunID,
		&o.RepositoryID,
		&status,
		&o.SourceBranch,
		&o.TargetBranch,
		&mergeCommitSHA,
		&ledgerCommitSHA,
		&failureClass,
		&failureDetail,
		&resolvedBy,
		&resolutionReason,
		&o.Attempts,
		&o.CreatedAt,
		&o.UpdatedAt,
		&o.MergedAt,
		&o.LastAttemptedAt,
	)
	if err != nil {
		return o, err
	}
	o.Status = domain.RepositoryMergeStatus(status)
	if mergeCommitSHA != nil {
		o.MergeCommitSHA = *mergeCommitSHA
	}
	if ledgerCommitSHA != nil {
		o.LedgerCommitSHA = *ledgerCommitSHA
	}
	if failureClass != nil {
		o.FailureClass = domain.MergeFailureClass(*failureClass)
	}
	if failureDetail != nil {
		o.FailureDetail = *failureDetail
	}
	if resolvedBy != nil {
		o.ResolvedBy = *resolvedBy
	}
	if resolutionReason != nil {
		o.ResolutionReason = *resolutionReason
	}
	return o, nil
}
