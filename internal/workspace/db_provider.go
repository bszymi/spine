package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bszymi/spine/internal/secrets"
)

// DBProvider implements Resolver by reading workspace configuration from a
// PostgreSQL workspace registry table. This is the provider used in shared
// runtime mode. Resolved configs are cached in memory with a configurable TTL.
//
// The `database_url` column stores either a direct PostgreSQL URL
// (legacy rows) or a secret-store ref of the form
// `secret-store://workspaces/{id}/runtime_db`. When SecretClient is
// configured, ref-shaped values are dereferenced at Resolve time and
// the resulting credential is wrapped in secrets.SecretValue so it
// redacts in logs and JSON. Legacy URL values are wrapped as-is.
type DBProvider struct {
	pool         *pgxpool.Pool
	cacheTTL     time.Duration
	mu           sync.RWMutex
	cache        map[string]cachedConfig
	lastListAt   time.Time
	listCache    []Config
	secretClient secrets.SecretClient
}

type cachedConfig struct {
	config    Config
	fetchedAt time.Time
}

// DBProviderConfig holds configuration for the database provider.
type DBProviderConfig struct {
	// CacheTTL is how long resolved configs are cached before refresh.
	// Default: 60 seconds.
	CacheTTL time.Duration

	// SecretClient is used to dereference ref-shaped values stored in
	// the `database_url` column. Optional: when nil, every column
	// value is treated as a literal URL. When set, values that begin
	// with `secret-store://` are passed to SecretClient.Get.
	SecretClient secrets.SecretClient
}

// NewDBProvider creates a DBProvider connected to the given registry database.
func NewDBProvider(ctx context.Context, databaseURL string, cfg DBProviderConfig) (*DBProvider, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}

	ttl := cfg.CacheTTL
	if ttl == 0 {
		ttl = 60 * time.Second
	}

	return &DBProvider{
		pool:         pool,
		cacheTTL:     ttl,
		cache:        make(map[string]cachedConfig),
		secretClient: cfg.SecretClient,
	}, nil
}

// Resolve returns the configuration for the given workspace ID.
// Returns ErrWorkspaceNotFound if the ID does not exist.
// Returns ErrWorkspaceInactive if the workspace exists but is inactive.
//
// Resolve is the only path that dereferences a ref-shaped
// `database_url` through SecretClient. List, ListAllWorkspaces, and
// GetWorkspace return the column value wrapped verbatim because
// their callers either don't need the credential (operator
// metadata) or expect to dereference per-workspace themselves
// (batch migrators, schedulers).
func (p *DBProvider) Resolve(ctx context.Context, workspaceID string) (*Config, error) {
	// Reject malformed IDs before any cache lookup or SQL. A
	// traversal-shaped ID won't match a registry row in practice,
	// but validating first keeps the error surface consistent
	// (invalid id → invalid-params error, not a generic not-found).
	if err := ValidateID(workspaceID); err != nil {
		return nil, ErrWorkspaceNotFound
	}

	// Check cache first.
	p.mu.RLock()
	if cached, ok := p.cache[workspaceID]; ok {
		if time.Since(cached.fetchedAt) < p.cacheTTL {
			p.mu.RUnlock()
			cfg := cached.config
			return &cfg, nil
		}
	}
	p.mu.RUnlock()

	cfg, dbURL, err := p.scanRowRaw(ctx, workspaceID)
	if err != nil {
		return nil, err
	}

	if cfg.Status != StatusActive {
		return nil, ErrWorkspaceInactive
	}

	// Active row: dereference a ref through SecretClient. Reject
	// any ref whose workspace segment does not match the row's
	// workspace_id — a misprovisioned or tampered row pointing at
	// another tenant's runtime_db would otherwise route this
	// workspace to a different database (tenant-isolation
	// violation). A missing/denied/down secret on a valid ref
	// surfaces as ErrWorkspaceUnavailable, which the gateway maps
	// to 503.
	if err := validateStoredRefMatchesWorkspace(dbURL, workspaceID); err != nil {
		return nil, err
	}
	dbValue, err := p.resolveStoredDBURL(ctx, dbURL)
	if err != nil {
		return nil, err
	}
	cfg.DatabaseURL = dbValue

	// Update cache.
	p.mu.Lock()
	p.cache[workspaceID] = cachedConfig{config: *cfg, fetchedAt: time.Now()}
	p.mu.Unlock()

	return cfg, nil
}

// scanRowRaw loads a workspace_registry row and returns the
// metadata config plus the raw database_url column. The caller
// decides whether to dereference a ref-shaped value through
// SecretClient — see Resolve. Returns ErrWorkspaceNotFound for a
// missing row.
func (p *DBProvider) scanRowRaw(ctx context.Context, workspaceID string) (*Config, string, error) {
	var cfg Config
	var status, dbURL string
	err := p.pool.QueryRow(ctx,
		`SELECT workspace_id, display_name, database_url, repo_path, actor_scope, status, smp_workspace_id
		 FROM public.workspace_registry
		 WHERE workspace_id = $1`, workspaceID,
	).Scan(&cfg.ID, &cfg.DisplayName, &dbURL, &cfg.RepoPath, &cfg.ActorScope, &status, &cfg.SMPWorkspaceID)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return nil, "", ErrWorkspaceNotFound
		}
		return nil, "", err
	}
	cfg.Status = WorkspaceStatus(status)
	return &cfg, dbURL, nil
}

// validateStoredRefMatchesWorkspace ensures a ref-shaped column
// value names the same workspace as the row that holds it AND
// names the runtime_db purpose. Returns nil for non-ref values (no
// validation needed for literal URLs) and for values that fail to
// parse as refs (the SecretClient call will reject those with
// ErrInvalidRef and the resolver maps that to ErrWorkspaceUnavailable).
// Mismatches — cross-tenant or wrong purpose (a row pointing at the
// workspace's git or projection_db secret as if it were the runtime
// DB) — surface as ErrWorkspaceUnavailable so request handling
// fails closed.
func validateStoredRefMatchesWorkspace(raw, workspaceID string) error {
	if !strings.HasPrefix(raw, "secret-store://") {
		return nil
	}
	refWS, purpose, err := secrets.ParseRef(secrets.SecretRef(raw))
	if err != nil {
		return nil
	}
	if refWS != workspaceID {
		return fmt.Errorf("%w: registry row for %q references %q's runtime_db (cross-tenant)", ErrWorkspaceUnavailable, workspaceID, refWS)
	}
	if purpose != secrets.PurposeRuntimeDB {
		return fmt.Errorf("%w: registry row for %q references purpose %q (expected %q)", ErrWorkspaceUnavailable, workspaceID, purpose, secrets.PurposeRuntimeDB)
	}
	return nil
}

// resolveStoredDBURL converts a column value into a SecretValue. A
// ref-shaped value goes through SecretClient when configured;
// anything else is wrapped verbatim. Empty strings stay zero-valued
// so callers can detect "no DB" via len(cfg.DatabaseURL.Reveal())==0.
func (p *DBProvider) resolveStoredDBURL(ctx context.Context, raw string) (secrets.SecretValue, error) {
	if raw == "" {
		return secrets.SecretValue{}, nil
	}
	if !strings.HasPrefix(raw, "secret-store://") {
		return secrets.NewSecretValue([]byte(raw)), nil
	}
	if p.secretClient == nil {
		return secrets.SecretValue{}, fmt.Errorf("%w: ref-shaped database_url stored without a SecretClient", ErrWorkspaceUnavailable)
	}
	v, _, err := p.secretClient.Get(ctx, secrets.SecretRef(raw))
	switch {
	case err == nil:
		// An empty value from a ref-shaped column indicates a
		// misprovisioned secret. A literal-empty database_url is
		// already short-circuited above and stays the legitimate
		// "no DB" sentinel; here, we know the row pointed at a
		// secret-store ref, so the operator's intent was to have
		// a database. Fail closed.
		if len(v.Reveal()) == 0 {
			return secrets.SecretValue{}, fmt.Errorf("%w: secret %q resolved to empty value", ErrWorkspaceUnavailable, raw)
		}
		return v, nil
	case errors.Is(err, secrets.ErrSecretNotFound):
		return secrets.SecretValue{}, fmt.Errorf("%w: secret %q: %v", ErrWorkspaceUnavailable, raw, err)
	default:
		return secrets.SecretValue{}, fmt.Errorf("%w: secret %q: %v", ErrWorkspaceUnavailable, raw, err)
	}
}

// List returns all active workspace configurations. The runtime
// database credential is **not** dereferenced — DatabaseURL holds
// the column value wrapped verbatim. Per-workspace consumers
// (MultiScheduler, MultiProjectionSync, batch migrators) drive
// dereferencing through Resolve when they actually need the URL,
// so a single workspace with a missing or unavailable secret no
// longer aborts enumeration for everyone.
func (p *DBProvider) List(ctx context.Context) ([]Config, error) {
	// Check list cache.
	p.mu.RLock()
	if p.listCache != nil && time.Since(p.lastListAt) < p.cacheTTL {
		result := make([]Config, len(p.listCache))
		copy(result, p.listCache)
		p.mu.RUnlock()
		return result, nil
	}
	p.mu.RUnlock()

	rows, err := p.pool.Query(ctx,
		`SELECT workspace_id, display_name, database_url, repo_path, actor_scope, status, smp_workspace_id
		 FROM public.workspace_registry
		 WHERE status = 'active'
		 ORDER BY workspace_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var configs []Config
	for rows.Next() {
		var cfg Config
		var status, dbURL string
		if err := rows.Scan(&cfg.ID, &cfg.DisplayName, &dbURL, &cfg.RepoPath, &cfg.ActorScope, &status, &cfg.SMPWorkspaceID); err != nil {
			return nil, err
		}
		cfg.Status = WorkspaceStatus(status)
		if dbURL != "" {
			cfg.DatabaseURL = secrets.NewSecretValue([]byte(dbURL))
		}
		configs = append(configs, cfg)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Update list cache only. Per-workspace caches are populated
	// by Resolve, which performs the SecretClient round-trip and
	// stores the dereferenced credential.
	p.mu.Lock()
	p.listCache = make([]Config, len(configs))
	copy(p.listCache, configs)
	p.lastListAt = time.Now()
	p.mu.Unlock()

	return configs, nil
}

// Invalidate removes a workspace from the resolver cache so the next
// Resolve call reads from the database. Used during deactivation.
func (p *DBProvider) Invalidate(workspaceID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.cache, workspaceID)
	p.listCache = nil // force list refresh
}

// CreateWorkspace inserts a new workspace into the registry. The
// caller decides what to store in the database_url column — a
// literal URL or a `secret-store://...` ref. cfg.DatabaseURL is
// revealed for the INSERT; treat it as sensitive on the call path
// to this method.
func (p *DBProvider) CreateWorkspace(ctx context.Context, cfg Config) error {
	if err := ValidateID(cfg.ID); err != nil {
		return err
	}
	_, err := p.pool.Exec(ctx,
		`INSERT INTO public.workspace_registry (workspace_id, display_name, database_url, repo_path, actor_scope, status, smp_workspace_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		cfg.ID, cfg.DisplayName, string(cfg.DatabaseURL.Reveal()), cfg.RepoPath, cfg.ActorScope, string(cfg.Status), cfg.SMPWorkspaceID)
	return err
}

// DeactivateWorkspace marks a workspace as inactive in the registry.
func (p *DBProvider) DeactivateWorkspace(ctx context.Context, workspaceID string) error {
	if err := ValidateID(workspaceID); err != nil {
		return ErrWorkspaceNotFound
	}
	result, err := p.pool.Exec(ctx,
		`UPDATE public.workspace_registry SET status = 'inactive', updated_at = now()
		 WHERE workspace_id = $1 AND status = 'active'`,
		workspaceID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrWorkspaceNotFound
	}
	return nil
}

// GetWorkspace returns a workspace config regardless of status for
// operator-facing metadata endpoints. The database_url column is
// **not** dereferenced — DatabaseURL holds the column value
// wrapped verbatim — so a missing / unavailable runtime_db secret
// does not turn `GET /api/v1/workspaces/{id}` into a 500.
func (p *DBProvider) GetWorkspace(ctx context.Context, workspaceID string) (*Config, error) {
	if err := ValidateID(workspaceID); err != nil {
		return nil, ErrWorkspaceNotFound
	}
	cfg, dbURL, err := p.scanRowRaw(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	if dbURL != "" {
		cfg.DatabaseURL = secrets.NewSecretValue([]byte(dbURL))
	}
	return cfg, nil
}

// ListAllWorkspaces returns all workspaces (active and inactive)
// for operator-facing listings. The runtime database credential is
// **not** dereferenced here: the only consumer is the management
// list endpoint, which never needs the URL, and a missing /
// inaccessible runtime_db secret on any one row would otherwise
// fail the entire listing. Callers that need the credential
// (resolver / migrator) go through Resolve or List.
func (p *DBProvider) ListAllWorkspaces(ctx context.Context) ([]Config, error) {
	rows, err := p.pool.Query(ctx,
		`SELECT workspace_id, display_name, database_url, repo_path, actor_scope, status, smp_workspace_id
		 FROM public.workspace_registry
		 ORDER BY workspace_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var configs []Config
	for rows.Next() {
		var cfg Config
		var status, dbURL string
		if err := rows.Scan(&cfg.ID, &cfg.DisplayName, &dbURL, &cfg.RepoPath, &cfg.ActorScope, &status, &cfg.SMPWorkspaceID); err != nil {
			return nil, err
		}
		cfg.Status = WorkspaceStatus(status)
		// Wrap the column value as-is — no SecretClient round-trip.
		// Ref-shaped values stay in their stored form so callers can
		// observe presence without needing access to the underlying
		// store; SecretValue's redaction prevents leaks if logged.
		if dbURL != "" {
			cfg.DatabaseURL = secrets.NewSecretValue([]byte(dbURL))
		}
		configs = append(configs, cfg)
	}
	return configs, rows.Err()
}

// DeleteWorkspace removes a workspace from the registry entirely.
// Used for test cleanup only — production should use DeactivateWorkspace.
func (p *DBProvider) DeleteWorkspace(ctx context.Context, workspaceID string) error {
	_, err := p.pool.Exec(ctx,
		`DELETE FROM public.workspace_registry WHERE workspace_id = $1`, workspaceID)
	return err
}

// Close closes the database connection pool.
func (p *DBProvider) Close() {
	p.pool.Close()
}
