package gateway

import (
	"net/http"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/engine"
)

type acknowledgeRequest struct {
	ActorID string `json:"actor_id"`
}

// handleStepAcknowledge processes a step acknowledge request.
// POST /api/v1/steps/{execution_id}/acknowledge
// Body: { "actor_id": "..." }
func (s *Server) handleStepAcknowledge(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "execution.claim") {
		return
	}
	stepAcknowledger := s.stepAcknowledgerFrom(r.Context())
	if stepAcknowledger == nil {
		WriteJSON(w, http.StatusServiceUnavailable, ErrorResponse{
			Status: "error",
			Errors: []ErrorDetail{{Code: "unavailable", Message: "step acknowledge not available"}},
		})
		return
	}

	executionID := r.PathValue("execution_id")
	if executionID == "" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "execution_id is required"))
		return
	}

	req, ok := decodeBody[acknowledgeRequest](w, r)
	if !ok {
		return
	}

	// Verify actor_id matches the authenticated caller to prevent impersonation.
	if actor := actorFromContext(r.Context()); actor != nil && req.ActorID != actor.ActorID {
		WriteError(w, domain.NewError(domain.ErrForbidden, "actor_id does not match authenticated caller"))
		return
	}

	result, err := stepAcknowledger.AcknowledgeStep(r.Context(), engine.AcknowledgeRequest{
		ActorID:     req.ActorID,
		ExecutionID: executionID,
	})
	if err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"execution_id": result.ExecutionID,
		"step_id":      result.StepID,
		"status":       result.Status,
		"started_at":   result.StartedAt,
	})
}
