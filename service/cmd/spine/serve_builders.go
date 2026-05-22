package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/bszymi/spine/core/actor"
	"github.com/bszymi/spine/core/artifact"
	"github.com/bszymi/spine/core/branchprotect"
	bpprojection "github.com/bszymi/spine/core/branchprotect/projection"
	"github.com/bszymi/spine/service/internal/config"
	spinecrypto "github.com/bszymi/spine/core/crypto"
	"github.com/bszymi/spine/adapters/delivery"
	"github.com/bszymi/spine/core/divergence"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/engine"
	"github.com/bszymi/spine/core/event"
	"github.com/bszymi/spine/core/evidence"
	"github.com/bszymi/spine/service/internal/gateway"
	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/service/internal/githttp"
	"github.com/bszymi/spine/core/queue"
	"github.com/bszymi/spine/adapters/scheduler"
	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/validation"
	"github.com/bszymi/spine/core/workflow"
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
	return func(repoID string) string {
		return fmt.Sprintf("%s/git/%s/%s", base, workspaceID, repoID)
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

	go delivery.StartRetentionCleanup(deliveryCtx, deps.Store, deps.EventRetention)

	return cancel, subscriber
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
func buildStore(ctx context.Context, resolver workspace.Resolver, secretCipher *spinecrypto.SecretCipher, log *slog.Logger) (store.Store, error) {
	var dbURL string
	if resolver != nil {
		cfg, err := resolver.Resolve(ctx, "")
		if err != nil {
			return nil, fmt.Errorf("resolve workspace for store: %w", err)
		}
		dbURL = string(cfg.DatabaseURL.Reveal())
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
	return pgStore, nil
}
