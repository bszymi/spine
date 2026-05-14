package main

import (
	"context"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/store"
)

// The smoke-test stub store is composed of per-role stubs. Each role
// stub is a no-op implementation of one store role interface
// (INIT-022 EPIC-001 TASK-010): future tests that need only a single
// role can embed just that stub instead of the union, and adding a
// method to any one role does not require updating every test that
// only depends on a different role.
//
// stubStore zero-value satisfies store.Store via embedding. Handler
// calls that dereference its zero returns will 500 via
// recoveryMiddleware, which is fine: the smoke test only asserts that
// no handler 503s with "service not configured".

// stubStore is the union no-op store used by the serve-startup smoke
// test.
type stubStore struct {
	stubSystem
	stubRunStore
	stubBranchStore
	stubArtifactStore
	stubWorkflowProjectionStore
	stubSyncStateStore
	stubAuthStore
	stubAssignmentStore
	stubSkillStore
	stubRepositoryStore
	stubBranchProtectionStore
	stubDiscussionStore
	stubDeliveryStore
	stubSubscriptionStore
}

// GetActorByTokenHash is overridden on the union so the smoke test
// returns an authenticated admin actor instead of (nil, nil, nil) —
// authMiddleware passes and the probe exercises handler-level wiring.
func (stubStore) GetActorByTokenHash(ctx context.Context, tokenHash string) (*domain.Actor, *domain.Token, error) {
	return &domain.Actor{
			ActorID: "smoke-test-actor",
			Type:    domain.ActorTypeHuman,
			Name:    "smoke",
			Role:    domain.RoleAdmin,
			Status:  domain.ActorStatusActive,
		}, &domain.Token{
			TokenID: "tok_smoke",
			ActorID: "smoke-test-actor",
		}, nil
}

var _ store.Store = stubStore{}

// stubSystem satisfies store.SystemStore.
type stubSystem struct{}

func (stubSystem) WithTx(ctx context.Context, fn func(tx store.Tx) error) error {
	return nil
}
func (stubSystem) Ping(ctx context.Context) error                                  { return nil }
func (stubSystem) ApplyMigrations(ctx context.Context, migrationsDir string) error { return nil }
func (stubSystem) IsMigrationApplied(ctx context.Context, version string) (bool, error) {
	return false, nil
}
func (stubSystem) Close() {}

var _ store.SystemStore = stubSystem{}

// stubRunStore satisfies store.RunStore.
type stubRunStore struct{}

func (stubRunStore) CreateRun(ctx context.Context, run *domain.Run) error { return nil }
func (stubRunStore) GetRun(ctx context.Context, runID string) (*domain.Run, error) {
	return nil, nil
}
func (stubRunStore) UpdateRunStatus(ctx context.Context, runID string, status domain.RunStatus) error {
	return nil
}
func (stubRunStore) UpdateRunStatusAt(ctx context.Context, runID string, status domain.RunStatus, completedAt time.Time) error {
	return nil
}
func (stubRunStore) TransitionRunStatus(ctx context.Context, runID string, fromStatus, toStatus domain.RunStatus) (bool, error) {
	return false, nil
}
func (stubRunStore) UpdateCurrentStep(ctx context.Context, runID, stepID string) error { return nil }
func (stubRunStore) SetCommitMeta(ctx context.Context, runID string, meta map[string]string) error {
	return nil
}
func (stubRunStore) ListRunsByTask(ctx context.Context, taskPath string) ([]domain.Run, error) {
	return nil, nil
}
func (stubRunStore) ListRunsByStatus(ctx context.Context, status domain.RunStatus) ([]domain.Run, error) {
	return nil, nil
}
func (stubRunStore) ListStaleActiveRuns(ctx context.Context, noActivitySince time.Time) ([]domain.Run, error) {
	return nil, nil
}
func (stubRunStore) ListTimedOutRuns(ctx context.Context, now time.Time) ([]domain.Run, error) {
	return nil, nil
}
func (stubRunStore) CreateStepExecution(ctx context.Context, exec *domain.StepExecution) error {
	return nil
}
func (stubRunStore) GetStepExecution(ctx context.Context, executionID string) (*domain.StepExecution, error) {
	return nil, nil
}
func (stubRunStore) UpdateStepExecution(ctx context.Context, exec *domain.StepExecution) error {
	return nil
}
func (stubRunStore) ListStepExecutionsByRun(ctx context.Context, runID string) ([]domain.StepExecution, error) {
	return nil, nil
}
func (stubRunStore) ListActiveStepExecutions(ctx context.Context) ([]domain.StepExecution, error) {
	return nil, nil
}
func (stubRunStore) UpsertRepositoryMergeOutcome(ctx context.Context, outcome *domain.RepositoryMergeOutcome) error {
	return nil
}
func (stubRunStore) GetRepositoryMergeOutcome(ctx context.Context, runID, repositoryID string) (*domain.RepositoryMergeOutcome, error) {
	return nil, nil
}
func (stubRunStore) ListRepositoryMergeOutcomes(ctx context.Context, runID string) ([]domain.RepositoryMergeOutcome, error) {
	return nil, nil
}

var _ store.RunStore = stubRunStore{}

// stubBranchStore satisfies store.BranchStore.
type stubBranchStore struct{}

func (stubBranchStore) CreateDivergenceContext(ctx context.Context, div *domain.DivergenceContext) error {
	return nil
}
func (stubBranchStore) UpdateDivergenceContext(ctx context.Context, div *domain.DivergenceContext) error {
	return nil
}
func (stubBranchStore) GetDivergenceContext(ctx context.Context, divergenceID string) (*domain.DivergenceContext, error) {
	return nil, nil
}
func (stubBranchStore) CreateBranch(ctx context.Context, branch *domain.Branch) error { return nil }
func (stubBranchStore) UpdateBranch(ctx context.Context, branch *domain.Branch) error { return nil }
func (stubBranchStore) GetBranch(ctx context.Context, branchID string) (*domain.Branch, error) {
	return nil, nil
}
func (stubBranchStore) ListBranchesByDivergence(ctx context.Context, divergenceID string) ([]domain.Branch, error) {
	return nil, nil
}

var _ store.BranchStore = stubBranchStore{}

// stubArtifactStore satisfies store.ArtifactStore.
type stubArtifactStore struct{}

func (stubArtifactStore) UpsertArtifactProjection(ctx context.Context, proj *store.ArtifactProjection) error {
	return nil
}
func (stubArtifactStore) DeleteArtifactProjection(ctx context.Context, artifactPath string) error {
	return nil
}
func (stubArtifactStore) GetArtifactProjection(ctx context.Context, artifactPath string) (*store.ArtifactProjection, error) {
	return nil, nil
}
func (stubArtifactStore) QueryArtifacts(ctx context.Context, query store.ArtifactQuery) (*store.ArtifactQueryResult, error) {
	return nil, nil
}
func (stubArtifactStore) DeleteAllProjections(ctx context.Context) error { return nil }
func (stubArtifactStore) UpsertArtifactLinks(ctx context.Context, sourcePath string, links []store.ArtifactLink, sourceCommit string) error {
	return nil
}
func (stubArtifactStore) DeleteArtifactLinks(ctx context.Context, sourcePath string) error {
	return nil
}
func (stubArtifactStore) QueryArtifactLinks(ctx context.Context, sourcePath string) ([]store.ArtifactLink, error) {
	return nil, nil
}
func (stubArtifactStore) QueryArtifactLinksByTarget(ctx context.Context, targetPath string) ([]store.ArtifactLink, error) {
	return nil, nil
}
func (stubArtifactStore) UpsertExecutionProjection(ctx context.Context, proj *store.ExecutionProjection) error {
	return nil
}
func (stubArtifactStore) GetExecutionProjection(ctx context.Context, taskPath string) (*store.ExecutionProjection, error) {
	return nil, nil
}
func (stubArtifactStore) QueryExecutionProjections(ctx context.Context, query store.ExecutionProjectionQuery) ([]store.ExecutionProjection, error) {
	return nil, nil
}
func (stubArtifactStore) DeleteExecutionProjection(ctx context.Context, taskPath string) error {
	return nil
}

var _ store.ArtifactStore = stubArtifactStore{}

// stubWorkflowProjectionStore satisfies store.WorkflowProjectionStore.
type stubWorkflowProjectionStore struct{}

func (stubWorkflowProjectionStore) UpsertWorkflowProjection(ctx context.Context, proj *store.WorkflowProjection) error {
	return nil
}
func (stubWorkflowProjectionStore) DeleteWorkflowProjection(ctx context.Context, workflowPath string) error {
	return nil
}
func (stubWorkflowProjectionStore) GetWorkflowProjection(ctx context.Context, workflowPath string) (*store.WorkflowProjection, error) {
	return nil, nil
}
func (stubWorkflowProjectionStore) ListActiveWorkflowProjections(ctx context.Context) ([]store.WorkflowProjection, error) {
	return nil, nil
}

var _ store.WorkflowProjectionStore = stubWorkflowProjectionStore{}

// stubSyncStateStore satisfies store.SyncStateStore.
type stubSyncStateStore struct{}

func (stubSyncStateStore) GetSyncState(ctx context.Context) (*store.SyncState, error) {
	return nil, nil
}
func (stubSyncStateStore) UpdateSyncState(ctx context.Context, state *store.SyncState) error {
	return nil
}

var _ store.SyncStateStore = stubSyncStateStore{}

// stubAuthStore satisfies store.AuthStore.
type stubAuthStore struct{}

func (stubAuthStore) GetActor(ctx context.Context, actorID string) (*domain.Actor, error) {
	return nil, nil
}
func (stubAuthStore) CreateActor(ctx context.Context, actor *domain.Actor) error { return nil }
func (stubAuthStore) UpdateActor(ctx context.Context, actor *domain.Actor) error { return nil }
func (stubAuthStore) ListActors(ctx context.Context) ([]domain.Actor, error)     { return nil, nil }
func (stubAuthStore) ListActorsByStatus(ctx context.Context, status domain.ActorStatus) ([]domain.Actor, error) {
	return nil, nil
}
func (stubAuthStore) GetActorByTokenHash(ctx context.Context, tokenHash string) (*domain.Actor, *domain.Token, error) {
	return nil, nil, nil
}
func (stubAuthStore) CreateToken(ctx context.Context, record *store.TokenRecord) error { return nil }
func (stubAuthStore) RevokeToken(ctx context.Context, tokenID string) error            { return nil }
func (stubAuthStore) ListTokensByActor(ctx context.Context, actorID string) ([]domain.Token, error) {
	return nil, nil
}

var _ store.AuthStore = stubAuthStore{}

// stubAssignmentStore satisfies store.AssignmentStore.
type stubAssignmentStore struct{}

func (stubAssignmentStore) CreateAssignment(ctx context.Context, a *domain.Assignment) error {
	return nil
}
func (stubAssignmentStore) UpdateAssignmentStatus(ctx context.Context, assignmentID string, status domain.AssignmentStatus, respondedAt *time.Time) error {
	return nil
}
func (stubAssignmentStore) GetAssignment(ctx context.Context, assignmentID string) (*domain.Assignment, error) {
	return nil, nil
}
func (stubAssignmentStore) ListAssignmentsByActor(ctx context.Context, actorID string, status *domain.AssignmentStatus) ([]domain.Assignment, error) {
	return nil, nil
}
func (stubAssignmentStore) ListExpiredAssignments(ctx context.Context, before time.Time) ([]domain.Assignment, error) {
	return nil, nil
}

var _ store.AssignmentStore = stubAssignmentStore{}

// stubSkillStore satisfies store.SkillStore.
type stubSkillStore struct{}

func (stubSkillStore) CreateSkill(ctx context.Context, skill *domain.Skill) error { return nil }
func (stubSkillStore) GetSkill(ctx context.Context, skillID string) (*domain.Skill, error) {
	return nil, nil
}
func (stubSkillStore) UpdateSkill(ctx context.Context, skill *domain.Skill) error { return nil }
func (stubSkillStore) ListSkills(ctx context.Context) ([]domain.Skill, error)     { return nil, nil }
func (stubSkillStore) ListSkillsByCategory(ctx context.Context, category string) ([]domain.Skill, error) {
	return nil, nil
}
func (stubSkillStore) AddSkillToActor(ctx context.Context, actorID, skillID string) error {
	return nil
}
func (stubSkillStore) RemoveSkillFromActor(ctx context.Context, actorID, skillID string) error {
	return nil
}
func (stubSkillStore) ListActorSkills(ctx context.Context, actorID string) ([]domain.Skill, error) {
	return nil, nil
}
func (stubSkillStore) ListActorsBySkills(ctx context.Context, skillNames []string) ([]domain.Actor, error) {
	return nil, nil
}

var _ store.SkillStore = stubSkillStore{}

// stubRepositoryStore satisfies store.RepositoryStore.
type stubRepositoryStore struct{}

func (stubRepositoryStore) CreateRepositoryBinding(ctx context.Context, b *store.RepositoryBinding) error {
	return nil
}
func (stubRepositoryStore) GetRepositoryBinding(ctx context.Context, workspaceID, repositoryID string) (*store.RepositoryBinding, error) {
	return nil, nil
}
func (stubRepositoryStore) GetActiveRepositoryBinding(ctx context.Context, workspaceID, repositoryID string) (*store.RepositoryBinding, error) {
	return nil, nil
}
func (stubRepositoryStore) UpdateRepositoryBinding(ctx context.Context, b *store.RepositoryBinding) error {
	return nil
}
func (stubRepositoryStore) ListRepositoryBindings(ctx context.Context, workspaceID string) ([]store.RepositoryBinding, error) {
	return nil, nil
}
func (stubRepositoryStore) ListActiveRepositoryBindings(ctx context.Context, workspaceID string) ([]store.RepositoryBinding, error) {
	return nil, nil
}
func (stubRepositoryStore) DeactivateRepositoryBinding(ctx context.Context, workspaceID, repositoryID string) error {
	return nil
}

var _ store.RepositoryStore = stubRepositoryStore{}

// stubBranchProtectionStore satisfies store.BranchProtectionStore.
type stubBranchProtectionStore struct{}

func (stubBranchProtectionStore) UpsertBranchProtectionRules(ctx context.Context, rules []store.BranchProtectionRuleProjection, sourceCommit string) error {
	return nil
}
func (stubBranchProtectionStore) ListBranchProtectionRules(ctx context.Context) ([]store.BranchProtectionRuleProjection, error) {
	return nil, nil
}

var _ store.BranchProtectionStore = stubBranchProtectionStore{}

// stubDiscussionStore satisfies store.DiscussionStore.
type stubDiscussionStore struct{}

func (stubDiscussionStore) CreateThread(ctx context.Context, thread *domain.DiscussionThread) error {
	return nil
}
func (stubDiscussionStore) GetThread(ctx context.Context, threadID string) (*domain.DiscussionThread, error) {
	return nil, nil
}
func (stubDiscussionStore) ListThreads(ctx context.Context, anchorType domain.AnchorType, anchorID string) ([]domain.DiscussionThread, error) {
	return nil, nil
}
func (stubDiscussionStore) UpdateThread(ctx context.Context, thread *domain.DiscussionThread) error {
	return nil
}
func (stubDiscussionStore) CreateComment(ctx context.Context, comment *domain.Comment) error {
	return nil
}
func (stubDiscussionStore) ListComments(ctx context.Context, threadID string) ([]domain.Comment, error) {
	return nil, nil
}
func (stubDiscussionStore) HasOpenThreads(ctx context.Context, anchorType domain.AnchorType, anchorID string) (bool, error) {
	return false, nil
}

var _ store.DiscussionStore = stubDiscussionStore{}

// stubDeliveryStore satisfies store.DeliveryStore.
type stubDeliveryStore struct{}

func (stubDeliveryStore) EnqueueDelivery(ctx context.Context, entry *store.DeliveryEntry) error {
	return nil
}
func (stubDeliveryStore) ClaimDeliveries(ctx context.Context, limit int) ([]store.DeliveryEntry, error) {
	return nil, nil
}
func (stubDeliveryStore) UpdateDeliveryStatus(ctx context.Context, deliveryID, status string, lastError string, nextRetryAt *time.Time) error {
	return nil
}
func (stubDeliveryStore) MarkDelivered(ctx context.Context, deliveryID string) error { return nil }
func (stubDeliveryStore) LogDeliveryAttempt(ctx context.Context, entry *store.DeliveryLogEntry) error {
	return nil
}
func (stubDeliveryStore) ListDeliveryHistory(ctx context.Context, query store.DeliveryHistoryQuery) ([]store.DeliveryLogEntry, error) {
	return nil, nil
}
func (stubDeliveryStore) GetDelivery(ctx context.Context, deliveryID string) (*store.DeliveryEntry, error) {
	return nil, nil
}
func (stubDeliveryStore) ListDeliveries(ctx context.Context, subscriptionID string, status string, limit int) ([]store.DeliveryEntry, error) {
	return nil, nil
}
func (stubDeliveryStore) GetDeliveryStats(ctx context.Context, subscriptionID string) (*store.DeliveryStats, error) {
	return nil, nil
}
func (stubDeliveryStore) WriteEventLog(ctx context.Context, entry *store.EventLogEntry) error {
	return nil
}
func (stubDeliveryStore) ListEventsAfter(ctx context.Context, afterEventID string, eventTypes []string, limit int) ([]store.EventLogEntry, error) {
	return nil, nil
}
func (stubDeliveryStore) DeleteExpiredDeliveries(ctx context.Context, before time.Time) (int64, error) {
	return 0, nil
}

var _ store.DeliveryStore = stubDeliveryStore{}

// stubSubscriptionStore satisfies store.SubscriptionStore.
type stubSubscriptionStore struct{}

func (stubSubscriptionStore) CreateSubscription(ctx context.Context, sub *store.EventSubscription) error {
	return nil
}
func (stubSubscriptionStore) GetSubscription(ctx context.Context, subscriptionID string) (*store.EventSubscription, error) {
	return nil, nil
}
func (stubSubscriptionStore) UpdateSubscription(ctx context.Context, sub *store.EventSubscription) error {
	return nil
}
func (stubSubscriptionStore) DeleteSubscription(ctx context.Context, subscriptionID string) error {
	return nil
}
func (stubSubscriptionStore) ListSubscriptions(ctx context.Context, workspaceID string) ([]store.EventSubscription, error) {
	return nil, nil
}
func (stubSubscriptionStore) ListActiveSubscriptionsByEventType(ctx context.Context, eventType string) ([]store.EventSubscription, error) {
	return nil, nil
}

var _ store.SubscriptionStore = stubSubscriptionStore{}
