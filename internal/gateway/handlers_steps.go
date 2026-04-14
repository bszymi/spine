package gateway

import (
	"net/http"
	"strconv"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/engine"
)

// handleListStepExecutions returns active step executions filtered by actor.
// GET /api/v1/execution/steps?actor_id=runner-abc&actor_type=automated_system&status=waiting&limit=10
func (s *Server) handleListStepExecutions(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "execution.query") {
		return
	}
	if s.stepExecutionLister == nil {
		WriteJSON(w, http.StatusServiceUnavailable, ErrorResponse{
			Status: "error",
			Errors: []ErrorDetail{{Code: "unavailable", Message: "step execution listing not available"}},
		})
		return
	}

	q := r.URL.Query()

	status := q.Get("status")
	if status != "" && status != string(domain.StepStatusWaiting) && status != string(domain.StepStatusAssigned) && status != string(domain.StepStatusInProgress) {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "status must be one of: waiting, assigned, in_progress"))
		return
	}

	limit := 10
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			WriteError(w, domain.NewError(domain.ErrInvalidParams, "limit must be a positive integer"))
			return
		}
		limit = n
	}

	steps, err := s.stepExecutionLister.ListStepExecutions(r.Context(), engine.StepExecutionQuery{
		ActorID:   q.Get("actor_id"),
		ActorType: q.Get("actor_type"),
		Status:    status,
		Limit:     limit,
	})
	if err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{"steps": steps})
}
