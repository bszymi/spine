package actor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/domain"
)

func TestMockProvider_DefaultResponse(t *testing.T) {
	p := NewMockProvider()
	p.SetDefault(MockResponse{OutcomeID: "done", Summary: "all good"})

	result, err := p.Execute(context.Background(), AssignmentRequest{
		AssignmentID: "a-1",
		RunID:        "run-1",
		StepID:       "start",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.OutcomeID != "done" {
		t.Errorf("expected outcome done, got %s", result.OutcomeID)
	}
	if result.Summary != "all good" {
		t.Errorf("expected summary 'all good', got %s", result.Summary)
	}
	if result.ActorID != "mock-actor" {
		t.Errorf("expected actor mock-actor, got %s", result.ActorID)
	}
}

func TestMockProvider_PerStepResponse(t *testing.T) {
	p := NewMockProvider()
	p.SetDefault(MockResponse{OutcomeID: "default"})
	p.SetStepResponse("review", MockResponse{OutcomeID: "accepted"})

	// Default step.
	r1, _ := p.Execute(context.Background(), AssignmentRequest{StepID: "start"})
	if r1.OutcomeID != "default" {
		t.Errorf("expected default, got %s", r1.OutcomeID)
	}

	// Configured step.
	r2, _ := p.Execute(context.Background(), AssignmentRequest{StepID: "review"})
	if r2.OutcomeID != "accepted" {
		t.Errorf("expected accepted, got %s", r2.OutcomeID)
	}
}

func TestMockProvider_ErrorResponse(t *testing.T) {
	p := NewMockProvider()
	p.SetDefault(MockResponse{Err: errors.New("provider crash")})

	_, err := p.Execute(context.Background(), AssignmentRequest{StepID: "start"})
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "provider crash" {
		t.Errorf("expected 'provider crash', got %s", err.Error())
	}
}

func TestMockProvider_NoOutcome(t *testing.T) {
	p := NewMockProvider()
	// Default has empty OutcomeID.

	_, err := p.Execute(context.Background(), AssignmentRequest{StepID: "start"})
	if err == nil {
		t.Fatal("expected error for missing outcome")
	}
}

func TestMockProvider_Delay(t *testing.T) {
	p := NewMockProvider()
	p.SetDefault(MockResponse{OutcomeID: "done", Delay: 50 * time.Millisecond})

	start := time.Now()
	_, err := p.Execute(context.Background(), AssignmentRequest{StepID: "start"})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed < 40*time.Millisecond {
		t.Errorf("expected at least 40ms delay, got %v", elapsed)
	}
}

func TestMockProvider_DelayWithCancellation(t *testing.T) {
	p := NewMockProvider()
	p.SetDefault(MockResponse{OutcomeID: "done", Delay: 5 * time.Second})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := p.Execute(ctx, AssignmentRequest{StepID: "start"})
	if err == nil {
		t.Fatal("expected context error")
	}
}

func TestMockProvider_CanHandle_AllTypes(t *testing.T) {
	p := NewMockProvider() // no specific types → handles all
	if !p.CanHandle(domain.ActorTypeHuman) {
		t.Error("expected to handle human")
	}
	if !p.CanHandle(domain.ActorTypeAIAgent) {
		t.Error("expected to handle ai_agent")
	}
	if !p.CanHandle(domain.ActorTypeAutomated) {
		t.Error("expected to handle automated_system")
	}
}

func TestMockProvider_CanHandle_SpecificTypes(t *testing.T) {
	p := NewMockProvider(domain.ActorTypeAIAgent)

	if !p.CanHandle(domain.ActorTypeAIAgent) {
		t.Error("expected to handle ai_agent")
	}
	if p.CanHandle(domain.ActorTypeHuman) {
		t.Error("expected NOT to handle human")
	}
}

func TestMockProvider_Executed(t *testing.T) {
	p := NewMockProvider()
	p.SetDefault(MockResponse{OutcomeID: "done"})

	p.Execute(context.Background(), AssignmentRequest{StepID: "s1"})
	p.Execute(context.Background(), AssignmentRequest{StepID: "s2"})

	execs := p.Executed()
	if len(execs) != 2 {
		t.Fatalf("expected 2 executions, got %d", len(execs))
	}
	if execs[0].StepID != "s1" {
		t.Errorf("expected s1, got %s", execs[0].StepID)
	}
	if execs[1].StepID != "s2" {
		t.Errorf("expected s2, got %s", execs[1].StepID)
	}
}

func TestMockProvider_ArtifactsProduced(t *testing.T) {
	p := NewMockProvider()
	p.SetDefault(MockResponse{
		OutcomeID:         "done",
		ArtifactsProduced: []string{"output.md", "report.md"},
	})

	result, _ := p.Execute(context.Background(), AssignmentRequest{StepID: "start"})
	if len(result.ArtifactsProduced) != 2 {
		t.Fatalf("expected 2 artifacts, got %d", len(result.ArtifactsProduced))
	}
}

func TestMockProvider_ExecuteAI(t *testing.T) {
	p := NewMockProvider()
	p.SetDefault(MockResponse{OutcomeID: "completed", Summary: "AI output"})

	resp, err := p.ExecuteAI(context.Background(), AIPrompt{
		SystemPrompt: "You are a reviewer",
		UserPrompt:   "Review this code",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.OutcomeID != "completed" {
		t.Errorf("expected completed, got %s", resp.OutcomeID)
	}
	if resp.Content != "AI output" {
		t.Errorf("expected 'AI output', got %s", resp.Content)
	}
}

func TestMockProvider_ExecuteAI_Error(t *testing.T) {
	p := NewMockProvider()
	p.SetDefault(MockResponse{Err: errors.New("AI unavailable")})

	_, err := p.ExecuteAI(context.Background(), AIPrompt{})
	if err == nil {
		t.Fatal("expected error")
	}
}
