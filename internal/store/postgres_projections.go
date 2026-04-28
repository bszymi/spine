package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

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

	// Clamp at the store boundary so a misbehaving internal caller
	// (any non-HTTP path that builds an ArtifactQuery directly)
	// cannot interpolate an unbounded LIMIT into the SQL below.
	limit := query.ClampedLimit()

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
	// execution_projections is path-keyed off artifacts and re-populated
	// on each sync pass, so a rebuild must wipe it too — otherwise rows
	// for renamed/removed artifacts survive forever and keep showing up
	// in /api/v1/execution/tasks/ready.
	if _, err := s.pool.Exec(ctx, "DELETE FROM projection.execution_projections"); err != nil {
		return fmt.Errorf("delete execution_projections: %w", err)
	}
	// Intentionally omit projection.branch_protection_rules — it is
	// replaced atomically by UpsertBranchProtectionRules, so wiping it
	// here would open a window where the rule-source adapter sees an
	// empty table (treated as "explicit empty, unprotected") if the
	// subsequent projection step fails for any reason. The atomic
	// swap preserves the previous ruleset until replacement succeeds.
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

// ── Branch Protection Rules ──

// UpsertBranchProtectionRules atomically replaces the entire effective
// ruleset. An empty rules slice leaves zero rows (the author's explicit
// "nothing protected" choice); ADR-009 bootstrap defaults are applied
// upstream by the projection handler when the config file is absent, not
// by this method. sourceCommit is the commit that produced the projection
// and is stamped on every inserted row.
func (s *PostgresStore) UpsertBranchProtectionRules(ctx context.Context, rules []BranchProtectionRuleProjection, sourceCommit string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	if _, err := tx.Exec(ctx, `DELETE FROM projection.branch_protection_rules`); err != nil {
		return fmt.Errorf("clear branch_protection_rules: %w", err)
	}
	for _, r := range rules {
		if _, err := tx.Exec(ctx, `
			INSERT INTO projection.branch_protection_rules (branch_pattern, rule_order, protections, source_commit)
			VALUES ($1, $2, $3, $4)`,
			r.BranchPattern, r.RuleOrder, r.Protections, sourceCommit,
		); err != nil {
			return fmt.Errorf("insert branch_protection_rules: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// ListBranchProtectionRules returns every projected rule in source-file
// order. The branchprotect package relies on this ordering to preserve
// the author's intended match sequence (config.Config.MatchRules
// contract).
func (s *PostgresStore) ListBranchProtectionRules(ctx context.Context) ([]BranchProtectionRuleProjection, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT branch_pattern, rule_order, protections, source_commit
		FROM projection.branch_protection_rules
		ORDER BY rule_order, branch_pattern`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var projections []BranchProtectionRuleProjection
	for rows.Next() {
		var p BranchProtectionRuleProjection
		if err := rows.Scan(&p.BranchPattern, &p.RuleOrder, &p.Protections, &p.SourceCommit); err != nil {
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
