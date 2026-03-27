package gateway

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/store"
)

func (s *Server) handleQueryArtifacts(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "query.artifacts") {
		return
	}

	if s.projQuery == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "query service not configured"))
		return
	}

	limit, cursor := parsePagination(r)
	result, err := s.projQuery.QueryArtifacts(r.Context(), store.ArtifactQuery{
		Type:       r.URL.Query().Get("type"),
		Status:     r.URL.Query().Get("status"),
		ParentPath: r.URL.Query().Get("parent_path"),
		Search:     r.URL.Query().Get("search"),
		Limit:      limit,
		Cursor:     cursor,
	})
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

func (s *Server) handleQueryGraph(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "query.graph") {
		return
	}

	if s.projQuery == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "query service not configured"))
		return
	}

	root := r.URL.Query().Get("root")
	if root == "" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "root query parameter required"))
		return
	}

	depth := 2
	if d := r.URL.Query().Get("depth"); d != "" {
		if parsed, err := strconv.Atoi(d); err == nil && parsed >= 1 && parsed <= 5 {
			depth = parsed
		}
	}

	var linkTypes []string
	if lt := r.URL.Query().Get("link_types"); lt != "" {
		linkTypes = strings.Split(lt, ",")
	}

	graph, err := s.projQuery.QueryGraph(r.Context(), root, depth, linkTypes)
	if err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusOK, graph)
}

func (s *Server) handleQueryHistory(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "query.history") {
		return
	}

	if s.projQuery == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "query service not configured"))
		return
	}

	path := r.URL.Query().Get("path")
	if path == "" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "path query parameter required"))
		return
	}

	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	history, err := s.projQuery.QueryHistory(r.Context(), path, limit)
	if err != nil {
		WriteError(w, err)
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"items":       history,
		"next_cursor": nil,
		"has_more":    false,
	})
}

func (s *Server) handleQueryRuns(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r, "query.runs") {
		return
	}

	if s.projQuery == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "query service not configured"))
		return
	}

	taskPath := r.URL.Query().Get("task_path")
	if taskPath == "" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "task_path query parameter required"))
		return
	}

	runs, err := s.projQuery.QueryRuns(r.Context(), taskPath)
	if err != nil {
		WriteError(w, err)
		return
	}

	// Filter by status if provided.
	if statusFilter := r.URL.Query().Get("status"); statusFilter != "" {
		filtered := make([]domain.Run, 0)
		for i := range runs {
			if string(runs[i].Status) == statusFilter {
				filtered = append(filtered, runs[i])
			}
		}
		runs = filtered
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"items":       runs,
		"next_cursor": nil,
		"has_more":    false,
	})
}
