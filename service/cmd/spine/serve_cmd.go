package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/event"
	"github.com/bszymi/spine/core/observe"
	"github.com/bszymi/spine/core/queue"
	"github.com/bszymi/spine/service/internal/config"
	"github.com/bszymi/spine/service/internal/gateway"
	"github.com/bszymi/spine/service/internal/workspace"
	"github.com/spf13/cobra"
)

// serveCmd is the cobra entrypoint for `spine serve`. RunE composes the
// deployment from environment variables, builds the dependency bundle
// (serveDeps), assembles the gateway server config (buildServerConfig),
// then hands off to runServer for the listen/shutdown lifecycle.
//
// The body intentionally reads top-down: validate prereqs, resolve the
// workspace topology (file/db/platform-binding), open the store, load
// the spine config, build runtime fixtures (git client, queue, event
// router), assemble deps, build the runtime, run.
func serveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Start the Spine runtime server",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			ctx = observe.WithComponent(ctx, "server")
			log := observe.Logger(ctx)

			// Git floor first (INIT-023 TASK-002): the git client's argv
			// needs git >= 2.30 (`rev-parse --end-of-options`) and has no
			// fallback without the terminator. Refuse to start rather
			// than fail closed on the first read with a confusing error,
			// and before any database, workspace or push-auth setup.
			if err := git.CheckGitVersion(ctx); err != nil {
				return err
			}

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
			// SPINE_GIT_HTTP_DEV_NO_AUTH (INIT-023 TASK-015): refused
			// unless SPINE_ENV=development, before any store, secret or
			// listener is opened, so a misconfiguration fails at startup.
			gitDevNoAuth := gitHTTPDevNoAuthEnabled()
			if err := guardGitHTTPDevNoAuth(runtimeEnv, gitDevNoAuth); err != nil {
				return err
			}

			// SMP_ADMIN_TOKEN strength gate (INIT-023 TASK-013): checked
			// before any store, secret or listener is opened.
			smpAdminWarning, err := validateSMPAdminToken(os.Getenv("SMP_ADMIN_TOKEN"), runtimeEnv)
			if err != nil {
				return err
			}
			if smpAdminWarning != "" {
				log.Warn(smpAdminWarning, "env", runtimeEnv)
			}

			codeRepoBase, err := loadCodeRepoBase(runtimeEnv)
			if err != nil {
				return err
			}
			if codeRepoBase == "" {
				log.Warn("SPINE_CODE_REPO_BASE is not set — code-repo binding local paths are not containment-checked", "env", runtimeEnv)
			} else {
				log.Info("code-repo containment base configured", "code_repo_base", codeRepoBase)
			}

			port := os.Getenv("SPINE_SERVER_PORT")
			if port == "" {
				port = "8080"
			}

			deliveryCfg := loadWorkspaceDeliveryConfig()

			// Engine step-entry policy (INIT-023 TASK-007): strict unless
			// SPINE_STEP_ENTRY_POLICY=legacy; any other value refuses to
			// start. Parsed once and threaded to both orchestrator builders.
			stepEntryPolicy, err := stepEntryPolicyFromEnv()
			if err != nil {
				return err
			}
			logStepEntryPolicy(log, stepEntryPolicy)

			// The subscription-secret key ring and envelope mode, the
			// workspace resolver (with its ServicePool in shared mode) and
			// the file-mode store come from one call, which loads the mode
			// itself so no argument here can drop it (INIT-023 TASK-009).
			// The step-entry policy reaches every pooled workspace's
			// orchestrator through the same call (INIT-023 TASK-007).
			workspaces, err := buildServeWorkspaces(ctx, runtimeEnv, log, deliveryCfg, codeRepoBase, stepEntryPolicy)
			if err != nil {
				return err
			}
			defer workspaces.Close()
			secretCipher, wsWiring, st := workspaces.SecretCipher, workspaces.Wiring, workspaces.Store

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
			// The workspace's primary client is standalone's shared working
			// tree (INIT-023 TASK-012): its home branch is the governed-merge
			// target, engine.AuthoritativeBranch ("main"), which every
			// sequence on the tree restores and every "HEAD" read means
			// (workspace.PrimaryClientOptions; gitOpts is not modified).
			gitClient := git.NewCLIClient(repoPath, workspace.PrimaryClientOptions(gitOpts)...)
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
			gitTrustedCIDRs := parseGitHTTPTrustedCIDRs(os.Getenv("SPINE_GIT_HTTP_TRUSTED_CIDRS"))
			if overlap := overlappingCIDRs(gitTrustedCIDRs, trustedProxyCIDRs); len(overlap) > 0 {
				log.Warn("SPINE_GIT_HTTP_TRUSTED_CIDRS overlaps SPINE_TRUSTED_PROXY_CIDRS: a request from a declared proxy is never given token-less git access, so token-less clones from the overlap will be refused",
					"overlap", overlap)
			}

			orphanThreshold := time.Duration(0)
			if v := os.Getenv("SPINE_ORPHAN_THRESHOLD"); v != "" {
				if d, parseErr := time.ParseDuration(v); parseErr == nil {
					orphanThreshold = d
				} else {
					log.Error("invalid SPINE_ORPHAN_THRESHOLD, using default", "value", v, "error", parseErr)
				}
			}

			runtimeWS := os.Getenv("SPINE_WORKSPACE_ID")
			if runtimeWS == "" {
				runtimeWS = "default"
			}

			deps := serveDeps{
				Store:                      st,
				RepoPath:                   repoPath,
				CodeRepoBase:               codeRepoBase,
				WorkspaceID:                runtimeWS,
				SpineCfg:                   spineCfg,
				GitClient:                  gitClient,
				Queue:                      q,
				Events:                     eventRouter,
				WSResolver:                 wsWiring.Resolver,
				WSDBProvider:               wsWiring.DBProvider,
				WSServicePool:              wsWiring.Pool,
				BindingInvalidationHandler: wsWiring.BindingInvalidationHandler,
				SecretCipher:               secretCipher,
				SecretClient:               wsWiring.SecretClient,
				RuntimeEnv:                 runtimeEnv,
				DevMode:                    devMode,
				TrustedProxyCIDRs:          trustedProxyCIDRs,
				TrustedGitHTTPCIDRs:        gitTrustedCIDRs,
				GitHTTPDevNoAuth:           gitDevNoAuth,
				GitReceivePackEnabled:      parseGitReceivePackEnabled(os.Getenv("SPINE_GIT_RECEIVE_PACK_ENABLED")),
				EventDeliveryOn:            deliveryCfg.Enabled,
				SMPEventURL:                deliveryCfg.SMPEventURL,
				SMPWorkspaceID:             os.Getenv("SMP_WORKSPACE_ID"),
				SMPInternalToken:           deliveryCfg.SMPInternalToken,
				EventRetention:             deliveryCfg.EventRetention,
				SSEMaxConn:                 parsePositiveIntEnv("SPINE_SSE_MAX_CONN_PER_ACTOR"),
				OrphanThreshold:            orphanThreshold,
				WebhookTargets:             deliveryCfg.WebhookTargets,
				NotifyBridgeEnabled:        parseEventLogNotifyBridge(os.Getenv("SPINE_EVENT_LOG_NOTIFY_BRIDGE")),
				StepEntryPolicy:            stepEntryPolicy,
			}

			rt, err := buildServerConfig(ctx, deps)
			if err != nil {
				return err
			}

			srv := gateway.NewServer(":"+port, rt.Config)
			return runServer(ctx, srv, rt, log, port)
		},
	}
}
