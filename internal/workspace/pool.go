package workspace

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/bszymi/spine/internal/artifact"
	"github.com/bszymi/spine/internal/auth"
	"github.com/bszymi/spine/internal/config"
	spinecrypto "github.com/bszymi/spine/internal/crypto"
	"github.com/bszymi/spine/internal/divergence"
	"github.com/bszymi/spine/internal/event"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/projection"
	"github.com/bszymi/spine/internal/queue"
	"github.com/bszymi/spine/internal/store"
	"github.com/bszymi/spine/internal/validation"
	"github.com/bszymi/spine/internal/workflow"
)

// ServiceSet holds all per-workspace service instances.
// Each workspace gets its own set, lazily created and cached by the pool.
type ServiceSet struct {
	Config    Config
	Store     store.Store
	Auth      *auth.Service
	GitClient *git.CLIClient
	Artifacts *artifact.Service
	Workflows any // workspace-scoped workflow service; typed as any to avoid an import cycle (see gateway.WorkflowService)
	ProjQuery *projection.QueryService
	ProjSync  *projection.Service
	Queue     *queue.MemoryQueue
	Events    *event.QueueRouter

	// Workspace-scoped services constructed in buildServiceSet.
	Validator  *validation.Engine
	Divergence *divergence.Service

	// Engine-dependent callback functions for the multi-workspace scheduler.
	// Set by the PoolConfig.Builder when the engine orchestrator is available.
	CommitRetryFn  func(ctx context.Context, runID string) error
	StepRecoveryFn func(ctx context.Context, executionID string) error
	RunFailFn      func(ctx context.Context, runID, reason string) error

	// RunStarter and PlanningRunStarter hold workspace-scoped run adapters.
	// Typed as any to avoid a workspace → engine → scheduler → workspace
	// import cycle. Consumers type-assert to the expected interface
	// (e.g. gateway.RunStarter, gateway.PlanningRunStarter).
	RunStarter         any
	PlanningRunStarter any
	WFPlanningStarter  any
	RunCanceller       any

	// close is called when the service set is evicted or the pool shuts down.
	close func()
}

type poolEntry struct {
	services   *ServiceSet
	lastAccess time.Time
	refCount   int32 // number of active users of this service set
	evicting   bool  // marked for deferred close on last Release
}

// ServicePool lazily creates and caches per-workspace service sets.
// Per components.md §6.5.
type ServicePool struct {
	resolver     Resolver
	mu           sync.Mutex
	entries      map[string]*poolEntry
	idleTimeout  time.Duration
	builder      ServiceSetBuilder
	secretCipher *spinecrypto.SecretCipher
	closed       bool
	ctx          context.Context    // pool-lifetime context for background goroutines
	cancel       context.CancelFunc // cancels pool-lifetime context on Close
}

// ServiceSetBuilder is an optional post-construction hook that extends a
// ServiceSet with engine-dependent services (orchestrator adapters, scheduler
// callbacks). It is called after basic services are built.
type ServiceSetBuilder func(ctx context.Context, ss *ServiceSet) error

// PoolConfig holds configuration for the service pool.
type PoolConfig struct {
	// IdleTimeout is how long an unused service set is kept before eviction.
	// Default: 15 minutes.
	IdleTimeout time.Duration

	// Builder is an optional hook called after basic service construction.
	// Use it to inject orchestrator-dependent services that would create
	// import cycles if constructed directly in buildServiceSet.
	Builder ServiceSetBuilder

	// SecretCipher, if set, is installed on each per-workspace
	// PostgresStore so at-rest secrets (e.g. webhook signing secrets)
	// are encrypted with the same key used in single-workspace mode.
	SecretCipher *spinecrypto.SecretCipher
}

// NewServicePool creates a service pool backed by the given resolver.
// The provided context is used as the parent for pool-lifetime goroutines
// (e.g., queue workers). It should not be a request context.
func NewServicePool(ctx context.Context, resolver Resolver, cfg PoolConfig) *ServicePool {
	timeout := cfg.IdleTimeout
	if timeout == 0 {
		timeout = 15 * time.Minute
	}
	poolCtx, cancel := context.WithCancel(ctx)
	return &ServicePool{
		resolver:     resolver,
		entries:      make(map[string]*poolEntry),
		idleTimeout:  timeout,
		builder:      cfg.Builder,
		secretCipher: cfg.SecretCipher,
		ctx:          poolCtx,
		cancel:       cancel,
	}
}

// Get returns the service set for the given workspace ID and increments
// its reference count. Call Release when done to allow idle eviction.
// If no set exists, one is lazily created from the workspace config.
// Thread-safe — concurrent first requests for the same workspace only
// initialize once.
func (p *ServicePool) Get(ctx context.Context, workspaceID string) (*ServiceSet, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, fmt.Errorf("service pool is closed")
	}

	// Resolve first to canonicalize the workspace ID.
	cfg, err := p.resolver.Resolve(ctx, workspaceID)
	if err != nil {
		p.mu.Unlock()
		return nil, err
	}

	canonicalID := cfg.ID

	if entry, ok := p.entries[canonicalID]; ok && !entry.evicting {
		entry.lastAccess = time.Now()
		entry.refCount++
		p.mu.Unlock()
		return entry.services, nil
	}

	// Hold the lock during initialization to prevent double-init.
	// This is acceptable because workspace init is rare (first request only).
	ss, err := buildServiceSet(p.ctx, *cfg, p.builder, p.secretCipher)
	if err != nil {
		p.mu.Unlock()
		return nil, fmt.Errorf("init workspace %q services: %w", canonicalID, err)
	}

	p.entries[canonicalID] = &poolEntry{
		services:   ss,
		lastAccess: time.Now(),
		refCount:   1,
	}
	p.mu.Unlock()

	log := observe.Logger(ctx)
	log.Info("workspace service set initialized", "workspace_id", canonicalID)

	return ss, nil
}

// Release decrements the reference count for a workspace service set.
// Call this when a request or background task is done using the set.
// If the entry was marked for eviction and this was the last reference,
// the service set is closed and removed.
func (p *ServicePool) Release(workspaceID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if entry, ok := p.entries[workspaceID]; ok {
		if entry.refCount > 0 {
			entry.refCount--
		}
		entry.lastAccess = time.Now()

		if entry.evicting && entry.refCount == 0 {
			entry.services.close()
			delete(p.entries, workspaceID)
		}
	}
}

// Evict removes a specific workspace's service set from the pool.
// If the set has active references, it is marked for deferred closure —
// Release will close it when the last reference is dropped. If no
// active references, it is closed and removed immediately.
func (p *ServicePool) Evict(workspaceID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if entry, ok := p.entries[workspaceID]; ok {
		if entry.refCount > 0 {
			// Mark for deferred close — Release will handle cleanup.
			entry.evicting = true
		} else {
			entry.services.close()
			delete(p.entries, workspaceID)
		}
	}
}

// ActiveCount returns the number of currently cached workspace service sets.
func (p *ServicePool) ActiveCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.entries)
}

// RefCount returns the current reference count for a workspace's cached
// service set, or 0 if no entry exists. Primarily intended for tests
// that verify Get/Release pairing under adversarial conditions.
func (p *ServicePool) RefCount(workspaceID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if entry, ok := p.entries[workspaceID]; ok {
		return int(entry.refCount)
	}
	return 0
}

// EvictIdle removes service sets that have not been accessed within the idle timeout
// and have no active references. Call this periodically (e.g., from a background ticker).
func (p *ServicePool) EvictIdle() {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	for id, entry := range p.entries {
		if entry.refCount == 0 && now.Sub(entry.lastAccess) > p.idleTimeout {
			entry.services.close()
			delete(p.entries, id)
		}
	}
}

// Close shuts down all cached service sets, cancels the pool-lifetime context,
// and marks the pool as closed.
func (p *ServicePool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.cancel()
	for id, entry := range p.entries {
		entry.services.close()
		delete(p.entries, id)
	}
	p.closed = true
}

// buildServiceSet creates a complete service set from a workspace config.
func buildServiceSet(ctx context.Context, cfg Config, builder ServiceSetBuilder, cipher *spinecrypto.SecretCipher) (*ServiceSet, error) {
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
		pgStore.SetSecretCipher(cipher)
		st = pgStore
		closers = append(closers, pgStore.Close)
	}

	// Git client.
	repoPath := cfg.RepoPath
	if repoPath == "" {
		repoPath = "."
	}
	// Resolve auth from the shared cache so tokens scrubbed at startup
	// (see git.LoadPushAuthFromEnv) still apply to lazily-built
	// per-workspace clients in shared mode.
	gitOpts := git.PushAuthOpts()
	if cfg.SMPWorkspaceID != "" {
		gitOpts = append(gitOpts, git.WithPushEnv("SMP_WORKSPACE_ID="+cfg.SMPWorkspaceID))
	}
	gitClient := git.NewCLIClient(repoPath, gitOpts...)

	// Configure credential helper in repo-local git config if set.
	if err := gitClient.ConfigureCredentialHelper(ctx); err != nil {
		observe.Logger(ctx).Warn("failed to configure credential helper", "error", err)
	}

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

	// Workflow service (ADR-007): dedicated surface for workflow definition
	// writes, kept separate from the generic artifact service so the
	// workflow validation suite owns the write path.
	workflowSvc := workflow.NewService(gitClient, repoPath)

	// Projection services.
	var projQuery *projection.QueryService
	var projSync *projection.Service
	if st != nil {
		projQuery = projection.NewQueryService(st, gitClient)
		projSync = projection.NewService(gitClient, st, eventRouter, 30*time.Second)
		projSync.WithArtifactsDir(spineCfg.ArtifactsDir)
	}

	// Validation engine.
	var validator *validation.Engine
	if st != nil {
		validator = validation.NewEngine(st)
	}

	// Divergence service (implements BranchCreator).
	var divSvc *divergence.Service
	if st != nil {
		divSvc = divergence.NewService(st, gitClient, eventRouter)
	}

	closeAll := func() {
		for i := len(closers) - 1; i >= 0; i-- {
			closers[i]()
		}
	}

	// Auth service.
	var authSvc *auth.Service
	if st != nil {
		authSvc = auth.NewService(st)
	}

	ss := &ServiceSet{
		Config:     cfg,
		Store:      st,
		Auth:       authSvc,
		GitClient:  gitClient,
		Artifacts:  artifactSvc,
		Workflows:  workflowSvc,
		ProjQuery:  projQuery,
		ProjSync:   projSync,
		Queue:      q,
		Events:     eventRouter,
		Validator:  validator,
		Divergence: divSvc,
		close:      closeAll,
	}

	// Run optional builder hook for engine-dependent services.
	if builder != nil {
		if err := builder(ctx, ss); err != nil {
			closeAll()
			return nil, fmt.Errorf("service set builder: %w", err)
		}
	}

	return ss, nil
}
