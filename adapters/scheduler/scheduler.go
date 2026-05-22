package scheduler

import (
	"context"
	"time"

	"github.com/bszymi/spine/core/event"
	"github.com/bszymi/spine/core/observe"
	"github.com/bszymi/spine/adapters/store"
)

// Scheduler implements time-based triggers and crash recovery.
// Per Engine State Machine §6.3.
type Scheduler struct {
	store            store.Store
	events           event.EventRouter
	timeoutInterval  time.Duration
	orphanInterval   time.Duration
	orphanThreshold  time.Duration
	commitRetryFn    CommitRetryFunc
	commitMaxRetries int
	commitThreshold  time.Duration
	stepRecoveryFn   StepRecoveryFunc
	runFailFn        RunFailFunc
	// now is the clock source for scan-loop time reads (run timeouts,
	// step timeouts, orphan threshold). Production keeps the default
	// time.Now; scenario tests inject a controllable clock via
	// WithClock so harness.Clock.Advance moves the scheduler's
	// observed time deterministically. Recovery paths still use
	// time.Now directly — those write CreatedAt timestamps and event
	// IDs rather than gating policy on a comparison, so injecting a
	// clock there would only add a seam tests don't need.
	now  func() time.Time
	done chan struct{}
}

// New creates a Scheduler with the given options.
func New(s store.Store, events event.EventRouter, opts ...Option) *Scheduler {
	sched := &Scheduler{
		store:           s,
		events:          events,
		timeoutInterval: 30 * time.Second,
		orphanInterval:  60 * time.Second,
		orphanThreshold: 30 * 24 * time.Hour, // 30 days — Spine workflows are human-paced
		now:             time.Now,
		done:            make(chan struct{}),
	}
	for _, opt := range opts {
		opt(sched)
	}
	if sched.now == nil {
		sched.now = time.Now
	}
	return sched
}

// Start begins the scheduler polling loops. Blocks until ctx is cancelled or Stop is called.
func (s *Scheduler) Start(ctx context.Context) {
	ctx = observe.WithComponent(ctx, "scheduler")
	log := observe.Logger(ctx)
	log.Info("scheduler started",
		"timeout_interval", s.timeoutInterval,
		"orphan_interval", s.orphanInterval,
	)

	timeoutTicker := time.NewTicker(s.timeoutInterval)
	defer timeoutTicker.Stop()

	orphanTicker := time.NewTicker(s.orphanInterval)
	defer orphanTicker.Stop()

	// Commit retry interval matches orphan scan (reasonable frequency).
	commitTicker := time.NewTicker(s.orphanInterval)
	defer commitTicker.Stop()

	for {
		select {
		case <-timeoutTicker.C:
			if err := s.ScanTimeouts(ctx); err != nil {
				log.Error("timeout scan failed", "error", err)
			}
			if err := s.ScanRunTimeouts(ctx); err != nil {
				log.Error("run timeout scan failed", "error", err)
			}
		case <-orphanTicker.C:
			if err := s.ScanOrphans(ctx); err != nil {
				log.Error("orphan scan failed", "error", err)
			}
		case <-commitTicker.C:
			if s.commitRetryFn != nil {
				s.retryCommittingRuns(ctx)
			}
		case <-ctx.Done():
			return
		case <-s.done:
			return
		}
	}
}

// Stop signals the scheduler to shut down.
func (s *Scheduler) Stop() {
	close(s.done)
}
