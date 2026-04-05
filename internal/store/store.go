package store

import (
	"context"
	"time"

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
	UpdateCurrentStep(ctx context.Context, runID, stepID string) error
	ListRunsByTask(ctx context.Context, taskPath string) ([]domain.Run, error)

	// Step Executions
	CreateStepExecution(ctx context.Context, exec *domain.StepExecution) error
	GetStepExecution(ctx context.Context, executionID string) (*domain.StepExecution, error)
	UpdateStepExecution(ctx context.Context, exec *domain.StepExecution) error
	ListStepExecutionsByRun(ctx context.Context, runID string) ([]domain.StepExecution, error)

	// Projections
	UpsertArtifactProjection(ctx context.Context, proj *ArtifactProjection) error
	DeleteArtifactProjection(ctx context.Context, artifactPath string) error
	GetArtifactProjection(ctx context.Context, artifactPath string) (*ArtifactProjection, error)
	QueryArtifacts(ctx context.Context, query ArtifactQuery) (*ArtifactQueryResult, error)
	DeleteAllProjections(ctx context.Context) error

	// Links
	UpsertArtifactLinks(ctx context.Context, sourcePath string, links []ArtifactLink, sourceCommit string) error
	DeleteArtifactLinks(ctx context.Context, sourcePath string) error
	QueryArtifactLinks(ctx context.Context, sourcePath string) ([]ArtifactLink, error)
	QueryArtifactLinksByTarget(ctx context.Context, targetPath string) ([]ArtifactLink, error)

	// Actors
	GetActor(ctx context.Context, actorID string) (*domain.Actor, error)
	CreateActor(ctx context.Context, actor *domain.Actor) error
	UpdateActor(ctx context.Context, actor *domain.Actor) error
	ListActors(ctx context.Context) ([]domain.Actor, error)
	ListActorsByStatus(ctx context.Context, status domain.ActorStatus) ([]domain.Actor, error)

	// Tokens
	GetActorByTokenHash(ctx context.Context, tokenHash string) (*domain.Actor, *domain.Token, error)
	CreateToken(ctx context.Context, record *TokenRecord) error
	RevokeToken(ctx context.Context, tokenID string) error
	ListTokensByActor(ctx context.Context, actorID string) ([]domain.Token, error)

	// Divergence
	CreateDivergenceContext(ctx context.Context, div *domain.DivergenceContext) error
	UpdateDivergenceContext(ctx context.Context, div *domain.DivergenceContext) error
	GetDivergenceContext(ctx context.Context, divergenceID string) (*domain.DivergenceContext, error)
	CreateBranch(ctx context.Context, branch *domain.Branch) error
	UpdateBranch(ctx context.Context, branch *domain.Branch) error
	GetBranch(ctx context.Context, branchID string) (*domain.Branch, error)
	ListBranchesByDivergence(ctx context.Context, divergenceID string) ([]domain.Branch, error)

	// Assignments
	CreateAssignment(ctx context.Context, a *domain.Assignment) error
	UpdateAssignmentStatus(ctx context.Context, assignmentID string, status domain.AssignmentStatus, respondedAt *time.Time) error
	GetAssignment(ctx context.Context, assignmentID string) (*domain.Assignment, error)
	ListAssignmentsByActor(ctx context.Context, actorID string, status *domain.AssignmentStatus) ([]domain.Assignment, error)
	ListExpiredAssignments(ctx context.Context, before time.Time) ([]domain.Assignment, error)

	// Scheduler queries
	ListRunsByStatus(ctx context.Context, status domain.RunStatus) ([]domain.Run, error)
	ListActiveStepExecutions(ctx context.Context) ([]domain.StepExecution, error)
	ListStaleActiveRuns(ctx context.Context, noActivitySince time.Time) ([]domain.Run, error)
	ListTimedOutRuns(ctx context.Context, now time.Time) ([]domain.Run, error)

	// Workflows
	UpsertWorkflowProjection(ctx context.Context, proj *WorkflowProjection) error
	DeleteWorkflowProjection(ctx context.Context, workflowPath string) error
	GetWorkflowProjection(ctx context.Context, workflowPath string) (*WorkflowProjection, error)
	ListActiveWorkflowProjections(ctx context.Context) ([]WorkflowProjection, error)

	// Sync State
	GetSyncState(ctx context.Context) (*SyncState, error)
	UpdateSyncState(ctx context.Context, state *SyncState) error

	// Discussions
	CreateThread(ctx context.Context, thread *domain.DiscussionThread) error
	GetThread(ctx context.Context, threadID string) (*domain.DiscussionThread, error)
	ListThreads(ctx context.Context, anchorType domain.AnchorType, anchorID string) ([]domain.DiscussionThread, error)
	UpdateThread(ctx context.Context, thread *domain.DiscussionThread) error
	CreateComment(ctx context.Context, comment *domain.Comment) error
	ListComments(ctx context.Context, threadID string) ([]domain.Comment, error)
	HasOpenThreads(ctx context.Context, anchorType domain.AnchorType, anchorID string) (bool, error)

	// Skills
	CreateSkill(ctx context.Context, skill *domain.Skill) error
	GetSkill(ctx context.Context, skillID string) (*domain.Skill, error)
	UpdateSkill(ctx context.Context, skill *domain.Skill) error
	ListSkills(ctx context.Context) ([]domain.Skill, error)
	ListSkillsByCategory(ctx context.Context, category string) ([]domain.Skill, error)

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

// ArtifactLink represents a denormalized link in the projection store.
type ArtifactLink struct {
	SourcePath string `json:"source_path"`
	TargetPath string `json:"target_path"`
	LinkType   string `json:"link_type"`
}

// WorkflowProjection represents a projected workflow definition in the database.
type WorkflowProjection struct {
	WorkflowPath string `json:"workflow_path"`
	WorkflowID   string `json:"workflow_id"`
	Name         string `json:"name"`
	Version      string `json:"version"`
	Status       string `json:"status"`
	AppliesTo    []byte `json:"applies_to"` // JSONB
	Definition   []byte `json:"definition"` // JSONB
	SourceCommit string `json:"source_commit"`
}

// SyncState tracks projection sync progress.
type SyncState struct {
	LastSyncedCommit string     `json:"last_synced_commit"`
	LastSyncedAt     *time.Time `json:"last_synced_at,omitempty"`
	Status           string     `json:"status"` // idle, syncing, rebuilding, error
	ErrorDetail      string     `json:"error_detail,omitempty"`
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

// TokenRecord represents a token in the database (includes hash).
type TokenRecord struct {
	TokenID   string
	ActorID   string
	TokenHash string
	Name      string
	ExpiresAt *time.Time
	RevokedAt *time.Time
	CreatedAt time.Time
}
