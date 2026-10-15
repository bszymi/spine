package scheduler

import (
	"context"
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
// the commit retry budget. Not constructed by `spine serve` today (owner
// question 3: wiring it into the shared modes is a follow-up); bounded so
// that wiring inherits the limits.
type MultiScheduler struct {
	pool             WorkspacePool
	resolver         WorkspaceResolver
	timeoutInterval  time.Duration
	orphanInterval   time.Duration
	orphanThreshold  time.Duration
	commitMaxRetries int
	workspaceBudget  time.Duration
	maxConcurrent    int
	done             chan struct{}
}

// MultiSchedulerConfig holds configuration for the multi-workspace scheduler.
type MultiSchedulerConfig struct {
	TimeoutInterval time.Duration
	OrphanInterval  time.Duration
	OrphanThreshold time.Duration
	// CommitMaxRetries is each workspace's automatic merge retry budget
	// (WithCommitRetry). 0 means DefaultCommitMaxRetries; a negative value
	// keeps retries unbounded.
	CommitMaxRetries int
	// WorkspaceBudget is the soft time budget of one workspace's scan per
	// tick: its per-item loops stop between items once it is spent and
	// leave the rest to the next tick (a Warn names the scan and how many
	// items remain). It is never a context deadline: cancelling the
	// context would stop git mid-command and could leave a lock file that
	// fails every later attempt. 0 means TimeoutInterval.
	WorkspaceBudget time.Duration
	// MaxConcurrentWorkspaces bounds how many workspaces one tick scans at
	// once, so a slow tenant no longer delays every other tenant's
	// timeouts, orphan scans and merge retries. 0 means
	// DefaultMaxConcurrentWorkspaces.
	MaxConcurrentWorkspaces int
}

// Defaults for MultiSchedulerConfig.
const (
	// DefaultCommitMaxRetries matches `spine serve`'s single-workspace
	// scheduler (owner question 1).
	DefaultCommitMaxRetries        = 3
	DefaultMaxConcurrentWorkspaces = 4
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
	if cfg.OrphanThreshold == 0 {
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
	return &MultiScheduler{
		pool:             pool,
		resolver:         resolver,
		timeoutInterval:  cfg.TimeoutInterval,
		orphanInterval:   cfg.OrphanInterval,
		orphanThreshold:  cfg.OrphanThreshold,
		commitMaxRetries: cfg.CommitMaxRetries,
		workspaceBudget:  cfg.WorkspaceBudget,
		maxConcurrent:    cfg.MaxConcurrentWorkspaces,
		done:             make(chan struct{}),
	}
}

// Start begins the multi-workspace scheduler polling loops.
func (ms *MultiScheduler) Start(ctx context.Context) {
	ctx = observe.WithComponent(ctx, "multi-scheduler")
	log := observe.Logger(ctx)
	log.Info("multi-workspace scheduler started")

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
			ms.forEachWorkspace(ctx, "timeout-scan", func(ctx context.Context, sched *Scheduler) {
				if err := sched.ScanTimeouts(ctx); err != nil {
					observe.Logger(ctx).Error("timeout scan failed", "error", err)
				}
				if err := sched.ScanRunTimeouts(ctx); err != nil {
					observe.Logger(ctx).Error("run timeout scan failed", "error", err)
				}
			})
		case <-orphanTicker.C:
			ms.forEachWorkspace(ctx, "orphan-scan", func(ctx context.Context, sched *Scheduler) {
				if err := sched.ScanOrphans(ctx); err != nil {
					observe.Logger(ctx).Error("orphan scan failed", "error", err)
				}
			})
		case <-commitTicker.C:
			ms.forEachWorkspace(ctx, "commit-retry", func(ctx context.Context, sched *Scheduler) {
				sched.RunCommitRetry(ctx)
			})
		case <-ctx.Done():
			return
		case <-ms.done:
			return
		}
	}
}

// Stop signals the multi-workspace scheduler to shut down.
func (ms *MultiScheduler) Stop() {
	close(ms.done)
}

// forEachWorkspace runs fn with a per-workspace scheduler for every active
// workspace. Errors in one workspace do not block others.
//
// Workspaces run concurrently, at most maxConcurrent at once, and the call
// returns only after every one finished, so ticks never overlap. Each
// workspace's context carries a soft budget (withSoftBudget) that its scan
// loops check between items; the context itself is never cancelled by the
// budget.
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
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for _, ws := range workspaces {
		wsID := ws.WorkspaceID()
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			ms.runWorkspace(ctx, scanName, wsID, fn)
		}()
	}
	wg.Wait()
}

// runWorkspace is one workspace's share of a tick.
func (ms *MultiScheduler) runWorkspace(ctx context.Context, scanName, wsID string, fn func(ctx context.Context, sched *Scheduler)) {
	log := observe.Logger(ctx)
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
		wsCtx = withSoftBudget(wsCtx, time.Now().Add(ms.workspaceBudget))
	}
	fn(wsCtx, sched)
}

// softBudgetKey carries a workspace scan's soft deadline.
type softBudgetKey struct{}

// withSoftBudget attaches a soft deadline to ctx. It is a value, not a
// context deadline: nothing is cancelled when it passes.
func withSoftBudget(ctx context.Context, deadline time.Time) context.Context {
	return context.WithValue(ctx, softBudgetKey{}, deadline)
}

// softBudgetSpent reports whether ctx's soft budget has passed; the
// caller's loop then stops before item processed of total. It logs the
// stop at Warn. A context without a budget (single-workspace mode) never
// stops.
func softBudgetSpent(ctx context.Context, scan string, processed, total int) bool {
	deadline, ok := ctx.Value(softBudgetKey{}).(time.Time)
	if !ok || time.Now().Before(deadline) {
		return false
	}
	observe.Logger(ctx).Warn("workspace scan budget spent; the rest waits for the next tick",
		"scan", scan, "workspace_id", observe.WorkspaceID(ctx),
		"processed", processed, "remaining", total-processed)
	return true
}
