package store

import (
	"context"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/jackc/pgx/v5"
)

// ── Runs ──

func (s *PostgresStore) CreateRun(ctx context.Context, run *domain.Run) error {
	// Honor the missing-metadata fallback per INIT-014 EPIC-004 TASK-001
	// acceptance: any caller (including legacy harness callers that build a
	// domain.Run literal without populating the multi-repo fields) still
	// persists as a primary-repo-only run. Without this normalization the
	// explicit zero values would override the column defaults and persist
	// `affected_repositories = {}` with `primary_repository = false`.
	affected := run.AffectedRepositories
	primary := run.PrimaryRepository
	if len(affected) == 0 {
		affected = []string{domain.PrimaryRepositoryID}
		primary = true
	}
	branches := run.RepositoryBranches
	if branches == nil {
		branches = map[string]string{}
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO runtime.runs (run_id, task_path, workflow_path, workflow_id, workflow_version, workflow_version_label, status, current_step_id, branch_name, trace_id, timeout_at, started_at, completed_at, created_at, mode, affected_repositories, primary_repository, repository_branches)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)`,
		run.RunID, run.TaskPath, run.WorkflowPath, run.WorkflowID, run.WorkflowVersion,
		run.WorkflowVersionLabel, run.Status, nilIfEmpty(run.CurrentStepID), nilIfEmpty(run.BranchName), run.TraceID,
		run.TimeoutAt, run.StartedAt, run.CompletedAt, run.CreatedAt, modeOrDefault(run.Mode),
		affected, primary, branches,
	)
	return err
}

// runColumns is the standard column list for runtime.runs queries. It is
// shared by every read path so a schema addition only requires updating
// scanRun and this constant.
const runColumns = `run_id, task_path, workflow_path, workflow_id, workflow_version, workflow_version_label, status, current_step_id, branch_name, trace_id, timeout_at, started_at, completed_at, created_at, mode, commit_meta, affected_repositories, primary_repository, repository_branches`

// scanRun scans a single row into a domain.Run, handling nullable columns.
func scanRun(scanner interface{ Scan(dest ...any) error }) (domain.Run, error) {
	var run domain.Run
	var currentStepID, branchName *string
	var commitMeta map[string]string
	var affected []string
	var branches map[string]string
	err := scanner.Scan(
		&run.RunID, &run.TaskPath, &run.WorkflowPath, &run.WorkflowID, &run.WorkflowVersion,
		&run.WorkflowVersionLabel, &run.Status, &currentStepID, &branchName, &run.TraceID,
		&run.TimeoutAt, &run.StartedAt, &run.CompletedAt, &run.CreatedAt, &run.Mode, &commitMeta,
		&affected, &run.PrimaryRepository, &branches,
	)
	if err != nil {
		return run, err
	}
	if currentStepID != nil {
		run.CurrentStepID = *currentStepID
	}
	if branchName != nil {
		run.BranchName = *branchName
	}
	run.CommitMeta = commitMeta
	if len(affected) > 0 {
		run.AffectedRepositories = affected
	}
	if len(branches) > 0 {
		run.RepositoryBranches = branches
	}
	return run, nil
}

// scanRuns scans multiple rows into a slice of domain.Run.
func scanRuns(rows pgx.Rows) ([]domain.Run, error) {
	defer rows.Close()
	var runs []domain.Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (s *PostgresStore) GetRun(ctx context.Context, runID string) (*domain.Run, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+runColumns+` FROM runtime.runs WHERE run_id = $1`, runID)
	run, err := scanRun(row)
	if err != nil {
		return nil, notFoundOr(err, "run not found")
	}
	return &run, nil
}

func (s *PostgresStore) UpdateRunStatus(ctx context.Context, runID string, status domain.RunStatus) error {
	// Set started_at on first activation (pending→active), completed_at on terminal states.
	tag, err := s.pool.Exec(ctx, `
		UPDATE runtime.runs
		SET status = $1,
			started_at = CASE WHEN $1 = 'active' AND started_at IS NULL THEN now() ELSE started_at END,
			completed_at = CASE WHEN $1 IN ('completed', 'failed', 'cancelled') AND completed_at IS NULL THEN now() ELSE completed_at END
		WHERE run_id = $2`, status, runID)
	if err != nil {
		return err
	}
	return mustAffect(tag, "run not found")
}

func (s *PostgresStore) TransitionRunStatus(ctx context.Context, runID string, fromStatus, toStatus domain.RunStatus) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE runtime.runs
		SET status = $1,
			started_at = CASE WHEN $1 = 'active' AND started_at IS NULL THEN now() ELSE started_at END,
			completed_at = CASE WHEN $1 IN ('completed', 'failed', 'cancelled') AND completed_at IS NULL THEN now() ELSE completed_at END
		WHERE run_id = $2 AND status = $3`, toStatus, runID, fromStatus)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func (s *PostgresStore) SetCommitMeta(ctx context.Context, runID string, meta map[string]string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE runtime.runs SET commit_meta = $1 WHERE run_id = $2`, meta, runID)
	if err != nil {
		return err
	}
	return mustAffect(tag, "run not found")
}

func (s *PostgresStore) UpdateCurrentStep(ctx context.Context, runID, stepID string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE runtime.runs SET current_step_id = $1 WHERE run_id = $2`, stepID, runID)
	if err != nil {
		return err
	}
	return mustAffect(tag, "run not found")
}

func (s *PostgresStore) ListRunsByTask(ctx context.Context, taskPath string) ([]domain.Run, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+runColumns+` FROM runtime.runs WHERE task_path = $1 ORDER BY created_at DESC`, taskPath)
	if err != nil {
		return nil, err
	}
	return scanRuns(rows)
}

// ── Scheduler Queries (runs) ──
func (s *PostgresStore) ListRunsByStatus(ctx context.Context, status domain.RunStatus) ([]domain.Run, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+runColumns+` FROM runtime.runs WHERE status = $1 ORDER BY created_at`, status)
	if err != nil {
		return nil, err
	}
	return scanRuns(rows)
}
func (s *PostgresStore) ListStaleActiveRuns(ctx context.Context, noActivitySince time.Time) ([]domain.Run, error) {
	// A run is stale if no step execution has recent activity. The
	// projected column list comes from runColumns so this query stays in
	// lockstep with scanRun whenever a column is added.
	rows, err := s.pool.Query(ctx, `
		SELECT `+runColumns+`
		FROM runtime.runs
		WHERE status = 'active'
		AND NOT EXISTS (
			SELECT 1 FROM runtime.step_executions se
			WHERE se.run_id = runtime.runs.run_id
			AND GREATEST(se.created_at, COALESCE(se.started_at, se.created_at), COALESCE(se.completed_at, se.created_at)) > $1
		)
		AND created_at < $1
		ORDER BY created_at`, noActivitySince)
	if err != nil {
		return nil, err
	}
	return scanRuns(rows)
}

func (s *PostgresStore) ListTimedOutRuns(ctx context.Context, now time.Time) ([]domain.Run, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+runColumns+`
		FROM runtime.runs
		WHERE status IN ('active', 'paused')
		AND timeout_at IS NOT NULL
		AND timeout_at <= $1
		ORDER BY timeout_at`, now)
	if err != nil {
		return nil, err
	}
	return scanRuns(rows)
}
