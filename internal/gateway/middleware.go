package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"runtime/debug"
	"strings"
	"time"

	"github.com/bszymi/spine/internal/auth"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/workspace"
)

// Context keys are empty structs so they collide only with themselves and
// their label never leaks when a context is formatted for debug output.
type (
	actorKey      struct{}
	workspaceKey  struct{}
	serviceSetKey struct{}
)

// WorkspaceHeader is the HTTP header used to pass workspace ID.
const WorkspaceHeader = "X-Workspace-ID"

// actorFromContext returns the authenticated actor from the request context.
func actorFromContext(ctx context.Context) *domain.Actor {
	actor, _ := ctx.Value(actorKey{}).(*domain.Actor)
	return actor
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
func (s *Server) workspaceMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.wsResolver == nil {
			next.ServeHTTP(w, r)
			return
		}

		workspaceID := r.Header.Get(WorkspaceHeader)

		cfg, err := s.wsResolver.Resolve(r.Context(), workspaceID)
		if err != nil {
			if errors.Is(err, workspace.ErrWorkspaceNotFound) {
				if workspaceID == "" {
					WriteError(w, domain.NewError(domain.ErrInvalidParams, "X-Workspace-ID header is required"))
				} else {
					WriteError(w, domain.NewError(domain.ErrNotFound, fmt.Sprintf("workspace %q not found", workspaceID)))
				}
				return
			}
			if errors.Is(err, workspace.ErrWorkspaceInactive) {
				WriteError(w, domain.NewError(domain.ErrForbidden, fmt.Sprintf("workspace %q is inactive", workspaceID)))
				return
			}
			WriteError(w, domain.NewError(domain.ErrInternal, "failed to resolve workspace"))
			return
		}

		ctx := context.WithValue(r.Context(), workspaceKey{}, cfg)
		ctx = observe.WithWorkspaceID(ctx, cfg.ID)

		// If service pool is configured, get the workspace's service set.
		if s.servicePool != nil {
			ss, err := s.servicePool.Get(r.Context(), cfg.ID)
			if err != nil {
				WriteError(w, domain.NewError(domain.ErrInternal, "failed to initialize workspace services"))
				return
			}
			ctx = context.WithValue(ctx, serviceSetKey{}, ss)
			defer s.servicePool.Release(cfg.ID)
		}

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// authMiddleware validates the Bearer token and sets the actor in context.
// Uses the workspace-scoped auth service when available (shared multi-tenant mode),
// falling back to the server-level auth service (single-workspace mode).
// Fails closed: if no auth service is available, all authenticated routes return 401.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authSvc := s.authFrom(r.Context())
		if authSvc == nil {
			WriteError(w, domain.NewError(domain.ErrUnavailable, "authentication not configured"))
			return
		}

		header := r.Header.Get("Authorization")
		if header == "" {
			WriteError(w, domain.NewError(domain.ErrUnauthorized, "authorization header required"))
			return
		}

		// HTTP auth schemes are case-insensitive per RFC 7235
		if len(header) < 7 || !strings.EqualFold(header[:7], "bearer ") {
			WriteError(w, domain.NewError(domain.ErrUnauthorized, "invalid authorization header format"))
			return
		}
		token := header[7:]
		if token == "" {
			WriteError(w, domain.NewError(domain.ErrUnauthorized, "invalid authorization header format"))
			return
		}

		actor, err := authSvc.ValidateToken(r.Context(), token)
		if err != nil {
			WriteError(w, err)
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
		ctx = observe.WithActorID(ctx, actor.ActorID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
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
