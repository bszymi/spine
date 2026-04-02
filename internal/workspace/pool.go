package workspace

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/bszymi/spine/internal/artifact"
	"github.com/bszymi/spine/internal/config"
	"github.com/bszymi/spine/internal/event"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/projection"
	"github.com/bszymi/spine/internal/queue"
	"github.com/bszymi/spine/internal/store"
)

// ServiceSet holds all per-workspace service instances.
// Each workspace gets its own set, lazily created and cached by the pool.
type ServiceSet struct {
	Config     Config
	Store      store.Store
	GitClient  *git.CLIClient
	Artifacts  *artifact.Service
	ProjQuery  *projection.QueryService
	ProjSync   *projection.Service
	Queue      *queue.MemoryQueue
	Events     *event.QueueRouter

	// close is called when the service set is evicted or the pool shuts down.
	close func()
}

type poolEntry struct {
	services   *ServiceSet
	lastAccess time.Time
}

// ServicePool lazily creates and caches per-workspace service sets.
// Per components.md §6.5.
type ServicePool struct {
	resolver    Resolver
	mu          sync.Mutex
	entries     map[string]*poolEntry
	idleTimeout time.Duration
	closed      bool
}

// PoolConfig holds configuration for the service pool.
type PoolConfig struct {
	// IdleTimeout is how long an unused service set is kept before eviction.
	// Default: 15 minutes.
	IdleTimeout time.Duration
}

// NewServicePool creates a service pool backed by the given resolver.
func NewServicePool(resolver Resolver, cfg PoolConfig) *ServicePool {
	timeout := cfg.IdleTimeout
	if timeout == 0 {
		timeout = 15 * time.Minute
	}
	return &ServicePool{
		resolver:    resolver,
		entries:     make(map[string]*poolEntry),
		idleTimeout: timeout,
	}
}

// Get returns the service set for the given workspace ID. If no set exists,
// one is lazily created from the workspace config. Thread-safe — concurrent
// first requests for the same workspace only initialize once.
func (p *ServicePool) Get(ctx context.Context, workspaceID string) (*ServiceSet, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, fmt.Errorf("service pool is closed")
	}

	if entry, ok := p.entries[workspaceID]; ok {
		entry.lastAccess = time.Now()
		p.mu.Unlock()
		return entry.services, nil
	}

	// Hold the lock during initialization to prevent double-init.
	// This is acceptable because workspace init is rare (first request only).
	cfg, err := p.resolver.Resolve(ctx, workspaceID)
	if err != nil {
		p.mu.Unlock()
		return nil, err
	}

	ss, err := buildServiceSet(ctx, *cfg)
	if err != nil {
		p.mu.Unlock()
		return nil, fmt.Errorf("init workspace %q services: %w", workspaceID, err)
	}

	p.entries[workspaceID] = &poolEntry{
		services:   ss,
		lastAccess: time.Now(),
	}
	p.mu.Unlock()

	log := observe.Logger(ctx)
	log.Info("workspace service set initialized", "workspace_id", workspaceID)

	return ss, nil
}

// ActiveCount returns the number of currently cached workspace service sets.
func (p *ServicePool) ActiveCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.entries)
}

// EvictIdle removes service sets that have not been accessed within the idle timeout.
// Call this periodically (e.g., from a background ticker).
func (p *ServicePool) EvictIdle() {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	for id, entry := range p.entries {
		if now.Sub(entry.lastAccess) > p.idleTimeout {
			entry.services.close()
			delete(p.entries, id)
		}
	}
}

// Close shuts down all cached service sets and marks the pool as closed.
func (p *ServicePool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for id, entry := range p.entries {
		entry.services.close()
		delete(p.entries, id)
	}
	p.closed = true
}

// buildServiceSet creates a complete service set from a workspace config.
func buildServiceSet(ctx context.Context, cfg Config) (*ServiceSet, error) {
	var closers []func()

	// Database.
	var st store.Store
	var pgStore *store.PostgresStore
	if cfg.DatabaseURL != "" {
		var err error
		pgStore, err = store.NewPostgresStore(ctx, cfg.DatabaseURL)
		if err != nil {
			return nil, fmt.Errorf("connect to workspace database: %w", err)
		}
		st = pgStore
		closers = append(closers, pgStore.Close)
	}

	// Git client.
	repoPath := cfg.RepoPath
	if repoPath == "" {
		repoPath = "."
	}
	gitClient := git.NewCLIClient(repoPath)

	// Load spine config from repo.
	spineCfg, err := config.Load(repoPath)
	if err != nil {
		spineCfg = &config.SpineConfig{ArtifactsDir: "/"}
	}

	// Queue and event router.
	q := queue.NewMemoryQueue(100)
	go q.Start(ctx)
	closers = append(closers, func() { q.Stop() })
	eventRouter := event.NewQueueRouter(q)

	// Artifact service.
	artifactSvc := artifact.NewService(gitClient, eventRouter, repoPath)
	artifactSvc.WithArtifactsDir(spineCfg.ArtifactsDir)

	// Projection services.
	var projQuery *projection.QueryService
	var projSync *projection.Service
	if st != nil {
		projQuery = projection.NewQueryService(st, gitClient)
		projSync = projection.NewService(gitClient, st, eventRouter, 30*time.Second)
	}

	closeAll := func() {
		for i := len(closers) - 1; i >= 0; i-- {
			closers[i]()
		}
	}

	return &ServiceSet{
		Config:    cfg,
		Store:     st,
		GitClient: gitClient,
		Artifacts: artifactSvc,
		ProjQuery: projQuery,
		ProjSync:  projSync,
		Queue:     q,
		Events:    eventRouter,
		close:     closeAll,
	}, nil
}
