package workspace

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/bszymi/spine/internal/observe"
	"github.com/bszymi/spine/internal/store"
)

// DatabaseProvisioner creates and migrates PostgreSQL databases for workspaces.
type DatabaseProvisioner struct {
	// adminURL is a connection string with CREATE DATABASE privileges,
	// typically pointing to the 'postgres' database.
	adminURL string
	// migrationsDir is the path to workspace schema migrations.
	migrationsDir string
}

// NewDatabaseProvisioner creates a provisioner.
// adminURL should be SPINE_PROVISIONING_DATABASE_URL.
// migrationsDir defaults to "migrations" if empty.
func NewDatabaseProvisioner(adminURL, migrationsDir string) *DatabaseProvisioner {
	if migrationsDir == "" {
		migrationsDir = "migrations"
	}
	return &DatabaseProvisioner{
		adminURL:      adminURL,
		migrationsDir: migrationsDir,
	}
}

// ProvisionDatabase creates a new PostgreSQL database for a workspace and
// runs all schema migrations. Returns the database URL for the new workspace.
// If any step fails, the partially created database is dropped.
func (p *DatabaseProvisioner) ProvisionDatabase(ctx context.Context, workspaceID string) (string, error) {
	log := observe.Logger(ctx)

	dbName := sanitizeDBName(workspaceID)
	log.Info("provisioning workspace database", "workspace_id", workspaceID, "database", dbName)

	// Connect to admin database.
	adminConn, err := pgx.Connect(ctx, p.adminURL)
	if err != nil {
		return "", fmt.Errorf("connect to admin database: %w", err)
	}
	defer adminConn.Close(ctx)

	// Create database. CREATE DATABASE cannot run inside a transaction.
	_, err = adminConn.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s", pgIdentifier(dbName)))
	if err != nil {
		return "", fmt.Errorf("create database %s: %w", dbName, err)
	}

	// Build connection URL for the new database by replacing the database name
	// in the admin URL.
	wsDBURL := replaceDatabaseInURL(p.adminURL, dbName)

	// Run migrations against the new database.
	wsStore, err := store.NewPostgresStore(ctx, wsDBURL)
	if err != nil {
		// Rollback: drop the database.
		log.Error("connect to new database failed, rolling back", "database", dbName, "error", err)
		_, _ = adminConn.Exec(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s", pgIdentifier(dbName)))
		return "", fmt.Errorf("connect to workspace database: %w", err)
	}

	if err := wsStore.ApplyMigrations(ctx, p.migrationsDir); err != nil {
		wsStore.Close()
		log.Error("migrations failed, rolling back", "database", dbName, "error", err)
		_, _ = adminConn.Exec(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s", pgIdentifier(dbName)))
		return "", fmt.Errorf("apply migrations to %s: %w", dbName, err)
	}

	wsStore.Close()

	log.Info("workspace database provisioned", "workspace_id", workspaceID, "database", dbName)
	return wsDBURL, nil
}

// sanitizeDBName converts a workspace ID to a valid PostgreSQL database name.
// Replaces non-alphanumeric characters with underscores, lowercases, and
// prefixes with "spine_ws_".
func sanitizeDBName(workspaceID string) string {
	re := regexp.MustCompile(`[^a-zA-Z0-9]`)
	safe := re.ReplaceAllString(workspaceID, "_")
	safe = strings.ToLower(safe)
	return "spine_ws_" + safe
}

// pgIdentifier quotes a PostgreSQL identifier to prevent SQL injection.
func pgIdentifier(name string) string {
	// Double any double-quotes in the name, then wrap in double-quotes.
	escaped := strings.ReplaceAll(name, `"`, `""`)
	return `"` + escaped + `"`
}

// replaceDatabaseInURL replaces the database name in a PostgreSQL connection URL.
// Handles both postgres://user:pass@host:port/dbname and key=value formats.
func replaceDatabaseInURL(connURL, newDB string) string {
	// Handle postgres:// URL format.
	if strings.HasPrefix(connURL, "postgres://") || strings.HasPrefix(connURL, "postgresql://") {
		// Find the last / before any ? query params.
		qIdx := strings.Index(connURL, "?")
		query := ""
		base := connURL
		if qIdx >= 0 {
			query = connURL[qIdx:]
			base = connURL[:qIdx]
		}

		lastSlash := strings.LastIndex(base, "/")
		if lastSlash >= 0 {
			return base[:lastSlash+1] + newDB + query
		}
		return base + "/" + newDB + query
	}

	// Handle key=value format — replace dbname=xxx.
	if strings.Contains(connURL, "dbname=") {
		re := regexp.MustCompile(`dbname=\S+`)
		return re.ReplaceAllString(connURL, "dbname="+newDB)
	}

	// Fallback: append dbname.
	return connURL + " dbname=" + newDB
}
