package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/bszymi/spine/internal/actor"
	"github.com/bszymi/spine/internal/auth"
	spinecrypto "github.com/bszymi/spine/internal/crypto"
	"github.com/bszymi/spine/internal/delivery"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/engine"
	"github.com/bszymi/spine/internal/evidence"
	"github.com/bszymi/spine/internal/secrets"
	"github.com/bszymi/spine/internal/workflow"
	"github.com/bszymi/spine/internal/workspace"
)

// workspaceOrchestratorBuilder constructs per-workspace engine orchestrator,
// run starters, and scheduler callbacks from the ServiceSet's basic services.
func workspaceOrchestratorBuilder(ctx context.Context, ss *workspace.ServiceSet) error {
	if ss.Store == nil {
		return nil
	}

	wfProvider := workflow.NewProjectionProviderFromListFn(func(ctx context.Context) ([]workflow.WorkflowProjection, error) {
		projs, err := ss.Store.ListActiveWorkflowProjections(ctx)
		if err != nil {
			return nil, err
		}
		result := make([]workflow.WorkflowProjection, len(projs))
		for i := range projs {
			result[i] = workflow.WorkflowProjection{
				WorkflowPath: projs[i].WorkflowPath,
				WorkflowID:   projs[i].WorkflowID,
				Name:         projs[i].Name,
				Version:      projs[i].Version,
				Status:       projs[i].Status,
				AppliesTo:    projs[i].AppliesTo,
				Definition:   projs[i].Definition,
				SourceCommit: projs[i].SourceCommit,
			}
		}
		return result, nil
	})
	// Run-time consumers (binding resolver, workflow loader) take the
	// abstract git.GitClient and so receive pool.PrimaryClient() —
	// keeping the call site routed through the pool. engine.New
	// requires the richer engine.GitOperator (includes Checkout, not
	// part of git.GitClient), so it stays on the concrete CLI client;
	// either form points at the same underlying repo.
	primaryClient := ss.GitPool.PrimaryClient()
	bindingResolver := engine.NewBindingResolver(wfProvider, primaryClient)

	actorSvc := actor.NewService(ss.Store)
	actorGw := actor.NewGateway(ss.Store, ss.Events, ss.Queue, actorSvc)
	wfLoader := engine.NewGitWorkflowLoader(primaryClient)

	orch, err := engine.New(
		bindingResolver, ss.Store, actorGw, ss.Artifacts, ss.Events, ss.GitClient, wfLoader,
	)
	if err != nil {
		return fmt.Errorf("engine orchestrator init: %w", err)
	}

	orch.WithAssignmentStore(ss.Store)
	orch.WithActorSelector(actorSvc)
	if ss.Validator != nil {
		orch.WithValidator(ss.Validator)
	}
	orch.WithDiscussions(ss.Store)
	if ss.Divergence != nil {
		orch.WithDivergence(ss.Divergence)
		orch.WithConvergence(ss.Divergence)
	}
	orch.WithArtifactWriter(ss.Artifacts)
	orch.WithBlockingStore(ss.Store)
	// Branch-protection guard for MergeRunBranch (ADR-009 §3). Same
	// projection-backed policy wired into the Artifact Service above, so
	// both API writes and governed merges share a single decision point.
	bpPolicy, err := buildBranchProtectPolicy(ss.Store)
	if err != nil {
		return fmt.Errorf("workspace orchestrator branch-protect policy: %w", err)
	}
	orch.WithBranchProtectPolicy(bpPolicy)
	// Run-start repository preconditions (INIT-014 EPIC-002 TASK-004).
	// Mirrors the wiring on the process-level orchestrator so shared
	// workspace deployments enforce the same invariants. The
	// workspace's ServiceSet already owns the registry constructed
	// in buildServiceSet, so reuse it here rather than building a
	// duplicate that could drift if the loader changes.
	if ss.Registry != nil {
		orch.WithRepositoryResolver(ss.Registry)
	}
	// Multi-repo branch creation at run start (INIT-014 EPIC-004
	// TASK-002). The workspace pool is the single point of per-repo
	// client resolution; wiring it here lets StartRun create the same
	// run branch on every affected code repo, not just the primary.
	if ss.GitPool != nil {
		orch.WithRepositoryGitClients(ss.GitPool)
	}
	// Runner-facing clone URL builder (INIT-014 EPIC-004 TASK-004,
	// governed by ADR-015 *Assignment payload shape*). Always the
	// workspace's git HTTP endpoint; the runtime binding's external
	// upstream URL is never published into assignment payloads.
	if base := os.Getenv("SPINE_RUNNER_GIT_BASE_URL"); base != "" {
		if b := buildWorkspaceCloneURLBuilder(base, ss.Config.ID); b != nil {
			orch.WithCloneURLBuilder(b)
		}
	}
	// Runner-facing workspace ID (INIT-014 EPIC-004 TASK-005). Wired
	// from the same source as buildWorkspaceCloneURLBuilder above so
	// assignment payloads always pair the workspace_id field with a
	// matching clone_url; the gateway's git HTTP router would reject a
	// mismatch as workspace-not-found at clone time.
	orch.WithWorkspaceID(ss.Config.ID)
	// Workflow writer is required for ADR-008 planning runs. INIT-022
	// EPIC-001 TASK-010 typed ss.Workflows as *workflow.Service which
	// satisfies engine.WorkflowWriter at compile time, so the previous
	// runtime type-assertion + fmt.Errorf path is no longer reachable.
	if ss.Workflows != nil {
		orch.WithWorkflowWriter(ss.Workflows)
	}

	// Contract: every gateway field the orchestrator owns must also live
	// on ServiceSet and have a *From(ctx) resolver in gateway/server.go.
	// In platform-binding mode the top-level gateway.ServerConfig has no
	// orchestrator (deps.Store is nil), so handlers that reach for
	// s.<field> directly nil-deref. Each per-workspace orch instance is
	// stashed here and resolved through the request's ServiceSet.
	// Per-workspace event delivery (subscriber + dispatcher) is wired
	// alongside this in newPooledWorkspaceBuilder so it shares the same
	// ServiceSet eviction lifecycle.
	ss.RunStarter = &runAdapter{orch: orch}
	ss.PlanningRunStarter = &planningRunAdapter{orch: orch}
	ss.WFPlanningStarter = &workflowPlanningRunAdapter{orch: orch}
	ss.RunCanceller = orch
	ss.RunMergeResolver = orch
	ss.StepAssigner = orch
	ss.ResultHandler = &resultAdapter{orch: orch}
	ss.StepAcknowledger = orch
	ss.CandidateFinder = orch
	ss.StepClaimer = orch
	ss.StepReleaser = orch
	ss.StepExecutionLister = orch
	// Per-workspace evidence querier (INIT-014 EPIC-006 TASK-005). The
	// querier reads /.spine/runs/{run_id}/evidence/{repository_id}.yaml
	// from the workspace's primary repo via ss.GitClient at the
	// authoritative branch — `main`, the same constant the engine
	// merge step (internal/engine/merge.go::authoritativeBranch)
	// writes to. Reader and writer MUST stay in sync; introducing a
	// configurable reader override here while the merge target is
	// hardcoded would create a silent missing-evidence trap for
	// operators whose configuration drifts. Unset when GitClient is
	// nil — the gateway's evidenceQuerierFrom accessor then resolves
	// to nil and run.status omits the evidence field rather than
	// 503-ing.
	if ss.GitClient != nil {
		ss.EvidenceQuerier = evidence.NewQuerier(ss.GitClient)
	}

	ss.CommitRetryFn = func(ctx context.Context, runID string) error {
		return orch.MergeRunBranch(ctx, runID)
	}
	ss.StepRecoveryFn = func(ctx context.Context, execID string) error {
		exec, err := ss.Store.GetStepExecution(ctx, execID)
		if err != nil {
			return err
		}
		if exec.Status == domain.StepStatusCompleted {
			return orch.SubmitStepResult(ctx, execID, engine.StepResult{OutcomeID: exec.OutcomeID})
		}
		return orch.RetryStep(ctx, exec)
	}
	ss.RunFailFn = func(ctx context.Context, runID, reason string) error {
		return orch.FailRun(ctx, runID, reason)
	}

	return nil
}

// newPooledWorkspaceBuilder composes the orchestrator wiring with
// optional per-workspace event delivery into a single
// workspace.ServiceSetBuilder. Used by db and platform-binding pool
// modes, where deps.Store is nil and the top-level startEventDelivery
// guard skips delivery entirely.
//
// Without this, every workspace event (`step.assigned`, `step.completed`,
// etc.) emitted onto the per-workspace ss.Events router has no
// subscriber, so runtime.event_log stays empty and no webhook ever
// fires. The fix routes the same DeliverySubscriber + WebhookDispatcher
// pair through every workspace's per-tenant store and event router,
// with cancellation tied to ServiceSet eviction.
func newPooledWorkspaceBuilder(deliveryCfg workspaceDeliveryConfig, log *slog.Logger) workspace.ServiceSetBuilder {
	return func(ctx context.Context, ss *workspace.ServiceSet) error {
		if err := workspaceOrchestratorBuilder(ctx, ss); err != nil {
			return err
		}
		bootstrapInternalAdmin(ctx, ss, deliveryCfg, log)
		if deliveryCfg.Enabled && ss.Store != nil && ss.Events != nil {
			wireWorkspaceDelivery(ctx, ss, deliveryCfg, log)
		}
		return nil
	}
}

// bootstrapInternalAdmin invokes auth.BootstrapInternalAdmin when
// SMP_ADMIN_TOKEN is configured. Runs independently of the event-
// delivery gate: a platform-binding deployment uses bearer auth on
// every workspace request whether or not it subscribes to events, so
// gating this on SPINE_EVENT_DELIVERY would leave the very 401 the
// bootstrap exists to prevent. Errors are logged and swallowed so a
// transient bootstrap failure does not block workspace activation.
func bootstrapInternalAdmin(ctx context.Context, ss *workspace.ServiceSet, cfg workspaceDeliveryConfig, log *slog.Logger) {
	if cfg.SMPAdminToken == "" || ss.Store == nil {
		return
	}
	if err := auth.BootstrapInternalAdmin(ctx, ss.Store, auth.BootstrapAdminConfig{
		Token: cfg.SMPAdminToken,
	}); err != nil {
		log.Error("workspace bootstrap internal admin failed", "workspace", ss.Config.ID, "error", err)
	}
}

// wireWorkspaceDelivery starts a per-workspace DeliverySubscriber,
// WebhookDispatcher, and retention cleanup loop, all bound to the
// workspace's own store and event router. Cancellation is tied to
// ServiceSet eviction via AppendCloser so a workspace going idle (or
// the pool closing) tears the goroutines down cleanly.
//
// The workspace's SMP binding (ss.Config.SMPWorkspaceID) is the source
// of truth for the bootstrap subscription's WorkspaceID — using the
// process-level SMP_WORKSPACE_ID env var here would label every
// workspace's internal subscription with the same SMP ID, which is
// only correct in single-tenant dedicated mode.
//
// Idle-eviction trade-off: when the pool evicts an idle workspace its
// dispatcher stops, but pending and scheduled-retry deliveries persist
// in the per-workspace runtime.event_delivery_queue. The next pool.Get
// for that workspace re-invokes this function, and the fresh dispatcher
// claims any deliveries whose next_attempt_at is in the past — so a
// retry scheduled past the idle window is still delivered the next
// time the workspace sees traffic. The remaining failure mode is a
// workspace that is permanently inactive after a retry is queued; for
// the default backoff (max ~16s, well under the 10-min default
// IdleTimeout) this is unreachable. Operators choosing a short
// SPINE_WS_POOL_IDLE_TIMEOUT relative to peer Retry-After windows
// should expect retry latency to extend until the next pool.Get for
// the affected workspace.
func wireWorkspaceDelivery(ctx context.Context, ss *workspace.ServiceSet, cfg workspaceDeliveryConfig, log *slog.Logger) {
	deliveryCtx, cancel := context.WithCancel(ctx)

	if cfg.SMPEventURL != "" {
		if err := delivery.BootstrapInternalSubscription(deliveryCtx, ss.Store, delivery.BootstrapConfig{
			EventURL:    cfg.SMPEventURL,
			WorkspaceID: ss.Config.SMPWorkspaceID,
			Token:       cfg.SMPInternalToken,
		}); err != nil {
			log.Error("workspace bootstrap internal subscription failed", "workspace", ss.Config.ID, "error", err)
		}
	}

	subLister := delivery.NewStoreSubscriptionLister(ss.Store)
	subscriber := delivery.NewDeliverySubscriber(ss.Store, subLister)
	if err := subscriber.Subscribe(deliveryCtx, ss.Events); err != nil {
		log.Error("workspace delivery subscriber failed", "workspace", ss.Config.ID, "error", err)
		cancel()
		return
	}

	subResolver := delivery.NewStoreSubscriptionResolver(ss.Store)
	dispatcher := delivery.NewWebhookDispatcher(ss.Store, subResolver, delivery.DispatcherConfig{
		Targets: cfg.WebhookTargets,
	})
	go dispatcher.Run(deliveryCtx)
	go delivery.StartRetentionCleanup(deliveryCtx, ss.Store, cfg.EventRetention)

	// Plumb the subscriber's broadcaster onto the ServiceSet so the
	// gateway's /api/v1/events/stream handler can resolve a workspace-
	// scoped SSE fan-out via *From(ctx). Without this hand-off,
	// platform-binding stacks fail SSE with 503 even though delivery
	// is wired — same nil-deref shape TASK-002 fixed for the lifecycle
	// handlers.
	ss.EventBroadcaster = subscriber.Broadcaster

	log.Info("per-workspace event delivery started", "workspace", ss.Config.ID)

	ss.AppendCloser(func(string) { cancel() })
}

// resolverWiring is the bundle returned by buildWorkspaceResolver:
// the resolver itself, the optional concrete DBProvider (for
// workspace-management endpoints), the optional ServicePool, and the
// optional binding-invalidation webhook handler that drives both the
// resolver cache and the pool when the platform pushes an
// invalidation (ADR-011).
type resolverWiring struct {
	Resolver                   workspace.Resolver
	DBProvider                 *workspace.DBProvider
	Pool                       *workspace.ServicePool
	BindingInvalidationHandler http.Handler
	// SecretClient is the workspace-scoped SecretClient already wired
	// for runtime DB resolution (db / platform-binding modes). The
	// dedicated-mode git client pool reuses it for per-binding
	// `credentials_ref` resolution so we don't build a parallel client
	// for the same backend (INIT-014 EPIC-003 TASK-006).
	SecretClient secrets.SecretClient
}

// buildWorkspaceResolver initializes the workspace resolver based on
// the WORKSPACE_RESOLVER env var (file | db | platform-binding).
//
// SPINE_WORKSPACE_MODE was retired in ADR-011; if it is set without
// WORKSPACE_RESOLVER, startup fails fast so a deployment that missed
// the migration cannot silently fall back.
//
// deliveryCfg is threaded into the pool builder for db and
// platform-binding modes so each workspace gets its own subscriber +
// dispatcher pair (TASK-003). File mode is single-workspace and uses
// the top-level startEventDelivery, so it ignores the value.
func buildWorkspaceResolver(
	ctx context.Context,
	secretCipher *spinecrypto.SecretCipher,
	log *slog.Logger,
	deliveryCfg workspaceDeliveryConfig,
	codeRepoBase string,
) (*resolverWiring, error) {
	resolver := strings.ToLower(strings.TrimSpace(os.Getenv("WORKSPACE_RESOLVER")))
	legacyMode := os.Getenv("SPINE_WORKSPACE_MODE")

	if resolver == "" {
		if legacyMode != "" {
			return nil, fmt.Errorf("SPINE_WORKSPACE_MODE=%q is no longer supported; set WORKSPACE_RESOLVER=file|db|platform-binding (see ADR-011)", legacyMode)
		}
		resolver = "file"
	}

	switch resolver {
	case "file":
		fileSecretClient, err := buildFileResolverSecretClient(ctx)
		if err != nil {
			return nil, err
		}
		log.Info("workspace resolver: file", "workspace_id", os.Getenv("SPINE_WORKSPACE_ID"))
		// fileSecretClient is propagated to deps so the dedicated git
		// pool can dereference per-binding credentials_ref through the
		// same backend already used for runtime DB resolution. Without
		// this, file-mode deployments would always see "no secret
		// client configured" for credentialed bindings even when the
		// file backend is fully wired.
		return &resolverWiring{
			Resolver:     workspace.NewFileProvider(fileSecretClient),
			SecretClient: fileSecretClient,
		}, nil

	case "db":
		registryURL := os.Getenv("SPINE_REGISTRY_DATABASE_URL")
		if registryURL == "" {
			return nil, fmt.Errorf("SPINE_REGISTRY_DATABASE_URL is required for WORKSPACE_RESOLVER=db")
		}
		if err := requireSecureDBURL(registryURL); err != nil {
			return nil, fmt.Errorf("registry URL: %w", err)
		}
		// SecretClient is required for ref-shaped database_url rows.
		// Optional when no secret backend is configured — legacy URL
		// rows still work, but ref rows would fail at Resolve.
		var dbSecretClient secrets.SecretClient
		if secretClientConfigured() {
			c, err := buildSecretClient(ctx)
			if err != nil {
				return nil, err
			}
			dbSecretClient = c
		}
		provider, err := workspace.NewDBProvider(ctx, registryURL, workspace.DBProviderConfig{
			SecretClient: dbSecretClient,
		})
		if err != nil {
			return nil, fmt.Errorf("connect to workspace registry: %w", err)
		}
		pool := workspace.NewServicePool(ctx, provider, workspace.PoolConfig{
			Builder:      newPooledWorkspaceBuilder(deliveryCfg, log),
			SecretCipher: secretCipher,
			SecretClient: dbSecretClient,
			DBPolicy:     dbPolicyFromEnv(),
			IdleTimeout:  poolIdleTimeoutFromEnv(),
			CodeRepoBase: codeRepoBase,
		})
		log.Info("workspace resolver: db", "registry_url", "***")
		return &resolverWiring{Resolver: provider, DBProvider: provider, Pool: pool, SecretClient: dbSecretClient}, nil

	case "platform-binding":
		platformURL := os.Getenv("SPINE_PLATFORM_URL")
		if platformURL == "" {
			return nil, fmt.Errorf("SPINE_PLATFORM_URL is required for WORKSPACE_RESOLVER=platform-binding")
		}
		serviceToken := os.Getenv("SPINE_PLATFORM_SERVICE_TOKEN")
		if serviceToken == "" {
			return nil, fmt.Errorf("SPINE_PLATFORM_SERVICE_TOKEN is required for WORKSPACE_RESOLVER=platform-binding")
		}
		secretClient, err := buildSecretClient(ctx)
		if err != nil {
			return nil, err
		}
		provider, err := workspace.NewPlatformBindingProvider(workspace.PlatformBindingConfig{
			PlatformBaseURL: platformURL,
			ServiceToken:    serviceToken,
			SecretClient:    secretClient,
		})
		if err != nil {
			return nil, fmt.Errorf("init platform-binding resolver: %w", err)
		}
		// Multi-workspace traffic routes per-workspace stores/git
		// services through ServicePool; wire it on top of the new
		// resolver so requests don't fall through to the
		// process-level store. EPIC-003 (TASK-006/007) refines pool
		// sizing and invalidation for this mode.
		pool := workspace.NewServicePool(ctx, provider, workspace.PoolConfig{
			Builder:      newPooledWorkspaceBuilder(deliveryCfg, log),
			SecretCipher: secretCipher,
			SecretClient: secretClient,
			DBPolicy:     dbPolicyFromEnv(),
			IdleTimeout:  poolIdleTimeoutFromEnv(),
			CodeRepoBase: codeRepoBase,
		})
		invalidator := &workspace.CombinedBindingInvalidator{
			Provider: provider,
			Pool:     pool,
		}
		handler, err := workspace.NewBindingInvalidateHandler(workspace.BindingInvalidateHandlerConfig{
			Invalidator:  invalidator,
			ServiceToken: serviceToken,
		})
		if err != nil {
			return nil, fmt.Errorf("init binding invalidate handler: %w", err)
		}
		log.Info("workspace resolver: platform-binding", "platform_url", platformURL)
		return &resolverWiring{
			Resolver:                   provider,
			Pool:                       pool,
			BindingInvalidationHandler: handler,
			SecretClient:               secretClient,
		}, nil

	default:
		return nil, fmt.Errorf("unknown WORKSPACE_RESOLVER=%q (expected file|db|platform-binding)", resolver)
	}
}
