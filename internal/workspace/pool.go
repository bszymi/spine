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
	"github.com/bszymi/spine/internal/gitpool"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/projection"
	"github.com/bszymi/spine/internal/queue"
	"github.com/bszymi/spine/internal/repository"
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

	// Registry is the per-workspace repository.Registry — the single
	// authoritative resolver of catalog identity + binding row for
	// run-start preconditions, the git pool, and any code path that
	// needs to know which repos are active. INIT-014 EPIC-003.
	Registry *repository.Registry

	// GitPool routes Git client lookups by repository ID. Production
	// callers in this workspace pull primary clients via
	// GitPool.PrimaryClient() (semantically identical to GitClient
	// today, kept distinct so the pool can later mediate per-repo
	// auth and lazy clone). Always non-nil after buildServiceSet.
	GitPool *gitpool.Pool

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

	// close is called when the service set is evicted or the pool
	// shuts down. The reason is recorded on the per-workspace pool
	// close-reason metric (ADR-012). Callers pass one of:
	// "shutdown", "idle", "invalidate", "init-error".
	close func(reason string)
}

type poolEntry struct {
	services    *ServiceSet
	lastAccess  time.Time
	refCount    int32  // number of active users of this service set
	evicting    bool   // marked for deferred close on last Release
	evictReason string // close-reason when the deferred close fires

	// ready signals completion of initialization. Closed exactly once
	// (tracked by readyClosed) when services is populated on success or
	// initErr is set on failure. Waiters read the channel without the
	// lock; all state transitions happen under p.mu.
	ready       chan struct{}
	readyClosed bool
	initErr     error

	// gone signals removal of this entry from p.entries. Closed exactly
	// once by removeLocked. A concurrent Get observing an evicting entry
	// waits on this channel so it can start a fresh initialization only
	// after the old entry is fully released (preventing the old
	// initiator's Release from mutating a replacement entry under the
	// same workspace ID).
	gone chan struct{}
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
	dbPolicy     PoolPolicy
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
	// Default: 10 minutes (ADR-012).
	IdleTimeout time.Duration

	// IdleCheckInterval is how often the background eviction loop
	// scans for idle workspaces. Default: IdleTimeout / 4, clamped
	// to [30s, 5min]. Set to a negative value to disable the loop
	// entirely (tests that drive EvictIdle by hand).
	IdleCheckInterval time.Duration

	// Builder is an optional hook called after basic service construction.
	// Use it to inject orchestrator-dependent services that would create
	// import cycles if constructed directly in buildServiceSet.
	Builder ServiceSetBuilder

	// SecretCipher, if set, is installed on each per-workspace
	// PostgresStore so at-rest secrets (e.g. webhook signing secrets)
	// are encrypted with the same key used in single-workspace mode.
	SecretCipher *spinecrypto.SecretCipher

	// DBPolicy is the per-workspace connection-pool policy from
	// ADR-012. Zero-valued fields fall back to PoolPolicyDefault().
	DBPolicy PoolPolicy
}

// NewServicePool creates a service pool backed by the given resolver.
// The provided context is used as the parent for pool-lifetime goroutines
// (e.g., queue workers, idle-eviction ticker). It should not be a
// request context.
func NewServicePool(ctx context.Context, resolver Resolver, cfg PoolConfig) *ServicePool {
	timeout := cfg.IdleTimeout
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	poolCtx, cancel := context.WithCancel(ctx)
	p := &ServicePool{
		resolver:     resolver,
		entries:      make(map[string]*poolEntry),
		idleTimeout:  timeout,
		builder:      cfg.Builder,
		secretCipher: cfg.SecretCipher,
		dbPolicy:     cfg.DBPolicy,
		ctx:          poolCtx,
		cancel:       cancel,
	}
	if interval := resolveIdleCheckInterval(cfg.IdleCheckInterval, timeout); interval > 0 {
		go p.runIdleEvictor(interval)
	}
	return p
}

// resolveIdleCheckInterval picks the eviction tick rate. A negative
// configured value disables the background loop (callers drive
// EvictIdle by hand — used by unit tests). Zero means "use a sane
// default derived from IdleTimeout". Otherwise the configured value
// is used verbatim.
func resolveIdleCheckInterval(configured, idleTimeout time.Duration) time.Duration {
	if configured < 0 {
		return 0
	}
	if configured > 0 {
		return configured
	}
	interval := idleTimeout / 4
	if interval < 30*time.Second {
		interval = 30 * time.Second
	}
	if interval > 5*time.Minute {
		interval = 5 * time.Minute
	}
	return interval
}

// runIdleEvictor scans for idle workspaces every interval and closes
// any whose lastAccess is older than idleTimeout with no active
// references. Exits when the pool's lifetime context is cancelled
// (Close).
func (p *ServicePool) runIdleEvictor(interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-t.C:
			p.EvictIdle()
		}
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
		if !ok {
			// No entry — start a new initialization ourselves. Register
			// a placeholder so concurrent Get calls for the same
			// workspace join this in-flight init instead of launching a
			// duplicate.
			newEntry := &poolEntry{
				ready:      make(chan struct{}),
				gone:       make(chan struct{}),
				refCount:   1,
				lastAccess: time.Now(),
			}
			p.entries[canonicalID] = newEntry
			p.mu.Unlock()
			return p.initializeEntry(ctx, canonicalID, *cfg, newEntry)
		}

		if entry.evicting {
			// Entry is being phased out. Wait until it is fully removed
			// from the map before starting a fresh initialization —
			// otherwise the old initiator's Release(workspaceID) would
			// decrement the wrong entry's refcount.
			gone := entry.gone
			p.mu.Unlock()
			select {
			case <-gone:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			p.mu.Lock()
			if p.closed {
				p.mu.Unlock()
				return nil, fmt.Errorf("service pool is closed")
			}
			continue
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

// publishReadyLocked closes entry.ready exactly once. Callers must hold
// p.mu. Idempotent so the normal initializer handoff and Close's
// wake-up-waiters path can both call it safely.
func publishReadyLocked(entry *poolEntry) {
	if entry == nil || entry.ready == nil {
		return
	}
	if !entry.readyClosed {
		close(entry.ready)
		entry.readyClosed = true
	}
}

// removeLocked removes entry from p.entries (if it is still the current
// entry under id) and signals any Get waiters blocked on its gone channel.
// Callers must hold p.mu.
func (p *ServicePool) removeLocked(id string, entry *poolEntry) {
	if cur, ok := p.entries[id]; ok && cur == entry {
		delete(p.entries, id)
	}
	if entry.gone != nil {
		close(entry.gone)
		entry.gone = nil
	}
}

// initializeEntry runs buildServiceSet outside the pool mutex for the
// supplied entry and publishes the outcome. It always closes entry.ready.
// On success, entry.services is set; on failure, entry.initErr is set and
// the entry is removed from the cache. The caller must have already
// inserted the entry into p.entries with refCount=1 before releasing the
// mutex.
func (p *ServicePool) initializeEntry(ctx context.Context, canonicalID string, cfg Config, entry *poolEntry) (*ServiceSet, error) {
	ss, buildErr := buildServiceSet(p.ctx, cfg, p.builder, p.secretCipher, p.dbPolicy)

	p.mu.Lock()
	defer p.mu.Unlock()

	// If the pool was closed while we were initializing, drop whatever
	// we built so we don't leak goroutines or DB connections.
	if p.closed {
		if ss != nil {
			ss.close("shutdown")
		}
		closedErr := fmt.Errorf("service pool is closed")
		// Close may already have set initErr and signaled waiters; the
		// helpers below are idempotent either way.
		if entry.initErr == nil {
			entry.initErr = closedErr
		}
		p.removeLocked(canonicalID, entry)
		publishReadyLocked(entry)
		return nil, entry.initErr
	}

	if buildErr != nil {
		wrapped := fmt.Errorf("init workspace %q services: %w", canonicalID, buildErr)
		entry.initErr = wrapped
		p.removeLocked(canonicalID, entry)
		publishReadyLocked(entry)
		return nil, wrapped
	}

	// Success — publish the service set. If the entry was marked for
	// eviction while init was in flight, leave evicting=true so the
	// initiator's eventual Release triggers the deferred close path.
	entry.services = ss
	entry.lastAccess = time.Now()
	publishReadyLocked(entry)

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
			if entry.services != nil {
				reason := entry.evictReason
				if reason == "" {
					reason = "invalidate"
				}
				entry.services.close(reason)
			}
			p.removeLocked(workspaceID, entry)
		}
	}
}

// Evict removes a specific workspace's service set from the pool.
// If the set has active references, it is marked for deferred closure —
// Release will close it when the last reference is dropped. If no
// active references, it is closed and removed immediately. The
// close-reason is recorded as "invalidate" so the per-workspace
// pool close-reason metric distinguishes platform-driven drops
// from idle eviction and shutdown.
func (p *ServicePool) Evict(workspaceID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if entry, ok := p.entries[workspaceID]; ok {
		if entry.refCount > 0 {
			// Mark for deferred close — Release will handle cleanup.
			entry.evicting = true
			entry.evictReason = "invalidate"
		} else {
			if entry.services != nil {
				entry.services.close("invalidate")
			}
			p.removeLocked(workspaceID, entry)
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
			if entry.services != nil {
				entry.services.close("idle")
			}
			p.removeLocked(id, entry)
		}
	}
}

// Close shuts down all cached service sets, cancels the pool-lifetime context,
// and marks the pool as closed. Any in-flight initialization's handler
// observes p.closed when it re-acquires the lock and closes the service
// set it built. Any Get waiting on an in-flight entry's ready channel is
// woken immediately with a closed-pool error.
func (p *ServicePool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.cancel()
	closedErr := fmt.Errorf("service pool is closed")
	for id, entry := range p.entries {
		if entry.services != nil {
			entry.services.close("shutdown")
		} else {
			// In-flight entry: wake any Get waiters with a closed-pool
			// error so they don't block until ctx is cancelled. The
			// initialize handler's later re-lock will observe p.closed
			// and clean up the service set it built; publishReadyLocked
			// and removeLocked are both idempotent so that double-touch
			// is safe.
			if entry.initErr == nil {
				entry.initErr = closedErr
			}
			publishReadyLocked(entry)
		}
		p.removeLocked(id, entry)
	}
	p.closed = true
}

// buildServiceSet creates a complete service set from a workspace config.
func buildServiceSet(ctx context.Context, cfg Config, builder ServiceSetBuilder, cipher *spinecrypto.SecretCipher, dbPolicy PoolPolicy) (*ServiceSet, error) {
	// Each closer accepts the reason the service set is being torn
	// down so the workspace pool can record the per-reason
	// close-counter (ADR-012). Closers that don't care about the
	// reason simply ignore it.
	var closers []func(reason string)

	// Database. Reveal the workspace credential only at this
	// boundary; the pgxpool driver is the legitimate consumer of the
	// URL string (ADR-010).
	var st store.Store
	var pgStore *store.PostgresStore
	if dbURL := string(cfg.DatabaseURL.Reveal()); dbURL != "" {
		// Build a per-workspace pgxpool with ADR-012 policy and wrap
		// it for saturation observability. The PostgresStore reads
		// through the underlying pgxpool; the WorkspaceDBPool owns
		// teardown and metric registration.
		wp, err := NewWorkspaceDBPool(ctx, cfg.ID, dbURL, dbPolicy)
		if err != nil {
			return nil, fmt.Errorf("connect to workspace database: %w", err)
		}
		pgStore = store.NewPostgresStoreWithQuerier(wp)
		pgStore.SetSecretCipher(cipher)
		st = pgStore
		closers = append(closers, func(reason string) { wp.Close(reason) })
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

	// Repository registry — primary-only catalog (Git-backed loader
	// lands later in INIT-014). Primary lookup always succeeds; code
	// repos resolve through the binding store when one is configured.
	repoSpec := repository.PrimarySpec{LocalPath: repoPath}
	registry := repository.New(
		cfg.ID,
		repoSpec,
		func(_ context.Context) (*repository.Catalog, error) {
			return repository.ParseCatalog(nil, repoSpec)
		},
		st,
	)

	// Git client pool. PrimaryClient() returns gitClient unchanged —
	// services keep using the primary repo without any behavior
	// change. EPIC-003 TASK-006 will replace the bare CLI factory
	// with credential-aware per-binding resolution; until then, code
	// repo clients reuse the primary's auth profile via gitOpts.
	gitPool, err := gitpool.New(gitClient, registry, gitpool.NewCLIClientFactory(gitOpts...))
	if err != nil {
		return nil, fmt.Errorf("git client pool: %w", err)
	}
	primaryClient := gitPool.PrimaryClient()

	// Load spine config from repo.
	spineCfg, err := config.Load(repoPath)
	if err != nil {
		spineCfg = &config.SpineConfig{ArtifactsDir: "/"}
	}

	// Queue and event router.
	q := queue.NewMemoryQueue(100)
	go q.Start(ctx)
	closers = append(closers, func(string) { q.Stop() })
	eventRouter := event.NewQueueRouter(q)

	// Artifact service. The branch-protection policy is wired from the
	// projection-backed RuleSource when a Store is available; otherwise a
	// permissive policy keeps the very early bootstrap window functional
	// without silently disabling protection in production (the same
	// pattern cmd/spine uses — production workspaces always have a Store).
	artifactSvc := artifact.NewService(primaryClient, eventRouter, repoPath)
	artifactSvc.WithArtifactsDir(spineCfg.ArtifactsDir)
	if st != nil {
		artifactSvc.WithPolicy(branchprotect.New(bpprojection.New(st)))
	} else {
		artifactSvc.WithPolicy(branchprotect.NewPermissive())
	}

	// Workflow service (ADR-007): dedicated surface for workflow definition
	// writes, kept separate from the generic artifact service so the
	// workflow validation suite owns the write path.
	workflowSvc := workflow.NewService(primaryClient, repoPath)

	// Projection services.
	var projQuery *projection.QueryService
	var projSync *projection.Service
	if st != nil {
		projQuery = projection.NewQueryService(st, primaryClient)
		projSync = projection.NewService(primaryClient, st, eventRouter, 30*time.Second)
		projSync.WithArtifactsDir(spineCfg.ArtifactsDir)
	}

	// Validation engine.
	var validator *validation.Engine
	if st != nil {
		// Today no production code reads /.spine/repositories.yaml from
		// Git, so every workspace behaves as single-repo. Wiring the
		// primary-only catalog snapshot here matches that real state:
		// RE-001 accepts `repositories: [spine]` and rejects any other
		// ID. When the Git-backed loader lands (later INIT-014 task),
		// this single line is replaced with that loader and RE-001
		// upgrades to full multi-repo enforcement automatically.
		validator = validation.NewEngine(st,
			validation.WithCatalogSnapshot(validation.PrimaryOnlyCatalogSnapshot(repository.PrimarySpec{})))
	}

	// Divergence service (implements BranchCreator).
	var divSvc *divergence.Service
	if st != nil {
		divSvc = divergence.NewService(st, primaryClient, eventRouter)
		// Same projection-backed policy wired into the Artifact Service
		// and Orchestrator above. spine/* divergence branches never
		// match user rules, so the check is audit-consistency only,
		// but wiring it keeps the guard symmetric (ADR-009 §3).
		divSvc.WithBranchProtectPolicy(branchprotect.New(bpprojection.New(st)))
	}

	closeAll := func(reason string) {
		for i := len(closers) - 1; i >= 0; i-- {
			closers[i](reason)
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
		Registry:   registry,
		GitPool:    gitPool,
		Validator:  validator,
		Divergence: divSvc,
		close:      closeAll,
	}

	// Run optional builder hook for engine-dependent services.
	if builder != nil {
		if err := builder(ctx, ss); err != nil {
			closeAll("init-error")
			return nil, fmt.Errorf("service set builder: %w", err)
		}
	}

	return ss, nil
}
