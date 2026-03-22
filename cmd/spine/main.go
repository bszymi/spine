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
	"github.com/bszymi/spine/internal/event"
	"github.com/bszymi/spine/internal/gateway"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/projection"
	"github.com/bszymi/spine/internal/queue"
	"github.com/bszymi/spine/internal/store"
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

	root.AddCommand(serveCmd())
	root.AddCommand(healthCmd())
	root.AddCommand(migrateCmd())
	root.AddCommand(initRepoCmd())
	root.AddCommand(artifactCmd())
	root.AddCommand(runCmd())
	root.AddCommand(taskCmd())

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

			srv := gateway.NewServer(":"+port, gateway.ServerConfig{
				Store:     st,
				Auth:      authSvc,
				Artifacts: artifactSvc,
				ProjQuery: projQuery,
				ProjSync:  projSync,
				Git:       gitClient,
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
		Use:   "init-repo",
		Short: "Initialize Git repository for Spine",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("spine: init-repo not yet implemented")
			return nil
		},
	}
}
