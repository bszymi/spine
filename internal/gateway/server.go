package gateway

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/bszymi/spine/internal/artifact"
	"github.com/bszymi/spine/internal/auth"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/engine"
	"github.com/bszymi/spine/internal/projection"
	"github.com/bszymi/spine/internal/store"
	"github.com/bszymi/spine/internal/validation"
	"github.com/bszymi/spine/internal/workspace"
)

// ArtifactService defines the artifact operations the gateway needs.
type ArtifactService interface {
	Create(ctx context.Context, path, content string) (*artifact.WriteResult, error)
	Read(ctx context.Context, path, ref string) (*domain.Artifact, error)
	Update(ctx context.Context, path, content string) (*artifact.WriteResult, error)
	List(ctx context.Context, ref string) ([]*domain.Artifact, error)
	AcceptTask(ctx context.Context, path, rationale string) (*artifact.WriteResult, error)
	RejectTask(ctx context.Context, path string, acceptance domain.TaskAcceptance, rationale string) (*artifact.WriteResult, error)
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
	ListFiles(ctx context.Context, ref, pattern string) ([]string, error)
	Head(ctx context.Context) (string, error)
}

// Server is the HTTP Access Gateway for Spine.
// EventEmitterGW emits events from the gateway.
type EventEmitterGW interface {
	Emit(ctx context.Context, event domain.Event) error
}

// RunStarter starts standard workflow runs for tasks.
type RunStarter interface {
	StartRun(ctx context.Context, taskPath string) (*RunStartResult, error)
}

// RunStartResult contains the result of starting a standard run.
type RunStartResult struct {
	RunID        string
	TaskPath     string
	WorkflowID   string
	Status       string
	BranchName   string
	TraceID      string
	VersionLabel string
	CommitSHA    string
}

// PlanningRunStarter starts planning runs for governed artifact creation.
type PlanningRunStarter interface {
	StartPlanningRun(ctx context.Context, artifactPath, artifactContent string) (*PlanningRunResult, error)
}

// PlanningRunResult contains the result of starting a planning run.
// Mirrors engine.StartRunResult but avoids gateway importing engine.
type PlanningRunResult struct {
	RunID        string
	TaskPath     string
	WorkflowID   string
	Status       string
	Mode         string
	BranchName   string
	TraceID      string
	EntryStepID  string
	VersionLabel string
	CommitSHA    string
}

// BranchCreator creates exploratory branches and manages divergence windows.
type BranchCreator interface {
	CreateExploratoryBranch(ctx context.Context, divCtx *domain.DivergenceContext, branchID, startStep string) (*domain.Branch, error)
	CloseWindow(ctx context.Context, divCtx *domain.DivergenceContext) error
}

type Server struct {
	httpServer         *http.Server
	store              store.Store
	auth               *auth.Service
	artifacts          ArtifactService
	projQuery          ProjectionQuerier
	projSync           ProjectionSyncer
	git                GitReader
	validator          *validation.Engine
	resultHandler      ResultHandler
	workflowResolver   WorkflowResolverFn
	branchCreator      BranchCreator          // optional, nil if not configured
	events             EventEmitterGW         // optional, nil if not configured
	runStarter         RunStarter             // optional, nil if not configured
	planningRunStarter PlanningRunStarter     // optional, nil if not configured
	wsResolver         workspace.Resolver     // optional, nil if not configured
	servicePool        *workspace.ServicePool // optional, nil if not configured
	wsDBProvider       *workspace.DBProvider  // optional, nil in single mode
	candidateFinder    CandidateFinder        // optional, nil if not configured
	stepClaimer        StepClaimer            // optional, nil if not configured
	stepReleaser       StepReleaser           // optional, nil if not configured
	devMode            bool                   // when true, authorize allows unauthenticated requests
	rebuilds           sync.Map               // rebuild_id -> *rebuildState
}

// CandidateFinder discovers tasks ready for execution.
type CandidateFinder interface {
	FindExecutionCandidates(ctx context.Context, filter engine.ExecutionCandidateFilter) ([]engine.ExecutionCandidate, error)
}

// StepClaimer allows actors to claim step executions.
type StepClaimer interface {
	ClaimStep(ctx context.Context, req engine.ClaimRequest) (*engine.ClaimResult, error)
}

// StepReleaser allows actors to release step assignments.
type StepReleaser interface {
	ReleaseStep(ctx context.Context, req engine.ReleaseRequest) error
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
	Timeout      string // max run duration (e.g. "24h")
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
	Store              store.Store
	Auth               *auth.Service
	Artifacts          ArtifactService
	ProjQuery          ProjectionQuerier
	ProjSync           ProjectionSyncer
	Git                GitReader
	Validator          *validation.Engine
	ResultHandler      ResultHandler
	WorkflowResolver   WorkflowResolverFn
	BranchCreator      BranchCreator
	Events             EventEmitterGW
	RunStarter         RunStarter
	PlanningRunStarter PlanningRunStarter
	WorkspaceResolver  workspace.Resolver
	ServicePool        *workspace.ServicePool
	WSDBProvider       *workspace.DBProvider
	CandidateFinder    CandidateFinder
	StepClaimer        StepClaimer
	StepReleaser       StepReleaser
	DevMode            bool          // when true, authorize allows unauthenticated requests
	ReadHeaderTimeout  time.Duration // defaults to 10s
	ReadTimeout        time.Duration // defaults to 30s
	WriteTimeout       time.Duration // defaults to 60s
	IdleTimeout        time.Duration // defaults to 120s
}

// NewServer creates a new HTTP server with all routes and middleware.
func NewServer(addr string, cfg ServerConfig) *Server {
	s := &Server{
		store:              cfg.Store,
		auth:               cfg.Auth,
		artifacts:          cfg.Artifacts,
		projQuery:          cfg.ProjQuery,
		projSync:           cfg.ProjSync,
		git:                cfg.Git,
		validator:          cfg.Validator,
		resultHandler:      cfg.ResultHandler,
		branchCreator:      cfg.BranchCreator,
		events:             cfg.Events,
		runStarter:         cfg.RunStarter,
		planningRunStarter: cfg.PlanningRunStarter,
		workflowResolver:   cfg.WorkflowResolver,
		wsResolver:         cfg.WorkspaceResolver,
		servicePool:        cfg.ServicePool,
		wsDBProvider:       cfg.WSDBProvider,
		candidateFinder:    cfg.CandidateFinder,
		stepClaimer:        cfg.StepClaimer,
		stepReleaser:       cfg.StepReleaser,
		devMode:            cfg.DevMode,
	}
	s.httpServer = &http.Server{
		Addr:              addr,
		Handler:           s.routes(),
		ReadHeaderTimeout: withDefault(cfg.ReadHeaderTimeout, 10*time.Second),
		ReadTimeout:       withDefault(cfg.ReadTimeout, 30*time.Second),
		WriteTimeout:      withDefault(cfg.WriteTimeout, 60*time.Second),
		IdleTimeout:       withDefault(cfg.IdleTimeout, 120*time.Second),
	}
	return s
}

// Workspace-scoped service accessors. These check the request context
// for a workspace ServiceSet first, falling back to the Server's direct
// fields for backward compatibility and single-workspace mode.

func (s *Server) storeFrom(ctx context.Context) store.Store {
	if ss := serviceSetFromContext(ctx); ss != nil && ss.Store != nil {
		return ss.Store
	}
	return s.store
}

func (s *Server) artifactsFrom(ctx context.Context) ArtifactService {
	if ss := serviceSetFromContext(ctx); ss != nil && ss.Artifacts != nil {
		return ss.Artifacts
	}
	return s.artifacts
}

func (s *Server) projQueryFrom(ctx context.Context) ProjectionQuerier {
	if ss := serviceSetFromContext(ctx); ss != nil && ss.ProjQuery != nil {
		return ss.ProjQuery
	}
	return s.projQuery
}

func (s *Server) projSyncFrom(ctx context.Context) ProjectionSyncer {
	if ss := serviceSetFromContext(ctx); ss != nil && ss.ProjSync != nil {
		return ss.ProjSync
	}
	return s.projSync
}

func (s *Server) gitFrom(ctx context.Context) GitReader {
	if ss := serviceSetFromContext(ctx); ss != nil && ss.GitClient != nil {
		return ss.GitClient
	}
	return s.git
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

func withDefault(d, fallback time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return fallback
}
