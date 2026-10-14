package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/bszymi/spine/core/actor"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/observe"
	"github.com/bszymi/spine/core/queue"
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
	// submitting counts, per assignment id, the deliveries of this
	// consumer that are inside SubmitStepResult (INIT-023 TASK-008 review
	// round 1). Guarded by mu; see executeAndSubmit.
	submitting map[string]int
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

	// ActorID is the actor the assignment was delivered to (INIT-023
	// TASK-008): a step rebound to another actor while the provider ran is
	// refused (ErrForbidden) instead of completed on the new owner's
	// behalf.
	leave := c.enterSubmit(req.AssignmentID)
	err = c.orchestrator.SubmitStepResult(ctx, req.AssignmentID, StepResult{
		OutcomeID:         result.OutcomeID,
		ArtifactsProduced: result.ArtifactsProduced,
		ActorID:           req.ActorID,
	})
	leave()
	// Another delivery of this assignment still inside SubmitStepResult
	// may be the winner, between its terminal write and its routing.
	// Checked after leaving, so of several deliveries that lost at once the
	// last one out sees none and recovers.
	routingElsewhere := err != nil && c.submittingNow(req.AssignmentID)
	if err != nil {
		// A lost race, or an ownership refusal for an execution rebound
		// while the provider ran (codex review, round 5): when the
		// execution is already resolved there is nothing left to submit.
		if errors.Is(err, ErrStepStateChanged) || isForbidden(err) {
			// Only an execution someone else already resolved (terminal)
			// means there is nothing left to do (codex review, round 3: a
			// concurrent acknowledgment by the same actor is absorbed by
			// SubmitStepResult itself and never lands here). A live
			// execution that moved — released, rebound — keeps the error:
			// the result was not applied.
			if cur, rerr := c.orchestrator.store.GetStepExecution(ctx, req.AssignmentID); rerr == nil && cur.Status.IsTerminal() {
				// A completed execution whose routing never finished (a
				// redelivery after the first submit completed it and then
				// failed to create the next execution) is routed now (codex
				// review, round 4); RecoverCompletedStep refuses — a
				// conflict, nothing changed — when it was routed.
				//
				// Not while another delivery of this assignment is still
				// submitting in this consumer (review round 1): that one
				// may have completed the execution and be about to route
				// it, and recovery cannot tell it from a crash — its checks
				// all pass, it creates the successor, and the winner's
				// advanceToNextStep then numbers a second attempt from its
				// own later listing. The winner routes; if its routing
				// fails it returns the error, and the redelivery that
				// follows recovers. Skipping is safe for a delivery that is
				// merely racing too: it loses the same way and, last out,
				// recovers. A winner in another process, or outside the
				// consumer, is not seen (KNOWN-LIMITATIONS "Engine State
				// Writes …" 7).
				if cur.Status == domain.StepStatusCompleted && routingElsewhere {
					log.Info("step result not applied; another delivery of this assignment is routing it",
						"status", cur.Status, "error", err)
					return nil
				}
				if cur.Status == domain.StepStatusCompleted {
					if rerr := c.orchestrator.RecoverCompletedStep(ctx, req.AssignmentID); rerr != nil && !isConflict(rerr) {
						log.Error("completed step could not be recovered", "error", rerr)
						return fmt.Errorf("recover completed step: %w", rerr)
					}
				}
				log.Info("step result not applied; the execution was resolved concurrently",
					"status", cur.Status, "error", err)
				return nil
			}
		}
		log.Error("failed to submit step result", "error", err)
		return fmt.Errorf("submit step result: %w", err)
	}

	log.Info("assignment completed", "outcome_id", result.OutcomeID)
	return nil
}

// enterSubmit records that a delivery of assignmentID is inside
// SubmitStepResult; the returned func records that it left.
func (c *Consumer) enterSubmit(assignmentID string) func() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.submitting == nil {
		c.submitting = map[string]int{}
	}
	c.submitting[assignmentID]++
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.submitting[assignmentID]--
		if c.submitting[assignmentID] <= 0 {
			delete(c.submitting, assignmentID)
		}
	}
}

// submittingNow reports whether a delivery of assignmentID is inside
// SubmitStepResult (the caller has already left).
func (c *Consumer) submittingNow(assignmentID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.submitting[assignmentID] > 0
}

// isForbidden reports whether err is a domain ErrForbidden.
func isForbidden(err error) bool {
	var se *domain.SpineError
	return errors.As(err, &se) && se.Code == domain.ErrForbidden
}

// isConflict reports whether err is a domain ErrConflict.
func isConflict(err error) bool {
	var se *domain.SpineError
	return errors.As(err, &se) && se.Code == domain.ErrConflict
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
