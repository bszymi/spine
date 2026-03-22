package gateway

import (
	"net/http"

	"github.com/bszymi/spine/internal/artifact"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
)

// handleHealth returns system health status (unauthenticated).
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	status := "healthy"
	components := map[string]string{}

	if s.store != nil {
		if err := s.store.Ping(r.Context()); err != nil {
			status = "unhealthy"
			components["database"] = "unhealthy"
		} else {
			components["database"] = "healthy"
		}
	} else {
		status = "unhealthy"
		components["database"] = "not_configured"
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"status":     status,
		"components": components,
	})
}

func (s *Server) handleSystemRebuild(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "system.rebuild") {
		return
	}

	if s.projSync == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "projection service not configured"))
		return
	}

	if err := s.projSync.FullRebuild(r.Context()); err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"status":   "completed",
		"trace_id": observe.TraceID(r.Context()),
	})
}

func (s *Server) handleSystemRebuildStatus(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "system.rebuild") {
		return
	}

	if s.store == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "store not configured"))
		return
	}

	state, err := s.store.GetSyncState(r.Context())
	if err != nil {
		WriteError(w, err)
		return
	}

	if state == nil {
		WriteJSON(w, http.StatusOK, map[string]any{"status": "no_sync_state"})
		return
	}

	WriteJSON(w, http.StatusOK, state)
}

func (s *Server) handleSystemValidate(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "system.validate") {
		return
	}

	if s.artifacts == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "artifact service not configured"))
		return
	}

	artifacts, err := s.artifacts.List(r.Context(), "")
	if err != nil {
		WriteError(w, err)
		return
	}

	var results []map[string]any
	for _, a := range artifacts {
		result := artifact.Validate(a)
		if result.Status != "passed" {
			results = append(results, map[string]any{
				"path":   a.Path,
				"result": result,
			})
		}
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"total_artifacts": len(artifacts),
		"issues":          results,
		"trace_id":        observe.TraceID(r.Context()),
	})
}
