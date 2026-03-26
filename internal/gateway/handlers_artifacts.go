package gateway

import (
	"net/http"

	"github.com/bszymi/spine/internal/artifact"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/store"
)

type artifactCreateRequest struct {
	Path         string `json:"path"`
	Content      string `json:"content"`
	WriteContext string `json:"write_context,omitempty"`
}

type artifactUpdateRequest struct {
	Content      string `json:"content"`
	WriteContext string `json:"write_context,omitempty"`
}

func (s *Server) handleArtifactCreate(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "artifact.create") {
		return
	}

	var req artifactCreateRequest
	if err := decodeJSON(r, &req); err != nil {
		WriteError(w, err)
		return
	}
	if req.Path == "" || req.Content == "" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "path and content required"))
		return
	}

	if s.artifacts == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "artifact service not configured"))
		return
	}

	ctx := r.Context()
	if req.WriteContext != "" {
		ctx = artifact.WithWriteContext(ctx, artifact.WriteContext{Branch: req.WriteContext})
	}
	a, err := s.artifacts.Create(ctx, req.Path, req.Content)
	if err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusCreated, map[string]any{
		"artifact_path": a.Path,
		"artifact_id":   a.ID,
		"artifact_type": a.Type,
		"title":         a.Title,
		"status":        a.Status,
		"trace_id":      observe.TraceID(r.Context()),
	})
}

func (s *Server) handleArtifactList(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "artifact.list") {
		return
	}

	limit, cursor := parsePagination(r)
	query := store.ArtifactQuery{
		Type:       r.URL.Query().Get("type"),
		Status:     r.URL.Query().Get("status"),
		ParentPath: r.URL.Query().Get("parent_path"),
		Search:     r.URL.Query().Get("search"),
		Limit:      limit,
		Cursor:     cursor,
	}

	if s.store == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "store not configured"))
		return
	}

	result, err := s.store.QueryArtifacts(r.Context(), query)
	if err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"items":       result.Items,
		"next_cursor": result.NextCursor,
		"has_more":    result.HasMore,
	})
}

// handleArtifactWildcard dispatches artifact requests with slash-containing paths.
func (s *Server) handleArtifactWildcard(w http.ResponseWriter, r *http.Request) {
	path, suffix := extractArtifactPath(r)

	switch {
	case suffix == "/validate" && r.Method == http.MethodPost:
		s.handleArtifactValidate(w, r, path)
	case suffix == "/links" && r.Method == http.MethodGet:
		s.handleArtifactLinks(w, r, path)
	case suffix == "" && r.Method == http.MethodGet:
		s.handleArtifactRead(w, r, path)
	case suffix == "" && r.Method == http.MethodPut:
		s.handleArtifactUpdate(w, r, path)
	default:
		WriteError(w, domain.NewError(domain.ErrNotFound, "not found"))
	}
}

func (s *Server) handleArtifactRead(w http.ResponseWriter, r *http.Request, path string) {
	if !s.authorize(w, r, "artifact.read") {
		return
	}

	if s.artifacts == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "artifact service not configured"))
		return
	}

	ref := r.URL.Query().Get("ref")
	a, err := s.artifacts.Read(r.Context(), path, ref)
	if err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusOK, a)
}

func (s *Server) handleArtifactUpdate(w http.ResponseWriter, r *http.Request, path string) {
	if !s.authorize(w, r, "artifact.update") {
		return
	}

	var req artifactUpdateRequest
	if err := decodeJSON(r, &req); err != nil {
		WriteError(w, err)
		return
	}
	if req.Content == "" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "content required"))
		return
	}

	if s.artifacts == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "artifact service not configured"))
		return
	}

	ctx := r.Context()
	if req.WriteContext != "" {
		ctx = artifact.WithWriteContext(ctx, artifact.WriteContext{Branch: req.WriteContext})
	}
	a, err := s.artifacts.Update(ctx, path, req.Content)
	if err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"artifact_path": a.Path,
		"artifact_id":   a.ID,
		"artifact_type": a.Type,
		"title":         a.Title,
		"status":        a.Status,
		"trace_id":      observe.TraceID(r.Context()),
	})
}

func (s *Server) handleArtifactValidate(w http.ResponseWriter, r *http.Request, path string) {
	if !s.authorize(w, r, "artifact.validate") {
		return
	}

	if s.artifacts == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "artifact service not configured"))
		return
	}

	// Read the artifact, validate it
	a, err := s.artifacts.Read(r.Context(), path, "")
	if err != nil {
		WriteError(w, err)
		return
	}

	result := artifact.Validate(a)
	WriteJSON(w, http.StatusOK, result)
}

func (s *Server) handleArtifactLinks(w http.ResponseWriter, r *http.Request, path string) {
	if !s.authorize(w, r, "artifact.links") {
		return
	}

	if s.store == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "store not configured"))
		return
	}

	links, err := s.store.QueryArtifactLinks(r.Context(), path)
	if err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{"items": links})
}
