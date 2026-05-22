package scheduler

import (
	"context"
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
type MultiScheduler struct {
	pool            WorkspacePool
	resolver        WorkspaceResolver
	timeoutInterval time.Duration
	orphanInterval  time.Duration
	orphanThreshold time.Duration
	done            chan struct{}
}

// MultiSchedulerConfig holds configuration for the multi-workspace scheduler.
type MultiSchedulerConfig struct {
	TimeoutInterval time.Duration
	OrphanInterval  time.Duration
	OrphanThreshold time.Duration
}

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
	Store          store.Store
	Events         event.EventRouter
	CommitRetryFn  CommitRetryFunc
	StepRecoveryFn StepRecoveryFunc
	RunFailFn      RunFailFunc
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
	return &MultiScheduler{
		pool:            pool,
		resolver:        resolver,
		timeoutInterval: cfg.TimeoutInterval,
		orphanInterval:  cfg.OrphanInterval,
		orphanThreshold: cfg.OrphanThreshold,
		done:            make(chan struct{}),
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

// forEachWorkspace iterates over all active workspaces and runs the given
// function with a per-workspace scheduler. Errors in one workspace do not
// block others.
func (ms *MultiScheduler) forEachWorkspace(ctx context.Context, scanName string, fn func(ctx context.Context, sched *Scheduler)) {
	log := observe.Logger(ctx)

	workspaces, err := ms.resolver.List(ctx)
	if err != nil {
		log.Error("list workspaces failed", "scan", scanName, "error", err)
		return
	}

	for _, ws := range workspaces {
		wsID := ws.WorkspaceID()
		ss, err := ms.pool.Get(ctx, wsID)
		if err != nil {
			log.Error("get workspace services failed",
				"scan", scanName,
				"workspace_id", wsID,
				"error", err,
			)
			continue
		}

		if ss.Store == nil {
			ms.pool.Release(wsID)
			continue // no store, skip
		}

		// Create a per-workspace scheduler with engine callbacks from the ServiceSet.
		opts := []Option{WithOrphanThreshold(ms.orphanThreshold)}
		if ss.CommitRetryFn != nil {
			opts = append(opts, WithCommitRetry(ss.CommitRetryFn, 0, 0))
		}
		if ss.StepRecoveryFn != nil {
			opts = append(opts, WithStepRecovery(ss.StepRecoveryFn))
		}
		if ss.RunFailFn != nil {
			opts = append(opts, WithRunFail(ss.RunFailFn))
		}
		sched := New(ss.Store, ss.Events, opts...)

		wsCtx := observe.WithWorkspaceID(ctx, wsID)
		fn(wsCtx, sched)

		ms.pool.Release(wsID)
	}
}
