package workspace

import (
	"context"
	"time"

	"github.com/bszymi/spine/internal/observe"
)

// MultiProjectionSync runs projection sync across all active workspaces.
// Each workspace's projection service (from the ServiceSet) performs
// its own incremental sync against its own database and Git repo.
// Per components.md §6.5, background services iterate over workspaces
// from List() and process each using its service set from the pool.
type MultiProjectionSync struct {
	pool         *ServicePool
	resolver     Resolver
	pollInterval time.Duration
	done         chan struct{}
}

// MultiProjectionSyncConfig holds configuration for multi-workspace projection sync.
type MultiProjectionSyncConfig struct {
	PollInterval time.Duration
}

// NewMultiProjectionSync creates a multi-workspace projection sync service.
func NewMultiProjectionSync(pool *ServicePool, resolver Resolver, cfg MultiProjectionSyncConfig) *MultiProjectionSync {
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 30 * time.Second
	}
	return &MultiProjectionSync{
		pool:         pool,
		resolver:     resolver,
		pollInterval: cfg.PollInterval,
		done:         make(chan struct{}),
	}
}

// Start begins the multi-workspace sync polling loop.
func (ms *MultiProjectionSync) Start(ctx context.Context) {
	ctx = observe.WithComponent(ctx, "multi-projection-sync")
	log := observe.Logger(ctx)
	log.Info("multi-workspace projection sync started", "interval", ms.pollInterval)

	ticker := time.NewTicker(ms.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			ms.syncAll(ctx)
		case <-ctx.Done():
			log.Info("multi-workspace projection sync stopped")
			return
		case <-ms.done:
			log.Info("multi-workspace projection sync stopped")
			return
		}
	}
}

// Stop signals the sync loop to shut down.
func (ms *MultiProjectionSync) Stop() {
	close(ms.done)
}

// syncAll iterates over all active workspaces and runs incremental sync
// for each. Errors in one workspace do not block others.
func (ms *MultiProjectionSync) syncAll(ctx context.Context) {
	log := observe.Logger(ctx)

	workspaces, err := ms.resolver.List(ctx)
	if err != nil {
		log.Error("list workspaces for projection sync failed", "error", err)
		return
	}

	for _, ws := range workspaces {
		ss, err := ms.pool.Get(ctx, ws.ID)
		if err != nil {
			log.Error("get workspace services for projection sync failed",
				"workspace_id", ws.ID,
				"error", err,
			)
			continue
		}

		if ss.ProjSync == nil {
			ms.pool.Release(ws.ID)
			continue
		}

		wsCtx := observe.WithWorkspaceID(ctx, ws.ID)
		if err := ss.ProjSync.IncrementalSync(wsCtx); err != nil {
			observe.Logger(wsCtx).Error("workspace projection sync failed", "error", err)
		}

		ms.pool.Release(ws.ID)
	}
}
