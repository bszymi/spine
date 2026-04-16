package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/githttp"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/workspace"
)

// mountGitRoutes adds the git smart HTTP routes to the router.
// These routes have their own middleware chain — workspace is resolved from
// the URL path (not X-Workspace-ID header), and auth is optional for trusted IPs.
func (s *Server) mountGitRoutes(r chi.Router) {
	if s.gitHTTP == nil {
		return
	}

	// /git/{workspace_id}/* — shared mode with explicit workspace
	r.HandleFunc("/git/{workspace_id}/*", s.handleGit)

	// /git/* — single-workspace fallback (workspace_id param will be empty)
	r.HandleFunc("/git/*", s.handleGit)
}

// handleGit resolves the workspace from the URL, applies auth if needed,
// and delegates to the githttp.Handler.
func (s *Server) handleGit(w http.ResponseWriter, r *http.Request) {
	if s.gitHTTP == nil {
		WriteError(w, domain.NewError(domain.ErrUnavailable, "git HTTP endpoint not configured"))
		return
	}

	log := observe.Logger(r.Context())

	// Resolve workspace from URL path.
	workspaceID, gitPath := parseGitPath(r)

	cfg, err := s.resolveGitWorkspace(r.Context(), workspaceID)
	if err != nil {
		if errors.Is(err, workspace.ErrWorkspaceNotFound) {
			if workspaceID == "" {
				WriteError(w, domain.NewError(domain.ErrInvalidParams, "workspace ID required in shared mode"))
			} else {
				WriteError(w, domain.NewError(domain.ErrNotFound, fmt.Sprintf("workspace %q not found", workspaceID)))
			}
			return
		}
		if errors.Is(err, workspace.ErrWorkspaceInactive) {
			WriteError(w, domain.NewError(domain.ErrForbidden, fmt.Sprintf("workspace %q is inactive", workspaceID)))
			return
		}
		log.Error("workspace resolution failed", "error", err)
		WriteError(w, domain.NewError(domain.ErrInternal, "workspace resolution failed"))
		return
	}

	// Auth: skip for trusted IPs, require bearer token for others.
	if !s.gitHTTP.IsTrustedIP(r.RemoteAddr) {
		if !s.devMode {
			if err := s.validateGitAuth(r, cfg); err != nil {
				WriteError(w, err)
				return
			}
		}
	}

	// Rewrite request path to just the git-specific portion.
	r.URL.Path = gitPath

	// Set repo path in context and delegate to git handler.
	ctx := githttp.WithRepoPath(r.Context(), cfg.RepoPath)
	ctx = observe.WithWorkspaceID(ctx, cfg.ID)
	s.gitHTTP.ServeHTTP(w, r.WithContext(ctx))
}

// parseGitPath extracts the workspace ID and git-specific path from the request.
// URL patterns:
//
//	/git/{workspace_id}/info/refs  -> ("workspace_id", "/info/refs")
//	/git/{workspace_id}/git-upload-pack -> ("workspace_id", "/git-upload-pack")
//	/git/info/refs                 -> ("", "/info/refs")
//	/git/git-upload-pack           -> ("", "/git-upload-pack")
func parseGitPath(r *http.Request) (workspaceID string, gitPath string) {
	// chi captures workspace_id if the /{workspace_id}/* route matched.
	workspaceID = chi.URLParam(r, "workspace_id")
	wildcard := chi.URLParam(r, "*")

	if workspaceID != "" {
		// Matched /git/{workspace_id}/* — wildcard is the git path.
		return workspaceID, "/" + wildcard
	}

	// Matched /git/* — the wildcard contains everything after /git/.
	// Check if the first segment is a known git path or a workspace ID.
	if isGitProtocolPath(wildcard) {
		return "", "/" + wildcard
	}

	// First segment is the workspace ID.
	parts := strings.SplitN(wildcard, "/", 2)
	if len(parts) == 2 {
		return parts[0], "/" + parts[1]
	}
	return parts[0], "/"
}

// isGitProtocolPath returns true if the path starts with a known git protocol segment.
func isGitProtocolPath(path string) bool {
	return strings.HasPrefix(path, "info/") ||
		strings.HasPrefix(path, "git-upload-pack") ||
		strings.HasPrefix(path, "git-receive-pack") ||
		strings.HasPrefix(path, "HEAD") ||
		strings.HasPrefix(path, "objects/")
}

// resolveGitWorkspace resolves the workspace config for git access.
func (s *Server) resolveGitWorkspace(ctx context.Context, workspaceID string) (*workspace.Config, error) {
	if s.wsResolver == nil {
		return nil, fmt.Errorf("workspace resolver not configured")
	}
	return s.wsResolver.Resolve(ctx, workspaceID)
}

// validateGitAuth validates the bearer token for non-trusted git requests.
func (s *Server) validateGitAuth(r *http.Request, cfg *workspace.Config) error {
	header := r.Header.Get("Authorization")
	if header == "" {
		return domain.NewError(domain.ErrUnauthorized, "authorization required for external git access")
	}

	if len(header) < 7 || !strings.EqualFold(header[:7], "bearer ") {
		return domain.NewError(domain.ErrUnauthorized, "invalid authorization header format")
	}
	token := header[7:]
	if token == "" {
		return domain.NewError(domain.ErrUnauthorized, "invalid authorization header format")
	}

	// Use workspace-scoped auth if available, otherwise server-level.
	authSvc := s.auth
	if s.servicePool != nil {
		ss, err := s.servicePool.Get(r.Context(), cfg.ID)
		if err == nil && ss.Auth != nil {
			authSvc = ss.Auth
			defer s.servicePool.Release(cfg.ID)
		}
	}

	if authSvc == nil {
		return domain.NewError(domain.ErrUnavailable, "authentication not configured")
	}

	_, err := authSvc.ValidateToken(r.Context(), token)
	return err
}
