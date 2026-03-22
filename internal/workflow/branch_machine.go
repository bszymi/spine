package workflow

import (
	"fmt"

	"github.com/bszymi/spine/internal/domain"
)

// BranchTrigger represents an event that may cause a Branch state transition.
type BranchTrigger string

const (
	BranchTriggerStart      BranchTrigger = "branch.start"
	BranchTriggerStepDone   BranchTrigger = "branch_step.completed"
	BranchTriggerStepFailed BranchTrigger = "branch_step.failed_permanently"
)

// BranchTransitionRequest describes a requested branch state transition.
type BranchTransitionRequest struct {
	Trigger     BranchTrigger
	HasNextStep bool // true if there's a next step in the branch
}

// BranchTransitionResult describes the outcome of a branch state transition.
type BranchTransitionResult struct {
	FromStatus domain.BranchStatus
	ToStatus   domain.BranchStatus
	Trigger    BranchTrigger
}

// EvaluateBranchTransition determines the target state for a Branch.
// Per Engine State Machine §5.2.
func EvaluateBranchTransition(current domain.BranchStatus, req BranchTransitionRequest) (*BranchTransitionResult, error) {
	if current == domain.BranchStatusCompleted || current == domain.BranchStatusFailed {
		return nil, domain.NewError(domain.ErrConflict,
			fmt.Sprintf("cannot transition from terminal branch state %q", current))
	}

	result := &BranchTransitionResult{
		FromStatus: current,
		Trigger:    req.Trigger,
	}

	switch current {
	case domain.BranchStatusPending:
		if req.Trigger != BranchTriggerStart {
			return nil, domain.NewError(domain.ErrConflict,
				fmt.Sprintf("invalid trigger %q for pending branch", req.Trigger))
		}
		result.ToStatus = domain.BranchStatusInProgress
		return result, nil

	case domain.BranchStatusInProgress:
		switch req.Trigger {
		case BranchTriggerStepDone:
			if req.HasNextStep {
				result.ToStatus = domain.BranchStatusInProgress
			} else {
				result.ToStatus = domain.BranchStatusCompleted
			}
			return result, nil
		case BranchTriggerStepFailed:
			result.ToStatus = domain.BranchStatusFailed
			return result, nil
		default:
			return nil, domain.NewError(domain.ErrConflict,
				fmt.Sprintf("invalid trigger %q for in_progress branch", req.Trigger))
		}

	default:
		return nil, domain.NewError(domain.ErrConflict,
			fmt.Sprintf("unknown branch status %q", current))
	}
}
