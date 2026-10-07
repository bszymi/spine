package workflow

import (
	"fmt"

	"github.com/bszymi/spine/core/domain"
)

// StepTrigger represents an event that may cause a StepExecution state transition.
type StepTrigger string

const (
	StepTriggerAssign        StepTrigger = "step.assign"
	StepTriggerTimeout       StepTrigger = "step.timeout"
	StepTriggerSkip          StepTrigger = "step.skip"
	StepTriggerAcknowledged  StepTrigger = "actor.acknowledged"
	StepTriggerActorUnavail  StepTrigger = "actor.unavailable"
	StepTriggerSubmit        StepTrigger = "step.submit"
	StepTriggerSubmitInvalid StepTrigger = "step.submit_invalid"
	StepTriggerBlocked       StepTrigger = "step.blocked"
	StepTriggerUnblocked     StepTrigger = "step.unblocked"
	// StepTriggerRunTerminated closes a live execution of a run that
	// was cancelled or failed (INIT-023 TASK-007).
	StepTriggerRunTerminated StepTrigger = "run.terminated"
)

// StepTransitionRequest describes a requested step state transition.
type StepTransitionRequest struct {
	Trigger               StepTrigger
	FailureClassification domain.FailureClassification // for failed transitions
	OutcomeID             string                       // for step.submit
	HasTimeoutOutcome     bool                         // if true, timeout routes to configured outcome (completed, not failed)
}

// StepTransitionResult describes the outcome of a step state transition.
type StepTransitionResult struct {
	FromStatus domain.StepExecutionStatus
	ToStatus   domain.StepExecutionStatus
	Trigger    StepTrigger
}

// EvaluateStepTransition determines the target state for a StepExecution given a trigger.
// Per Engine State Machine §3.2 and §3.4.
func EvaluateStepTransition(currentStatus domain.StepExecutionStatus, req StepTransitionRequest) (*StepTransitionResult, error) {
	// §3.4: Terminal states are immutable
	// Exception: duplicate step.submit on completed is idempotent (no-op)
	if currentStatus.IsTerminal() {
		if currentStatus == domain.StepStatusCompleted && req.Trigger == StepTriggerSubmit {
			return &StepTransitionResult{
				FromStatus: currentStatus,
				ToStatus:   currentStatus,
				Trigger:    req.Trigger,
			}, nil // idempotent no-op
		}
		return nil, domain.NewError(domain.ErrConflict,
			fmt.Sprintf("cannot transition from terminal step state %q", currentStatus))
	}

	result := &StepTransitionResult{
		FromStatus: currentStatus,
		Trigger:    req.Trigger,
	}

	// run.terminated closes any live execution of a run that was cancelled
	// or failed (INIT-023 TASK-007; engine-state-machine.md §2.2 "terminate
	// in-progress steps"). `skipped` is used because the step status check
	// constraint has no `cancelled` value; the run_terminated failure
	// classification on error_detail carries the cause. Terminal states
	// were refused above, so a terminated step is never re-terminated.
	if req.Trigger == StepTriggerRunTerminated {
		switch currentStatus {
		case domain.StepStatusWaiting, domain.StepStatusAssigned,
			domain.StepStatusInProgress, domain.StepStatusBlocked:
			result.ToStatus = domain.StepStatusSkipped
			return result, nil
		default:
			return nil, domain.NewError(domain.ErrConflict,
				fmt.Sprintf("invalid trigger %q for %s step", req.Trigger, currentStatus))
		}
	}

	switch currentStatus {
	case domain.StepStatusWaiting:
		return evaluateWaitingTransition(result, req)
	case domain.StepStatusAssigned:
		return evaluateAssignedTransition(result, req)
	case domain.StepStatusInProgress:
		return evaluateInProgressTransition(result, req)
	case domain.StepStatusBlocked:
		return evaluateBlockedTransition(result, req)
	default:
		return nil, domain.NewError(domain.ErrConflict,
			fmt.Sprintf("unknown step status %q", currentStatus))
	}
}

func evaluateWaitingTransition(result *StepTransitionResult, req StepTransitionRequest) (*StepTransitionResult, error) {
	switch req.Trigger {
	case StepTriggerAssign:
		result.ToStatus = domain.StepStatusAssigned
		return result, nil
	case StepTriggerTimeout:
		if req.HasTimeoutOutcome {
			result.ToStatus = domain.StepStatusCompleted
		} else {
			result.ToStatus = domain.StepStatusFailed
		}
		return result, nil
	case StepTriggerSkip:
		result.ToStatus = domain.StepStatusSkipped
		return result, nil
	default:
		return nil, domain.NewError(domain.ErrConflict,
			fmt.Sprintf("invalid trigger %q for waiting step", req.Trigger))
	}
}

func evaluateAssignedTransition(result *StepTransitionResult, req StepTransitionRequest) (*StepTransitionResult, error) {
	switch req.Trigger {
	case StepTriggerAcknowledged:
		result.ToStatus = domain.StepStatusInProgress
		return result, nil
	case StepTriggerTimeout:
		if req.HasTimeoutOutcome {
			result.ToStatus = domain.StepStatusCompleted
		} else {
			result.ToStatus = domain.StepStatusFailed
		}
		return result, nil
	case StepTriggerActorUnavail:
		result.ToStatus = domain.StepStatusWaiting
		return result, nil
	default:
		return nil, domain.NewError(domain.ErrConflict,
			fmt.Sprintf("invalid trigger %q for assigned step", req.Trigger))
	}
}

func evaluateInProgressTransition(result *StepTransitionResult, req StepTransitionRequest) (*StepTransitionResult, error) {
	switch req.Trigger {
	case StepTriggerSubmit:
		if req.OutcomeID == "" {
			return nil, domain.NewError(domain.ErrInvalidParams,
				"step.submit requires OutcomeID")
		}
		result.ToStatus = domain.StepStatusCompleted
		return result, nil
	case StepTriggerSubmitInvalid:
		result.ToStatus = domain.StepStatusFailed
		return result, nil
	case StepTriggerTimeout:
		if req.HasTimeoutOutcome {
			// Timeout with configured outcome → completed (routes to timeout_outcome)
			result.ToStatus = domain.StepStatusCompleted
		} else {
			result.ToStatus = domain.StepStatusFailed
		}
		return result, nil
	case StepTriggerActorUnavail:
		result.ToStatus = domain.StepStatusFailed
		return result, nil
	case StepTriggerBlocked:
		result.ToStatus = domain.StepStatusBlocked
		return result, nil
	default:
		return nil, domain.NewError(domain.ErrConflict,
			fmt.Sprintf("invalid trigger %q for in_progress step", req.Trigger))
	}
}

func evaluateBlockedTransition(result *StepTransitionResult, req StepTransitionRequest) (*StepTransitionResult, error) {
	switch req.Trigger {
	case StepTriggerUnblocked:
		result.ToStatus = domain.StepStatusInProgress
		return result, nil
	case StepTriggerTimeout:
		if req.HasTimeoutOutcome {
			result.ToStatus = domain.StepStatusCompleted
		} else {
			result.ToStatus = domain.StepStatusFailed
		}
		return result, nil
	default:
		return nil, domain.NewError(domain.ErrConflict,
			fmt.Sprintf("invalid trigger %q for blocked step", req.Trigger))
	}
}

// ShouldRetry determines if a failed step should be retried.
// Per Engine State Machine §3.3.
func ShouldRetry(attempt, retryLimit int, classification domain.FailureClassification) bool {
	if retryLimit <= 0 {
		return false
	}
	if attempt >= retryLimit {
		return false
	}
	return classification.IsRetryable()
}
