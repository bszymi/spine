package scheduler

import (
	"context"
	"fmt"
	"time"

	"github.com/bszymi/spine/internal/observe"
)

// ScanOrphans detects runs that have been active without recent step activity.
// Per Error Handling §6.2: orphaned runs are flagged but never deleted.
func (s *Scheduler) ScanOrphans(ctx context.Context) error {
	log := observe.Logger(ctx)
	observe.GlobalMetrics.SchedulerScans.Inc()

	threshold := time.Now().Add(-s.orphanThreshold)
	runs, err := s.store.ListStaleActiveRuns(ctx, threshold)
	if err != nil {
		return fmt.Errorf("list stale active runs: %w", err)
	}

	for i := range runs {
		observe.GlobalMetrics.OrphansDetected.Inc()
		log.Warn("potential orphan run detected",
			"run_id", runs[i].RunID,
			"task_path", runs[i].TaskPath,
			"created_at", runs[i].CreatedAt,
			"current_step_id", runs[i].CurrentStepID,
		)

		// Only fail runs that have been stale for 3x the orphan threshold.
		// Single-threshold orphans are logged as warnings but may just be slow;
		// persistent orphans are truly stuck and should be failed.
		if s.runFailFn != nil && time.Since(runs[i].CreatedAt) > 3*s.orphanThreshold {
			if err := s.runFailFn(ctx, runs[i].RunID,
				fmt.Sprintf("orphaned: no activity for %s", s.orphanThreshold)); err != nil {
				log.Error("failed to fail orphaned run", "run_id", runs[i].RunID, "error", err)
			} else {
				log.Info("orphaned run failed", "run_id", runs[i].RunID)
			}
		}
	}

	return nil
}
