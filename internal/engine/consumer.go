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
	ctx          context.Context
	stopped      bool
	mu           sync.Mutex
}

// NewConsumer creates a queue consumer wired to the orchestrator.
func NewConsumer(q queue.Queue, orch *Orchestrator, providers ...ActorProvider) *Consumer {
	return &Consumer{
		queue:        q,
		orchestrator: orch,
		providers:    providers,
		ctx:          context.Background(),
	}
}

// Start subscribes to step_assignment messages and begins processing.
// It runs until the context is cancelled or Stop is called.
func (c *Consumer) Start(ctx context.Context) error {
	c.ctx, c.cancel = context.WithCancel(ctx)

	if err := c.queue.Subscribe(c.ctx, "step_assignment", c.handleAssignment); err != nil {
		return fmt.Errorf("subscribe to step_assignment: %w", err)
	}

	observe.Logger(c.ctx).Info("consumer started", "providers", len(c.providers))
	return nil
}

// Stop signals the consumer to stop, waits for in-flight work to finish,
// then cancels the context. This ensures providers complete with a live
// context before cleanup.
func (c *Consumer) Stop() {
	c.mu.Lock()
	c.stopped = true
	c.mu.Unlock()

	c.wg.Wait()
	if c.cancel != nil {
		c.cancel()
	}
}

// handleAssignment processes a single step_assignment queue entry.
// It runs the provider synchronously so the queue only acknowledges
// the entry after execution completes — preventing message loss on crash.
func (c *Consumer) handleAssignment(_ context.Context, entry queue.Entry) error {
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return fmt.Errorf("consumer stopped, rejecting assignment %s", entry.EntryID)
	}
	c.mu.Unlock()

	log := observe.Logger(c.ctx)

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

	// Execute synchronously so the queue only acknowledges after completion.
	// This prevents message loss if the process crashes mid-execution.
	c.wg.Add(1)
	defer c.wg.Done()

	return c.executeAndSubmit(c.ctx, log, req, provider)
}

// executeAndSubmit runs the provider and submits the result to the orchestrator.
func (c *Consumer) executeAndSubmit(ctx context.Context, log *slog.Logger, req actor.AssignmentRequest, provider ActorProvider) error {
	result, err := provider.Execute(ctx, req)
	if err != nil {
		log.Error("provider execution failed", "error", err)
		return fmt.Errorf("provider execution: %w", err)
	}

	if err := c.orchestrator.SubmitStepResult(ctx, req.AssignmentID, StepResult{
		OutcomeID:         result.OutcomeID,
		ArtifactsProduced: result.ArtifactsProduced,
	}); err != nil {
		log.Error("failed to submit step result", "error", err)
		return fmt.Errorf("submit step result: %w", err)
	}

	log.Info("assignment completed", "outcome_id", result.OutcomeID)
	return nil
}

// findProvider returns the first provider that can handle the given step type.
// NOTE: In v0.x, routing is based on step type only. Full execution mode
// routing (ai_only, human_only, hybrid) requires enriching AssignmentRequest
// with ExecutionMode, which is planned for a future task.
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
