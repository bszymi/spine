package gateway_test

import (
	"context"
	"time"

	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/adapters/store"
)

// Per-role no-op stubs for gateway tests (INIT-022 EPIC-001 TASK-029).
//
// Gateway test fakes previously embedded the kitchen-sink store.Store
// interface — any unimplemented method panicked at runtime via nil
// dispatch, and adding a method to store.Store did not break tests.
// These per-role stubs compose into a typed minimal store: each fake
// embeds the role stubs it needs, then overrides only the methods that
// matter for the handler under test. Adding a method to a role
// interface now fails the var _ store.X = stubX{} assertion below,
// catching the omission at build time rather than at the first run
// that happens to exercise that method.
//
// Mirrors cmd/spine/stubstore_test.go. The per-role pattern is repeated
// because Go test packages are not shared across module boundaries; the
// gateway package's own internal-test stub (handlers_tokens_test.go's
// tokenStubStore) implements store.AuthStore directly instead because
// it's the only Store-touching fake in package gateway.

// stubSystem satisfies store.SystemStore with no-ops.
type stubSystem struct{}

func (stubSystem) WithTx(_ context.Context, fn func(tx store.Tx) error) error {
	return fn(stubTx{})
}
func (stubSystem) Ping(_ context.Context) error                      { return nil }
func (stubSystem) ApplyMigrations(_ context.Context, _ string) error { return nil }
func (stubSystem) IsMigrationApplied(_ context.Context, _ string) (bool, error) {
	return false, nil
}
func (stubSystem) Close() {}

var _ store.SystemStore = stubSystem{}

// stubTx is the no-op transaction handed to WithTx callers. Fake
// stores that need transactional semantics override WithTx to supply
// their own Tx implementation.
type stubTx struct{}

func (stubTx) CreateRun(_ context.Context, _ *domain.Run) error                      { return nil }
func (stubTx) UpdateRunStatus(_ context.Context, _ string, _ domain.RunStatus) error { return nil }
func (stubTx) TransitionRunStatus(_ context.Context, _ string, _, _ domain.RunStatus) (bool, error) {
	return true, nil
}
func (stubTx) CreateStepExecution(_ context.Context, _ *domain.StepExecution) error { return nil }
func (stubTx) UpdateStepExecution(_ context.Context, _ *domain.StepExecution) error { return nil }

var _ store.Tx = stubTx{}

// stubRunStore satisfies store.RunStore with no-ops.
type stubRunStore struct{}

func (stubRunStore) CreateRun(_ context.Context, _ *domain.Run) error { return nil }
func (stubRunStore) GetRun(_ context.Context, _ string) (*domain.Run, error) {
	return nil, nil
}
func (stubRunStore) UpdateRunStatus(_ context.Context, _ string, _ domain.RunStatus) error {
	return nil
}
func (stubRunStore) UpdateRunStatusAt(_ context.Context, _ string, _ domain.RunStatus, _ time.Time) error {
	return nil
}
func (stubRunStore) TransitionRunStatus(_ context.Context, _ string, _, _ domain.RunStatus) (bool, error) {
	return false, nil
}
func (stubRunStore) UpdateCurrentStep(_ context.Context, _, _ string) error { return nil }
func (stubRunStore) SetCommitMeta(_ context.Context, _ string, _ map[string]string) error {
	return nil
}
func (stubRunStore) ListRunsByTask(_ context.Context, _ string) ([]domain.Run, error) {
	return nil, nil
}
func (stubRunStore) ListRunsByStatus(_ context.Context, _ domain.RunStatus) ([]domain.Run, error) {
	return nil, nil
}
func (stubRunStore) ListStaleActiveRuns(_ context.Context, _ time.Time) ([]domain.Run, error) {
	return nil, nil
}
func (stubRunStore) ListTimedOutRuns(_ context.Context, _ time.Time) ([]domain.Run, error) {
	return nil, nil
}
func (stubRunStore) CreateStepExecution(_ context.Context, _ *domain.StepExecution) error {
	return nil
}
func (stubRunStore) GetStepExecution(_ context.Context, _ string) (*domain.StepExecution, error) {
	return nil, nil
}
func (stubRunStore) UpdateStepExecution(_ context.Context, _ *domain.StepExecution) error {
	return nil
}
func (stubRunStore) ListStepExecutionsByRun(_ context.Context, _ string) ([]domain.StepExecution, error) {
	return nil, nil
}
func (stubRunStore) ListActiveStepExecutions(_ context.Context) ([]domain.StepExecution, error) {
	return nil, nil
}
func (stubRunStore) UpsertRepositoryMergeOutcome(_ context.Context, _ *domain.RepositoryMergeOutcome) error {
	return nil
}
func (stubRunStore) GetRepositoryMergeOutcome(_ context.Context, _, _ string) (*domain.RepositoryMergeOutcome, error) {
	return nil, nil
}
func (stubRunStore) ListRepositoryMergeOutcomes(_ context.Context, _ string) ([]domain.RepositoryMergeOutcome, error) {
	return nil, nil
}

var _ store.RunStore = stubRunStore{}

// stubBranchStore satisfies store.BranchStore with no-ops.
type stubBranchStore struct{}

func (stubBranchStore) CreateDivergenceContext(_ context.Context, _ *domain.DivergenceContext) error {
	return nil
}
func (stubBranchStore) UpdateDivergenceContext(_ context.Context, _ *domain.DivergenceContext) error {
	return nil
}
func (stubBranchStore) GetDivergenceContext(_ context.Context, _ string) (*domain.DivergenceContext, error) {
	return nil, nil
}
func (stubBranchStore) CreateBranch(_ context.Context, _ *domain.Branch) error { return nil }
func (stubBranchStore) UpdateBranch(_ context.Context, _ *domain.Branch) error { return nil }
func (stubBranchStore) GetBranch(_ context.Context, _ string) (*domain.Branch, error) {
	return nil, nil
}
func (stubBranchStore) ListBranchesByDivergence(_ context.Context, _ string) ([]domain.Branch, error) {
	return nil, nil
}

var _ store.BranchStore = stubBranchStore{}

// stubArtifactStore satisfies store.ArtifactStore with no-ops.
type stubArtifactStore struct{}

func (stubArtifactStore) UpsertArtifactProjection(_ context.Context, _ *store.ArtifactProjection) error {
	return nil
}
func (stubArtifactStore) DeleteArtifactProjection(_ context.Context, _ string) error { return nil }
func (stubArtifactStore) GetArtifactProjection(_ context.Context, _ string) (*store.ArtifactProjection, error) {
	return nil, nil
}
func (stubArtifactStore) QueryArtifacts(_ context.Context, _ store.ArtifactQuery) (*store.ArtifactQueryResult, error) {
	return nil, nil
}
func (stubArtifactStore) DeleteAllProjections(_ context.Context) error { return nil }
func (stubArtifactStore) UpsertArtifactLinks(_ context.Context, _ string, _ []store.ArtifactLink, _ string) error {
	return nil
}
func (stubArtifactStore) DeleteArtifactLinks(_ context.Context, _ string) error { return nil }
func (stubArtifactStore) QueryArtifactLinks(_ context.Context, _ string) ([]store.ArtifactLink, error) {
	return nil, nil
}
func (stubArtifactStore) QueryArtifactLinksByTarget(_ context.Context, _ string) ([]store.ArtifactLink, error) {
	return nil, nil
}
func (stubArtifactStore) UpsertExecutionProjection(_ context.Context, _ *store.ExecutionProjection) error {
	return nil
}
func (stubArtifactStore) GetExecutionProjection(_ context.Context, _ string) (*store.ExecutionProjection, error) {
	return nil, nil
}
func (stubArtifactStore) QueryExecutionProjections(_ context.Context, _ store.ExecutionProjectionQuery) ([]store.ExecutionProjection, error) {
	return nil, nil
}
func (stubArtifactStore) DeleteExecutionProjection(_ context.Context, _ string) error {
	return nil
}

var _ store.ArtifactStore = stubArtifactStore{}

// stubWorkflowProjectionStore satisfies store.WorkflowProjectionStore with no-ops.
type stubWorkflowProjectionStore struct{}

func (stubWorkflowProjectionStore) UpsertWorkflowProjection(_ context.Context, _ *store.WorkflowProjection) error {
	return nil
}
func (stubWorkflowProjectionStore) DeleteWorkflowProjection(_ context.Context, _ string) error {
	return nil
}
func (stubWorkflowProjectionStore) GetWorkflowProjection(_ context.Context, _ string) (*store.WorkflowProjection, error) {
	return nil, nil
}
func (stubWorkflowProjectionStore) ListActiveWorkflowProjections(_ context.Context) ([]store.WorkflowProjection, error) {
	return nil, nil
}

var _ store.WorkflowProjectionStore = stubWorkflowProjectionStore{}

// stubSyncStateStore satisfies store.SyncStateStore with no-ops.
type stubSyncStateStore struct{}

func (stubSyncStateStore) GetSyncState(_ context.Context) (*store.SyncState, error) {
	return nil, nil
}
func (stubSyncStateStore) UpdateSyncState(_ context.Context, _ *store.SyncState) error {
	return nil
}

var _ store.SyncStateStore = stubSyncStateStore{}

// stubAuthStore satisfies store.AuthStore with no-ops.
type stubAuthStore struct{}

func (stubAuthStore) GetActor(_ context.Context, _ string) (*domain.Actor, error) {
	return nil, nil
}
func (stubAuthStore) CreateActor(_ context.Context, _ *domain.Actor) error { return nil }
func (stubAuthStore) UpdateActor(_ context.Context, _ *domain.Actor) error { return nil }
func (stubAuthStore) ListActors(_ context.Context) ([]domain.Actor, error) { return nil, nil }
func (stubAuthStore) ListActorsByStatus(_ context.Context, _ domain.ActorStatus) ([]domain.Actor, error) {
	return nil, nil
}
func (stubAuthStore) GetActorByTokenHash(_ context.Context, _ string) (*domain.Actor, *domain.Token, error) {
	return nil, nil, nil
}
func (stubAuthStore) CreateToken(_ context.Context, _ *store.TokenRecord) error { return nil }
func (stubAuthStore) RevokeToken(_ context.Context, _ string) error             { return nil }
func (stubAuthStore) ListTokensByActor(_ context.Context, _ string) ([]domain.Token, error) {
	return nil, nil
}

var _ store.AuthStore = stubAuthStore{}

// stubAssignmentStore satisfies store.AssignmentStore with no-ops.
type stubAssignmentStore struct{}

func (stubAssignmentStore) CreateAssignment(_ context.Context, _ *domain.Assignment) error {
	return nil
}
func (stubAssignmentStore) UpdateAssignmentStatus(_ context.Context, _ string, _ domain.AssignmentStatus, _ *time.Time) error {
	return nil
}
func (stubAssignmentStore) GetAssignment(_ context.Context, _ string) (*domain.Assignment, error) {
	return nil, nil
}
func (stubAssignmentStore) ListAssignmentsByActor(_ context.Context, _ string, _ *domain.AssignmentStatus) ([]domain.Assignment, error) {
	return nil, nil
}
func (stubAssignmentStore) ListExpiredAssignments(_ context.Context, _ time.Time) ([]domain.Assignment, error) {
	return nil, nil
}

var _ store.AssignmentStore = stubAssignmentStore{}

// stubSkillStore satisfies store.SkillStore with no-ops.
type stubSkillStore struct{}

func (stubSkillStore) CreateSkill(_ context.Context, _ *domain.Skill) error { return nil }
func (stubSkillStore) GetSkill(_ context.Context, _ string) (*domain.Skill, error) {
	return nil, nil
}
func (stubSkillStore) UpdateSkill(_ context.Context, _ *domain.Skill) error { return nil }
func (stubSkillStore) ListSkills(_ context.Context) ([]domain.Skill, error) { return nil, nil }
func (stubSkillStore) ListSkillsByCategory(_ context.Context, _ string) ([]domain.Skill, error) {
	return nil, nil
}
func (stubSkillStore) AddSkillToActor(_ context.Context, _, _ string) error      { return nil }
func (stubSkillStore) RemoveSkillFromActor(_ context.Context, _, _ string) error { return nil }
func (stubSkillStore) ListActorSkills(_ context.Context, _ string) ([]domain.Skill, error) {
	return nil, nil
}
func (stubSkillStore) ListActorsBySkills(_ context.Context, _ []string) ([]domain.Actor, error) {
	return nil, nil
}

var _ store.SkillStore = stubSkillStore{}

// stubRepositoryStore satisfies store.RepositoryStore with no-ops.
type stubRepositoryStore struct{}

func (stubRepositoryStore) CreateRepositoryBinding(_ context.Context, _ *store.RepositoryBinding) error {
	return nil
}
func (stubRepositoryStore) GetRepositoryBinding(_ context.Context, _, _ string) (*store.RepositoryBinding, error) {
	return nil, nil
}
func (stubRepositoryStore) GetActiveRepositoryBinding(_ context.Context, _, _ string) (*store.RepositoryBinding, error) {
	return nil, nil
}
func (stubRepositoryStore) UpdateRepositoryBinding(_ context.Context, _ *store.RepositoryBinding) error {
	return nil
}
func (stubRepositoryStore) ListRepositoryBindings(_ context.Context, _ string) ([]store.RepositoryBinding, error) {
	return nil, nil
}
func (stubRepositoryStore) ListActiveRepositoryBindings(_ context.Context, _ string) ([]store.RepositoryBinding, error) {
	return nil, nil
}
func (stubRepositoryStore) DeactivateRepositoryBinding(_ context.Context, _, _ string) error {
	return nil
}

var _ store.RepositoryStore = stubRepositoryStore{}

// stubBranchProtectionStore satisfies store.BranchProtectionStore with no-ops.
type stubBranchProtectionStore struct{}

func (stubBranchProtectionStore) UpsertBranchProtectionRules(_ context.Context, _ []store.BranchProtectionRuleProjection, _ string) error {
	return nil
}
func (stubBranchProtectionStore) ListBranchProtectionRules(_ context.Context) ([]store.BranchProtectionRuleProjection, error) {
	return nil, nil
}

var _ store.BranchProtectionStore = stubBranchProtectionStore{}

// stubDiscussionStore satisfies store.DiscussionStore with no-ops.
type stubDiscussionStore struct{}

func (stubDiscussionStore) CreateThread(_ context.Context, _ *domain.DiscussionThread) error {
	return nil
}
func (stubDiscussionStore) GetThread(_ context.Context, _ string) (*domain.DiscussionThread, error) {
	return nil, nil
}
func (stubDiscussionStore) ListThreads(_ context.Context, _ domain.AnchorType, _ string) ([]domain.DiscussionThread, error) {
	return nil, nil
}
func (stubDiscussionStore) UpdateThread(_ context.Context, _ *domain.DiscussionThread) error {
	return nil
}
func (stubDiscussionStore) CreateComment(_ context.Context, _ *domain.Comment) error {
	return nil
}
func (stubDiscussionStore) ListComments(_ context.Context, _ string) ([]domain.Comment, error) {
	return nil, nil
}
func (stubDiscussionStore) HasOpenThreads(_ context.Context, _ domain.AnchorType, _ string) (bool, error) {
	return false, nil
}

var _ store.DiscussionStore = stubDiscussionStore{}

// stubDeliveryStore satisfies store.DeliveryStore with no-ops.
type stubDeliveryStore struct{}

func (stubDeliveryStore) EnqueueDelivery(_ context.Context, _ *store.DeliveryEntry) error {
	return nil
}
func (stubDeliveryStore) ClaimDeliveries(_ context.Context, _ int) ([]store.DeliveryEntry, error) {
	return nil, nil
}
func (stubDeliveryStore) UpdateDeliveryStatus(_ context.Context, _, _ string, _ string, _ *time.Time) error {
	return nil
}
func (stubDeliveryStore) MarkDelivered(_ context.Context, _ string) error { return nil }
func (stubDeliveryStore) LogDeliveryAttempt(_ context.Context, _ *store.DeliveryLogEntry) error {
	return nil
}
func (stubDeliveryStore) ListDeliveryHistory(_ context.Context, _ store.DeliveryHistoryQuery) ([]store.DeliveryLogEntry, error) {
	return nil, nil
}
func (stubDeliveryStore) GetDelivery(_ context.Context, _ string) (*store.DeliveryEntry, error) {
	return nil, nil
}
func (stubDeliveryStore) ListDeliveries(_ context.Context, _ string, _ string, _ int) ([]store.DeliveryEntry, error) {
	return nil, nil
}
func (stubDeliveryStore) GetDeliveryStats(_ context.Context, _ string) (*store.DeliveryStats, error) {
	return nil, nil
}
func (stubDeliveryStore) WriteEventLog(_ context.Context, _ *store.EventLogEntry) error {
	return nil
}
func (stubDeliveryStore) ListEventsAfter(_ context.Context, _ string, _ []string, _ int) ([]store.EventLogEntry, error) {
	return nil, nil
}
func (stubDeliveryStore) DeleteExpiredDeliveries(_ context.Context, _ time.Time) (int64, error) {
	return 0, nil
}

var _ store.DeliveryStore = stubDeliveryStore{}

// stubSubscriptionStore satisfies store.SubscriptionStore with no-ops.
type stubSubscriptionStore struct{}

func (stubSubscriptionStore) CreateSubscription(_ context.Context, _ *store.EventSubscription) error {
	return nil
}
func (stubSubscriptionStore) GetSubscription(_ context.Context, _ string) (*store.EventSubscription, error) {
	return nil, nil
}
func (stubSubscriptionStore) UpdateSubscription(_ context.Context, _ *store.EventSubscription) error {
	return nil
}
func (stubSubscriptionStore) DeleteSubscription(_ context.Context, _ string) error {
	return nil
}
func (stubSubscriptionStore) ListSubscriptions(_ context.Context, _ string) ([]store.EventSubscription, error) {
	return nil, nil
}
func (stubSubscriptionStore) ListActiveSubscriptionsByEventType(_ context.Context, _ string) ([]store.EventSubscription, error) {
	return nil, nil
}

var _ store.SubscriptionStore = stubSubscriptionStore{}

// stubRoleStore composes every per-role no-op stub. Test fakes that
// need to satisfy the full store.Store union embed this to get all
// roles without listing each one; fakes that only depend on one or two
// roles can embed just those role stubs instead.
type stubRoleStore struct {
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

var _ store.Store = stubRoleStore{}
