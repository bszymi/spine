package gateway

import (
	"net/http"

	"github.com/bszymi/spine/internal/domain"
)

// handleTaskWildcard dispatches task governance actions with slash-containing paths.
// Routes: POST /tasks/{path}/accept|reject|cancel|abandon|supersede
func (s *Server) handleTaskWildcard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, domain.NewError(domain.ErrNotFound, "not found"))
		return
	}

	_, action := extractTaskAction(r)
	switch action {
	case "accept", "reject", "cancel", "abandon", "supersede":
		WriteNotImplemented(w)
	default:
		WriteError(w, domain.NewError(domain.ErrNotFound, "not found"))
	}
}
