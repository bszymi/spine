package gateway

import (
	"net/http"

	"github.com/bszymi/spine/internal/domain"
)

func (s *Server) handleListAssignments(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "assignments.list") {
		return
	}

	if s.storeFrom(r.Context()) == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "store not configured"))
		return
	}

	actorID := r.URL.Query().Get("actor_id")
	if actorID == "" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "actor_id query parameter required"))
		return
	}

	statusFilter := r.URL.Query().Get("status")
	var statusPtr *domain.AssignmentStatus
	if statusFilter != "" {
		s := domain.AssignmentStatus(statusFilter)
		statusPtr = &s
	}

	assignments, err := s.storeFrom(r.Context()).ListAssignmentsByActor(r.Context(), actorID, statusPtr)
	if err != nil {
		WriteError(w, err)
		return
	}

	if assignments == nil {
		assignments = []domain.Assignment{}
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"assignments": assignments,
		"count":       len(assignments),
	})
}
