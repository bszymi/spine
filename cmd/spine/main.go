package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/bszymi/spine/internal/observe"
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
			log.Info("spine server starting", "status", "placeholder")
			fmt.Println("spine: serve not yet implemented — waiting for signal to exit")

			sig := make(chan os.Signal, 1)
			signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
			<-sig

			log.Info("spine server shutting down")
			return nil
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
