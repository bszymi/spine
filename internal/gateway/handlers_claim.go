package gateway

import (
	"encoding/json"
	"net/http"

	"github.com/bszymi/spine/internal/engine"
)

// handleExecutionClaim processes a step claim request.
// POST /api/v1/execution/claim
// Body: { "actor_id": "...", "execution_id": "..." }
func (s *Server) handleExecutionClaim(w http.ResponseWriter, r *http.Request) {
	if s.stepClaimer == nil {
		WriteJSON(w, http.StatusServiceUnavailable, ErrorResponse{
			Status: "error",
			Errors: []ErrorDetail{{Code: "unavailable", Message: "step claiming not available"}},
		})
		return
	}

	var req struct {
		ActorID     string `json:"actor_id"`
		ExecutionID string `json:"execution_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteJSON(w, http.StatusBadRequest, ErrorResponse{
			Status: "error",
			Errors: []ErrorDetail{{Code: "invalid_params", Message: "invalid request body"}},
		})
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
