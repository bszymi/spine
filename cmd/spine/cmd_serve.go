package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/bszymi/spine/internal/actor"
	"github.com/bszymi/spine/internal/artifact"
	"github.com/bszymi/spine/internal/auth"
	"github.com/bszymi/spine/internal/branchprotect"
	bpprojection "github.com/bszymi/spine/internal/branchprotect/projection"
	"github.com/bszymi/spine/internal/config"
	spinecrypto "github.com/bszymi/spine/internal/crypto"
	"github.com/bszymi/spine/internal/delivery"
	"github.com/bszymi/spine/internal/divergence"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/engine"
	"github.com/bszymi/spine/internal/event"
	"github.com/bszymi/spine/internal/gateway"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/githttp"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/projection"
	"github.com/bszymi/spine/internal/queue"
	"github.com/bszymi/spine/internal/scheduler"
	"github.com/bszymi/spine/internal/secrets"
	"github.com/bszymi/spine/internal/store"
	"github.com/bszymi/spine/internal/validation"
	"github.com/bszymi/spine/internal/workflow"
	"github.com/bszymi/spine/internal/workspace"
	"github.com/spf13/cobra"
)

// operatorTokenMinLength is the minimum acceptable length for
// SPINE_OPERATOR_TOKEN. A 32-byte random secret gives ~192 bits of
// entropy, well beyond brute-force reach.
const operatorTokenMinLength = 32

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
	bindingResolver := engine.NewBindingResolver(wfProvider, ss.GitClient)

	actorSvc := actor.NewService(ss.Store)
	actorGw := actor.NewGateway(ss.Store, ss.Events, ss.Queue, actorSvc)
	wfLoader := engine.NewGitWorkflowLoader(ss.GitClient)

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
	orch.WithBranchProtectPolicy(buildBranchProtectPolicy(ss.Store))
	// Workflow writer is required for ADR-008 planning runs. Fail fast at
	// startup if ss.Workflows is populated but doesn't satisfy the
	// interface — a silent skip here degrades workflow.create into 503 at
	// request time, which is much harder to notice in production logs.
	if ss.Workflows != nil {
		wfWriter, ok := ss.Workflows.(engine.WorkflowWriter)
		if !ok {
			return fmt.Errorf("engine orchestrator init: ss.Workflows (%T) does not satisfy engine.WorkflowWriter", ss.Workflows)
		}
		orch.WithWorkflowWriter(wfWriter)
	}

	ss.RunStarter = &runAdapter{orch: orch}
	ss.PlanningRunStarter = &planningRunAdapter{orch: orch}
	ss.WFPlanningStarter = &workflowPlanningRunAdapter{orch: orch}
	ss.RunCanceller = orch
	ss.StepAssigner = orch

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

// parsePositiveIntEnv returns the integer value of the named env var,
// or 0 if the var is unset or not a positive integer.
func parsePositiveIntEnv(name string) int {
	raw := os.Getenv(name)
	if raw == "" {
		return 0
	}
	n := 0
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// parseWebhookAllowedHosts parses the SPINE_WEBHOOK_ALLOWED_HOSTS env
// var into a slice of hostnames the webhook target validator should
// exempt from the "HTTPS + public IP only" default. Entries are
// comma-separated; empty or whitespace-only entries are dropped.
// Operators opt into private destinations explicitly — a bare env var
// never permits arbitrary private ranges.
func parseWebhookAllowedHosts(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseGitHTTPTrustedCIDRs parses the SPINE_GIT_HTTP_TRUSTED_CIDRS
// comma-separated list. An empty input yields nil so deployments must
// opt in explicitly — a prior default of all RFC1918 ranges made any
// container on any Docker bridge "trusted".
func parseGitHTTPTrustedCIDRs(raw string) []string {
	if raw == "" {
		return nil
	}
	var out []string
	for _, c := range strings.Split(raw, ",") {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	return out
}

// parseGitReceivePackEnabled reads the SPINE_GIT_RECEIVE_PACK_ENABLED
// env var. Default false — an explicit true/1/yes/on is required to
// turn push on. Unrecognised values fall back to false so a typo never
// silently enables the endpoint.
func parseGitReceivePackEnabled(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// requireSecureDBURL rejects connection strings that use sslmode=disable
// unless SPINE_INSECURE_LOCAL=1 is set.
func requireSecureDBURL(url string) error {
	if !strings.Contains(url, "sslmode=disable") {
		return nil
	}
	if os.Getenv("SPINE_INSECURE_LOCAL") == "1" {
		return nil
	}
	return fmt.Errorf("database URL uses sslmode=disable; set SPINE_INSECURE_LOCAL=1 to acknowledge (local development only) or use sslmode=require/verify-full")
}

// resolveRuntimeEnv returns the normalized SPINE_ENV value.
func resolveRuntimeEnv() string {
	v := strings.TrimSpace(strings.ToLower(os.Getenv("SPINE_ENV")))
	switch v {
	case "production", "staging", "development":
		return v
	default:
		return ""
	}
}

// devModeEnabled reports whether SPINE_DEV_MODE is set to an
// affirmative value.
func devModeEnabled() bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv("SPINE_DEV_MODE")))
	return v == "1" || v == "true"
}

// guardDevModeEnv enforces TASK-020. Running with dev-mode auth in a
// production environment is refused outright.
func guardDevModeEnv(env string, dev bool) error {
	if !dev {
		return nil
	}
	if env == "production" {
		return fmt.Errorf("SPINE_DEV_MODE is enabled but SPINE_ENV=production; dev-mode auth bypass MUST NOT run in production")
	}
	return nil
}

// validateOperatorToken enforces the startup gate described in TASK-010.
func validateOperatorToken(token string) error {
	if token == "" {
		return nil
	}
	if len(token) < operatorTokenMinLength {
		return fmt.Errorf("SPINE_OPERATOR_TOKEN is %d characters; minimum is %d. Generate a stronger secret (e.g. `openssl rand -hex 32`) before starting spine serve", len(token), operatorTokenMinLength)
	}
	return nil
}

// loadSecretCipher builds the at-rest secret cipher from
// SPINE_SECRET_ENCRYPTION_KEY. In production the key is required.
func loadSecretCipher(env string) (*spinecrypto.SecretCipher, error) {
	encoded := os.Getenv("SPINE_SECRET_ENCRYPTION_KEY")
	if encoded == "" {
		if env == "production" {
			return nil, fmt.Errorf("SPINE_SECRET_ENCRYPTION_KEY is required when SPINE_ENV=production; generate one with `openssl rand -base64 32`")
		}
		return nil, nil
	}
	key, err := spinecrypto.ParseEncryptionKey(encoded)
	if err != nil {
		return nil, fmt.Errorf("SPINE_SECRET_ENCRYPTION_KEY: %w", err)
	}
	return spinecrypto.NewSecretCipher(key)
}

// serveDeps is the full set of pre-built dependencies needed to assemble
// a gateway.ServerConfig. serveCmd reads env and constructs these; the
// serve-startup smoke test builds them in-memory.
type serveDeps struct {
	Store         store.Store
	RepoPath      string
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
	RuntimeEnv                 string
	DevMode                    bool
	TrustedProxyCIDRs          []*net.IPNet
	TrustedGitHTTPCIDRs        []string
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

	artifactSvc := buildArtifactService(deps.GitClient, deps.Events, deps.RepoPath, deps.SpineCfg, deps.Store)
	workflowSvc := workflow.NewService(deps.GitClient, deps.RepoPath)

	var projQuery *projection.QueryService
	var projSync *projection.Service
	if deps.Store != nil {
		projQuery = projection.NewQueryService(deps.Store, deps.GitClient)
		projSync = projection.NewService(deps.GitClient, deps.Store, deps.Events, 30*time.Second)
		projSync.WithArtifactsDir(deps.SpineCfg.ArtifactsDir)
	}

	var validator *validation.Engine
	if deps.Store != nil {
		validator = validation.NewEngine(deps.Store)
	}

	wfResolver, wfProvider := buildWorkflowResolver(deps.Store, deps.GitClient)
	orch := buildOrchestrator(deps.Store, wfProvider, deps.GitClient, deps.Events, deps.Queue, artifactSvc, validator, log)

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
		divSvcForGateway = divergence.NewService(deps.Store, deps.GitClient, deps.Events)
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

	gitHTTPHandler := buildGitHTTPHandler(deps.WSResolver, deps.TrustedGitHTTPCIDRs, deps.GitReceivePackEnabled, buildBranchProtectPolicy(deps.Store), log)
	gitPushResolver := buildGitPushResolver(deps.WSServicePool, deps.Store, deps.Events)

	cfg := gateway.ServerConfig{
		Store:                      deps.Store,
		Auth:                       authSvc,
		Artifacts:                  artifactSvc,
		Workflows:                  workflowSvc,
		ProjQuery:                  projQuery,
		ProjSync:                   projSync,
		Git:                        deps.GitClient,
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
func buildArtifactService(gitClient *git.CLIClient, events event.EventRouter, repoPath string, cfg *config.SpineConfig, st store.Store) *artifact.Service {
	svc := artifact.NewService(gitClient, events, repoPath)
	if cfg != nil {
		svc.WithArtifactsDir(cfg.ArtifactsDir)
	}
	svc.WithPolicy(buildBranchProtectPolicy(st))
	return svc
}

// buildBranchProtectPolicy returns a branchprotect.Policy suitable for
// the Artifact Service's guard. When a Store is configured, the policy
// reads the projection-backed rules; otherwise it falls back to a
// rules-less source, which evaluates to "no matching rule, allow" for
// every branch and keeps early-bootstrap paths functional.
func buildBranchProtectPolicy(st store.Store) branchprotect.Policy {
	if st == nil {
		return branchprotect.NewPermissive()
	}
	return branchprotect.New(bpprojection.New(st))
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
			return gateway.GitPushResources{
				Policy: buildBranchProtectPolicy(fallbackStore),
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
			policy = buildBranchProtectPolicy(ss.Store)
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
func buildWorkflowResolver(st store.Store, gitClient *git.CLIClient) (gateway.WorkflowResolverFn, *workflow.ProjectionWorkflowProvider) {
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
	orch.WithBranchProtectPolicy(buildBranchProtectPolicy(st))

	divSvc := divergence.NewService(st, gitClient, events)
	divSvc.WithBranchProtectPolicy(buildBranchProtectPolicy(st))
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

func serveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Start the Spine runtime server",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			ctx = observe.WithComponent(ctx, "server")
			log := observe.Logger(ctx)

			if err := validateOperatorToken(os.Getenv("SPINE_OPERATOR_TOKEN")); err != nil {
				return err
			}

			runtimeEnv := resolveRuntimeEnv()
			devMode := devModeEnabled()
			if err := guardDevModeEnv(runtimeEnv, devMode); err != nil {
				return err
			}
			if devMode {
				log.Warn("SPINE_DEV_MODE is enabled — unauthenticated requests will be allowed", "env", runtimeEnv)
			}

			secretCipher, err := loadSecretCipher(runtimeEnv)
			if err != nil {
				return err
			}
			if secretCipher == nil {
				log.Warn("SPINE_SECRET_ENCRYPTION_KEY is not set — webhook signing secrets will be stored in plaintext", "env", runtimeEnv)
			}

			port := os.Getenv("SPINE_SERVER_PORT")
			if port == "" {
				port = "8080"
			}

			wsWiring, err := buildWorkspaceResolver(ctx, secretCipher, log)
			if err != nil {
				return err
			}
			if wsWiring.DBProvider != nil {
				defer wsWiring.DBProvider.Close()
			}
			if wsWiring.Pool != nil {
				defer wsWiring.Pool.Close()
			}

			// Only the file resolver is single-workspace and sized to
			// back the process-level store. db / platform-binding
			// modes route per-workspace via ServicePool, so passing
			// their resolver here would resolve only one tenant's
			// credential into a shared store — not the desired model.
			var storeResolver workspace.Resolver
			if wsWiring.Pool == nil {
				storeResolver = wsWiring.Resolver
			}
			st, err := buildStore(ctx, storeResolver, secretCipher, log)
			if err != nil {
				return err
			}
			if closer, ok := st.(interface{ Close() }); ok && st != nil {
				defer closer.Close()
			}

			repoPath := os.Getenv("SPINE_REPO_PATH")
			if repoPath == "" {
				repoPath = "."
			}
			spineCfg, err := config.Load(repoPath)
			if err != nil {
				log.Warn("failed to load .spine.yaml, using defaults", "error", err)
				spineCfg = &config.SpineConfig{ArtifactsDir: "/"}
			}
			log.Info("spine config loaded", "artifacts_dir", spineCfg.ArtifactsDir)

			authWarnings, err := git.LoadPushAuthFromEnv()
			if err != nil {
				return fmt.Errorf("credential helper: %w", err)
			}
			for _, w := range authWarnings {
				log.Warn(w)
			}
			gitOpts := git.PushAuthOpts()
			if smpID := os.Getenv("SMP_WORKSPACE_ID"); smpID != "" {
				gitOpts = append(gitOpts, git.WithPushEnv("SMP_WORKSPACE_ID="+smpID))
			}
			gitClient := git.NewCLIClient(repoPath, gitOpts...)
			if err := gitClient.ConfigureCredentialHelper(ctx); err != nil {
				log.Warn("failed to configure credential helper", "error", err)
			}

			q := queue.NewMemoryQueue(100)
			go q.Start(ctx)
			eventRouter := event.NewQueueRouter(q)

			trustedProxyCIDRs, err := gateway.ParseTrustedProxyCIDRs(os.Getenv("SPINE_TRUSTED_PROXY_CIDRS"))
			if err != nil {
				return fmt.Errorf("SPINE_TRUSTED_PROXY_CIDRS: %w", err)
			}
			if len(trustedProxyCIDRs) > 0 {
				log.Info("rate limiter will honor X-Forwarded-For from trusted proxies", "cidrs", os.Getenv("SPINE_TRUSTED_PROXY_CIDRS"))
			}

			orphanThreshold := time.Duration(0)
			if v := os.Getenv("SPINE_ORPHAN_THRESHOLD"); v != "" {
				if d, parseErr := time.ParseDuration(v); parseErr == nil {
					orphanThreshold = d
				} else {
					log.Error("invalid SPINE_ORPHAN_THRESHOLD, using default", "value", v, "error", parseErr)
				}
			}

			var eventRetention time.Duration
			if v := os.Getenv("SPINE_EVENT_RETENTION"); v != "" {
				if d, err := time.ParseDuration(v); err == nil {
					eventRetention = d
				}
			}

			deps := serveDeps{
				Store:                      st,
				RepoPath:                   repoPath,
				SpineCfg:                   spineCfg,
				GitClient:                  gitClient,
				Queue:                      q,
				Events:                     eventRouter,
				WSResolver:                 wsWiring.Resolver,
				WSDBProvider:               wsWiring.DBProvider,
				WSServicePool:              wsWiring.Pool,
				BindingInvalidationHandler: wsWiring.BindingInvalidationHandler,
				SecretCipher:               secretCipher,
				RuntimeEnv:                 runtimeEnv,
				DevMode:                    devMode,
				TrustedProxyCIDRs:          trustedProxyCIDRs,
				TrustedGitHTTPCIDRs:        parseGitHTTPTrustedCIDRs(os.Getenv("SPINE_GIT_HTTP_TRUSTED_CIDRS")),
				GitReceivePackEnabled:      parseGitReceivePackEnabled(os.Getenv("SPINE_GIT_RECEIVE_PACK_ENABLED")),
				EventDeliveryOn:            os.Getenv("SPINE_EVENT_DELIVERY") == "true",
				SMPEventURL:                os.Getenv("SMP_EVENT_URL"),
				SMPWorkspaceID:             os.Getenv("SMP_WORKSPACE_ID"),
				SMPInternalToken:           os.Getenv("SMP_INTERNAL_TOKEN"),
				EventRetention:             eventRetention,
				SSEMaxConn:                 parsePositiveIntEnv("SPINE_SSE_MAX_CONN_PER_ACTOR"),
				OrphanThreshold:            orphanThreshold,
				WebhookTargets:             delivery.NewTargetValidator(parseWebhookAllowedHosts(os.Getenv("SPINE_WEBHOOK_ALLOWED_HOSTS"))),
			}

			rt, err := buildServerConfig(ctx, deps)
			if err != nil {
				return err
			}

			srv := gateway.NewServer(":"+port, rt.Config)

			if rt.Scheduler != nil {
				if result, err := rt.Scheduler.RecoverOnStartup(ctx); err != nil {
					log.Error("startup recovery failed", "error", err)
				} else {
					log.Info("startup recovery complete",
						"pending_activated", result.PendingActivated,
						"active_resumed", result.ActiveResumed,
					)
				}
				go rt.Scheduler.Start(ctx)
			}
			if rt.ProjSync != nil {
				go rt.ProjSync.StartSyncLoop(ctx)
			}

			listenErr := make(chan error, 1)
			go func() {
				log.Info("spine server starting", "port", port)
				if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
					listenErr <- err
				}
			}()

			sig := make(chan os.Signal, 1)
			signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

			select {
			case err := <-listenErr:
				return fmt.Errorf("server failed to start: %w", err)
			case <-sig:
			}

			log.Info("spine server shutting down")
			if rt.DeliveryCancel != nil {
				rt.DeliveryCancel()
			}
			if rt.Scheduler != nil {
				rt.Scheduler.Stop()
			}
			shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			return srv.Shutdown(shutdownCtx)
		},
	}
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
}

// buildWorkspaceResolver initializes the workspace resolver based on
// the WORKSPACE_RESOLVER env var (file | db | platform-binding).
//
// SPINE_WORKSPACE_MODE was retired in ADR-011; if it is set without
// WORKSPACE_RESOLVER, startup fails fast so a deployment that missed
// the migration cannot silently fall back.
func buildWorkspaceResolver(
	ctx context.Context,
	secretCipher *spinecrypto.SecretCipher,
	log *slog.Logger,
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
		return &resolverWiring{Resolver: workspace.NewFileProvider(fileSecretClient)}, nil

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
			Builder:      workspaceOrchestratorBuilder,
			SecretCipher: secretCipher,
			DBPolicy:     dbPolicyFromEnv(),
			IdleTimeout:  poolIdleTimeoutFromEnv(),
		})
		log.Info("workspace resolver: db", "registry_url", "***")
		return &resolverWiring{Resolver: provider, DBProvider: provider, Pool: pool}, nil

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
			Builder:      workspaceOrchestratorBuilder,
			SecretCipher: secretCipher,
			DBPolicy:     dbPolicyFromEnv(),
			IdleTimeout:  poolIdleTimeoutFromEnv(),
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
		}, nil

	default:
		return nil, fmt.Errorf("unknown WORKSPACE_RESOLVER=%q (expected file|db|platform-binding)", resolver)
	}
}

// dbPolicyFromEnv reads ADR-012 pool overrides from environment
// variables. Unset fields fall back to PoolPolicyDefault().
//
//   - SPINE_WS_POOL_MIN_CONNS, SPINE_WS_POOL_MAX_CONNS
//   - SPINE_WS_POOL_ACQUIRE_TIMEOUT (Go duration; e.g. "5s")
//   - SPINE_WS_POOL_HEALTH_CHECK_PERIOD
//   - SPINE_WS_POOL_QUEUE_SIZE
//
// Per-workspace overrides via binding metadata are out of scope
// here; ADR-012 leaves that for a future binding field.
func dbPolicyFromEnv() workspace.PoolPolicy {
	var p workspace.PoolPolicy
	if v := parsePositiveIntEnv("SPINE_WS_POOL_MIN_CONNS"); v > 0 && v <= math.MaxInt32 {
		p.MinConns = int32(v)
	}
	if v := parsePositiveIntEnv("SPINE_WS_POOL_MAX_CONNS"); v > 0 && v <= math.MaxInt32 {
		p.MaxConns = int32(v)
	}
	if raw := os.Getenv("SPINE_WS_POOL_ACQUIRE_TIMEOUT"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			p.AcquireTimeout = d
		}
	}
	if raw := os.Getenv("SPINE_WS_POOL_HEALTH_CHECK_PERIOD"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			p.HealthCheckPeriod = d
		}
	}
	if v := parsePositiveIntEnv("SPINE_WS_POOL_QUEUE_SIZE"); v > 0 {
		p.QueueSize = v
	}
	return p
}

// secretClientConfigured reports whether the env signals any secret
// backend selection. It treats both the explicit
// SPINE_SECRET_PROVIDER selector and the file-mode SPINE_SECRET_FILE_ROOT
// (which buildSecretClient honours when SPINE_SECRET_PROVIDER is
// unset) as "configured". Used by db-mode and migrate paths to
// decide whether a SecretClient should be wired into DBProvider for
// dereferencing ref-shaped database_url rows.
func secretClientConfigured() bool {
	if strings.TrimSpace(os.Getenv("SPINE_SECRET_PROVIDER")) != "" {
		return true
	}
	if os.Getenv("SPINE_SECRET_FILE_ROOT") != "" {
		return true
	}
	return false
}

// buildFileResolverSecretClient assembles the SecretClient used by
// the single-workspace FileProvider. The shape depends on the
// configured backend:
//
//   - No backend / file backend: a real (or NotFound) client wrapped
//     by EnvFallbackSecretClient so SPINE_DATABASE_URL keeps working
//     for the canonical default/runtime_db ref. Dev-friendly bootstrap.
//   - AWS backend: the AWS client is returned directly, with no env
//     fallback. Production deployments that explicitly opted into AWS
//     must fail closed when a runtime_db secret is missing — falling
//     back to a stale SPINE_DATABASE_URL would mask a misprovisioned
//     secret.
func buildFileResolverSecretClient(ctx context.Context) (secrets.SecretClient, error) {
	id := os.Getenv("SPINE_WORKSPACE_ID")
	if id == "" {
		id = "default"
	}

	provider := strings.ToLower(strings.TrimSpace(os.Getenv("SPINE_SECRET_PROVIDER")))

	if provider == "aws" {
		// Production: no env-var bypass. A missing AWS secret must
		// surface as ErrSecretNotFound, not silently resolve to
		// whatever is in SPINE_DATABASE_URL.
		return buildSecretClient(ctx)
	}

	var inner secrets.SecretClient
	if !secretClientConfigured() {
		inner = workspace.NotFoundSecretClient{}
	} else {
		c, err := buildSecretClient(ctx)
		if err != nil {
			return nil, err
		}
		inner = c
	}

	return workspace.NewEnvFallbackSecretClient(inner, id, "SPINE_DATABASE_URL")
}

// poolIdleTimeoutFromEnv reads SPINE_WS_POOL_IDLE_TIMEOUT (Go duration,
// e.g. "10m"). Zero means "use the ServicePool default" (10m, ADR-012).
// Bad values are silently ignored so a typo doesn't fail startup; the
// default keeps the runtime correct.
func poolIdleTimeoutFromEnv() time.Duration {
	raw := os.Getenv("SPINE_WS_POOL_IDLE_TIMEOUT")
	if raw == "" {
		return 0
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0
	}
	return d
}

// buildSecretClient constructs a SecretClient based on
// SPINE_SECRET_PROVIDER. Required for WORKSPACE_RESOLVER=platform-binding.
//
// Supported values:
//   - file: dev/test path, reads from SPINE_SECRET_FILE_ROOT.
//   - aws : production path, reads from SPINE_SECRET_AWS_REGION,
//     SPINE_SECRET_AWS_ACCOUNT, SPINE_SECRET_AWS_ENV.
func buildSecretClient(ctx context.Context) (secrets.SecretClient, error) {
	provider := strings.ToLower(strings.TrimSpace(os.Getenv("SPINE_SECRET_PROVIDER")))
	switch provider {
	case "", "file":
		root := os.Getenv("SPINE_SECRET_FILE_ROOT")
		if root == "" {
			return nil, fmt.Errorf("SPINE_SECRET_FILE_ROOT is required for SPINE_SECRET_PROVIDER=file")
		}
		return secrets.NewFileClient(secrets.FileConfig{Root: root})

	case "aws":
		region := os.Getenv("SPINE_SECRET_AWS_REGION")
		account := os.Getenv("SPINE_SECRET_AWS_ACCOUNT")
		env := os.Getenv("SPINE_SECRET_AWS_ENV")
		if region == "" || account == "" || env == "" {
			return nil, fmt.Errorf("SPINE_SECRET_AWS_REGION, SPINE_SECRET_AWS_ACCOUNT, SPINE_SECRET_AWS_ENV are required for SPINE_SECRET_PROVIDER=aws")
		}
		return secrets.NewAWSClient(ctx, secrets.AWSConfig{Region: region, Account: account, Env: env})

	default:
		return nil, fmt.Errorf("unknown SPINE_SECRET_PROVIDER=%q (expected file|aws)", provider)
	}
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
