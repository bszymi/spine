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
	"github.com/bszymi/spine/core/refadmit"
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

// buildWorkflowService constructs the workflow-definition service and wires
// the same branch-protection policy buildArtifactService installs (ADR-009
// §3; INIT-023 TASK-014, audit F7): the projection-backed policy with a
// Store, the permissive one without (early bootstrap). With it, a
// non-bypass workflow write to a protected main is refused at the service
// even if a handler forgets the reviewer dispatch, and an ADR-008 bypass
// needs an operator-or-above actor in the context.
//
// The policy-builder error is returned, never swallowed: a nil policy is
// workflow.Service's "no protection" mode (I4), which only embedders that
// write through actor-less bypass contexts (SMP) may run in.
func buildWorkflowService(gitClient git.GitClient, repoPath string, st store.Store) (*workflow.Service, error) {
	svc := workflow.NewService(gitClient, repoPath)
	policy, err := buildBranchProtectPolicy(st)
	if err != nil {
		return nil, fmt.Errorf("workflow service branch-protect policy: %w", err)
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
// push to fetch the target workspace's branch-protection policy, event
// emitter, and the run reader plus step-definition lookup the ref-class
// admission uses for run, plan and divergence branches (INIT-023
// TASK-016). In shared mode the pool yields the workspace's own store,
// event router and orchestrator (different database and event stream
// per workspace, so all of them MUST be per-request). In single mode
// (pool == nil) we fall back to the process-level store, event router
// and step-definition lookup — artifact-path and push-path writes
// evaluate, admit and audit against the same services there.
//
// Runs is nil exactly when there is no store (a shared-mode workspace
// with no database yet, or a single-mode process without one), and
// StepDefs is nil when no orchestrator is wired. Either way run, plan
// and divergence branches are operator-only for that push: admission
// fails closed, unlike the permissive policy below (Decision 4).
//
// The returned release callback decrements the ServicePool ref so
// workspace pools do not leak. Callers defer it; it is always safe to
// call.
func buildGitPushResolver(pool *workspace.ServicePool, fallbackStore store.Store, fallbackEvents event.EventRouter, fallbackStepDefs refadmit.StepDefLookup) gateway.GitPushResolverFunc {
	return func(ctx context.Context, workspaceID string) (gateway.GitPushResources, func(), error) {
		noop := func() {}
		if pool == nil {
			policy, err := buildBranchProtectPolicy(fallbackStore)
			if err != nil {
				return gateway.GitPushResources{}, noop, fmt.Errorf("git-push fallback branch-protect policy: %w", err)
			}
			return gateway.GitPushResources{
				Policy:   policy,
				Events:   fallbackEvents,
				Runs:     runReaderOrNil(fallbackStore),
				StepDefs: fallbackStepDefs,
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
		return gateway.GitPushResources{
			Policy:   policy,
			Events:   events,
			Runs:     runReaderOrNil(ss.Store),
			StepDefs: stepDefLookupOrNil(ss.StepAssigner),
		}, release, nil
	}
}

// runReaderOrNil returns st as the ref admission's run reader, or a
// true nil interface when st is nil, so the admission sees "no reader"
// (operator-only) rather than a typed nil it would call.
func runReaderOrNil(st store.Store) refadmit.RunReader {
	if st == nil {
		return nil
	}
	return st
}

// stepDefLookupOrNil returns the workspace's step assigner (the
// per-workspace *engine.Orchestrator, whose LookupStepDef loads the
// run's pinned workflow version) as the review-freeze hook, or a true
// nil interface when none is wired or it is a typed nil.
func stepDefLookupOrNil(v any) refadmit.StepDefLookup {
	switch l := v.(type) {
	case nil:
		return nil
	case *engine.Orchestrator:
		if l == nil {
			return nil
		}
		return l
	case refadmit.StepDefLookup:
		return l
	}
	return nil
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
	stepEntryPolicy engine.StepEntryPolicy,
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
	// SHA-bound governed merges (INIT-023 TASK-005): `spine serve` always
	// merges exactly the commit that passed validation and review, with
	// the default moved-head policy (reject). There is deliberately no
	// switch to turn it off (owner question 6); the rollback is the
	// previous build.
	if err := orch.EnableMergeBinding(engine.MergeBindingConfig{}); err != nil {
		log.Error("merge binding init failed", "error", err)
		return nil
	}
	// Engine step-entry policy (INIT-023 TASK-007): strict by default in
	// `spine serve`, zero value under SPINE_STEP_ENTRY_POLICY=legacy.
	orch.WithStepEntryPolicy(stepEntryPolicy)

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

// defaultCommitMaxRetries is the automatic retry budget of a committing
// run's governed merge in `spine serve` (INIT-023 TASK-012, owner question
// 1: the value standalone already configured, now enforced). With the
// 60 s commit tick, about four minutes of transient failure.
const defaultCommitMaxRetries = 3

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
					// The only engine path that re-routes a completed
					// execution (INIT-023 TASK-008): it re-checks the run
					// is active, at this step and without a successor.
					return orch.RecoverCompletedStep(ctx, execID)
				}
				return orch.RetryStep(ctx, exec)
			}),
			scheduler.WithRunFail(func(ctx context.Context, runID, reason string) error {
				return orch.FailRun(ctx, runID, reason)
			}),
			// The run-timeout cancel writes the run row directly; this
			// hook closes the run's live executions afterwards (INIT-023
			// TASK-007). A no-op unless the step-entry policy sets
			// TerminateStepsOnCancel.
			scheduler.WithRunTerminated(orch.TerminateLiveSteps),
			// The retry budget is enforced (INIT-023 TASK-012, P3-27): after
			// defaultCommitMaxRetries automatic retries of a transiently
			// failing merge the run is handed to AbandonExhaustedMerge,
			// which routes it like a permanent merge failure.
			scheduler.WithCommitRetry(func(ctx context.Context, runID string) error {
				return orch.MergeRunBranch(ctx, runID)
			}, defaultCommitMaxRetries, 2*time.Minute),
			scheduler.WithCommitExhausted(orch.AbandonExhaustedMerge),
		)
	}
	return scheduler.New(st, events, opts...)
}

// buildGitHTTPHandler assembles the git HTTP handler. Returns nil when
// no workspace resolver is available or when handler construction fails.
//
// runs and stepDefs are the handler's fallback pair for the ref-class
// admission of run, plan and divergence branches (INIT-023 TASK-016);
// the gateway's per-push resolver attaches the target workspace's pair
// per request, which wins.
//
// perWorkspace is true when that pair is resolved per workspace on each
// push (shared mode): the fallback pair is then never consulted and is
// nil by design, so its absence is not reported (it would warn on every
// shared-mode start); a workspace without a store or engine is refused
// operator-only at push time, with a Warn log per refused ref. In single
// mode the resolver attaches exactly this pair, so the warning below
// describes what pushes will actually get.
func buildGitHTTPHandler(wsResolver workspace.Resolver, trustedCIDRs []string, receivePackEnabled bool, policy branchprotect.Policy, runs refadmit.RunReader, stepDefs refadmit.StepDefLookup, perWorkspace bool, log *slog.Logger) *githttp.Handler {
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
		RunRefs:            runs,
		StepDefs:           stepDefs,
	})
	if err != nil {
		log.Warn("git HTTP endpoint disabled", "reason", err.Error())
		return nil
	}
	if len(trustedCIDRs) == 0 {
		// Recommend the runners' own addresses, never a subnet: a
		// range that contains a Docker bridge gateway or a reverse
		// proxy hands token-less reads to every client it relays — the
		// compose /16 default INIT-023 TASK-015 (P3-30) removed.
		log.Warn("git HTTP endpoint enabled with no trusted CIDRs; all clients must present a bearer token. " +
			"To let runners clone token-less, set SPINE_GIT_HTTP_TRUSTED_CIDRS to the runners' own addresses only " +
			"(never a range containing a Docker bridge gateway or a reverse proxy; see docker-compose.override.yaml.example).")
	} else {
		log.Info("git HTTP endpoint enabled", "trusted_cidrs", trustedCIDRs)
	}
	if receivePackEnabled {
		if policy == nil {
			// A nil policy is valid (early-bootstrap, no Store), but
			// it means pushes are not gated by branch protection —
			// surface that loudly.
			log.Warn("git HTTP receive-pack is ENABLED with no branch-protection policy; branch pushes will land unchecked (ref-class admission still applies)")
		} else {
			log.Info("git HTTP receive-pack is ENABLED with branch-protection pre-receive enforcement (EPIC-004)")
		}
		switch {
		case perWorkspace:
			log.Info("run-branch push admission resolves each workspace's run store and engine per push; a workspace without them is operator-only")
		case runs == nil:
			log.Warn("run-branch pushes are operator-only: no run store wired")
		case stepDefs == nil:
			log.Warn("run-branch pushes are operator-only: no step-definition lookup wired for the review freeze")
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
func buildServeWorkspaces(ctx context.Context, runtimeEnv string, log *slog.Logger, deliveryCfg workspaceDeliveryConfig, codeRepoBase string, stepEntryPolicy engine.StepEntryPolicy) (*serveWorkspaces, error) {
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

	wiring, err := buildWorkspaceResolver(ctx, secretCipher, secretEnvelopeMode, log, deliveryCfg, codeRepoBase, stepEntryPolicy)
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

// buildPrimaryGitClient builds the single-workspace primary client of
// `spine serve`. It is standalone's shared working tree (INIT-023
// TASK-012): its home branch is the governed-merge target,
// engine.AuthoritativeBranch ("main"), which every sequence on the tree
// restores and every "HEAD" read means (workspace.PrimaryClientOptions;
// gitOpts is not modified, it still feeds code-repository clients).
func buildPrimaryGitClient(repoPath string, gitOpts []git.CLIOption) *git.CLIClient {
	return git.NewCLIClient(repoPath, workspace.PrimaryClientOptions(gitOpts)...)
}
