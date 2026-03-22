package workflow

import (
	"fmt"

	"github.com/bszymi/spine/internal/domain"
)

// DivergenceTrigger represents an event that may cause a DivergenceContext state transition.
type DivergenceTrigger string

const (
	DivergenceTriggerStart        DivergenceTrigger = "divergence.start"
	DivergenceTriggerBranchDone   DivergenceTrigger = "branch.terminal"
	DivergenceTriggerEvalDone     DivergenceTrigger = "evaluation.completed"
	DivergenceTriggerEvalFailed   DivergenceTrigger = "evaluation.failed"
	DivergenceTriggerCreateBranch DivergenceTrigger = "branch.create"
	DivergenceTriggerCloseWindow  DivergenceTrigger = "divergence.close_window"
)

// DivergenceTransitionRequest describes a requested divergence state transition.
type DivergenceTransitionRequest struct {
	Trigger           DivergenceTrigger
	EntryPolicy       domain.EntryPolicy
	BranchesTotal     int
	BranchesTerminal  int
	BranchesCompleted int // completed (not failed)
	MinBranches       int
	Strategy          domain.ConvergenceStrategy
	BranchFailed      bool
	WindowOpen        bool
	BranchCount       int
	MaxBranches       int
}

// DivergenceTransitionResult describes the outcome of a divergence state transition.
type DivergenceTransitionResult struct {
	FromStatus domain.DivergenceStatus
	ToStatus   domain.DivergenceStatus
	Trigger    DivergenceTrigger
}

// EvaluateDivergenceTransition determines the target state for a DivergenceContext.
// Per Engine State Machine §4.2.
func EvaluateDivergenceTransition(current domain.DivergenceStatus, req DivergenceTransitionRequest) (*DivergenceTransitionResult, error) {
	if current == domain.DivergenceStatusResolved || current == domain.DivergenceStatusFailed {
		return nil, domain.NewError(domain.ErrConflict,
			fmt.Sprintf("cannot transition from terminal divergence state %q", current))
	}

	result := &DivergenceTransitionResult{
		FromStatus: current,
		Trigger:    req.Trigger,
	}

	switch current {
	case domain.DivergenceStatusPending:
		return evaluateDivergencePending(result, req)
	case domain.DivergenceStatusActive:
		return evaluateDivergenceActive(result, req)
	case domain.DivergenceStatusConverging:
		return evaluateDivergenceConverging(result, req)
	default:
		return nil, domain.NewError(domain.ErrConflict,
			fmt.Sprintf("unknown divergence status %q", current))
	}
}

func evaluateDivergencePending(result *DivergenceTransitionResult, req DivergenceTransitionRequest) (*DivergenceTransitionResult, error) {
	if req.Trigger != DivergenceTriggerStart {
		return nil, domain.NewError(domain.ErrConflict,
			fmt.Sprintf("invalid trigger %q for pending divergence", req.Trigger))
	}
	result.ToStatus = domain.DivergenceStatusActive
	return result, nil
}

func evaluateDivergenceActive(result *DivergenceTransitionResult, req DivergenceTransitionRequest) (*DivergenceTransitionResult, error) {
	switch req.Trigger {
	case DivergenceTriggerBranchDone:
		// Check require_all failure
		if req.BranchFailed && req.Strategy == domain.ConvergenceRequireAll {
			result.ToStatus = domain.DivergenceStatusFailed
			return result, nil
		}
		// Check entry policy
		if isEntryPolicySatisfied(req) {
			result.ToStatus = domain.DivergenceStatusConverging
		} else {
			result.ToStatus = domain.DivergenceStatusActive
		}
		return result, nil

	case DivergenceTriggerCreateBranch:
		if !req.WindowOpen {
			return nil, domain.NewError(domain.ErrConflict, "divergence window is closed")
		}
		if req.MaxBranches > 0 && req.BranchCount >= req.MaxBranches {
			return nil, domain.NewError(domain.ErrConflict, "maximum branch count reached")
		}
		result.ToStatus = domain.DivergenceStatusActive
		return result, nil

	case DivergenceTriggerCloseWindow:
		if !req.WindowOpen {
			return nil, domain.NewError(domain.ErrConflict, "window already closed")
		}
		result.ToStatus = domain.DivergenceStatusActive
		return result, nil

	default:
		return nil, domain.NewError(domain.ErrConflict,
			fmt.Sprintf("invalid trigger %q for active divergence", req.Trigger))
	}
}

func evaluateDivergenceConverging(result *DivergenceTransitionResult, req DivergenceTransitionRequest) (*DivergenceTransitionResult, error) {
	switch req.Trigger {
	case DivergenceTriggerEvalDone:
		result.ToStatus = domain.DivergenceStatusResolved
		return result, nil
	case DivergenceTriggerEvalFailed:
		result.ToStatus = domain.DivergenceStatusFailed
		return result, nil
	default:
		return nil, domain.NewError(domain.ErrConflict,
			fmt.Sprintf("invalid trigger %q for converging divergence", req.Trigger))
	}
}

// isEntryPolicySatisfied checks if the convergence entry policy is met.
func isEntryPolicySatisfied(req DivergenceTransitionRequest) bool {
	switch req.EntryPolicy {
	case domain.EntryPolicyAllTerminal:
		return req.BranchesTerminal >= req.BranchesTotal && req.BranchesTotal > 0
	case domain.EntryPolicyMinCompleted:
		return req.BranchesCompleted >= req.MinBranches
	case domain.EntryPolicyManualTrigger:
		return false // requires explicit trigger, not evaluated here
	case domain.EntryPolicyDeadlineReached:
		return false // evaluated by scheduler, not here
	default:
		return false
	}
}
