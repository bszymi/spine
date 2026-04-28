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
	TransitionRunStatus(ctx context.Context, runID string, fromStatus, toStatus domain.RunStatus) (bool, error)
	UpdateCurrentStep(ctx context.Context, runID, stepID string) error
	SetCommitMeta(ctx context.Context, runID string, meta map[string]string) error
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

	// Branch Protection Rules (ADR-009 projection). Atomic swap — a
	// single call replaces the workspace's effective ruleset.
	UpsertBranchProtectionRules(ctx context.Context, rules []BranchProtectionRuleProjection, sourceCommit string) error
	ListBranchProtectionRules(ctx context.Context) ([]BranchProtectionRuleProjection, error)

	// Repository Bindings (ADR-013, INIT-014 EPIC-001).
	// Operational connection details for code repositories registered in
	// the workspace catalog at /.spine/repositories.yaml. The primary
	// "spine" repository has no row and is resolved virtually from the
	// workspace's RepoPath and configured authoritative branch.
	CreateRepositoryBinding(ctx context.Context, b *RepositoryBinding) error
	GetRepositoryBinding(ctx context.Context, workspaceID, repositoryID string) (*RepositoryBinding, error)
	GetActiveRepositoryBinding(ctx context.Context, workspaceID, repositoryID string) (*RepositoryBinding, error)
	UpdateRepositoryBinding(ctx context.Context, b *RepositoryBinding) error
	ListRepositoryBindings(ctx context.Context, workspaceID string) ([]RepositoryBinding, error)
	ListActiveRepositoryBindings(ctx context.Context, workspaceID string) ([]RepositoryBinding, error)
	DeactivateRepositoryBinding(ctx context.Context, workspaceID, repositoryID string) error

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

	// Actor-Skill Associations
	AddSkillToActor(ctx context.Context, actorID, skillID string) error
	RemoveSkillFromActor(ctx context.Context, actorID, skillID string) error
	ListActorSkills(ctx context.Context, actorID string) ([]domain.Skill, error)
	ListActorsBySkills(ctx context.Context, skillNames []string) ([]domain.Actor, error)

	// Execution Projections
	UpsertExecutionProjection(ctx context.Context, proj *ExecutionProjection) error
	GetExecutionProjection(ctx context.Context, taskPath string) (*ExecutionProjection, error)
	QueryExecutionProjections(ctx context.Context, query ExecutionProjectionQuery) ([]ExecutionProjection, error)
	DeleteExecutionProjection(ctx context.Context, taskPath string) error

	// Event Delivery Queue
	EnqueueDelivery(ctx context.Context, entry *DeliveryEntry) error
	ClaimDeliveries(ctx context.Context, limit int) ([]DeliveryEntry, error)
	UpdateDeliveryStatus(ctx context.Context, deliveryID, status string, lastError string, nextRetryAt *time.Time) error
	MarkDelivered(ctx context.Context, deliveryID string) error
	LogDeliveryAttempt(ctx context.Context, entry *DeliveryLogEntry) error
	ListDeliveryHistory(ctx context.Context, query DeliveryHistoryQuery) ([]DeliveryLogEntry, error)
	GetDelivery(ctx context.Context, deliveryID string) (*DeliveryEntry, error)
	ListDeliveries(ctx context.Context, subscriptionID string, status string, limit int) ([]DeliveryEntry, error)
	GetDeliveryStats(ctx context.Context, subscriptionID string) (*DeliveryStats, error)
	WriteEventLog(ctx context.Context, entry *EventLogEntry) error
	ListEventsAfter(ctx context.Context, afterEventID string, eventTypes []string, limit int) ([]EventLogEntry, error)
	DeleteExpiredDeliveries(ctx context.Context, before time.Time) (int64, error)

	// Event Subscriptions
	CreateSubscription(ctx context.Context, sub *EventSubscription) error
	GetSubscription(ctx context.Context, subscriptionID string) (*EventSubscription, error)
	UpdateSubscription(ctx context.Context, sub *EventSubscription) error
	DeleteSubscription(ctx context.Context, subscriptionID string) error
	ListSubscriptions(ctx context.Context, workspaceID string) ([]EventSubscription, error)
	ListActiveSubscriptionsByEventType(ctx context.Context, eventType string) ([]EventSubscription, error)

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
	TransitionRunStatus(ctx context.Context, runID string, fromStatus, toStatus domain.RunStatus) (bool, error)
	CreateStepExecution(ctx context.Context, exec *domain.StepExecution) error
	UpdateStepExecution(ctx context.Context, exec *domain.StepExecution) error
}

// ArtifactProjection represents a projected artifact in the database.
type ArtifactProjection struct {
	ArtifactPath string   `json:"artifact_path"`
	ArtifactID   string   `json:"artifact_id"`
	ArtifactType string   `json:"artifact_type"`
	Title        string   `json:"title"`
	Status       string   `json:"status"`
	Metadata     []byte   `json:"metadata"` // JSONB
	Content      string   `json:"content"`
	Links        []byte   `json:"links"`        // JSONB
	Repositories []string `json:"repositories"` // task-only: code repository IDs declared in frontmatter
	SourceCommit string   `json:"source_commit"`
	ContentHash  string   `json:"content_hash"`
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

// BranchProtectionRuleProjection is one row of the projected
// /.spine/branch-protection.yaml ruleset (ADR-009 §1). Protections is a
// JSONB array of rule-kind strings (e.g. ["no-delete","no-direct-write"])
// mirroring the YAML value; the branchprotect package converts it to its
// typed RuleKind enum. SourceCommit is "bootstrap" for the seed rows
// installed by the 018 migration, otherwise the commit SHA that produced
// the projection.
type BranchProtectionRuleProjection struct {
	BranchPattern string `json:"branch_pattern"`
	RuleOrder     int    `json:"rule_order"`
	Protections   []byte `json:"protections"` // JSONB array of strings
	SourceCommit  string `json:"source_commit"`
}

// RepositoryBinding holds the operational connection details for a code
// repository registered in the workspace catalog (ADR-013). Identity
// fields (kind, name, default_branch, role, description) live in
// /.spine/repositories.yaml — only the runtime fields are stored here.
//
// The primary Spine repository (`repository_id = "spine"`) has no
// binding row; it is resolved virtually from the workspace's RepoPath
// and configured authoritative branch. The migration enforces this
// with a CHECK constraint, and the store's create path rejects the ID
// up front.
//
// DefaultBranch is an optional override — when empty, callers fall
// back to the catalog's `default_branch` for the same repository ID.
// It exists so a single workspace can pin a non-catalog branch (e.g.
// a long-lived release branch) without rewriting the governed
// catalog.
type RepositoryBinding struct {
	RepositoryID   string    `json:"repository_id" yaml:"repository_id"`
	WorkspaceID    string    `json:"workspace_id" yaml:"workspace_id"`
	CloneURL       string    `json:"clone_url" yaml:"clone_url"`
	CredentialsRef string    `json:"credentials_ref,omitempty" yaml:"credentials_ref,omitempty"`
	LocalPath      string    `json:"local_path" yaml:"local_path"`
	DefaultBranch  string    `json:"default_branch,omitempty" yaml:"default_branch,omitempty"`
	Status         string    `json:"status" yaml:"status"`
	CreatedAt      time.Time `json:"created_at" yaml:"created_at"`
	UpdatedAt      time.Time `json:"updated_at" yaml:"updated_at"`
}

// Repository binding statuses.
const (
	RepositoryBindingStatusActive   = "active"
	RepositoryBindingStatusInactive = "inactive"
)

// PrimaryRepositoryID is the reserved ID of the primary Spine
// repository within a workspace. No binding row may use it — it is
// resolved virtually from the workspace's RepoPath and the configured
// authoritative branch. The 019 migration enforces this with a CHECK
// constraint, and the store's create path rejects it explicitly so
// callers see a SpineError instead of a generic database constraint
// violation.
const PrimaryRepositoryID = "spine"

// SyncState tracks projection sync progress.
type SyncState struct {
	LastSyncedCommit string     `json:"last_synced_commit"`
	LastSyncedAt     *time.Time `json:"last_synced_at,omitempty"`
	Status           string     `json:"status"` // idle, syncing, rebuilding, error
	ErrorDetail      string     `json:"error_detail,omitempty"`
}

// Artifact-query pagination bounds shared between the HTTP handler
// and the store. The store enforces these defensively even when an
// internal caller bypasses the HTTP pagination helper, so a stray
// `Limit: 0` cannot fan out to an unbounded scan and a `Limit:
// 1_000_000` cannot push a multi-megabyte result set through the
// gateway.
const (
	ArtifactQueryDefaultLimit = 50
	ArtifactQueryMaxLimit     = 200
)

// ArtifactQuery defines parameters for querying projected artifacts.
type ArtifactQuery struct {
	Type       string
	Status     string
	ParentPath string
	Search     string
	Limit      int
	Cursor     string
}

// ClampedLimit returns the limit value the store will use for this
// query: non-positive limits resolve to ArtifactQueryDefaultLimit and
// oversize limits are capped at ArtifactQueryMaxLimit. Exposed so
// callers (and tests) can preview the effective limit before
// QueryArtifacts runs.
func (q ArtifactQuery) ClampedLimit() int {
	if q.Limit <= 0 {
		return ArtifactQueryDefaultLimit
	}
	if q.Limit > ArtifactQueryMaxLimit {
		return ArtifactQueryMaxLimit
	}
	return q.Limit
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
