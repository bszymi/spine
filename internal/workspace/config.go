package workspace

// WorkspaceStatus represents the lifecycle status of a workspace.
type WorkspaceStatus string

const (
	StatusActive   WorkspaceStatus = "active"
	StatusInactive WorkspaceStatus = "inactive"
)

// Config holds the resource configuration for a single workspace.
// Per data-model.md §7.2 and components.md §6.5.
type Config struct {
	// ID is the unique identifier for the workspace.
	ID string

	// DisplayName is a human-readable name for the workspace.
	DisplayName string

	// DatabaseURL is the connection string for the workspace's PostgreSQL database
	// (runtime + projection schemas).
	DatabaseURL string

	// RepoPath is the filesystem path to the workspace's Git working directory.
	RepoPath string

	// Status indicates whether the workspace is active or inactive.
	Status WorkspaceStatus

	// ActorScope defines workspace-scoped actor/auth configuration.
	// Actors registered in one workspace cannot operate in another.
	// This field carries whatever auth context is needed to initialize
	// per-workspace actor services (e.g., token signing scope, actor registry ID).
	ActorScope string
}
