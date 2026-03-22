package gateway

import (
	"context"
	"net/http"

	"github.com/bszymi/spine/internal/store"
)

// Server is the HTTP Access Gateway for Spine.
type Server struct {
	httpServer *http.Server
	store      store.Store
}

// NewServer creates a new HTTP server with all routes and middleware.
func NewServer(addr string, st store.Store) *Server {
	s := &Server{
		store: st,
	}
	s.httpServer = &http.Server{
		Addr:    addr,
		Handler: s.routes(),
	}
	return s
}

// ListenAndServe starts the HTTP server.
func (s *Server) ListenAndServe() error {
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// Handler returns the server's HTTP handler for testing.
func (s *Server) Handler() http.Handler {
	return s.httpServer.Handler
}
