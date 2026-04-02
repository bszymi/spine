package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bszymi/spine/internal/actor"
	"github.com/bszymi/spine/internal/artifact"
	"github.com/bszymi/spine/internal/auth"
	"github.com/bszymi/spine/internal/cli"
	"github.com/bszymi/spine/internal/config"
	"github.com/bszymi/spine/internal/divergence"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/engine"
	"github.com/bszymi/spine/internal/event"
	"github.com/bszymi/spine/internal/gateway"
	"github.com/bszymi/spine/internal/git"
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

func serveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Start the Spine runtime server",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			ctx = observe.WithComponent(ctx, "server")
			log := observe.Logger(ctx)

			port := os.Getenv("SPINE_SERVER_PORT")
			if port == "" {
				port = "8080"
			}

			// Initialize workspace resolver.
			// SPINE_WORKSPACE_MODE: "single" (default) or "shared".
			var wsResolver workspace.Resolver
			var wsDBProvider *workspace.DBProvider
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
				var err error
				wsDBProvider, err = workspace.NewDBProvider(ctx, registryURL, workspace.DBProviderConfig{})
				if err != nil {
					return fmt.Errorf("connect to workspace registry: %w", err)
				}
				defer wsDBProvider.Close()
				wsResolver = wsDBProvider
				log.Info("workspace mode: shared", "registry_url", "***")
			default:
				return fmt.Errorf("unknown SPINE_WORKSPACE_MODE: %q (expected \"single\" or \"shared\")", wsMode)
			}

			_ = wsResolver // Will be used by gateway and background services in future epics.

			// Connect to database (optional — server starts without it)
			var st store.Store
			dbURL := os.Getenv("SPINE_DATABASE_URL")
			if dbURL != "" {
				pgStore, err := store.NewPostgresStore(ctx, dbURL)
				if err != nil {
					log.Error("database connection failed, starting without store", "error", err)
				} else {
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

			gitClient := git.NewCLIClient(repoPath)
			q := queue.NewMemoryQueue(100)
			go q.Start(ctx)
			eventRouter := event.NewQueueRouter(q)

			var artifactSvc *artifact.Service
			var projQuery *projection.QueryService
			var projSync *projection.Service

			artifactSvc = artifact.NewService(gitClient, eventRouter, repoPath)
			artifactSvc.WithArtifactsDir(spineCfg.ArtifactsDir)
			if st != nil {
				projQuery = projection.NewQueryService(st, gitClient)
				projSync = projection.NewService(gitClient, st, eventRouter, 30*time.Second)
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

			// Set up gateway with all services.
			var divSvcForGateway gateway.BranchCreator
			if st != nil {
				divSvcForGateway = divergence.NewService(st, gitClient, eventRouter)
			}

			var starter gateway.RunStarter
			var planningStarter gateway.PlanningRunStarter
			if orch != nil {
				orch.WithArtifactWriter(artifactSvc)
				starter = &runAdapter{orch: orch}
				planningStarter = &planningRunAdapter{orch: orch}
			}

			srv := gateway.NewServer(":"+port, gateway.ServerConfig{
				Store:              st,
				Auth:               authSvc,
				Artifacts:          artifactSvc,
				ProjQuery:          projQuery,
				ProjSync:           projSync,
				Git:                gitClient,
				Validator:          validator,
				WorkflowResolver:   wfResolver,
				BranchCreator:      divSvcForGateway,
				Events:             eventRouter,
				RunStarter:         starter,
				PlanningRunStarter: planningStarter,
				WorkspaceResolver:  wsResolver,
				WSDBProvider:       wsDBProvider,
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

			ctx := context.Background()
			ctx = observe.WithComponent(ctx, "migrate")
			log := observe.Logger(ctx)

			if !allWorkspaces {
				// Single database migration (existing behavior).
				dbURL := os.Getenv("SPINE_DATABASE_URL")
				if dbURL == "" {
					return fmt.Errorf("SPINE_DATABASE_URL is required")
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

			// 1. Migrate the registry database itself.
			log.Info("migrating registry database")
			registryStore, err := store.NewPostgresStore(ctx, registryURL)
			if err != nil {
				return fmt.Errorf("connect to registry database: %w", err)
			}
			if err := registryStore.ApplyMigrations(ctx, "migrations/registry"); err != nil {
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

				if err := wsStore.ApplyMigrations(wsCtx, migrationsDir); err != nil {
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

	cmd.AddCommand(listCmd, showCmd, resolveCmd)
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
