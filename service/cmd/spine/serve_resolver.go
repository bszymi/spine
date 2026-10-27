package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/bszymi/spine/adapters/delivery"
	"github.com/bszymi/spine/adapters/secrets"
	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/actor"
	"github.com/bszymi/spine/core/auth"
	spinecrypto "github.com/bszymi/spine/core/crypto"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/engine"
	"github.com/bszymi/spine/core/evidence"
	"github.com/bszymi/spine/core/observe"
	"github.com/bszymi/spine/core/workflow"
	"github.com/bszymi/spine/service/internal/workspace"
)

// workspaceOrchestratorBuilder constructs per-workspace engine orchestrator,
// run starters, and scheduler callbacks from the ServiceSet's basic services.
// stepEntryPolicy is the process-wide SPINE_STEP_ENTRY_POLICY selection, so
// every workspace's orchestrator enforces the same step-entry checks as the
// process-level one.
func workspaceOrchestratorBuilder(ctx context.Context, ss *workspace.ServiceSet, stepEntryPolicy engine.StepEntryPolicy) error {
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
	// SHA-bound governed merges (INIT-023 TASK-005), as on the
	// process-level orchestrator; ss.GitClient is a *git.CLIClient, which
	// implements engine.CommitBinder.
	if err := orch.EnableMergeBinding(engine.MergeBindingConfig{}); err != nil {
		return fmt.Errorf("workspace orchestrator merge binding: %w", err)
	}
	// Declared artifact-status lifecycle (INIT-023 TASK-017), as on the
	// process-level orchestrator, under this workspace's artifacts_dir
	// (the workspace artifact service's, which StartRun reads with).
	orch.WithStatusLifecycle(domain.DefaultStatusLifecycle())
	orch.WithArtifactsDir(ss.Artifacts.ArtifactsDir())
	// Governance-path guards (INIT-023 TASK-006), as on the process-level
	// orchestrator and projection: the engine guard over this workspace
	// projection's workflow directories, and the projection's
	// protection-removal guard. The builder runs before the ServiceSet is
	// returned, so no sync has run yet.
	if err := enableGovernanceGuard(orch, workspaceWorkflowDirs(ss)); err != nil {
		return fmt.Errorf("workspace orchestrator governance guard: %w", err)
	}
	enableProtectionRemovalGuard(ss.ProjSync, ss.Store)
	// Engine step-entry policy (INIT-023 TASK-007), same selection as the
	// process-level orchestrator.
	orch.WithStepEntryPolicy(stepEntryPolicy)
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
	// TASK-004 (ADR-018 §6.1): no separate WithWorkspaceID — the
	// workspace ID rides on the CloneURLBuilder's return value
	// alongside the matching clone URL, so the gateway's git HTTP
	// router and the runner-facing payload always agree by
	// construction.
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

	ss.CommitRetryFn = commitRetryFor(orch)
	// The merge retry budget's exhaustion handler (INIT-023 TASK-012,
	// P3-27), which the shared-mode multi-workspace scheduler applies with
	// defaultCommitMaxRetries (INIT-024 TASK-008, serve_scheduler.go).
	ss.CommitExhaustedFn = orch.AbandonExhaustedMerge
	// Recovery re-routes a completed execution through RecoverCompletedStep,
	// never SubmitStepResult (INIT-023 TASK-008; stepRecoveryFor).
	ss.StepRecoveryFn = stepRecoveryFor(ss.Store, orch)
	ss.RunFailFn = func(ctx context.Context, runID, reason string) error {
		return orch.FailRun(ctx, runID, reason)
	}
	// Run-timeout hook for the multi-workspace scheduler (INIT-023
	// TASK-007); mirrors buildScheduler's WithRunTerminated.
	ss.RunTerminatedFn = orch.TerminateLiveSteps

	return nil
}

// workspaceWorkflowDirs is the workflow-directory set of a workspace's
// projection (its artifacts_dir), or the root set when the ServiceSet has
// no projection service.
func workspaceWorkflowDirs(ss *workspace.ServiceSet) []string {
	if ss.ProjSync != nil {
		return ss.ProjSync.WorkflowDirs()
	}
	return workflowDirsFor(nil)
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
func newPooledWorkspaceBuilder(deliveryCfg workspaceDeliveryConfig, stepEntryPolicy engine.StepEntryPolicy, log *slog.Logger) workspace.ServiceSetBuilder {
	return func(ctx context.Context, ss *workspace.ServiceSet) error {
		if err := workspaceOrchestratorBuilder(ctx, ss, stepEntryPolicy); err != nil {
			return err
		}
		if err := bootstrapInternalAdmin(ctx, ss, deliveryCfg, log); err != nil {
			return err
		}
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
// bootstrap exists to prevent.
//
// Error policy:
//
//   - auth.ErrAdminTokenHashCollision under SPINE_ENV=production
//     (cfg.ProductionStrict) is returned so the pool builder fails
//     workspace load. The bearer would otherwise be silently bound to
//     the wrong actor on every workspace request, and the symptom
//     ("401 unauthorized") points operators at the platform side
//     rather than the colliding auth.tokens row. Strict-startup is
//     the same policy that gates SPINE_DEV_MODE,
//     SPINE_CODE_REPO_BASE, and SPINE_SECRET_ENCRYPTION_KEY at boot.
//     Off production the collision is logged and swallowed, and not
//     retried: it is deterministic until an operator cleans up the
//     colliding row. It is logged before being returned too, so the
//     production failure has a structured log breadcrumb alongside the
//     workspace-load error.
//   - All other errors are logged at Error and swallowed, so a
//     transient store blip does not knock a workspace out of rotation,
//     and the bootstrap is retried in the background
//     (retryInternalAdminBootstrap) until it succeeds or this
//     ServiceSet closes. The retry matters for security, not just
//     availability: a failed bootstrap may have left a superseded
//     bootstrap bearer live after an SMP_ADMIN_TOKEN rotation, and this
//     builder only runs again when the ServiceSet is rebuilt — after an
//     idle eviction, a binding invalidation or a restart, which a busy
//     workspace may not see for as long as the process lives.
func bootstrapInternalAdmin(ctx context.Context, ss *workspace.ServiceSet, cfg workspaceDeliveryConfig, log *slog.Logger) error {
	if cfg.SMPAdminToken == "" || ss.Store == nil {
		return nil
	}
	// The builder runs on the pool's context, which names no workspace;
	// core/auth logs (a revoked current bearer, a superseded row left in
	// place, each revocation and its audit line) must say which
	// workspace they concern, because the operator acts per workspace.
	ctx = observe.WithWorkspaceID(ctx, ss.Config.ID)
	// The first attempt is one of the build's one-shot calls, bounded by
	// its init deadline (workspace.InitContext); the background retry
	// below keeps the set's lifetime context.
	initCtx, cancelInit := workspace.InitContext(ctx)
	err := runInternalAdminBootstrap(initCtx, ss.Store, cfg)
	cancelInit()
	if err == nil {
		return nil
	}
	log.Error("workspace bootstrap internal admin failed", "workspace", ss.Config.ID, "error", err)
	if errors.Is(err, auth.ErrAdminTokenHashCollision) {
		if cfg.ProductionStrict {
			return fmt.Errorf("workspace %q: %w", ss.Config.ID, err)
		}
		return nil
	}
	// ctx is the pool's lifetime context. The retry stops when this
	// ServiceSet closes: AppendCloser cancels an attempt in flight on a
	// normal close, and Done also covers a builder failure, whose
	// teardown skips appended closers. The next build of the workspace
	// runs the bootstrap afresh.
	retryCtx, cancel := context.WithCancel(ctx)
	ss.AppendCloser(func(string) { cancel() })
	go retryInternalAdminBootstrap(retryCtx, ss.Store, ss.Done, ss.Config.ID, cfg, log,
		internalAdminRetryInitialDelay, internalAdminRetryMaxDelay)
	return nil
}

// runInternalAdminBootstrap is the one auth.BootstrapInternalAdmin call
// shape, shared by the first attempt and the retries.
func runInternalAdminBootstrap(ctx context.Context, st store.AuthStore, cfg workspaceDeliveryConfig) error {
	return auth.BootstrapInternalAdmin(ctx, st, auth.BootstrapAdminConfig{
		Token: cfg.SMPAdminToken,
		// A rotation revokes only bootstrap rows created before this
		// process booted (INIT-023 TASK-013): an old replica in a
		// rolling deploy never revokes the newer bearer.
		SupersededBefore: cfg.ProcessStart,
	})
}

// Pacing of the background admin-bootstrap retry: exponential from the
// initial delay, capped at the maximum, unbounded in count. Variables
// so tests can shorten them; read once, by the goroutine that starts
// the retry, so a retry in flight never reads them.
var (
	internalAdminRetryInitialDelay = time.Second
	internalAdminRetryMaxDelay     = time.Minute
)

// retryInternalAdminBootstrap re-runs the idempotent admin bootstrap
// after a failed first attempt (INIT-023 TASK-013 review round 1). It
// returns on the first success, when ctx is cancelled or done closes
// (the ServiceSet is closing), or on a hash collision, which retrying
// cannot fix. Every failed attempt is logged at Error with its attempt
// number: while it lasts, a superseded bootstrap bearer may still be
// live in this workspace.
func retryInternalAdminBootstrap(ctx context.Context, st store.AuthStore, done <-chan struct{}, workspaceID string,
	cfg workspaceDeliveryConfig, log *slog.Logger, initialDelay, maxDelay time.Duration) {
	delay := initialDelay
	for attempt := 2; ; attempt++ {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-done:
			timer.Stop()
			return
		case <-timer.C:
		}
		err := runInternalAdminBootstrap(ctx, st, cfg)
		if err == nil {
			log.Info("workspace bootstrap internal admin succeeded on retry", "workspace", workspaceID, "attempt", attempt)
			return
		}
		if ctx.Err() != nil {
			return
		}
		log.Error("workspace bootstrap internal admin failed", "workspace", workspaceID, "attempt", attempt, "error", err)
		if errors.Is(err, auth.ErrAdminTokenHashCollision) {
			return
		}
		delay = min(delay*2, maxDelay)
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
		// A one-shot call of the build: under its init deadline
		// (workspace.InitContext). The dispatcher below keeps deliveryCtx.
		initCtx, cancelInit := workspace.InitContext(deliveryCtx)
		if err := delivery.BootstrapInternalSubscription(initCtx, ss.Store, delivery.BootstrapConfig{
			EventURL:    cfg.SMPEventURL,
			WorkspaceID: ss.Config.SMPWorkspaceID,
			Token:       cfg.SMPInternalToken,
		}); err != nil {
			log.Error("workspace bootstrap internal subscription failed", "workspace", ss.Config.ID, "error", err)
		}
		cancelInit()
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

// sharedModePoolConfig is the ServicePool configuration of both shared
// resolvers (db and platform-binding). It is one constructor so the
// subscription-secret cipher and envelope mode — every per-workspace store
// is bound and swept with them (INIT-023 TASK-009) — cannot be dropped
// from one branch: "" means migrate, so a dropped mode would compile, log
// the configured mode at startup, and still bind every store in migrate,
// converting the legacy rows strict exists to refuse. buildWorkspaceResolver
// calls it from exactly one place (newSharedPool), and the
// TestBuildServeWorkspaces_* tests drive that place from the environment.
func sharedModePoolConfig(
	deliveryCfg workspaceDeliveryConfig,
	log *slog.Logger,
	secretCipher *spinecrypto.SecretCipher,
	secretEnvelopeMode store.SecretEnvelopeMode,
	secretClient secrets.SecretClient,
	codeRepoBase string,
	stepEntryPolicy engine.StepEntryPolicy,
) workspace.PoolConfig {
	// buildWorkspaceResolver has refused an invalid value already.
	initTimeout, _ := poolInitTimeoutFromEnv()
	return workspace.PoolConfig{
		Builder:            newPooledWorkspaceBuilder(deliveryCfg, stepEntryPolicy, log),
		InitTimeout:        initTimeout,
		SecretCipher:       secretCipher,
		SecretEnvelopeMode: secretEnvelopeMode,
		SecretClient:       secretClient,
		DBPolicy:           dbPolicyFromEnv(),
		IdleTimeout:        poolIdleTimeoutFromEnv(),
		CodeRepoBase:       codeRepoBase,
	}
}

// poolInitTimeoutEnv overrides workspace.PoolConfig.InitTimeout (default
// workspace.DefaultInitTimeout, 2 minutes): how long a request or a scan
// waits for a workspace's services to be built, and the deadline of the
// build's one-shot calls (INIT-024 TASK-008). Raise it for a workspace
// whose healthy build is slower than that.
const poolInitTimeoutEnv = "SPINE_WS_POOL_INIT_TIMEOUT"

// poolInitTimeoutFromEnv reads SPINE_WS_POOL_INIT_TIMEOUT. Unset, empty or
// blank means the default (0); anything but a positive Go duration is an
// error, which buildWorkspaceResolver turns into a refusal to start (as
// for the SPINE_SCHEDULER_* bounds; no silent fallback to a value the
// operator did not choose).
func poolInitTimeoutFromEnv() (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(poolInitTimeoutEnv))
	if raw == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s=%q: want a positive duration such as 2m", poolInitTimeoutEnv, raw)
	}
	return d, nil
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
	secretEnvelopeMode store.SecretEnvelopeMode,
	log *slog.Logger,
	deliveryCfg workspaceDeliveryConfig,
	codeRepoBase string,
	stepEntryPolicy engine.StepEntryPolicy,
) (*resolverWiring, error) {
	resolver := strings.ToLower(strings.TrimSpace(os.Getenv("WORKSPACE_RESOLVER")))
	legacyMode := os.Getenv("SPINE_WORKSPACE_MODE")
	// Validated in every mode, so a mistyped value never starts silently;
	// only the shared modes' pool reads it (sharedModePoolConfig).
	if _, err := poolInitTimeoutFromEnv(); err != nil {
		return nil, err
	}

	if resolver == "" {
		if legacyMode != "" {
			return nil, fmt.Errorf("SPINE_WORKSPACE_MODE=%q is no longer supported; set WORKSPACE_RESOLVER=file|db|platform-binding (see ADR-011)", legacyMode)
		}
		resolver = "file"
	}

	// newSharedPool is the one ServicePool constructor of both shared
	// resolvers (db and platform-binding). The subscription-secret cipher
	// and envelope mode enter it here, once; the branches below pass only
	// what differs between them (the provider and its secret client), so
	// no branch can bind its workspaces' stores in a mode other than the
	// configured one (INIT-023 TASK-009, review round 2).
	newSharedPool := func(provider workspace.Resolver, secretClient secrets.SecretClient) *workspace.ServicePool {
		return workspace.NewServicePool(ctx, provider,
			sharedModePoolConfig(deliveryCfg, log, secretCipher, secretEnvelopeMode, secretClient, codeRepoBase, stepEntryPolicy))
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
		pool := newSharedPool(provider, dbSecretClient)
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
		pool := newSharedPool(provider, secretClient)
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
