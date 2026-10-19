package store

import (
	"context"

	"github.com/bszymi/spine/core/domain"
	"github.com/jackc/pgx/v5"
)

// ── Step Executions ──

func (s *PostgresStore) CreateStepExecution(ctx context.Context, exec *domain.StepExecution) error {
	eligibleActorIDs := exec.EligibleActorIDs
	if eligibleActorIDs == nil {
		eligibleActorIDs = []string{}
	}
	// RepositoryID defaults to PrimaryRepositoryID when empty so direct
	// Store callers (existing fixtures, simple primary-repo CRUD tests)
	// keep working. The DB column also has NOT NULL DEFAULT 'spine'
	// for the same reason — but the INSERT below always passes a value,
	// so the column default never fires; this code-side default is the
	// authoritative one. Production callers in the engine and scheduler
	// always resolve and populate RepositoryID explicitly per ADR-015
	// (see resolveStepRepoIDByStepID and Scheduler.lookupEntryStep);
	// the default below is only the safety net for paths that have
	// not yet been updated to the new contract.
	if exec.RepositoryID == "" {
		exec.RepositoryID = domain.PrimaryRepositoryID
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO runtime.step_executions (execution_id, run_id, step_id, branch_id, actor_id, status, attempt, outcome_id, retry_after, started_at, completed_at, error_detail, created_at, eligible_actor_ids, repository_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		exec.ExecutionID, exec.RunID, exec.StepID, nilIfEmpty(exec.BranchID),
		nilIfEmpty(exec.ActorID), exec.Status, exec.Attempt, nilIfEmpty(exec.OutcomeID),
		exec.RetryAfter, exec.StartedAt, exec.CompletedAt, exec.ErrorDetail, exec.CreatedAt,
		eligibleActorIDs, exec.RepositoryID,
	)
	return err
}

const stepExecColumns = `execution_id, run_id, step_id, branch_id, actor_id, status, attempt, outcome_id, retry_after, started_at, completed_at, error_detail, created_at, eligible_actor_ids, repository_id`

func scanStepExecution(scanner interface{ Scan(dest ...any) error }) (domain.StepExecution, error) {
	var exec domain.StepExecution
	var branchID, actorID, outcomeID *string
	err := scanner.Scan(
		&exec.ExecutionID, &exec.RunID, &exec.StepID, &branchID, &actorID,
		&exec.Status, &exec.Attempt, &outcomeID, &exec.RetryAfter,
		&exec.StartedAt, &exec.CompletedAt, &exec.ErrorDetail, &exec.CreatedAt,
		&exec.EligibleActorIDs, &exec.RepositoryID,
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

// stepExecUpdateSet is the column set UpdateStepExecution and
// TransitionStepExecution write ($1..$8, then execution_id as $9). Both
// statements are built from it and from stepExecUpdateArgs, so a column
// added to the blind update is written by the conditional one too
// (INIT-023 TASK-008).
const stepExecUpdateSet = `status = $1, actor_id = $2, outcome_id = $3, retry_after = $4, started_at = $5, completed_at = $6, error_detail = $7, eligible_actor_ids = $8`

// stepExecUpdateArgs returns the arguments for stepExecUpdateSet followed
// by the execution id ($9).
func stepExecUpdateArgs(exec *domain.StepExecution) []any {
	eligibleActorIDs := exec.EligibleActorIDs
	if eligibleActorIDs == nil {
		eligibleActorIDs = []string{}
	}
	return []any{
		exec.Status, nilIfEmpty(exec.ActorID), nilIfEmpty(exec.OutcomeID),
		exec.RetryAfter, exec.StartedAt, exec.CompletedAt, exec.ErrorDetail,
		eligibleActorIDs, exec.ExecutionID,
	}
}

func (s *PostgresStore) UpdateStepExecution(ctx context.Context, exec *domain.StepExecution) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE runtime.step_executions
		SET `+stepExecUpdateSet+`
		WHERE execution_id = $9`,
		stepExecUpdateArgs(exec)...,
	)
	if err != nil {
		return err
	}
	return mustAffect(tag, "step execution not found")
}

// TransitionStepExecution is UpdateStepExecution made conditional
// (INIT-023 TASK-008, audit P3-24): it writes exactly the same columns, but
// only while the row still holds fromStatus and fromActorID — the status
// and actor the caller read before deciding on the write. It implements
// the engine's optional engine.StepExecutionTransitioner port (the
// compile-time assertion lives in service/cmd/spine, since this package
// cannot import core/engine).
//
// The actor comparison coalesces actor_id to the empty string and compares
// it with the plain fromActorID ($11): Spine always stores NULL for "no
// actor" (nilIfEmpty), but rows written directly (fixtures, embedders) may
// hold an empty string — both mean "no actor" — and a plain text parameter
// avoids a typed-NULL comparison.
//
// (true, nil): applied. (false, nil): the row exists but has moved on
// (another writer won); nothing was written. ErrNotFound: no such row.
// Status alone would let a stale write from actor A land after a release
// and a re-assign to B restored the same status; the actor closes that
// (an ABA by the same actor is accepted).
func (s *PostgresStore) TransitionStepExecution(ctx context.Context, exec *domain.StepExecution, fromStatus domain.StepExecutionStatus, fromActorID string) (bool, error) {
	args := append(stepExecUpdateArgs(exec), fromStatus, fromActorID)
	tag, err := s.pool.Exec(ctx, `
		UPDATE runtime.step_executions
		SET `+stepExecUpdateSet+`
		WHERE execution_id = $9 AND status = $10 AND COALESCE(actor_id, '') = $11`,
		args...,
	)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() > 0 {
		return true, nil
	}
	var exists bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM runtime.step_executions WHERE execution_id = $1)`,
		exec.ExecutionID).Scan(&exists); err != nil {
		return false, err
	}
	if !exists {
		return false, domain.NewError(domain.ErrNotFound, "step execution not found")
	}
	return false, nil
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
