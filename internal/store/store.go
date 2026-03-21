package store

import (
	"context"

	"github.com/bszymi/spine/internal/domain"
)

// Store defines the database access interface for Spine.
// Per Implementation Guide §3.4.
type Store interface {
	// Transactions
	WithTx(ctx context.Context, fn func(tx Tx) error) error

	// Health
	Ping(ctx context.Context) error

	// Runs
	CreateRun(ctx context.Context, run *domain.Run) error
	GetRun(ctx context.Context, runID string) (*domain.Run, error)
	UpdateRunStatus(ctx context.Context, runID string, status domain.RunStatus) error
	ListRunsByTask(ctx context.Context, taskPath string) ([]domain.Run, error)

	// Step Executions
	CreateStepExecution(ctx context.Context, exec *domain.StepExecution) error
	GetStepExecution(ctx context.Context, executionID string) (*domain.StepExecution, error)
	UpdateStepExecution(ctx context.Context, exec *domain.StepExecution) error
	ListStepExecutionsByRun(ctx context.Context, runID string) ([]domain.StepExecution, error)

	// Projections
	UpsertArtifactProjection(ctx context.Context, proj *ArtifactProjection) error
	GetArtifactProjection(ctx context.Context, artifactPath string) (*ArtifactProjection, error)
	QueryArtifacts(ctx context.Context, query ArtifactQuery) (*ArtifactQueryResult, error)

	// Migrations
	ApplyMigrations(ctx context.Context, migrationsDir string) error
	IsMigrationApplied(ctx context.Context, version string) (bool, error)

	// Close
	Close()
}

// Tx represents a database transaction.
type Tx interface {
	CreateRun(ctx context.Context, run *domain.Run) error
	UpdateRunStatus(ctx context.Context, runID string, status domain.RunStatus) error
	CreateStepExecution(ctx context.Context, exec *domain.StepExecution) error
	UpdateStepExecution(ctx context.Context, exec *domain.StepExecution) error
}

// ArtifactProjection represents a projected artifact in the database.
type ArtifactProjection struct {
	ArtifactPath string `json:"artifact_path"`
	ArtifactID   string `json:"artifact_id"`
	ArtifactType string `json:"artifact_type"`
	Title        string `json:"title"`
	Status       string `json:"status"`
	Metadata     []byte `json:"metadata"` // JSONB
	Content      string `json:"content"`
	Links        []byte `json:"links"` // JSONB
	SourceCommit string `json:"source_commit"`
	ContentHash  string `json:"content_hash"`
}

// ArtifactQuery defines parameters for querying projected artifacts.
type ArtifactQuery struct {
	Type       string
	Status     string
	ParentPath string
	Search     string
	Limit      int
	Cursor     string
}

// ArtifactQueryResult contains the result of an artifact query.
type ArtifactQueryResult struct {
	Items      []ArtifactProjection
	NextCursor string
	HasMore    bool
}
