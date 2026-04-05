package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresStore implements Store using PostgreSQL via pgx.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore creates a new PostgreSQL store with connection pooling.
func NewPostgresStore(ctx context.Context, databaseURL string) (*PostgresStore, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return &PostgresStore{pool: pool}, nil
}

// Close closes the connection pool.
func (s *PostgresStore) Close() {
	s.pool.Close()
}

// Ping checks database connectivity.
func (s *PostgresStore) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// WithTx executes a function within a database transaction.
// The transaction is committed if fn returns nil, rolled back otherwise.
func (s *PostgresStore) WithTx(ctx context.Context, fn func(tx Tx) error) error {
	pgxTx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	tx := &postgresTx{tx: pgxTx}
	if err := fn(tx); err != nil {
		if rbErr := pgxTx.Rollback(ctx); rbErr != nil {
			return fmt.Errorf("%w (rollback also failed: %v)", err, rbErr)
		}
		return err
	}

	return pgxTx.Commit(ctx)
}

// ── Runs ──

func (s *PostgresStore) CreateRun(ctx context.Context, run *domain.Run) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO runtime.runs (run_id, task_path, workflow_path, workflow_id, workflow_version, workflow_version_label, status, current_step_id, branch_name, trace_id, timeout_at, started_at, completed_at, created_at, mode)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		run.RunID, run.TaskPath, run.WorkflowPath, run.WorkflowID, run.WorkflowVersion,
		run.WorkflowVersionLabel, run.Status, nilIfEmpty(run.CurrentStepID), nilIfEmpty(run.BranchName), run.TraceID,
		run.TimeoutAt, run.StartedAt, run.CompletedAt, run.CreatedAt, modeOrDefault(run.Mode),
	)
	return err
}

// runColumns is the standard column list for runtime.runs queries.
const runColumns = `run_id, task_path, workflow_path, workflow_id, workflow_version, workflow_version_label, status, current_step_id, branch_name, trace_id, timeout_at, started_at, completed_at, created_at, mode`

// scanRun scans a single row into a domain.Run, handling nullable columns.
func scanRun(scanner interface{ Scan(dest ...any) error }) (domain.Run, error) {
	var run domain.Run
	var currentStepID, branchName *string
	err := scanner.Scan(
		&run.RunID, &run.TaskPath, &run.WorkflowPath, &run.WorkflowID, &run.WorkflowVersion,
		&run.WorkflowVersionLabel, &run.Status, &currentStepID, &branchName, &run.TraceID,
		&run.TimeoutAt, &run.StartedAt, &run.CompletedAt, &run.CreatedAt, &run.Mode,
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
		if err == pgx.ErrNoRows {
			return nil, domain.NewError(domain.ErrNotFound, "run not found")
		}
		return nil, err
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
	if tag.RowsAffected() == 0 {
		return domain.NewError(domain.ErrNotFound, "run not found")
	}
	return nil
}

func (s *PostgresStore) UpdateCurrentStep(ctx context.Context, runID, stepID string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE runtime.runs SET current_step_id = $1 WHERE run_id = $2`, stepID, runID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.NewError(domain.ErrNotFound, "run not found")
	}
	return nil
}

func (s *PostgresStore) ListRunsByTask(ctx context.Context, taskPath string) ([]domain.Run, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+runColumns+` FROM runtime.runs WHERE task_path = $1 ORDER BY created_at DESC`, taskPath)
	if err != nil {
		return nil, err
	}
	return scanRuns(rows)
}

// ── Step Executions ──

func (s *PostgresStore) CreateStepExecution(ctx context.Context, exec *domain.StepExecution) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO runtime.step_executions (execution_id, run_id, step_id, branch_id, actor_id, status, attempt, outcome_id, retry_after, started_at, completed_at, error_detail, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		exec.ExecutionID, exec.RunID, exec.StepID, nilIfEmpty(exec.BranchID),
		nilIfEmpty(exec.ActorID), exec.Status, exec.Attempt, nilIfEmpty(exec.OutcomeID),
		exec.RetryAfter, exec.StartedAt, exec.CompletedAt, exec.ErrorDetail, exec.CreatedAt,
	)
	return err
}

const stepExecColumns = `execution_id, run_id, step_id, branch_id, actor_id, status, attempt, outcome_id, retry_after, started_at, completed_at, error_detail, created_at`

func scanStepExecution(scanner interface{ Scan(dest ...any) error }) (domain.StepExecution, error) {
	var exec domain.StepExecution
	var branchID, actorID, outcomeID *string
	err := scanner.Scan(
		&exec.ExecutionID, &exec.RunID, &exec.StepID, &branchID, &actorID,
		&exec.Status, &exec.Attempt, &outcomeID, &exec.RetryAfter,
		&exec.StartedAt, &exec.CompletedAt, &exec.ErrorDetail, &exec.CreatedAt,
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
		if err == pgx.ErrNoRows {
			return nil, domain.NewError(domain.ErrNotFound, "step execution not found")
		}
		return nil, err
	}
	return &exec, nil
}

func (s *PostgresStore) UpdateStepExecution(ctx context.Context, exec *domain.StepExecution) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE runtime.step_executions
		SET status = $1, actor_id = $2, outcome_id = $3, retry_after = $4, started_at = $5, completed_at = $6, error_detail = $7
		WHERE execution_id = $8`,
		exec.Status, nilIfEmpty(exec.ActorID), nilIfEmpty(exec.OutcomeID),
		exec.RetryAfter, exec.StartedAt, exec.CompletedAt, exec.ErrorDetail, exec.ExecutionID,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.NewError(domain.ErrNotFound, "step execution not found")
	}
	return nil
}

func (s *PostgresStore) ListStepExecutionsByRun(ctx context.Context, runID string) ([]domain.StepExecution, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+stepExecColumns+` FROM runtime.step_executions WHERE run_id = $1 ORDER BY created_at`, runID)
	if err != nil {
		return nil, err
	}
	return scanStepExecutions(rows)
}

// ── Actors ──

func (s *PostgresStore) GetActor(ctx context.Context, actorID string) (*domain.Actor, error) {
	var actor domain.Actor
	var capabilities []byte
	err := s.pool.QueryRow(ctx, `
		SELECT actor_id, actor_type, name, role, capabilities, status
		FROM auth.actors WHERE actor_id = $1`, actorID,
	).Scan(&actor.ActorID, &actor.Type, &actor.Name, &actor.Role, &capabilities, &actor.Status)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, domain.NewError(domain.ErrNotFound, "actor not found")
		}
		return nil, err
	}
	if capabilities != nil {
		_ = json.Unmarshal(capabilities, &actor.Capabilities)
	}
	return &actor, nil
}

func (s *PostgresStore) CreateActor(ctx context.Context, actor *domain.Actor) error {
	capabilities, err := json.Marshal(actor.Capabilities)
	if err != nil {
		return fmt.Errorf("marshal capabilities: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO auth.actors (actor_id, actor_type, name, role, capabilities, status)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		actor.ActorID, actor.Type, actor.Name, actor.Role, capabilities, actor.Status,
	)
	return err
}

func (s *PostgresStore) UpdateActor(ctx context.Context, actor *domain.Actor) error {
	capabilities, err := json.Marshal(actor.Capabilities)
	if err != nil {
		return fmt.Errorf("marshal capabilities: %w", err)
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE auth.actors SET name = $1, role = $2, capabilities = $3, status = $4, updated_at = now()
		WHERE actor_id = $5`,
		actor.Name, actor.Role, capabilities, actor.Status, actor.ActorID,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.NewError(domain.ErrNotFound, "actor not found")
	}
	return nil
}

func (s *PostgresStore) ListActors(ctx context.Context) ([]domain.Actor, error) {
	return s.listActorsQuery(ctx, `
		SELECT actor_id, actor_type, name, role, capabilities, status
		FROM auth.actors ORDER BY actor_id`)
}

func (s *PostgresStore) ListActorsByStatus(ctx context.Context, status domain.ActorStatus) ([]domain.Actor, error) {
	return s.listActorsQuery(ctx, `
		SELECT actor_id, actor_type, name, role, capabilities, status
		FROM auth.actors WHERE status = $1 ORDER BY actor_id`, status)
}

func (s *PostgresStore) listActorsQuery(ctx context.Context, query string, args ...any) ([]domain.Actor, error) {
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var actors []domain.Actor
	for rows.Next() {
		var actor domain.Actor
		var capabilities []byte
		if err := rows.Scan(&actor.ActorID, &actor.Type, &actor.Name, &actor.Role, &capabilities, &actor.Status); err != nil {
			return nil, err
		}
		if capabilities != nil {
			_ = json.Unmarshal(capabilities, &actor.Capabilities)
		}
		actors = append(actors, actor)
	}
	return actors, rows.Err()
}

// ── Tokens ──

func (s *PostgresStore) GetActorByTokenHash(ctx context.Context, tokenHash string) (*domain.Actor, *domain.Token, error) {
	var actor domain.Actor
	var token domain.Token
	var capabilities []byte
	err := s.pool.QueryRow(ctx, `
		SELECT a.actor_id, a.actor_type, a.name, a.role, a.capabilities, a.status,
		       t.token_id, t.actor_id, t.name, t.expires_at, t.revoked_at, t.created_at
		FROM auth.tokens t
		JOIN auth.actors a ON t.actor_id = a.actor_id
		WHERE t.token_hash = $1`, tokenHash,
	).Scan(
		&actor.ActorID, &actor.Type, &actor.Name, &actor.Role, &capabilities, &actor.Status,
		&token.TokenID, &token.ActorID, &token.Name, &token.ExpiresAt, &token.RevokedAt, &token.CreatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil, domain.NewError(domain.ErrUnauthorized, "invalid token")
		}
		return nil, nil, err
	}
	if capabilities != nil {
		_ = json.Unmarshal(capabilities, &actor.Capabilities)
	}
	return &actor, &token, nil
}

func (s *PostgresStore) CreateToken(ctx context.Context, record *TokenRecord) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO auth.tokens (token_id, actor_id, token_hash, name, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		record.TokenID, record.ActorID, record.TokenHash, record.Name, record.ExpiresAt, record.CreatedAt,
	)
	return err
}

func (s *PostgresStore) RevokeToken(ctx context.Context, tokenID string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE auth.tokens SET revoked_at = now() WHERE token_id = $1 AND revoked_at IS NULL`, tokenID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.NewError(domain.ErrNotFound, "token not found or already revoked")
	}
	return nil
}

func (s *PostgresStore) ListTokensByActor(ctx context.Context, actorID string) ([]domain.Token, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT token_id, actor_id, name, expires_at, revoked_at, created_at
		FROM auth.tokens WHERE actor_id = $1 ORDER BY created_at DESC`, actorID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tokens []domain.Token
	for rows.Next() {
		var t domain.Token
		if err := rows.Scan(&t.TokenID, &t.ActorID, &t.Name, &t.ExpiresAt, &t.RevokedAt, &t.CreatedAt); err != nil {
			return nil, err
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

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
	if tag.RowsAffected() == 0 {
		return domain.NewError(domain.ErrNotFound, "divergence context not found")
	}
	return nil
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
		if err == pgx.ErrNoRows {
			return nil, domain.NewError(domain.ErrNotFound, "divergence context not found")
		}
		return nil, err
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
	if tag.RowsAffected() == 0 {
		return domain.NewError(domain.ErrNotFound, "branch not found")
	}
	return nil
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
		if err == pgx.ErrNoRows {
			return nil, domain.NewError(domain.ErrNotFound, "branch not found")
		}
		return nil, err
	}
	if currentStepID != nil {
		branch.CurrentStepID = *currentStepID
	}
	return &branch, nil
}

func (s *PostgresStore) ListBranchesByDivergence(ctx context.Context, divergenceID string) ([]domain.Branch, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT branch_id, run_id, divergence_id, status, current_step_id, outcome, artifacts_produced, created_at, completed_at
		FROM runtime.branches WHERE divergence_id = $1 ORDER BY created_at`, divergenceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var branches []domain.Branch
	for rows.Next() {
		var b domain.Branch
		var currentStepID *string
		if err := rows.Scan(&b.BranchID, &b.RunID, &b.DivergenceID, &b.Status,
			&currentStepID, &b.Outcome, &b.ArtifactsProduced, &b.CreatedAt, &b.CompletedAt); err != nil {
			return nil, err
		}
		if currentStepID != nil {
			b.CurrentStepID = *currentStepID
		}
		branches = append(branches, b)
	}
	return branches, rows.Err()
}

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
	if tag.RowsAffected() == 0 {
		return domain.NewError(domain.ErrNotFound, "assignment not found")
	}
	return nil
}

func (s *PostgresStore) GetAssignment(ctx context.Context, assignmentID string) (*domain.Assignment, error) {
	var a domain.Assignment
	err := s.pool.QueryRow(ctx, `
		SELECT assignment_id, run_id, execution_id, actor_id, status, assigned_at, responded_at, timeout_at
		FROM runtime.actor_assignments WHERE assignment_id = $1`, assignmentID,
	).Scan(&a.AssignmentID, &a.RunID, &a.ExecutionID, &a.ActorID, &a.Status, &a.AssignedAt, &a.RespondedAt, &a.TimeoutAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, domain.NewError(domain.ErrNotFound, "assignment not found")
		}
		return nil, err
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
	rows, err := s.pool.Query(ctx, `
		SELECT assignment_id, run_id, execution_id, actor_id, status, assigned_at, responded_at, timeout_at
		FROM runtime.actor_assignments WHERE status = 'active' AND timeout_at IS NOT NULL AND timeout_at < $1
		ORDER BY timeout_at`, before,
	)
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

// ── Scheduler Queries ──

func (s *PostgresStore) ListRunsByStatus(ctx context.Context, status domain.RunStatus) ([]domain.Run, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+runColumns+` FROM runtime.runs WHERE status = $1 ORDER BY created_at`, status)
	if err != nil {
		return nil, err
	}
	return scanRuns(rows)
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

func (s *PostgresStore) ListStaleActiveRuns(ctx context.Context, noActivitySince time.Time) ([]domain.Run, error) {
	// A run is stale if no step execution has recent activity.
	// Use column aliases to match runColumns ordering (r.* prefix).
	rows, err := s.pool.Query(ctx, `
		SELECT r.run_id, r.task_path, r.workflow_path, r.workflow_id, r.workflow_version, r.workflow_version_label, r.status, r.current_step_id, r.branch_name, r.trace_id, r.timeout_at, r.started_at, r.completed_at, r.created_at, r.mode
		FROM runtime.runs r
		WHERE r.status = 'active'
		AND NOT EXISTS (
			SELECT 1 FROM runtime.step_executions se
			WHERE se.run_id = r.run_id
			AND GREATEST(se.created_at, COALESCE(se.started_at, se.created_at), COALESCE(se.completed_at, se.created_at)) > $1
		)
		AND r.created_at < $1
		ORDER BY r.created_at`, noActivitySince)
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

// ── Projections ──

func (s *PostgresStore) UpsertArtifactProjection(ctx context.Context, proj *ArtifactProjection) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO projection.artifacts (artifact_path, artifact_id, artifact_type, title, status, metadata, content, links, source_commit, content_hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (artifact_path) DO UPDATE SET
			artifact_id = EXCLUDED.artifact_id,
			artifact_type = EXCLUDED.artifact_type,
			title = EXCLUDED.title,
			status = EXCLUDED.status,
			metadata = EXCLUDED.metadata,
			content = EXCLUDED.content,
			links = EXCLUDED.links,
			source_commit = EXCLUDED.source_commit,
			content_hash = EXCLUDED.content_hash,
			synced_at = now()`,
		proj.ArtifactPath, proj.ArtifactID, proj.ArtifactType, proj.Title,
		proj.Status, proj.Metadata, proj.Content, proj.Links,
		proj.SourceCommit, proj.ContentHash,
	)
	return err
}

func (s *PostgresStore) GetArtifactProjection(ctx context.Context, artifactPath string) (*ArtifactProjection, error) {
	var proj ArtifactProjection
	err := s.pool.QueryRow(ctx, `
		SELECT artifact_path, artifact_id, artifact_type, title, status, metadata, content, links, source_commit, content_hash
		FROM projection.artifacts WHERE artifact_path = $1`, artifactPath,
	).Scan(
		&proj.ArtifactPath, &proj.ArtifactID, &proj.ArtifactType, &proj.Title,
		&proj.Status, &proj.Metadata, &proj.Content, &proj.Links,
		&proj.SourceCommit, &proj.ContentHash,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, domain.NewError(domain.ErrNotFound, "artifact not found")
		}
		return nil, err
	}
	return &proj, nil
}

func (s *PostgresStore) QueryArtifacts(ctx context.Context, query ArtifactQuery) (*ArtifactQueryResult, error) {
	var conditions []string
	var args []any
	argIdx := 1

	if query.Type != "" {
		conditions = append(conditions, fmt.Sprintf("artifact_type = $%d", argIdx))
		args = append(args, query.Type)
		argIdx++
	}
	if query.Status != "" {
		conditions = append(conditions, fmt.Sprintf("status = $%d", argIdx))
		args = append(args, query.Status)
		argIdx++
	}
	if query.ParentPath != "" {
		conditions = append(conditions, fmt.Sprintf("artifact_path LIKE $%d", argIdx))
		args = append(args, query.ParentPath+"%")
		argIdx++
	}
	if query.Search != "" {
		conditions = append(conditions, fmt.Sprintf("(title ILIKE $%d OR content ILIKE $%d)", argIdx, argIdx))
		args = append(args, "%"+query.Search+"%")
		argIdx++
	}
	if query.Cursor != "" {
		conditions = append(conditions, fmt.Sprintf("artifact_path > $%d", argIdx))
		args = append(args, query.Cursor)
	}

	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + strings.Join(conditions, " AND ")
	}

	limit := query.Limit
	if limit <= 0 {
		limit = 50
	}

	sql := fmt.Sprintf(`
		SELECT artifact_path, artifact_id, artifact_type, title, status, metadata, content, links, source_commit, content_hash
		FROM projection.artifacts %s
		ORDER BY artifact_path
		LIMIT %d`, where, limit+1)

	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []ArtifactProjection
	for rows.Next() {
		var proj ArtifactProjection
		if err := rows.Scan(
			&proj.ArtifactPath, &proj.ArtifactID, &proj.ArtifactType, &proj.Title,
			&proj.Status, &proj.Metadata, &proj.Content, &proj.Links,
			&proj.SourceCommit, &proj.ContentHash,
		); err != nil {
			return nil, err
		}
		items = append(items, proj)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := &ArtifactQueryResult{}
	if len(items) > limit {
		result.HasMore = true
		result.NextCursor = items[limit-1].ArtifactPath
		result.Items = items[:limit]
	} else {
		result.Items = items
	}
	return result, nil
}

func (s *PostgresStore) DeleteArtifactProjection(ctx context.Context, artifactPath string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM projection.artifacts WHERE artifact_path = $1`, artifactPath)
	return err
}

func (s *PostgresStore) DeleteAllProjections(ctx context.Context) error {
	// Explicit DELETE statements — no dynamic table names to prevent SQL injection patterns
	if _, err := s.pool.Exec(ctx, "DELETE FROM projection.artifact_links"); err != nil {
		return fmt.Errorf("delete artifact_links: %w", err)
	}
	if _, err := s.pool.Exec(ctx, "DELETE FROM projection.artifacts"); err != nil {
		return fmt.Errorf("delete artifacts: %w", err)
	}
	if _, err := s.pool.Exec(ctx, "DELETE FROM projection.workflows"); err != nil {
		return fmt.Errorf("delete workflows: %w", err)
	}
	return nil
}

// ── Links ──

func (s *PostgresStore) UpsertArtifactLinks(ctx context.Context, sourcePath string, links []ArtifactLink, sourceCommit string) error {
	// Delete existing links for this source, then insert new ones
	if _, err := s.pool.Exec(ctx, `DELETE FROM projection.artifact_links WHERE source_path = $1`, sourcePath); err != nil {
		return err
	}
	for _, link := range links {
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO projection.artifact_links (source_path, target_path, link_type, source_commit)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (source_path, target_path, link_type) DO UPDATE SET source_commit = EXCLUDED.source_commit`,
			link.SourcePath, link.TargetPath, link.LinkType, sourceCommit,
		); err != nil {
			return err
		}
	}
	return nil
}

func (s *PostgresStore) QueryArtifactLinks(ctx context.Context, sourcePath string) ([]ArtifactLink, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT source_path, target_path, link_type
		FROM projection.artifact_links WHERE source_path = $1`, sourcePath)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var links []ArtifactLink
	for rows.Next() {
		var link ArtifactLink
		if err := rows.Scan(&link.SourcePath, &link.TargetPath, &link.LinkType); err != nil {
			return nil, err
		}
		links = append(links, link)
	}
	return links, rows.Err()
}

func (s *PostgresStore) QueryArtifactLinksByTarget(ctx context.Context, targetPath string) ([]ArtifactLink, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT source_path, target_path, link_type
		FROM projection.artifact_links WHERE target_path = $1`, targetPath)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var links []ArtifactLink
	for rows.Next() {
		var link ArtifactLink
		if err := rows.Scan(&link.SourcePath, &link.TargetPath, &link.LinkType); err != nil {
			return nil, err
		}
		links = append(links, link)
	}
	return links, rows.Err()
}

func (s *PostgresStore) DeleteArtifactLinks(ctx context.Context, sourcePath string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM projection.artifact_links WHERE source_path = $1`, sourcePath)
	return err
}

// ── Workflows ──

func (s *PostgresStore) UpsertWorkflowProjection(ctx context.Context, proj *WorkflowProjection) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO projection.workflows (workflow_path, workflow_id, name, version, status, applies_to, definition, source_commit)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (workflow_path) DO UPDATE SET
			workflow_id = EXCLUDED.workflow_id,
			name = EXCLUDED.name,
			version = EXCLUDED.version,
			status = EXCLUDED.status,
			applies_to = EXCLUDED.applies_to,
			definition = EXCLUDED.definition,
			source_commit = EXCLUDED.source_commit,
			synced_at = now()`,
		proj.WorkflowPath, proj.WorkflowID, proj.Name, proj.Version,
		proj.Status, proj.AppliesTo, proj.Definition, proj.SourceCommit,
	)
	return err
}

func (s *PostgresStore) GetWorkflowProjection(ctx context.Context, workflowPath string) (*WorkflowProjection, error) {
	var proj WorkflowProjection
	err := s.pool.QueryRow(ctx, `
		SELECT workflow_path, workflow_id, name, version, status, applies_to, definition, source_commit
		FROM projection.workflows WHERE workflow_path = $1`, workflowPath,
	).Scan(
		&proj.WorkflowPath, &proj.WorkflowID, &proj.Name, &proj.Version,
		&proj.Status, &proj.AppliesTo, &proj.Definition, &proj.SourceCommit,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, domain.NewError(domain.ErrNotFound, "workflow not found")
		}
		return nil, err
	}
	return &proj, nil
}

func (s *PostgresStore) DeleteWorkflowProjection(ctx context.Context, workflowPath string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM projection.workflows WHERE workflow_path = $1`, workflowPath)
	return err
}

func (s *PostgresStore) ListActiveWorkflowProjections(ctx context.Context) ([]WorkflowProjection, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT workflow_path, workflow_id, name, version, status, applies_to, definition, source_commit
		FROM projection.workflows WHERE status = 'Active' ORDER BY workflow_id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var projections []WorkflowProjection
	for rows.Next() {
		var p WorkflowProjection
		if err := rows.Scan(&p.WorkflowPath, &p.WorkflowID, &p.Name, &p.Version,
			&p.Status, &p.AppliesTo, &p.Definition, &p.SourceCommit); err != nil {
			return nil, err
		}
		projections = append(projections, p)
	}
	return projections, rows.Err()
}

// ── Sync State ──

func (s *PostgresStore) GetSyncState(ctx context.Context) (*SyncState, error) {
	var state SyncState
	err := s.pool.QueryRow(ctx, `
		SELECT last_synced_commit, last_synced_at, status, COALESCE(error_detail, '')
		FROM projection.sync_state WHERE id = 'global'`,
	).Scan(&state.LastSyncedCommit, &state.LastSyncedAt, &state.Status, &state.ErrorDetail)

	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil // no sync state yet
		}
		return nil, err
	}
	return &state, nil
}

func (s *PostgresStore) UpdateSyncState(ctx context.Context, state *SyncState) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO projection.sync_state (id, last_synced_commit, last_synced_at, status, error_detail)
		VALUES ('global', $1, now(), $2, $3)
		ON CONFLICT (id) DO UPDATE SET
			last_synced_commit = EXCLUDED.last_synced_commit,
			last_synced_at = now(),
			status = EXCLUDED.status,
			error_detail = EXCLUDED.error_detail`,
		state.LastSyncedCommit, state.Status, nilIfEmpty(state.ErrorDetail),
	)
	return err
}

// ── Skills ──

func (s *PostgresStore) CreateSkill(ctx context.Context, skill *domain.Skill) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO auth.skills (skill_id, name, description, category, status)
		VALUES ($1, $2, $3, $4, $5)`,
		skill.SkillID, skill.Name, skill.Description, skill.Category, skill.Status,
	)
	return err
}

func (s *PostgresStore) GetSkill(ctx context.Context, skillID string) (*domain.Skill, error) {
	var skill domain.Skill
	err := s.pool.QueryRow(ctx, `
		SELECT skill_id, name, description, category, status, created_at, updated_at
		FROM auth.skills WHERE skill_id = $1`, skillID,
	).Scan(&skill.SkillID, &skill.Name, &skill.Description, &skill.Category, &skill.Status, &skill.CreatedAt, &skill.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, domain.NewError(domain.ErrNotFound, "skill not found")
		}
		return nil, err
	}
	return &skill, nil
}

func (s *PostgresStore) UpdateSkill(ctx context.Context, skill *domain.Skill) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE auth.skills SET name = $1, description = $2, category = $3, status = $4, updated_at = now()
		WHERE skill_id = $5`,
		skill.Name, skill.Description, skill.Category, skill.Status, skill.SkillID,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.NewError(domain.ErrNotFound, "skill not found")
	}
	return nil
}

func (s *PostgresStore) ListSkills(ctx context.Context) ([]domain.Skill, error) {
	return s.listSkillsQuery(ctx, `
		SELECT skill_id, name, description, category, status, created_at, updated_at
		FROM auth.skills ORDER BY name`)
}

func (s *PostgresStore) ListSkillsByCategory(ctx context.Context, category string) ([]domain.Skill, error) {
	return s.listSkillsQuery(ctx, `
		SELECT skill_id, name, description, category, status, created_at, updated_at
		FROM auth.skills WHERE category = $1 ORDER BY name`, category)
}

func (s *PostgresStore) listSkillsQuery(ctx context.Context, query string, args ...any) ([]domain.Skill, error) {
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var skills []domain.Skill
	for rows.Next() {
		var skill domain.Skill
		if err := rows.Scan(&skill.SkillID, &skill.Name, &skill.Description, &skill.Category, &skill.Status, &skill.CreatedAt, &skill.UpdatedAt); err != nil {
			return nil, err
		}
		skills = append(skills, skill)
	}
	return skills, rows.Err()
}

// ── Actor-Skill Associations ──

func (s *PostgresStore) AddSkillToActor(ctx context.Context, actorID, skillID string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO auth.actor_skills (actor_id, skill_id)
		VALUES ($1, $2)
		ON CONFLICT (actor_id, skill_id) DO NOTHING`,
		actorID, skillID,
	)
	return err
}

func (s *PostgresStore) RemoveSkillFromActor(ctx context.Context, actorID, skillID string) error {
	_, err := s.pool.Exec(ctx, `
		DELETE FROM auth.actor_skills
		WHERE actor_id = $1 AND skill_id = $2`,
		actorID, skillID,
	)
	return err
}

func (s *PostgresStore) ListActorSkills(ctx context.Context, actorID string) ([]domain.Skill, error) {
	return s.listSkillsQuery(ctx, `
		SELECT s.skill_id, s.name, s.description, s.category, s.status, s.created_at, s.updated_at
		FROM auth.skills s
		JOIN auth.actor_skills as_ ON s.skill_id = as_.skill_id
		WHERE as_.actor_id = $1
		ORDER BY s.name`, actorID)
}

func (s *PostgresStore) ListActorsBySkills(ctx context.Context, skillNames []string) ([]domain.Actor, error) {
	if len(skillNames) == 0 {
		return s.ListActorsByStatus(ctx, domain.ActorStatusActive)
	}

	// Find active actors possessing ALL specified skills (AND matching).
	// Uses a COUNT/HAVING pattern to require all skills are present.
	return s.listActorsQuery(ctx, `
		SELECT a.actor_id, a.actor_type, a.name, a.role, a.capabilities, a.status
		FROM auth.actors a
		JOIN auth.actor_skills as_ ON a.actor_id = as_.actor_id
		JOIN auth.skills s ON as_.skill_id = s.skill_id
		WHERE a.status = 'active'
		  AND s.name = ANY($1)
		GROUP BY a.actor_id, a.actor_type, a.name, a.role, a.capabilities, a.status
		HAVING COUNT(DISTINCT s.name) = $2
		ORDER BY a.actor_id`, skillNames, len(skillNames))
}

// ── Migrations ──

func (s *PostgresStore) ApplyMigrations(ctx context.Context, migrationsDir string) error {
	// Ensure schema_migrations table exists
	_, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS public.schema_migrations (
			version     text        PRIMARY KEY,
			applied_at  timestamptz NOT NULL DEFAULT now()
		)`)
	if err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		version := strings.TrimSuffix(entry.Name(), ".sql")

		applied, err := s.IsMigrationApplied(ctx, version)
		if err != nil {
			return err
		}
		if applied {
			continue
		}

		content, err := os.ReadFile(migrationsDir + "/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}

		// Use simple protocol (Exec with no parameters) to allow multi-statement SQL
		if _, err := s.pool.Exec(ctx, string(content)); err != nil {
			return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
		}

		// Record migration if the file didn't self-record
		alreadyApplied, checkErr := s.IsMigrationApplied(ctx, version)
		if checkErr != nil {
			return fmt.Errorf("check migration %s: %w", version, checkErr)
		}
		if !alreadyApplied {
			if _, err := s.pool.Exec(ctx, `INSERT INTO public.schema_migrations (version) VALUES ($1) ON CONFLICT DO NOTHING`, version); err != nil {
				return fmt.Errorf("record migration %s: %w", version, err)
			}
		}
	}

	return nil
}

func (s *PostgresStore) IsMigrationApplied(ctx context.Context, version string) (bool, error) {
	var count int
	err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM public.schema_migrations WHERE version = $1`, version).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// ── Discussions ──

func (s *PostgresStore) CreateThread(ctx context.Context, thread *domain.DiscussionThread) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO runtime.discussion_threads (thread_id, anchor_type, anchor_id, topic_key, title, status, created_by, created_at, resolved_at, resolution_type, resolution_refs)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		thread.ThreadID, thread.AnchorType, thread.AnchorID,
		nilIfEmpty(thread.TopicKey), nilIfEmpty(thread.Title),
		thread.Status, thread.CreatedBy, thread.CreatedAt,
		thread.ResolvedAt, nilIfEmpty(string(thread.ResolutionType)), thread.ResolutionRefs,
	)
	return err
}

func (s *PostgresStore) GetThread(ctx context.Context, threadID string) (*domain.DiscussionThread, error) {
	var t domain.DiscussionThread
	var topicKey, title, resolutionType *string
	err := s.pool.QueryRow(ctx, `
		SELECT thread_id, anchor_type, anchor_id, topic_key, title, status, created_by, created_at, resolved_at, resolution_type, resolution_refs
		FROM runtime.discussion_threads WHERE thread_id = $1`, threadID,
	).Scan(
		&t.ThreadID, &t.AnchorType, &t.AnchorID, &topicKey, &title,
		&t.Status, &t.CreatedBy, &t.CreatedAt, &t.ResolvedAt,
		&resolutionType, &t.ResolutionRefs,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, domain.NewError(domain.ErrNotFound, "thread not found")
		}
		return nil, err
	}
	if topicKey != nil {
		t.TopicKey = *topicKey
	}
	if title != nil {
		t.Title = *title
	}
	if resolutionType != nil {
		t.ResolutionType = domain.ResolutionType(*resolutionType)
	}
	return &t, nil
}

func (s *PostgresStore) ListThreads(ctx context.Context, anchorType domain.AnchorType, anchorID string) ([]domain.DiscussionThread, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT thread_id, anchor_type, anchor_id, topic_key, title, status, created_by, created_at, resolved_at, resolution_type, resolution_refs
		FROM runtime.discussion_threads WHERE anchor_type = $1 AND anchor_id = $2
		ORDER BY created_at DESC`, anchorType, anchorID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var threads []domain.DiscussionThread
	for rows.Next() {
		var t domain.DiscussionThread
		var topicKey, title, resolutionType *string
		if err := rows.Scan(
			&t.ThreadID, &t.AnchorType, &t.AnchorID, &topicKey, &title,
			&t.Status, &t.CreatedBy, &t.CreatedAt, &t.ResolvedAt,
			&resolutionType, &t.ResolutionRefs,
		); err != nil {
			return nil, err
		}
		if topicKey != nil {
			t.TopicKey = *topicKey
		}
		if title != nil {
			t.Title = *title
		}
		if resolutionType != nil {
			t.ResolutionType = domain.ResolutionType(*resolutionType)
		}
		threads = append(threads, t)
	}
	return threads, rows.Err()
}

func (s *PostgresStore) UpdateThread(ctx context.Context, thread *domain.DiscussionThread) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE runtime.discussion_threads
		SET status = $1, title = $2, resolved_at = $3, resolution_type = $4, resolution_refs = $5
		WHERE thread_id = $6`,
		thread.Status, nilIfEmpty(thread.Title), thread.ResolvedAt,
		nilIfEmpty(string(thread.ResolutionType)), thread.ResolutionRefs, thread.ThreadID,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.NewError(domain.ErrNotFound, "thread not found")
	}
	return nil
}

func (s *PostgresStore) CreateComment(ctx context.Context, comment *domain.Comment) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO runtime.comments (comment_id, thread_id, parent_comment_id, author_id, author_type, content, metadata, created_at, edited_at, deleted)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		comment.CommentID, comment.ThreadID, nilIfEmpty(comment.ParentCommentID),
		comment.AuthorID, comment.AuthorType, comment.Content, comment.Metadata,
		comment.CreatedAt, comment.EditedAt, comment.Deleted,
	)
	return err
}

func (s *PostgresStore) ListComments(ctx context.Context, threadID string) ([]domain.Comment, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT comment_id, thread_id, parent_comment_id, author_id, author_type, content, metadata, created_at, edited_at, deleted
		FROM runtime.comments WHERE thread_id = $1
		ORDER BY created_at ASC`, threadID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var comments []domain.Comment
	for rows.Next() {
		var c domain.Comment
		var parentCommentID *string
		if err := rows.Scan(
			&c.CommentID, &c.ThreadID, &parentCommentID,
			&c.AuthorID, &c.AuthorType, &c.Content, &c.Metadata,
			&c.CreatedAt, &c.EditedAt, &c.Deleted,
		); err != nil {
			return nil, err
		}
		if parentCommentID != nil {
			c.ParentCommentID = *parentCommentID
		}
		comments = append(comments, c)
	}
	return comments, rows.Err()
}

func (s *PostgresStore) HasOpenThreads(ctx context.Context, anchorType domain.AnchorType, anchorID string) (bool, error) {
	var count int
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM runtime.discussion_threads
		WHERE anchor_type = $1 AND anchor_id = $2 AND status = 'open'`,
		anchorType, anchorID,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// ── Helpers ──

func modeOrDefault(m domain.RunMode) string {
	if m == "" {
		return string(domain.RunModeStandard)
	}
	return string(m)
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
