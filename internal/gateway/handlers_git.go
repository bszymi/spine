package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/bszymi/spine/internal/branchprotect"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/event"
	"github.com/bszymi/spine/internal/githttp"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/workspace"
)

// GitPushResources bundles per-workspace state the pre-receive gate
// needs: the branch-protection policy for evaluating ref updates and
// the event emitter for honored-override governance events (ADR-009
// §4). Both come off the target workspace's ServiceSet so shared-mode
// deployments evaluate and audit against the right workspace.
type GitPushResources struct {
	Policy branchprotect.Policy
	Events event.Emitter
}

// GitPushResolverFunc resolves the per-workspace push resources. Callers
// must defer the returned release — it decrements the ServicePool ref
// the resolver took, so pools do not leak across pushes. The callback
// is never nil even on error.
type GitPushResolverFunc func(ctx context.Context, workspaceID string) (GitPushResources, func(), error)

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
		if errors.Is(err, workspace.ErrWorkspaceUnavailable) {
			WriteError(w, domain.NewError(domain.ErrUnavailable, fmt.Sprintf("workspace %q is currently unavailable", workspaceID)))
			return
		}
		log.Error("workspace resolution failed", "error", err)
		WriteError(w, domain.NewError(domain.ErrInternal, "workspace resolution failed"))
		return
	}

	// Auth gate.
	//
	//  - Read-only (clone/fetch): trusted-CIDR source IPs bypass the
	//    bearer-token check. This is the original runner-container
	//    affordance.
	//  - Push (git-receive-pack) with the flag ON: the trusted-CIDR
	//    bypass does NOT apply. Every push must carry a bearer token
	//    so an actor identity is pinned for the upcoming
	//    branch-protection pre-receive check (EPIC-004 TASK-002) and
	//    for audit. Without this split, a runner subnet configured
	//    for token-less cloning could also push anonymously once
	//    receive-pack is on.
	//  - Push with the flag OFF: skip the auth gate so the handler
	//    can return its documented 403 with `SPINE_GIT_RECEIVE_PACK_ENABLED`
	//    guidance. Authenticating a request we are about to reject
	//    adds no security value and hides the flag name from
	//    unauthenticated callers who are exactly the ones most likely
	//    to hit it.
	//  - devMode bypasses auth entirely so local development does not
	//    require wiring a token.
	push := githttp.IsReceivePack(r)
	pushDisabledShortCircuit := push && !s.gitHTTP.ReceivePackEnabled()
	trustedBypass := !push && s.gitHTTP.IsTrustedIP(r.RemoteAddr)
	var actor *domain.Actor
	if !pushDisabledShortCircuit && !trustedBypass && !s.devMode {
		a, err := s.validateGitAuth(r, cfg)
		if err != nil {
			WriteError(w, err)
			return
		}
		actor = a
	}

	// Rewrite request path to just the git-specific portion.
	r.URL.Path = gitPath

	// Set repo path in context and delegate to git handler. The actor
	// is attached so the handler's pre-receive check can pin the
	// caller identity on every ref update (EPIC-004 TASK-002).
	ctx := githttp.WithRepoPath(r.Context(), cfg.RepoPath)
	ctx = observe.WithWorkspaceID(ctx, cfg.ID)
	if actor != nil {
		ctx = domain.WithActor(ctx, actor)
	}

	// Resolve the workspace-scoped branch-protection policy and
	// attach it to the context. Scoped narrowly to:
	//   - POST /git-receive-pack (the only path that actually
	//     carries ref updates to evaluate — info/refs for push is
	//     just capability advertisement),
	//   - with the flag on (a disabled-push attempt falls through
	//     to the handler's 403 with `SPINE_GIT_RECEIVE_PACK_ENABLED`
	//     guidance — resolving the policy here would hide that
	//     message behind a DB-lookup error),
	//   - and only when a resolver is configured.
	//
	// Release must run after ServeHTTP so any ServicePool ref taken
	// by the resolver is held for the life of the push.
	if push && r.Method == http.MethodPost && s.gitHTTP.ReceivePackEnabled() && s.gitPushResolver != nil {
		res, release, err := s.gitPushResolver(ctx, cfg.ID)
		defer release()
		if err != nil {
			observe.Logger(ctx).Error("resolve push resources failed",
				"workspace_id", cfg.ID, "error", err)
			WriteError(w, domain.NewError(domain.ErrInternal, "branch-protection policy unavailable"))
			return
		}
		if res.Policy != nil {
			ctx = githttp.WithPolicy(ctx, res.Policy)
		}
		if res.Events != nil {
			ctx = githttp.WithEvents(ctx, res.Events)
		}
	}

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

	// chi prefers the more specific route, so /git/info/refs matches
	// /git/{workspace_id}/* with workspace_id="info". Detect this case
	// by checking if the captured workspace_id is actually a git protocol
	// segment, and fall back to single-workspace mode.
	if isGitProtocolSegment(workspaceID) {
		if wildcard != "" {
			return "", "/" + workspaceID + "/" + wildcard
		}
		return "", "/" + workspaceID
	}

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

// isGitProtocolSegment returns true if a single path segment is a known
// git protocol root ("info", "objects", "git-upload-pack", etc.).
func isGitProtocolSegment(seg string) bool {
	switch seg {
	case "info", "objects", "git-upload-pack", "git-receive-pack", "HEAD":
		return true
	}
	return false
}

// resolveGitWorkspace resolves the workspace config for git access.
func (s *Server) resolveGitWorkspace(ctx context.Context, workspaceID string) (*workspace.Config, error) {
	if s.wsResolver == nil {
		return nil, fmt.Errorf("workspace resolver not configured")
	}
	return s.wsResolver.Resolve(ctx, workspaceID)
}

// validateGitAuth validates the bearer token for non-trusted git requests.
// Returns the authenticated actor so the caller can attach it to the
// request context — the push path's pre-receive check needs the actor
// identity to evaluate branch-protection rules.
func (s *Server) validateGitAuth(r *http.Request, cfg *workspace.Config) (*domain.Actor, error) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return nil, domain.NewError(domain.ErrUnauthorized, "authorization required for external git access")
	}

	if len(header) < 7 || !strings.EqualFold(header[:7], "bearer ") {
		return nil, domain.NewError(domain.ErrUnauthorized, "invalid authorization header format")
	}
	token := header[7:]
	if token == "" {
		return nil, domain.NewError(domain.ErrUnauthorized, "invalid authorization header format")
	}

	// Use workspace-scoped auth if available, otherwise server-level.
	// The Release must run on every path that Get succeeded on — even
	// when ss.Auth is nil or ValidateToken below fails — otherwise
	// repeated auth failures leak pool references and eventually
	// exhaust the workspace pool.
	authSvc := s.auth
	if s.servicePool != nil {
		ss, err := s.servicePool.Get(r.Context(), cfg.ID)
		if err == nil {
			defer s.servicePool.Release(cfg.ID)
			if ss.Auth != nil {
				authSvc = ss.Auth
			}
		}
	}

	if authSvc == nil {
		return nil, domain.NewError(domain.ErrUnavailable, "authentication not configured")
	}

	return authSvc.ValidateToken(r.Context(), token)
}
