package gateway

import (
	"net/http"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/engine"
)

type claimRequest struct {
	ActorID     string `json:"actor_id"`
	ExecutionID string `json:"execution_id"`
}

// handleExecutionClaim processes a step claim request.
// POST /api/v1/execution/claim
// Body: { "actor_id": "...", "execution_id": "..." }
func (s *Server) handleExecutionClaim(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "execution.claim") {
		return
	}
	if s.stepClaimer == nil {
		WriteJSON(w, http.StatusServiceUnavailable, ErrorResponse{
			Status: "error",
			Errors: []ErrorDetail{{Code: "unavailable", Message: "step claiming not available"}},
		})
		return
	}

	req, ok := decodeBody[claimRequest](w, r)
	if !ok {
		return
	}

	// Verify actor_id matches the authenticated caller to prevent impersonation.
	if actor := actorFromContext(r.Context()); actor != nil && req.ActorID != actor.ActorID {
		WriteError(w, domain.NewError(domain.ErrForbidden, "actor_id does not match authenticated caller"))
		return
	}

	result, err := s.stepClaimer.ClaimStep(r.Context(), engine.ClaimRequest{
		ActorID:     req.ActorID,
		ExecutionID: req.ExecutionID,
	})
	if err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"assignment_id": result.Assignment.AssignmentID,
		"run_id":        result.RunID,
		"step_id":       result.StepID,
		"actor_id":      result.Assignment.ActorID,
		"status":        "claimed",
	})
}
