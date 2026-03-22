package actor_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/actor"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/event"
	"github.com/bszymi/spine/internal/queue"
)

// ── Fake Event Router ──

type fakeEventRouter struct {
	events []domain.Event
}

func (f *fakeEventRouter) Emit(_ context.Context, evt domain.Event) error {
	f.events = append(f.events, evt)
	return nil
}

func (f *fakeEventRouter) Subscribe(_ context.Context, _ domain.EventType, _ event.EventHandler) error {
	return nil
}

// ── Fake Queue ──

type fakeQueue struct {
	entries []queue.Entry
}

func (f *fakeQueue) Publish(_ context.Context, entry queue.Entry) error {
	f.entries = append(f.entries, entry)
	return nil
}

func (f *fakeQueue) Subscribe(_ context.Context, _ string, _ queue.EntryHandler) error { return nil }
func (f *fakeQueue) Acknowledge(_ context.Context, _ string) error                     { return nil }

// ── Fake Store (extending from actor_test.go fakeStore) ──

type gatewayFakeStore struct {
	fakeStore
	stepExecs map[string]*domain.StepExecution
}

func newGatewayFakeStore() *gatewayFakeStore {
	return &gatewayFakeStore{
		fakeStore: fakeStore{actors: make(map[string]*domain.Actor)},
		stepExecs: make(map[string]*domain.StepExecution),
	}
}

func (f *gatewayFakeStore) GetStepExecution(_ context.Context, execID string) (*domain.StepExecution, error) {
	exec, ok := f.stepExecs[execID]
	if !ok {
		return nil, domain.NewError(domain.ErrNotFound, "not found")
	}
	return exec, nil
}

func (f *gatewayFakeStore) UpdateStepExecution(_ context.Context, exec *domain.StepExecution) error {
	if _, ok := f.stepExecs[exec.ExecutionID]; !ok {
		return domain.NewError(domain.ErrNotFound, "not found")
	}
	f.stepExecs[exec.ExecutionID] = exec
	return nil
}

// ── ValidateResult Tests ──

func sampleRequest() actor.AssignmentRequest {
	return actor.AssignmentRequest{
		AssignmentID: "assign-1",
		RunID:        "run-1",
		TraceID:      "trace-1",
		StepID:       "step-1",
		StepName:     "Review",
		ActorID:      "actor-1",
		Constraints: actor.AssignmentConstraints{
			ExpectedOutcomes: []string{"approved", "rejected"},
		},
	}
}

func TestValidateResultSuccess(t *testing.T) {
	req := sampleRequest()
	result := actor.AssignmentResult{
		AssignmentID: "assign-1",
		RunID:        "run-1",
		ActorID:      "actor-1",
		OutcomeID:    "approved",
	}
	if err := actor.ValidateResult(req, result); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateResultMismatchAssignment(t *testing.T) {
	req := sampleRequest()
	result := actor.AssignmentResult{
		AssignmentID: "wrong",
		RunID:        "run-1",
		ActorID:      "actor-1",
		OutcomeID:    "approved",
	}
	if err := actor.ValidateResult(req, result); err == nil {
		t.Error("expected error for mismatched assignment_id")
	}
}

func TestValidateResultMismatchRun(t *testing.T) {
	req := sampleRequest()
	result := actor.AssignmentResult{
		AssignmentID: "assign-1",
		RunID:        "wrong",
		ActorID:      "actor-1",
		OutcomeID:    "approved",
	}
	if err := actor.ValidateResult(req, result); err == nil {
		t.Error("expected error for mismatched run_id")
	}
}

func TestValidateResultMismatchActor(t *testing.T) {
	req := sampleRequest()
	result := actor.AssignmentResult{
		AssignmentID: "assign-1",
		RunID:        "run-1",
		ActorID:      "wrong",
		OutcomeID:    "approved",
	}
	if err := actor.ValidateResult(req, result); err == nil {
		t.Error("expected error for mismatched actor_id")
	}
}

func TestValidateResultEmptyOutcome(t *testing.T) {
	req := sampleRequest()
	result := actor.AssignmentResult{
		AssignmentID: "assign-1",
		RunID:        "run-1",
		ActorID:      "actor-1",
		OutcomeID:    "",
	}
	if err := actor.ValidateResult(req, result); err == nil {
		t.Error("expected error for empty outcome_id")
	}
}

func TestValidateResultInvalidOutcome(t *testing.T) {
	req := sampleRequest()
	result := actor.AssignmentResult{
		AssignmentID: "assign-1",
		RunID:        "run-1",
		ActorID:      "actor-1",
		OutcomeID:    "unknown_outcome",
	}
	if err := actor.ValidateResult(req, result); err == nil {
		t.Error("expected error for invalid outcome_id")
	}
}

func TestValidateResultNoConstraints(t *testing.T) {
	req := actor.AssignmentRequest{
		AssignmentID: "assign-1",
		RunID:        "run-1",
		ActorID:      "actor-1",
	}
	result := actor.AssignmentResult{
		AssignmentID: "assign-1",
		RunID:        "run-1",
		ActorID:      "actor-1",
		OutcomeID:    "anything",
	}
	if err := actor.ValidateResult(req, result); err != nil {
		t.Fatalf("unexpected error with no constraints: %v", err)
	}
}

// ── Gateway Tests ──

func TestDeliverAssignment(t *testing.T) {
	fs := newGatewayFakeStore()
	events := &fakeEventRouter{}
	q := &fakeQueue{}
	svc := actor.NewService(fs)
	gw := actor.NewGateway(fs, events, q, svc)

	req := sampleRequest()
	if err := gw.DeliverAssignment(context.Background(), req); err != nil {
		t.Fatalf("deliver: %v", err)
	}

	if len(q.entries) != 1 {
		t.Fatalf("expected 1 queue entry, got %d", len(q.entries))
	}
	if q.entries[0].EntryType != "step_assignment" {
		t.Errorf("expected step_assignment, got %s", q.entries[0].EntryType)
	}
	if q.entries[0].IdempotencyKey != "assign-1" {
		t.Errorf("expected idempotency key assign-1, got %s", q.entries[0].IdempotencyKey)
	}

	if len(events.events) != 1 || events.events[0].Type != domain.EventStepAssigned {
		t.Error("expected step_assigned event")
	}
}

func TestProcessResultSuccess(t *testing.T) {
	fs := newGatewayFakeStore()
	fs.stepExecs["assign-1"] = &domain.StepExecution{
		ExecutionID: "assign-1",
		Status:      domain.StepStatusInProgress,
	}
	events := &fakeEventRouter{}
	q := &fakeQueue{}
	svc := actor.NewService(fs)
	gw := actor.NewGateway(fs, events, q, svc)

	req := sampleRequest()
	result := actor.AssignmentResult{
		AssignmentID: "assign-1",
		RunID:        "run-1",
		ActorID:      "actor-1",
		OutcomeID:    "approved",
		Summary:      "Looks good",
	}

	if err := gw.ProcessResult(context.Background(), req, result); err != nil {
		t.Fatalf("process: %v", err)
	}

	if len(events.events) != 1 || events.events[0].Type != domain.EventStepCompleted {
		t.Error("expected step_completed event")
	}
}

func TestProcessResultDuplicate(t *testing.T) {
	fs := newGatewayFakeStore()
	fs.stepExecs["assign-1"] = &domain.StepExecution{
		ExecutionID: "assign-1",
		Status:      domain.StepStatusCompleted, // already completed
	}
	events := &fakeEventRouter{}
	q := &fakeQueue{}
	svc := actor.NewService(fs)
	gw := actor.NewGateway(fs, events, q, svc)

	req := sampleRequest()
	result := actor.AssignmentResult{
		AssignmentID: "assign-1",
		RunID:        "run-1",
		ActorID:      "actor-1",
		OutcomeID:    "approved",
	}

	// Should succeed (idempotent)
	if err := gw.ProcessResult(context.Background(), req, result); err != nil {
		t.Fatalf("duplicate should be idempotent: %v", err)
	}

	// No event emitted for duplicate
	if len(events.events) != 0 {
		t.Error("expected no events for duplicate submission")
	}
}

func TestProcessResultInvalidResult(t *testing.T) {
	fs := newGatewayFakeStore()
	events := &fakeEventRouter{}
	q := &fakeQueue{}
	svc := actor.NewService(fs)
	gw := actor.NewGateway(fs, events, q, svc)

	req := sampleRequest()
	result := actor.AssignmentResult{
		AssignmentID: "wrong-id", // mismatch
		RunID:        "run-1",
		ActorID:      "actor-1",
		OutcomeID:    "approved",
	}

	if err := gw.ProcessResult(context.Background(), req, result); err == nil {
		t.Error("expected error for invalid result")
	}
}

// ── Prompt Tests ──

func TestBuildPrompt(t *testing.T) {
	req := actor.AssignmentRequest{
		StepID:   "step-1",
		StepName: "Code Review",
		Context: actor.AssignmentContext{
			TaskPath:        "initiatives/test/task.md",
			Instructions:    "Review the code changes",
			RequiredInputs:  []string{"src/main.go"},
			RequiredOutputs: []string{"review-notes.md"},
		},
		Constraints: actor.AssignmentConstraints{
			ExpectedOutcomes: []string{"approved", "changes_requested"},
			Timeout:          "30m",
		},
	}

	prompt := actor.BuildPrompt(req, "You are a code reviewer.")

	if prompt.SystemPrompt != "You are a code reviewer." {
		t.Errorf("expected system prompt, got %s", prompt.SystemPrompt)
	}
	if !strings.Contains(prompt.UserPrompt, "Code Review") {
		t.Error("expected step name in prompt")
	}
	if !strings.Contains(prompt.UserPrompt, "Review the code changes") {
		t.Error("expected instructions in prompt")
	}
	if !strings.Contains(prompt.UserPrompt, "src/main.go") {
		t.Error("expected required inputs in prompt")
	}
	if !strings.Contains(prompt.UserPrompt, "review-notes.md") {
		t.Error("expected required outputs in prompt")
	}
	if !strings.Contains(prompt.UserPrompt, "approved") {
		t.Error("expected expected outcomes in prompt")
	}
	if !strings.Contains(prompt.UserPrompt, "30m") {
		t.Error("expected timeout in prompt")
	}
}

func TestBuildPromptMinimal(t *testing.T) {
	req := actor.AssignmentRequest{
		StepID:   "step-1",
		StepName: "Simple Step",
		Context: actor.AssignmentContext{
			TaskPath: "test/task.md",
		},
	}

	prompt := actor.BuildPrompt(req, "")
	if prompt.SystemPrompt != "" {
		t.Error("expected empty system prompt")
	}
	if !strings.Contains(prompt.UserPrompt, "Simple Step") {
		t.Error("expected step name in minimal prompt")
	}
}

// suppress unused import
var _ = time.Now
var _ json.RawMessage
