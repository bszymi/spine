package workflow_test

import (
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/workflow"
)

// ── Valid Transitions (§2.2) ──

func TestPendingToActive(t *testing.T) {
	result, err := workflow.EvaluateRunTransition(domain.RunStatusPending, workflow.TransitionRequest{
		Trigger: workflow.TriggerActivate,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ToStatus != domain.RunStatusActive {
		t.Errorf("expected active, got %s", result.ToStatus)
	}
}

func TestActiveToActiveOnStepCompleted(t *testing.T) {
	result, err := workflow.EvaluateRunTransition(domain.RunStatusActive, workflow.TransitionRequest{
		Trigger:    workflow.TriggerStepCompleted,
		NextStepID: "execute",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ToStatus != domain.RunStatusActive {
		t.Errorf("expected active, got %s", result.ToStatus)
	}
}

func TestActiveToPaused(t *testing.T) {
	result, err := workflow.EvaluateRunTransition(domain.RunStatusActive, workflow.TransitionRequest{
		Trigger:     workflow.TriggerStepBlocked,
		PauseReason: "waiting for external API",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ToStatus != domain.RunStatusPaused {
		t.Errorf("expected paused, got %s", result.ToStatus)
	}
}

func TestActiveToCommitting(t *testing.T) {
	result, err := workflow.EvaluateRunTransition(domain.RunStatusActive, workflow.TransitionRequest{
		Trigger:    workflow.TriggerStepCompleted,
		NextStepID: "end",
		HasCommit:  true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ToStatus != domain.RunStatusCommitting {
		t.Errorf("expected committing, got %s", result.ToStatus)
	}
}

func TestActiveToCompletedNoCommit(t *testing.T) {
	result, err := workflow.EvaluateRunTransition(domain.RunStatusActive, workflow.TransitionRequest{
		Trigger:    workflow.TriggerStepCompleted,
		NextStepID: "end",
		HasCommit:  false,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ToStatus != domain.RunStatusCompleted {
		t.Errorf("expected completed, got %s", result.ToStatus)
	}
}

func TestActiveToCompletedEmptyNextStep(t *testing.T) {
	result, err := workflow.EvaluateRunTransition(domain.RunStatusActive, workflow.TransitionRequest{
		Trigger:    workflow.TriggerStepCompleted,
		NextStepID: "",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ToStatus != domain.RunStatusCompleted {
		t.Errorf("expected completed for empty next_step, got %s", result.ToStatus)
	}
}

func TestActiveToFailedStepFailed(t *testing.T) {
	result, err := workflow.EvaluateRunTransition(domain.RunStatusActive, workflow.TransitionRequest{
		Trigger: workflow.TriggerStepFailedPermanently,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ToStatus != domain.RunStatusFailed {
		t.Errorf("expected failed, got %s", result.ToStatus)
	}
}

func TestActiveToFailedDivergenceFailed(t *testing.T) {
	result, err := workflow.EvaluateRunTransition(domain.RunStatusActive, workflow.TransitionRequest{
		Trigger: workflow.TriggerDivergenceFailed,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ToStatus != domain.RunStatusFailed {
		t.Errorf("expected failed, got %s", result.ToStatus)
	}
}

func TestActiveToCancelled(t *testing.T) {
	result, err := workflow.EvaluateRunTransition(domain.RunStatusActive, workflow.TransitionRequest{
		Trigger: workflow.TriggerCancel,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ToStatus != domain.RunStatusCancelled {
		t.Errorf("expected cancelled, got %s", result.ToStatus)
	}
}

func TestPausedToActive(t *testing.T) {
	result, err := workflow.EvaluateRunTransition(domain.RunStatusPaused, workflow.TransitionRequest{
		Trigger: workflow.TriggerResume,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ToStatus != domain.RunStatusActive {
		t.Errorf("expected active, got %s", result.ToStatus)
	}
}

func TestPausedToCancelled(t *testing.T) {
	result, err := workflow.EvaluateRunTransition(domain.RunStatusPaused, workflow.TransitionRequest{
		Trigger: workflow.TriggerCancel,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ToStatus != domain.RunStatusCancelled {
		t.Errorf("expected cancelled, got %s", result.ToStatus)
	}
}

func TestCommittingToCompleted(t *testing.T) {
	result, err := workflow.EvaluateRunTransition(domain.RunStatusCommitting, workflow.TransitionRequest{
		Trigger: workflow.TriggerGitCommitSucceeded,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ToStatus != domain.RunStatusCompleted {
		t.Errorf("expected completed, got %s", result.ToStatus)
	}
}

func TestCommittingToCommittingRetry(t *testing.T) {
	result, err := workflow.EvaluateRunTransition(domain.RunStatusCommitting, workflow.TransitionRequest{
		Trigger: workflow.TriggerGitCommitFailedTrans,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ToStatus != domain.RunStatusCommitting {
		t.Errorf("expected committing (retry), got %s", result.ToStatus)
	}
}

func TestCommittingToFailed(t *testing.T) {
	result, err := workflow.EvaluateRunTransition(domain.RunStatusCommitting, workflow.TransitionRequest{
		Trigger: workflow.TriggerGitCommitFailedPerm,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ToStatus != domain.RunStatusFailed {
		t.Errorf("expected failed, got %s", result.ToStatus)
	}
}

// ── Invalid Transitions (§2.3) ──

func TestTerminalStatesAreImmutable(t *testing.T) {
	terminalStates := []domain.RunStatus{
		domain.RunStatusCompleted,
		domain.RunStatusFailed,
		domain.RunStatusCancelled,
	}
	triggers := []workflow.Trigger{
		workflow.TriggerActivate,
		workflow.TriggerStepCompleted,
		workflow.TriggerCancel,
		workflow.TriggerResume,
	}

	for _, state := range terminalStates {
		for _, trigger := range triggers {
			_, err := workflow.EvaluateRunTransition(state, workflow.TransitionRequest{Trigger: trigger})
			if err == nil {
				t.Errorf("expected error for %s + %s, got nil", state, trigger)
			}
		}
	}
}

func TestPendingRejectsInvalidTriggers(t *testing.T) {
	invalidTriggers := []workflow.Trigger{
		workflow.TriggerStepCompleted,
		workflow.TriggerCancel,
		workflow.TriggerResume,
		workflow.TriggerGitCommitSucceeded,
	}
	for _, trigger := range invalidTriggers {
		_, err := workflow.EvaluateRunTransition(domain.RunStatusPending, workflow.TransitionRequest{Trigger: trigger})
		if err == nil {
			t.Errorf("expected error for pending + %s", trigger)
		}
	}
}

func TestActiveRejectsInvalidTriggers(t *testing.T) {
	invalidTriggers := []workflow.Trigger{
		workflow.TriggerActivate,
		workflow.TriggerResume,
		workflow.TriggerGitCommitSucceeded,
	}
	for _, trigger := range invalidTriggers {
		_, err := workflow.EvaluateRunTransition(domain.RunStatusActive, workflow.TransitionRequest{Trigger: trigger})
		if err == nil {
			t.Errorf("expected error for active + %s", trigger)
		}
	}
}

func TestPausedRejectsInvalidTriggers(t *testing.T) {
	invalidTriggers := []workflow.Trigger{
		workflow.TriggerActivate,
		workflow.TriggerStepCompleted,
		workflow.TriggerGitCommitSucceeded,
	}
	for _, trigger := range invalidTriggers {
		_, err := workflow.EvaluateRunTransition(domain.RunStatusPaused, workflow.TransitionRequest{Trigger: trigger})
		if err == nil {
			t.Errorf("expected error for paused + %s", trigger)
		}
	}
}

func TestCommittingRejectsInvalidTriggers(t *testing.T) {
	invalidTriggers := []workflow.Trigger{
		workflow.TriggerActivate,
		workflow.TriggerStepCompleted,
		workflow.TriggerCancel,
		workflow.TriggerResume,
	}
	for _, trigger := range invalidTriggers {
		_, err := workflow.EvaluateRunTransition(domain.RunStatusCommitting, workflow.TransitionRequest{Trigger: trigger})
		if err == nil {
			t.Errorf("expected error for committing + %s", trigger)
		}
	}
}

// ── Transition metadata ──

func TestTransitionResultMetadata(t *testing.T) {
	result, err := workflow.EvaluateRunTransition(domain.RunStatusPending, workflow.TransitionRequest{
		Trigger: workflow.TriggerActivate,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.FromStatus != domain.RunStatusPending {
		t.Errorf("expected from=pending, got %s", result.FromStatus)
	}
	if result.Trigger != workflow.TriggerActivate {
		t.Errorf("expected trigger=run.activate, got %s", result.Trigger)
	}
}

func TestValidRunTransitions(t *testing.T) {
	transitions := workflow.ValidRunTransitions()
	if len(transitions) != 13 {
		t.Errorf("expected 13 valid transitions, got %d", len(transitions))
	}
}
