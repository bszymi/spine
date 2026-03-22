package gateway

import (
	"fmt"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/observe"
)

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
