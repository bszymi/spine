package workflow_test

import (
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/workflow"
)

// ── Valid Transitions (§3.2) ──

func TestWaitingToAssigned(t *testing.T) {
	r, err := workflow.EvaluateStepTransition(domain.StepStatusWaiting, workflow.StepTransitionRequest{
		Trigger: workflow.StepTriggerAssign,
	})
	assertStepTransition(t, r, err, domain.StepStatusAssigned)
}

func TestWaitingToFailed(t *testing.T) {
	r, err := workflow.EvaluateStepTransition(domain.StepStatusWaiting, workflow.StepTransitionRequest{
		Trigger: workflow.StepTriggerTimeout,
	})
	assertStepTransition(t, r, err, domain.StepStatusFailed)
}

func TestWaitingToSkipped(t *testing.T) {
	r, err := workflow.EvaluateStepTransition(domain.StepStatusWaiting, workflow.StepTransitionRequest{
		Trigger: workflow.StepTriggerSkip,
	})
	assertStepTransition(t, r, err, domain.StepStatusSkipped)
}

func TestAssignedToInProgress(t *testing.T) {
	r, err := workflow.EvaluateStepTransition(domain.StepStatusAssigned, workflow.StepTransitionRequest{
		Trigger: workflow.StepTriggerAcknowledged,
	})
	assertStepTransition(t, r, err, domain.StepStatusInProgress)
}

func TestAssignedToFailed(t *testing.T) {
	r, err := workflow.EvaluateStepTransition(domain.StepStatusAssigned, workflow.StepTransitionRequest{
		Trigger: workflow.StepTriggerTimeout,
	})
	assertStepTransition(t, r, err, domain.StepStatusFailed)
}

func TestAssignedToWaiting(t *testing.T) {
	r, err := workflow.EvaluateStepTransition(domain.StepStatusAssigned, workflow.StepTransitionRequest{
		Trigger: workflow.StepTriggerActorUnavail,
	})
	assertStepTransition(t, r, err, domain.StepStatusWaiting)
}

func TestInProgressToCompleted(t *testing.T) {
	r, err := workflow.EvaluateStepTransition(domain.StepStatusInProgress, workflow.StepTransitionRequest{
		Trigger:   workflow.StepTriggerSubmit,
		OutcomeID: "accepted",
	})
	assertStepTransition(t, r, err, domain.StepStatusCompleted)
}

func TestInProgressToFailedInvalid(t *testing.T) {
	r, err := workflow.EvaluateStepTransition(domain.StepStatusInProgress, workflow.StepTransitionRequest{
		Trigger: workflow.StepTriggerSubmitInvalid,
	})
	assertStepTransition(t, r, err, domain.StepStatusFailed)
}

func TestInProgressToFailedTimeout(t *testing.T) {
	r, err := workflow.EvaluateStepTransition(domain.StepStatusInProgress, workflow.StepTransitionRequest{
		Trigger: workflow.StepTriggerTimeout,
	})
	assertStepTransition(t, r, err, domain.StepStatusFailed)
}

func TestInProgressToFailedActorUnavail(t *testing.T) {
	r, err := workflow.EvaluateStepTransition(domain.StepStatusInProgress, workflow.StepTransitionRequest{
		Trigger: workflow.StepTriggerActorUnavail,
	})
	assertStepTransition(t, r, err, domain.StepStatusFailed)
}

func TestInProgressToBlocked(t *testing.T) {
	r, err := workflow.EvaluateStepTransition(domain.StepStatusInProgress, workflow.StepTransitionRequest{
		Trigger: workflow.StepTriggerBlocked,
	})
	assertStepTransition(t, r, err, domain.StepStatusBlocked)
}

func TestBlockedToInProgress(t *testing.T) {
	r, err := workflow.EvaluateStepTransition(domain.StepStatusBlocked, workflow.StepTransitionRequest{
		Trigger: workflow.StepTriggerUnblocked,
	})
	assertStepTransition(t, r, err, domain.StepStatusInProgress)
}

func TestBlockedToFailed(t *testing.T) {
	r, err := workflow.EvaluateStepTransition(domain.StepStatusBlocked, workflow.StepTransitionRequest{
		Trigger: workflow.StepTriggerTimeout,
	})
	assertStepTransition(t, r, err, domain.StepStatusFailed)
}

// ── Invalid Transitions (§3.4) ──

func TestTerminalStepStatesImmutable(t *testing.T) {
	terminals := []domain.StepExecutionStatus{
		domain.StepStatusCompleted,
		domain.StepStatusFailed,
		domain.StepStatusSkipped,
	}
	triggers := []workflow.StepTrigger{
		workflow.StepTriggerAssign,
		workflow.StepTriggerTimeout,
		workflow.StepTriggerUnblocked,
	}
	for _, state := range terminals {
		for _, trigger := range triggers {
			// Skip idempotent case: completed + submit is a no-op
			if state == domain.StepStatusCompleted && trigger == workflow.StepTriggerSubmit {
				continue
			}
			_, err := workflow.EvaluateStepTransition(state, workflow.StepTransitionRequest{Trigger: trigger})
			if err == nil {
				t.Errorf("expected error for %s + %s", state, trigger)
			}
		}
	}
	// Verify non-completed terminals still reject submit
	for _, state := range []domain.StepExecutionStatus{domain.StepStatusFailed, domain.StepStatusSkipped} {
		_, err := workflow.EvaluateStepTransition(state, workflow.StepTransitionRequest{
			Trigger:   workflow.StepTriggerSubmit,
			OutcomeID: "test",
		})
		if err == nil {
			t.Errorf("expected error for %s + step.submit", state)
		}
	}
}

func TestWaitingRejectsInvalid(t *testing.T) {
	invalid := []workflow.StepTrigger{
		workflow.StepTriggerSubmit,
		workflow.StepTriggerAcknowledged,
		workflow.StepTriggerUnblocked,
	}
	for _, trigger := range invalid {
		_, err := workflow.EvaluateStepTransition(domain.StepStatusWaiting, workflow.StepTransitionRequest{Trigger: trigger})
		if err == nil {
			t.Errorf("expected error for waiting + %s", trigger)
		}
	}
}

func TestAssignedRejectsInvalid(t *testing.T) {
	invalid := []workflow.StepTrigger{
		workflow.StepTriggerAssign,
		workflow.StepTriggerSubmit,
		workflow.StepTriggerBlocked,
	}
	for _, trigger := range invalid {
		_, err := workflow.EvaluateStepTransition(domain.StepStatusAssigned, workflow.StepTransitionRequest{Trigger: trigger})
		if err == nil {
			t.Errorf("expected error for assigned + %s", trigger)
		}
	}
}

func TestInProgressRejectsInvalid(t *testing.T) {
	invalid := []workflow.StepTrigger{
		workflow.StepTriggerAssign,
		workflow.StepTriggerAcknowledged,
		workflow.StepTriggerSkip,
	}
	for _, trigger := range invalid {
		_, err := workflow.EvaluateStepTransition(domain.StepStatusInProgress, workflow.StepTransitionRequest{Trigger: trigger})
		if err == nil {
			t.Errorf("expected error for in_progress + %s", trigger)
		}
	}
}

func TestBlockedRejectsInvalid(t *testing.T) {
	invalid := []workflow.StepTrigger{
		workflow.StepTriggerAssign,
		workflow.StepTriggerSubmit,
		workflow.StepTriggerAcknowledged,
	}
	for _, trigger := range invalid {
		_, err := workflow.EvaluateStepTransition(domain.StepStatusBlocked, workflow.StepTransitionRequest{Trigger: trigger})
		if err == nil {
			t.Errorf("expected error for blocked + %s", trigger)
		}
	}
}

// ── Retry Logic (§3.3) ──

func TestShouldRetryTransient(t *testing.T) {
	if !workflow.ShouldRetry(1, 3, domain.FailureTransient) {
		t.Error("transient error with retries remaining should retry")
	}
}

func TestShouldRetryActorUnavailable(t *testing.T) {
	if !workflow.ShouldRetry(1, 3, domain.FailureActorUnavailable) {
		t.Error("actor_unavailable with retries remaining should retry")
	}
}

func TestShouldNotRetryPermanent(t *testing.T) {
	if workflow.ShouldRetry(1, 3, domain.FailurePermanent) {
		t.Error("permanent error should not retry")
	}
}

func TestShouldNotRetryGitConflict(t *testing.T) {
	if workflow.ShouldRetry(1, 3, domain.FailureGitConflict) {
		t.Error("git_conflict should not retry")
	}
}

func TestShouldNotRetryExhausted(t *testing.T) {
	if workflow.ShouldRetry(3, 3, domain.FailureTransient) {
		t.Error("should not retry when attempts exhausted")
	}
}

func TestShouldNotRetryNoLimit(t *testing.T) {
	if workflow.ShouldRetry(1, 0, domain.FailureTransient) {
		t.Error("should not retry with zero retry limit")
	}
}

// ── Timeout with configured outcome ──

func TestWaitingTimeoutWithOutcome(t *testing.T) {
	r, err := workflow.EvaluateStepTransition(domain.StepStatusWaiting, workflow.StepTransitionRequest{
		Trigger:           workflow.StepTriggerTimeout,
		HasTimeoutOutcome: true,
	})
	assertStepTransition(t, r, err, domain.StepStatusCompleted)
}

func TestInProgressTimeoutWithOutcome(t *testing.T) {
	r, err := workflow.EvaluateStepTransition(domain.StepStatusInProgress, workflow.StepTransitionRequest{
		Trigger:           workflow.StepTriggerTimeout,
		HasTimeoutOutcome: true,
	})
	assertStepTransition(t, r, err, domain.StepStatusCompleted)
}

// ── Idempotent submit on completed ──

func TestCompletedSubmitIdempotent(t *testing.T) {
	r, err := workflow.EvaluateStepTransition(domain.StepStatusCompleted, workflow.StepTransitionRequest{
		Trigger:   workflow.StepTriggerSubmit,
		OutcomeID: "accepted",
	})
	if err != nil {
		t.Fatalf("expected idempotent no-op, got error: %v", err)
	}
	if r.ToStatus != domain.StepStatusCompleted {
		t.Errorf("expected completed (no-op), got %s", r.ToStatus)
	}
}

// ── Empty OutcomeID rejected ──

func TestSubmitRejectsEmptyOutcomeID(t *testing.T) {
	_, err := workflow.EvaluateStepTransition(domain.StepStatusInProgress, workflow.StepTransitionRequest{
		Trigger:   workflow.StepTriggerSubmit,
		OutcomeID: "",
	})
	if err == nil {
		t.Fatal("expected error for empty OutcomeID on step.submit")
	}
}

// ── Transition metadata ──

func TestStepTransitionMetadata(t *testing.T) {
	r, err := workflow.EvaluateStepTransition(domain.StepStatusWaiting, workflow.StepTransitionRequest{
		Trigger: workflow.StepTriggerAssign,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.FromStatus != domain.StepStatusWaiting {
		t.Errorf("expected from=waiting, got %s", r.FromStatus)
	}
	if r.Trigger != workflow.StepTriggerAssign {
		t.Errorf("expected trigger=step.assign, got %s", r.Trigger)
	}
}

// ── Helper ──

func assertStepTransition(t *testing.T, r *workflow.StepTransitionResult, err error, expected domain.StepExecutionStatus) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.ToStatus != expected {
		t.Errorf("expected %s, got %s", expected, r.ToStatus)
	}
}
