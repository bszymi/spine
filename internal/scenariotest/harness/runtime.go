package harness

import (
	"context"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/artifact"
	"github.com/bszymi/spine/internal/event"
	"github.com/bszymi/spine/internal/projection"
	"github.com/bszymi/spine/internal/queue"
	"github.com/bszymi/spine/internal/store"
	"github.com/bszymi/spine/internal/validation"
)

// TestRuntime wires Spine services for in-process scenario execution.
// Services are constructed identically to production (cmd/spine/main.go),
// ensuring test fidelity. The runtime is configured for synchronous,
// deterministic behaviour — no background polling or async event delivery.
type TestRuntime struct {
	Store       *store.PostgresStore
	Artifacts   *artifact.Service
	Projections *projection.Service
	Validator   *validation.Engine
	Events      event.EventRouter
	Queue       *queue.MemoryQueue
}

// RuntimeOption configures optional components of the TestRuntime.
type RuntimeOption func(*runtimeConfig)

type runtimeConfig struct {
	withEvents     bool
	withValidation bool
}

// WithEvents enables the event system (MemoryQueue + QueueRouter).
// When enabled, artifact and projection services receive a real event
// router instead of nil. The queue runs in a background goroutine
// and is stopped automatically when the test ends.
func WithEvents() RuntimeOption {
	return func(c *runtimeConfig) {
		c.withEvents = true
	}
}

// WithValidation enables the cross-artifact validation engine.
func WithValidation() RuntimeOption {
	return func(c *runtimeConfig) {
		c.withValidation = true
	}
}

// NewTestRuntime creates a TestRuntime wired to the given repo and database.
// By default, event routers are nil and validation is disabled.
// Use WithEvents() and WithValidation() to enable optional components.
func NewTestRuntime(t *testing.T, repo *TestRepo, db *TestDB, opts ...RuntimeOption) *TestRuntime {
	t.Helper()

	cfg := &runtimeConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	rt := &TestRuntime{
		Store: db.Store,
	}

	var eventRouter event.EventRouter
	if cfg.withEvents {
		q := queue.NewMemoryQueue(100)
		ctx, cancel := context.WithCancel(context.Background())
		go q.Start(ctx)
		t.Cleanup(func() {
			q.Stop()
			cancel()
		})
		rt.Queue = q
		rt.Events = event.NewQueueRouter(q)
		eventRouter = rt.Events
	}

	rt.Artifacts = artifact.NewService(repo.Git, eventRouter, repo.Dir)
	rt.Projections = projection.NewService(repo.Git, db.Store, eventRouter, 1*time.Second)

	if cfg.withValidation {
		rt.Validator = validation.NewEngine(db.Store)
	}

	return rt
}
