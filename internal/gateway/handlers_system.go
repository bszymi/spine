package gateway

import "net/http"

// handleHealth returns system health status.
// This is the only non-stub handler — it checks database connectivity.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	status := "healthy"
	components := map[string]string{}

	if s.store != nil {
		if err := s.store.Ping(r.Context()); err != nil {
			status = "degraded"
			components["database"] = "unhealthy"
		} else {
			components["database"] = "healthy"
		}
	} else {
		status = "degraded"
		components["database"] = "not_configured"
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"status":     status,
		"components": components,
	})
}

func (s *Server) handleSystemRebuild(w http.ResponseWriter, _ *http.Request) {
	WriteNotImplemented(w)
}

func (s *Server) handleSystemRebuildStatus(w http.ResponseWriter, _ *http.Request) {
	WriteNotImplemented(w)
}

func (s *Server) handleSystemValidate(w http.ResponseWriter, _ *http.Request) {
	WriteNotImplemented(w)
}
