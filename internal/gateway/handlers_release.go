package gateway

import (
	"net/http"

	"github.com/bszymi/spine/internal/domain"
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
	if err := decodeJSON(r, &req); err != nil {
		WriteError(w, err)
		return
	}

	// Verify actor_id matches the authenticated caller to prevent impersonation.
	if actor := actorFromContext(r.Context()); actor != nil && req.ActorID != actor.ActorID {
		WriteError(w, domain.NewError(domain.ErrForbidden, "actor_id does not match authenticated caller"))
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
