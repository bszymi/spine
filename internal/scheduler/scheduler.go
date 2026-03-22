package scheduler

import (
	"context"
	"time"

	"github.com/bszymi/spine/internal/event"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/store"
)

// Scheduler implements time-based triggers and crash recovery.
// Per Engine State Machine §6.3.
type Scheduler struct {
	store           store.Store
	events          event.EventRouter
	timeoutInterval time.Duration
	orphanInterval  time.Duration
	orphanThreshold time.Duration
	done            chan struct{}
}

// New creates a Scheduler with the given options.
func New(s store.Store, events event.EventRouter, opts ...Option) *Scheduler {
	sched := &Scheduler{
		store:           s,
		events:          events,
		timeoutInterval: 30 * time.Second,
		orphanInterval:  60 * time.Second,
		orphanThreshold: 5 * time.Minute,
		done:            make(chan struct{}),
	}
	for _, opt := range opts {
		opt(sched)
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

	for {
		select {
		case <-timeoutTicker.C:
			if err := s.ScanTimeouts(ctx); err != nil {
				log.Error("timeout scan failed", "error", err)
			}
		case <-orphanTicker.C:
			if err := s.ScanOrphans(ctx); err != nil {
				log.Error("orphan scan failed", "error", err)
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
