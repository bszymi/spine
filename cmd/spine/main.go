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

	"github.com/bszymi/spine/internal/artifact"
	"github.com/bszymi/spine/internal/auth"
	"github.com/bszymi/spine/internal/cli"
	"github.com/bszymi/spine/internal/event"
	"github.com/bszymi/spine/internal/gateway"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/projection"
	"github.com/bszymi/spine/internal/queue"
	"github.com/bszymi/spine/internal/store"
	"github.com/bszymi/spine/internal/validation"
	"github.com/spf13/cobra"
)

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

			gitClient := git.NewCLIClient(repoPath)
			q := queue.NewMemoryQueue(100)
			go q.Start(ctx)
			eventRouter := event.NewQueueRouter(q)

			var artifactSvc *artifact.Service
			var projQuery *projection.QueryService
			var projSync *projection.Service

			artifactSvc = artifact.NewService(gitClient, eventRouter, repoPath)
			if st != nil {
				projQuery = projection.NewQueryService(st, gitClient)
				projSync = projection.NewService(gitClient, st, eventRouter, 30*time.Second)
			}

			var validator *validation.Engine
			if st != nil {
				validator = validation.NewEngine(st)
			}

			srv := gateway.NewServer(":"+port, gateway.ServerConfig{
				Store:     st,
				Auth:      authSvc,
				Artifacts: artifactSvc,
				ProjQuery: projQuery,
				ProjSync:  projSync,
				Git:       gitClient,
				Validator: validator,
			})

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
