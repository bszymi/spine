package scheduler

import (
	"context"
	"time"

	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/workspace"
)

// MultiScheduler runs scheduler scans across all active workspaces.
// It uses the workspace service pool to get per-workspace stores and
// event routers. Per components.md §6.5, background services iterate
// over workspaces from List() and process each using its service set.
type MultiScheduler struct {
	pool            *workspace.ServicePool
	resolver        workspace.Resolver
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

// NewMultiScheduler creates a scheduler that operates across all active workspaces.
func NewMultiScheduler(pool *workspace.ServicePool, resolver workspace.Resolver, cfg MultiSchedulerConfig) *MultiScheduler {
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
		ss, err := ms.pool.Get(ctx, ws.ID)
		if err != nil {
			log.Error("get workspace services failed",
				"scan", scanName,
				"workspace_id", ws.ID,
				"error", err,
			)
			continue
		}

		if ss.Store == nil {
			continue // no store, skip
		}

		// Create a per-workspace scheduler for this scan pass.
		// Note: engine-dependent recovery functions are not wired here —
		// they require per-workspace orchestrator construction (TODO INIT-009).
		sched := New(ss.Store, ss.Events,
			WithOrphanThreshold(ms.orphanThreshold),
		)

		wsCtx := observe.WithWorkspaceID(ctx, ws.ID)
		fn(wsCtx, sched)

		ms.pool.Release(ws.ID)
	}
}
