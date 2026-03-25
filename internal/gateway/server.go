package gateway

import (
	"context"
	"net/http"

	"github.com/bszymi/spine/internal/auth"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/projection"
	"github.com/bszymi/spine/internal/store"
	"github.com/bszymi/spine/internal/validation"
)

// ArtifactService defines the artifact operations the gateway needs.
type ArtifactService interface {
	Create(ctx context.Context, path, content string) (*domain.Artifact, error)
	Read(ctx context.Context, path, ref string) (*domain.Artifact, error)
	Update(ctx context.Context, path, content string) (*domain.Artifact, error)
	List(ctx context.Context, ref string) ([]*domain.Artifact, error)
	AcceptTask(ctx context.Context, path, rationale string) (*domain.Artifact, error)
	RejectTask(ctx context.Context, path string, acceptance domain.TaskAcceptance, rationale string) (*domain.Artifact, error)
}

// ProjectionQuerier defines the projection query operations the gateway needs.
type ProjectionQuerier interface {
	QueryArtifacts(ctx context.Context, query store.ArtifactQuery) (*store.ArtifactQueryResult, error)
	QueryGraph(ctx context.Context, rootPath string, depth int, linkTypes []string) (*projection.GraphResult, error)
	QueryHistory(ctx context.Context, path string, limit int) ([]projection.HistoryEntry, error)
	QueryRuns(ctx context.Context, taskPath string) ([]domain.Run, error)
}

// ProjectionSyncer defines the projection sync operations the gateway needs.
type ProjectionSyncer interface {
	FullRebuild(ctx context.Context) error
}

// GitReader defines the Git read operations the gateway needs.
type GitReader interface {
	ReadFile(ctx context.Context, ref, path string) ([]byte, error)
}

// Server is the HTTP Access Gateway for Spine.
type Server struct {
	httpServer       *http.Server
	store            store.Store
	auth             *auth.Service
	artifacts        ArtifactService
	projQuery        ProjectionQuerier
	projSync         ProjectionSyncer
	git              GitReader
	validator        *validation.Engine
	resultHandler    ResultHandler
	workflowResolver WorkflowResolverFn
}

// WorkflowResolverFn resolves the governing workflow for an artifact type.
type WorkflowResolverFn func(ctx context.Context, artifactType, workType string) (*ResolvedWorkflow, error)

// ResolvedWorkflow contains the resolved workflow binding result.
type ResolvedWorkflow struct {
	WorkflowID   string
	WorkflowPath string
	EntryStep    string
	CommitSHA    string
	VersionLabel string
}

// ResultHandler processes step result submissions through the engine pipeline.
type ResultHandler interface {
	IngestResult(ctx context.Context, req ResultSubmission) (*ResultResponse, error)
}

// ResultSubmission is the gateway-facing result submission.
type ResultSubmission struct {
	ExecutionID       string
	OutcomeID         string
	ArtifactsProduced []string
}

// ResultResponse is returned after result ingestion.
type ResultResponse struct {
	ExecutionID string `json:"execution_id"`
	StepID      string `json:"step_id"`
	Status      string `json:"status"`
	OutcomeID   string `json:"outcome_id"`
}

// ServerConfig holds optional service dependencies for the server.
type ServerConfig struct {
	Store            store.Store
	Auth             *auth.Service
	Artifacts        ArtifactService
	ProjQuery        ProjectionQuerier
	ProjSync         ProjectionSyncer
	Git              GitReader
	Validator        *validation.Engine
	ResultHandler    ResultHandler
	WorkflowResolver WorkflowResolverFn
}

// NewServer creates a new HTTP server with all routes and middleware.
func NewServer(addr string, cfg ServerConfig) *Server {
	s := &Server{
		store:            cfg.Store,
		auth:             cfg.Auth,
		artifacts:        cfg.Artifacts,
		projQuery:        cfg.ProjQuery,
		projSync:         cfg.ProjSync,
		git:              cfg.Git,
		validator:        cfg.Validator,
		resultHandler:    cfg.ResultHandler,
		workflowResolver: cfg.WorkflowResolver,
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
