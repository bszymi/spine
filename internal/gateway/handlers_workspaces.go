package gateway

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
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
	if token != "" && len(token) < 32 {
		fmt.Fprintf(os.Stderr, "WARNING: SPINE_OPERATOR_TOKEN is shorter than 32 characters — vulnerable to brute force\n")
	}
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
		if subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
			WriteError(w, domain.NewError(domain.ErrUnauthorized, "invalid operator token"))
			return
		}

		next.ServeHTTP(w, r)
	})
}

type workspaceCreateRequest struct {
	WorkspaceID    string `json:"workspace_id"`
	DisplayName    string `json:"display_name"`
	GitURL         string `json:"git_url,omitempty"`
	SMPWorkspaceID string `json:"smp_workspace_id,omitempty"`
}

func (s *Server) handleWorkspaceCreate(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeBody[workspaceCreateRequest](w, r)
	if !ok {
		return
	}

	// Validate the ID before the provider check so malformed IDs
	// reliably surface as invalid_params regardless of whether shared
	// mode is configured. Without this, the single-mode server
	// returns 503 for a traversal-shaped ID — correct in effect, but
	// the reason (malformed input) never reaches the operator.
	if err := workspace.ValidateID(req.WorkspaceID); err != nil {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, err.Error()))
		return
	}

	if s.wsDBProvider == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "workspace management requires shared mode"))
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

	// TODO(EPIC-007): Call database provisioning (TASK-002) and
	// git repo provisioning (TASK-003) here. For now, create the registry
	// entry as inactive — it must be provisioned before it can serve traffic.
	cfg := workspace.Config{
		ID:          req.WorkspaceID,
		DisplayName: displayName,
		// DatabaseURL set by provisioning (TASK-002); RepoPath set by
		// provisioning (TASK-003). Both default to zero-value here.
		Status:         workspace.StatusInactive,
		SMPWorkspaceID: req.SMPWorkspaceID,
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
	workspaceID := chi.URLParam(r, "workspace_id")
	if err := workspace.ValidateID(workspaceID); err != nil {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, err.Error()))
		return
	}

	if s.wsDBProvider == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "workspace management requires shared mode"))
		return
	}
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
	// Database credential is redacted via secrets.SecretValue: include
	// the field as a presence indicator, but never echo the URL.
	if len(ws.DatabaseURL.Reveal()) > 0 {
		resp["database_url"] = ws.DatabaseURL.String()
	}
	WriteJSON(w, http.StatusOK, resp)
}

func (s *Server) handleWorkspaceDeactivate(w http.ResponseWriter, r *http.Request) {
	workspaceID := chi.URLParam(r, "workspace_id")
	if err := workspace.ValidateID(workspaceID); err != nil {
		WriteError(w, domain.NewError(domain.ErrInvalidParams, err.Error()))
		return
	}

	if s.wsDBProvider == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "workspace management requires shared mode"))
		return
	}

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
