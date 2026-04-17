package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/bszymi/spine/internal/actor"
	"github.com/bszymi/spine/internal/artifact"
	"github.com/bszymi/spine/internal/auth"
	"github.com/bszymi/spine/internal/cli"
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
	"github.com/bszymi/spine/internal/store"
	"github.com/bszymi/spine/internal/validation"
	"github.com/bszymi/spine/internal/workflow"
	"github.com/bszymi/spine/internal/workspace"
	"github.com/spf13/cobra"
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
	}, nil
}

// workspaceOrchestratorBuilder constructs per-workspace engine orchestrator,
// run starters, and scheduler callbacks from the ServiceSet's basic services.
func workspaceOrchestratorBuilder(ctx context.Context, ss *workspace.ServiceSet) error {
	if ss.Store == nil {
		return nil
	}

	// Workflow provider from projection store.
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

	// Wire run starters and canceller.
	ss.RunStarter = &runAdapter{orch: orch}
	ss.PlanningRunStarter = &planningRunAdapter{orch: orch}
	ss.RunCanceller = orch

	// Wire scheduler callbacks.
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

func main() {
	// Initialize observability before anything else
	logLevel := os.Getenv("SPINE_LOG_LEVEL")
	if logLevel == "" {
		logLevel = "info"
	}
	logFormat := os.Getenv("SPINE_LOG_FORMAT")
	if logFormat == "" {
		logFormat = "json"
	}
	observe.SetupLogger(logLevel, logFormat)

	root := &cobra.Command{
		Use:   "spine",
		Short: "Spine — Git-native Product-to-Execution System",
	}

	root.PersistentFlags().StringVarP(&outputFormat, "output", "o", "json", "Output format: json or table")

	// Workspace flag — global, defaults to SPINE_WORKSPACE_ID env var.
	globalWorkspaceID = os.Getenv("SPINE_WORKSPACE_ID")
	root.PersistentFlags().StringVar(&globalWorkspaceID, "workspace", globalWorkspaceID, "Workspace ID (overrides SPINE_WORKSPACE_ID)")

	root.AddCommand(serveCmd())
	root.AddCommand(healthCmd())
	root.AddCommand(migrateCmd())
	root.AddCommand(initRepoCmd())
	root.AddCommand(artifactCmd())
	root.AddCommand(runCmd())
	root.AddCommand(taskCmd())
	root.AddCommand(queryCmd())
	root.AddCommand(workflowCmd())
	root.AddCommand(validateCmd())
	root.AddCommand(discussionCmd())
	root.AddCommand(workspaceCmd())

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

// parsePositiveIntEnv returns the integer value of the named env var,
// or 0 if the var is unset or not a positive integer. Used to let
// ServerConfig fields fall back to their internal defaults.
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

// parseGitHTTPTrustedCIDRs parses the SPINE_GIT_HTTP_TRUSTED_CIDRS
// comma-separated list. Returns an empty slice when the input is empty —
// callers must then require bearer-token auth for every git request.
// Previously this helper fell back to all RFC1918 ranges, which made any
// container on any Docker bridge "trusted"; that default has been
// removed deliberately so deployments must opt in explicitly.
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

// requireSecureDBURL rejects connection strings that use sslmode=disable
// unless SPINE_INSECURE_LOCAL=1 is set. This prevents production
// deployments from silently transmitting credentials and data in
// plaintext. Local Docker development can opt in via the env var.
func requireSecureDBURL(url string) error {
	if !strings.Contains(url, "sslmode=disable") {
		return nil
	}
	if os.Getenv("SPINE_INSECURE_LOCAL") == "1" {
		return nil
	}
	return fmt.Errorf("database URL uses sslmode=disable; set SPINE_INSECURE_LOCAL=1 to acknowledge (local development only) or use sslmode=require/verify-full")
}

// operatorTokenMinLength is the minimum acceptable length for
// SPINE_OPERATOR_TOKEN. A 32-byte random secret gives ~192 bits of
// entropy, well beyond brute-force reach even if per-IP rate limiting
// is circumvented by distributed sources.
const operatorTokenMinLength = 32

// resolveRuntimeEnv returns the normalized SPINE_ENV value. Accepted
// values are "production", "staging", and "development". Any other
// input — including the empty string — is treated as "unspecified" so
// operators can choose to leave it unset during local development.
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
// affirmative value. Only "1" / "true" (case-insensitive) enable dev
// mode so a stray non-empty env value doesn't accidentally bypass auth.
func devModeEnabled() bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv("SPINE_DEV_MODE")))
	return v == "1" || v == "true"
}

// guardDevModeEnv enforces TASK-020. Running with dev-mode auth in a
// production environment is refused outright. For any other env (or
// none at all) the caller is expected to emit a loud warning log;
// this helper returns nil so startup can proceed.
func guardDevModeEnv(env string, dev bool) error {
	if !dev {
		return nil
	}
	if env == "production" {
		return fmt.Errorf("SPINE_DEV_MODE is enabled but SPINE_ENV=production; dev-mode auth bypass MUST NOT run in production")
	}
	return nil
}

// validateOperatorToken enforces the startup gate described in
// TASK-010. If the env var is set, its length must meet the minimum;
// unset tokens are allowed (operator-scoped routes then return 503 at
// request time, which is preferable to refusing to start for deployments
// that don't use the operator surface).
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
// SPINE_SECRET_ENCRYPTION_KEY (TASK-007). In production environments
// the key is required: without it, webhook signing secrets would be
// written to the database in plaintext and a DB compromise would
// hand the attacker the ability to forge webhooks. For non-production
// environments an unset key is allowed so local development keeps
// working without extra setup; the caller is expected to log a clear
// warning in that case.
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

			// Initialize workspace resolver.
			// SPINE_WORKSPACE_MODE: "single" (default) or "shared".
			var wsResolver workspace.Resolver
			var wsDBProvider *workspace.DBProvider
			var wsServicePool *workspace.ServicePool
			wsMode := os.Getenv("SPINE_WORKSPACE_MODE")
			if wsMode == "" {
				wsMode = "single"
			}

			switch wsMode {
			case "single":
				wsResolver = workspace.NewFileProvider()
				log.Info("workspace mode: single", "workspace_id", os.Getenv("SPINE_WORKSPACE_ID"))
			case "shared":
				registryURL := os.Getenv("SPINE_REGISTRY_DATABASE_URL")
				if registryURL == "" {
					return fmt.Errorf("SPINE_REGISTRY_DATABASE_URL is required in shared workspace mode")
				}
				if err := requireSecureDBURL(registryURL); err != nil {
					return fmt.Errorf("registry URL: %w", err)
				}
				var err error
				wsDBProvider, err = workspace.NewDBProvider(ctx, registryURL, workspace.DBProviderConfig{})
				if err != nil {
					return fmt.Errorf("connect to workspace registry: %w", err)
				}
				defer wsDBProvider.Close()
				wsResolver = wsDBProvider
				wsServicePool = workspace.NewServicePool(ctx, wsDBProvider, workspace.PoolConfig{
					Builder:      workspaceOrchestratorBuilder,
					SecretCipher: secretCipher,
				})
				defer wsServicePool.Close()
				log.Info("workspace mode: shared", "registry_url", "***")
			default:
				return fmt.Errorf("unknown SPINE_WORKSPACE_MODE: %q (expected \"single\" or \"shared\")", wsMode)
			}

			// Connect to database (optional — server starts without it)
			var st store.Store
			dbURL := os.Getenv("SPINE_DATABASE_URL")
			if dbURL != "" {
				if err := requireSecureDBURL(dbURL); err != nil {
					return err
				}
				pgStore, err := store.NewPostgresStore(ctx, dbURL)
				if err != nil {
					log.Error("database connection failed, starting without store", "error", err)
				} else {
					pgStore.SetSecretCipher(secretCipher)
					st = pgStore
					defer pgStore.Close()
				}
			}

			var authSvc *auth.Service
			if st != nil {
				authSvc = auth.NewService(st)
			}

			// Set up Git client and services
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

			var artifactSvc *artifact.Service
			var projQuery *projection.QueryService
			var projSync *projection.Service

			artifactSvc = artifact.NewService(gitClient, eventRouter, repoPath)
			artifactSvc.WithArtifactsDir(spineCfg.ArtifactsDir)

			// Workflow service (ADR-007): writes workflow YAML files on the
			// authoritative branch and runs the full validation suite before commit.
			workflowSvc := workflow.NewService(gitClient, repoPath)
			if st != nil {
				projQuery = projection.NewQueryService(st, gitClient)
				projSync = projection.NewService(gitClient, st, eventRouter, 30*time.Second)
				projSync.WithArtifactsDir(spineCfg.ArtifactsDir)
			}

			var validator *validation.Engine
			if st != nil {
				validator = validation.NewEngine(st)
			}

			// Set up workflow provider and resolver.
			var wfResolver gateway.WorkflowResolverFn
			var wfProvider *workflow.ProjectionWorkflowProvider
			if st != nil {
				wfProvider = workflow.NewProjectionProviderFromListFn(func(ctx context.Context) ([]workflow.WorkflowProjection, error) {
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
				wfResolver = func(ctx context.Context, artifactType, _ string) (*gateway.ResolvedWorkflow, error) {
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
			}

			// Set up engine orchestrator.
			var orch *engine.Orchestrator
			if st != nil && wfProvider != nil {
				actorSvc := actor.NewService(st)
				actorGw := actor.NewGateway(st, eventRouter, q, actorSvc)
				wfLoader := engine.NewGitWorkflowLoader(gitClient)
				bindingResolver := engine.NewBindingResolver(wfProvider, gitClient)

				var err error
				orch, err = engine.New(
					bindingResolver,
					st, actorGw, artifactSvc, eventRouter, gitClient, wfLoader,
				)
				if err != nil {
					log.Error("engine orchestrator init failed", "error", err)
				} else {
					orch.WithAssignmentStore(st)
					orch.WithActorSelector(actorSvc)
					if validator != nil {
						orch.WithValidator(validator)
					}

					// Wire discussion preconditions.
					orch.WithDiscussions(st)

					// Wire divergence and convergence.
					divSvc := divergence.NewService(st, gitClient, eventRouter)
					orch.WithDivergence(divSvc)
					orch.WithConvergence(divSvc)
				}
			}

			// Set up scheduler.
			var sched *scheduler.Scheduler
			if st != nil {
				opts := []scheduler.Option{}
				if v := os.Getenv("SPINE_ORPHAN_THRESHOLD"); v != "" {
					d, err := time.ParseDuration(v)
					if err != nil {
						log.Error("invalid SPINE_ORPHAN_THRESHOLD, using default", "value", v, "error", err)
					} else {
						opts = append(opts, scheduler.WithOrphanThreshold(d))
					}
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
				sched = scheduler.New(st, eventRouter, opts...)
			}

			// Set up event delivery system (feature-flagged).
			var deliveryCancel context.CancelFunc
			var deliverySubscriber *delivery.DeliverySubscriber
			if os.Getenv("SPINE_EVENT_DELIVERY") == "true" && st != nil {
				deliveryCtx, dCancel := context.WithCancel(ctx)
				deliveryCancel = dCancel

				// Bootstrap internal SMP subscription from env vars.
				if smpURL := os.Getenv("SMP_EVENT_URL"); smpURL != "" {
					if err := delivery.BootstrapInternalSubscription(deliveryCtx, st, delivery.BootstrapConfig{
						EventURL:    smpURL,
						WorkspaceID: os.Getenv("SMP_WORKSPACE_ID"),
						Token:       os.Getenv("SMP_INTERNAL_TOKEN"),
					}); err != nil {
						log.Error("failed to bootstrap internal subscription", "error", err)
					}
				}

				subLister := delivery.NewStoreSubscriptionLister(st)
				deliverySubscriber = delivery.NewDeliverySubscriber(st, subLister)
				if err := deliverySubscriber.Subscribe(deliveryCtx, eventRouter); err != nil {
					log.Error("failed to start delivery subscriber", "error", err)
				} else {
					subResolver := delivery.NewStoreSubscriptionResolver(st)
					dispatcher := delivery.NewWebhookDispatcher(st, subResolver, delivery.DispatcherConfig{})
					go dispatcher.Run(deliveryCtx)
					log.Info("event delivery system started")
				}

				// Start retention cleanup for expired deliveries.
				var retention time.Duration
				if v := os.Getenv("SPINE_EVENT_RETENTION"); v != "" {
					if d, err := time.ParseDuration(v); err == nil {
						retention = d
					}
				}
				go delivery.StartRetentionCleanup(deliveryCtx, st, retention)
			}

			// Set up gateway with all services.
			var divSvcForGateway gateway.BranchCreator
			if st != nil {
				divSvcForGateway = divergence.NewService(st, gitClient, eventRouter)
			}

			var starter gateway.RunStarter
			var planningStarter gateway.PlanningRunStarter
			var resultHandler gateway.ResultHandler
			if orch != nil {
				orch.WithArtifactWriter(artifactSvc)
				orch.WithBlockingStore(st)
				starter = &runAdapter{orch: orch}
				planningStarter = &planningRunAdapter{orch: orch}
				resultHandler = &resultAdapter{orch: orch}
			}

			var eventBroadcaster *delivery.EventBroadcaster
			if deliverySubscriber != nil {
				eventBroadcaster = deliverySubscriber.Broadcaster
			}

			// Initialize git HTTP handler for serving repos to runner containers.
			var gitHTTPHandler *githttp.Handler
			if wsResolver != nil {
				trustedCIDRs := parseGitHTTPTrustedCIDRs(os.Getenv("SPINE_GIT_HTTP_TRUSTED_CIDRS"))
				var err error
				gitHTTPHandler, err = githttp.NewHandler(githttp.Config{
					ResolveRepoPath: func(ctx context.Context, workspaceID string) (string, error) {
						cfg, err := wsResolver.Resolve(ctx, workspaceID)
						if err != nil {
							return "", err
						}
						return cfg.RepoPath, nil
					},
					TrustedCIDRs: trustedCIDRs,
				})
				if err != nil {
					log.Warn("git HTTP endpoint disabled", "reason", err.Error())
				} else if len(trustedCIDRs) == 0 {
					log.Warn("git HTTP endpoint enabled with no trusted CIDRs; all clients must present a bearer token. Set SPINE_GIT_HTTP_TRUSTED_CIDRS to a narrow runner subnet to opt in to token-less access.")
				} else {
					log.Info("git HTTP endpoint enabled", "trusted_cidrs", trustedCIDRs)
				}
			}

			trustedProxyCIDRs, err := gateway.ParseTrustedProxyCIDRs(os.Getenv("SPINE_TRUSTED_PROXY_CIDRS"))
			if err != nil {
				return fmt.Errorf("SPINE_TRUSTED_PROXY_CIDRS: %w", err)
			}
			if len(trustedProxyCIDRs) > 0 {
				log.Info("rate limiter will honor X-Forwarded-For from trusted proxies", "cidrs", os.Getenv("SPINE_TRUSTED_PROXY_CIDRS"))
			}

			srv := gateway.NewServer(":"+port, gateway.ServerConfig{
				Store:               st,
				Auth:                authSvc,
				Artifacts:           artifactSvc,
				Workflows:           workflowSvc,
				ProjQuery:           projQuery,
				ProjSync:            projSync,
				Git:                 gitClient,
				Validator:           validator,
				WorkflowResolver:    wfResolver,
				BranchCreator:       divSvcForGateway,
				Events:              eventRouter,
				RunStarter:          starter,
				PlanningRunStarter:  planningStarter,
				ResultHandler:       resultHandler,
				WorkspaceResolver:   wsResolver,
				ServicePool:         wsServicePool,
				WSDBProvider:        wsDBProvider,
				RunCanceller:        orch,
				CandidateFinder:     orch,
				StepClaimer:         orch,
				StepReleaser:        orch,
				StepExecutionLister: orch,
				StepAcknowledger:    orch,
				EventBroadcaster:    eventBroadcaster,
				GitHTTP:             gitHTTPHandler,
				SSEMaxConnPerActor:  parsePositiveIntEnv("SPINE_SSE_MAX_CONN_PER_ACTOR"),
				TrustedProxyCIDRs:   trustedProxyCIDRs,
				DevMode:             devMode,
				Env:                 runtimeEnv,
			})

			// Run startup recovery and start background services.
			if sched != nil {
				if result, err := sched.RecoverOnStartup(ctx); err != nil {
					log.Error("startup recovery failed", "error", err)
				} else {
					log.Info("startup recovery complete",
						"pending_activated", result.PendingActivated,
						"active_resumed", result.ActiveResumed,
					)
				}
				go sched.Start(ctx)
			}
			if projSync != nil {
				go projSync.StartSyncLoop(ctx)
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
			if deliveryCancel != nil {
				deliveryCancel()
			}
			if sched != nil {
				sched.Stop()
			}
			shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			return srv.Shutdown(shutdownCtx)
		},
	}
}

func healthCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "health",
		Short: "Check system health",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println(`{"status":"healthy","components":{}}`)
			return nil
		},
	}
}

func migrateCmd() *cobra.Command {
	var allWorkspaces bool

	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Run database migrations",
		RunE: func(cmd *cobra.Command, args []string) error {
			migrationsDir := os.Getenv("SPINE_MIGRATIONS_DIR")
			if migrationsDir == "" {
				migrationsDir = "migrations"
			}

			// In --all-workspaces mode, workspace and registry migrations use
			// separate directories. SPINE_MIGRATIONS_DIR is for workspace schemas;
			// SPINE_REGISTRY_MIGRATIONS_DIR is for the registry schema.
			registryMigrationsDir := os.Getenv("SPINE_REGISTRY_MIGRATIONS_DIR")
			if registryMigrationsDir == "" {
				registryMigrationsDir = "migrations/registry"
			}

			wsMigrationsDir := os.Getenv("SPINE_WORKSPACE_MIGRATIONS_DIR")
			if wsMigrationsDir == "" {
				wsMigrationsDir = "migrations"
			}

			ctx := context.Background()
			ctx = observe.WithComponent(ctx, "migrate")
			log := observe.Logger(ctx)

			if !allWorkspaces {
				// Single database migration (existing behavior).
				dbURL := os.Getenv("SPINE_DATABASE_URL")
				if dbURL == "" {
					return fmt.Errorf("SPINE_DATABASE_URL is required")
				}
				if err := requireSecureDBURL(dbURL); err != nil {
					return err
				}

				s, err := store.NewPostgresStore(ctx, dbURL)
				if err != nil {
					return fmt.Errorf("connect to database: %w", err)
				}
				defer s.Close()

				log.Info("applying migrations", "dir", migrationsDir)
				if err := s.ApplyMigrations(ctx, migrationsDir); err != nil {
					return fmt.Errorf("apply migrations: %w", err)
				}

				log.Info("migrations applied successfully")
				return nil
			}

			// Batch migration: all workspace databases + registry.
			registryURL := os.Getenv("SPINE_REGISTRY_DATABASE_URL")
			if registryURL == "" {
				return fmt.Errorf("SPINE_REGISTRY_DATABASE_URL is required for --all-workspaces")
			}
			if err := requireSecureDBURL(registryURL); err != nil {
				return fmt.Errorf("registry URL: %w", err)
			}

			// 1. Migrate the registry database itself.
			log.Info("migrating registry database")
			registryStore, err := store.NewPostgresStore(ctx, registryURL)
			if err != nil {
				return fmt.Errorf("connect to registry database: %w", err)
			}
			if err := registryStore.ApplyMigrations(ctx, registryMigrationsDir); err != nil {
				registryStore.Close()
				return fmt.Errorf("apply registry migrations: %w", err)
			}
			registryStore.Close()
			log.Info("registry migrations applied")

			// 2. List all active workspaces.
			dbProvider, err := workspace.NewDBProvider(ctx, registryURL, workspace.DBProviderConfig{})
			if err != nil {
				return fmt.Errorf("connect to workspace registry: %w", err)
			}
			defer dbProvider.Close()

			workspaces, err := dbProvider.List(ctx)
			if err != nil {
				return fmt.Errorf("list workspaces: %w", err)
			}

			if len(workspaces) == 0 {
				log.Info("no active workspaces found")
				return nil
			}

			// 3. Migrate each workspace database.
			var failed []string
			for _, ws := range workspaces {
				wsCtx := observe.WithWorkspaceID(ctx, ws.ID)
				wsLog := observe.Logger(wsCtx)

				if ws.DatabaseURL == "" {
					wsLog.Warn("workspace has no database URL, skipping")
					continue
				}

				wsLog.Info("migrating workspace database")
				wsStore, err := store.NewPostgresStore(wsCtx, ws.DatabaseURL)
				if err != nil {
					wsLog.Error("connect to workspace database failed", "error", err)
					failed = append(failed, ws.ID)
					continue
				}

				if err := wsStore.ApplyMigrations(wsCtx, wsMigrationsDir); err != nil {
					wsLog.Error("apply workspace migrations failed", "error", err)
					failed = append(failed, ws.ID)
					wsStore.Close()
					continue
				}

				wsStore.Close()
				wsLog.Info("workspace migrations applied")
			}

			log.Info("batch migration complete",
				"total", len(workspaces),
				"succeeded", len(workspaces)-len(failed),
				"failed", len(failed),
			)

			if len(failed) > 0 {
				return fmt.Errorf("migration failed for %d workspace(s): %v", len(failed), failed)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&allWorkspaces, "all-workspaces", false, "Migrate all workspace databases (shared mode)")
	return cmd
}

func initRepoCmd() *cobra.Command {
	var artifactsDir string
	var noBranch bool

	cmd := &cobra.Command{
		Use:   "init-repo [path]",
		Short: "Initialize a new Spine repository with directory structure and seed documents",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "."
			if len(args) > 0 {
				path = args[0]
			}
			return cli.InitRepo(path, cli.InitOpts{
				ArtifactsDir: artifactsDir,
				NoBranch:     noBranch,
			})
		},
	}
	cmd.Flags().StringVar(&artifactsDir, "artifacts-dir", "spine", "Directory for Spine artifacts (use / for repo root)")
	cmd.Flags().BoolVar(&noBranch, "no-branch", false, "Commit directly to current branch instead of spine/init")
	return cmd
}

func queryCmd() *cobra.Command {
	apiURL := os.Getenv("SPINE_API_URL")
	if apiURL == "" {
		apiURL = "http://localhost:8080"
	}
	token := os.Getenv("SPINE_TOKEN")
	outputFmt := "table"

	cmd := &cobra.Command{
		Use:   "query",
		Short: "Query artifacts, graph, history, and runs",
	}
	cmd.PersistentFlags().StringVarP(&outputFmt, "output", "o", "table", "Output format: table or json")

	// spine query artifacts
	artifactsCmd := &cobra.Command{
		Use:   "artifacts",
		Short: "List artifacts with optional filters",
		RunE: func(cmd *cobra.Command, args []string) error {
			artType, _ := cmd.Flags().GetString("type")
			status, _ := cmd.Flags().GetString("status")
			parent, _ := cmd.Flags().GetString("parent")
			client := cli.NewClient(apiURL, token).WithWorkspace(globalWorkspaceID)
			return cli.QueryArtifacts(cmd.Context(), client, artType, status, parent, cli.OutputFormat(outputFmt))
		},
	}
	artifactsCmd.Flags().String("type", "", "Filter by artifact type")
	artifactsCmd.Flags().String("status", "", "Filter by status")
	artifactsCmd.Flags().String("parent", "", "Filter by parent path")

	// spine query graph
	graphCmd := &cobra.Command{
		Use:   "graph [artifact-path]",
		Short: "Display artifact relationship graph",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := ""
			if len(args) > 0 {
				path = args[0]
			}
			depth, _ := cmd.Flags().GetInt("depth")
			client := cli.NewClient(apiURL, token).WithWorkspace(globalWorkspaceID)
			return cli.QueryGraph(cmd.Context(), client, path, depth, cli.OutputFormat(outputFmt))
		},
	}
	graphCmd.Flags().Int("depth", 0, "Maximum traversal depth")

	// spine query history
	historyCmd := &cobra.Command{
		Use:   "history [artifact-path]",
		Short: "Show change history for an artifact",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := ""
			if len(args) > 0 {
				path = args[0]
			}
			client := cli.NewClient(apiURL, token).WithWorkspace(globalWorkspaceID)
			return cli.QueryHistory(cmd.Context(), client, path, cli.OutputFormat(outputFmt))
		},
	}

	// spine query runs
	runsCmd := &cobra.Command{
		Use:   "runs",
		Short: "List workflow runs with optional filters",
		RunE: func(cmd *cobra.Command, args []string) error {
			task, _ := cmd.Flags().GetString("task")
			status, _ := cmd.Flags().GetString("status")
			client := cli.NewClient(apiURL, token).WithWorkspace(globalWorkspaceID)
			return cli.QueryRuns(cmd.Context(), client, task, status, cli.OutputFormat(outputFmt))
		},
	}
	runsCmd.Flags().String("task", "", "Filter by task path")
	runsCmd.Flags().String("status", "", "Filter by run status")

	cmd.AddCommand(artifactsCmd, graphCmd, historyCmd, runsCmd)
	return cmd
}

func workflowCmd() *cobra.Command {
	repoPath := "."
	outputFmt := "table"

	cmd := &cobra.Command{
		Use:   "workflow",
		Short: "Manage workflow definitions",
	}
	cmd.PersistentFlags().StringVarP(&outputFmt, "output", "o", "table", "Output format: table or json")
	cmd.PersistentFlags().StringVar(&repoPath, "repo", ".", "Repository path")

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List all available workflow definitions",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cli.ListWorkflows(repoPath, cli.OutputFormat(outputFmt))
		},
	}

	showCmd := &cobra.Command{
		Use:   "show [workflow-path]",
		Short: "Display workflow definition details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return cli.ShowWorkflow(repoPath, args[0], cli.OutputFormat(outputFmt))
		},
	}

	resolveCmd := &cobra.Command{
		Use:   "resolve [artifact-path]",
		Short: "Show which workflow would bind to the given artifact",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return cli.ResolveWorkflow(repoPath, args[0], cli.OutputFormat(outputFmt))
		},
	}

	createCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new workflow definition via the API (ADR-007)",
		RunE: func(cmd *cobra.Command, args []string) error {
			id, _ := cmd.Flags().GetString("id")
			file, _ := cmd.Flags().GetString("file")
			if id == "" || file == "" {
				return fmt.Errorf("--id and --file are required")
			}
			body, err := os.ReadFile(file)
			if err != nil {
				return fmt.Errorf("read file: %w", err)
			}
			c := newAPIClient()
			data, err := c.Post(cmd.Context(), "/api/v1/workflows", map[string]string{
				"id":   id,
				"body": string(body),
			})
			if err != nil {
				return err
			}
			return printResponse(data)
		},
	}
	createCmd.Flags().String("id", "", "Workflow identifier (e.g. task-default)")
	createCmd.Flags().String("file", "", "Path to YAML file containing the workflow body")

	updateCmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Update an existing workflow definition via the API",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			file, _ := cmd.Flags().GetString("file")
			if file == "" {
				return fmt.Errorf("--file is required")
			}
			body, err := os.ReadFile(file)
			if err != nil {
				return fmt.Errorf("read file: %w", err)
			}
			c := newAPIClient()
			data, err := c.Put(cmd.Context(), "/api/v1/workflows/"+args[0], map[string]string{
				"body": string(body),
			})
			if err != nil {
				return err
			}
			return printResponse(data)
		},
	}
	updateCmd.Flags().String("file", "", "Path to updated YAML file")

	validateCmd := &cobra.Command{
		Use:   "validate <id>",
		Short: "Validate a candidate workflow body via the API (no persist)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			file, _ := cmd.Flags().GetString("file")
			if file == "" {
				return fmt.Errorf("--file is required")
			}
			body, err := os.ReadFile(file)
			if err != nil {
				return fmt.Errorf("read file: %w", err)
			}
			c := newAPIClient()
			data, err := c.Post(cmd.Context(), "/api/v1/workflows/"+args[0]+"/validate", map[string]string{
				"body": string(body),
			})
			if err != nil {
				return err
			}
			return printResponse(data)
		},
	}
	validateCmd.Flags().String("file", "", "Path to candidate YAML file")

	apiListCmd := &cobra.Command{
		Use:   "api-list",
		Short: "List workflows via the API (instead of reading the working tree)",
		RunE: func(cmd *cobra.Command, args []string) error {
			params := url.Values{}
			if v, _ := cmd.Flags().GetString("applies_to"); v != "" {
				params.Set("applies_to", v)
			}
			if v, _ := cmd.Flags().GetString("status"); v != "" {
				params.Set("status", v)
			}
			if v, _ := cmd.Flags().GetString("mode"); v != "" {
				params.Set("mode", v)
			}
			c := newAPIClient()
			data, err := c.Get(cmd.Context(), "/api/v1/workflows", params)
			if err != nil {
				return err
			}
			return printResponse(data)
		},
	}
	apiListCmd.Flags().String("applies_to", "", "Filter by artifact type")
	apiListCmd.Flags().String("status", "", "Filter by status (Active|Deprecated|Superseded)")
	apiListCmd.Flags().String("mode", "", "Filter by mode (execution|creation)")

	apiReadCmd := &cobra.Command{
		Use:   "api-read <id>",
		Short: "Read a workflow via the API (returns executable body)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			params := url.Values{}
			if v, _ := cmd.Flags().GetString("ref"); v != "" {
				params.Set("ref", v)
			}
			c := newAPIClient()
			data, err := c.Get(cmd.Context(), "/api/v1/workflows/"+args[0], params)
			if err != nil {
				return err
			}
			return printResponse(data)
		},
	}
	apiReadCmd.Flags().String("ref", "", "Git ref to read from")

	cmd.AddCommand(listCmd, showCmd, resolveCmd, createCmd, updateCmd, validateCmd, apiListCmd, apiReadCmd)
	return cmd
}

func validateCmd() *cobra.Command {
	outputFmt := "table"

	cmd := &cobra.Command{
		Use:   "validate [artifact-path]",
		Short: "Run cross-artifact validation",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			all, _ := cmd.Flags().GetBool("all")
			c := newAPIClient()
			if all || len(args) == 0 {
				return cli.ValidateAll(cmd.Context(), c, cli.OutputFormat(outputFmt))
			}
			return cli.ValidateArtifact(cmd.Context(), c, args[0], cli.OutputFormat(outputFmt))
		},
	}
	cmd.Flags().Bool("all", false, "Validate entire repository")
	cmd.Flags().StringVarP(&outputFmt, "output", "o", "table", "Output format: table or json")
	return cmd
}
