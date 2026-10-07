package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/bszymi/spine/adapters/delivery"
	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/adapters/scheduler"
	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/actor"
	"github.com/bszymi/spine/core/artifact"
	"github.com/bszymi/spine/core/branchprotect"
	bpprojection "github.com/bszymi/spine/core/branchprotect/projection"
	spinecrypto "github.com/bszymi/spine/core/crypto"
	"github.com/bszymi/spine/core/divergence"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/engine"
	"github.com/bszymi/spine/core/event"
	"github.com/bszymi/spine/core/evidence"
	"github.com/bszymi/spine/core/queue"
	"github.com/bszymi/spine/core/validation"
	"github.com/bszymi/spine/core/workflow"
	"github.com/bszymi/spine/service/internal/config"
	"github.com/bszymi/spine/service/internal/gateway"
	"github.com/bszymi/spine/service/internal/githttp"
	"github.com/bszymi/spine/service/internal/workspace"
)

// buildArtifactService constructs the artifact service with the
// configured artifacts directory and wires the branch-protection policy
// (ADR-009 §3). The policy reads from the projection-backed RuleSource
// so evaluation is an in-memory lookup against the runtime table; the
// projection handler keeps the table in sync with the committed config
// on every advance of the authoritative branch.
//
// When no Store is available (e.g. early bootstrap before migrations run)
// the policy is built over an empty static rule set. That still denies
// nothing — there are no rules to match — but the Service remains
// "policy-wired" and any future rule, once the Store is online, flows
// through the same code path.
func buildArtifactService(gitClient git.GitClient, events event.EventRouter, repoPath string, cfg *config.SpineConfig, st store.Store) (*artifact.Service, error) {
	svc := artifact.NewService(gitClient, events, repoPath)
	if cfg != nil {
		svc.WithArtifactsDir(cfg.ArtifactsDir)
	}
	policy, err := buildBranchProtectPolicy(st)
	if err != nil {
		return nil, fmt.Errorf("artifact service branch-protect policy: %w", err)
	}
	svc.WithPolicy(policy)
	return svc, nil
}

// buildBranchProtectPolicy returns a branchprotect.Policy suitable for
// the Artifact Service's guard. When a Store is configured, the policy
// reads the projection-backed rules; otherwise it falls back to a
// rules-less source, which evaluates to "no matching rule, allow" for
// every branch and keeps early-bootstrap paths functional.
//
// Returns an error if the projection adapter rejects its inputs (today,
// only on a nil ListReader — the st != nil guard above shields this in
// production, but the error path lets a missed nil-check surface as a
// startup failure rather than a panic; INIT-022 EPIC-001 TASK-014).
func buildBranchProtectPolicy(st store.Store) (branchprotect.Policy, error) {
	if st == nil {
		return branchprotect.NewPermissive(), nil
	}
	rs, err := bpprojection.New(st)
	if err != nil {
		return nil, fmt.Errorf("branchprotect projection: %w", err)
	}
	return branchprotect.New(rs), nil
}

// buildEvidenceQuerier constructs the gateway-facing
// EvidenceQuerier used by run.status (INIT-014 EPIC-006 TASK-005).
// Returns nil when no primary git client is configured (single-mode
// startup before deps.Store is wired, or platform-binding mode where
// the per-workspace builder owns the wiring); the gateway's
// evidenceQuerierFrom accessor then resolves to nil and run.status
// silently omits the evidence field rather than 503-ing.
//
// The querier always reads from `main` — the same branch the engine
// merge step writes to (internal/engine/merge.go::authoritativeBranch).
// Reader and writer MUST stay in sync; a configurable reader override
// while the merge target is hardcoded would create a silent
// missing-evidence trap. When the engine path becomes configurable
// (future epic), the reader's branch resolution can grow alongside
// it via [evidence.Querier.WithPrimaryBranch].
func buildEvidenceQuerier(primary git.GitClient) gateway.EvidenceQuerier {
	if primary == nil {
		return nil
	}
	return evidence.NewQuerier(primary)
}

// buildWorkspaceCloneURLBuilder returns an engine.CloneURLBuilder that
// constructs runner-facing clone URLs against the workspace's git HTTP
// endpoint per ADR-015 *Assignment payload shape*. The trailing slash
// on `base` is normalized so callers can supply either form.
//
// `workspaceID` must be non-empty: the gateway's git HTTP router
// (handlers_git.go::parseGitPath) parses `/git/{first_segment}/...` as
// `{workspace_id}={first_segment}, {repository_id}=...`. A URL of the
// form `/git/{repository_id}/info/refs` would therefore be resolved as
// workspace=`{repository_id}` and fail with workspace-not-found. When
// workspaceID is blank, this returns nil; callers must guard for nil
// (or skip wiring) so the orchestrator never publishes a malformed
// clone URL into an assignment payload.
func buildWorkspaceCloneURLBuilder(base, workspaceID string) engine.CloneURLBuilder {
	for len(base) > 0 && base[len(base)-1] == '/' {
		base = base[:len(base)-1]
	}
	if workspaceID == "" {
		return nil
	}
	return func(repoID string) (wsID, cloneURL string) {
		return workspaceID, fmt.Sprintf("%s/git/%s/%s", base, workspaceID, repoID)
	}
}

// buildGitPushResolver returns a resolver the gateway calls on each
// push to fetch the target workspace's branch-protection policy and
// event emitter. In shared mode the pool yields the workspace's own
// store and event router (different database and event stream per
// workspace, so both MUST be per-request). In single mode
// (pool == nil) we fall back to the process-level store + event
// router — both artifact-path and push-path writes evaluate and audit
// against the same services there.
//
// The returned release callback decrements the ServicePool ref so
// workspace pools do not leak. Callers defer it; it is always safe to
// call.
func buildGitPushResolver(pool *workspace.ServicePool, fallbackStore store.Store, fallbackEvents event.EventRouter) gateway.GitPushResolverFunc {
	return func(ctx context.Context, workspaceID string) (gateway.GitPushResources, func(), error) {
		noop := func() {}
		if pool == nil {
			policy, err := buildBranchProtectPolicy(fallbackStore)
			if err != nil {
				return gateway.GitPushResources{}, noop, fmt.Errorf("git-push fallback branch-protect policy: %w", err)
			}
			return gateway.GitPushResources{
				Policy: policy,
				Events: fallbackEvents,
			}, noop, nil
		}
		ss, err := pool.Get(ctx, workspaceID)
		if err != nil {
			return gateway.GitPushResources{}, noop, err
		}
		release := func() { pool.Release(workspaceID) }
		var policy branchprotect.Policy
		if ss.Store == nil {
			// Shared-mode workspace with no database URL yet: a
			// permissive policy avoids blocking the push. The event
			// stream is also unavailable in that state; an override
			// on such a workspace emits nothing, which is the right
			// audit behaviour (there is no rule to override, so
			// nothing to record).
			policy = branchprotect.NewPermissive()
		} else {
			policy, err = buildBranchProtectPolicy(ss.Store)
			if err != nil {
				release()
				return gateway.GitPushResources{}, noop, fmt.Errorf("git-push branch-protect policy: %w", err)
			}
		}
		var events event.Emitter
		if ss.Events != nil {
			events = ss.Events
		}
		return gateway.GitPushResources{Policy: policy, Events: events}, release, nil
	}
}

// buildWorkflowResolver builds the projection-backed workflow resolver
// and the provider used by buildOrchestrator. Returns (nil, nil) when
// no store is available.
func buildWorkflowResolver(st store.Store, gitClient git.GitClient) (gateway.WorkflowResolverFn, *workflow.ProjectionWorkflowProvider) {
	if st == nil {
		return nil, nil
	}
	wfProvider := workflow.NewProjectionProviderFromListFn(func(ctx context.Context) ([]workflow.WorkflowProjection, error) {
		projs, err := st.ListActiveWorkflowProjections(ctx)
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
	bindingResolver := engine.NewBindingResolver(wfProvider, gitClient)
	resolver := func(ctx context.Context, artifactType, _ string) (*gateway.ResolvedWorkflow, error) {
		result, err := bindingResolver.ResolveWorkflow(ctx, artifactType, "")
		if err != nil {
			return nil, err
		}
		timeout := ""
		if result.Workflow != nil {
			timeout = result.Workflow.Timeout
		}
		return &gateway.ResolvedWorkflow{
			WorkflowID:   result.Workflow.ID,
			WorkflowPath: result.Workflow.Path,
			EntryStep:    result.Workflow.EntryStep,
			CommitSHA:    result.CommitSHA,
			VersionLabel: result.VersionLabel,
			Timeout:      timeout,
		}, nil
	}
	return resolver, wfProvider
}

// buildOrchestrator constructs the engine orchestrator. Returns nil when
// store or workflow provider are unavailable.
func buildOrchestrator(
	st store.Store,
	wfProvider *workflow.ProjectionWorkflowProvider,
	gitClient *git.CLIClient,
	events event.EventRouter,
	q *queue.MemoryQueue,
	artifactSvc *artifact.Service,
	validator *validation.Engine,
	log *slog.Logger,
) *engine.Orchestrator {
	if st == nil || wfProvider == nil {
		return nil
	}
	actorSvc := actor.NewService(st)
	actorGw := actor.NewGateway(st, events, q, actorSvc)
	wfLoader := engine.NewGitWorkflowLoader(gitClient)
	bindingResolver := engine.NewBindingResolver(wfProvider, gitClient)

	orch, err := engine.New(
		bindingResolver,
		st, actorGw, artifactSvc, events, gitClient, wfLoader,
	)
	if err != nil {
		log.Error("engine orchestrator init failed", "error", err)
		return nil
	}

	orch.WithAssignmentStore(st)
	orch.WithActorSelector(actorSvc)
	if validator != nil {
		orch.WithValidator(validator)
	}
	orch.WithDiscussions(st)
	// Branch-protection guard for MergeRunBranch (ADR-009 §3). Uses the
	// same projection-backed policy the Artifact Service installs, so
	// API-path writes and governed merges share a single decision point.
	bpPolicy, err := buildBranchProtectPolicy(st)
	if err != nil {
		log.Error("branch-protect policy init failed", "error", err)
		return nil
	}
	orch.WithBranchProtectPolicy(bpPolicy)

	divSvc := divergence.NewService(st, gitClient, events)
	divPolicy, err := buildBranchProtectPolicy(st)
	if err != nil {
		log.Error("branch-protect policy init failed", "error", err)
		return nil
	}
	divSvc.WithBranchProtectPolicy(divPolicy)
	orch.WithDivergence(divSvc)
	orch.WithConvergence(divSvc)

	return orch
}

// buildScheduler wires the background scheduler with recovery callbacks.
func buildScheduler(
	st store.Store,
	events event.EventRouter,
	orch *engine.Orchestrator,
	orphanThreshold time.Duration,
) *scheduler.Scheduler {
	opts := []scheduler.Option{}
	if orphanThreshold > 0 {
		opts = append(opts, scheduler.WithOrphanThreshold(orphanThreshold))
	}
	if orch != nil {
		opts = append(opts,
			scheduler.WithStepRecovery(func(ctx context.Context, execID string) error {
				exec, err := st.GetStepExecution(ctx, execID)
				if err != nil {
					return err
				}
				if exec.Status == domain.StepStatusCompleted {
					return orch.SubmitStepResult(ctx, execID, engine.StepResult{OutcomeID: exec.OutcomeID})
				}
				return orch.RetryStep(ctx, exec)
			}),
			scheduler.WithRunFail(func(ctx context.Context, runID, reason string) error {
				return orch.FailRun(ctx, runID, reason)
			}),
			scheduler.WithCommitRetry(func(ctx context.Context, runID string) error {
				return orch.MergeRunBranch(ctx, runID)
			}, 3, 2*time.Minute),
		)
	}
	return scheduler.New(st, events, opts...)
}

// buildGitHTTPHandler assembles the git HTTP handler. Returns nil when
// no workspace resolver is available or when handler construction fails.
func buildGitHTTPHandler(wsResolver workspace.Resolver, trustedCIDRs []string, receivePackEnabled bool, policy branchprotect.Policy, log *slog.Logger) *githttp.Handler {
	if wsResolver == nil {
		return nil
	}
	h, err := githttp.NewHandler(githttp.Config{
		ResolveRepoPath: func(ctx context.Context, workspaceID string) (string, error) {
			cfg, err := wsResolver.Resolve(ctx, workspaceID)
			if err != nil {
				return "", err
			}
			return cfg.RepoPath, nil
		},
		TrustedCIDRs:       trustedCIDRs,
		ReceivePackEnabled: receivePackEnabled,
		Policy:             policy,
	})
	if err != nil {
		log.Warn("git HTTP endpoint disabled", "reason", err.Error())
		return nil
	}
	if len(trustedCIDRs) == 0 {
		log.Warn("git HTTP endpoint enabled with no trusted CIDRs; all clients must present a bearer token. Set SPINE_GIT_HTTP_TRUSTED_CIDRS to a narrow runner subnet to opt in to token-less access.")
	} else {
		log.Info("git HTTP endpoint enabled", "trusted_cidrs", trustedCIDRs)
	}
	if receivePackEnabled {
		if policy == nil {
			// A nil policy is valid (early-bootstrap, no Store), but
			// it means pushes are not gated by branch protection —
			// surface that loudly.
			log.Warn("git HTTP receive-pack is ENABLED with no branch-protection policy; pushes will land unchecked")
		} else {
			log.Info("git HTTP receive-pack is ENABLED with branch-protection pre-receive enforcement (EPIC-004)")
		}
	}
	return h
}

// startEventDelivery bootstraps the event delivery subscriber and
// webhook dispatcher. Returns the cancel func for the delivery context
// and the subscriber so its broadcaster can be wired into the gateway.
func startEventDelivery(ctx context.Context, deps serveDeps, log *slog.Logger) (context.CancelFunc, *delivery.DeliverySubscriber) {
	deliveryCtx, cancel := context.WithCancel(ctx)

	if deps.SMPEventURL != "" {
		if err := delivery.BootstrapInternalSubscription(deliveryCtx, deps.Store, delivery.BootstrapConfig{
			EventURL:    deps.SMPEventURL,
			WorkspaceID: deps.SMPWorkspaceID,
			Token:       deps.SMPInternalToken,
		}); err != nil {
			log.Error("failed to bootstrap internal subscription", "error", err)
		}
	}

	subLister := delivery.NewStoreSubscriptionLister(deps.Store)
	subscriber := delivery.NewDeliverySubscriber(deps.Store, subLister)
	if err := subscriber.Subscribe(deliveryCtx, deps.Events); err != nil {
		log.Error("failed to start delivery subscriber", "error", err)
	} else {
		subResolver := delivery.NewStoreSubscriptionResolver(deps.Store)
		dispatcher := delivery.NewWebhookDispatcher(deps.Store, subResolver, delivery.DispatcherConfig{
			Targets: deps.WebhookTargets,
		})
		go dispatcher.Run(deliveryCtx)
		log.Info("event delivery system started")
	}

	// NotifyListener bridges out-of-process event_log writes (today:
	// SMP's spinecore.EventEmitter, INIT-011/EPIC-003/TASK-006 sub-PR
	// 10b) into the in-process EventBroadcaster so connected
	// /events/stream clients see SMP-originated events live, without
	// waiting for the SSE reconnect-replay path to catch up. Opt-in
	// via SPINE_EVENT_LOG_NOTIFY_BRIDGE so operators can roll the
	// listener in independently of the SMP-side notify (sub-PR 10b),
	// and back it out without a redeploy if it misbehaves. When
	// disabled the seam doc's accepted reconnect-replay semantics
	// continue to apply.
	if deps.NotifyBridgeEnabled {
		maybeStartNotifyListener(deliveryCtx, deps.Store, subscriber.Broadcaster, log)
	}

	go delivery.StartRetentionCleanup(deliveryCtx, deps.Store, deps.EventRetention)

	return cancel, subscriber
}

// maybeStartNotifyListener launches the LISTEN goroutine when the
// store exposes a raw *pgxpool.Pool. The per-workspace gated-pool
// path (NewPostgresStoreWithQuerier) returns nil from RawPool, in
// which case the listener is silently skipped — those deployments
// run the listener at the pool level instead (out of scope for this
// PR; the live-broadcast bridge under sub-PR 10a targets the
// single-workspace file mode that ships first).
func maybeStartNotifyListener(ctx context.Context, st store.Store, b *delivery.EventBroadcaster, log *slog.Logger) {
	pgStore, ok := st.(*store.PostgresStore)
	if !ok {
		log.Info("event notify listener: store is not *PostgresStore; skipping")
		return
	}
	pool := pgStore.RawPool()
	if pool == nil {
		log.Info("event notify listener: store has no raw pgxpool; skipping")
		return
	}
	listener := delivery.NewNotifyListener(pool, b)
	go func() {
		if err := listener.Run(ctx); err != nil && !errIsCancellation(err) {
			log.Error("event notify listener exited unexpectedly", "error", err)
		}
	}()
}

// errIsCancellation reports whether the error is the context-cancel
// shape NotifyListener.Run returns at shutdown. Uses errors.Is so a
// wrapped context.Canceled (e.g. `wait for notification: context
// canceled` from inside runSession) still reads as a clean shutdown
// and doesn't fire the "exited unexpectedly" log line.
func errIsCancellation(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// serveWorkspaces is the workspace topology `spine serve` runs on, as
// buildServeWorkspaces assembles it from the environment.
type serveWorkspaces struct {
	// SecretCipher and SecretEnvelopeMode are the subscription-secret key
	// ring and mode every runtime store below is bound and swept with
	// (INIT-023 TASK-009). SecretCipher is nil when
	// SPINE_SECRET_ENCRYPTION_KEY is unset outside production; no store is
	// bound then and the mode has no effect.
	SecretCipher       *spinecrypto.SecretCipher
	SecretEnvelopeMode store.SecretEnvelopeMode

	// Wiring is the workspace resolver and, with WORKSPACE_RESOLVER=db or
	// platform-binding, the ServicePool that builds (binds and sweeps)
	// every workspace's store.
	Wiring *resolverWiring

	// Store is the process-level store: file mode only. It is nil in
	// shared mode and when the database is unreachable at start-up.
	Store store.Store
}

// Close releases the store, the pool and the registry connection, in the
// reverse order of construction.
func (w *serveWorkspaces) Close() {
	if closer, ok := w.Store.(interface{ Close() }); ok && w.Store != nil {
		closer.Close()
	}
	if w.Wiring.Pool != nil {
		w.Wiring.Pool.Close()
	}
	if w.Wiring.DBProvider != nil {
		w.Wiring.DBProvider.Close()
	}
}

// buildServeWorkspaces is the start-up step of `spine serve` that turns the
// environment into the subscription-secret key ring and envelope mode, the
// workspace resolver (with its ServicePool in shared mode) and the
// process-level store (file mode).
//
// It loads the mode itself and hands the same value to both consumers —
// buildWorkspaceResolver, whose pool binds every shared-mode workspace
// store, and buildStore, which binds the file-mode store — so serveCmd
// carries no mode argument a refactor could drop. That matters because ""
// is an untyped constant that converts to SecretEnvelopeMode and means
// migrate: a dropped argument would still compile, the start-up log would
// still print the configured mode, and every store would be bound in
// migrate, converting the legacy rows strict exists to refuse (INIT-023
// TASK-009, review round 2). TestBuildServeWorkspaces_* drive this function
// from the environment for every resolver.
func buildServeWorkspaces(ctx context.Context, runtimeEnv string, log *slog.Logger, deliveryCfg workspaceDeliveryConfig, codeRepoBase string) (*serveWorkspaces, error) {
	secretCipher, err := loadSecretCipher(runtimeEnv)
	if err != nil {
		return nil, err
	}
	secretEnvelopeMode, err := loadSecretEnvelopeMode()
	if err != nil {
		return nil, err
	}
	if secretCipher == nil {
		log.Warn("SPINE_SECRET_ENCRYPTION_KEY is not set — webhook signing secrets will be stored in plaintext", "env", runtimeEnv)
		if secretEnvelopeMode != store.SecretEnvelopeMigrate {
			log.Warn("SPINE_SECRET_ENVELOPE_MODE is ignored without SPINE_SECRET_ENCRYPTION_KEY", "mode", string(secretEnvelopeMode))
		}
	} else {
		ids := secretCipher.KeyIDs()
		log.Info("secret envelope configured",
			"primary_key_id", secretCipher.PrimaryKeyID(),
			"previous_key_ids", ids[1:],
			"mode", string(secretEnvelopeMode),
		)
	}

	wiring, err := buildWorkspaceResolver(ctx, secretCipher, secretEnvelopeMode, log, deliveryCfg, codeRepoBase)
	if err != nil {
		return nil, err
	}
	ws := &serveWorkspaces{
		SecretCipher:       secretCipher,
		SecretEnvelopeMode: secretEnvelopeMode,
		Wiring:             wiring,
	}

	// Only the file resolver is single-workspace and sized to back the
	// process-level store. db / platform-binding modes route
	// per-workspace via ServicePool, so passing their resolver here would
	// resolve only one tenant's credential into a shared store — not the
	// desired model.
	var storeResolver workspace.Resolver
	if wiring.Pool == nil {
		storeResolver = wiring.Resolver
	}
	st, err := buildStore(ctx, storeResolver, secretCipher, secretEnvelopeMode, log)
	if err != nil {
		ws.Close()
		return nil, err
	}
	ws.Store = st
	return ws, nil
}

// buildStore connects to the workspace database in single-workspace
// mode. The runtime DB URL is read through the resolver — which in
// turn goes through SecretClient (ADR-010, TASK-008) — so a deployment
// configured with `SPINE_SECRET_PROVIDER=file|aws` opens its store
// against the value supplied by the secret backend instead of an
// env var. The bootstrap shim in front of the file SecretClient
// preserves legacy `SPINE_DATABASE_URL=…` workflows.
//
// A connection failure is logged but does not abort startup so the
// server still serves store-independent endpoints.
//
// With a cipher, the store's subscription signing secrets are bound to
// the resolver's workspace id (SPINE_WORKSPACE_ID, default "default" —
// the id the gateway serves) in secretEnvelopeMode, and legacy rows are
// swept to enc:v2 before anything reads them (INIT-023 TASK-009;
// startEventDelivery's bootstrap runs later). A sweep error is logged and
// does not abort startup; a binding error does.
func buildStore(ctx context.Context, resolver workspace.Resolver, secretCipher *spinecrypto.SecretCipher, secretEnvelopeMode store.SecretEnvelopeMode, log *slog.Logger) (store.Store, error) {
	var dbURL, workspaceID string
	if resolver != nil {
		cfg, err := resolver.Resolve(ctx, "")
		if err != nil {
			return nil, fmt.Errorf("resolve workspace for store: %w", err)
		}
		dbURL = string(cfg.DatabaseURL.Reveal())
		workspaceID = cfg.ID
	}
	if dbURL == "" {
		return nil, nil
	}
	if err := requireSecureDBURL(dbURL); err != nil {
		return nil, err
	}
	pgStore, err := store.NewPostgresStore(ctx, dbURL)
	if err != nil {
		log.Error("database connection failed, starting without store", "error", err)
		return nil, nil
	}
	pgStore.SetSecretCipher(secretCipher)
	if _, err := workspace.PrepareSubscriptionSecrets(ctx, pgStore, secretCipher, workspaceID, secretEnvelopeMode, log); errors.Is(err, workspace.ErrSubscriptionSecretBinding) {
		pgStore.Close()
		return nil, err
	}
	return pgStore, nil
}
