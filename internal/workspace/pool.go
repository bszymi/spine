package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
	"github.com/bszymi/spine/internal/secrets"
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
	// Workflows is the workspace-scoped workflow service. INIT-022
	// EPIC-001 TASK-010: typed as the concrete *workflow.Service rather
	// than the previous `any`. Consumers in gateway and engine
	// type-assert to their own narrower interfaces (gateway.WorkflowService,
	// engine.WorkflowWriter) — those assertions still work because the
	// concrete type satisfies them, and now misuse fails at compile
	// time instead of at the type-assert call site.
	Workflows *workflow.Service
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
	RunStarter          any
	PlanningRunStarter  any
	WFPlanningStarter   any
	RunCanceller        any
	RunMergeResolver    any
	StepAssigner        any
	ResultHandler       any
	StepAcknowledger    any
	CandidateFinder     any
	StepClaimer         any
	StepReleaser        any
	StepExecutionLister any
	// EvidenceQuerier surfaces per-repository ExecutionEvidence on the
	// run.status response (INIT-014 EPIC-006 TASK-005). Typed as any so
	// this package stays free of the evidence/gateway import; consumers
	// type-assert to gateway.EvidenceQuerier.
	EvidenceQuerier any
	// EventBroadcaster is the per-workspace SSE fan-out point owned by
	// the workspace's DeliverySubscriber. Populated by the cmd/spine
	// pool builder when SPINE_EVENT_DELIVERY is on; resolved by the
	// gateway's /api/v1/events/stream handler via *From(ctx) so the
	// per-workspace stream sees the same events that landed in
	// runtime.event_log. Typed as any to keep this package free of the
	// delivery import.
	EventBroadcaster any

	// Done is closed at the start of close() so long-lived observers
	// (most importantly the SSE event stream) can detect that this
	// ServiceSet is being torn down — idle eviction, binding
	// invalidation, or pool shutdown — and disconnect promptly. After
	// Done fires, the workspace's per-tenant store and event router
	// are racing to shut down; observers must NOT issue further calls
	// against ss.* and instead surrender so the pool can rebuild
	// fresh services for the next pool.Get. Single-workspace test
	// constructions that build a ServiceSet by hand without
	// buildServiceSet leave Done nil, which selects-block forever —
	// the desired non-pooled semantics.
	Done <-chan struct{}

	// close is called when the service set is evicted or the pool
	// shuts down. The reason is recorded on the per-workspace pool
	// close-reason metric (ADR-012). Callers pass one of:
	// "shutdown", "idle", "invalidate", "init-error".
	close func(reason string)
}

// AppendCloser registers a function to run when this ServiceSet is
// closed. It runs BEFORE the existing closers, so callers can stop
// dependents (e.g. cancel a per-workspace event-delivery context) ahead
// of foundational teardown like queue.Stop / store close. Used by the
// post-construction builder hook to attach engine-dependent goroutine
// cancellation; buildServiceSet's intra-construction closers stay in
// the original closeAll chain.
func (ss *ServiceSet) AppendCloser(fn func(reason string)) {
	prev := ss.close
	ss.close = func(reason string) {
		fn(reason)
		if prev != nil {
			prev(reason)
		}
	}
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
	secretClient secrets.SecretClient
	dbPolicy     PoolPolicy
	codeRepoBase string
	closed       bool
	ctx          context.Context    // pool-lifetime context for background goroutines
	cancel       context.CancelFunc // cancels pool-lifetime context on Close

	// activeResolves counts in-flight resolver.Resolve calls per
	// workspaceID. Get increments on entry, decrements on return.
	// Evict consults this to decide whether an invalidation could
	// race a cold Get (count > 0 → race window open; count == 0 →
	// no in-flight resolve, no stale-cache risk, true no-op).
	// Tracking only the active-flight window keeps evictGen bounded
	// — both maps are deleted for a workspaceID as soon as the last
	// resolver drains.
	//
	// evictGen is the per-workspace eviction generation. Each Evict
	// for a workspaceID with activeResolves > 0 bumps this counter.
	// Get snapshots evictGen[workspaceID] before Resolve and
	// rechecks after re-acquiring the mutex; a mismatch means an
	// invalidation arrived during Resolve, the resolved cfg may be
	// stale, and Get retries the whole Resolve+cache-insert
	// sequence (bounded by maxGetAttempts so a hot workspace under
	// continuous invalidation cannot livelock the caller). EvictIdle
	// does NOT bump the gen because idle eviction is cleanup, not
	// an invalidation signal. INIT-022 EPIC-001 TASK-015.
	activeResolves map[string]int
	evictGen       map[string]uint64
}

// maxGetAttempts caps how many times Get will retry resolver.Resolve
// when an Evict for the same workspaceID races the in-flight call.
// Three is generous; a hot workspace seeing >3 invalidations during a
// single Resolve is a genuine outage signal worth surfacing.
const maxGetAttempts = 3

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

	// SecretClient, if set, is wired into each workspace's git client
	// pool as the credential resolver for per-binding `credentials_ref`
	// references (INIT-014 EPIC-003 TASK-006). The same client must
	// satisfy ADR-010 redaction guarantees; the pool reveals bytes only
	// at the boundary with the git CLI's GIT_ASKPASS env. When nil,
	// bindings without credentials_ref keep working through the
	// process-wide SPINE_GIT_PUSH_TOKEN; bindings that declare a ref
	// fail closed with a typed credentials-unavailable error.
	SecretClient secrets.SecretClient

	// DBPolicy is the per-workspace connection-pool policy from
	// ADR-012. Zero-valued fields fall back to PoolPolicyDefault().
	DBPolicy PoolPolicy

	// CodeRepoBase is the absolute filesystem directory that contains
	// all per-workspace code-repo subtrees, sourced from
	// SPINE_CODE_REPO_BASE at startup. The pool narrows this to
	// `<CodeRepoBase>/<workspace_id>` per ServiceSet before passing to
	// gitpool.WithRepoBase, so each workspace's pool only accepts
	// LocalPaths under its own subtree — workspace A cannot clone a
	// binding whose LocalPath points inside workspace B's tree even
	// though both pools share the same configured root. Empty
	// disables the gitpool-side check; production deployments are
	// expected to set it (cmd/spine fails fast when
	// SPINE_ENV=production and this is unset).
	CodeRepoBase string
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
		resolver:       resolver,
		entries:        make(map[string]*poolEntry),
		activeResolves: make(map[string]int),
		evictGen:       make(map[string]uint64),
		idleTimeout:    timeout,
		builder:        cfg.Builder,
		secretCipher:   cfg.SecretCipher,
		secretClient:   cfg.SecretClient,
		dbPolicy:       cfg.DBPolicy,
		codeRepoBase:   cfg.CodeRepoBase,
		ctx:            poolCtx,
		cancel:         cancel,
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
// Thread-safe. The pool mutex is released across both slow steps —
// resolver.Resolve (which performs network I/O in platform-binding
// mode) and buildServiceSet — so a slow upstream or stuck
// initialization for one workspace does not block Get, Release,
// Evict, or Close on unrelated workspaces. Two concurrent Gets for
// the same workspace ID may both call Resolve; they converge on the
// same canonicalID below, where the entry-level singleflight
// (entry.ready) deduplicates the actual initializeEntry work, so the
// redundant Resolve cost is bounded to one extra call per
// simultaneous miss. A failed initialization is removed from the
// cache so later calls retry cleanly.
//
// Invalidation race: when an Evict for workspaceID fires while this
// Get's Resolve is in flight, the in-flight cfg may reflect
// pre-invalidation state. The pool snapshots evictGen[workspaceID]
// before Resolve and rechecks after; a mismatch retries the whole
// Get with a fresh Resolve. Bounded by maxGetAttempts to prevent
// livelock under continuous invalidation. This restores the
// pre-TASK-015 contract where the pool mutex implicitly serialized
// Evict to run after the cache-insert.
func (p *ServicePool) Get(ctx context.Context, workspaceID string) (*ServiceSet, error) {
	for attempt := 0; attempt < maxGetAttempts; attempt++ {
		// Per-attempt: fast-fail on a closed pool, snapshot
		// evictGen, and register an in-flight Resolve so a
		// concurrent Evict knows to bump the gen rather than
		// silently no-op.
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, fmt.Errorf("service pool is closed")
		}
		p.activeResolves[workspaceID]++
		startGen := p.evictGen[workspaceID]
		p.mu.Unlock()

		// Resolve runs without the pool mutex. In platform-binding
		// mode this performs network I/O (see
		// internal/workspace/platform_binding_provider.go); holding
		// the mutex would stall every other workspace's
		// Get/Release/Evict/Close on a slow upstream, defeating the
		// per-workspace isolation the pool promises (INIT-022
		// EPIC-001 TASK-015).
		cfg, resolveErr := p.resolver.Resolve(ctx, workspaceID)

		p.mu.Lock()
		// Drain the in-flight counter and clean up evictGen as soon
		// as the last resolver leaves so the maps stay bounded —
		// otherwise every distinct workspaceID ever resolved would
		// retain an entry for the lifetime of the pool.
		p.activeResolves[workspaceID]--
		raced := p.evictGen[workspaceID] != startGen
		if p.activeResolves[workspaceID] == 0 {
			delete(p.activeResolves, workspaceID)
			delete(p.evictGen, workspaceID)
		}
		if resolveErr != nil {
			p.mu.Unlock()
			return nil, resolveErr
		}
		if p.closed {
			p.mu.Unlock()
			return nil, fmt.Errorf("service pool is closed")
		}
		if raced {
			// An Evict for this workspaceID arrived during our
			// Resolve. The cfg in hand may reflect the
			// pre-invalidation binding (the Provider's cache was
			// invalidated too, but only between our snapshot and
			// the Provider's clear; a Resolve already in the
			// fetch path completes against whatever the platform
			// returned). Retry to get a fresh resolve.
			p.mu.Unlock()
			continue
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
	return nil, fmt.Errorf("workspace %q: invalidation raced cold init for %d consecutive attempts", workspaceID, maxGetAttempts)
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
	ss, buildErr := buildServiceSet(p.ctx, cfg, p.builder, p.secretCipher, p.secretClient, p.dbPolicy, p.codeRepoBase)

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
	// Always bump the eviction generation when a Resolve is in
	// flight for this workspaceID — both for the cold-miss case
	// (no entry yet) and the hot-cache case (entry exists, but a
	// Get's unlocked Resolve is concurrently fetching pre-
	// invalidation cfg). Without bumping on the hot-cache path, an
	// Evict that removes the existing entry would still let the
	// in-flight Get cache a stale service set in its place. When
	// no Resolve is in flight, no stale state could be in the
	// resolver pipeline and the bump is unnecessary — keeping the
	// no-op contract for unknown / cold workspaces.
	if p.activeResolves[workspaceID] > 0 {
		p.evictGen[workspaceID]++
	}
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
// PerWorkspaceCodeRepoBase is the public adapter around
// perWorkspaceCodeRepoBase. cmd/spine uses it to narrow the file-mode
// top-level pool's containment boundary by the runtime workspace ID,
// matching the per-workspace narrowing applied to shared-mode pools
// in buildServiceSet. Exported so the two narrowing call sites share
// one implementation.
func PerWorkspaceCodeRepoBase(base, workspaceID string) (string, error) {
	return perWorkspaceCodeRepoBase(base, workspaceID)
}

// perWorkspaceCodeRepoBase narrows a deployment-wide code-repo
// containment root to a single workspace's subtree by joining the
// configured base with the workspace ID. Empty base = empty result
// (containment disabled). The resulting path is used with
// gitpool.WithRepoBase so per-workspace pools enforce
// `<base>/<workspace_id>` as their boundary, preventing a binding for
// workspace A from cloning into workspace B's tree under the same
// configured root.
//
// To guarantee the directory we hand to gitpool is a real directory
// (not a symlink that gitpool would later EvalSymlinks-resolve to an
// unrelated tree), this helper creates `<base>/<workspaceID>` if it is
// absent (MkdirAll, mode 0700) and then resolves it through
// filepath.EvalSymlinks. The resolved path must match the expected
// location `EvalSymlinks(base)/<workspaceID>` exactly. Strict equality
// (rather than a prefix check) refuses two distinct misconfigurations:
//
//   - `<base>/acme` -> `/etc` (symlink outside the deployment root)
//   - `<base>/acme` -> `<base>/globex` (symlink into a sibling
//     workspace's subtree — still under the deployment root, but
//     would let workspace A serve paths inside workspace B's tree)
//
// The mkdir-then-validate pattern closes the symlink race the codex
// review flagged: if the directory is missing at startup, an
// attacker could otherwise create it as a symlink before the first
// clone/open and gitpool would resolve its `repoBase` to the
// symlink's target. By creating the directory ourselves we own it,
// and the subsequent EvalSymlinks check fails closed if it isn't a
// real subdirectory of the deployment base.
//
// Returns an error so the workspace pool refuses to initialize rather
// than silently widen the boundary.
func perWorkspaceCodeRepoBase(base, workspaceID string) (string, error) {
	if base == "" {
		return "", nil
	}
	// Validate before joining: a traversal-shaped ID like ".." would
	// otherwise let filepath.Join normalize both `narrowed` and the
	// later `expected` value to the parent of base, silently bypassing
	// containment. ValidateID is the same allowlist every workspace-ID
	// entry point uses (see internal/workspace/validate.go).
	if err := ValidateID(workspaceID); err != nil {
		return "", fmt.Errorf("invalid workspace ID for code-repo containment: %w", err)
	}
	narrowed := filepath.Join(base, workspaceID)
	info, statErr := os.Stat(narrowed)
	switch {
	case statErr == nil:
		// Reject a regular file masquerading as the workspace base.
		// EvalSymlinks below would still succeed and the helper would
		// hand gitpool a path that fails ENOTDIR on every clone, so
		// fail fast with a clear startup error instead.
		if !info.IsDir() {
			return "", fmt.Errorf("workspace code-repo subdirectory %q exists but is not a directory (mode %v)",
				narrowed, info.Mode())
		}
	case os.IsNotExist(statErr):
		// os.Mkdir (not MkdirAll) so we fail fast if the deployment
		// base is missing — operators must own the configured base
		// directory; auto-creating the entire chain would silently
		// land a tree at an unintended location.
		if err := os.Mkdir(narrowed, 0o700); err != nil {
			return "", fmt.Errorf("create workspace code-repo subdirectory %q: %w", narrowed, err)
		}
	default:
		return "", fmt.Errorf("stat workspace code-repo subdirectory %q: %w", narrowed, statErr)
	}
	resolvedBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", fmt.Errorf("resolve deployment code-repo base %q: %w", base, err)
	}
	resolvedNarrowed, err := filepath.EvalSymlinks(narrowed)
	if err != nil {
		return "", fmt.Errorf("resolve workspace code-repo subdirectory %q: %w", narrowed, err)
	}
	expected := filepath.Join(resolvedBase, workspaceID)
	if resolvedNarrowed != expected {
		return "", fmt.Errorf("workspace code-repo subdirectory %q resolves to %q but must resolve to %q (its own subtree under deployment base %q resolved as %q); refusing to widen the containment boundary",
			narrowed, resolvedNarrowed, expected, base, resolvedBase)
	}
	return narrowed, nil
}

func buildServiceSet(ctx context.Context, cfg Config, builder ServiceSetBuilder, cipher *spinecrypto.SecretCipher, secretClient secrets.SecretClient, dbPolicy PoolPolicy, codeRepoBase string) (*ServiceSet, error) {
	// Each closer accepts the reason the service set is being torn
	// down so the workspace pool can record the per-reason
	// close-counter (ADR-012). Closers that don't care about the
	// reason simply ignore it.
	var closers []func(reason string)

	// Derive the per-workspace code-repo containment base BEFORE
	// opening any database pool. perWorkspaceCodeRepoBase may
	// fail closed (invalid workspace ID, non-directory, symlink
	// outside the deployment root), and surfacing that failure here
	// avoids leaking a pgx pool that NewWorkspaceDBPool would have
	// otherwise opened in the block below.
	wsCodeRepoBase, err := perWorkspaceCodeRepoBase(codeRepoBase, cfg.ID)
	if err != nil {
		return nil, fmt.Errorf("derive per-workspace code-repo base: %w", err)
	}

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

	// Git client pool. PrimaryClient() returns gitClient unchanged so
	// governance services keep operating on the primary repo without
	// any behavior change.
	//
	// WithCloner uses gitpool.NewCLICloner — a per-call factory that
	// builds a fresh *git.CLIClient per clone, so per-binding
	// credentials can be threaded through GIT_ASKPASS for one
	// invocation without leaking into a long-lived shared client.
	// gitOpts (the workspace's process-level auth profile) flows into
	// every clone as a baseline; SecretCredentialResolver layers a
	// per-binding token on top when the binding sets credentials_ref.
	//
	// The credential resolver is wired from cfg.SecretClient when
	// available — that's the same SecretClient the workspace already
	// uses for runtime/projection DB credentials (ADR-010), so adding
	// a per-binding `credentials_ref` reuses existing infrastructure
	// instead of introducing a parallel secret pipeline. Workspaces
	// without a SecretClient (single-workspace dev mode) fall back to
	// the legacy process-wide SPINE_GIT_PUSH_TOKEN path baked into
	// gitOpts: bindings without credentials_ref still work, and any
	// binding that does declare one fails closed with a typed
	// credentials-unavailable error.
	// SecretCredentialResolver is installed unconditionally so a
	// binding with a non-empty CredentialsRef in a workspace that
	// happens to lack a SecretClient (single-workspace dev mode, DB
	// resolver without secret backend) fails closed with a typed
	// credentials-unavailable error rather than silently cloning
	// unauthenticated. The resolver itself short-circuits to the
	// empty Credential for repos with no CredentialsRef, so the
	// public-repo path stays free of secret-store round-trips even
	// when secretClient is nil.
	// WithRepoBase enforces that every code-repo binding's LocalPath
	// resolves under the workspace's containment subtree before the
	// pool will clone or open it. Empty disables the check; production
	// deployments pin SPINE_CODE_REPO_BASE.
	//
	// In shared multi-workspace mode the configured base is the
	// PARENT of per-workspace trees: each ServiceSet lives under
	// `<base>/<workspace_id>` so workspace A's pool cannot clone a
	// binding whose LocalPath points inside workspace B's subtree.
	// Without this per-workspace narrowing, a single shared base
	// would only prove containment "under the common parent" — A's
	// `/git/{workspace}/{repo}` endpoint could end up serving a path
	// inside B's tree because validateRepoBase would still accept it.
	gitPool, err := gitpool.New(gitClient, registry,
		gitpool.NewCLIClientFactory(gitOpts...),
		gitpool.WithCloner(gitpool.NewCLICloner(gitOpts...)),
		gitpool.WithRepoBase(wsCodeRepoBase),
		gitpool.WithCredentialResolver(&gitpool.SecretCredentialResolver{
			Client: secretClient,
		}),
	)
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
		rs, err := bpprojection.New(st)
		if err != nil {
			return nil, fmt.Errorf("artifact branch-protect policy: %w", err)
		}
		artifactSvc.WithPolicy(branchprotect.New(rs))
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
			validation.WithCatalogSnapshot(validation.PrimaryOnlyCatalogSnapshot(repository.PrimarySpec{})),
			// Validation policy registry wiring lands with TASK-004
			// (EPIC-006); see cmd_serve.go for the matching seam.
			validation.WithGovernedFileResolver(validation.NoopGovernedFileResolver()))
	}

	// Divergence service (implements BranchCreator).
	var divSvc *divergence.Service
	if st != nil {
		divSvc = divergence.NewService(st, primaryClient, eventRouter)
		// Same projection-backed policy wired into the Artifact Service
		// and Orchestrator above. spine/* divergence branches never
		// match user rules, so the check is audit-consistency only,
		// but wiring it keeps the guard symmetric (ADR-009 §3).
		rs, err := bpprojection.New(st)
		if err != nil {
			return nil, fmt.Errorf("divergence branch-protect policy: %w", err)
		}
		divSvc.WithBranchProtectPolicy(branchprotect.New(rs))
	}

	// done fires at the START of teardown so long-lived workspace-bound
	// observers (SSE streams, future watchers) can detect eviction
	// before any closer runs. One-shot: the first close call signals
	// done; defensive sync.Once guards against any future caller
	// double-closing the same ServiceSet.
	done := make(chan struct{})
	var doneOnce sync.Once
	closeAll := func(reason string) {
		doneOnce.Do(func() { close(done) })
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
		Done:       done,
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
