package store

import (
	"context"

	"github.com/bszymi/spine/internal/domain"
	"github.com/jackc/pgx/v5"
)

// ── Step Executions ──

func (s *PostgresStore) CreateStepExecution(ctx context.Context, exec *domain.StepExecution) error {
	eligibleActorIDs := exec.EligibleActorIDs
	if eligibleActorIDs == nil {
		eligibleActorIDs = []string{}
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO runtime.step_executions (execution_id, run_id, step_id, branch_id, actor_id, status, attempt, outcome_id, retry_after, started_at, completed_at, error_detail, created_at, eligible_actor_ids)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
		exec.ExecutionID, exec.RunID, exec.StepID, nilIfEmpty(exec.BranchID),
		nilIfEmpty(exec.ActorID), exec.Status, exec.Attempt, nilIfEmpty(exec.OutcomeID),
		exec.RetryAfter, exec.StartedAt, exec.CompletedAt, exec.ErrorDetail, exec.CreatedAt,
		eligibleActorIDs,
	)
	return err
}

const stepExecColumns = `execution_id, run_id, step_id, branch_id, actor_id, status, attempt, outcome_id, retry_after, started_at, completed_at, error_detail, created_at, eligible_actor_ids`

func scanStepExecution(scanner interface{ Scan(dest ...any) error }) (domain.StepExecution, error) {
	var exec domain.StepExecution
	var branchID, actorID, outcomeID *string
	err := scanner.Scan(
		&exec.ExecutionID, &exec.RunID, &exec.StepID, &branchID, &actorID,
		&exec.Status, &exec.Attempt, &outcomeID, &exec.RetryAfter,
		&exec.StartedAt, &exec.CompletedAt, &exec.ErrorDetail, &exec.CreatedAt,
		&exec.EligibleActorIDs,
	)
	if err != nil {
		return exec, err
	}
	if branchID != nil {
		exec.BranchID = *branchID
	}
	if actorID != nil {
		exec.ActorID = *actorID
	}
	if outcomeID != nil {
		exec.OutcomeID = *outcomeID
	}
	return exec, nil
}

func scanStepExecutions(rows pgx.Rows) ([]domain.StepExecution, error) {
	defer rows.Close()
	var execs []domain.StepExecution
	for rows.Next() {
		exec, err := scanStepExecution(rows)
		if err != nil {
			return nil, err
		}
		execs = append(execs, exec)
	}
	return execs, rows.Err()
}

func (s *PostgresStore) GetStepExecution(ctx context.Context, executionID string) (*domain.StepExecution, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+stepExecColumns+` FROM runtime.step_executions WHERE execution_id = $1`, executionID)
	exec, err := scanStepExecution(row)
	if err != nil {
		return nil, notFoundOr(err, "step execution not found")
	}
	return &exec, nil
}

func (s *PostgresStore) UpdateStepExecution(ctx context.Context, exec *domain.StepExecution) error {
	eligibleActorIDs := exec.EligibleActorIDs
	if eligibleActorIDs == nil {
		eligibleActorIDs = []string{}
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE runtime.step_executions
		SET status = $1, actor_id = $2, outcome_id = $3, retry_after = $4, started_at = $5, completed_at = $6, error_detail = $7, eligible_actor_ids = $8
		WHERE execution_id = $9`,
		exec.Status, nilIfEmpty(exec.ActorID), nilIfEmpty(exec.OutcomeID),
		exec.RetryAfter, exec.StartedAt, exec.CompletedAt, exec.ErrorDetail,
		eligibleActorIDs, exec.ExecutionID,
	)
	if err != nil {
		return err
	}
	return mustAffect(tag, "step execution not found")
}

func (s *PostgresStore) ListStepExecutionsByRun(ctx context.Context, runID string) ([]domain.StepExecution, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+stepExecColumns+` FROM runtime.step_executions WHERE run_id = $1 ORDER BY created_at`, runID)
	if err != nil {
		return nil, err
	}
	return scanStepExecutions(rows)
}

func (s *PostgresStore) ListActiveStepExecutions(ctx context.Context) ([]domain.StepExecution, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+stepExecColumns+`
		FROM runtime.step_executions
		WHERE status NOT IN ('completed', 'failed', 'skipped')
		ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	return scanStepExecutions(rows)
}
