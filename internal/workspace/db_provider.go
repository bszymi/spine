package workspace

import (
	"context"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DBProvider implements Resolver by reading workspace configuration from a
// PostgreSQL workspace registry table. This is the provider used in shared
// runtime mode. Resolved configs are cached in memory with a configurable TTL.
type DBProvider struct {
	pool       *pgxpool.Pool
	cacheTTL   time.Duration
	mu         sync.RWMutex
	cache      map[string]cachedConfig
	lastListAt time.Time
	listCache  []Config
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
		pool:     pool,
		cacheTTL: ttl,
		cache:    make(map[string]cachedConfig),
	}, nil
}

// Resolve returns the configuration for the given workspace ID.
// Returns ErrWorkspaceNotFound if the ID does not exist.
// Returns ErrWorkspaceInactive if the workspace exists but is inactive.
func (p *DBProvider) Resolve(ctx context.Context, workspaceID string) (*Config, error) {
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

	// Cache miss or expired — query database.
	var cfg Config
	var status string
	err := p.pool.QueryRow(ctx,
		`SELECT workspace_id, display_name, database_url, repo_path, actor_scope, status
		 FROM public.workspace_registry
		 WHERE workspace_id = $1`, workspaceID,
	).Scan(&cfg.ID, &cfg.DisplayName, &cfg.DatabaseURL, &cfg.RepoPath, &cfg.ActorScope, &status)

	if err != nil {
		if err.Error() == "no rows in result set" {
			return nil, ErrWorkspaceNotFound
		}
		return nil, err
	}

	cfg.Status = WorkspaceStatus(status)

	if cfg.Status != StatusActive {
		return nil, ErrWorkspaceInactive
	}

	// Update cache.
	p.mu.Lock()
	p.cache[workspaceID] = cachedConfig{config: cfg, fetchedAt: time.Now()}
	p.mu.Unlock()

	return &cfg, nil
}

// List returns all active workspace configurations.
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
		`SELECT workspace_id, display_name, database_url, repo_path, actor_scope, status
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
		var status string
		if err := rows.Scan(&cfg.ID, &cfg.DisplayName, &cfg.DatabaseURL, &cfg.RepoPath, &cfg.ActorScope, &status); err != nil {
			return nil, err
		}
		cfg.Status = WorkspaceStatus(status)
		configs = append(configs, cfg)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Update list cache and individual caches.
	p.mu.Lock()
	p.listCache = make([]Config, len(configs))
	copy(p.listCache, configs)
	p.lastListAt = time.Now()
	now := time.Now()
	for _, cfg := range configs {
		p.cache[cfg.ID] = cachedConfig{config: cfg, fetchedAt: now}
	}
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

// CreateWorkspace inserts a new workspace into the registry.
func (p *DBProvider) CreateWorkspace(ctx context.Context, cfg Config) error {
	_, err := p.pool.Exec(ctx,
		`INSERT INTO public.workspace_registry (workspace_id, display_name, database_url, repo_path, actor_scope, status)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		cfg.ID, cfg.DisplayName, cfg.DatabaseURL, cfg.RepoPath, cfg.ActorScope, string(cfg.Status))
	return err
}

// DeactivateWorkspace marks a workspace as inactive in the registry.
func (p *DBProvider) DeactivateWorkspace(ctx context.Context, workspaceID string) error {
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

// GetWorkspace returns a workspace config regardless of status.
func (p *DBProvider) GetWorkspace(ctx context.Context, workspaceID string) (*Config, error) {
	var cfg Config
	var status string
	err := p.pool.QueryRow(ctx,
		`SELECT workspace_id, display_name, database_url, repo_path, actor_scope, status
		 FROM public.workspace_registry
		 WHERE workspace_id = $1`, workspaceID,
	).Scan(&cfg.ID, &cfg.DisplayName, &cfg.DatabaseURL, &cfg.RepoPath, &cfg.ActorScope, &status)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return nil, ErrWorkspaceNotFound
		}
		return nil, err
	}
	cfg.Status = WorkspaceStatus(status)
	return &cfg, nil
}

// ListAllWorkspaces returns all workspaces (active and inactive).
func (p *DBProvider) ListAllWorkspaces(ctx context.Context) ([]Config, error) {
	rows, err := p.pool.Query(ctx,
		`SELECT workspace_id, display_name, database_url, repo_path, actor_scope, status
		 FROM public.workspace_registry
		 ORDER BY workspace_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var configs []Config
	for rows.Next() {
		var cfg Config
		var status string
		if err := rows.Scan(&cfg.ID, &cfg.DisplayName, &cfg.DatabaseURL, &cfg.RepoPath, &cfg.ActorScope, &status); err != nil {
			return nil, err
		}
		cfg.Status = WorkspaceStatus(status)
		configs = append(configs, cfg)
	}
	return configs, rows.Err()
}

// Close closes the database connection pool.
func (p *DBProvider) Close() {
	p.pool.Close()
}
