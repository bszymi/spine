package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/bszymi/spine/core/auth"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/observe"
	"github.com/bszymi/spine/service/internal/workspace"
)

// Context keys are empty structs so they collide only with themselves and
// their label never leaks when a context is formatted for debug output.
type (
	actorKey               struct{}
	workspaceKey           struct{}
	serviceSetKey          struct{}
	workspaceErrKey        struct{}
	releaseWorkspaceRefKey struct{}
)

// releaseWorkspaceRef invokes the request's pool-release function if
// the workspace middleware installed one. Long-lived handlers (e.g.
// SSE streams) call this after they've finished using the
// ServiceSet's underlying store so a binding invalidation does not
// have to wait on the entire request lifetime to evict and rebuild
// the workspace's services. The middleware's deferred release is
// idempotent (sync.Once-protected), so calling this early is safe.
func releaseWorkspaceRef(ctx context.Context) {
	if release, ok := ctx.Value(releaseWorkspaceRefKey{}).(func()); ok {
		release()
	}
}

// pendingWorkspaceErr returns the workspace-resolution error that
// workspaceMiddleware deferred to authMiddleware (and ok=false if there is
// none). The error is only surfaced after the bearer token has been
// validated, so unauthenticated callers cannot enumerate workspace
// existence/state by probing distinct error codes.
func pendingWorkspaceErr(ctx context.Context) (error, bool) {
	v := ctx.Value(workspaceErrKey{})
	if v == nil {
		return nil, false
	}
	return v.(error), true
}

// WorkspaceHeader is the HTTP header used to pass workspace ID.
const WorkspaceHeader = "X-Workspace-ID"

// actorFromContext returns the authenticated actor from the request context.
// The gateway stores the actor under both its private key (for handler code
// that stayed local to this package before the domain helper existed) and
// the shared domain key (so downstream services like internal/artifact can
// read the actor without importing gateway). Reads prefer the local key
// but fall back to the shared one.
func actorFromContext(ctx context.Context) *domain.Actor {
	if actor, ok := ctx.Value(actorKey{}).(*domain.Actor); ok {
		return actor
	}
	return domain.ActorFromContext(ctx)
}

// WorkspaceConfigFromContext returns the resolved workspace config from the request context.
func WorkspaceConfigFromContext(ctx context.Context) *workspace.Config {
	cfg, _ := ctx.Value(workspaceKey{}).(*workspace.Config)
	return cfg
}

// serviceSetFromContext returns the workspace-scoped service set from the request context.
func serviceSetFromContext(ctx context.Context) *workspace.ServiceSet {
	ss, _ := ctx.Value(serviceSetKey{}).(*workspace.ServiceSet)
	return ss
}

// workspaceMiddleware resolves the workspace from the X-Workspace-ID header.
// In shared mode, the header is required — missing or invalid IDs are rejected.
// In single mode (FileProvider), an empty header falls back to the default workspace.
//
// Resolution failures are not written to the response here. They are stashed
// in the request context so authMiddleware can run first and reject
// unauthenticated callers with a uniform 401 — without leaking whether the
// workspace exists, is inactive, or is currently unavailable. Authenticated
// callers see the original tenant-state error after the bearer is validated.
//
// It resolves the workspace's configuration only. It never builds the
// workspace's ServiceSet (INIT-024 TASK-007, review finding N-15): that
// opens the workspace's database pool, seeds the bootstrap admin and
// sweeps its subscription secrets, and it ran here for every request —
// before authMiddleware had seen a bearer — so an anonymous caller could
// make Spine load any tenant, and a build failure answered 500 where a
// missing workspace answered 401, telling the two apart. authMiddleware
// acquires the ServiceSet (acquireServiceSet) once a well-formed bearer
// is present, because the bearer can only be checked against that
// workspace's own token store.
func (s *Server) workspaceMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.wsResolver == nil {
			next.ServeHTTP(w, r)
			return
		}

		workspaceID := r.Header.Get(WorkspaceHeader)

		cfg, err := s.wsResolver.Resolve(r.Context(), workspaceID)
		if err != nil {
			ctx := context.WithValue(r.Context(), workspaceErrKey{}, mapWorkspaceResolveErr(err, workspaceID))
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		ctx := context.WithValue(r.Context(), workspaceKey{}, cfg)
		ctx = observe.WithWorkspaceID(ctx, cfg.ID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// acquireServiceSet takes a pool reference on the workspace's ServiceSet
// (building it on first use) and returns the request context carrying it,
// plus the release function the caller must defer. The release is
// sync.Once-guarded and also stored in the context, so a long-lived
// handler (handleEventStream) can drop the reference early with
// releaseWorkspaceRef once it no longer needs the ServiceSet's store, and
// the caller's deferred release stays a safe no-op.
//
// Only authMiddleware calls it, and only for a request that carries a
// well-formed bearer (INIT-024 TASK-007, N-15).
func (s *Server) acquireServiceSet(ctx context.Context, workspaceID string) (context.Context, func(), error) {
	ss, err := s.servicePool.Get(ctx, workspaceID)
	if err != nil {
		return ctx, func() {}, err
	}
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { s.servicePool.Release(workspaceID) })
	}
	ctx = context.WithValue(ctx, serviceSetKey{}, ss)
	ctx = context.WithValue(ctx, releaseWorkspaceRefKey{}, release)
	return ctx, release, nil
}

// uniformUnauthorizedMessage is the one message every pre-authentication
// refusal answers in shared mode, whatever its cause (writeUniformUnauthorized).
const uniformUnauthorizedMessage = "authentication required"

// writeUniformUnauthorized writes the shared-mode pre-authentication 401
// (INIT-024 TASK-007, N-15). Status and body are byte-identical for every
// cause, so a caller who has not proven a bearer cannot learn whether the
// workspace exists, is inactive or unavailable, failed to build, has no
// auth service, or whether its token was unknown, expired or revoked.
func writeUniformUnauthorized(w http.ResponseWriter) {
	WriteError(w, domain.NewError(domain.ErrUnauthorized, uniformUnauthorizedMessage))
}

// bearerFromRequest extracts the bearer token from the Authorization header
// without any I/O. The scheme is case-insensitive (RFC 7235). The error is
// the single-mode response for a missing or malformed header; shared mode
// answers every such request with writeUniformUnauthorized instead.
func bearerFromRequest(r *http.Request) (string, error) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return "", domain.NewError(domain.ErrUnauthorized, "authorization header required")
	}
	if len(header) < 7 || !strings.EqualFold(header[:7], "bearer ") {
		return "", domain.NewError(domain.ErrUnauthorized, "invalid authorization header format")
	}
	token := header[7:]
	if token == "" {
		return "", domain.NewError(domain.ErrUnauthorized, "invalid authorization header format")
	}
	return token, nil
}

// mapWorkspaceResolveErr translates a workspace.Resolver error into the
// public-facing domain error (and message) that authMiddleware will surface
// to authenticated callers.
func mapWorkspaceResolveErr(err error, workspaceID string) error {
	switch {
	case errors.Is(err, workspace.ErrWorkspaceNotFound):
		if workspaceID == "" {
			return domain.NewError(domain.ErrInvalidParams, "X-Workspace-ID header is required")
		}
		return domain.NewError(domain.ErrNotFound, fmt.Sprintf("workspace %q not found", workspaceID))
	case errors.Is(err, workspace.ErrWorkspaceInactive):
		return domain.NewError(domain.ErrForbidden, fmt.Sprintf("workspace %q is inactive", workspaceID))
	case errors.Is(err, workspace.ErrWorkspaceUnavailable):
		// Transient: secret store down, missing/denied runtime_db, or
		// platform binding fetch failure. 503 so callers retry instead
		// of treating it as a permanent 500.
		return domain.NewError(domain.ErrUnavailable, fmt.Sprintf("workspace %q is currently unavailable", workspaceID))
	default:
		return domain.NewError(domain.ErrInternal, "failed to resolve workspace")
	}
}

// authMiddleware validates the Bearer token and sets the actor in context.
// Uses the workspace-scoped auth service when available (shared multi-tenant mode),
// falling back to the server-level auth service (single-workspace mode).
// Fails closed: if no auth service is available, all authenticated routes return 401.
//
// Any workspace-resolution error stashed by workspaceMiddleware is only
// surfaced after the bearer has been successfully validated. Unauthenticated
// callers always receive a uniform 401 so they cannot enumerate workspace
// IDs or status by probing distinct error codes.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.servicePool != nil && s.wsResolver != nil {
			s.authenticatePooled(w, r, next)
			return
		}

		wsErr, hasWSErr := pendingWorkspaceErr(r.Context())

		authSvc := s.authFrom(r.Context())
		if authSvc == nil {
			// In pool-backed shared mode the server-level auth is nil
			// and the workspace's auth.Service only becomes available
			// after a successful resolve. If resolution failed there
			// is no store to validate the bearer against — collapse to
			// a uniform 401 so the missing auth service cannot itself
			// be used to enumerate workspace state. Authenticated
			// callers in this mode forfeit the granular 404/403/503
			// they would have seen pre-fix; that trade is the point of
			// TASK-030. Without a pending workspace error this is a
			// genuine server config issue, so keep the original 503.
			if hasWSErr {
				WriteError(w, domain.NewError(domain.ErrUnauthorized, "authentication required"))
				return
			}
			WriteError(w, domain.NewError(domain.ErrUnavailable, "authentication not configured"))
			return
		}

		token, err := bearerFromRequest(r)
		if err != nil {
			WriteError(w, err)
			return
		}

		actor, err := authSvc.ValidateToken(r.Context(), token)
		if err != nil {
			WriteError(w, err)
			return
		}

		// Bearer is now proven good; safe to surface the deferred
		// workspace-resolution error to the authenticated caller.
		if hasWSErr {
			WriteError(w, wsErr)
			return
		}

		// Defense-in-depth: when a workspace is in scope, re-verify the
		// actor is present in the workspace-scoped store. Today this is
		// redundant with ValidateToken (it hits the same store), but it
		// makes the actor-belongs-to-workspace invariant an explicit
		// middleware concern rather than an implicit artifact of the
		// service-pool routing. A future code path that validates tokens
		// against a shared identity source would still be blocked here.
		if cfg := WorkspaceConfigFromContext(r.Context()); cfg != nil {
			if st := s.storeFrom(r.Context()); st != nil {
				if _, gerr := st.GetActor(r.Context(), actor.ActorID); gerr != nil {
					observe.Logger(r.Context()).Warn("actor not a member of requested workspace",
						"actor_id", actor.ActorID,
						"workspace_id", cfg.ID,
						"error", gerr.Error(),
					)
					WriteError(w, domain.NewError(domain.ErrForbidden, "actor has no membership in this workspace"))
					return
				}
			}
		}

		ctx := context.WithValue(r.Context(), actorKey{}, actor)
		ctx = domain.WithActor(ctx, actor)
		ctx = observe.WithActorID(ctx, actor.ActorID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// authenticatePooled is authMiddleware for shared mode — a ServicePool and
// a workspace resolver, as `spine serve` wires for WORKSPACE_RESOLVER=db and
// platform-binding (INIT-024 TASK-007, review finding N-15).
//
// The bearer can only be checked against the workspace's own token store,
// which lives in its ServiceSet, so authentication and the ServiceSet build
// are ordered:
//
//  1. Bearer shape (no I/O). A request without a well-formed bearer never
//     reaches servicePool.Get: no database pool is opened, no bootstrap
//     admin seeded, no secret sweep run for an anonymous caller.
//  2. A workspace that did not resolve (missing, inactive, unavailable,
//     no X-Workspace-ID) has nothing to validate against: uniform 401, no
//     build. Unchanged from INIT-008 TASK-030.
//  3. Build (or reuse) the ServiceSet, then validate the bearer against
//     its auth service.
//
// Until step 3 has validated the bearer, every refusal is
// writeUniformUnauthorized: the same status and body for a missing header,
// a malformed one, an unresolved workspace, a ServiceSet that failed to
// build, a workspace with no auth service, an unknown, expired or revoked
// token, a suspended actor, and a token-store error. Each of these used to
// answer differently (500 for a build failure, 503 for a missing auth
// service, 500 for a store error, a distinct 401 message per token state),
// so any caller — even one sending "Bearer x" — could tell an existing
// workspace from a missing one. The cause is logged server-side instead
// (never the token), so an outage still shows up for operators. A caller
// whose bearer is valid but whose workspace cannot be built also gets the
// 401: without the workspace's store there is no way to know it is valid
// (Implementation Notes, deviation from AC 3's 500).
//
// After step 3 the actor is authenticated and the original granular
// errors apply: the membership check's 403, and every handler error.
func (s *Server) authenticatePooled(w http.ResponseWriter, r *http.Request, next http.Handler) {
	ctx := r.Context()
	log := observe.Logger(ctx)

	token, tokenErr := bearerFromRequest(r)
	if tokenErr != nil {
		writeUniformUnauthorized(w)
		return
	}
	if _, hasWSErr := pendingWorkspaceErr(ctx); hasWSErr {
		writeUniformUnauthorized(w)
		return
	}
	cfg := WorkspaceConfigFromContext(ctx)
	if cfg == nil || cfg.ID == "" {
		// A resolver that returned no error and no config. Defensive:
		// nothing to build or validate against.
		log.Error("workspace resolver returned no configuration; refusing the request")
		writeUniformUnauthorized(w)
		return
	}

	ctx, release, err := s.acquireServiceSet(ctx, cfg.ID)
	if err != nil {
		log.Warn("workspace services could not be built for a request that is not yet authenticated; answering the uniform 401",
			"workspace_id", cfg.ID, "error", err)
		writeUniformUnauthorized(w)
		return
	}
	defer release()

	// The workspace's own auth service only: falling back to a
	// process-level one would validate this tenant's bearer against
	// another store.
	ss := serviceSetFromContext(ctx)
	if ss == nil || ss.Auth == nil {
		log.Error("workspace has no auth service (no runtime database?); answering the uniform 401",
			"workspace_id", cfg.ID)
		writeUniformUnauthorized(w)
		return
	}

	actor, err := ss.Auth.ValidateToken(ctx, token)
	if err != nil {
		var se *domain.SpineError
		if !errors.As(err, &se) || se.Code != domain.ErrUnauthorized {
			log.Warn("bearer validation failed on the workspace token store; answering the uniform 401",
				"workspace_id", cfg.ID, "error", err)
		}
		writeUniformUnauthorized(w)
		return
	}

	// Defense-in-depth membership check, as in single mode: the actor
	// must exist in this workspace's own store (never a process-level
	// fallback).
	if ss.Store != nil {
		if _, gerr := ss.Store.GetActor(ctx, actor.ActorID); gerr != nil {
			log.Warn("actor not a member of requested workspace",
				"actor_id", actor.ActorID,
				"workspace_id", cfg.ID,
				"error", gerr.Error(),
			)
			WriteError(w, domain.NewError(domain.ErrForbidden, "actor has no membership in this workspace"))
			return
		}
	}

	ctx = context.WithValue(ctx, actorKey{}, actor)
	ctx = domain.WithActor(ctx, actor)
	ctx = observe.WithActorID(ctx, actor.ActorID)
	next.ServeHTTP(w, r.WithContext(ctx))
}

// authorize checks if the authenticated actor has permission for the operation.
// Returns false and writes an error response if not authorized.
// In dev mode, unauthenticated requests are allowed (no actor in context).
func (s *Server) authorize(w http.ResponseWriter, r *http.Request, op auth.Operation) bool {
	actor := actorFromContext(r.Context())
	if actor == nil {
		if s.devMode {
			return true
		}
		WriteError(w, domain.NewError(domain.ErrUnauthorized, "authentication required"))
		return false
	}
	if err := auth.Authorize(actor, op); err != nil {
		WriteError(w, err)
		return false
	}
	return true
}

// validTraceID matches alphanumeric characters and hyphens, 8-64 chars.
var validTraceID = regexp.MustCompile(`^[a-zA-Z0-9\-]{8,64}$`)

// traceIDMiddleware extracts or generates a trace ID and propagates it
// through the request context and response header.
// Client-provided trace IDs are validated to prevent log injection.
func traceIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := r.Header.Get("X-Trace-Id")
		if traceID == "" || !validTraceID.MatchString(traceID) {
			generated, err := observe.GenerateTraceID()
			if err != nil {
				traceID = fmt.Sprintf("fallback-%d", time.Now().UnixNano())
			} else {
				traceID = generated
			}
		}

		ctx := observe.WithTraceID(r.Context(), traceID)
		ctx = observe.WithComponent(ctx, "gateway")
		w.Header().Set("X-Trace-Id", traceID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// loggingMiddleware logs each request with method, path, status, and duration.
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(sw, r)

		log := observe.Logger(r.Context())
		log.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

// statusWriter wraps http.ResponseWriter to capture the status code.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

// Flush forwards to the embedded ResponseWriter if it supports Flusher.
// Without this, wrapping breaks SSE handlers that type-assert to http.Flusher.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap exposes the underlying ResponseWriter for http.NewResponseController,
// which lets the SSE handler reach SetWriteDeadline on the real writer.
func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// securityHeadersMiddleware sets standard security response headers.
func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// recoveryMiddleware catches panics and returns a 500 JSON error response.
func recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log := observe.Logger(r.Context())
				log.Error("panic recovered",
					"panic", fmt.Sprintf("%v", rec),
					"stack", string(debug.Stack()),
				)
				WriteError(w, domain.NewError(domain.ErrInternal, "internal server error"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}
