package gateway

import (
	"net/http"

	"github.com/bszymi/spine/internal/domain"
)

func (s *Server) handleArtifactCreate(w http.ResponseWriter, _ *http.Request) {
	WriteNotImplemented(w)
}

func (s *Server) handleArtifactList(w http.ResponseWriter, _ *http.Request) {
	WriteNotImplemented(w)
}

// handleArtifactWildcard dispatches artifact requests with slash-containing paths.
// Routes: GET /artifacts/{path}, PUT /artifacts/{path},
// POST /artifacts/{path}/validate, GET /artifacts/{path}/links
func (s *Server) handleArtifactWildcard(w http.ResponseWriter, r *http.Request) {
	_, suffix := extractArtifactPath(r)

	switch {
	case suffix == "/validate" && r.Method == http.MethodPost:
		WriteNotImplemented(w)
	case suffix == "/links" && r.Method == http.MethodGet:
		WriteNotImplemented(w)
	case suffix == "" && r.Method == http.MethodGet:
		WriteNotImplemented(w)
	case suffix == "" && r.Method == http.MethodPut:
		WriteNotImplemented(w)
	default:
		WriteError(w, domain.NewError(domain.ErrNotFound, "not found"))
	}
}
