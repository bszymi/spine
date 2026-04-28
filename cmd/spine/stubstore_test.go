package main

import (
	"context"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/store"
)

// stubStore is a no-op implementation of store.Store used by the
// serve-startup smoke test. Its only job is to satisfy the interface
// so buildServerConfig wires the full ServerConfig surface without
// requiring a live database. Handler calls that dereference its zero
// returns will 500 via recoveryMiddleware, which is fine: the smoke
// test only asserts that no handler 503s with "service not configured".
type stubStore struct{}

func (stubStore) WithTx(ctx context.Context, fn func(tx store.Tx) error) error {
	return nil
}

func (stubStore) Ping(ctx context.Context) error {
	return nil
}

func (stubStore) CreateRun(ctx context.Context, run *domain.Run) error {
	return nil
}

func (stubStore) GetRun(ctx context.Context, runID string) (*domain.Run, error) {
	return nil, nil
}

func (stubStore) UpdateRunStatus(ctx context.Context, runID string, status domain.RunStatus) error {
	return nil
}

func (stubStore) TransitionRunStatus(ctx context.Context, runID string, fromStatus, toStatus domain.RunStatus) (bool, error) {
	return false, nil
}

func (stubStore) UpdateCurrentStep(ctx context.Context, runID, stepID string) error {
	return nil
}

func (stubStore) SetCommitMeta(ctx context.Context, runID string, meta map[string]string) error {
	return nil
}

func (stubStore) ListRunsByTask(ctx context.Context, taskPath string) ([]domain.Run, error) {
	return nil, nil
}

func (stubStore) CreateStepExecution(ctx context.Context, exec *domain.StepExecution) error {
	return nil
}

func (stubStore) GetStepExecution(ctx context.Context, executionID string) (*domain.StepExecution, error) {
	return nil, nil
}

func (stubStore) UpdateStepExecution(ctx context.Context, exec *domain.StepExecution) error {
	return nil
}

func (stubStore) ListStepExecutionsByRun(ctx context.Context, runID string) ([]domain.StepExecution, error) {
	return nil, nil
}

func (stubStore) UpsertArtifactProjection(ctx context.Context, proj *store.ArtifactProjection) error {
	return nil
}

func (stubStore) DeleteArtifactProjection(ctx context.Context, artifactPath string) error {
	return nil
}

func (stubStore) GetArtifactProjection(ctx context.Context, artifactPath string) (*store.ArtifactProjection, error) {
	return nil, nil
}

func (stubStore) QueryArtifacts(ctx context.Context, query store.ArtifactQuery) (*store.ArtifactQueryResult, error) {
	return nil, nil
}

func (stubStore) DeleteAllProjections(ctx context.Context) error {
	return nil
}

func (stubStore) UpsertArtifactLinks(ctx context.Context, sourcePath string, links []store.ArtifactLink, sourceCommit string) error {
	return nil
}

func (stubStore) DeleteArtifactLinks(ctx context.Context, sourcePath string) error {
	return nil
}

func (stubStore) QueryArtifactLinks(ctx context.Context, sourcePath string) ([]store.ArtifactLink, error) {
	return nil, nil
}

func (stubStore) QueryArtifactLinksByTarget(ctx context.Context, targetPath string) ([]store.ArtifactLink, error) {
	return nil, nil
}

func (stubStore) GetActor(ctx context.Context, actorID string) (*domain.Actor, error) {
	return nil, nil
}

func (stubStore) CreateActor(ctx context.Context, actor *domain.Actor) error {
	return nil
}

func (stubStore) UpdateActor(ctx context.Context, actor *domain.Actor) error {
	return nil
}

func (stubStore) ListActors(ctx context.Context) ([]domain.Actor, error) {
	return nil, nil
}

func (stubStore) ListActorsByStatus(ctx context.Context, status domain.ActorStatus) ([]domain.Actor, error) {
	return nil, nil
}

func (stubStore) GetActorByTokenHash(ctx context.Context, tokenHash string) (*domain.Actor, *domain.Token, error) {
	// Smoke test always returns a valid admin actor so authMiddleware
	// passes and the probe exercises handler-level wiring.
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

func (stubStore) CreateToken(ctx context.Context, record *store.TokenRecord) error {
	return nil
}

func (stubStore) RevokeToken(ctx context.Context, tokenID string) error {
	return nil
}

func (stubStore) ListTokensByActor(ctx context.Context, actorID string) ([]domain.Token, error) {
	return nil, nil
}

func (stubStore) CreateDivergenceContext(ctx context.Context, div *domain.DivergenceContext) error {
	return nil
}

func (stubStore) UpdateDivergenceContext(ctx context.Context, div *domain.DivergenceContext) error {
	return nil
}

func (stubStore) GetDivergenceContext(ctx context.Context, divergenceID string) (*domain.DivergenceContext, error) {
	return nil, nil
}

func (stubStore) CreateBranch(ctx context.Context, branch *domain.Branch) error {
	return nil
}

func (stubStore) UpdateBranch(ctx context.Context, branch *domain.Branch) error {
	return nil
}

func (stubStore) GetBranch(ctx context.Context, branchID string) (*domain.Branch, error) {
	return nil, nil
}

func (stubStore) ListBranchesByDivergence(ctx context.Context, divergenceID string) ([]domain.Branch, error) {
	return nil, nil
}

func (stubStore) CreateAssignment(ctx context.Context, a *domain.Assignment) error {
	return nil
}

func (stubStore) UpdateAssignmentStatus(ctx context.Context, assignmentID string, status domain.AssignmentStatus, respondedAt *time.Time) error {
	return nil
}

func (stubStore) GetAssignment(ctx context.Context, assignmentID string) (*domain.Assignment, error) {
	return nil, nil
}

func (stubStore) ListAssignmentsByActor(ctx context.Context, actorID string, status *domain.AssignmentStatus) ([]domain.Assignment, error) {
	return nil, nil
}

func (stubStore) ListExpiredAssignments(ctx context.Context, before time.Time) ([]domain.Assignment, error) {
	return nil, nil
}

func (stubStore) ListRunsByStatus(ctx context.Context, status domain.RunStatus) ([]domain.Run, error) {
	return nil, nil
}

func (stubStore) ListActiveStepExecutions(ctx context.Context) ([]domain.StepExecution, error) {
	return nil, nil
}

func (stubStore) ListStaleActiveRuns(ctx context.Context, noActivitySince time.Time) ([]domain.Run, error) {
	return nil, nil
}

func (stubStore) ListTimedOutRuns(ctx context.Context, now time.Time) ([]domain.Run, error) {
	return nil, nil
}

func (stubStore) UpsertWorkflowProjection(ctx context.Context, proj *store.WorkflowProjection) error {
	return nil
}

func (stubStore) DeleteWorkflowProjection(ctx context.Context, workflowPath string) error {
	return nil
}

func (stubStore) GetWorkflowProjection(ctx context.Context, workflowPath string) (*store.WorkflowProjection, error) {
	return nil, nil
}

func (stubStore) ListActiveWorkflowProjections(ctx context.Context) ([]store.WorkflowProjection, error) {
	return nil, nil
}

func (stubStore) UpsertBranchProtectionRules(ctx context.Context, rules []store.BranchProtectionRuleProjection, sourceCommit string) error {
	return nil
}

func (stubStore) ListBranchProtectionRules(ctx context.Context) ([]store.BranchProtectionRuleProjection, error) {
	return nil, nil
}

func (stubStore) CreateRepositoryBinding(ctx context.Context, b *store.RepositoryBinding) error {
	return nil
}

func (stubStore) GetRepositoryBinding(ctx context.Context, workspaceID, repositoryID string) (*store.RepositoryBinding, error) {
	return nil, nil
}

func (stubStore) GetActiveRepositoryBinding(ctx context.Context, workspaceID, repositoryID string) (*store.RepositoryBinding, error) {
	return nil, nil
}

func (stubStore) UpdateRepositoryBinding(ctx context.Context, b *store.RepositoryBinding) error {
	return nil
}

func (stubStore) ListRepositoryBindings(ctx context.Context, workspaceID string) ([]store.RepositoryBinding, error) {
	return nil, nil
}

func (stubStore) ListActiveRepositoryBindings(ctx context.Context, workspaceID string) ([]store.RepositoryBinding, error) {
	return nil, nil
}

func (stubStore) DeactivateRepositoryBinding(ctx context.Context, workspaceID, repositoryID string) error {
	return nil
}

func (stubStore) GetSyncState(ctx context.Context) (*store.SyncState, error) {
	return nil, nil
}

func (stubStore) UpdateSyncState(ctx context.Context, state *store.SyncState) error {
	return nil
}

func (stubStore) CreateThread(ctx context.Context, thread *domain.DiscussionThread) error {
	return nil
}

func (stubStore) GetThread(ctx context.Context, threadID string) (*domain.DiscussionThread, error) {
	return nil, nil
}

func (stubStore) ListThreads(ctx context.Context, anchorType domain.AnchorType, anchorID string) ([]domain.DiscussionThread, error) {
	return nil, nil
}

func (stubStore) UpdateThread(ctx context.Context, thread *domain.DiscussionThread) error {
	return nil
}

func (stubStore) CreateComment(ctx context.Context, comment *domain.Comment) error {
	return nil
}

func (stubStore) ListComments(ctx context.Context, threadID string) ([]domain.Comment, error) {
	return nil, nil
}

func (stubStore) HasOpenThreads(ctx context.Context, anchorType domain.AnchorType, anchorID string) (bool, error) {
	return false, nil
}

func (stubStore) CreateSkill(ctx context.Context, skill *domain.Skill) error {
	return nil
}

func (stubStore) GetSkill(ctx context.Context, skillID string) (*domain.Skill, error) {
	return nil, nil
}

func (stubStore) UpdateSkill(ctx context.Context, skill *domain.Skill) error {
	return nil
}

func (stubStore) ListSkills(ctx context.Context) ([]domain.Skill, error) {
	return nil, nil
}

func (stubStore) ListSkillsByCategory(ctx context.Context, category string) ([]domain.Skill, error) {
	return nil, nil
}

func (stubStore) AddSkillToActor(ctx context.Context, actorID, skillID string) error {
	return nil
}

func (stubStore) RemoveSkillFromActor(ctx context.Context, actorID, skillID string) error {
	return nil
}

func (stubStore) ListActorSkills(ctx context.Context, actorID string) ([]domain.Skill, error) {
	return nil, nil
}

func (stubStore) ListActorsBySkills(ctx context.Context, skillNames []string) ([]domain.Actor, error) {
	return nil, nil
}

func (stubStore) UpsertExecutionProjection(ctx context.Context, proj *store.ExecutionProjection) error {
	return nil
}

func (stubStore) GetExecutionProjection(ctx context.Context, taskPath string) (*store.ExecutionProjection, error) {
	return nil, nil
}

func (stubStore) QueryExecutionProjections(ctx context.Context, query store.ExecutionProjectionQuery) ([]store.ExecutionProjection, error) {
	return nil, nil
}

func (stubStore) DeleteExecutionProjection(ctx context.Context, taskPath string) error {
	return nil
}

func (stubStore) EnqueueDelivery(ctx context.Context, entry *store.DeliveryEntry) error {
	return nil
}

func (stubStore) ClaimDeliveries(ctx context.Context, limit int) ([]store.DeliveryEntry, error) {
	return nil, nil
}

func (stubStore) UpdateDeliveryStatus(ctx context.Context, deliveryID, status string, lastError string, nextRetryAt *time.Time) error {
	return nil
}

func (stubStore) MarkDelivered(ctx context.Context, deliveryID string) error {
	return nil
}

func (stubStore) LogDeliveryAttempt(ctx context.Context, entry *store.DeliveryLogEntry) error {
	return nil
}

func (stubStore) ListDeliveryHistory(ctx context.Context, query store.DeliveryHistoryQuery) ([]store.DeliveryLogEntry, error) {
	return nil, nil
}

func (stubStore) GetDelivery(ctx context.Context, deliveryID string) (*store.DeliveryEntry, error) {
	return nil, nil
}

func (stubStore) ListDeliveries(ctx context.Context, subscriptionID string, status string, limit int) ([]store.DeliveryEntry, error) {
	return nil, nil
}

func (stubStore) GetDeliveryStats(ctx context.Context, subscriptionID string) (*store.DeliveryStats, error) {
	return nil, nil
}

func (stubStore) WriteEventLog(ctx context.Context, entry *store.EventLogEntry) error {
	return nil
}

func (stubStore) ListEventsAfter(ctx context.Context, afterEventID string, eventTypes []string, limit int) ([]store.EventLogEntry, error) {
	return nil, nil
}

func (stubStore) DeleteExpiredDeliveries(ctx context.Context, before time.Time) (int64, error) {
	return 0, nil
}

func (stubStore) CreateSubscription(ctx context.Context, sub *store.EventSubscription) error {
	return nil
}

func (stubStore) GetSubscription(ctx context.Context, subscriptionID string) (*store.EventSubscription, error) {
	return nil, nil
}

func (stubStore) UpdateSubscription(ctx context.Context, sub *store.EventSubscription) error {
	return nil
}

func (stubStore) DeleteSubscription(ctx context.Context, subscriptionID string) error {
	return nil
}

func (stubStore) ListSubscriptions(ctx context.Context, workspaceID string) ([]store.EventSubscription, error) {
	return nil, nil
}

func (stubStore) ListActiveSubscriptionsByEventType(ctx context.Context, eventType string) ([]store.EventSubscription, error) {
	return nil, nil
}

func (stubStore) ApplyMigrations(ctx context.Context, migrationsDir string) error {
	return nil
}

func (stubStore) IsMigrationApplied(ctx context.Context, version string) (bool, error) {
	return false, nil
}

func (stubStore) Close() {
}

var _ store.Store = stubStore{}
