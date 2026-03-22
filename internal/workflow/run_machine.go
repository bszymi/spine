package workflow

import (
	"fmt"

	"github.com/bszymi/spine/internal/domain"
)

// Trigger represents an event that may cause a Run state transition.
type Trigger string

const (
	TriggerActivate              Trigger = "run.activate"
	TriggerStepCompleted         Trigger = "step.completed"
	TriggerStepBlocked           Trigger = "step.blocked"
	TriggerStepFailedPermanently Trigger = "step.failed_permanently"
	TriggerDivergenceFailed      Trigger = "divergence.failed"
	TriggerCancel                Trigger = "run.cancel"
	TriggerResume                Trigger = "run.resume"
	TriggerGitCommitSucceeded    Trigger = "git.commit_succeeded"
	TriggerGitCommitFailedTrans  Trigger = "git.commit_failed_transient"
	TriggerGitCommitFailedPerm   Trigger = "git.commit_failed_permanent"
)

// TransitionRequest describes a requested state transition for a Run.
type TransitionRequest struct {
	Trigger     Trigger
	NextStepID  string // for step.completed: the next step to activate
	HasCommit   bool   // for step.completed at end: whether the outcome has a commit effect
	PauseReason string // for step.blocked
}

// TransitionResult describes the outcome of a state transition.
type TransitionResult struct {
	FromStatus domain.RunStatus
	ToStatus   domain.RunStatus
	Trigger    Trigger
}

// EvaluateRunTransition determines the target state for a Run given a trigger.
// Returns an error if the transition is invalid.
// Per Engine State Machine §2.2 and §2.3.
func EvaluateRunTransition(currentStatus domain.RunStatus, req TransitionRequest) (*TransitionResult, error) {
	// §2.3: Terminal states are immutable
	if currentStatus.IsTerminal() {
		return nil, domain.NewError(domain.ErrConflict,
			fmt.Sprintf("cannot transition from terminal state %q", currentStatus))
	}

	result := &TransitionResult{
		FromStatus: currentStatus,
		Trigger:    req.Trigger,
	}

	switch currentStatus {
	case domain.RunStatusPending:
		return evaluatePendingTransition(result, req)
	case domain.RunStatusActive:
		return evaluateActiveTransition(result, req)
	case domain.RunStatusPaused:
		return evaluatePausedTransition(result, req)
	case domain.RunStatusCommitting:
		return evaluateCommittingTransition(result, req)
	default:
		return nil, domain.NewError(domain.ErrConflict,
			fmt.Sprintf("unknown run status %q", currentStatus))
	}
}

func evaluatePendingTransition(result *TransitionResult, req TransitionRequest) (*TransitionResult, error) {
	switch req.Trigger {
	case TriggerActivate:
		result.ToStatus = domain.RunStatusActive
		return result, nil
	default:
		return nil, domain.NewError(domain.ErrConflict,
			fmt.Sprintf("invalid trigger %q for pending Run", req.Trigger))
	}
}

func evaluateActiveTransition(result *TransitionResult, req TransitionRequest) (*TransitionResult, error) {
	switch req.Trigger {
	case TriggerStepCompleted:
		if req.NextStepID == "" {
			return nil, domain.NewError(domain.ErrInvalidParams,
				"step.completed requires NextStepID (use 'end' for terminal)")
		}
		if req.NextStepID == "end" {
			// Terminal step
			if req.HasCommit {
				result.ToStatus = domain.RunStatusCommitting
			} else {
				result.ToStatus = domain.RunStatusCompleted
			}
		} else {
			// Continue to next step — stay active
			result.ToStatus = domain.RunStatusActive
		}
		return result, nil

	case TriggerStepBlocked:
		result.ToStatus = domain.RunStatusPaused
		return result, nil

	case TriggerStepFailedPermanently:
		result.ToStatus = domain.RunStatusFailed
		return result, nil

	case TriggerDivergenceFailed:
		result.ToStatus = domain.RunStatusFailed
		return result, nil

	case TriggerCancel:
		result.ToStatus = domain.RunStatusCancelled
		return result, nil

	default:
		return nil, domain.NewError(domain.ErrConflict,
			fmt.Sprintf("invalid trigger %q for active Run", req.Trigger))
	}
}

func evaluatePausedTransition(result *TransitionResult, req TransitionRequest) (*TransitionResult, error) {
	switch req.Trigger {
	case TriggerResume:
		result.ToStatus = domain.RunStatusActive
		return result, nil

	case TriggerCancel:
		result.ToStatus = domain.RunStatusCancelled
		return result, nil

	default:
		return nil, domain.NewError(domain.ErrConflict,
			fmt.Sprintf("invalid trigger %q for paused Run", req.Trigger))
	}
}

func evaluateCommittingTransition(result *TransitionResult, req TransitionRequest) (*TransitionResult, error) {
	switch req.Trigger {
	case TriggerGitCommitSucceeded:
		result.ToStatus = domain.RunStatusCompleted
		return result, nil

	case TriggerGitCommitFailedTrans:
		// Stay in committing — retry
		result.ToStatus = domain.RunStatusCommitting
		return result, nil

	case TriggerGitCommitFailedPerm:
		result.ToStatus = domain.RunStatusFailed
		return result, nil

	default:
		return nil, domain.NewError(domain.ErrConflict,
			fmt.Sprintf("invalid trigger %q for committing Run", req.Trigger))
	}
}

// ValidRunTransitions returns all valid (from, trigger, to) triples
// for documentation and testing purposes.
func ValidRunTransitions() []TransitionResult {
	return []TransitionResult{
		{domain.RunStatusPending, domain.RunStatusActive, TriggerActivate},
		{domain.RunStatusActive, domain.RunStatusActive, TriggerStepCompleted},
		{domain.RunStatusActive, domain.RunStatusPaused, TriggerStepBlocked},
		{domain.RunStatusActive, domain.RunStatusCommitting, TriggerStepCompleted},
		{domain.RunStatusActive, domain.RunStatusCompleted, TriggerStepCompleted},
		{domain.RunStatusActive, domain.RunStatusFailed, TriggerStepFailedPermanently},
		{domain.RunStatusActive, domain.RunStatusFailed, TriggerDivergenceFailed},
		{domain.RunStatusActive, domain.RunStatusCancelled, TriggerCancel},
		{domain.RunStatusPaused, domain.RunStatusActive, TriggerResume},
		{domain.RunStatusPaused, domain.RunStatusCancelled, TriggerCancel},
		{domain.RunStatusCommitting, domain.RunStatusCompleted, TriggerGitCommitSucceeded},
		{domain.RunStatusCommitting, domain.RunStatusCommitting, TriggerGitCommitFailedTrans},
		{domain.RunStatusCommitting, domain.RunStatusFailed, TriggerGitCommitFailedPerm},
	}
}
