package delivery

import (
	"context"
	"time"

	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/store"
)

const defaultRetention = 7 * 24 * time.Hour // 7 days

// StartRetentionCleanup runs a background loop that deletes expired
// delivered/dead delivery entries. Runs every hour. Takes a
// store.DeliveryStore (INIT-022 EPIC-001 TASK-010) — only
// DeleteExpiredDeliveries is called, but the full role interface keeps
// the dependency typed at the level of the surface this routine owns.
func StartRetentionCleanup(ctx context.Context, st store.DeliveryStore, retention time.Duration) {
	if retention <= 0 {
		retention = defaultRetention
	}

	log := observe.Logger(ctx)
	log.Info("delivery retention cleanup started", "retention", retention)

	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			before := time.Now().Add(-retention)
			deleted, err := st.DeleteExpiredDeliveries(ctx, before)
			if err != nil {
				log.Error("retention cleanup failed", "error", err)
			} else if deleted > 0 {
				log.Info("retention cleanup complete", "deleted", deleted)
			}
		}
	}
}
