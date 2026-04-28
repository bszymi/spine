package gateway

import (
	"context"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/bszymi/spine/internal/artifact"
	"github.com/bszymi/spine/internal/auth"
	"github.com/bszymi/spine/internal/delivery"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/engine"
	"github.com/bszymi/spine/internal/githttp"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/projection"
	"github.com/bszymi/spine/internal/repository"
	"github.com/bszymi/spine/internal/store"
	"github.com/bszymi/spine/internal/validation"
	"github.com/bszymi/spine/internal/workflow"
	"github.com/bszymi/spine/internal/workspace"
)

// WorkflowService defines the workflow definition operations the gateway needs.
// Per ADR-007, workflow definitions are a first-class resource with dedicated
// create/update/read/list/validate operations that run workflow-specific
// validation at write time.
type WorkflowService interface {
	Create(ctx context.Context, id, body string) (*workflow.WriteResult, error)
	Update(ctx context.Context, id, body string) (*workflow.WriteResult, error)
	Read(ctx context.Context, id, ref string) (*workflow.ReadResult, error)
	List(ctx context.Context, opts workflow.ListOptions) ([]*domain.WorkflowDefinition, error)
	ValidateBody(ctx context.Context, id, body string) domain.ValidationResult
}

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

// WorkflowPlanningRunStarter starts planning runs that govern workflow
// definition edits (ADR-008). Separate from PlanningRunStarter because the
// body is a YAML workflow definition, not a Markdown artifact.
type WorkflowPlanningRunStarter interface {
	StartWorkflowPlanningRun(ctx context.Context, workflowID, body string) (*PlanningRunResult, error)
}

// RunCanceller cancels an active or paused run through the engine,
// emitting events and cleaning up branches.
type RunCanceller interface {
	CancelRun(ctx context.Context, runID string) error
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
	httpServer                 *http.Server
	store                      store.Store
	auth                       *auth.Service
	artifacts                  ArtifactService
	workflows                  WorkflowService
	projQuery                  ProjectionQuerier
	projSync                   ProjectionSyncer
	git                        GitReader
	validator                  *validation.Engine
	resultHandler              ResultHandler
	workflowResolver           WorkflowResolverFn
	branchCreator              BranchCreator              // optional, nil if not configured
	events                     EventEmitterGW             // optional, nil if not configured
	runStarter                 RunStarter                 // optional, nil if not configured
	planningRunStarter         PlanningRunStarter         // optional, nil if not configured
	wfPlanningStarter          WorkflowPlanningRunStarter // optional, nil if not configured
	runCanceller               RunCanceller               // optional, nil if not configured
	wsResolver                 workspace.Resolver         // optional, nil if not configured
	servicePool                *workspace.ServicePool     // optional, nil if not configured
	wsDBProvider               *workspace.DBProvider      // optional, nil in single mode
	candidateFinder            CandidateFinder            // optional, nil if not configured
	stepClaimer                StepClaimer                // optional, nil if not configured
	stepReleaser               StepReleaser               // optional, nil if not configured
	stepExecutionLister        StepExecutionLister        // optional, nil if not configured
	stepAcknowledger           StepAcknowledger           // optional, nil if not configured
	stepAssigner               StepAssigner               // optional, nil if not configured
	eventBroadcaster           *delivery.EventBroadcaster // optional, nil if not configured
	gitHTTP                    *githttp.Handler           // optional, nil if not configured
	gitPushResolver            GitPushResolverFunc        // optional, nil if not configured; resolves per-workspace policy+events for push
	webhookTargets             *delivery.TargetValidator  // optional, nil = permissive; enforces SSRF rules on target_url writes and tests
	bindingInvalidationHandler http.Handler               // optional, nil if not configured; ADR-011 platform → Spine invalidation webhook
	repoManager                *repository.Manager        // optional, nil if not configured; INIT-014 EPIC-001 multi-repo workspace management
	devMode                    bool                       // when true, authorize allows unauthenticated requests
	env                        string                     // SPINE_ENV value (production/staging/development); surfaced in health
	sseLimiter                 *sseLimiter                // caps concurrent SSE streams per actor
	trustedProxyCIDRs          []*net.IPNet               // reverse-proxy networks whose XFF header is honored for rate limiting
	rebuilds                   sync.Map                   // rebuild_id -> *rebuildState
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

// StepExecutionLister queries active step executions for actor polling.
type StepExecutionLister interface {
	ListStepExecutions(ctx context.Context, q engine.StepExecutionQuery) ([]engine.StepExecutionItem, error)
}

// StepAcknowledger transitions an assigned step to in_progress.
type StepAcknowledger interface {
	AcknowledgeStep(ctx context.Context, req engine.AcknowledgeRequest) (*engine.AcknowledgeResult, error)
}

// StepAssigner performs a manual step assignment (third-party selects the
// actor). Mirrors StepClaimer / StepAcknowledger but for the assign
// transition. The handler retains precondition evaluation; the assigner
// owns the state-machine transition.
type StepAssigner interface {
	AssignStep(ctx context.Context, req engine.AssignRequest) (*engine.AssignResult, error)
	LookupStepDef(ctx context.Context, runID, stepID string) (*domain.StepDefinition, *domain.Run)
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
	Store               store.Store
	Auth                *auth.Service
	Artifacts           ArtifactService
	Workflows           WorkflowService
	ProjQuery           ProjectionQuerier
	ProjSync            ProjectionSyncer
	Git                 GitReader
	Validator           *validation.Engine
	ResultHandler       ResultHandler
	WorkflowResolver    WorkflowResolverFn
	BranchCreator       BranchCreator
	Events              EventEmitterGW
	RunStarter          RunStarter
	PlanningRunStarter  PlanningRunStarter
	WFPlanningStarter   WorkflowPlanningRunStarter
	RunCanceller        RunCanceller
	WorkspaceResolver   workspace.Resolver
	ServicePool         *workspace.ServicePool
	WSDBProvider        *workspace.DBProvider
	CandidateFinder     CandidateFinder
	StepClaimer         StepClaimer
	StepReleaser        StepReleaser
	StepExecutionLister StepExecutionLister
	StepAcknowledger    StepAcknowledger
	StepAssigner        StepAssigner
	EventBroadcaster    *delivery.EventBroadcaster
	GitHTTP             *githttp.Handler    // optional, serves git repos over HTTP
	GitPushResolver     GitPushResolverFunc // optional; resolves the per-workspace policy + events used by the Git push pre-receive gate. Required for correct shared-mode enforcement (each workspace has its own branch-protection table and event stream); single-mode callers may omit it and the handler's default policy is used.

	// BindingInvalidationHandler is the platform → Spine webhook
	// receiver for ADR-011 binding invalidations. When non-nil, it
	// is mounted at POST /internal/v1/workspaces/{workspace_id}/binding-invalidate.
	// The handler owns its own bearer-token auth, so it sits
	// outside the operator-token and per-actor middleware chains.
	BindingInvalidationHandler http.Handler
	RepositoryManager          *repository.Manager       // optional; serves /api/v1/repositories
	WebhookTargets             *delivery.TargetValidator // optional; enforces webhook target_url SSRF rules on create/update/test. A nil validator permits every URL and is only appropriate for tests.
	DevMode                    bool                      // when true, authorize allows unauthenticated requests
	Env                        string                    // SPINE_ENV: production/staging/development; surfaced in /system/health
	ReadHeaderTimeout          time.Duration             // defaults to 10s
	ReadTimeout                time.Duration             // defaults to 30s
	WriteTimeout               time.Duration             // defaults to 60s
	IdleTimeout                time.Duration             // defaults to 120s
	SSEMaxConnPerActor         int                       // per-actor SSE connection cap; defaults to 5, <=0 disables
	TrustedProxyCIDRs          []*net.IPNet              // reverse-proxy networks whose XFF header is honored for rate limiting; nil disables
}

// NewServer creates a new HTTP server with all routes and middleware.
func NewServer(addr string, cfg ServerConfig) *Server {
	s := &Server{
		store:                      cfg.Store,
		auth:                       cfg.Auth,
		artifacts:                  cfg.Artifacts,
		workflows:                  cfg.Workflows,
		projQuery:                  cfg.ProjQuery,
		projSync:                   cfg.ProjSync,
		git:                        cfg.Git,
		validator:                  cfg.Validator,
		resultHandler:              cfg.ResultHandler,
		branchCreator:              cfg.BranchCreator,
		events:                     cfg.Events,
		runStarter:                 cfg.RunStarter,
		planningRunStarter:         cfg.PlanningRunStarter,
		wfPlanningStarter:          cfg.WFPlanningStarter,
		runCanceller:               cfg.RunCanceller,
		workflowResolver:           cfg.WorkflowResolver,
		wsResolver:                 cfg.WorkspaceResolver,
		servicePool:                cfg.ServicePool,
		wsDBProvider:               cfg.WSDBProvider,
		candidateFinder:            cfg.CandidateFinder,
		stepClaimer:                cfg.StepClaimer,
		stepReleaser:               cfg.StepReleaser,
		stepExecutionLister:        cfg.StepExecutionLister,
		stepAcknowledger:           cfg.StepAcknowledger,
		stepAssigner:               cfg.StepAssigner,
		eventBroadcaster:           cfg.EventBroadcaster,
		gitHTTP:                    cfg.GitHTTP,
		gitPushResolver:            cfg.GitPushResolver,
		bindingInvalidationHandler: cfg.BindingInvalidationHandler,
		repoManager:                cfg.RepositoryManager,
		webhookTargets:             cfg.WebhookTargets,
		devMode:                    cfg.DevMode,
		env:                        cfg.Env,
		sseLimiter:                 newSSELimiter(withDefaultInt(cfg.SSEMaxConnPerActor, 5)),
		trustedProxyCIDRs:          cfg.TrustedProxyCIDRs,
	}
	if cfg.DevMode {
		observe.Logger(context.Background()).Warn("DEV MODE ENABLED — authentication is bypassed for unauthenticated requests, do not use in production")
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

// Workspace-scoped service accessors. Each checks the request context for a
// workspace ServiceSet first, falling back to the Server's direct field for
// single-workspace mode. resolve collapses the identical "if ss != nil &&
// ss.X != nil return ss.X; return s.x" shape into a single helper.

// resolve returns the per-request override from the workspace ServiceSet (if
// pick returns a non-zero value), otherwise falls back to the given default.
// T must be comparable against its zero value, which holds for interfaces
// and pointers (the only concrete accessor types here).
func resolve[T comparable](ctx context.Context, pick func(*workspace.ServiceSet) T, fallback T) T {
	var zero T
	if ss := serviceSetFromContext(ctx); ss != nil {
		if v := pick(ss); v != zero {
			return v
		}
	}
	return fallback
}

func (s *Server) authFrom(ctx context.Context) *auth.Service {
	return resolve(ctx, func(ss *workspace.ServiceSet) *auth.Service { return ss.Auth }, s.auth)
}

func (s *Server) storeFrom(ctx context.Context) store.Store {
	return resolve(ctx, func(ss *workspace.ServiceSet) store.Store { return ss.Store }, s.store)
}

func (s *Server) artifactsFrom(ctx context.Context) ArtifactService {
	return resolve(ctx, func(ss *workspace.ServiceSet) ArtifactService { return ss.Artifacts }, s.artifacts)
}

func (s *Server) workflowsFrom(ctx context.Context) WorkflowService {
	return resolve(ctx, func(ss *workspace.ServiceSet) WorkflowService {
		if ws, ok := ss.Workflows.(WorkflowService); ok {
			return ws
		}
		return nil
	}, s.workflows)
}

func (s *Server) projQueryFrom(ctx context.Context) ProjectionQuerier {
	return resolve(ctx, func(ss *workspace.ServiceSet) ProjectionQuerier { return ss.ProjQuery }, s.projQuery)
}

func (s *Server) projSyncFrom(ctx context.Context) ProjectionSyncer {
	return resolve(ctx, func(ss *workspace.ServiceSet) ProjectionSyncer { return ss.ProjSync }, s.projSync)
}

func (s *Server) gitFrom(ctx context.Context) GitReader {
	return resolve(ctx, func(ss *workspace.ServiceSet) GitReader { return ss.GitClient }, s.git)
}

func (s *Server) validatorFrom(ctx context.Context) *validation.Engine {
	return resolve(ctx, func(ss *workspace.ServiceSet) *validation.Engine { return ss.Validator }, s.validator)
}

func (s *Server) branchCreatorFrom(ctx context.Context) BranchCreator {
	return resolve(ctx, func(ss *workspace.ServiceSet) BranchCreator { return ss.Divergence }, s.branchCreator)
}

func (s *Server) runStarterFrom(ctx context.Context) RunStarter {
	return resolve(ctx, func(ss *workspace.ServiceSet) RunStarter {
		if rs, ok := ss.RunStarter.(RunStarter); ok {
			return rs
		}
		return nil
	}, s.runStarter)
}

func (s *Server) runCancellerFrom(ctx context.Context) RunCanceller {
	return resolve(ctx, func(ss *workspace.ServiceSet) RunCanceller {
		if rc, ok := ss.RunCanceller.(RunCanceller); ok {
			return rc
		}
		return nil
	}, s.runCanceller)
}

func (s *Server) planningRunStarterFrom(ctx context.Context) PlanningRunStarter {
	return resolve(ctx, func(ss *workspace.ServiceSet) PlanningRunStarter {
		if ps, ok := ss.PlanningRunStarter.(PlanningRunStarter); ok {
			return ps
		}
		return nil
	}, s.planningRunStarter)
}

func (s *Server) wfPlanningStarterFrom(ctx context.Context) WorkflowPlanningRunStarter {
	return resolve(ctx, func(ss *workspace.ServiceSet) WorkflowPlanningRunStarter {
		if ps, ok := ss.WFPlanningStarter.(WorkflowPlanningRunStarter); ok {
			return ps
		}
		return nil
	}, s.wfPlanningStarter)
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

func withDefaultInt(v, fallback int) int {
	if v > 0 {
		return v
	}
	return fallback
}
