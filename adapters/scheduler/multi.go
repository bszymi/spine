package scheduler

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sort"
	"sync"
	"time"

	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/event"
	"github.com/bszymi/spine/core/observe"
)

// MultiScheduler runs scheduler scans across all active workspaces.
// It uses the workspace service pool to get per-workspace stores and
// event routers. Per components.md §6.5, background services iterate
// over workspaces from List() and process each using its service set.
//
// TASK-004 extraction: the pool and resolver are now satisfied by
// locally-declared interfaces (WorkspacePool, WorkspaceResolver,
// ServiceSet) so this file does not import service/internal/workspace.
// The composition root passes the concrete workspace.ServicePool /
// workspace.Resolver, which already satisfy these shapes.
//
// Bounded (INIT-023 TASK-012, audit P3-27): workspaces are scanned
// concurrently, at most MaxConcurrentWorkspaces at a time, each under a
// soft per-workspace time budget, and every per-workspace Scheduler gets
// the commit retry budget.
//
// `spine serve` runs it in the shared resolver modes (db and
// platform-binding; INIT-024 TASK-008 closes the open half of P3-27), so
// one process scans every tenant. One tenant must not be able to stop the
// others' timeouts, orphan scans and merge retries (INIT-024 TASK-008
// adversarial review), so a tick isolates its workspaces:
//
//   - One scan per workspace at a time. A workspace whose previous scan
//     (of any kind) is still running is skipped by the tick, with a Warn,
//     never scanned twice at once — the serialisation single-workspace
//     mode has.
//   - A tick stops waiting once it has made no progress (no slot came
//     free, none of its scans ended) for TickWait. A scan still running
//     then continues in the background, an Error names its workspace, and
//     later ticks skip that workspace until it ends. Without this a single
//     item that never returns — a code repository's push has no deadline
//     of its own, so a remote that accepts the connection and never
//     answers holds it — blocked the tick, and with it the one loop every
//     tenant's scans run on, for good.
//   - Every workspace scan runs under a hard deadline, ScanDeadline (10
//     minutes by default), far beyond its soft budget: an abandoned scan
//     has its context cancelled then — its git children are stopped with
//     SIGTERM, whose handlers remove git's lock files — and gives back its
//     pool reference, which an evicted workspace's next request waits for.
//   - A panic in one workspace's scan is recovered and logged with its
//     stack (runWorkspace); it ends that workspace's share of the tick,
//     not the process every tenant shares.
//
// What that bounds: a tenant holds at most one scan at a time, and each of
// its scans delays the tick it runs in by at most one TickWait past the
// tick's last progress — a scan that never ends costs that once, since
// later ticks skip its workspace; one that ends just after each TickWait
// costs it on every tick that dispatches it, i.e. every other tick of a
// loop. A scan a tick stopped waiting for is not counted against a later
// tick's MaxConcurrentWorkspaces (each such scan is its own workspace's
// single scan), so tenants that hang cannot use up the other tenants'
// slots.
type MultiScheduler struct {
	pool             WorkspacePool
	resolver         WorkspaceResolver
	timeoutInterval  time.Duration
	orphanInterval   time.Duration
	orphanThreshold  time.Duration
	commitMaxRetries int
	workspaceBudget  time.Duration
	maxConcurrent    int
	tickWait         time.Duration
	scanDeadline     time.Duration
	done             chan struct{}
	stopOnce         sync.Once

	// exited is closed when Start returns (Wait).
	exited   chan struct{}
	exitOnce sync.Once

	// scans counts the running per-workspace scan goroutines, including
	// those a tick stopped waiting for (Wait).
	scans sync.WaitGroup

	// inflight is the set of workspaces with a scan running: a tick skips
	// them (one scan per workspace at a time).
	inflightMu sync.Mutex
	inflight   map[string]struct{}

	// resumes holds, per workspace, where each budgeted scan stopped.
	resumeMu sync.Mutex
	resumes  map[string]*scanResume
}

// MultiSchedulerConfig holds configuration for the multi-workspace scheduler.
type MultiSchedulerConfig struct {
	TimeoutInterval time.Duration
	OrphanInterval  time.Duration
	// OrphanThreshold is how long an active run must be without step
	// activity to be reported as an orphan (it is failed past three
	// thresholds). 0 or a negative value means the 30-day default: a
	// negative threshold lies in the future, so every active run would be
	// an orphan past three of them and be failed on the first orphan tick.
	OrphanThreshold time.Duration
	// CommitMaxRetries is each workspace's automatic merge retry budget
	// (WithCommitRetry). 0 means DefaultCommitMaxRetries; a negative value
	// keeps retries unbounded.
	CommitMaxRetries int
	// WorkspaceBudget is the soft time budget of one workspace's scan per
	// tick: its per-item loops stop between items once it is spent and
	// leave the rest to the next tick (a Warn names the scan and how many
	// items remain). It is never a context deadline: an item in progress
	// is never interrupted by it. 0 means TimeoutInterval.
	WorkspaceBudget time.Duration
	// MaxConcurrentWorkspaces bounds how many workspaces one tick scans at
	// once, so a slow tenant no longer delays every other tenant's
	// timeouts, orphan scans and merge retries. 0 means
	// DefaultMaxConcurrentWorkspaces.
	MaxConcurrentWorkspaces int
	// TickWait is how long one tick waits without progress — no slot
	// coming free, none of its scans ending — before it stops waiting
	// (INIT-024 TASK-008). A scan still running then continues in the
	// background and later ticks skip its workspace until it ends; the
	// workspaces not yet dispatched are left to the next tick. A tick that
	// keeps making progress is never cut short. 0 means WorkspaceBudget +
	// TimeoutInterval: a scan stops itself at its budget, and the extra
	// interval covers one item that started just before the budget ran out.
	TickWait time.Duration
	// ScanDeadline is the hard deadline of one workspace's scan (INIT-024
	// TASK-008): its context is cancelled once it has run that long. It is
	// the backstop for a scan no tick waits for any more — one that never
	// ends would keep its pool reference, and an evicted workspace's next
	// request waits for the last one. Cancellation stops its git children
	// with SIGTERM (adapters/git), whose handlers remove git's lock files;
	// a sequence on a shared working tree runs detached from the caller's
	// cancellation and ends within its own bound (5 minutes). 0 means
	// DefaultScanDeadline, twice that bound, so the deadline never cuts
	// an item the sequence bound allows.
	ScanDeadline time.Duration
}

// Defaults for MultiSchedulerConfig.
const (
	// DefaultCommitMaxRetries matches `spine serve`'s single-workspace
	// scheduler (owner question 1).
	DefaultCommitMaxRetries        = 3
	DefaultMaxConcurrentWorkspaces = 4
	DefaultScanDeadline            = 10 * time.Minute
)

// WorkspaceResolver enumerates active workspaces by ID. Satisfied by
// service/internal/workspace.Resolver.
type WorkspaceResolver interface {
	List(ctx context.Context) ([]WorkspaceRef, error)
}

// WorkspaceRef identifies a workspace returned by the resolver.
// Satisfied by service/internal/workspace.Config (which carries an ID).
type WorkspaceRef interface {
	WorkspaceID() string
}

// WorkspacePool resolves a workspace ID to its per-workspace
// scheduler-relevant capabilities. Satisfied by
// service/internal/workspace.ServicePool through a thin adapter passed
// at composition.
type WorkspacePool interface {
	Get(ctx context.Context, workspaceID string) (ServiceSet, error)
	Release(workspaceID string)
}

// ServiceSet collects the per-workspace dependencies the scheduler needs
// to construct and run a per-workspace Scheduler. Satisfied by an
// adapter over service/internal/workspace.ServiceSet at composition.
type ServiceSet struct {
	Store         store.Store
	Events        event.EventRouter
	CommitRetryFn CommitRetryFunc
	// CommitExhaustedFn ends a committing run whose retry budget is spent
	// (INIT-023 TASK-012); see WithCommitExhausted.
	CommitExhaustedFn CommitExhaustedFunc
	StepRecoveryFn    StepRecoveryFunc
	RunFailFn         RunFailFunc
	// RunTerminatedFn closes a timed-out run's live executions
	// (INIT-023 TASK-007); see WithRunTerminated.
	RunTerminatedFn RunTerminatedFunc
}

// NewMultiScheduler creates a scheduler that operates across all active workspaces.
func NewMultiScheduler(pool WorkspacePool, resolver WorkspaceResolver, cfg MultiSchedulerConfig) *MultiScheduler {
	if cfg.TimeoutInterval == 0 {
		cfg.TimeoutInterval = 30 * time.Second
	}
	if cfg.OrphanInterval == 0 {
		cfg.OrphanInterval = 60 * time.Second
	}
	if cfg.OrphanThreshold <= 0 {
		cfg.OrphanThreshold = 30 * 24 * time.Hour
	}
	switch {
	case cfg.CommitMaxRetries == 0:
		cfg.CommitMaxRetries = DefaultCommitMaxRetries
	case cfg.CommitMaxRetries < 0:
		cfg.CommitMaxRetries = 0 // unbounded
	}
	if cfg.WorkspaceBudget <= 0 {
		cfg.WorkspaceBudget = cfg.TimeoutInterval
	}
	if cfg.MaxConcurrentWorkspaces <= 0 {
		cfg.MaxConcurrentWorkspaces = DefaultMaxConcurrentWorkspaces
	}
	if cfg.TickWait <= 0 {
		cfg.TickWait = cfg.WorkspaceBudget + cfg.TimeoutInterval
	}
	if cfg.ScanDeadline <= 0 {
		cfg.ScanDeadline = DefaultScanDeadline
	}
	return &MultiScheduler{
		pool:             pool,
		resolver:         resolver,
		timeoutInterval:  cfg.TimeoutInterval,
		orphanInterval:   cfg.OrphanInterval,
		orphanThreshold:  cfg.OrphanThreshold,
		commitMaxRetries: cfg.CommitMaxRetries,
		workspaceBudget:  cfg.WorkspaceBudget,
		maxConcurrent:    cfg.MaxConcurrentWorkspaces,
		tickWait:         cfg.TickWait,
		scanDeadline:     cfg.ScanDeadline,
		done:             make(chan struct{}),
		exited:           make(chan struct{}),
	}
}

// Config reports the effective configuration, defaults applied (for
// wiring checks and the start-up log).
func (ms *MultiScheduler) Config() MultiSchedulerConfig {
	return MultiSchedulerConfig{
		TimeoutInterval:         ms.timeoutInterval,
		OrphanInterval:          ms.orphanInterval,
		OrphanThreshold:         ms.orphanThreshold,
		CommitMaxRetries:        ms.commitMaxRetries,
		WorkspaceBudget:         ms.workspaceBudget,
		MaxConcurrentWorkspaces: ms.maxConcurrent,
		TickWait:                ms.tickWait,
		ScanDeadline:            ms.scanDeadline,
	}
}

// Start begins the multi-workspace scheduler polling loops. It blocks
// until ctx is cancelled or Stop is called, and must be called once. A
// scan still running when it returns keeps running; Wait waits for it.
func (ms *MultiScheduler) Start(ctx context.Context) {
	defer ms.exitOnce.Do(func() { close(ms.exited) })
	ctx = observe.WithComponent(ctx, "multi-scheduler")
	log := observe.Logger(ctx)
	log.Info("multi-workspace scheduler started",
		"timeout_interval", ms.timeoutInterval,
		"orphan_interval", ms.orphanInterval,
		"max_concurrent_workspaces", ms.maxConcurrent,
		"workspace_budget", ms.workspaceBudget,
		"tick_wait", ms.tickWait,
	)

	timeoutTicker := time.NewTicker(ms.timeoutInterval)
	defer timeoutTicker.Stop()

	orphanTicker := time.NewTicker(ms.orphanInterval)
	defer orphanTicker.Stop()

	// Commit retry interval matches orphan scan (same as single-workspace scheduler).
	commitTicker := time.NewTicker(ms.orphanInterval)
	defer commitTicker.Stop()

	for {
		select {
		case <-timeoutTicker.C:
			ms.ScanTimeouts(ctx)
		case <-orphanTicker.C:
			ms.ScanOrphans(ctx)
		case <-commitTicker.C:
			ms.RunCommitRetry(ctx)
		case <-ctx.Done():
			return
		case <-ms.done:
			return
		}
	}
}

// ScanTimeouts is one tick of the timeout loop: the step and run timeout
// scans of every active workspace (Scheduler.ScanTimeouts and
// ScanRunTimeouts). Start calls it every TimeoutInterval; it is exported
// so a caller (an operator endpoint, a test) can drive one tick
// synchronously. It returns once the tick's scans have ended, or once the
// tick has stalled for TickWait (forEachWorkspace).
func (ms *MultiScheduler) ScanTimeouts(ctx context.Context) {
	ms.forEachWorkspace(ctx, "timeout-scan", func(ctx context.Context, sched *Scheduler) {
		if err := sched.ScanTimeouts(ctx); err != nil {
			observe.Logger(ctx).Error("timeout scan failed", "error", err)
		}
		if err := sched.ScanRunTimeouts(ctx); err != nil {
			observe.Logger(ctx).Error("run timeout scan failed", "error", err)
		}
	})
}

// ScanOrphans is one tick of the orphan loop over every active workspace
// (Scheduler.ScanOrphans); see ScanTimeouts.
func (ms *MultiScheduler) ScanOrphans(ctx context.Context) {
	ms.forEachWorkspace(ctx, "orphan-scan", func(ctx context.Context, sched *Scheduler) {
		if err := sched.ScanOrphans(ctx); err != nil {
			observe.Logger(ctx).Error("orphan scan failed", "error", err)
		}
	})
}

// RunCommitRetry is one tick of the commit-retry loop over every active
// workspace (Scheduler.RunCommitRetry: committing and partially-merged
// runs, under the retry budget); see ScanTimeouts.
func (ms *MultiScheduler) RunCommitRetry(ctx context.Context) {
	ms.forEachWorkspace(ctx, "commit-retry", func(ctx context.Context, sched *Scheduler) {
		sched.RunCommitRetry(ctx)
	})
}

// Stop signals the multi-workspace scheduler to shut down: Start returns,
// a tick in progress stops dispatching and stops waiting, and no new scan
// starts. Scans already running are not cancelled; Wait waits for them.
// Safe to call more than once.
func (ms *MultiScheduler) Stop() {
	ms.stopOnce.Do(func() { close(ms.done) })
}

// Wait blocks until Start has returned and every workspace scan the
// scheduler started — including one a tick stopped waiting for — has
// ended, or until ctx is done (its error is returned). Call it after
// Start, once Stop was called or Start's context cancelled, and not
// concurrently with the one-tick methods. `spine serve` calls it on
// shutdown, under its shutdown deadline, so the workspace pool is not
// closed under a scan that is still writing.
func (ms *MultiScheduler) Wait(ctx context.Context) error {
	select {
	case <-ms.exited:
	case <-ctx.Done():
		return ctx.Err()
	}
	drained := make(chan struct{})
	go func() {
		ms.scans.Wait()
		close(drained)
	}()
	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// stopped reports whether Stop was called.
func (ms *MultiScheduler) stopped() bool {
	select {
	case <-ms.done:
		return true
	default:
		return false
	}
}

// claim marks wsID as having a scan running; false when it already has one.
func (ms *MultiScheduler) claim(wsID string) bool {
	ms.inflightMu.Lock()
	defer ms.inflightMu.Unlock()
	if ms.inflight == nil {
		ms.inflight = map[string]struct{}{}
	}
	if _, busy := ms.inflight[wsID]; busy {
		return false
	}
	ms.inflight[wsID] = struct{}{}
	return true
}

// unclaim ends wsID's running scan.
func (ms *MultiScheduler) unclaim(wsID string) {
	ms.inflightMu.Lock()
	defer ms.inflightMu.Unlock()
	delete(ms.inflight, wsID)
}

// tickPending is the set of one tick's scans that have not ended, for the
// Error naming the workspaces a tick stopped waiting for.
type tickPending struct {
	mu  sync.Mutex
	ids map[string]struct{}
}

func (t *tickPending) add(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ids[id] = struct{}{}
}

func (t *tickPending) remove(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.ids, id)
}

func (t *tickPending) list() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]string, 0, len(t.ids))
	for id := range t.ids {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// forEachWorkspace runs fn with a per-workspace scheduler for every active
// workspace. Errors in one workspace do not block others.
//
// Workspaces run concurrently, at most maxConcurrent at once. The call
// returns once every scan it started has finished — a tick of healthy but
// slow workspaces still scans every one of them, however long that takes —
// unless the tick stalls: once TickWait passes with no slot coming free and
// no scan of the tick ending, it stops waiting (the tenant isolation on
// MultiScheduler). A scan still running then is left running and its
// workspace stays claimed, so later ticks skip it; workspaces not yet
// dispatched wait for the next tick, which no longer counts the stalled
// scans against its slots. Stop and ctx's cancellation also end the wait.
// A workspace whose previous scan is still running is skipped. Each
// workspace's context carries a soft budget (withSoftBudget) that its scan
// loops check between items; neither the budget nor TickWait ever cancels
// the context a scan runs with.
func (ms *MultiScheduler) forEachWorkspace(ctx context.Context, scanName string, fn func(ctx context.Context, sched *Scheduler)) {
	log := observe.Logger(ctx)

	workspaces, err := ms.resolver.List(ctx)
	if err != nil {
		log.Error("list workspaces failed", "scan", scanName, "error", err)
		return
	}

	limit := ms.maxConcurrent
	if limit <= 0 {
		limit = DefaultMaxConcurrentWorkspaces
	}
	wait := ms.tickWait
	if wait <= 0 {
		wait = ms.workspaceBudget + ms.timeoutInterval
	}
	// stall fires once the tick has made no progress — no slot taken, no
	// scan ended — for wait; every progress resets it (Go 1.23+ timers:
	// Reset never leaves a stale tick behind).
	stall := time.NewTimer(wait)
	defer stall.Stop()

	sem := make(chan struct{}, limit)
	// Buffered for every scan this tick can start, so a scan that ends
	// after the tick stopped waiting never blocks on it.
	finished := make(chan struct{}, len(workspaces))
	pending := &tickPending{ids: map[string]struct{}{}}
	started := 0
dispatch:
	for i, ws := range workspaces {
		if ms.stopped() || ctx.Err() != nil {
			break
		}
		wsID := ws.WorkspaceID()
		if !ms.claim(wsID) {
			log.Warn("previous scan of this workspace is still running; skipped this tick",
				"scan", scanName, "workspace_id", wsID)
			continue
		}
		select {
		case sem <- struct{}{}:
			stall.Reset(wait)
		case <-stall.C:
			// Every slot is held by a scan that has not ended for wait:
			// the tick has stalled, so it neither dispatches nor waits
			// any longer (the timer has fired; waiting below again would
			// wait for good).
			ms.unclaim(wsID)
			log.Error("workspace scans made no progress within the tick's wait; they continue in the background, later ticks skip these workspaces until they end, and the remaining workspaces wait for the next tick",
				"scan", scanName, "workspace_ids", pending.list(), "remaining", len(workspaces)-i,
				"max_concurrent_workspaces", limit, "tick_wait", wait)
			return
		case <-ctx.Done():
			ms.unclaim(wsID)
			break dispatch
		case <-ms.done:
			ms.unclaim(wsID)
			break dispatch
		}
		started++
		pending.add(wsID)
		ms.scans.Add(1)
		go func() {
			defer ms.scans.Done()
			defer func() { finished <- struct{}{} }()
			defer pending.remove(wsID)
			defer ms.unclaim(wsID)
			defer func() { <-sem }()
			ms.runWorkspace(ctx, scanName, wsID, fn)
		}()
	}

	for n := 0; n < started; n++ {
		select {
		case <-finished:
			stall.Reset(wait)
		case <-stall.C:
			log.Error("workspace scans made no progress within the tick's wait; they continue in the background and later ticks skip these workspaces until they end",
				"scan", scanName, "workspace_ids", pending.list(), "tick_wait", wait)
			return
		case <-ctx.Done():
			return
		case <-ms.done:
			return
		}
	}
}

// resumeFor returns the workspace's scan resume positions.
func (ms *MultiScheduler) resumeFor(wsID string) *scanResume {
	ms.resumeMu.Lock()
	defer ms.resumeMu.Unlock()
	if ms.resumes == nil {
		ms.resumes = map[string]*scanResume{}
	}
	r := ms.resumes[wsID]
	if r == nil {
		r = &scanResume{}
		ms.resumes[wsID] = r
	}
	return r
}

// runWorkspace is one workspace's share of a tick.
//
// A panic in it — a store or engine bug, data a scan did not expect (the
// engine slices stored values, e.g. a run's trace id) — is recovered and
// logged at Error with its stack (INIT-024 TASK-008): in the shared modes
// it would otherwise end the process every tenant shares, on every tick
// that reaches the same data. The workspace is scanned again on the next
// tick. The recover is deferred first so that it runs last: the pool
// reference taken below, and every lock or transaction the scan released
// by defer, is released before it (net/http's per-request recovery makes
// the same assumption of the code behind every HTTP handler).
//
// The whole share — the pool Get included — runs under ScanDeadline: past
// it the scan's context is cancelled (an Error says so), so a scan no tick
// waits for any more still ends and releases its pool reference.
func (ms *MultiScheduler) runWorkspace(ctx context.Context, scanName, wsID string, fn func(ctx context.Context, sched *Scheduler)) {
	log := observe.Logger(ctx)
	defer func() {
		if r := recover(); r != nil {
			log.Error("workspace scan panicked; recovered, the workspace is scanned again on the next tick",
				"scan", scanName, "workspace_id", wsID, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
		}
	}()
	deadline := ms.scanDeadline
	if deadline <= 0 {
		deadline = DefaultScanDeadline
	}
	parent := ctx
	scanCtx, cancel := context.WithTimeout(parent, deadline)
	defer cancel()
	defer func() {
		if errors.Is(scanCtx.Err(), context.DeadlineExceeded) && parent.Err() == nil {
			log.Error("workspace scan reached its deadline; its context was cancelled and the workspace is scanned again on a later tick",
				"scan", scanName, "workspace_id", wsID, "scan_deadline", deadline)
		}
	}()
	ctx = scanCtx
	ss, err := ms.pool.Get(ctx, wsID)
	if err != nil {
		log.Error("get workspace services failed",
			"scan", scanName,
			"workspace_id", wsID,
			"error", err,
		)
		return
	}
	defer ms.pool.Release(wsID)

	if ss.Store == nil {
		return // no store, skip
	}

	// Create a per-workspace scheduler with engine callbacks from the ServiceSet.
	opts := []Option{WithOrphanThreshold(ms.orphanThreshold)}
	if ss.CommitRetryFn != nil {
		opts = append(opts, WithCommitRetry(ss.CommitRetryFn, ms.commitMaxRetries, 0))
	}
	if ss.CommitExhaustedFn != nil {
		opts = append(opts, WithCommitExhausted(ss.CommitExhaustedFn))
	}
	if ss.StepRecoveryFn != nil {
		opts = append(opts, WithStepRecovery(ss.StepRecoveryFn))
	}
	if ss.RunFailFn != nil {
		opts = append(opts, WithRunFail(ss.RunFailFn))
	}
	if ss.RunTerminatedFn != nil {
		opts = append(opts, WithRunTerminated(ss.RunTerminatedFn))
	}
	sched := New(ss.Store, ss.Events, opts...)

	wsCtx := observe.WithWorkspaceID(ctx, wsID)
	if ms.workspaceBudget > 0 {
		wsCtx = withSoftBudget(wsCtx, time.Now().Add(ms.workspaceBudget), ms.resumeFor(wsID))
	}
	fn(wsCtx, sched)
}

// softBudgetKey carries a workspace scan's softBudget.
type softBudgetKey struct{}

// softBudget is one workspace's soft time budget for one tick, and the
// positions its budgeted scans resume from (kept across ticks).
type softBudget struct {
	deadline time.Time
	resume   *scanResume
}

// scanResume is where each budgeted scan of one workspace stopped (codex
// review round 1): a scan cut short resumes there on the next tick
// instead of at the start of the list again, so a prefix that keeps using
// the whole budget cannot starve the items behind it.
type scanResume struct {
	mu   sync.Mutex
	next map[string]int
}

func (r *scanResume) get(scan string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.next[scan]
}

func (r *scanResume) set(scan string, i int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.next == nil {
		r.next = map[string]int{}
	}
	r.next[scan] = i
}

// withSoftBudget attaches a soft deadline (and the workspace's resume
// positions) to ctx. It is a value, not a context deadline: nothing is
// cancelled when it passes.
func withSoftBudget(ctx context.Context, deadline time.Time, resume *scanResume) context.Context {
	if resume == nil {
		resume = &scanResume{}
	}
	return context.WithValue(ctx, softBudgetKey{}, &softBudget{deadline: deadline, resume: resume})
}

// budgetIter walks the n items of one scan. Without a budget in ctx
// (single-workspace mode) it visits 0..n-1 in order, exactly the loop it
// replaces. With one it starts where the scan's previous pass stopped,
// wraps around, and stops between items once the deadline has passed —
// always after at least one item, so every scan makes progress each tick —
// recording where to resume and logging the stop at Warn (scan,
// workspace_id, processed, remaining).
type budgetIter struct {
	ctx      context.Context
	scan     string
	n        int
	k        int // items visited so far
	start    int
	i        int // the current item's index
	b        *softBudget
	finished bool
}

func newBudgetIter(ctx context.Context, scan string, n int) *budgetIter {
	it := &budgetIter{ctx: ctx, scan: scan, n: n}
	if b, ok := ctx.Value(softBudgetKey{}).(*softBudget); ok && b != nil {
		it.b = b
		if n > 0 {
			it.start = b.resume.get(scan) % n
		}
	}
	return it
}

// next advances to the next item and reports whether there is one.
func (it *budgetIter) next() bool {
	if it.finished {
		return false
	}
	if it.k >= it.n {
		it.finished = true
		if it.b != nil {
			it.b.resume.set(it.scan, 0)
		}
		return false
	}
	if it.k > 0 && it.b != nil && !time.Now().Before(it.b.deadline) {
		it.finished = true
		it.b.resume.set(it.scan, (it.start+it.k)%it.n)
		observe.Logger(it.ctx).Warn("workspace scan budget spent; the rest waits for the next tick",
			"scan", it.scan, "workspace_id", observe.WorkspaceID(it.ctx),
			"processed", it.k, "remaining", it.n-it.k)
		return false
	}
	it.i = (it.start + it.k) % it.n
	it.k++
	return true
}
