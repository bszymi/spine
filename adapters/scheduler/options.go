package scheduler

import (
	"context"
	"time"
)

// Option configures the Scheduler.
type Option func(*Scheduler)

// CommitRetryFunc is called by the scheduler to retry merge for committing runs.
type CommitRetryFunc func(ctx context.Context, runID string) error

// CommitExhaustedFunc decides, before a commit retry, whether a committing
// run's automatic merge retries are spent (INIT-023 TASK-012, audit
// P3-27). When they are it ends the run (the engine routes it like a
// permanent merge failure) and returns abandoned = true; the scheduler then
// does not retry it. `spine serve` passes engine.Orchestrator's
// AbandonExhaustedMerge.
type CommitExhaustedFunc func(ctx context.Context, runID string, maxRetries int) (abandoned bool, err error)

// StepRecoveryFunc is called by recovery to resume a step through the engine.
// For completed steps it advances the run; for failed steps it evaluates retry.
type StepRecoveryFunc func(ctx context.Context, executionID string) error

// RunFailFunc is called by recovery to fail a run that cannot be recovered.
type RunFailFunc func(ctx context.Context, runID, reason string) error

// RunTerminatedFunc is called after the scheduler itself ends a run, so the
// engine can close the run's live step executions (INIT-023 TASK-007). The
// scheduler does not import the engine; `spine serve` passes the
// orchestrator's TerminateLiveSteps.
type RunTerminatedFunc func(ctx context.Context, runID, message string)

// WithTimeoutScanInterval sets how often the timeout scanner runs.
func WithTimeoutScanInterval(d time.Duration) Option {
	return func(s *Scheduler) { s.timeoutInterval = d }
}

// WithOrphanScanInterval sets how often the orphan detector runs.
func WithOrphanScanInterval(d time.Duration) Option {
	return func(s *Scheduler) { s.orphanInterval = d }
}

// WithOrphanThreshold sets how long a run must be inactive before being flagged.
func WithOrphanThreshold(d time.Duration) Option {
	return func(s *Scheduler) { s.orphanThreshold = d }
}

// WithCommitRetry sets the function used to retry merges for committing runs.
//
// maxRetries is the automatic retry budget (INIT-023 TASK-012, P3-27): with
// maxRetries > 0, a committing run whose recorded merge outcome failed
// transiently more than maxRetries times is handed to the
// WithCommitExhausted function instead of being retried (or, with none
// wired, left in committing with an Error log). 0 keeps today's unbounded
// retries. threshold delays the startup recovery pass.
func WithCommitRetry(fn CommitRetryFunc, maxRetries int, threshold time.Duration) Option {
	return func(s *Scheduler) {
		s.commitRetryFn = fn
		s.commitMaxRetries = maxRetries
		s.commitThreshold = threshold
	}
}

// WithCommitExhausted sets the function that ends a committing run whose
// merge retry budget (WithCommitRetry's maxRetries) is spent. Unset, an
// exhausted run is never retried again and stays in committing, with an
// Error log each tick: a misconfiguration stops the loop instead of
// retrying forever.
func WithCommitExhausted(fn CommitExhaustedFunc) Option {
	return func(s *Scheduler) { s.commitExhaustedFn = fn }
}

// WithStepRecovery sets the function used to recover completed/failed steps.
func WithStepRecovery(fn StepRecoveryFunc) Option {
	return func(s *Scheduler) { s.stepRecoveryFn = fn }
}

// WithRunFail sets the function used to fail unrecoverable runs.
func WithRunFail(fn RunFailFunc) Option {
	return func(s *Scheduler) { s.runFailFn = fn }
}

// WithRunTerminated sets the function called after a run timeout has
// cancelled a run (only once the cancel write is applied). Unset, the
// timeout path behaves exactly as before.
func WithRunTerminated(fn RunTerminatedFunc) Option {
	return func(s *Scheduler) { s.runTerminatedFn = fn }
}

// WithClock overrides the time source used by scan-loop policy reads
// (run timeouts, step timeouts, orphan threshold). The scenario
// harness uses this to drive deterministic time advance via
// harness.Clock.Advance — passing nil restores the time.Now default.
func WithClock(now func() time.Time) Option {
	return func(s *Scheduler) { s.now = now }
}
