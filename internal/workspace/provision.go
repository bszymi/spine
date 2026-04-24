package workspace

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/bszymi/spine/internal/cli"
	"github.com/bszymi/spine/internal/git"
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
	if err := ValidateID(workspaceID); err != nil {
		return "", err
	}

	log := observe.Logger(ctx)

	dbName := sanitizeDBName(workspaceID)
	log.Info("provisioning workspace database", "workspace_id", workspaceID, "database", dbName)

	// Connect to admin database.
	adminConn, err := pgx.Connect(ctx, p.adminURL)
	if err != nil {
		return "", fmt.Errorf("connect to admin database: %w", err)
	}
	defer func() { _ = adminConn.Close(ctx) }()

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
		// Split off any ?query portion so LastIndex("/") only scans the
		// authority+path, never the query.
		qIdx := strings.Index(connURL, "?")
		query := ""
		base := connURL
		if qIdx >= 0 {
			query = connURL[qIdx:]
			base = connURL[:qIdx]
		}

		// Look for a "/" *after* the "://" scheme separator so the two
		// slashes in the scheme itself aren't mistaken for a path.
		const sep = "://"
		schemeEnd := strings.Index(base, sep)
		authority := base[schemeEnd+len(sep):]
		if pathStart := strings.Index(authority, "/"); pathStart >= 0 {
			lastSlash := strings.LastIndex(authority, "/")
			return base[:schemeEnd+len(sep)+lastSlash+1] + newDB + query
		}
		// No existing path component — append one.
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

// RepoProvisioner initializes Git repositories for workspaces.
type RepoProvisioner struct {
	// baseDir is the parent directory for all workspace repos.
	baseDir string
}

// NewRepoProvisioner creates a provisioner.
// baseDir should be SPINE_WORKSPACE_REPOS_DIR.
func NewRepoProvisioner(baseDir string) *RepoProvisioner {
	return &RepoProvisioner{baseDir: baseDir}
}

// ProvisionRepo sets up a Git repository for a workspace.
// If gitURL is non-empty, clones the remote. Otherwise initializes a fresh repo.
// Detects existing Spine repos and skips init for those.
// Returns the repo path. On failure, cleans up the partial directory.
func (p *RepoProvisioner) ProvisionRepo(ctx context.Context, workspaceID, gitURL string) (string, error) {
	if err := ValidateID(workspaceID); err != nil {
		return "", err
	}

	log := observe.Logger(ctx)

	repoPath := filepath.Join(p.baseDir, workspaceID)

	// Belt-and-braces: even though ValidateID rules out traversal
	// shapes, confirm the joined path is still inside baseDir. Protects
	// against future relaxations of the regex and guards call sites
	// that somehow bypassed ValidateID.
	absBase, err := filepath.Abs(p.baseDir)
	if err != nil {
		return "", fmt.Errorf("resolve base dir: %w", err)
	}
	absRepo, err := filepath.Abs(repoPath)
	if err != nil {
		return "", fmt.Errorf("resolve repo path: %w", err)
	}
	rel, err := filepath.Rel(absBase, absRepo)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("workspace_id %q escapes workspace repos dir", workspaceID)
	}

	// Check if directory already exists.
	if _, err := os.Stat(repoPath); err == nil {
		return "", fmt.Errorf("repo directory already exists: %s", repoPath)
	}

	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		return "", fmt.Errorf("create repo directory: %w", err)
	}

	// Cleanup on failure.
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(repoPath)
		}
	}()

	if gitURL != "" {
		// Validate URL scheme before cloning.
		if err := git.ValidateCloneURL(gitURL); err != nil {
			return "", fmt.Errorf("invalid git URL: %w", err)
		}
		// Clone mode.
		redactedURL := gitURL
		if u, err := url.Parse(gitURL); err == nil {
			redactedURL = u.Redacted()
		}
		log.Info("cloning remote repo", "workspace_id", workspaceID, "git_url", redactedURL)
		gitClient := git.NewCLIClient("")
		if err := gitClient.Clone(ctx, gitURL, repoPath); err != nil {
			return "", fmt.Errorf("clone %s: %w", gitURL, err)
		}

		if IsSpineRepo(repoPath) {
			log.Info("existing Spine repo detected, skipping init", "workspace_id", workspaceID)
			// Full projection sync will be triggered when workspace is activated.
		} else {
			log.Info("non-Spine repo, initializing Spine structure", "workspace_id", workspaceID)
			if err := cli.InitRepo(repoPath, cli.InitOpts{NoBranch: true}); err != nil {
				return "", fmt.Errorf("init spine in cloned repo: %w", err)
			}
			if err := gitCommitAll(ctx, repoPath, "Initialize Spine structure"); err != nil {
				return "", fmt.Errorf("commit spine init: %w", err)
			}
		}
	} else {
		// Fresh mode.
		log.Info("initializing fresh repo", "workspace_id", workspaceID)
		if err := cli.InitRepo(repoPath, cli.InitOpts{NoBranch: true}); err != nil {
			return "", fmt.Errorf("init fresh repo: %w", err)
		}
		// InitRepo with NoBranch writes files but doesn't commit.
		// Commit them so the repo has a valid HEAD.
		if err := gitCommitAll(ctx, repoPath, "Initialize Spine workspace"); err != nil {
			return "", fmt.Errorf("commit initial files: %w", err)
		}
	}

	success = true
	log.Info("workspace repo provisioned", "workspace_id", workspaceID, "path", repoPath)
	return repoPath, nil
}

// gitCommitAll stages all files and creates a commit using raw exec.
// Also configures git user if not already set.
func gitCommitAll(_ context.Context, repoPath, message string) error {
	// Ensure git user is configured for the repo.
	for _, cfg := range [][]string{
		{"config", "user.email", "spine@local"},
		{"config", "user.name", "Spine"},
	} {
		cmd := exec.Command("git", cfg...)
		cmd.Dir = repoPath
		_ = cmd.Run() // best-effort, may already be set
	}

	add := exec.Command("git", "add", ".")
	add.Dir = repoPath
	if out, err := add.CombinedOutput(); err != nil {
		return fmt.Errorf("git add: %s: %w", out, err)
	}
	commit := exec.Command("git", "commit", "-m", message, "--allow-empty")
	commit.Dir = repoPath
	commit.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Spine", "GIT_AUTHOR_EMAIL=spine@local",
		"GIT_COMMITTER_NAME=Spine", "GIT_COMMITTER_EMAIL=spine@local",
	)
	if out, err := commit.CombinedOutput(); err != nil {
		return fmt.Errorf("git commit: %s: %w", out, err)
	}
	return nil
}

// IsSpineRepo checks if a directory is an existing Spine repository
// by looking for .spine.yaml or governance/ directory.
func IsSpineRepo(repoPath string) bool {
	indicators := []string{
		filepath.Join(repoPath, ".spine.yaml"),
		filepath.Join(repoPath, "governance"),
		filepath.Join(repoPath, "workflows"),
	}
	for _, path := range indicators {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}
