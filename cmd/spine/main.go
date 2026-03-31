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
	"github.com/spf13/cobra"
)

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

			var planningStarter gateway.PlanningRunStarter
			if orch != nil {
				orch.WithArtifactWriter(artifactSvc)
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
				PlanningRunStarter: planningStarter,
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
	return &cobra.Command{
		Use:   "migrate",
		Short: "Run database migrations",
		RunE: func(cmd *cobra.Command, args []string) error {
			dbURL := os.Getenv("SPINE_DATABASE_URL")
			if dbURL == "" {
				return fmt.Errorf("SPINE_DATABASE_URL is required")
			}

			migrationsDir := os.Getenv("SPINE_MIGRATIONS_DIR")
			if migrationsDir == "" {
				migrationsDir = "migrations"
			}

			ctx := context.Background()
			ctx = observe.WithComponent(ctx, "migrate")
			log := observe.Logger(ctx)

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
		},
	}
}

func initRepoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init-repo [path]",
		Short: "Initialize a new Spine repository with directory structure and seed documents",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "."
			if len(args) > 0 {
				path = args[0]
			}
			if err := cli.InitRepo(path); err != nil {
				return err
			}
			fmt.Printf("Spine repository initialized at %s\n", path)
			return nil
		},
	}
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
			client := cli.NewClient(apiURL, token)
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
			client := cli.NewClient(apiURL, token)
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
			client := cli.NewClient(apiURL, token)
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
			client := cli.NewClient(apiURL, token)
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
