package gateway

import (
	"net/http"
	"strings"

	"github.com/bszymi/spine/internal/engine"
)

// handleExecutionCandidates returns tasks ready for execution, filtered by
// actor type, skills, and blocking status.
// GET /api/v1/execution/candidates?actor_type=ai_agent&skills=backend_development&include_blocked=true
func (s *Server) handleExecutionCandidates(w http.ResponseWriter, r *http.Request) {
	if s.candidateFinder == nil {
		WriteJSON(w, http.StatusServiceUnavailable, ErrorResponse{
			Status: "error",
			Errors: []ErrorDetail{{Code: "unavailable", Message: "execution candidate discovery not available"}},
		})
		return
	}

	filter := engine.ExecutionCandidateFilter{
		ActorType:      r.URL.Query().Get("actor_type"),
		IncludeBlocked: r.URL.Query().Get("include_blocked") == "true",
	}
	if skills := r.URL.Query().Get("skills"); skills != "" {
		filter.Skills = strings.Split(skills, ",")
	}

	candidates, err := s.candidateFinder.FindExecutionCandidates(r.Context(), filter)
	if err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"candidates": candidates,
		"count":      len(candidates),
	})
}
