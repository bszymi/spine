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

func TestActiveRejectsEmptyNextStep(t *testing.T) {
	_, err := workflow.EvaluateRunTransition(domain.RunStatusActive, workflow.TransitionRequest{
		Trigger:    workflow.TriggerStepCompleted,
		NextStepID: "",
	})
	if err == nil {
		t.Fatal("expected error for empty NextStepID on step.completed")
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
	if len(transitions) != 18 {
		t.Errorf("expected 18 valid transitions, got %d", len(transitions))
	}
}

// TestEvaluateRunTransition_CommittingToPartiallyMerged pins the
// committing → partially-merged path that EPIC-005 TASK-003 added.
// The trigger fires when the primary repo merged but at least one
// affected code repo ended in a permanent failure.
func TestEvaluateRunTransition_CommittingToPartiallyMerged(t *testing.T) {
	result, err := workflow.EvaluateRunTransition(domain.RunStatusCommitting, workflow.TransitionRequest{
		Trigger: workflow.TriggerCodeRepoPartialFailure,
	})
	if err != nil {
		t.Fatalf("EvaluateRunTransition: %v", err)
	}
	if result.ToStatus != domain.RunStatusPartiallyMerged {
		t.Errorf("ToStatus: got %s, want partially-merged", result.ToStatus)
	}
}

// TestEvaluateRunTransition_PartiallyMergedToCommitting verifies the
// resume path the scheduler uses to retry a partially-merged run.
func TestEvaluateRunTransition_PartiallyMergedToCommitting(t *testing.T) {
	result, err := workflow.EvaluateRunTransition(domain.RunStatusPartiallyMerged, workflow.TransitionRequest{
		Trigger: workflow.TriggerRetryPartialMerge,
	})
	if err != nil {
		t.Fatalf("EvaluateRunTransition: %v", err)
	}
	if result.ToStatus != domain.RunStatusCommitting {
		t.Errorf("ToStatus: got %s, want committing", result.ToStatus)
	}
}

// TestEvaluateRunTransition_PartiallyMergedToCancelled confirms the
// operator-cancel path is allowed from partially-merged.
func TestEvaluateRunTransition_PartiallyMergedToCancelled(t *testing.T) {
	result, err := workflow.EvaluateRunTransition(domain.RunStatusPartiallyMerged, workflow.TransitionRequest{
		Trigger: workflow.TriggerCancel,
	})
	if err != nil {
		t.Fatalf("EvaluateRunTransition: %v", err)
	}
	if result.ToStatus != domain.RunStatusCancelled {
		t.Errorf("ToStatus: got %s, want cancelled", result.ToStatus)
	}
}

// TestEvaluateRunTransition_PartiallyMergedRejectsUnknownTrigger
// confirms only the documented triggers transition from
// partially-merged.
func TestEvaluateRunTransition_PartiallyMergedRejectsUnknownTrigger(t *testing.T) {
	if _, err := workflow.EvaluateRunTransition(domain.RunStatusPartiallyMerged, workflow.TransitionRequest{
		Trigger: workflow.TriggerActivate,
	}); err == nil {
		t.Error("expected error for invalid trigger from partially-merged")
	}
}
