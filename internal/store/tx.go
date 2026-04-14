package store

import (
	"context"
	"fmt"

	"github.com/bszymi/spine/internal/domain"
	"github.com/jackc/pgx/v5"
)

// postgresTx implements Tx using a pgx transaction.
type postgresTx struct {
	tx pgx.Tx
}

func (t *postgresTx) CreateRun(ctx context.Context, run *domain.Run) error {
	_, err := t.tx.Exec(ctx, `
		INSERT INTO runtime.runs (run_id, task_path, workflow_path, workflow_id, workflow_version, workflow_version_label, status, current_step_id, branch_name, trace_id, timeout_at, started_at, completed_at, created_at, mode)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		run.RunID, run.TaskPath, run.WorkflowPath, run.WorkflowID, run.WorkflowVersion,
		run.WorkflowVersionLabel, run.Status, nilIfEmpty(run.CurrentStepID), nilIfEmpty(run.BranchName), run.TraceID,
		run.TimeoutAt, run.StartedAt, run.CompletedAt, run.CreatedAt, modeOrDefault(run.Mode),
	)
	if err != nil {
		return fmt.Errorf("create run in tx: %w", err)
	}
	return nil
}

func (t *postgresTx) UpdateRunStatus(ctx context.Context, runID string, status domain.RunStatus) error {
	tag, err := t.tx.Exec(ctx, `
		UPDATE runtime.runs
		SET status = $1,
			started_at = CASE WHEN $1 = 'active' AND started_at IS NULL THEN now() ELSE started_at END,
			completed_at = CASE WHEN $1 IN ('completed', 'failed', 'cancelled') AND completed_at IS NULL THEN now() ELSE completed_at END
		WHERE run_id = $2`, status, runID)
	if err != nil {
		return fmt.Errorf("update run status in tx: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.NewError(domain.ErrNotFound, "run not found")
	}
	return nil
}

func (t *postgresTx) TransitionRunStatus(ctx context.Context, runID string, fromStatus, toStatus domain.RunStatus) (bool, error) {
	tag, err := t.tx.Exec(ctx, `
		UPDATE runtime.runs
		SET status = $1,
			started_at = CASE WHEN $1 = 'active' AND started_at IS NULL THEN now() ELSE started_at END,
			completed_at = CASE WHEN $1 IN ('completed', 'failed', 'cancelled') AND completed_at IS NULL THEN now() ELSE completed_at END
		WHERE run_id = $2 AND status = $3`, toStatus, runID, fromStatus)
	if err != nil {
		return false, fmt.Errorf("transition run status in tx: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (t *postgresTx) CreateStepExecution(ctx context.Context, exec *domain.StepExecution) error {
	eligibleActorIDs := exec.EligibleActorIDs
	if eligibleActorIDs == nil {
		eligibleActorIDs = []string{}
	}
	_, err := t.tx.Exec(ctx, `
		INSERT INTO runtime.step_executions (execution_id, run_id, step_id, branch_id, actor_id, status, attempt, outcome_id, retry_after, started_at, completed_at, error_detail, created_at, eligible_actor_ids)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
		exec.ExecutionID, exec.RunID, exec.StepID, nilIfEmpty(exec.BranchID),
		nilIfEmpty(exec.ActorID), exec.Status, exec.Attempt, nilIfEmpty(exec.OutcomeID),
		exec.RetryAfter, exec.StartedAt, exec.CompletedAt, exec.ErrorDetail, exec.CreatedAt,
		eligibleActorIDs,
	)
	if err != nil {
		return fmt.Errorf("create step execution in tx: %w", err)
	}
	return nil
}

func (t *postgresTx) UpdateStepExecution(ctx context.Context, exec *domain.StepExecution) error {
	eligibleActorIDs := exec.EligibleActorIDs
	if eligibleActorIDs == nil {
		eligibleActorIDs = []string{}
	}
	tag, err := t.tx.Exec(ctx, `
		UPDATE runtime.step_executions
		SET status = $1, actor_id = $2, outcome_id = $3, retry_after = $4, started_at = $5, completed_at = $6, error_detail = $7, eligible_actor_ids = $8
		WHERE execution_id = $9`,
		exec.Status, nilIfEmpty(exec.ActorID), nilIfEmpty(exec.OutcomeID),
		exec.RetryAfter, exec.StartedAt, exec.CompletedAt, exec.ErrorDetail,
		eligibleActorIDs, exec.ExecutionID,
	)
	if err != nil {
		return fmt.Errorf("update step execution in tx: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.NewError(domain.ErrNotFound, "step execution not found")
	}
	return nil
}
