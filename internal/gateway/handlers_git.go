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
	"github.com/bszymi/spine/internal/gitpool"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/repository"
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

	// Resolve workspace and repository from URL path.
	workspaceID, repositoryID, gitPath := parseGitPath(r)

	// Resolve workspace, but defer surfacing tenant-state errors
	// until we know the caller is allowed to learn them. Distinct
	// 404/403/503 responses on this endpoint would otherwise let an
	// untrusted unauthenticated client enumerate workspace IDs and
	// status by walking /git/{id}/info/refs (EPIC-004 TASK-031).
	cfg, resolveErr := s.resolveGitWorkspace(r.Context(), workspaceID)

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
	// pushDisabledShortCircuit only bypasses auth when the workspace
	// actually resolved. Otherwise an untrusted unauthenticated client
	// could probe `/git/{id}/git-receive-pack` with the flag off and
	// distinguish missing/inactive/unavailable workspaces by the leaked
	// error code. With this guard, missing-workspace probes go through
	// the auth gate and collapse to a uniform 401, while a flag-off
	// request against a real workspace still surfaces the operator-
	// friendly 403 with `SPINE_GIT_RECEIVE_PACK_ENABLED` guidance.
	pushDisabledShortCircuit := push && !s.gitHTTP.ReceivePackEnabled() && resolveErr == nil
	trustedBypass := !push && s.gitHTTP.IsTrustedIP(r.RemoteAddr)
	authBypass := pushDisabledShortCircuit || trustedBypass || s.devMode
	var actor *domain.Actor
	if !authBypass {
		// validateGitAuth tolerates a nil cfg — when workspace
		// resolution failed we still attempt server-level auth so
		// a missing/inactive/unavailable workspace returns the same
		// uniform 401 as a missing or invalid bearer.
		//
		// Failure handling splits on resolveErr:
		//  - resolveErr != nil: collapse every auth failure to a
		//    uniform 401. The caller has not authenticated, so we
		//    must not leak whether the workspace is missing,
		//    inactive, or unavailable.
		//  - resolveErr == nil: surface the real auth error. A
		//    valid-workspace outage like "authentication not
		//    configured" (503) or a store error (500) carries a
		//    retryable/server-side signal that operators and
		//    clients need; collapsing it to 401 would misreport
		//    the failure as bad credentials.
		a, err := s.validateGitAuth(r, cfg)
		if err != nil {
			if resolveErr != nil {
				WriteError(w, domain.NewError(domain.ErrUnauthorized, "authorization required for external git access"))
			} else {
				WriteError(w, err)
			}
			return
		}
		actor = a
	}

	// Past the auth gate (or bypassed). Surface the workspace
	// resolution error now — only authenticated or trusted callers
	// reach this point, so distinct codes are safe.
	if resolveErr != nil {
		if errors.Is(resolveErr, workspace.ErrWorkspaceNotFound) {
			if workspaceID == "" {
				WriteError(w, domain.NewError(domain.ErrInvalidParams, "workspace ID required in shared mode"))
			} else {
				WriteError(w, domain.NewError(domain.ErrNotFound, fmt.Sprintf("workspace %q not found", workspaceID)))
			}
			return
		}
		if errors.Is(resolveErr, workspace.ErrWorkspaceInactive) {
			WriteError(w, domain.NewError(domain.ErrForbidden, fmt.Sprintf("workspace %q is inactive", workspaceID)))
			return
		}
		if errors.Is(resolveErr, workspace.ErrWorkspaceUnavailable) {
			WriteError(w, domain.NewError(domain.ErrUnavailable, fmt.Sprintf("workspace %q is currently unavailable", workspaceID)))
			return
		}
		log.Error("workspace resolution failed", "error", resolveErr)
		WriteError(w, domain.NewError(domain.ErrInternal, "workspace resolution failed"))
		return
	}

	// Resolve the repository the request targets. An empty/"spine"
	// segment falls back to the workspace primary so existing single-
	// repo URLs (`/git/{ws}/info/refs`) keep working unchanged.
	// Catalog/binding errors from the registry surface as the
	// SpineError they were wrapped in (404 not found, 412 inactive/
	// unbound) — this is past the auth gate, so distinct codes are
	// safe and operators get an actionable error.
	//
	// The push-disabled short-circuit deliberately skipped the auth
	// gate so the inner handler can emit the uniform 403 with
	// `SPINE_GIT_RECEIVE_PACK_ENABLED` guidance. Resolving the
	// repository here would replace that 403 with a 404/412 carrying
	// the repository ID — leaking repository state to a caller we
	// chose not to authenticate. Use the workspace primary path; the
	// inner handler will refuse before touching it.
	var repoPath string
	if pushDisabledShortCircuit {
		repoPath = cfg.RepoPath
	} else {
		var repoErr error
		repoPath, repoErr = s.resolveGitRepoPath(r.Context(), cfg, repositoryID)
		if repoErr != nil {
			log.Info("git repo resolution failed",
				"workspace_id", cfg.ID, "repository_id", repositoryID, "error", repoErr)
			WriteError(w, repoErr)
			return
		}
	}

	// Rewrite request path to just the git-specific portion.
	r.URL.Path = gitPath

	// Set repo path in context and delegate to git handler. The actor
	// is attached so the handler's pre-receive check can pin the
	// caller identity on every ref update (EPIC-004 TASK-002).
	ctx := githttp.WithRepoPath(r.Context(), repoPath)
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

// parseGitPath extracts the workspace ID, optional repository ID, and
// git-specific path from the request. URL patterns:
//
//	/git/{workspace_id}/{repository_id}/info/refs  -> ("workspace_id", "repository_id", "/info/refs")
//	/git/{workspace_id}/info/refs                  -> ("workspace_id", "", "/info/refs")
//	/git/{workspace_id}/git-upload-pack            -> ("workspace_id", "", "/git-upload-pack")
//	/git/info/refs                                 -> ("", "", "/info/refs")
//	/git/git-upload-pack                           -> ("", "", "/git-upload-pack")
//
// An empty repository ID at the workspace level means "use the
// primary repo" — the caller resolves it through the registry as
// `spine`. The single-mode (no workspace) form preserves the legacy
// behaviour and never carries a repository segment.
func parseGitPath(r *http.Request) (workspaceID, repositoryID, gitPath string) {
	// chi captures workspace_id if the /{workspace_id}/* route matched.
	workspaceID = chi.URLParam(r, "workspace_id")
	wildcard := chi.URLParam(r, "*")

	// chi prefers the more specific route, so /git/info/refs matches
	// /git/{workspace_id}/* with workspace_id="info". Detect this case
	// by checking if the captured workspace_id is actually a git protocol
	// segment, and fall back to single-workspace mode.
	if isGitProtocolSegment(workspaceID) {
		if wildcard != "" {
			return "", "", "/" + workspaceID + "/" + wildcard
		}
		return "", "", "/" + workspaceID
	}

	if workspaceID != "" {
		// Matched /git/{workspace_id}/*. The wildcard is either the
		// raw git path (legacy single-repo form) or repository_id +
		// git path. Discriminate on the first segment: if it is a
		// known git protocol segment, no repository ID was supplied.
		repoID, rest := splitFirstSegment(wildcard)
		if isGitProtocolSegment(repoID) {
			return workspaceID, "", "/" + wildcard
		}
		return workspaceID, repoID, "/" + rest
	}

	// Matched /git/* — the wildcard contains everything after /git/.
	// Check if the first segment is a known git path or a workspace ID.
	if isGitProtocolPath(wildcard) {
		return "", "", "/" + wildcard
	}

	// First segment is the workspace ID. Single-mode (no workspace)
	// never carries a repository segment — the legacy form is
	// `/git/{workspace_id}/...` only. To extend per-repository
	// addressing here we would need a third "is this a repo or a
	// protocol segment" check, which the test matrix below
	// exercises explicitly.
	first, rest := splitFirstSegment(wildcard)
	if rest == "" {
		return first, "", "/"
	}
	repoID, gitRest := splitFirstSegment(rest)
	if isGitProtocolSegment(repoID) {
		return first, "", "/" + rest
	}
	return first, repoID, "/" + gitRest
}

// splitFirstSegment returns ("first", "rest-of-path") with no leading
// slash on either side. A path with no slash returns ("path", "").
func splitFirstSegment(s string) (string, string) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return parts[0], ""
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

// resolveGitRepoPath returns the absolute filesystem path for the
// repository targeted by a git HTTP request. An empty repositoryID or
// the literal `spine` ID resolves to the workspace primary path
// (cfg.RepoPath) so single-repo URLs keep working without any pool
// dependency. For code repos, resolution goes through the workspace's
// gitpool.Pool — `Pool.RepositoryPath` triggers lazy clone-on-miss
// and runs the path-base validation, so the gateway never hands an
// unmaterialised or out-of-base path to git-http-backend.
//
// Pool selection mirrors validateGitAuth's auth-service selection:
//
//   - Shared mode (s.servicePool != nil): the workspace-scoped pool
//     from ServiceSet.GitPool is authoritative, and a servicePool.Get
//     failure must surface to the caller — falling back to
//     s.gitPool here would resolve a `/git/{ws}/{repo}` request
//     against the wrong workspace's registry or mask a workspace
//     outage as a misleading repository error.
//   - Single mode (s.servicePool == nil): s.gitPool is the only
//     pool, and we use it directly.
func (s *Server) resolveGitRepoPath(ctx context.Context, cfg *workspace.Config, repositoryID string) (string, error) {
	if repositoryID == "" || repositoryID == repository.PrimaryRepositoryID {
		return cfg.RepoPath, nil
	}

	var pool *gitpool.Pool
	if s.servicePool != nil {
		if cfg == nil {
			// Caller invariant: cfg is set past the workspace
			// resolution step. Defensive nil-check so a future
			// refactor can't sneak a nil cfg past this branch.
			return "", domain.NewError(domain.ErrInternal,
				"git pool resolution requires a resolved workspace")
		}
		ss, err := s.servicePool.Get(ctx, cfg.ID)
		if err != nil {
			// Surface, do not silently fall back to s.gitPool —
			// that would resolve against the wrong registry in
			// shared mode.
			return "", err
		}
		defer s.servicePool.Release(cfg.ID)
		pool = ss.GitPool
	} else {
		pool = s.gitPool
	}

	if pool == nil {
		return "", domain.NewError(domain.ErrUnavailable,
			fmt.Sprintf("git pool not configured; cannot route repository %q", repositoryID))
	}

	path, err := pool.RepositoryPath(ctx, repositoryID)
	if err != nil {
		return "", err
	}
	// Defence against a binding row whose local_path slipped through
	// to an empty string. `Pool.RepositoryPath` already returns
	// ErrPrecondition for that case, but the explicit check protects
	// the handler from feeding "" into git-http-backend's repo path.
	if path == "" {
		return "", domain.NewError(domain.ErrPrecondition,
			fmt.Sprintf("repository %q has no local clone path", repositoryID))
	}
	return path, nil
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
	//
	// cfg may be nil when the caller invoked us before workspace
	// resolution succeeded (TASK-031 defers state-leaking workspace
	// errors past the auth gate). Skip the pool lookup in that case
	// — server-level auth still applies in single mode.
	authSvc := s.auth
	if cfg != nil && s.servicePool != nil {
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
