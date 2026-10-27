package gateway

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/bszymi/spine/adapters/gitpool"
	"github.com/bszymi/spine/adapters/repository"
	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/event"
	"github.com/bszymi/spine/core/observe"
	"github.com/bszymi/spine/core/refadmit"
	"github.com/bszymi/spine/service/internal/githttp"
	"github.com/bszymi/spine/service/internal/workspace"
)

// GitPushResources bundles per-workspace state the pre-receive gate
// needs: the branch-protection policy for evaluating ref updates, the
// event emitter for honored-override governance events (ADR-009 §4),
// and the run reader plus step-definition lookup the ref-class
// admission uses for run, plan and divergence branches (INIT-023
// TASK-016). All of them come off the target workspace's ServiceSet so
// shared-mode deployments evaluate, admit and audit against the right
// workspace.
//
// Runs and StepDefs are a pair: a nil Runs (a workspace with no
// database yet) or a nil StepDefs (no engine wired) makes run, plan and
// divergence branches operator-only for that push, which fails closed.
type GitPushResources struct {
	Policy   branchprotect.Policy
	Events   event.Emitter
	Runs     refadmit.RunReader
	StepDefs refadmit.StepDefLookup
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

	// Auth gate (INIT-023 TASK-015, audit P3-30 — invariants I3, I4).
	//
	//  - Read-only (clone/fetch) from a trusted-CIDR peer bound to the
	//    requested workspace (INIT-024 TASK-006, below) skips the bearer
	//    check: the runner-container affordance. The peer is
	//    the TCP peer of a DIRECT connection only (gitPeerIP). A request
	//    whose peer is a declared reverse proxy
	//    (SPINE_TRUSTED_PROXY_CIDRS), or that carries a forwarding
	//    header (X-Forwarded-For, Forwarded, X-Real-IP, ...), relays some
	//    other client and is never token-less: a proxy whose own address
	//    sat inside SPINE_GIT_HTTP_TRUSTED_CIDRS (Docker's bridge gateway
	//    fronting a published port, the compose default /16) gave every
	//    client it relayed token-less read access to every workspace's
	//    repository. No forwarded address is trusted either, because a
	//    proxy that passes the client's header through lets the client
	//    name a runner address (owner question 6 default, pending the
	//    owner). A TCP-level proxy that adds no header cannot be seen
	//    here; the docs and the compose default cover it.
	//  - Push (git-receive-pack) with the flag ON: no bypass applies.
	//    Every push carries a bearer, validated and attached as the
	//    actor for ref admission (TASK-016), branch protection and
	//    audit.
	//  - Push with the flag OFF: the auth gate applies as well
	//    (INIT-024 TASK-006). A caller authenticated for the workspace
	//    gets the handler's 403 naming `SPINE_GIT_RECEIVE_PACK_ENABLED`;
	//    any other caller (no token, a junk or non-bearer header, a
	//    token of another workspace) gets the uniform 401, whatever the
	//    workspace's state. The flag's 403 used to skip the gate when
	//    the workspace resolved, so it answered only for workspaces
	//    that exist.
	//  - SPINE_DEV_MODE has no effect here (it used to drop git auth for
	//    clone AND push whenever SPINE_ENV was not production). Only the
	//    dedicated SPINE_GIT_HTTP_DEV_NO_AUTH flag, which cmd/spine
	//    refuses unless SPINE_ENV=development, skips the bearer — and
	//    only for reads.
	//
	// "Push" is githttp.IsReceivePack, which also counts an info/refs
	// query other than the three a git client sends as a push: git-http-
	// backend parses the query itself and may read receive-pack where
	// url.ParseQuery does not (`service&git-receive-pack`), so such a
	// request never takes a token-less read path, and the handler then
	// refuses it with 400 (INIT-023 TASK-015 review round 2).
	push := githttp.IsReceivePack(r)
	// pushDisabled marks a canonical push request with the flag off: the
	// handler refuses it with the flag's 403 before touching any
	// repository. It is not an auth bypass (above); it only keeps the
	// repository lookup below out of the way, so an authenticated caller
	// gets that 403 rather than a repository error. A non-canonical
	// info/refs query is not one: the handler answers it 400 (wave 3
	// review), after the auth gate like any other request.
	pushDisabled := push && !githttp.IsNonCanonicalInfoRefs(r) && !s.gitHTTP.ReceivePackEnabled()
	// The trusted-CIDR read bypass is scoped to the workspace the request
	// resolved to (INIT-024 TASK-006, review finding N-2). It used to be
	// decided from the peer address alone, so in shared mode a runner
	// host trusted for one tenant — or any job running on it — read
	// every tenant's repository token-less, and the answer differed for
	// missing and present workspaces. Now:
	//
	//  - The trust is looked up under cfg.ID, the workspace whose
	//    repository (cfg.RepoPath, or a code repo of cfg's own pool) is
	//    served below — never under a raw URL segment, so no spelling of
	//    the path can pair one workspace's trust with another's
	//    repository.
	//  - A workspace that did not resolve (missing, inactive,
	//    unavailable) takes no bypass, so a trusted peer meets the auth
	//    gate's uniform 401 there, exactly as an untrusted one does; and a
	//    real workspace the peer is not bound to answers that same 401.
	//    The existence oracle closes with the cross-tenant read.
	//  - Single mode has one workspace, which `spine serve` binds the
	//    bare SPINE_GIT_HTTP_TRUSTED_CIDRS list to; shared mode refuses
	//    the bare list at startup and takes only the per-workspace form.
	//    The decision here is the same in both modes and needs no mode
	//    test: a workspace without a binding is never token-less.
	trustedBypass := false
	if !push && resolveErr == nil && cfg != nil {
		if ip := gitPeerIP(r, s.trustedProxyCIDRs); ip != nil {
			trustedBypass = s.gitHTTP.IsTrustedIPForWorkspace(cfg.ID, ip.String())
		} else if s.gitHTTP.IsTrustedIPForWorkspace(cfg.ID, r.RemoteAddr) {
			s.warnProxiedTrustedGitPeer(r)
		}
	}
	devBypass := !push && s.gitDevNoAuth
	authBypass := trustedBypass || devBypass
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
		//    the failure as bad credentials. Single mode only: in
		//    shared mode validatePooledGitAuth answers every
		//    failure with the uniform 401 (INIT-024 TASK-007).
		//  - Except a 401 itself, which is always the uniform one
		//    (INIT-024 TASK-006 adversarial review): its message
		//    ("invalid token", "invalid authorization header format",
		//    "token expired", ...) answered only for a workspace that
		//    resolved, so the answer to a junk bearer depended on the
		//    workspace's state.
		a, err := s.validateGitAuth(r, cfg)
		if err != nil {
			if resolveErr != nil {
				s.logTrustedPeerResolveFailure(r, workspaceID, resolveErr)
			}
			if resolveErr != nil || isUnauthorizedError(err) {
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
	// A push with the flag off is refused by the inner handler with
	// the 403 naming `SPINE_GIT_RECEIVE_PACK_ENABLED` before it touches
	// any repository. Resolving the repository here would replace that
	// 403 with a 404/412 about the repository ID. Use the workspace
	// primary path; the inner handler will refuse before touching it.
	var repoPath string
	if pushDisabled {
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
	// The repository segment of the URL ("" or "spine" for the primary)
	// rides along for the ref-class admission, which requires a run
	// branch's bound execution to target the same repository (INIT-023
	// TASK-016). Only the push path reads it.
	ctx = githttp.WithRepositoryID(ctx, repositoryID)
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
		// The admission's run reader and step-definition lookup travel
		// as a pair, and are attached even when nil: the resolver has
		// decided for this workspace, so githttp must not fall back to
		// its process-level pair (another workspace's store in shared
		// mode). A nil reader or lookup makes run, plan and divergence
		// refs operator-only for this push. The resolver's release is
		// deferred past ServeHTTP above, so a pooled workspace store
		// outlives the push.
		ctx = githttp.WithRunRefs(ctx, res.Runs)
		ctx = githttp.WithStepDefs(ctx, res.StepDefs)
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

	// Shared mode: the resolved workspace's own auth service only
	// (validatePooledGitAuth, INIT-024 TASK-007). Otherwise the
	// server-level one.
	//
	// cfg may be nil when the caller invoked us before workspace
	// resolution succeeded (TASK-031 defers state-leaking workspace
	// errors past the auth gate). Skip the pool lookup in that case
	// — server-level auth still applies in single mode, and in shared
	// mode (no server-level auth) handleGit answers the uniform 401.
	if cfg != nil && s.servicePool != nil {
		return s.validatePooledGitAuth(r, cfg.ID, token)
	}

	authSvc := s.auth
	if authSvc == nil {
		return nil, domain.NewError(domain.ErrUnavailable, "authentication not configured")
	}

	return authSvc.ValidateToken(r.Context(), token)
}

// logTrustedPeerResolveFailure records, at Info, a request from a peer
// trusted for the workspace its URL names whose resolution failed
// (missing, inactive, unavailable): the peer gets the uniform 401 like
// any caller (INIT-024 TASK-006), so this line is the operator's only
// trace of why a configured runner stopped cloning. Log only; it
// decides nothing. The URL's workspace segment is used here because
// no workspace resolved; it is logged only when it equals a configured
// binding (IsTrustedIPForWorkspace matches exactly), so no
// client-chosen string reaches the log.
func (s *Server) logTrustedPeerResolveFailure(r *http.Request, workspaceID string, resolveErr error) {
	if workspaceID == "" {
		return
	}
	ip := gitPeerIP(r, s.trustedProxyCIDRs)
	if ip == nil || !s.gitHTTP.IsTrustedIPForWorkspace(workspaceID, ip.String()) {
		return
	}
	observe.Logger(r.Context()).Info("git: token-less access refused for a trusted-CIDR peer because its workspace did not resolve; it gets the uniform 401",
		"workspace_id", workspaceID, "remote_addr", r.RemoteAddr, "error", resolveErr)
}

// isUnauthorizedError reports whether err is a SpineError with code
// ErrUnauthorized (a missing, malformed, unknown, expired or revoked
// bearer, or an inactive actor).
func isUnauthorizedError(err error) bool {
	var se *domain.SpineError
	return errors.As(err, &se) && se.Code == domain.ErrUnauthorized
}

// validatePooledGitAuth validates a /git bearer in shared mode against the
// workspace's own auth service (INIT-024 TASK-007, review finding N-15 —
// the /git half of authenticatePooled).
//
// The ServiceSet is built only here, after validateGitAuth has seen a
// well-formed bearer. Until the bearer is validated every failure is the
// gate's uniform 401 message, and its cause is logged instead:
//
//   - a ServiceSet build failure. It used to fall back to the
//     process-level auth service, which shared mode leaves nil, and so
//     answer 503 "authentication not configured" where a missing
//     workspace answered 401 — an existence oracle for any caller sending
//     "Bearer x";
//   - a workspace whose ServiceSet has no auth service (same 503). It is
//     never served by the process-level service either: that would check
//     this tenant's bearer against another store;
//   - a token-store error (it used to surface as a 500);
//   - an unknown, expired or revoked token or an inactive actor, whose
//     distinct messages ("invalid token", "token expired", ...) answered
//     only for a workspace that resolved. (INIT-024 TASK-006 also makes
//     handleGit answer every /git 401 with the uniform message, which
//     covers the missing and malformed headers this function refuses
//     before any build.)
//
// There is no fallback to the server-level auth service in shared mode,
// whatever is configured: a test server that sets one on a pooled server
// must give the workspaces' ServiceSets their auth instead.
func (s *Server) validatePooledGitAuth(r *http.Request, workspaceID, token string) (*domain.Actor, error) {
	log := observe.Logger(r.Context())
	uniform := domain.NewError(domain.ErrUnauthorized, "authorization required for external git access")

	// The Release must run on every path that Get succeeded on — even
	// when ss.Auth is nil or ValidateToken below fails — otherwise
	// repeated auth failures leak pool references and eventually
	// exhaust the workspace pool.
	ss, err := s.servicePool.Get(r.Context(), workspaceID)
	if err != nil {
		log.Warn("workspace services could not be built for a git request that is not yet authenticated; answering the uniform 401",
			"workspace_id", workspaceID, "error", describePreauthErr(err))
		return nil, uniform
	}
	defer s.servicePool.Release(workspaceID)
	if ss.Auth == nil {
		log.Error("workspace has no auth service (no runtime database?); answering the uniform 401",
			"workspace_id", workspaceID)
		return nil, uniform
	}
	actor, err := ss.Auth.ValidateToken(r.Context(), token)
	if err != nil {
		var se *domain.SpineError
		if !errors.As(err, &se) || se.Code != domain.ErrUnauthorized {
			log.Warn("git bearer validation failed on the workspace token store; answering the uniform 401",
				"workspace_id", workspaceID, "error", describePreauthErr(err))
		}
		return nil, uniform
	}
	return actor, nil
}

// gitForwardingHeaders are request headers a proxy adds. Their presence
// shows the request was relayed, so its TCP peer is not the client.
var gitForwardingHeaders = []string{
	"X-Forwarded-For",
	"Forwarded",
	"X-Real-Ip",
	"X-Forwarded-Host",
	"X-Forwarded-Proto",
	"Via",
}

// forwardingHeadersPresent returns the names (from gitForwardingHeaders)
// of the forwarding headers r carries, present even if empty. Matching
// ignores case, so a header map built in-process without canonical keys
// is judged the same as one net/http parsed.
func forwardingHeadersPresent(r *http.Request) []string {
	var out []string
	for _, h := range gitForwardingHeaders {
		for k := range r.Header {
			if strings.EqualFold(k, h) {
				out = append(out, h)
				break
			}
		}
	}
	return out
}

// gitPeerIP returns the client address the git trusted-CIDR bypass is
// evaluated against, or nil when the request must present a token
// (INIT-023 TASK-015, audit P3-30, invariant I3). Only a direct peer
// qualifies:
//
//   - RemoteAddr inside SPINE_TRUSTED_PROXY_CIDRS: nil. A declared proxy
//     relays arbitrary clients, and its X-Forwarded-For is not trusted
//     for access either: a proxy that passes the client's own header
//     through lets the client name a runner address. (The rate limiter
//     does read it, to pick a bucket — clientIPForRateLimit.)
//   - A forwarding header (gitForwardingHeaders) present: nil. The
//     request came through a proxy the operator did not declare. A
//     direct git client sends none of them, and a client that adds one
//     only loses the bypass.
//   - Otherwise the parsed RemoteAddr; an unparsable one is nil.
func gitPeerIP(r *http.Request, trustedProxies []*net.IPNet) net.IP {
	remote := net.ParseIP(extractIP(r))
	if remote == nil {
		return nil
	}
	if ipInNets(remote, trustedProxies) {
		return nil
	}
	if len(forwardingHeadersPresent(r)) > 0 {
		return nil
	}
	return remote
}

// gitProxyWarnInterval bounds how often warnProxiedTrustedGitPeer logs.
const gitProxyWarnInterval = time.Minute

// warnProxiedTrustedGitPeer logs, at most once per gitProxyWarnInterval,
// that a read whose TCP peer is inside SPINE_GIT_HTTP_TRUSTED_CIDRS was
// not given token-less access because it was proxied — the case where
// behaviour changed in INIT-023 TASK-015. Header values are not logged.
func (s *Server) warnProxiedTrustedGitPeer(r *http.Request) {
	now := time.Now().UnixNano()
	last := s.gitProxyWarnAt.Load()
	if last != 0 && now-last < int64(gitProxyWarnInterval) {
		return
	}
	if !s.gitProxyWarnAt.CompareAndSwap(last, now) {
		return
	}
	remote := net.ParseIP(extractIP(r))
	observe.Logger(r.Context()).Warn("git: token-less access refused for a trusted-CIDR peer because the request was proxied; "+
		"a peer in SPINE_TRUSTED_PROXY_CIDRS or a request carrying a forwarding header must present a bearer token "+
		"(rate-limited; declare reverse proxies in SPINE_TRUSTED_PROXY_CIDRS)",
		"remote_addr", r.RemoteAddr,
		"declared_proxy", remote != nil && ipInNets(remote, s.trustedProxyCIDRs),
		"forwarding_headers", forwardingHeadersPresent(r))
}
