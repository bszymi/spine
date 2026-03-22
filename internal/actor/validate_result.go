package actor

import (
	"fmt"

	"github.com/bszymi/spine/internal/domain"
)

// ValidateResult checks that a result matches its assignment.
// Per Actor Model §5.4: all actor responses are untrusted.
func ValidateResult(req AssignmentRequest, result AssignmentResult) error {
	if result.AssignmentID != req.AssignmentID {
		return domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("assignment_id mismatch: expected %s, got %s", req.AssignmentID, result.AssignmentID))
	}
	if result.RunID != req.RunID {
		return domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("run_id mismatch: expected %s, got %s", req.RunID, result.RunID))
	}
	if result.ActorID != req.ActorID {
		return domain.NewError(domain.ErrInvalidParams,
			fmt.Sprintf("actor_id mismatch: expected %s, got %s", req.ActorID, result.ActorID))
	}
	if result.OutcomeID == "" {
		return domain.NewError(domain.ErrInvalidParams, "outcome_id is required")
	}

	// Check outcome is in expected set
	if len(req.Constraints.ExpectedOutcomes) > 0 {
		valid := false
		for _, expected := range req.Constraints.ExpectedOutcomes {
			if result.OutcomeID == expected {
				valid = true
				break
			}
		}
		if !valid {
			return domain.NewError(domain.ErrInvalidParams,
				fmt.Sprintf("outcome_id %q is not in expected outcomes %v", result.OutcomeID, req.Constraints.ExpectedOutcomes))
		}
	}

	return nil
}
