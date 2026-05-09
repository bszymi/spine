package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/bszymi/spine/internal/auth"
	"github.com/bszymi/spine/internal/config"
	spinecrypto "github.com/bszymi/spine/internal/crypto"
	"github.com/bszymi/spine/internal/delivery"
	"github.com/bszymi/spine/internal/divergence"
	"github.com/bszymi/spine/internal/engine"
	"github.com/bszymi/spine/internal/event"
	"github.com/bszymi/spine/internal/gateway"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/gitpool"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/projection"
	"github.com/bszymi/spine/internal/queue"
	"github.com/bszymi/spine/internal/repository"
	"github.com/bszymi/spine/internal/scheduler"
	"github.com/bszymi/spine/internal/secrets"
	"github.com/bszymi/spine/internal/store"
	"github.com/bszymi/spine/internal/validation"
	"github.com/bszymi/spine/internal/workflow"
	"github.com/bszymi/spine/internal/workspace"
)

// runAdapter adapts engine.Orchestrator to gateway.RunStarter.
type runAdapter struct {
	orch *engine.Orchestrator
}

func (a *runAdapter) StartRun(ctx context.Context, taskPath string) (*gateway.RunStartResult, error) {
	result, err := a.orch.StartRun(ctx, taskPath)
	if err != nil {
		return nil, err
	}
	return &gateway.RunStartResult{
		RunID:        result.Run.RunID,
		TaskPath:     result.Run.TaskPath,
		WorkflowID:   result.Run.WorkflowID,
		Status:       string(result.Run.Status),
		BranchName:   result.Run.BranchName,
		TraceID:      result.Run.TraceID,
		VersionLabel: result.Run.WorkflowVersionLabel,
		CommitSHA:    result.Run.WorkflowVersion,
	}, nil
}

// resultAdapter adapts engine.Orchestrator to gateway.ResultHandler.
type resultAdapter struct {
	orch *engine.Orchestrator
}

func (a *resultAdapter) IngestResult(ctx context.Context, req gateway.ResultSubmission) (*gateway.ResultResponse, error) {
	resp, err := a.orch.IngestResult(ctx, engine.SubmitRequest{
		ExecutionID:       req.ExecutionID,
		OutcomeID:         req.OutcomeID,
		ArtifactsProduced: req.ArtifactsProduced,
	})
	if err != nil {
		return nil, err
	}
	return &gateway.ResultResponse{
		ExecutionID: resp.ExecutionID,
		StepID:      resp.StepID,
		Status:      string(resp.Status),
		OutcomeID:   resp.OutcomeID,
	}, nil
}

// planningRunAdapter adapts engine.Orchestrator to gateway.PlanningRunStarter.
type planningRunAdapter struct {
	orch *engine.Orchestrator
}

func (a *planningRunAdapter) StartPlanningRun(ctx context.Context, artifactPath, artifactContent string) (*gateway.PlanningRunResult, error) {
	result, err := a.orch.StartPlanningRun(ctx, artifactPath, artifactContent)
	if err != nil {
		return nil, err
	}
	return planningRunResult(result), nil
}

// workflowPlanningRunAdapter adapts engine.Orchestrator to
// gateway.WorkflowPlanningRunStarter for ADR-008 workflow-edit planning runs.
type workflowPlanningRunAdapter struct {
	orch *engine.Orchestrator
}

func (a *workflowPlanningRunAdapter) StartWorkflowPlanningRun(ctx context.Context, workflowID, body string) (*gateway.PlanningRunResult, error) {
	result, err := a.orch.StartWorkflowPlanningRun(ctx, workflowID, body)
	if err != nil {
		return nil, err
	}
	return planningRunResult(result), nil
}

func planningRunResult(result *engine.StartRunResult) *gateway.PlanningRunResult {
	return &gateway.PlanningRunResult{
		RunID:        result.Run.RunID,
		TaskPath:     result.Run.TaskPath,
		WorkflowID:   result.Run.WorkflowID,
		Status:       string(result.Run.Status),
		Mode:         string(result.Run.Mode),
		BranchName:   result.Run.BranchName,
		TraceID:      result.Run.TraceID,
		EntryStepID:  result.EntryStep.StepID,
		VersionLabel: result.Run.WorkflowVersionLabel,
		CommitSHA:    result.Run.WorkflowVersion,
	}
}

// serveDeps is the full set of pre-built dependencies needed to assemble
// a gateway.ServerConfig. serveCmd reads env and constructs these; the
// serve-startup smoke test builds them in-memory.
type serveDeps struct {
	Store    store.Store
	RepoPath string
	// CodeRepoBase is the absolute filesystem directory under which
	// every code-repo binding's LocalPath must resolve. Sourced from
	// SPINE_CODE_REPO_BASE; required when SPINE_ENV=production, optional
	// otherwise. The deployment-wide PARENT of all per-workspace
	// trees; cmd/spine narrows this to `<base>/<WorkspaceID>` per
	// pool before passing to gitpool.WithRepoBase so the file-mode
	// top-level pool and shared-mode per-workspace pools enforce the
	// same per-workspace boundary.
	CodeRepoBase string
	// WorkspaceID is the file-mode runtime workspace identifier
	// (SPINE_WORKSPACE_ID or "default"). Used to narrow the top-level
	// gitpool's CodeRepoBase to the file-mode workspace's subtree.
	WorkspaceID   string
	SpineCfg      *config.SpineConfig
	GitClient     *git.CLIClient
	Queue         *queue.MemoryQueue
	Events        event.EventRouter
	WSResolver    workspace.Resolver
	WSDBProvider  *workspace.DBProvider
	WSServicePool *workspace.ServicePool
	// BindingInvalidationHandler is the optional ADR-011 webhook
	// receiver. Wired only when WORKSPACE_RESOLVER=platform-binding.
	BindingInvalidationHandler http.Handler
	SecretCipher               *spinecrypto.SecretCipher
	// SecretClient is the workspace-scoped secret resolver used by the
	// git client pool to dereference per-binding `credentials_ref`
	// fields (INIT-014 EPIC-003 TASK-006). Optional: when nil, code
	// repos with a credentials_ref fail closed with a typed
	// credentials-unavailable error; bindings without one keep working
	// through the legacy SPINE_GIT_PUSH_TOKEN path.
	SecretClient        secrets.SecretClient
	RuntimeEnv          string
	DevMode             bool
	TrustedProxyCIDRs   []*net.IPNet
	TrustedGitHTTPCIDRs []string
	// GitReceivePackEnabled wires the git push endpoint (receive-pack).
	// Default false; EPIC-004 TASK-001. When true this is still a bare
	// passthrough — branch-protection enforcement is TASK-002.
	GitReceivePackEnabled bool
	EventDeliveryOn       bool
	SMPEventURL           string
	SMPWorkspaceID        string
	SMPInternalToken      string
	EventRetention        time.Duration
	SSEMaxConn            int
	OrphanThreshold       time.Duration

	// WebhookTargets is the SSRF gate applied to every subscription
	// target_url on create / update / test and to every webhook
	// delivery. Operators widen the default "public HTTPS only" policy
	// with SPINE_WEBHOOK_ALLOWED_HOSTS (comma-separated) when internal
	// or localhost destinations are intentional.
	WebhookTargets *delivery.TargetValidator
}

// serveRuntime bundles the ServerConfig with the long-lived background
// services the serve loop must start and stop.
type serveRuntime struct {
	Config         gateway.ServerConfig
	Scheduler      *scheduler.Scheduler
	ProjSync       *projection.Service
	DeliveryCancel context.CancelFunc
}

// buildServerConfig assembles a gateway.ServerConfig from the given deps.
// It is the single point of wiring for the serve command and the smoke
// test — any service added to ServerConfig must be wired here so both
// paths get the same surface.
func buildServerConfig(ctx context.Context, deps serveDeps) (*serveRuntime, error) {
	log := observe.Logger(ctx)

	var authSvc *auth.Service
	if deps.Store != nil {
		authSvc = auth.NewService(deps.Store)
	}

	// Repository registry — the single source of truth for catalog
	// identity + binding row joins, consumed by the git pool, the
	// run-start preconditions, and (later in INIT-014) the validator.
	// Built unconditionally so the pool always has a resolver, even
	// when the orchestrator path below is skipped (no store).
	repoSpec := repository.PrimarySpec{LocalPath: deps.RepoPath}
	repoRegistry := repository.New(
		deps.SMPWorkspaceID,
		repoSpec,
		func(_ context.Context) (*repository.Catalog, error) {
			return repository.ParseCatalog(nil, repoSpec)
		},
		deps.Store,
	)

	// Git client pool. PrimaryClient() returns deps.GitClient
	// unchanged so governance services keep operating on the primary
	// repo with no behavior change.
	//
	// WithCloner and the factory are both threaded with the full
	// auth profile already baked into deps.GitClient — process-level
	// SPINE_GIT_PUSH_TOKEN / credential-helper opts (PushAuthOpts)
	// plus the dedicated-mode SMP_WORKSPACE_ID env. Without the SMP
	// env, lazy code-repo clones that depend on a per-workspace
	// credential helper would invoke the helper missing the workspace
	// ID it needs to resolve credentials. SecretCredentialResolver
	// layers a per-binding token on top via WithPushToken when the
	// binding sets credentials_ref; the resolver is installed
	// unconditionally (even when SecretClient is nil) so a
	// misconfigured deployment with credentialed bindings fails
	// closed instead of cloning unauthenticated.
	codeRepoOpts := append([]git.CLIOption{}, git.PushAuthOpts()...)
	if deps.SMPWorkspaceID != "" {
		codeRepoOpts = append(codeRepoOpts, git.WithPushEnv("SMP_WORKSPACE_ID="+deps.SMPWorkspaceID))
	}
	// WithRepoBase pins the code-repo containment check to this
	// pool's per-workspace subtree (`<deps.CodeRepoBase>/<deps.WorkspaceID>`).
	// In file (single-workspace) mode this top-level pool is the one
	// routing code-repo Git operations, so per-workspace narrowing is
	// applied here. In shared mode (deps.WSServicePool != nil) code-repo
	// routes go through each workspace's ServiceSet.GitPool which
	// applies its own narrowing in buildServiceSet — narrowing here too
	// would create an unused `<base>/<runtime-ws>` (typically
	// `<base>/default`) and could fail startup against a deployment
	// root that's only writable per-tenant. Skip the narrowing when a
	// service pool will serve code repos.
	wsCodeRepoBase := ""
	if deps.WSServicePool == nil {
		var derr error
		wsCodeRepoBase, derr = workspace.PerWorkspaceCodeRepoBase(deps.CodeRepoBase, deps.WorkspaceID)
		if derr != nil {
			return nil, fmt.Errorf("derive top-level code-repo base for workspace %q: %w", deps.WorkspaceID, derr)
		}
	}
	gitPool, err := gitpool.New(deps.GitClient, repoRegistry,
		gitpool.NewCLIClientFactory(codeRepoOpts...),
		gitpool.WithCloner(gitpool.NewCLICloner(codeRepoOpts...)),
		gitpool.WithRepoBase(wsCodeRepoBase),
		gitpool.WithCredentialResolver(&gitpool.SecretCredentialResolver{
			Client: deps.SecretClient,
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("git client pool: %w", err)
	}
	primaryClient := gitPool.PrimaryClient()

	artifactSvc, err := buildArtifactService(primaryClient, deps.Events, deps.RepoPath, deps.SpineCfg, deps.Store)
	if err != nil {
		return nil, fmt.Errorf("artifact service: %w", err)
	}
	workflowSvc := workflow.NewService(primaryClient, deps.RepoPath)

	var projQuery *projection.QueryService
	var projSync *projection.Service
	if deps.Store != nil {
		projQuery = projection.NewQueryService(deps.Store, primaryClient)
		projSync = projection.NewService(primaryClient, deps.Store, deps.Events, 30*time.Second)
		projSync.WithArtifactsDir(deps.SpineCfg.ArtifactsDir)
	}

	var validator *validation.Engine
	if deps.Store != nil {
		// Today no production code reads /.spine/repositories.yaml from
		// Git, so every workspace behaves as single-repo. Wiring the
		// primary-only catalog snapshot here matches that real state:
		// RE-001 accepts `repositories: [spine]` and rejects any other
		// ID. When the Git-backed loader lands (later INIT-014 task),
		// this single line is replaced with that loader and RE-001
		// upgrades to full multi-repo enforcement automatically.
		validator = validation.NewEngine(deps.Store,
			validation.WithCatalogSnapshot(validation.PrimaryOnlyCatalogSnapshot(repository.PrimarySpec{})),
			// Validation policy registry wiring lands with TASK-004
			// (EPIC-006). The Noop resolver makes the seam visible
			// without changing today's LC-004 behavior — every
			// non-projection link target is still dangling.
			validation.WithGovernedFileResolver(validation.NoopGovernedFileResolver()))
	}

	wfResolver, wfProvider := buildWorkflowResolver(deps.Store, primaryClient)
	// buildOrchestrator forwards the client to engine.New, which
	// needs the engine.GitOperator surface (includes Checkout, not
	// part of git.GitClient). The concrete deps.GitClient is the
	// same instance pool.PrimaryClient() returns — passing it here
	// keeps semantics identical to a routed call without expanding
	// the abstract interface.
	orch := buildOrchestrator(deps.Store, wfProvider, deps.GitClient, deps.Events, deps.Queue, artifactSvc, validator, log)
	// Run-start repository preconditions (INIT-014 EPIC-002 TASK-004).
	// Reuses the registry built above so the pool and the orchestrator
	// resolve through the same authoritative source.
	if orch != nil && deps.Store != nil {
		orch.WithRepositoryResolver(repoRegistry)
	}
	// Multi-repo branch creation at run start (INIT-014 EPIC-004
	// TASK-002). The pool already routes by repository ID; wiring it
	// into the engine lets StartRun create the same run branch on
	// every affected code repo's working tree.
	if orch != nil && gitPool != nil {
		orch.WithRepositoryGitClients(gitPool)
	}
	// Runner-facing clone URL builder (INIT-014 EPIC-004 TASK-004,
	// governed by ADR-015) and workspace ID (TASK-005). Top-level
	// (single-workspace) path uses the runtime workspace ID — the
	// same value the gateway's git HTTP router resolves through
	// workspace.Resolver. SMPWorkspaceID is the *external* SMP
	// platform identifier and is not equivalent in deployments where
	// the two diverge; using it would produce `/git/{smp_id}/{repo}`
	// URLs that fail with workspace-not-found.
	if orch != nil {
		base := os.Getenv("SPINE_RUNNER_GIT_BASE_URL")
		if base != "" {
			if b := buildWorkspaceCloneURLBuilder(base, deps.WorkspaceID); b != nil {
				orch.WithCloneURLBuilder(b)
			}
		}
		orch.WithWorkspaceID(deps.WorkspaceID)
	}

	var sched *scheduler.Scheduler
	if deps.Store != nil {
		sched = buildScheduler(deps.Store, deps.Events, orch, deps.OrphanThreshold)
	}

	var deliveryCancel context.CancelFunc
	var deliverySubscriber *delivery.DeliverySubscriber
	if deps.EventDeliveryOn && deps.Store != nil {
		deliveryCancel, deliverySubscriber = startEventDelivery(ctx, deps, log)
	}

	var divSvcForGateway gateway.BranchCreator
	if deps.Store != nil {
		divSvcForGateway = divergence.NewService(deps.Store, primaryClient, deps.Events)
	}

	var starter gateway.RunStarter
	var planningStarter gateway.PlanningRunStarter
	var resultHandler gateway.ResultHandler
	var stepAssigner gateway.StepAssigner
	if orch != nil {
		orch.WithArtifactWriter(artifactSvc)
		orch.WithBlockingStore(deps.Store)
		starter = &runAdapter{orch: orch}
		planningStarter = &planningRunAdapter{orch: orch}
		resultHandler = &resultAdapter{orch: orch}
		stepAssigner = orch
	}

	var eventBroadcaster *delivery.EventBroadcaster
	if deliverySubscriber != nil {
		eventBroadcaster = deliverySubscriber.Broadcaster
	}

	gitHTTPPolicy, err := buildBranchProtectPolicy(deps.Store)
	if err != nil {
		return nil, fmt.Errorf("git-http branch-protect policy: %w", err)
	}
	gitHTTPHandler := buildGitHTTPHandler(deps.WSResolver, deps.TrustedGitHTTPCIDRs, deps.GitReceivePackEnabled, gitHTTPPolicy, log)
	gitPushResolver := buildGitPushResolver(deps.WSServicePool, deps.Store, deps.Events)

	cfg := gateway.ServerConfig{
		Store:                      deps.Store,
		Auth:                       authSvc,
		Artifacts:                  artifactSvc,
		Workflows:                  workflowSvc,
		ProjQuery:                  projQuery,
		ProjSync:                   projSync,
		Git:                        primaryClient,
		GitPool:                    gitPool,
		Validator:                  validator,
		WorkflowResolver:           wfResolver,
		BranchCreator:              divSvcForGateway,
		Events:                     deps.Events,
		RunStarter:                 starter,
		PlanningRunStarter:         planningStarter,
		ResultHandler:              resultHandler,
		WorkspaceResolver:          deps.WSResolver,
		ServicePool:                deps.WSServicePool,
		WSDBProvider:               deps.WSDBProvider,
		RunCanceller:               orch,
		RunMergeResolver:           orch,
		EvidenceQuerier:            buildEvidenceQuerier(primaryClient),
		CandidateFinder:            orch,
		StepClaimer:                orch,
		StepReleaser:               orch,
		StepExecutionLister:        orch,
		StepAcknowledger:           orch,
		StepAssigner:               stepAssigner,
		EventBroadcaster:           eventBroadcaster,
		GitHTTP:                    gitHTTPHandler,
		GitPushResolver:            gitPushResolver,
		BindingInvalidationHandler: deps.BindingInvalidationHandler,
		WebhookTargets:             deps.WebhookTargets,
		SSEMaxConnPerActor:         deps.SSEMaxConn,
		TrustedProxyCIDRs:          deps.TrustedProxyCIDRs,
		DevMode:                    deps.DevMode,
		Env:                        deps.RuntimeEnv,
	}

	return &serveRuntime{
		Config:         cfg,
		Scheduler:      sched,
		ProjSync:       projSync,
		DeliveryCancel: deliveryCancel,
	}, nil
}
