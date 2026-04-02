package workspace

import (
	"context"
	"os"
)

// FileProvider implements Resolver by reading workspace configuration from
// environment variables. It always returns a single workspace — this is the
// provider used in single-workspace (default) mode.
//
// Environment variables:
//   - SPINE_WORKSPACE_ID: workspace identifier (default: "default")
//   - SPINE_DATABASE_URL: PostgreSQL connection string for the workspace
//   - SPINE_REPO_PATH: filesystem path to the workspace's Git repository (default: ".")
type FileProvider struct {
	config Config
}

// NewFileProvider creates a FileProvider by reading current environment variables.
func NewFileProvider() *FileProvider {
	id := os.Getenv("SPINE_WORKSPACE_ID")
	if id == "" {
		id = "default"
	}

	dbURL := os.Getenv("SPINE_DATABASE_URL")

	repoPath := os.Getenv("SPINE_REPO_PATH")
	if repoPath == "" {
		repoPath = "."
	}

	return &FileProvider{
		config: Config{
			ID:          id,
			DisplayName: id,
			DatabaseURL: dbURL,
			RepoPath:    repoPath,
			Status:      StatusActive,
		},
	}
}

// Resolve returns the single configured workspace. In single mode, any
// workspace ID is accepted (the provider always returns its one workspace).
// This preserves backward compatibility — existing clients that don't send
// a workspace ID will work when the middleware falls back to the default.
func (p *FileProvider) Resolve(_ context.Context, _ string) (*Config, error) {
	cfg := p.config
	return &cfg, nil
}

// List returns a slice containing the single configured workspace.
func (p *FileProvider) List(_ context.Context) ([]Config, error) {
	return []Config{p.config}, nil
}
