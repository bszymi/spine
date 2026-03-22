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
	}

	return nil
}
