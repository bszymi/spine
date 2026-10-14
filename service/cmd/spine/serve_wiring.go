package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/bszymi/spine/adapters/delivery"
	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/gitpool"
	"github.com/bszymi/spine/adapters/projection"
	"github.com/bszymi/spine/adapters/repository"
	"github.com/bszymi/spine/adapters/scheduler"
	"github.com/bszymi/spine/adapters/secrets"
	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/auth"
	spinecrypto "github.com/bszymi/spine/core/crypto"
	"github.com/bszymi/spine/core/divergence"
	"github.com/bszymi/spine/core/engine"
	"github.com/bszymi/spine/core/event"
	"github.com/bszymi/spine/core/observe"
	"github.com/bszymi/spine/core/queue"
	"github.com/bszymi/spine/core/refadmit"
	"github.com/bszymi/spine/core/validation"
	"github.com/bszymi/spine/service/internal/config"
	"github.com/bszymi/spine/service/internal/gateway"
	"github.com/bszymi/spine/service/internal/workspace"
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
		ExpectedHeadSHA:   req.ExpectedHeadSHA,
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

// The pooled path stores the adapter in workspace.ServiceSet's untyped
// WFPlanningStarter field and the gateway type-asserts it per request, so a
// missing method would compile and then answer 503 for every reviewer
// workflow write. Assert the interface here (INIT-023 TASK-014).
var _ gateway.WorkflowPlanningRunStarter = (*workflowPlanningRunAdapter)(nil)

func (a *workflowPlanningRunAdapter) StartWorkflowPlanningRun(ctx context.Context, workflowID, body string) (*gateway.PlanningRunResult, error) {
	result, err := a.orch.StartWorkflowPlanningRun(ctx, workflowID, body)
	if err != nil {
		return nil, err
	}
	return planningRunResult(result), nil
}

// StartWorkflowUpdatePlanningRun opens the lifecycle run for an edit of an
// existing workflow (INIT-023 TASK-014, audit F7).
func (a *workflowPlanningRunAdapter) StartWorkflowUpdatePlanningRun(ctx context.Context, workflowID, body string) (*gateway.PlanningRunResult, error) {
	result, err := a.orch.StartWorkflowUpdatePlanningRun(ctx, workflowID, body)
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
	// credentials-unavailable error; bindings without one clone and
	// push with no credential at all — never the process-wide
	// SPINE_GIT_PUSH_TOKEN or credential helper (INIT-023 TASK-015).
	SecretClient        secrets.SecretClient
	RuntimeEnv          string
	DevMode             bool
	TrustedProxyCIDRs   []*net.IPNet
	TrustedGitHTTPCIDRs []string
	// GitHTTPDevNoAuth (SPINE_GIT_HTTP_DEV_NO_AUTH; serveCmd refuses it
	// unless SPINE_ENV=development) lets /git clone and fetch skip the
	// bearer. Pushes always need one. INIT-023 TASK-015.
	GitHTTPDevNoAuth bool
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

	// NotifyBridgeEnabled gates the runtime.event_log NOTIFY listener
	// that forwards out-of-process event_log writes into the in-process
	// EventBroadcaster (INIT-011/EPIC-003/TASK-006 sub-PR 10a). Sourced
	// from SPINE_EVENT_LOG_NOTIFY_BRIDGE. Default off so deployments
	// without an SMP-side notify-emitter (sub-PR 10b) don't burn a
	// pgxpool connection on a noop loop.
	NotifyBridgeEnabled bool

	// WebhookTargets is the SSRF gate applied to every subscription
	// target_url on create / update / test and to every webhook
	// delivery. Operators widen the default "public HTTPS only" policy
	// with SPINE_WEBHOOK_ALLOWED_HOSTS (comma-separated) when internal
	// or localhost destinations are intentional.
	WebhookTargets *delivery.TargetValidator

	// StepEntryPolicy is the engine step-entry policy installed on the
	// process-level orchestrator (INIT-023 TASK-007). serveCmd sets it
	// from SPINE_STEP_ENTRY_POLICY (strict by default); a non-zero
	// policy also disables the gateway's engine-less step-assign
	// fallback, which would otherwise bind with no checks at all.
	StepEntryPolicy engine.StepEntryPolicy
}

// serveRuntime bundles the ServerConfig with the long-lived background
// services the serve loop must start and stop.
type serveRuntime struct {
	Config         gateway.ServerConfig
	Scheduler      *scheduler.Scheduler
	ProjSync       *projection.Service
	MultiProjSync  *workspace.MultiProjectionSync
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
	// WithCloner and the factory are both threaded with the options
	// baked into deps.GitClient — process-level SPINE_GIT_PUSH_TOKEN /
	// credential-helper opts (PushAuthOpts) plus the dedicated-mode
	// SMP_WORKSPACE_ID env. Code repos authenticate only with their own
	// credentials_ref: SecretCredentialResolver layers a per-binding
	// token on top via WithPushToken, and for a binding without one the
	// cloner and factory strip the process token and credential helper
	// (INIT-023 TASK-015; owner question 2 ruled "never", pending the
	// owner), so the deployment's credential never reaches the host a
	// binding's clone_url names. Only the primary repository (this
	// deps.GitClient, returned by PrimaryClient) uses the process
	// credentials. The resolver is installed unconditionally (even when
	// SecretClient is nil) so a misconfigured deployment with
	// credentialed bindings fails closed instead of cloning
	// unauthenticated.
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
	workflowSvc, err := buildWorkflowService(primaryClient, deps.RepoPath, deps.Store)
	if err != nil {
		return nil, fmt.Errorf("workflow service: %w", err)
	}

	var projQuery *projection.QueryService
	var projSync *projection.Service
	if deps.Store != nil {
		projQuery = projection.NewQueryService(deps.Store, primaryClient)
		projSync = projection.NewService(primaryClient, deps.Store, deps.Events, 30*time.Second)
		projSync.WithArtifactsDir(deps.SpineCfg.ArtifactsDir)
		// The single-workspace repository is a non-bare working tree
		// the engine checks run branches out in: follow the
		// governed-merge target, not HEAD (INIT-023 TASK-011, audit
		// P3-22). One startup Warn when the branch does not resolve.
		if err := projSync.WithAuthoritativeBranch(engine.AuthoritativeBranch); err != nil {
			return nil, fmt.Errorf("projection authoritative branch: %w", err)
		}
		_ = projSync.ProbeAuthoritativeRef(ctx)
		// Protection-removal guard (INIT-023 TASK-006), on unless
		// SPINE_GOVERNANCE_GUARD=off.
		enableProtectionRemovalGuard(projSync, deps.Store)
	}

	// In platform-binding mode the global projSync above is moot: every
	// workspace has its own projection store inside its ServiceSet, and
	// the top-level ProjSync has no notion of "which workspace". Wire the
	// multi-workspace driver that iterates over resolved bindings and
	// calls each ServiceSet's own IncrementalSync.
	//
	// The driver is best-effort and skips workspaces whose binding is not
	// yet cached on the PlatformBindingProvider (no enumeration endpoint
	// exists yet on the platform — see PlatformBindingProvider.List).
	// New workspaces enter the rotation as soon as the first request
	// resolves them, which the customer-app's provisioner already does
	// during seed_governance / seed_skills.
	//
	// Poll interval comes from SPINE_PROJECTION_POLLING_INTERVAL with a
	// 30s default. The dev/test compose files set this env explicitly so
	// scenariotests don't have to poll for projection convergence over
	// the full 30s window.
	var multiProjSync *workspace.MultiProjectionSync
	if deps.WSServicePool != nil && deps.WSResolver != nil {
		interval := 30 * time.Second
		if raw := os.Getenv("SPINE_PROJECTION_POLLING_INTERVAL"); raw != "" {
			if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
				interval = parsed
			} else {
				log.Warn("invalid SPINE_PROJECTION_POLLING_INTERVAL; falling back to default",
					"value", raw, "default", interval)
			}
		}
		multiProjSync = workspace.NewMultiProjectionSync(
			deps.WSServicePool, deps.WSResolver,
			workspace.MultiProjectionSyncConfig{PollInterval: interval},
		)
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
	orch := buildOrchestrator(deps.Store, wfProvider, deps.GitClient, deps.Events, deps.Queue, artifactSvc, validator, deps.StepEntryPolicy, workflowDirsFor(deps.SpineCfg), log)
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
		// TASK-004 (ADR-018 §6.1): no separate WithWorkspaceID — the
		// workspace ID rides on the CloneURLBuilder's return value.
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
	var wfPlanningStarter gateway.WorkflowPlanningRunStarter
	var resultHandler gateway.ResultHandler
	var stepAssigner gateway.StepAssigner
	if orch != nil {
		orch.WithArtifactWriter(artifactSvc)
		orch.WithBlockingStore(deps.Store)
		// Workflow-lifecycle planning runs (ADR-008) in single-workspace
		// mode too (INIT-023 TASK-014, Decision 9). Pooled workspaces wire
		// theirs in serve_resolver.go; before this, a file-resolver
		// deployment had no workflow planning starter, and once reviewer
		// workflow updates are routed through the lifecycle every one of
		// them would have answered 503 with no governed path.
		orch.WithWorkflowWriter(workflowSvc)
		starter = &runAdapter{orch: orch}
		planningStarter = &planningRunAdapter{orch: orch}
		wfPlanningStarter = &workflowPlanningRunAdapter{orch: orch}
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
	// Ref-class push admission (INIT-023 TASK-016): the process-level
	// run reader and review-freeze lookup (the orchestrator, which
	// loads each run's pinned workflow) back single mode; shared mode
	// attaches each workspace's own pair per push.
	var gitStepDefs refadmit.StepDefLookup
	if orch != nil {
		gitStepDefs = orch
	}
	gitHTTPHandler := buildGitHTTPHandler(deps.WSResolver, deps.TrustedGitHTTPCIDRs, deps.GitReceivePackEnabled, gitHTTPPolicy, runReaderOrNil(deps.Store), gitStepDefs, deps.WSServicePool != nil, log)
	gitPushResolver := buildGitPushResolver(deps.WSServicePool, deps.Store, deps.Events, gitStepDefs)

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
		WFPlanningStarter:          wfPlanningStarter,
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
		GitHTTPDevNoAuth:           deps.GitHTTPDevNoAuth,
		Env:                        deps.RuntimeEnv,
		// INIT-023 TASK-007: with a step-entry policy installed, the
		// gateway must not fall back to its engine-less store binder (it
		// performs none of the policy's checks); a missing orchestrator
		// answers 503 instead.
		DisableStepAssignFallback: !deps.StepEntryPolicy.IsZero(),
	}

	return &serveRuntime{
		Config:         cfg,
		Scheduler:      sched,
		ProjSync:       projSync,
		MultiProjSync:  multiProjSync,
		DeliveryCancel: deliveryCancel,
	}, nil
}
