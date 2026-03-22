package scheduler

import "time"

// Option configures the Scheduler.
type Option func(*Scheduler)

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
