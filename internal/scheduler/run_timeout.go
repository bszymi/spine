package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/event"
	"github.com/bszymi/spine/internal/observe"
)

// ScanRunTimeouts checks active runs for run-level timeout expiry.
// Timed-out runs are cancelled. Per Engine State Machine §6.3.
//
// The scan-time `now` produced by s.now() is threaded into every side
// effect this scan triggers — the `ListTimedOutRuns` predicate, the
// per-run `UpdateRunStatusAt` write, and the emitted run_timeout
// event's `Timestamp` — so an injected clock (harness.Clock.Advance
// in scenario tests) drives all three reads coherently. Without this
// pinning, an `Advance(2h)` scenario crosses `timeout_at` for the
// predicate but the persisted `completed_at` and event timestamp
// remain in real time, leaving a "cancelled before its own
// completed_at reaches the deadline" inconsistency.
func (s *Scheduler) ScanRunTimeouts(ctx context.Context) error {
	log := observe.Logger(ctx)
	observe.GlobalMetrics.SchedulerScans.Inc()

	now := s.now()
	runs, err := s.store.ListTimedOutRuns(ctx, now)
	if err != nil {
		return fmt.Errorf("list timed out runs: %w", err)
	}

	for i := range runs {
		run := &runs[i]
		if err := s.handleRunTimeout(ctx, run, now); err != nil {
			log.Error("handle run timeout failed", "run_id", run.RunID, "error", err)
		}
	}

	return nil
}

func (s *Scheduler) handleRunTimeout(ctx context.Context, run *domain.Run, scanNow time.Time) error {
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

	if err := s.store.UpdateRunStatusAt(ctx, run.RunID, domain.RunStatusCancelled, scanNow); err != nil {
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
	event.EmitLogged(ctx, s.events, domain.Event{
		EventID:   fmt.Sprintf("timeout-run-%s", run.RunID),
		Type:      domain.EventRunTimeout,
		Timestamp: scanNow,
		RunID:     run.RunID,
		TraceID:   run.TraceID,
		Payload:   payload,
	})

	return nil
}
