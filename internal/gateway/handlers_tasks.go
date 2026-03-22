package gateway

import (
	"net/http"

	"github.com/bszymi/spine/internal/auth"
	"github.com/bszymi/spine/internal/domain"
)

// handleTaskWildcard dispatches task governance actions with slash-containing paths.
func (s *Server) handleTaskWildcard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, domain.NewError(domain.ErrNotFound, "not found"))
		return
	}

	_, action := extractTaskAction(r)
	switch action {
	case "accept", "reject", "cancel", "abandon", "supersede":
		if !s.authorize(w, r, auth.Operation("task."+action)) {
			return
		}
		WriteNotImplemented(w)
	default:
		WriteError(w, domain.NewError(domain.ErrNotFound, "not found"))
	}
}
