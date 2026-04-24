package workspace

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/bszymi/spine/internal/artifact"
	"github.com/bszymi/spine/internal/auth"
	"github.com/bszymi/spine/internal/branchprotect"
	bpprojection "github.com/bszymi/spine/internal/branchprotect/projection"
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
	StepAssigner       any

	// close is called when the service set is evicted or the pool shuts down.
	close func()
}

type poolEntry struct {
	services   *ServiceSet
	lastAccess time.Time
	refCount   int32 // number of active users of this service set
	evicting   bool  // marked for deferred close on last Release

	// ready is non-nil while initialization is in flight. Closing it
	// signals waiters that services (on success) or initErr (on failure)
	// has been populated. All state transitions are performed under the
	// pool mutex; the channel itself is read without the lock only by
	// waiters blocked on it.
	ready   chan struct{}
	initErr error
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
//
// Thread-safe. The pool mutex is released during the slow buildServiceSet
// step so a long or stuck initialization for one workspace does not block
// Get, Release, Evict, or Close on unrelated workspaces. Concurrent first
// requests for the same workspace share a single initialization (and its
// result or error) via the entry's ready channel; a failed initialization
// is removed from the cache so later calls retry cleanly.
func (p *ServicePool) Get(ctx context.Context, workspaceID string) (*ServiceSet, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, fmt.Errorf("service pool is closed")
	}

	// Resolve first to canonicalize the workspace ID. This is expected to
	// be fast; it stays under the mutex to keep cache lookups consistent
	// across renames/aliases.
	cfg, err := p.resolver.Resolve(ctx, workspaceID)
	if err != nil {
		p.mu.Unlock()
		return nil, err
	}

	canonicalID := cfg.ID

	for {
		entry, ok := p.entries[canonicalID]
		if !ok || entry.evicting {
			// No entry, or the current entry is being phased out — start
			// a new initialization ourselves. Register a placeholder so
			// concurrent Get calls for the same workspace join this
			// in-flight init instead of launching a duplicate.
			newEntry := &poolEntry{
				ready:      make(chan struct{}),
				refCount:   1,
				lastAccess: time.Now(),
			}
			p.entries[canonicalID] = newEntry
			p.mu.Unlock()
			return p.initializeEntry(ctx, canonicalID, *cfg, newEntry)
		}

		if entry.services != nil {
			// Ready and cached — take a ref and return.
			entry.lastAccess = time.Now()
			entry.refCount++
			p.mu.Unlock()
			return entry.services, nil
		}

		// Init is in flight. Drop the lock and wait for the signal,
		// then re-enter to re-read state under the lock.
		ready := entry.ready
		p.mu.Unlock()
		select {
		case <-ready:
		case <-ctx.Done():
			return nil, ctx.Err()
		}

		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, fmt.Errorf("service pool is closed")
		}
		if entry.initErr != nil {
			// Share the initiator's error. The entry has already been
			// removed from the map by the initiator, so a later Get will
			// re-run init from scratch.
			p.mu.Unlock()
			return nil, entry.initErr
		}
		// Otherwise loop: services should now be populated, or a new
		// entry has been inserted by another path; re-check.
	}
}

// initializeEntry runs buildServiceSet outside the pool mutex for the
// supplied entry and publishes the outcome. It always closes entry.ready.
// On success, entry.services is set; on failure, entry.initErr is set and
// the entry is removed from the cache. The caller must have already
// inserted the entry into p.entries with refCount=1 before releasing the
// mutex.
func (p *ServicePool) initializeEntry(ctx context.Context, canonicalID string, cfg Config, entry *poolEntry) (*ServiceSet, error) {
	ss, buildErr := buildServiceSet(p.ctx, cfg, p.builder, p.secretCipher)

	p.mu.Lock()
	defer p.mu.Unlock()

	// If the pool was closed while we were initializing, drop whatever
	// we built so we don't leak goroutines or DB connections.
	if p.closed {
		if ss != nil {
			ss.close()
		}
		closedErr := fmt.Errorf("service pool is closed")
		entry.initErr = closedErr
		if cur, ok := p.entries[canonicalID]; ok && cur == entry {
			delete(p.entries, canonicalID)
		}
		close(entry.ready)
		return nil, closedErr
	}

	if buildErr != nil {
		wrapped := fmt.Errorf("init workspace %q services: %w", canonicalID, buildErr)
		entry.initErr = wrapped
		// Only remove the entry if it is still the in-flight one; a
		// concurrent Evict/Close may have already replaced or removed it.
		if cur, ok := p.entries[canonicalID]; ok && cur == entry {
			delete(p.entries, canonicalID)
		}
		close(entry.ready)
		return nil, wrapped
	}

	// Success — publish the service set. If the entry was marked for
	// eviction while init was in flight, leave evicting=true so the
	// initiator's eventual Release triggers the deferred close path.
	entry.services = ss
	entry.lastAccess = time.Now()
	close(entry.ready)

	observe.Logger(ctx).Info("workspace service set initialized", "workspace_id", canonicalID)
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
// and marks the pool as closed. Any in-flight initialization will see the
// closed flag when it completes, close its own services, and signal its
// waiters.
func (p *ServicePool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.cancel()
	for id, entry := range p.entries {
		// In-flight entries have services==nil; their initialize handler
		// will clean up the service set (if any) once buildServiceSet
		// returns, because it observes p.closed.
		if entry.services != nil {
			entry.services.close()
		}
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

	// Artifact service. The branch-protection policy is wired from the
	// projection-backed RuleSource when a Store is available; otherwise a
	// permissive policy keeps the very early bootstrap window functional
	// without silently disabling protection in production (the same
	// pattern cmd/spine uses — production workspaces always have a Store).
	artifactSvc := artifact.NewService(gitClient, eventRouter, repoPath)
	artifactSvc.WithArtifactsDir(spineCfg.ArtifactsDir)
	if st != nil {
		artifactSvc.WithPolicy(branchprotect.New(bpprojection.New(st)))
	} else {
		artifactSvc.WithPolicy(branchprotect.NewPermissive())
	}

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
		// Same projection-backed policy wired into the Artifact Service
		// and Orchestrator above. spine/* divergence branches never
		// match user rules, so the check is audit-consistency only,
		// but wiring it keeps the guard symmetric (ADR-009 §3).
		divSvc.WithBranchProtectPolicy(branchprotect.New(bpprojection.New(st)))
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
