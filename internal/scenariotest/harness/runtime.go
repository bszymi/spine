package harness

import (
	"context"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/actor"
	"github.com/bszymi/spine/internal/artifact"
	"github.com/bszymi/spine/internal/engine"
	"github.com/bszymi/spine/internal/event"
	"github.com/bszymi/spine/internal/projection"
	"github.com/bszymi/spine/internal/queue"
	"github.com/bszymi/spine/internal/store"
	"github.com/bszymi/spine/internal/validation"
	"github.com/bszymi/spine/internal/workflow"
)

// TestRuntime wires Spine services for in-process scenario execution.
// Services are constructed identically to production (cmd/spine/main.go),
// ensuring test fidelity. The runtime is configured for synchronous,
// deterministic behaviour — no background polling or async event delivery.
type TestRuntime struct {
	Store        *store.PostgresStore
	Artifacts    *artifact.Service
	Projections  *projection.Service
	Validator    *validation.Engine
	Events       event.EventRouter
	Queue        *queue.MemoryQueue
	Orchestrator *engine.Orchestrator
}

// RuntimeOption configures optional components of the TestRuntime.
type RuntimeOption func(*runtimeConfig)

type runtimeConfig struct {
	withEvents       bool
	withValidation   bool
	withOrchestrator bool
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

// WithOrchestrator enables the workflow engine orchestrator.
// Implies WithEvents (the orchestrator needs event emission).
func WithOrchestrator() RuntimeOption {
	return func(c *runtimeConfig) {
		c.withOrchestrator = true
		c.withEvents = true
	}
}

// NewTestRuntime creates a TestRuntime wired to the given repo and database.
// By default, event routers are nil and validation is disabled.
// Use WithEvents(), WithValidation(), and WithOrchestrator() to enable
// optional components.
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

	if cfg.withOrchestrator {
		wfProvider := workflow.NewProjectionProviderFromListFn(func(ctx context.Context) ([]workflow.WorkflowProjection, error) {
			projs, err := db.Store.ListActiveWorkflowProjections(ctx)
			if err != nil {
				return nil, err
			}
			result := make([]workflow.WorkflowProjection, len(projs))
			for i := range projs {
				result[i] = workflow.WorkflowProjection{
					WorkflowPath: projs[i].WorkflowPath,
					WorkflowID:   projs[i].WorkflowID,
					Name:         projs[i].Name,
					Version:      projs[i].Version,
					Status:       projs[i].Status,
					AppliesTo:    projs[i].AppliesTo,
					Definition:   projs[i].Definition,
					SourceCommit: projs[i].SourceCommit,
				}
			}
			return result, nil
		})
		resolver := engine.NewBindingResolver(wfProvider, repo.Git)
		wfLoader := engine.NewGitWorkflowLoader(repo.Git)

		orch, err := engine.New(
			resolver,
			db.Store,
			&noopActorAssigner{},
			rt.Artifacts,
			eventRouter,
			repo.Git,
			wfLoader,
		)
		if err != nil {
			t.Fatalf("create orchestrator: %v", err)
		}
		if rt.Validator != nil {
			orch.WithValidator(rt.Validator)
		}
		orch.WithArtifactWriter(rt.Artifacts)
		orch.WithBlockingStore(db.Store)
		rt.Orchestrator = orch
	}

	return rt
}

// noopActorAssigner is a no-op implementation of engine.ActorAssigner
// for scenario testing. It accepts assignments without delivering them,
// allowing scenarios to manually submit step results.
type noopActorAssigner struct{}

func (n *noopActorAssigner) DeliverAssignment(_ context.Context, _ actor.AssignmentRequest) error {
	return nil
}

func (n *noopActorAssigner) ProcessResult(_ context.Context, _ actor.AssignmentRequest, _ actor.AssignmentResult) error {
	return nil
}
