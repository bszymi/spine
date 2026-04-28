package main

import (
	"context"
	"fmt"
	"os"

	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/secrets"
	"github.com/bszymi/spine/internal/store"
	"github.com/bszymi/spine/internal/workspace"
	"github.com/spf13/cobra"
)

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

			registryURL := os.Getenv("SPINE_REGISTRY_DATABASE_URL")
			if registryURL == "" {
				return fmt.Errorf("SPINE_REGISTRY_DATABASE_URL is required for --all-workspaces")
			}
			if err := requireSecureDBURL(registryURL); err != nil {
				return fmt.Errorf("registry URL: %w", err)
			}

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

			// Wire SecretClient when configured so registry rows that
			// store database_url as `secret-store://...` can be
			// dereferenced for migration. Optional: legacy URL rows
			// continue to work without it.
			var dbSecretClient secrets.SecretClient
			if secretClientConfigured() {
				c, err := buildSecretClient(ctx)
				if err != nil {
					return fmt.Errorf("build secret client: %w", err)
				}
				dbSecretClient = c
			}
			dbProvider, err := workspace.NewDBProvider(ctx, registryURL, workspace.DBProviderConfig{
				SecretClient: dbSecretClient,
			})
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

			var failed []string
			for _, ws := range workspaces {
				wsCtx := observe.WithWorkspaceID(ctx, ws.ID)
				wsLog := observe.Logger(wsCtx)

				// List returns column values wrapped verbatim — resolve
				// per workspace so a `secret-store://...` ref is
				// dereferenced through SecretClient before opening
				// the connection.
				resolved, err := dbProvider.Resolve(wsCtx, ws.ID)
				if err != nil {
					wsLog.Error("resolve workspace failed", "error", err)
					failed = append(failed, ws.ID)
					continue
				}
				dbURL := string(resolved.DatabaseURL.Reveal())
				if dbURL == "" {
					wsLog.Warn("workspace has no database URL, skipping")
					continue
				}

				wsLog.Info("migrating workspace database")
				wsStore, err := store.NewPostgresStore(wsCtx, dbURL)
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
