package store

import (
	"context"
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
		INSERT INTO runtime.runs (run_id, task_path, workflow_path, workflow_id, workflow_version, workflow_version_label, status, current_step_id, trace_id, started_at, completed_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		run.RunID, run.TaskPath, run.WorkflowPath, run.WorkflowID, run.WorkflowVersion,
		run.WorkflowVersionLabel, run.Status, nilIfEmpty(run.CurrentStepID), run.TraceID,
		run.StartedAt, run.CompletedAt, run.CreatedAt,
	)
	return err
}

func (s *PostgresStore) GetRun(ctx context.Context, runID string) (*domain.Run, error) {
	var run domain.Run
	var currentStepID *string
	err := s.pool.QueryRow(ctx, `
		SELECT run_id, task_path, workflow_path, workflow_id, workflow_version, workflow_version_label, status, current_step_id, trace_id, started_at, completed_at, created_at
		FROM runtime.runs WHERE run_id = $1`, runID,
	).Scan(
		&run.RunID, &run.TaskPath, &run.WorkflowPath, &run.WorkflowID, &run.WorkflowVersion,
		&run.WorkflowVersionLabel, &run.Status, &currentStepID, &run.TraceID,
		&run.StartedAt, &run.CompletedAt, &run.CreatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, domain.NewError(domain.ErrNotFound, "run not found")
		}
		return nil, err
	}
	if currentStepID != nil {
		run.CurrentStepID = *currentStepID
	}
	return &run, nil
}

func (s *PostgresStore) UpdateRunStatus(ctx context.Context, runID string, status domain.RunStatus) error {
	tag, err := s.pool.Exec(ctx, `UPDATE runtime.runs SET status = $1 WHERE run_id = $2`, status, runID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.NewError(domain.ErrNotFound, "run not found")
	}
	return nil
}

func (s *PostgresStore) ListRunsByTask(ctx context.Context, taskPath string) ([]domain.Run, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT run_id, task_path, workflow_path, workflow_id, workflow_version, workflow_version_label, status, current_step_id, trace_id, started_at, completed_at, created_at
		FROM runtime.runs WHERE task_path = $1 ORDER BY created_at DESC`, taskPath,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []domain.Run
	for rows.Next() {
		var run domain.Run
		var currentStepID *string
		if err := rows.Scan(
			&run.RunID, &run.TaskPath, &run.WorkflowPath, &run.WorkflowID, &run.WorkflowVersion,
			&run.WorkflowVersionLabel, &run.Status, &currentStepID, &run.TraceID,
			&run.StartedAt, &run.CompletedAt, &run.CreatedAt,
		); err != nil {
			return nil, err
		}
		if currentStepID != nil {
			run.CurrentStepID = *currentStepID
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

// ── Step Executions ──

func (s *PostgresStore) CreateStepExecution(ctx context.Context, exec *domain.StepExecution) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO runtime.step_executions (execution_id, run_id, step_id, branch_id, actor_id, status, attempt, outcome_id, started_at, completed_at, error_detail, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		exec.ExecutionID, exec.RunID, exec.StepID, nilIfEmpty(exec.BranchID),
		nilIfEmpty(exec.ActorID), exec.Status, exec.Attempt, nilIfEmpty(exec.OutcomeID),
		exec.StartedAt, exec.CompletedAt, exec.ErrorDetail, exec.CreatedAt,
	)
	return err
}

func (s *PostgresStore) GetStepExecution(ctx context.Context, executionID string) (*domain.StepExecution, error) {
	var exec domain.StepExecution
	var branchID, actorID, outcomeID *string
	err := s.pool.QueryRow(ctx, `
		SELECT execution_id, run_id, step_id, branch_id, actor_id, status, attempt, outcome_id, started_at, completed_at, error_detail, created_at
		FROM runtime.step_executions WHERE execution_id = $1`, executionID,
	).Scan(
		&exec.ExecutionID, &exec.RunID, &exec.StepID, &branchID, &actorID,
		&exec.Status, &exec.Attempt, &outcomeID,
		&exec.StartedAt, &exec.CompletedAt, &exec.ErrorDetail, &exec.CreatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, domain.NewError(domain.ErrNotFound, "step execution not found")
		}
		return nil, err
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
	return &exec, nil
}

func (s *PostgresStore) UpdateStepExecution(ctx context.Context, exec *domain.StepExecution) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE runtime.step_executions
		SET status = $1, actor_id = $2, outcome_id = $3, started_at = $4, completed_at = $5, error_detail = $6
		WHERE execution_id = $7`,
		exec.Status, nilIfEmpty(exec.ActorID), nilIfEmpty(exec.OutcomeID),
		exec.StartedAt, exec.CompletedAt, exec.ErrorDetail, exec.ExecutionID,
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
	rows, err := s.pool.Query(ctx, `
		SELECT execution_id, run_id, step_id, branch_id, actor_id, status, attempt, outcome_id, started_at, completed_at, error_detail, created_at
		FROM runtime.step_executions WHERE run_id = $1 ORDER BY created_at`, runID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var execs []domain.StepExecution
	for rows.Next() {
		var exec domain.StepExecution
		var branchID, actorID, outcomeID *string
		if err := rows.Scan(
			&exec.ExecutionID, &exec.RunID, &exec.StepID, &branchID, &actorID,
			&exec.Status, &exec.Attempt, &outcomeID,
			&exec.StartedAt, &exec.CompletedAt, &exec.ErrorDetail, &exec.CreatedAt,
		); err != nil {
			return nil, err
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
		execs = append(execs, exec)
	}
	return execs, rows.Err()
}

// ── Scheduler Queries ──

func (s *PostgresStore) ListRunsByStatus(ctx context.Context, status domain.RunStatus) ([]domain.Run, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT run_id, task_path, workflow_path, workflow_id, workflow_version, workflow_version_label, status, current_step_id, trace_id, started_at, completed_at, created_at
		FROM runtime.runs WHERE status = $1 ORDER BY created_at`, status,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []domain.Run
	for rows.Next() {
		var run domain.Run
		var currentStepID *string
		if err := rows.Scan(
			&run.RunID, &run.TaskPath, &run.WorkflowPath, &run.WorkflowID, &run.WorkflowVersion,
			&run.WorkflowVersionLabel, &run.Status, &currentStepID, &run.TraceID,
			&run.StartedAt, &run.CompletedAt, &run.CreatedAt,
		); err != nil {
			return nil, err
		}
		if currentStepID != nil {
			run.CurrentStepID = *currentStepID
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (s *PostgresStore) ListActiveStepExecutions(ctx context.Context) ([]domain.StepExecution, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT execution_id, run_id, step_id, branch_id, actor_id, status, attempt, outcome_id, started_at, completed_at, error_detail, created_at
		FROM runtime.step_executions
		WHERE status NOT IN ('completed', 'failed', 'skipped')
		ORDER BY created_at`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var execs []domain.StepExecution
	for rows.Next() {
		var exec domain.StepExecution
		var branchID, actorID, outcomeID *string
		if err := rows.Scan(
			&exec.ExecutionID, &exec.RunID, &exec.StepID, &branchID, &actorID,
			&exec.Status, &exec.Attempt, &outcomeID,
			&exec.StartedAt, &exec.CompletedAt, &exec.ErrorDetail, &exec.CreatedAt,
		); err != nil {
			return nil, err
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
		execs = append(execs, exec)
	}
	return execs, rows.Err()
}

func (s *PostgresStore) ListStaleActiveRuns(ctx context.Context, noActivitySince time.Time) ([]domain.Run, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT r.run_id, r.task_path, r.workflow_path, r.workflow_id, r.workflow_version, r.workflow_version_label, r.status, r.current_step_id, r.trace_id, r.started_at, r.completed_at, r.created_at
		FROM runtime.runs r
		WHERE r.status = 'active'
		AND NOT EXISTS (
			SELECT 1 FROM runtime.step_executions se
			WHERE se.run_id = r.run_id AND se.created_at > $1
		)
		AND r.created_at < $1
		ORDER BY r.created_at`, noActivitySince,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []domain.Run
	for rows.Next() {
		var run domain.Run
		var currentStepID *string
		if err := rows.Scan(
			&run.RunID, &run.TaskPath, &run.WorkflowPath, &run.WorkflowID, &run.WorkflowVersion,
			&run.WorkflowVersionLabel, &run.Status, &currentStepID, &run.TraceID,
			&run.StartedAt, &run.CompletedAt, &run.CreatedAt,
		); err != nil {
			return nil, err
		}
		if currentStepID != nil {
			run.CurrentStepID = *currentStepID
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
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
	tables := []string{
		"projection.artifact_links",
		"projection.artifacts",
		"projection.workflows",
	}
	for _, table := range tables {
		if _, err := s.pool.Exec(ctx, "DELETE FROM "+table); err != nil {
			return fmt.Errorf("delete %s: %w", table, err)
		}
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

// ── Helpers ──

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
