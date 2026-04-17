package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	spinecrypto "github.com/bszymi/spine/internal/crypto"
	"github.com/bszymi/spine/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresStore implements Store using PostgreSQL via pgx.
type PostgresStore struct {
	pool   *pgxpool.Pool
	cipher *spinecrypto.SecretCipher
}

// SetSecretCipher installs the AEAD used to encrypt at-rest secrets
// (currently: event_subscriptions.signing_secret). With no cipher
// configured the store falls back to plaintext, matching behaviour
// before TASK-007 so integration tests and development setups work
// without additional configuration.
func (s *PostgresStore) SetSecretCipher(c *spinecrypto.SecretCipher) {
	s.cipher = c
}

// secretCipher returns the installed cipher or nil.
func (s *PostgresStore) secretCipher() *spinecrypto.SecretCipher {
	return s.cipher
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
const runColumns = `run_id, task_path, workflow_path, workflow_id, workflow_version, workflow_version_label, status, current_step_id, branch_name, trace_id, timeout_at, started_at, completed_at, created_at, mode, commit_meta`

// scanRun scans a single row into a domain.Run, handling nullable columns.
func scanRun(scanner interface{ Scan(dest ...any) error }) (domain.Run, error) {
	var run domain.Run
	var currentStepID, branchName *string
	var commitMeta map[string]string
	err := scanner.Scan(
		&run.RunID, &run.TaskPath, &run.WorkflowPath, &run.WorkflowID, &run.WorkflowVersion,
		&run.WorkflowVersionLabel, &run.Status, &currentStepID, &branchName, &run.TraceID,
		&run.TimeoutAt, &run.StartedAt, &run.CompletedAt, &run.CreatedAt, &run.Mode, &commitMeta,
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

// ── Actors ──

func (s *PostgresStore) GetActor(ctx context.Context, actorID string) (*domain.Actor, error) {
	var actor domain.Actor
	err := s.pool.QueryRow(ctx, `
		SELECT actor_id, actor_type, name, role, status
		FROM auth.actors WHERE actor_id = $1`, actorID,
	).Scan(&actor.ActorID, &actor.Type, &actor.Name, &actor.Role, &actor.Status)
	if err != nil {
		return nil, notFoundOr(err, "actor not found")
	}
	return &actor, nil
}

func (s *PostgresStore) CreateActor(ctx context.Context, actor *domain.Actor) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO auth.actors (actor_id, actor_type, name, role, status)
		VALUES ($1, $2, $3, $4, $5)`,
		actor.ActorID, actor.Type, actor.Name, actor.Role, actor.Status,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return domain.NewError(domain.ErrConflict, "actor_id already exists")
		}
		return err
	}
	return nil
}

func (s *PostgresStore) UpdateActor(ctx context.Context, actor *domain.Actor) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE auth.actors SET name = $1, role = $2, status = $3, updated_at = now()
		WHERE actor_id = $4`,
		actor.Name, actor.Role, actor.Status, actor.ActorID,
	)
	if err != nil {
		return err
	}
	return mustAffect(tag, "actor not found")
}

func (s *PostgresStore) ListActors(ctx context.Context) ([]domain.Actor, error) {
	return s.listActorsQuery(ctx, `
		SELECT actor_id, actor_type, name, role, status
		FROM auth.actors ORDER BY actor_id`)
}

func (s *PostgresStore) ListActorsByStatus(ctx context.Context, status domain.ActorStatus) ([]domain.Actor, error) {
	return s.listActorsQuery(ctx, `
		SELECT actor_id, actor_type, name, role, status
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
		if err := rows.Scan(&actor.ActorID, &actor.Type, &actor.Name, &actor.Role, &actor.Status); err != nil {
			return nil, err
		}
		actors = append(actors, actor)
	}
	return actors, rows.Err()
}

// ── Tokens ──

func (s *PostgresStore) GetActorByTokenHash(ctx context.Context, tokenHash string) (*domain.Actor, *domain.Token, error) {
	var actor domain.Actor
	var token domain.Token
	err := s.pool.QueryRow(ctx, `
		SELECT a.actor_id, a.actor_type, a.name, a.role, a.status,
		       t.token_id, t.actor_id, t.name, t.expires_at, t.revoked_at, t.created_at
		FROM auth.tokens t
		JOIN auth.actors a ON t.actor_id = a.actor_id
		WHERE t.token_hash = $1`, tokenHash,
	).Scan(
		&actor.ActorID, &actor.Type, &actor.Name, &actor.Role, &actor.Status,
		&token.TokenID, &token.ActorID, &token.Name, &token.ExpiresAt, &token.RevokedAt, &token.CreatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil, domain.NewError(domain.ErrUnauthorized, "invalid token")
		}
		return nil, nil, err
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
	return mustAffect(tag, "token not found or already revoked")
}

func (s *PostgresStore) ListTokensByActor(ctx context.Context, actorID string) ([]domain.Token, error) {
	return queryAll(ctx, s.pool, `
		SELECT token_id, actor_id, name, expires_at, revoked_at, created_at
		FROM auth.tokens WHERE actor_id = $1 ORDER BY created_at DESC`,
		[]any{actorID},
		func(row pgx.Rows, t *domain.Token) error {
			return row.Scan(&t.TokenID, &t.ActorID, &t.Name, &t.ExpiresAt, &t.RevokedAt, &t.CreatedAt)
		},
	)
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
		return nil, notFoundOr(err, "artifact not found")
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
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	// Delete existing links for this source, then insert new ones — atomically.
	if _, err := tx.Exec(ctx, `DELETE FROM projection.artifact_links WHERE source_path = $1`, sourcePath); err != nil {
		return err
	}
	for _, link := range links {
		if _, err := tx.Exec(ctx, `
			INSERT INTO projection.artifact_links (source_path, target_path, link_type, source_commit)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (source_path, target_path, link_type) DO UPDATE SET source_commit = EXCLUDED.source_commit`,
			link.SourcePath, link.TargetPath, link.LinkType, sourceCommit,
		); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
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
		return nil, notFoundOr(err, "workflow not found")
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
		return nil, notFoundOr(err, "skill not found")
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
	return mustAffect(tag, "skill not found")
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
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM auth.actor_skills
		WHERE actor_id = $1 AND skill_id = $2`,
		actorID, skillID,
	)
	if err != nil {
		return err
	}
	return mustAffect(tag, "actor-skill assignment not found")
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
		SELECT a.actor_id, a.actor_type, a.name, a.role, a.status
		FROM auth.actors a
		JOIN auth.actor_skills as_ ON a.actor_id = as_.actor_id
		JOIN auth.skills s ON as_.skill_id = s.skill_id
		WHERE a.status = 'active'
		  AND s.status = 'active'
		  AND s.name = ANY($1)
		GROUP BY a.actor_id, a.actor_type, a.name, a.role, a.status
		HAVING COUNT(DISTINCT s.name) = $2
		ORDER BY a.actor_id`, skillNames, len(skillNames))
}

// ── Execution Projections ──

func (s *PostgresStore) UpsertExecutionProjection(ctx context.Context, proj *ExecutionProjection) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO projection.execution_projections
			(task_path, task_id, title, status, required_skills, allowed_actor_types,
			 blocked, blocked_by, assigned_actor_id, assignment_status, run_id, workflow_step, last_updated)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, now())
		ON CONFLICT (task_path) DO UPDATE SET
			task_id = EXCLUDED.task_id,
			title = EXCLUDED.title,
			status = EXCLUDED.status,
			required_skills = EXCLUDED.required_skills,
			allowed_actor_types = EXCLUDED.allowed_actor_types,
			blocked = EXCLUDED.blocked,
			blocked_by = EXCLUDED.blocked_by,
			assigned_actor_id = EXCLUDED.assigned_actor_id,
			assignment_status = EXCLUDED.assignment_status,
			run_id = EXCLUDED.run_id,
			workflow_step = EXCLUDED.workflow_step,
			last_updated = now()`,
		proj.TaskPath, proj.TaskID, proj.Title, proj.Status,
		MarshalSkills(proj.RequiredSkills), MarshalSkills(proj.AllowedActorTypes),
		proj.Blocked, MarshalSkills(proj.BlockedBy),
		nilIfEmpty(proj.AssignedActorID), proj.AssignmentStatus,
		nilIfEmpty(proj.RunID), nilIfEmpty(proj.WorkflowStep),
	)
	return err
}

func (s *PostgresStore) GetExecutionProjection(ctx context.Context, taskPath string) (*ExecutionProjection, error) {
	var proj ExecutionProjection
	var reqSkills, actorTypes, blockedBy []byte
	var assignedActor, runID, wfStep *string
	err := s.pool.QueryRow(ctx, `
		SELECT task_path, task_id, title, status, required_skills, allowed_actor_types,
		       blocked, blocked_by, assigned_actor_id, assignment_status, run_id, workflow_step, last_updated
		FROM projection.execution_projections WHERE task_path = $1`, taskPath,
	).Scan(&proj.TaskPath, &proj.TaskID, &proj.Title, &proj.Status,
		&reqSkills, &actorTypes, &proj.Blocked, &blockedBy,
		&assignedActor, &proj.AssignmentStatus, &runID, &wfStep, &proj.LastUpdated)
	if err != nil {
		return nil, notFoundOr(err, "execution projection not found")
	}
	proj.RequiredSkills = UnmarshalSkills(reqSkills)
	proj.AllowedActorTypes = UnmarshalSkills(actorTypes)
	proj.BlockedBy = UnmarshalSkills(blockedBy)
	if assignedActor != nil {
		proj.AssignedActorID = *assignedActor
	}
	if runID != nil {
		proj.RunID = *runID
	}
	if wfStep != nil {
		proj.WorkflowStep = *wfStep
	}
	return &proj, nil
}

func (s *PostgresStore) QueryExecutionProjections(ctx context.Context, query ExecutionProjectionQuery) ([]ExecutionProjection, error) {
	sql := `SELECT task_path, task_id, title, status, required_skills, allowed_actor_types,
	               blocked, blocked_by, assigned_actor_id, assignment_status, run_id, workflow_step, last_updated
	        FROM projection.execution_projections WHERE 1=1`
	var args []any
	argN := 1

	if query.Blocked != nil {
		sql += fmt.Sprintf(" AND blocked = $%d", argN)
		args = append(args, *query.Blocked)
		argN++
	}
	if query.AssignmentStatus != "" {
		sql += fmt.Sprintf(" AND assignment_status = $%d", argN)
		args = append(args, query.AssignmentStatus)
		argN++
	}
	if query.AssignedActorID != "" {
		sql += fmt.Sprintf(" AND assigned_actor_id = $%d", argN)
		args = append(args, query.AssignedActorID)
		argN++
	}

	sql += " ORDER BY last_updated DESC"

	if query.Limit > 0 {
		sql += fmt.Sprintf(" LIMIT $%d", argN)
		args = append(args, query.Limit)
	}

	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []ExecutionProjection
	for rows.Next() {
		var proj ExecutionProjection
		var reqSkills, actorTypes, blockedBy []byte
		var assignedActor, runID, wfStep *string
		if err := rows.Scan(&proj.TaskPath, &proj.TaskID, &proj.Title, &proj.Status,
			&reqSkills, &actorTypes, &proj.Blocked, &blockedBy,
			&assignedActor, &proj.AssignmentStatus, &runID, &wfStep, &proj.LastUpdated,
		); err != nil {
			return nil, err
		}
		proj.RequiredSkills = UnmarshalSkills(reqSkills)
		proj.AllowedActorTypes = UnmarshalSkills(actorTypes)
		proj.BlockedBy = UnmarshalSkills(blockedBy)
		if assignedActor != nil {
			proj.AssignedActorID = *assignedActor
		}
		if runID != nil {
			proj.RunID = *runID
		}
		if wfStep != nil {
			proj.WorkflowStep = *wfStep
		}
		results = append(results, proj)
	}
	return results, rows.Err()
}

func (s *PostgresStore) DeleteExecutionProjection(ctx context.Context, taskPath string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM projection.execution_projections WHERE task_path = $1`, taskPath)
	return err
}

// ── Event Delivery Queue ──

func (s *PostgresStore) EnqueueDelivery(ctx context.Context, entry *DeliveryEntry) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO runtime.event_delivery_queue
			(delivery_id, subscription_id, event_id, event_type, payload, status, attempt_count, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (subscription_id, event_id) DO NOTHING`,
		entry.DeliveryID, entry.SubscriptionID, entry.EventID, entry.EventType,
		entry.Payload, entry.Status, entry.AttemptCount, entry.CreatedAt,
	)
	return err
}

func (s *PostgresStore) ClaimDeliveries(ctx context.Context, limit int) ([]DeliveryEntry, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE runtime.event_delivery_queue
		SET status = 'delivering'
		WHERE delivery_id IN (
			SELECT delivery_id FROM runtime.event_delivery_queue
			WHERE status IN ('pending', 'failed')
			  AND (next_retry_at IS NULL OR next_retry_at <= now())
			ORDER BY created_at
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING delivery_id, subscription_id, event_id, event_type, payload,
		          status, attempt_count, next_retry_at, last_error, created_at, delivered_at`,
		limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []DeliveryEntry
	for rows.Next() {
		var e DeliveryEntry
		var lastError *string
		if err := rows.Scan(&e.DeliveryID, &e.SubscriptionID, &e.EventID, &e.EventType,
			&e.Payload, &e.Status, &e.AttemptCount, &e.NextRetryAt, &lastError,
			&e.CreatedAt, &e.DeliveredAt); err != nil {
			return nil, err
		}
		if lastError != nil {
			e.LastError = *lastError
		}
		results = append(results, e)
	}
	return results, rows.Err()
}

func (s *PostgresStore) UpdateDeliveryStatus(ctx context.Context, deliveryID, status string, lastError string, nextRetryAt *time.Time) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE runtime.event_delivery_queue
		SET status = $2, last_error = $3, next_retry_at = $4, attempt_count = attempt_count + 1
		WHERE delivery_id = $1`,
		deliveryID, status, nilIfEmpty(lastError), nextRetryAt)
	return err
}

func (s *PostgresStore) MarkDelivered(ctx context.Context, deliveryID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE runtime.event_delivery_queue
		SET status = 'delivered', delivered_at = now(), attempt_count = attempt_count + 1
		WHERE delivery_id = $1`,
		deliveryID)
	return err
}

func (s *PostgresStore) LogDeliveryAttempt(ctx context.Context, entry *DeliveryLogEntry) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO runtime.event_delivery_log
			(log_id, delivery_id, subscription_id, event_id, status_code, duration_ms, error, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		entry.LogID, entry.DeliveryID, entry.SubscriptionID, entry.EventID,
		entry.StatusCode, entry.DurationMs, nilIfEmpty(entry.Error), entry.CreatedAt,
	)
	return err
}

func (s *PostgresStore) ListDeliveryHistory(ctx context.Context, query DeliveryHistoryQuery) ([]DeliveryLogEntry, error) {
	limit := query.Limit
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT log_id, delivery_id, subscription_id, event_id, status_code, duration_ms, error, created_at
		FROM runtime.event_delivery_log
		WHERE subscription_id = $1
		ORDER BY created_at DESC
		LIMIT $2`,
		query.SubscriptionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []DeliveryLogEntry
	for rows.Next() {
		var e DeliveryLogEntry
		var statusCode, durationMs *int
		var errStr *string
		if err := rows.Scan(&e.LogID, &e.DeliveryID, &e.SubscriptionID, &e.EventID,
			&statusCode, &durationMs, &errStr, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.StatusCode = statusCode
		e.DurationMs = durationMs
		if errStr != nil {
			e.Error = *errStr
		}
		results = append(results, e)
	}
	return results, rows.Err()
}

func (s *PostgresStore) GetDelivery(ctx context.Context, deliveryID string) (*DeliveryEntry, error) {
	var e DeliveryEntry
	var lastError *string
	err := s.pool.QueryRow(ctx, `
		SELECT delivery_id, subscription_id, event_id, event_type, payload,
		       status, attempt_count, next_retry_at, last_error, created_at, delivered_at
		FROM runtime.event_delivery_queue WHERE delivery_id = $1`, deliveryID,
	).Scan(&e.DeliveryID, &e.SubscriptionID, &e.EventID, &e.EventType,
		&e.Payload, &e.Status, &e.AttemptCount, &e.NextRetryAt, &lastError,
		&e.CreatedAt, &e.DeliveredAt)
	if err != nil {
		return nil, notFoundOr(err, "delivery not found")
	}
	if lastError != nil {
		e.LastError = *lastError
	}
	return &e, nil
}

func (s *PostgresStore) ListDeliveries(ctx context.Context, subscriptionID string, status string, limit int) ([]DeliveryEntry, error) {
	if limit <= 0 {
		limit = 50
	}

	sql := `SELECT delivery_id, subscription_id, event_id, event_type, payload,
	               status, attempt_count, next_retry_at, last_error, created_at, delivered_at
	        FROM runtime.event_delivery_queue WHERE subscription_id = $1`
	args := []any{subscriptionID}
	argN := 2

	if status != "" {
		sql += fmt.Sprintf(" AND status = $%d", argN)
		args = append(args, status)
		argN++
	}

	sql += " ORDER BY created_at DESC"
	sql += fmt.Sprintf(" LIMIT $%d", argN)
	args = append(args, limit)

	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []DeliveryEntry
	for rows.Next() {
		var e DeliveryEntry
		var lastError *string
		if err := rows.Scan(&e.DeliveryID, &e.SubscriptionID, &e.EventID, &e.EventType,
			&e.Payload, &e.Status, &e.AttemptCount, &e.NextRetryAt, &lastError,
			&e.CreatedAt, &e.DeliveredAt); err != nil {
			return nil, err
		}
		if lastError != nil {
			e.LastError = *lastError
		}
		results = append(results, e)
	}
	return results, rows.Err()
}

func (s *PostgresStore) GetDeliveryStats(ctx context.Context, subscriptionID string) (*DeliveryStats, error) {
	var stats DeliveryStats
	err := s.pool.QueryRow(ctx, `
		SELECT
			COUNT(*) AS total,
			COUNT(*) FILTER (WHERE status = 'delivered') AS delivered,
			COUNT(*) FILTER (WHERE status = 'failed') AS failed,
			COUNT(*) FILTER (WHERE status = 'dead') AS dead,
			COUNT(*) FILTER (WHERE status IN ('pending', 'delivering')) AS pending
		FROM runtime.event_delivery_queue
		WHERE subscription_id = $1`, subscriptionID,
	).Scan(&stats.TotalDeliveries, &stats.Delivered, &stats.Failed, &stats.Dead, &stats.Pending)
	if err != nil {
		return nil, err
	}

	if stats.TotalDeliveries > 0 {
		stats.SuccessRate = float64(stats.Delivered) / float64(stats.TotalDeliveries)
	}

	// Average latency from delivery log
	var avgMs *float64
	err = s.pool.QueryRow(ctx, `
		SELECT AVG(duration_ms)::float8
		FROM runtime.event_delivery_log
		WHERE subscription_id = $1 AND status_code IS NOT NULL`, subscriptionID,
	).Scan(&avgMs)
	if err == nil && avgMs != nil {
		v := int(*avgMs)
		stats.AvgLatencyMs = &v
	}

	return &stats, nil
}

func (s *PostgresStore) WriteEventLog(ctx context.Context, entry *EventLogEntry) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO runtime.event_log (event_id, event_type, payload, created_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (event_id) DO NOTHING`,
		entry.EventID, entry.EventType, entry.Payload, entry.CreatedAt)
	return err
}

func (s *PostgresStore) ListEventsAfter(ctx context.Context, afterEventID string, eventTypes []string, limit int) ([]EventLogEntry, error) {
	if limit <= 0 {
		limit = 50
	}

	sql := `SELECT event_id, event_type, payload, created_at
	        FROM runtime.event_log WHERE 1=1`
	args := []any{}
	argN := 1

	if afterEventID != "" {
		sql += fmt.Sprintf(` AND created_at > (SELECT created_at FROM runtime.event_log WHERE event_id = $%d)`, argN)
		args = append(args, afterEventID)
		argN++
	}

	if len(eventTypes) > 0 {
		sql += fmt.Sprintf(` AND event_type = ANY($%d)`, argN)
		args = append(args, eventTypes)
		argN++
	}

	sql += ` ORDER BY created_at ASC`
	sql += fmt.Sprintf(` LIMIT $%d`, argN)
	args = append(args, limit)

	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []EventLogEntry
	for rows.Next() {
		var e EventLogEntry
		if err := rows.Scan(&e.EventID, &e.EventType, &e.Payload, &e.CreatedAt); err != nil {
			return nil, err
		}
		results = append(results, e)
	}
	return results, rows.Err()
}

func (s *PostgresStore) DeleteExpiredDeliveries(ctx context.Context, before time.Time) (int64, error) {
	// Delete log entries for expired deliveries first (FK constraint)
	_, err := s.pool.Exec(ctx, `
		DELETE FROM runtime.event_delivery_log
		WHERE delivery_id IN (
			SELECT delivery_id FROM runtime.event_delivery_queue
			WHERE created_at < $1 AND status IN ('delivered', 'dead')
		)`, before)
	if err != nil {
		return 0, err
	}

	tag, err := s.pool.Exec(ctx, `
		DELETE FROM runtime.event_delivery_queue
		WHERE created_at < $1 AND status IN ('delivered', 'dead')`, before)
	if err != nil {
		return 0, err
	}

	// Clean up event log entries past retention
	_, _ = s.pool.Exec(ctx, `DELETE FROM runtime.event_log WHERE created_at < $1`, before)

	return tag.RowsAffected(), nil
}

// ── Event Subscriptions ──

func (s *PostgresStore) CreateSubscription(ctx context.Context, sub *EventSubscription) error {
	secret, err := s.encryptSubscriptionSecret(sub.SigningSecret)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO runtime.event_subscriptions
			(subscription_id, workspace_id, name, target_type, target_url, event_types,
			 signing_secret, status, metadata, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		sub.SubscriptionID, nilIfEmpty(sub.WorkspaceID), sub.Name, sub.TargetType, sub.TargetURL,
		sub.EventTypes, secret, sub.Status, sub.Metadata,
		sub.CreatedBy, sub.CreatedAt, sub.UpdatedAt,
	)
	return err
}

// encryptSubscriptionSecret encrypts a signing secret at rest when a
// cipher is configured. If the value is already ciphertext (i.e. it
// was re-saved without being decrypted first) it is passed through so
// we never double-encrypt.
func (s *PostgresStore) encryptSubscriptionSecret(secret string) (string, error) {
	if s.cipher == nil || secret == "" || spinecrypto.IsEncrypted(secret) {
		return secret, nil
	}
	return s.cipher.Encrypt(secret)
}

// decryptSubscriptionSecret reverses encryptSubscriptionSecret.
// Pre-migration plaintext rows are returned as-is; they will be
// re-encrypted on their next UpdateSubscription call.
func (s *PostgresStore) decryptSubscriptionSecret(stored string) (string, error) {
	if stored == "" {
		return stored, nil
	}
	return s.cipher.Decrypt(stored)
}

func (s *PostgresStore) GetSubscription(ctx context.Context, subscriptionID string) (*EventSubscription, error) {
	var sub EventSubscription
	var wsID *string
	err := s.pool.QueryRow(ctx, `
		SELECT subscription_id, workspace_id, name, target_type, target_url, event_types,
		       signing_secret, status, metadata, created_by, created_at, updated_at
		FROM runtime.event_subscriptions WHERE subscription_id = $1`, subscriptionID,
	).Scan(&sub.SubscriptionID, &wsID, &sub.Name, &sub.TargetType, &sub.TargetURL,
		&sub.EventTypes, &sub.SigningSecret, &sub.Status, &sub.Metadata,
		&sub.CreatedBy, &sub.CreatedAt, &sub.UpdatedAt)
	if err != nil {
		return nil, notFoundOr(err, "subscription not found")
	}
	if wsID != nil {
		sub.WorkspaceID = *wsID
	}
	secret, err := s.decryptSubscriptionSecret(sub.SigningSecret)
	if err != nil {
		return nil, fmt.Errorf("decrypt signing_secret: %w", err)
	}
	sub.SigningSecret = secret
	return &sub, nil
}

func (s *PostgresStore) UpdateSubscription(ctx context.Context, sub *EventSubscription) error {
	secret, err := s.encryptSubscriptionSecret(sub.SigningSecret)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		UPDATE runtime.event_subscriptions SET
			name = $2, target_type = $3, target_url = $4, event_types = $5,
			signing_secret = $6, status = $7, metadata = $8, updated_at = now()
		WHERE subscription_id = $1`,
		sub.SubscriptionID, sub.Name, sub.TargetType, sub.TargetURL,
		sub.EventTypes, secret, sub.Status, sub.Metadata,
	)
	return err
}

func (s *PostgresStore) DeleteSubscription(ctx context.Context, subscriptionID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM runtime.event_subscriptions WHERE subscription_id = $1`, subscriptionID)
	return err
}

func (s *PostgresStore) ListSubscriptions(ctx context.Context, workspaceID string) ([]EventSubscription, error) {
	var rows pgx.Rows
	var err error
	if workspaceID == "" {
		rows, err = s.pool.Query(ctx, `
			SELECT subscription_id, workspace_id, name, target_type, target_url, event_types,
			       signing_secret, status, metadata, created_by, created_at, updated_at
			FROM runtime.event_subscriptions ORDER BY created_at`)
	} else {
		rows, err = s.pool.Query(ctx, `
			SELECT subscription_id, workspace_id, name, target_type, target_url, event_types,
			       signing_secret, status, metadata, created_by, created_at, updated_at
			FROM runtime.event_subscriptions WHERE workspace_id = $1 ORDER BY created_at`, workspaceID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.scanSubscriptions(rows)
}

func (s *PostgresStore) ListActiveSubscriptionsByEventType(ctx context.Context, eventType string) ([]EventSubscription, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT subscription_id, workspace_id, name, target_type, target_url, event_types,
		       signing_secret, status, metadata, created_by, created_at, updated_at
		FROM runtime.event_subscriptions
		WHERE status = 'active'
		  AND (event_types = '{}' OR $1 = ANY(event_types))
		ORDER BY created_at`, eventType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.scanSubscriptions(rows)
}

func (s *PostgresStore) scanSubscriptions(rows pgx.Rows) ([]EventSubscription, error) {
	var results []EventSubscription
	for rows.Next() {
		var sub EventSubscription
		var wsID *string
		if err := rows.Scan(&sub.SubscriptionID, &wsID, &sub.Name, &sub.TargetType, &sub.TargetURL,
			&sub.EventTypes, &sub.SigningSecret, &sub.Status, &sub.Metadata,
			&sub.CreatedBy, &sub.CreatedAt, &sub.UpdatedAt); err != nil {
			return nil, err
		}
		if wsID != nil {
			sub.WorkspaceID = *wsID
		}
		secret, err := s.decryptSubscriptionSecret(sub.SigningSecret)
		if err != nil {
			return nil, fmt.Errorf("decrypt signing_secret: %w", err)
		}
		sub.SigningSecret = secret
		results = append(results, sub)
	}
	return results, rows.Err()
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
		return nil, notFoundOr(err, "thread not found")
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
	return mustAffect(tag, "thread not found")
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
	return queryAll(ctx, s.pool, `
		SELECT comment_id, thread_id, parent_comment_id, author_id, author_type, content, metadata, created_at, edited_at, deleted
		FROM runtime.comments WHERE thread_id = $1
		ORDER BY created_at ASC`,
		[]any{threadID},
		func(row pgx.Rows, c *domain.Comment) error {
			var parentCommentID *string
			if err := row.Scan(
				&c.CommentID, &c.ThreadID, &parentCommentID,
				&c.AuthorID, &c.AuthorType, &c.Content, &c.Metadata,
				&c.CreatedAt, &c.EditedAt, &c.Deleted,
			); err != nil {
				return err
			}
			if parentCommentID != nil {
				c.ParentCommentID = *parentCommentID
			}
			return nil
		},
	)
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
