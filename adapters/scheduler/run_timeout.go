package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/event"
	"github.com/bszymi/spine/core/observe"
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

	for it := newBudgetIter(ctx, "run-timeouts", len(runs)); it.next(); {
		run := &runs[it.i]
		if err := s.handleRunTimeout(ctx, run, now); err != nil {
			log.Error("handle run timeout failed", "run_id", run.RunID, "error", err)
		}
	}

	return nil
}

// runTimeoutTransitioner is the store's optional conditional form of
// UpdateRunStatusAt (INIT-023 TASK-008): the same write, applied only while
// the run is still in fromStatus. *store.PostgresStore implements it.
type runTimeoutTransitioner interface {
	TransitionRunStatusAt(ctx context.Context, runID string, fromStatus, toStatus domain.RunStatus, completedAt time.Time) (bool, error)
}

// cancelTimedOutRun writes the cancel. With a conditional store it applies
// only while the run still holds the status the re-read saw, so a run that
// moved in between (to committing, completed, cancelled) is not
// overwritten; a store without it keeps the blind UpdateRunStatusAt.
func (s *Scheduler) cancelTimedOutRun(ctx context.Context, runID string, from domain.RunStatus, scanNow time.Time) (bool, error) {
	if t, ok := s.store.(runTimeoutTransitioner); ok {
		return t.TransitionRunStatusAt(ctx, runID, from, domain.RunStatusCancelled, scanNow)
	}
	return true, s.store.UpdateRunStatusAt(ctx, runID, domain.RunStatusCancelled, scanNow)
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

	applied, err := s.cancelTimedOutRun(ctx, run.RunID, current.Status, scanNow)
	if err != nil {
		return fmt.Errorf("cancel timed-out run: %w", err)
	}
	if !applied {
		log.Info("run changed before its timeout cancel was recorded; skipped",
			"run_id", run.RunID, "read_status", current.Status)
		return nil
	}
	// INIT-023 TASK-007: this cancel writes the run row directly and never
	// calls the engine's CancelRun (which would also delete the run branch,
	// something the timeout path deliberately does not do), so the run's
	// live executions are closed through the hook — only after the cancel
	// write has been applied. With TASK-008's compare-and-swap the hook
	// fires only on an applied write.
	if s.runTerminatedFn != nil {
		s.runTerminatedFn(ctx, run.RunID, "run cancelled: run timeout")
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
