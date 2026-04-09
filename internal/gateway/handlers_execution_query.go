package gateway

import (
	"net/http"

	"github.com/bszymi/spine/internal/store"
)

// handleExecutionTasksReady returns tasks that are not blocked and not assigned.
// GET /api/v1/execution/tasks/ready
func (s *Server) handleExecutionTasksReady(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "execution.query") {
		return
	}
	blocked := false
	projs, err := s.storeFrom(r.Context()).QueryExecutionProjections(r.Context(), store.ExecutionProjectionQuery{
		Blocked:          &blocked,
		AssignmentStatus: "unassigned",
	})
	if err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"tasks": projs, "count": len(projs)})
}

// handleExecutionTasksBlocked returns tasks that are blocked by dependencies.
// GET /api/v1/execution/tasks/blocked
func (s *Server) handleExecutionTasksBlocked(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "execution.query") {
		return
	}
	blocked := true
	projs, err := s.storeFrom(r.Context()).QueryExecutionProjections(r.Context(), store.ExecutionProjectionQuery{
		Blocked: &blocked,
	})
	if err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"tasks": projs, "count": len(projs)})
}

// handleExecutionTasksAssigned returns tasks assigned to a specific actor.
// GET /api/v1/execution/tasks/assigned?actor_id=...
func (s *Server) handleExecutionTasksAssigned(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "execution.query") {
		return
	}
	actorID := r.URL.Query().Get("actor_id")
	if actorID == "" {
		WriteJSON(w, http.StatusBadRequest, ErrorResponse{
			Status: "error",
			Errors: []ErrorDetail{{Code: "invalid_params", Message: "actor_id query parameter is required"}},
		})
		return
	}
	projs, err := s.storeFrom(r.Context()).QueryExecutionProjections(r.Context(), store.ExecutionProjectionQuery{
		AssignedActorID: actorID,
	})
	if err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"tasks": projs, "count": len(projs)})
}

// handleExecutionTasksAll returns all tasks with their blocking and assignment status.
// GET /api/v1/execution/tasks
func (s *Server) handleExecutionTasksAll(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "execution.query") {
		return
	}
	projs, err := s.storeFrom(r.Context()).QueryExecutionProjections(r.Context(), store.ExecutionProjectionQuery{})
	if err != nil {
		WriteError(w, err)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"tasks": projs, "count": len(projs)})
}
