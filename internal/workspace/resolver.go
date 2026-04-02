package workspace

import (
	"context"
	"errors"
)

// ErrWorkspaceNotFound is returned when a workspace ID does not exist in the registry.
var ErrWorkspaceNotFound = errors.New("workspace not found")

// ErrWorkspaceInactive is returned when a workspace exists but is not active.
var ErrWorkspaceInactive = errors.New("workspace inactive")

// Resolver resolves workspace configuration by ID.
// Per components.md §6.5, two implementations exist:
//   - File/env provider for single-workspace mode
//   - Database provider for shared-runtime mode
type Resolver interface {
	// Resolve returns the configuration for the given workspace ID.
	// Returns ErrWorkspaceNotFound if the ID does not exist.
	// Returns ErrWorkspaceInactive if the workspace exists but is inactive.
	Resolve(ctx context.Context, workspaceID string) (*Config, error)

	// List returns all active workspace configurations.
	// Used by background services (scheduler, projection sync) to iterate
	// over workspaces. See components.md §6.5.
	List(ctx context.Context) ([]Config, error)
}
