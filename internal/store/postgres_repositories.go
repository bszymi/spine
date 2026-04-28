package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/bszymi/spine/internal/domain"
)

// ── Repository Bindings (ADR-013, INIT-014 EPIC-001 TASK-002) ──
//
// Bindings hold operational connection details for code repositories.
// The primary "spine" repository is resolved virtually from the
// workspace state (RepoPath + authoritative branch) and never gets a
// row — the migration enforces that with a CHECK constraint, and
// CreateRepositoryBinding rejects the reserved ID up front so callers
// see a clear domain error rather than a generic database constraint
// violation.

const repositoryBindingColumns = `
	repository_id, workspace_id, clone_url, credentials_ref, local_path,
	default_branch, status, created_at, updated_at`

func (s *PostgresStore) CreateRepositoryBinding(ctx context.Context, b *RepositoryBinding) error {
	if b == nil {
		return domain.NewError(domain.ErrInvalidParams, "repository binding required")
	}
	if b.RepositoryID == PrimaryRepositoryID {
		return domain.NewError(domain.ErrInvalidParams,
			"the primary 'spine' repository has no binding row; it is resolved virtually from workspace state")
	}
	status := b.Status
	if status == "" {
		status = RepositoryBindingStatusActive
	}

	_, err := s.pool.Exec(ctx, `
		INSERT INTO runtime.repositories (
			repository_id, workspace_id, clone_url, credentials_ref, local_path,
			default_branch, status
		) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		b.RepositoryID, b.WorkspaceID, b.CloneURL, nilIfEmpty(b.CredentialsRef),
		b.LocalPath, nilIfEmpty(b.DefaultBranch), status,
	)
	return err
}

func (s *PostgresStore) GetRepositoryBinding(ctx context.Context, workspaceID, repositoryID string) (*RepositoryBinding, error) {
	return s.queryRepositoryBindingRow(ctx,
		`SELECT `+repositoryBindingColumns+`
		 FROM runtime.repositories
		 WHERE workspace_id = $1 AND repository_id = $2`,
		workspaceID, repositoryID,
	)
}

// GetActiveRepositoryBinding returns the binding only when it is
// active. An inactive binding is reported as not found so callers on
// the execution hot-path (clone resolution, run-branch routing) cannot
// accidentally operate against a deactivated repository.
func (s *PostgresStore) GetActiveRepositoryBinding(ctx context.Context, workspaceID, repositoryID string) (*RepositoryBinding, error) {
	return s.queryRepositoryBindingRow(ctx,
		`SELECT `+repositoryBindingColumns+`
		 FROM runtime.repositories
		 WHERE workspace_id = $1 AND repository_id = $2 AND status = 'active'`,
		workspaceID, repositoryID,
	)
}

func (s *PostgresStore) UpdateRepositoryBinding(ctx context.Context, b *RepositoryBinding) error {
	if b == nil {
		return domain.NewError(domain.ErrInvalidParams, "repository binding required")
	}
	if b.RepositoryID == PrimaryRepositoryID {
		return domain.NewError(domain.ErrInvalidParams,
			"the primary 'spine' repository has no binding row; it is resolved virtually from workspace state")
	}

	// Pass NULL when the caller leaves Status unset and let COALESCE
	// preserve the existing row value. Otherwise an update that only
	// rewrites operational fields (clone URL, local path, ...) on a
	// deactivated binding would silently flip it back to active and
	// expose it again to GetActiveRepositoryBinding.
	tag, err := s.pool.Exec(ctx, `
		UPDATE runtime.repositories
		SET clone_url = $1,
		    credentials_ref = $2,
		    local_path = $3,
		    default_branch = $4,
		    status = COALESCE($5, status),
		    updated_at = now()
		WHERE workspace_id = $6 AND repository_id = $7`,
		b.CloneURL, nilIfEmpty(b.CredentialsRef), b.LocalPath,
		nilIfEmpty(b.DefaultBranch), nilIfEmpty(b.Status),
		b.WorkspaceID, b.RepositoryID,
	)
	if err != nil {
		return err
	}
	return mustAffect(tag, "repository binding not found")
}

func (s *PostgresStore) ListRepositoryBindings(ctx context.Context, workspaceID string) ([]RepositoryBinding, error) {
	return queryAll(ctx, s.pool,
		`SELECT `+repositoryBindingColumns+`
		 FROM runtime.repositories
		 WHERE workspace_id = $1
		 ORDER BY repository_id`,
		[]any{workspaceID},
		scanRepositoryBinding,
	)
}

func (s *PostgresStore) ListActiveRepositoryBindings(ctx context.Context, workspaceID string) ([]RepositoryBinding, error) {
	return queryAll(ctx, s.pool,
		`SELECT `+repositoryBindingColumns+`
		 FROM runtime.repositories
		 WHERE workspace_id = $1 AND status = 'active'
		 ORDER BY repository_id`,
		[]any{workspaceID},
		scanRepositoryBinding,
	)
}

func (s *PostgresStore) DeactivateRepositoryBinding(ctx context.Context, workspaceID, repositoryID string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE runtime.repositories
		SET status = 'inactive',
		    updated_at = now()
		WHERE workspace_id = $1 AND repository_id = $2`,
		workspaceID, repositoryID,
	)
	if err != nil {
		return err
	}
	return mustAffect(tag, "repository binding not found")
}

func (s *PostgresStore) queryRepositoryBindingRow(ctx context.Context, sql string, args ...any) (*RepositoryBinding, error) {
	var b RepositoryBinding
	var credentialsRef, defaultBranch *string
	err := s.pool.QueryRow(ctx, sql, args...).Scan(
		&b.RepositoryID, &b.WorkspaceID, &b.CloneURL, &credentialsRef,
		&b.LocalPath, &defaultBranch, &b.Status, &b.CreatedAt, &b.UpdatedAt,
	)
	if err != nil {
		return nil, notFoundOr(err, "repository binding not found")
	}
	if credentialsRef != nil {
		b.CredentialsRef = *credentialsRef
	}
	if defaultBranch != nil {
		b.DefaultBranch = *defaultBranch
	}
	return &b, nil
}

func scanRepositoryBinding(rows pgx.Rows, b *RepositoryBinding) error {
	var credentialsRef, defaultBranch *string
	if err := rows.Scan(
		&b.RepositoryID, &b.WorkspaceID, &b.CloneURL, &credentialsRef,
		&b.LocalPath, &defaultBranch, &b.Status, &b.CreatedAt, &b.UpdatedAt,
	); err != nil {
		return err
	}
	if credentialsRef != nil {
		b.CredentialsRef = *credentialsRef
	}
	if defaultBranch != nil {
		b.DefaultBranch = *defaultBranch
	}
	return nil
}
