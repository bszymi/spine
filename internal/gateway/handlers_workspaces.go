package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/workspace"
)

// operatorTokenMiddleware authenticates system-level routes using a static
// operator token (SPINE_OPERATOR_TOKEN). This is separate from per-workspace
// actor authentication.
func operatorTokenMiddleware(next http.Handler) http.Handler {
	token := os.Getenv("SPINE_OPERATOR_TOKEN")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token == "" {
			WriteError(w, domain.NewError(domain.ErrUnavailable, "operator token not configured"))
			return
		}

		header := r.Header.Get("Authorization")
		if header == "" || !strings.EqualFold(header[:min(7, len(header))], "bearer ") {
			WriteError(w, domain.NewError(domain.ErrUnauthorized, "authorization header required"))
			return
		}

		provided := header[7:]
		if provided != token {
			WriteError(w, domain.NewError(domain.ErrUnauthorized, "invalid operator token"))
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleWorkspaceCreate(w http.ResponseWriter, r *http.Request) {
	if s.wsDBProvider == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "workspace management requires shared mode"))
		return
	}

	var req struct {
		WorkspaceID string `json:"workspace_id"`
		DisplayName string `json:"display_name"`
		GitURL      string `json:"git_url,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "invalid request body"))
		return
	}

	if req.WorkspaceID == "" {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, "workspace_id is required"))
		return
	}

	// Check for duplicates.
	existing, err := s.wsDBProvider.GetWorkspace(r.Context(), req.WorkspaceID)
	if err == nil && existing != nil {
		WriteError(w, domain.NewError(domain.ErrConflict, fmt.Sprintf("workspace %q already exists", req.WorkspaceID)))
		return
	}
	if err != nil && !errors.Is(err, workspace.ErrWorkspaceNotFound) {
		WriteError(w, domain.NewError(domain.ErrInternal, "failed to check workspace"))
		return
	}

	displayName := req.DisplayName
	if displayName == "" {
		displayName = req.WorkspaceID
	}

	// TODO(INIT-009/EPIC-007): Call database provisioning (TASK-002) and
	// git repo provisioning (TASK-003) here. For now, create the registry
	// entry as inactive — it must be provisioned before it can serve traffic.
	cfg := workspace.Config{
		ID:          req.WorkspaceID,
		DisplayName: displayName,
		DatabaseURL: "", // Set by provisioning in TASK-002
		RepoPath:    "", // Set by provisioning in TASK-003
		Status:      workspace.StatusInactive,
	}

	if err := s.wsDBProvider.CreateWorkspace(r.Context(), cfg); err != nil {
		WriteError(w, domain.NewError(domain.ErrInternal, "failed to create workspace"))
		return
	}

	WriteJSON(w, http.StatusCreated, map[string]any{
		"workspace_id": cfg.ID,
		"display_name": cfg.DisplayName,
		"status":       string(cfg.Status),
		"message":      "workspace created — run provisioning to activate",
	})
}

func (s *Server) handleWorkspaceList(w http.ResponseWriter, r *http.Request) {
	if s.wsDBProvider == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "workspace management requires shared mode"))
		return
	}

	workspaces, err := s.wsDBProvider.ListAllWorkspaces(r.Context())
	if err != nil {
		WriteError(w, domain.NewError(domain.ErrInternal, "failed to list workspaces"))
		return
	}

	result := make([]map[string]any, len(workspaces))
	for i, ws := range workspaces {
		result[i] = map[string]any{
			"workspace_id": ws.ID,
			"display_name": ws.DisplayName,
			"status":       string(ws.Status),
		}
	}

	WriteJSON(w, http.StatusOK, map[string]any{"workspaces": result})
}

func (s *Server) handleWorkspaceGet(w http.ResponseWriter, r *http.Request) {
	if s.wsDBProvider == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "workspace management requires shared mode"))
		return
	}

	workspaceID := chi.URLParam(r, "workspace_id")
	ws, err := s.wsDBProvider.GetWorkspace(r.Context(), workspaceID)
	if err != nil {
		if errors.Is(err, workspace.ErrWorkspaceNotFound) {
			WriteError(w, domain.NewError(domain.ErrNotFound, fmt.Sprintf("workspace %q not found", workspaceID)))
			return
		}
		WriteError(w, domain.NewError(domain.ErrInternal, "failed to get workspace"))
		return
	}

	resp := map[string]any{
		"workspace_id": ws.ID,
		"display_name": ws.DisplayName,
		"status":       string(ws.Status),
		"repo_path":    ws.RepoPath,
	}
	if ws.DatabaseURL != "" {
		resp["database_host"] = redactDatabaseURL(ws.DatabaseURL)
	}
	WriteJSON(w, http.StatusOK, resp)
}

func (s *Server) handleWorkspaceDeactivate(w http.ResponseWriter, r *http.Request) {
	if s.wsDBProvider == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "workspace management requires shared mode"))
		return
	}

	workspaceID := chi.URLParam(r, "workspace_id")

	if err := s.wsDBProvider.DeactivateWorkspace(r.Context(), workspaceID); err != nil {
		if errors.Is(err, workspace.ErrWorkspaceNotFound) {
			WriteError(w, domain.NewError(domain.ErrNotFound, fmt.Sprintf("workspace %q not found or already inactive", workspaceID)))
			return
		}
		WriteError(w, domain.NewError(domain.ErrInternal, "failed to deactivate workspace"))
		return
	}

	// Invalidate caches so the workspace stops being served immediately.
	s.wsDBProvider.Invalidate(workspaceID)
	if s.servicePool != nil {
		s.servicePool.Evict(workspaceID)
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"workspace_id": workspaceID,
		"status":       "inactive",
	})
}

// redactDatabaseURL strips credentials from a PostgreSQL connection string,
// returning only host:port/dbname. Returns the original string if parsing fails.
func redactDatabaseURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "***"
	}
	u.User = nil
	return u.Host + u.Path
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
