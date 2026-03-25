package scheduler

import (
	"context"
	"time"
)

// Option configures the Scheduler.
type Option func(*Scheduler)

// CommitRetryFunc is called by the scheduler to retry merge for committing runs.
type CommitRetryFunc func(ctx context.Context, runID string) error

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
func WithCommitRetry(fn CommitRetryFunc, maxRetries int, threshold time.Duration) Option {
	return func(s *Scheduler) {
		s.commitRetryFn = fn
		s.commitMaxRetries = maxRetries
		s.commitThreshold = threshold
	}
}
