package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/bszymi/spine/internal/actor"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/queue"
)

// ActorProvider handles the delivery of an assignment to a specific actor type
// and returns the result. Implementations exist per actor type (mock, AI, human).
type ActorProvider interface {
	// CanHandle returns true if this provider handles the given actor type.
	CanHandle(actorType domain.ActorType) bool

	// Execute delivers the assignment and returns the result synchronously.
	// For async actors (human), this would enqueue and return immediately
	// with a pending status; for sync actors (mock, automated), it blocks
	// until the result is available.
	Execute(ctx context.Context, req actor.AssignmentRequest) (*actor.AssignmentResult, error)
}

// Consumer subscribes to step_assignment messages from the queue,
// routes them to the appropriate ActorProvider based on actor type,
// and feeds results back into the orchestrator.
type Consumer struct {
	queue        queue.Queue
	orchestrator *Orchestrator
	providers    []ActorProvider
	wg           sync.WaitGroup
	cancel       context.CancelFunc
}

// NewConsumer creates a queue consumer wired to the orchestrator.
func NewConsumer(q queue.Queue, orch *Orchestrator, providers ...ActorProvider) *Consumer {
	return &Consumer{
		queue:        q,
		orchestrator: orch,
		providers:    providers,
	}
}

// Start subscribes to step_assignment messages and begins processing.
// It runs until the context is cancelled or Stop is called.
func (c *Consumer) Start(ctx context.Context) error {
	ctx, c.cancel = context.WithCancel(ctx)

	if err := c.queue.Subscribe(ctx, "step_assignment", c.handleAssignment); err != nil {
		return fmt.Errorf("subscribe to step_assignment: %w", err)
	}

	observe.Logger(ctx).Info("consumer started", "providers", len(c.providers))
	return nil
}

// Stop signals the consumer to stop and waits for in-flight work to drain.
func (c *Consumer) Stop() {
	if c.cancel != nil {
		c.cancel()
	}
	c.wg.Wait()
}

// handleAssignment processes a single step_assignment queue entry.
func (c *Consumer) handleAssignment(ctx context.Context, entry queue.Entry) error {
	log := observe.Logger(ctx)

	var req actor.AssignmentRequest
	if err := json.Unmarshal(entry.Payload, &req); err != nil {
		log.Error("failed to decode assignment", "entry_id", entry.EntryID, "error", err)
		return fmt.Errorf("decode assignment: %w", err)
	}

	log = log.With("assignment_id", req.AssignmentID, "step_id", req.StepID, "run_id", req.RunID)

	// Find a provider that can handle this actor type.
	provider := c.findProvider(req.StepType)
	if provider == nil {
		log.Warn("no provider for step type", "step_type", req.StepType)
		return fmt.Errorf("no provider for step type %s", req.StepType)
	}

	// Execute in a tracked goroutine for graceful shutdown.
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.executeAndSubmit(ctx, log, req, provider)
	}()

	return nil
}

// executeAndSubmit runs the provider and submits the result to the orchestrator.
func (c *Consumer) executeAndSubmit(ctx context.Context, log *slog.Logger, req actor.AssignmentRequest, provider ActorProvider) {
	result, err := provider.Execute(ctx, req)
	if err != nil {
		log.Error("provider execution failed", "error", err)
		return
	}

	if err := c.orchestrator.SubmitStepResult(ctx, req.AssignmentID, StepResult{
		OutcomeID:         result.OutcomeID,
		ArtifactsProduced: result.ArtifactsProduced,
	}); err != nil {
		log.Error("failed to submit step result", "error", err)
		return
	}

	log.Info("assignment completed", "outcome_id", result.OutcomeID)
}

// findProvider returns the first provider that can handle the given step type.
func (c *Consumer) findProvider(stepType domain.StepType) ActorProvider {
	// Map step type to actor type for provider lookup.
	var actorType domain.ActorType
	switch stepType {
	case domain.StepTypeAutomated:
		actorType = domain.ActorTypeAutomated
	case domain.StepTypeReview:
		actorType = domain.ActorTypeHuman
	case domain.StepTypeManual:
		actorType = domain.ActorTypeHuman
	default:
		actorType = domain.ActorTypeAIAgent
	}

	for _, p := range c.providers {
		if p.CanHandle(actorType) {
			return p
		}
	}
	return nil
}
