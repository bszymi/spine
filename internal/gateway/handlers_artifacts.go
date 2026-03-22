package gateway

import (
	"net/http"

	"github.com/bszymi/spine/internal/domain"
)

func (s *Server) handleArtifactCreate(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "artifact.create") {
		return
	}
	WriteNotImplemented(w)
}

func (s *Server) handleArtifactList(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "artifact.list") {
		return
	}
	WriteNotImplemented(w)
}

// handleArtifactWildcard dispatches artifact requests with slash-containing paths.
func (s *Server) handleArtifactWildcard(w http.ResponseWriter, r *http.Request) {
	_, suffix := extractArtifactPath(r)

	switch {
	case suffix == "/validate" && r.Method == http.MethodPost:
		if !s.authorize(w, r, "artifact.validate") {
			return
		}
		WriteNotImplemented(w)
	case suffix == "/links" && r.Method == http.MethodGet:
		if !s.authorize(w, r, "artifact.links") {
			return
		}
		WriteNotImplemented(w)
	case suffix == "" && r.Method == http.MethodGet:
		if !s.authorize(w, r, "artifact.read") {
			return
		}
		WriteNotImplemented(w)
	case suffix == "" && r.Method == http.MethodPut:
		if !s.authorize(w, r, "artifact.update") {
			return
		}
		WriteNotImplemented(w)
	default:
		WriteError(w, domain.NewError(domain.ErrNotFound, "not found"))
	}
}
