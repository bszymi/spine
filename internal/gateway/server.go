package gateway

import (
	"context"
	"net/http"

	"github.com/bszymi/spine/internal/artifact"
	"github.com/bszymi/spine/internal/auth"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/projection"
	"github.com/bszymi/spine/internal/store"
)

// Server is the HTTP Access Gateway for Spine.
type Server struct {
	httpServer *http.Server
	store      store.Store
	auth       *auth.Service
	artifacts  *artifact.Service
	projQuery  *projection.QueryService
	projSync   *projection.Service
	git        git.GitClient
}

// ServerConfig holds optional service dependencies for the server.
type ServerConfig struct {
	Store     store.Store
	Auth      *auth.Service
	Artifacts *artifact.Service
	ProjQuery *projection.QueryService
	ProjSync  *projection.Service
	Git       git.GitClient
}

// NewServer creates a new HTTP server with all routes and middleware.
func NewServer(addr string, cfg ServerConfig) *Server {
	s := &Server{
		store:     cfg.Store,
		auth:      cfg.Auth,
		artifacts: cfg.Artifacts,
		projQuery: cfg.ProjQuery,
		projSync:  cfg.ProjSync,
		git:       cfg.Git,
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
