package store

import (
	"context"
	"time"

	"github.com/bszymi/spine/core/domain"
)

// Role-specific store interfaces (INIT-022 EPIC-001 TASK-010).
//
// Store partitions the database surface so consumers can declare a
// narrower dependency than the full union. *PostgresStore satisfies
// every role; the union Store still exists (in store.go) for callers
// that legitimately need every method (the cmd/spine wiring, the
// per-workspace ServiceSet) but new consumers and migrated ones should
// take the smallest role they actually use.
//
// Compile-time satisfaction is asserted in interfaces_assertions.go so
// adding a method to a role automatically requires the matching
// implementation on PostgresStore.

// SystemStore covers transaction control, health, migrations, and
// lifecycle. Every consumer that needs one of these almost always also
// needs at least one of the other roles, so callers usually embed this
// alongside a domain role rather than depend on it alone.
type SystemStore interface {
	WithTx(ctx context.Context, fn func(tx Tx) error) error
	Ping(ctx context.Context) error
	ApplyMigrations(ctx context.Context, migrationsDir string) error
	IsMigrationApplied(ctx context.Context, version string) (bool, error)
	Close()
}

// RunStore covers Run + StepExecution + RepositoryMergeOutcome
// persistence — the orchestration surface used by the engine and the
// scheduler.
type RunStore interface {
	// Runs
	CreateRun(ctx context.Context, run *domain.Run) error
	GetRun(ctx context.Context, runID string) (*domain.Run, error)
	UpdateRunStatus(ctx context.Context, runID string, status domain.RunStatus) error
	// UpdateRunStatusAt is UpdateRunStatus with a caller-supplied
	// completion timestamp. Callers that drive a scan loop with an
	// injectable clock (e.g. Scheduler.ScanRunTimeouts) must use this
	// so the persisted `completed_at` reflects the scan's observed
	// `now` instead of Postgres' transaction-time `now()`. Without this
	// seam an `Advance(2h)` test scenario flips a run to cancelled
	// before its own `completed_at` reaches `timeout_at`, breaking
	// time-based assertions and the audit-log invariant that a
	// cancelled-for-timeout run was alive past the deadline.
	UpdateRunStatusAt(ctx context.Context, runID string, status domain.RunStatus, completedAt time.Time) error
	TransitionRunStatus(ctx context.Context, runID string, fromStatus, toStatus domain.RunStatus) (bool, error)
	UpdateCurrentStep(ctx context.Context, runID, stepID string) error
	SetCommitMeta(ctx context.Context, runID string, meta map[string]string) error
	ListRunsByTask(ctx context.Context, taskPath string) ([]domain.Run, error)
	ListRunsByStatus(ctx context.Context, status domain.RunStatus) ([]domain.Run, error)
	ListStaleActiveRuns(ctx context.Context, noActivitySince time.Time) ([]domain.Run, error)
	ListTimedOutRuns(ctx context.Context, now time.Time) ([]domain.Run, error)

	// Step Executions
	CreateStepExecution(ctx context.Context, exec *domain.StepExecution) error
	GetStepExecution(ctx context.Context, executionID string) (*domain.StepExecution, error)
	UpdateStepExecution(ctx context.Context, exec *domain.StepExecution) error
	ListStepExecutionsByRun(ctx context.Context, runID string) ([]domain.StepExecution, error)
	ListActiveStepExecutions(ctx context.Context) ([]domain.StepExecution, error)

	// Repository Merge Outcomes (INIT-014 EPIC-005). One row per
	// (run_id, repository_id); the orchestrator upserts an outcome
	// after each per-repo merge attempt so partial cross-repo states
	// are explicit and queryable.
	UpsertRepositoryMergeOutcome(ctx context.Context, outcome *domain.RepositoryMergeOutcome) error
	GetRepositoryMergeOutcome(ctx context.Context, runID, repositoryID string) (*domain.RepositoryMergeOutcome, error)
	ListRepositoryMergeOutcomes(ctx context.Context, runID string) ([]domain.RepositoryMergeOutcome, error)
}

// BranchStore covers DivergenceContext + Branch persistence.
type BranchStore interface {
	CreateDivergenceContext(ctx context.Context, div *domain.DivergenceContext) error
	UpdateDivergenceContext(ctx context.Context, div *domain.DivergenceContext) error
	GetDivergenceContext(ctx context.Context, divergenceID string) (*domain.DivergenceContext, error)
	CreateBranch(ctx context.Context, branch *domain.Branch) error
	UpdateBranch(ctx context.Context, branch *domain.Branch) error
	GetBranch(ctx context.Context, branchID string) (*domain.Branch, error)
	ListBranchesByDivergence(ctx context.Context, divergenceID string) ([]domain.Branch, error)
}

// ArtifactStore covers artifact projections + links + execution
// projections — the read/write surface used by the projection sync
// loop and the artifact + execution query handlers.
type ArtifactStore interface {
	UpsertArtifactProjection(ctx context.Context, proj *ArtifactProjection) error
	DeleteArtifactProjection(ctx context.Context, artifactPath string) error
	GetArtifactProjection(ctx context.Context, artifactPath string) (*ArtifactProjection, error)
	QueryArtifacts(ctx context.Context, query ArtifactQuery) (*ArtifactQueryResult, error)
	DeleteAllProjections(ctx context.Context) error

	UpsertArtifactLinks(ctx context.Context, sourcePath string, links []ArtifactLink, sourceCommit string) error
	DeleteArtifactLinks(ctx context.Context, sourcePath string) error
	QueryArtifactLinks(ctx context.Context, sourcePath string) ([]ArtifactLink, error)
	QueryArtifactLinksByTarget(ctx context.Context, targetPath string) ([]ArtifactLink, error)

	UpsertExecutionProjection(ctx context.Context, proj *ExecutionProjection) error
	GetExecutionProjection(ctx context.Context, taskPath string) (*ExecutionProjection, error)
	QueryExecutionProjections(ctx context.Context, query ExecutionProjectionQuery) ([]ExecutionProjection, error)
	DeleteExecutionProjection(ctx context.Context, taskPath string) error
}

// WorkflowProjectionStore covers projected workflow definitions.
type WorkflowProjectionStore interface {
	UpsertWorkflowProjection(ctx context.Context, proj *WorkflowProjection) error
	DeleteWorkflowProjection(ctx context.Context, workflowPath string) error
	GetWorkflowProjection(ctx context.Context, workflowPath string) (*WorkflowProjection, error)
	ListActiveWorkflowProjections(ctx context.Context) ([]WorkflowProjection, error)
}

// SyncStateStore covers projection-sync progress.
type SyncStateStore interface {
	GetSyncState(ctx context.Context) (*SyncState, error)
	UpdateSyncState(ctx context.Context, state *SyncState) error
}

// AuthStore covers Actor + Token persistence — the surface used by the
// auth service, the bootstrap-admin path, and the gateway's actor
// CRUD.
type AuthStore interface {
	GetActor(ctx context.Context, actorID string) (*domain.Actor, error)
	CreateActor(ctx context.Context, actor *domain.Actor) error
	UpdateActor(ctx context.Context, actor *domain.Actor) error
	ListActors(ctx context.Context) ([]domain.Actor, error)
	ListActorsByStatus(ctx context.Context, status domain.ActorStatus) ([]domain.Actor, error)

	GetActorByTokenHash(ctx context.Context, tokenHash string) (*domain.Actor, *domain.Token, error)
	CreateToken(ctx context.Context, record *TokenRecord) error
	RevokeToken(ctx context.Context, tokenID string) error
	ListTokensByActor(ctx context.Context, actorID string) ([]domain.Token, error)
}

// AssignmentStore covers Assignment persistence.
type AssignmentStore interface {
	CreateAssignment(ctx context.Context, a *domain.Assignment) error
	UpdateAssignmentStatus(ctx context.Context, assignmentID string, status domain.AssignmentStatus, respondedAt *time.Time) error
	GetAssignment(ctx context.Context, assignmentID string) (*domain.Assignment, error)
	ListAssignmentsByActor(ctx context.Context, actorID string, status *domain.AssignmentStatus) ([]domain.Assignment, error)
	ListExpiredAssignments(ctx context.Context, before time.Time) ([]domain.Assignment, error)
}

// SkillStore covers Skill records + actor↔skill associations.
type SkillStore interface {
	CreateSkill(ctx context.Context, skill *domain.Skill) error
	GetSkill(ctx context.Context, skillID string) (*domain.Skill, error)
	UpdateSkill(ctx context.Context, skill *domain.Skill) error
	ListSkills(ctx context.Context) ([]domain.Skill, error)
	ListSkillsByCategory(ctx context.Context, category string) ([]domain.Skill, error)

	AddSkillToActor(ctx context.Context, actorID, skillID string) error
	RemoveSkillFromActor(ctx context.Context, actorID, skillID string) error
	ListActorSkills(ctx context.Context, actorID string) ([]domain.Skill, error)
	ListActorsBySkills(ctx context.Context, skillNames []string) ([]domain.Actor, error)
}

// RepositoryStore covers RepositoryBinding persistence (ADR-013).
type RepositoryStore interface {
	CreateRepositoryBinding(ctx context.Context, b *RepositoryBinding) error
	GetRepositoryBinding(ctx context.Context, workspaceID, repositoryID string) (*RepositoryBinding, error)
	GetActiveRepositoryBinding(ctx context.Context, workspaceID, repositoryID string) (*RepositoryBinding, error)
	UpdateRepositoryBinding(ctx context.Context, b *RepositoryBinding) error
	ListRepositoryBindings(ctx context.Context, workspaceID string) ([]RepositoryBinding, error)
	ListActiveRepositoryBindings(ctx context.Context, workspaceID string) ([]RepositoryBinding, error)
	DeactivateRepositoryBinding(ctx context.Context, workspaceID, repositoryID string) error
}

// BranchProtectionStore covers branch-protection rule projections
// (ADR-009).
type BranchProtectionStore interface {
	UpsertBranchProtectionRules(ctx context.Context, rules []BranchProtectionRuleProjection, sourceCommit string) error
	ListBranchProtectionRules(ctx context.Context) ([]BranchProtectionRuleProjection, error)
}

// DiscussionStore covers thread + comment persistence.
type DiscussionStore interface {
	CreateThread(ctx context.Context, thread *domain.DiscussionThread) error
	GetThread(ctx context.Context, threadID string) (*domain.DiscussionThread, error)
	ListThreads(ctx context.Context, anchorType domain.AnchorType, anchorID string) ([]domain.DiscussionThread, error)
	UpdateThread(ctx context.Context, thread *domain.DiscussionThread) error
	CreateComment(ctx context.Context, comment *domain.Comment) error
	ListComments(ctx context.Context, threadID string) ([]domain.Comment, error)
	HasOpenThreads(ctx context.Context, anchorType domain.AnchorType, anchorID string) (bool, error)
}

// DeliveryStore covers the event delivery queue, delivery history, and
// the source-of-truth event log read by the SSE/webhook fan-out.
type DeliveryStore interface {
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
}

// SubscriptionStore covers EventSubscription persistence.
type SubscriptionStore interface {
	CreateSubscription(ctx context.Context, sub *EventSubscription) error
	GetSubscription(ctx context.Context, subscriptionID string) (*EventSubscription, error)
	UpdateSubscription(ctx context.Context, sub *EventSubscription) error
	DeleteSubscription(ctx context.Context, subscriptionID string) error
	ListSubscriptions(ctx context.Context, workspaceID string) ([]EventSubscription, error)
	ListActiveSubscriptionsByEventType(ctx context.Context, eventType string) ([]EventSubscription, error)
}
