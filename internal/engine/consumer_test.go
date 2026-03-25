package engine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/actor"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/queue"
)

// ── Test actor provider ──

type testProvider struct {
	canHandle bool
	result    *actor.AssignmentResult
	err       error
	executed  []actor.AssignmentRequest
}

func (p *testProvider) CanHandle(_ domain.ActorType) bool {
	return p.canHandle
}

func (p *testProvider) Execute(_ context.Context, req actor.AssignmentRequest) (*actor.AssignmentResult, error) {
	p.executed = append(p.executed, req)
	return p.result, p.err
}

// ── Consumer tests ──

func TestNewConsumer(t *testing.T) {
	q := queue.NewMemoryQueue(10)
	orch := &Orchestrator{}
	provider := &testProvider{canHandle: true}

	c := NewConsumer(q, orch, provider)
	if c == nil {
		t.Fatal("expected non-nil consumer")
	}
	if c.queue != q {
		t.Error("expected queue to be stored")
	}
	if len(c.providers) != 1 {
		t.Errorf("expected 1 provider, got %d", len(c.providers))
	}
}

func TestConsumer_StartStop(t *testing.T) {
	q := queue.NewMemoryQueue(10)
	orch := &Orchestrator{}
	c := NewConsumer(q, orch)

	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Stop should complete without hanging.
	c.Stop()
}

func TestConsumer_HandleAssignment(t *testing.T) {
	provider := &testProvider{
		canHandle: true,
		result: &actor.AssignmentResult{
			OutcomeID: "done",
		},
	}

	// Build a minimal orchestrator with in-memory store for result submission.
	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {
				RunID:           "run-1",
				Status:          domain.RunStatusActive,
				TraceID:         "trace-1234567890ab",
				WorkflowPath:    "workflows/test.yaml",
				WorkflowVersion: "abc",
			},
		},
		createdSteps: []*domain.StepExecution{
			{
				ExecutionID: "run-1-start-1",
				RunID:       "run-1",
				StepID:      "start",
				Status:      domain.StepStatusAssigned,
				Attempt:     1,
			},
		},
	}

	wfLoader := &mockWorkflowLoader{
		wfDef: &domain.WorkflowDefinition{
			ID:        "wf-test",
			EntryStep: "start",
			Steps: []domain.StepDefinition{
				{
					ID:   "start",
					Name: "Start",
					Type: domain.StepTypeAutomated,
					Outcomes: []domain.OutcomeDefinition{
						{ID: "done", Name: "Done"},
					},
				},
			},
		},
	}

	orch := &Orchestrator{
		workflows: &mockWorkflowResolver{},
		store:     store,
		actors:    &stubActorAssigner{},
		artifacts: &mockArtifactReader{},
		events:    &mockEventEmitter{},
		git:       &stubGitOperator{},
		wfLoader:  wfLoader,
	}

	q := queue.NewMemoryQueue(10)
	consumer := NewConsumer(q, orch, provider)

	ctx := context.Background()
	if err := consumer.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Publish a step assignment.
	payload, _ := json.Marshal(actor.AssignmentRequest{
		AssignmentID: "run-1-start-1",
		RunID:        "run-1",
		StepID:       "start",
		StepType:     domain.StepTypeAutomated,
	})

	if err := q.Publish(ctx, queue.Entry{
		EntryID:   "run-1-start-1",
		EntryType: "step_assignment",
		Payload:   payload,
	}); err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	// Start queue processing.
	go q.Start(ctx)
	defer q.Stop()

	// Wait for the consumer to process.
	time.Sleep(200 * time.Millisecond)
	consumer.Stop()

	// Provider should have been called.
	if len(provider.executed) != 1 {
		t.Fatalf("expected 1 execution, got %d", len(provider.executed))
	}
	if provider.executed[0].StepID != "start" {
		t.Errorf("expected step start, got %s", provider.executed[0].StepID)
	}

	// Step should be completed.
	step, _ := store.GetStepExecution(ctx, "run-1-start-1")
	if step.Status != domain.StepStatusCompleted {
		t.Errorf("expected step completed, got %s", step.Status)
	}
}

func TestConsumer_FindProvider(t *testing.T) {
	automatedProvider := &testProvider{canHandle: false}
	aiProvider := &testProvider{canHandle: true}

	c := &Consumer{providers: []ActorProvider{automatedProvider, aiProvider}}

	// Should find the second provider (ai).
	p := c.findProvider(domain.StepTypeAutomated)
	if p != aiProvider {
		t.Error("expected ai provider to be selected")
	}
}

func TestConsumer_FindProvider_NoMatch(t *testing.T) {
	provider := &testProvider{canHandle: false}
	c := &Consumer{providers: []ActorProvider{provider}}

	p := c.findProvider(domain.StepTypeAutomated)
	if p != nil {
		t.Error("expected nil when no provider matches")
	}
}

func TestConsumer_HandleAssignment_InvalidPayload(t *testing.T) {
	c := NewConsumer(queue.NewMemoryQueue(1), &Orchestrator{}, &testProvider{canHandle: true})

	err := c.handleAssignment(context.Background(), queue.Entry{
		EntryID:   "bad-1",
		EntryType: "step_assignment",
		Payload:   []byte("not json"),
	})
	if err == nil {
		t.Fatal("expected error for invalid payload")
	}
}

func TestConsumer_HandleAssignment_NoProvider(t *testing.T) {
	c := NewConsumer(queue.NewMemoryQueue(1), &Orchestrator{}, &testProvider{canHandle: false})

	payload, _ := json.Marshal(actor.AssignmentRequest{
		AssignmentID: "a-1",
		StepType:     domain.StepTypeManual,
	})

	err := c.handleAssignment(context.Background(), queue.Entry{
		EntryID:   "a-1",
		EntryType: "step_assignment",
		Payload:   payload,
	})
	if err == nil {
		t.Fatal("expected error when no provider matches")
	}
}
