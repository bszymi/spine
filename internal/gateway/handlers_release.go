package gateway

import (
	"encoding/json"
	"net/http"

	"github.com/bszymi/spine/internal/engine"
)

// handleExecutionRelease processes a step release request.
// POST /api/v1/execution/release
// Body: { "actor_id": "...", "assignment_id": "...", "reason": "..." }
func (s *Server) handleExecutionRelease(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "execution.release") {
		return
	}
	if s.stepReleaser == nil {
		WriteJSON(w, http.StatusServiceUnavailable, ErrorResponse{
			Status: "error",
			Errors: []ErrorDetail{{Code: "unavailable", Message: "step release not available"}},
		})
		return
	}

	var req struct {
		ActorID      string `json:"actor_id"`
		AssignmentID string `json:"assignment_id"`
		Reason       string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteJSON(w, http.StatusBadRequest, ErrorResponse{
			Status: "error",
			Errors: []ErrorDetail{{Code: "invalid_params", Message: "invalid request body"}},
		})
		return
	}

	err := s.stepReleaser.ReleaseStep(r.Context(), engine.ReleaseRequest{
		ActorID:      req.ActorID,
		AssignmentID: req.AssignmentID,
		Reason:       req.Reason,
	})
	if err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusOK, map[string]string{
		"status": "released",
	})
}
