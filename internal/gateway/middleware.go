package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/bszymi/spine/internal/auth"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/workspace"
)

type contextKey string

const (
	actorContextKey     contextKey = "actor"
	workspaceContextKey contextKey = "workspace_config"
)

// WorkspaceHeader is the HTTP header used to pass workspace ID.
const WorkspaceHeader = "X-Workspace-ID"

// actorFromContext returns the authenticated actor from the request context.
func actorFromContext(ctx context.Context) *domain.Actor {
	actor, _ := ctx.Value(actorContextKey).(*domain.Actor)
	return actor
}

// WorkspaceConfigFromContext returns the resolved workspace config from the request context.
func WorkspaceConfigFromContext(ctx context.Context) *workspace.Config {
	cfg, _ := ctx.Value(workspaceContextKey).(*workspace.Config)
	return cfg
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

		ctx := context.WithValue(r.Context(), workspaceContextKey, cfg)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// authMiddleware validates the Bearer token and sets the actor in context.
// Fails closed: if auth service is not configured, all authenticated routes return 401.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.auth == nil {
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

		actor, err := s.auth.ValidateToken(r.Context(), token)
		if err != nil {
			WriteError(w, err)
			return
		}

		ctx := context.WithValue(r.Context(), actorContextKey, actor)
		ctx = observe.WithActorID(ctx, actor.ActorID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// authorize checks if the authenticated actor has permission for the operation.
// Returns false and writes an error response if not authorized.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request, op auth.Operation) bool {
	actor := actorFromContext(r.Context())
	if actor == nil {
		// No auth middleware configured — allow in dev/test mode
		return true
	}
	if err := auth.Authorize(actor, op); err != nil {
		WriteError(w, err)
		return false
	}
	return true
}

// traceIDMiddleware extracts or generates a trace ID and propagates it
// through the request context and response header.
func traceIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := r.Header.Get("X-Trace-Id")
		if traceID == "" {
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
