package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
)

// ScanRunTimeouts checks active runs for run-level timeout expiry.
// Timed-out runs are cancelled. Per Engine State Machine §6.3.
func (s *Scheduler) ScanRunTimeouts(ctx context.Context) error {
	log := observe.Logger(ctx)
	observe.GlobalMetrics.SchedulerScans.Inc()

	now := time.Now()
	runs, err := s.store.ListTimedOutRuns(ctx, now)
	if err != nil {
		return fmt.Errorf("list timed out runs: %w", err)
	}

	for i := range runs {
		run := &runs[i]
		if err := s.handleRunTimeout(ctx, run); err != nil {
			log.Error("handle run timeout failed", "run_id", run.RunID, "error", err)
		}
	}

	return nil
}

func (s *Scheduler) handleRunTimeout(ctx context.Context, run *domain.Run) error {
	log := observe.Logger(ctx)

	// Re-read the run to avoid overwriting a newer state that arrived
	// between the scan snapshot and this update.
	current, err := s.store.GetRun(ctx, run.RunID)
	if err != nil {
		return fmt.Errorf("re-read run for timeout: %w", err)
	}
	if current.Status.IsTerminal() || current.Status == domain.RunStatusCommitting {
		log.Info("run already progressed past timeout window, skipping",
			"run_id", run.RunID, "status", current.Status)
		return nil
	}

	if err := s.store.UpdateRunStatus(ctx, run.RunID, domain.RunStatusCancelled); err != nil {
		return fmt.Errorf("cancel timed-out run: %w", err)
	}

	log.Info("run timed out",
		"run_id", run.RunID,
		"task_path", run.TaskPath,
		"timeout_at", run.TimeoutAt,
	)

	payload, _ := json.Marshal(map[string]string{
		"run_id":    run.RunID,
		"task_path": run.TaskPath,
	})
	if err := s.events.Emit(ctx, domain.Event{
		EventID:   fmt.Sprintf("timeout-run-%s", run.RunID),
		Type:      domain.EventRunTimeout,
		Timestamp: time.Now(),
		RunID:     run.RunID,
		TraceID:   run.TraceID,
		Payload:   payload,
	}); err != nil {
		log.Warn("failed to emit run_timeout event", "run_id", run.RunID, "error", err)
	}

	return nil
}
