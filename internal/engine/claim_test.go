package engine

import (
	"context"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/store"
)

// claimBlockingStore implements BlockingStore + GetActor + ListActorSkills for claim tests.
type claimBlockingStore struct {
	*fakeBlockingStore
	actors map[string]*domain.Actor
	skills map[string][]domain.Skill // actorID -> skills
}

func newClaimBlockingStore() *claimBlockingStore {
	return &claimBlockingStore{
		fakeBlockingStore: newFakeBlockingStore(),
		actors:            make(map[string]*domain.Actor),
		skills:            make(map[string][]domain.Skill),
	}
}

func (c *claimBlockingStore) GetActor(_ context.Context, actorID string) (*domain.Actor, error) {
	a, ok := c.actors[actorID]
	if !ok {
		return nil, domain.NewError(domain.ErrNotFound, "actor not found")
	}
	return a, nil
}

func (c *claimBlockingStore) ListActorSkills(_ context.Context, actorID string) ([]domain.Skill, error) {
	return c.skills[actorID], nil
}

func (c *claimBlockingStore) GetExecutionProjection(_ context.Context, _ string) (*store.ExecutionProjection, error) {
	return nil, domain.NewError(domain.ErrNotFound, "not found")
}

func (c *claimBlockingStore) UpsertExecutionProjection(_ context.Context, _ *store.ExecutionProjection) error {
	return nil
}

func TestClaimStep_Success(t *testing.T) {
	now := time.Now()
	st := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {
				RunID: "run-1", TaskPath: "tasks/task-1.md",
				WorkflowPath: "workflows/test.yaml", WorkflowVersion: "abc",
				Status: domain.RunStatusActive, TraceID: "trace-123456789",
			},
		},
	}
	st.createdSteps = []*domain.StepExecution{
		{ExecutionID: "exec-1", RunID: "run-1", StepID: "execute",
			Status: domain.StepStatusWaiting, CreatedAt: now},
	}
	wfLoader := &claimTestWFLoader{
		wf: &domain.WorkflowDefinition{
			ID: "test", Steps: []domain.StepDefinition{
				{ID: "execute", Name: "Execute", Type: domain.StepTypeManual},
			},
		},
	}
	orch := &Orchestrator{store: st, wfLoader: wfLoader, events: &stubEventEmitter{}}

	result, err := orch.ClaimStep(context.Background(), ClaimRequest{ActorID: "actor-1", ExecutionID: "exec-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.StepID != "execute" {
		t.Errorf("expected step_id execute, got %s", result.StepID)
	}
	exec, _ := st.GetStepExecution(context.Background(), "exec-1")
	if exec.Status != domain.StepStatusAssigned {
		t.Errorf("expected assigned, got %s", exec.Status)
	}
}

func TestClaimStep_AlreadyAssigned(t *testing.T) {
	now := time.Now()
	st := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {RunID: "run-1", Status: domain.RunStatusActive, TraceID: "trace-123456789"},
		},
	}
	st.createdSteps = []*domain.StepExecution{
		{ExecutionID: "exec-1", RunID: "run-1", StepID: "execute",
			Status: domain.StepStatusAssigned, ActorID: "other", CreatedAt: now},
	}
	orch := &Orchestrator{store: st}

	_, err := orch.ClaimStep(context.Background(), ClaimRequest{ActorID: "actor-1", ExecutionID: "exec-1"})
	if err == nil {
		t.Fatal("expected error for already assigned step")
	}
}

func TestClaimStep_MissingParams(t *testing.T) {
	orch := &Orchestrator{}
	if _, err := orch.ClaimStep(context.Background(), ClaimRequest{}); err == nil {
		t.Fatal("expected error for missing params")
	}
	if _, err := orch.ClaimStep(context.Background(), ClaimRequest{ActorID: "a1"}); err == nil {
		t.Fatal("expected error for missing execution_id")
	}
}

func TestClaimStep_ActorTypeIneligible(t *testing.T) {
	now := time.Now()
	bs := newClaimBlockingStore()
	bs.actors["bot-1"] = &domain.Actor{ActorID: "bot-1", Type: domain.ActorTypeAutomated, Status: domain.ActorStatusActive}

	st := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {RunID: "run-1", TaskPath: "tasks/t.md", WorkflowPath: "wf.yaml",
				WorkflowVersion: "v1", Status: domain.RunStatusActive, TraceID: "trace-123456789"},
		},
	}
	st.createdSteps = []*domain.StepExecution{
		{ExecutionID: "exec-1", RunID: "run-1", StepID: "review",
			Status: domain.StepStatusWaiting, CreatedAt: now},
	}
	wfLoader := &claimTestWFLoader{
		wf: &domain.WorkflowDefinition{
			ID: "test", Steps: []domain.StepDefinition{
				{ID: "review", Name: "Review", Type: domain.StepTypeReview,
					Execution: &domain.ExecutionConfig{
						Mode:               domain.ExecModeHumanOnly,
						EligibleActorTypes: []string{"human"},
					}},
			},
		},
	}
	orch := &Orchestrator{store: st, wfLoader: wfLoader, events: &stubEventEmitter{}, blocking: bs}

	_, err := orch.ClaimStep(context.Background(), ClaimRequest{ActorID: "bot-1", ExecutionID: "exec-1"})
	if err == nil {
		t.Fatal("expected error for ineligible actor type")
	}
	if !containsString(err.Error(), "not eligible") {
		t.Errorf("expected 'not eligible' in error, got: %s", err.Error())
	}
}

func TestClaimStep_MissingSkills(t *testing.T) {
	now := time.Now()
	bs := newClaimBlockingStore()
	bs.actors["dev-1"] = &domain.Actor{ActorID: "dev-1", Type: domain.ActorTypeHuman, Status: domain.ActorStatusActive}
	bs.skills["dev-1"] = []domain.Skill{
		{SkillID: "s1", Name: "backend", Status: domain.SkillStatusActive},
	}

	st := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {RunID: "run-1", TaskPath: "tasks/t.md", WorkflowPath: "wf.yaml",
				WorkflowVersion: "v1", Status: domain.RunStatusActive, TraceID: "trace-123456789"},
		},
	}
	st.createdSteps = []*domain.StepExecution{
		{ExecutionID: "exec-1", RunID: "run-1", StepID: "deploy",
			Status: domain.StepStatusWaiting, CreatedAt: now},
	}
	wfLoader := &claimTestWFLoader{
		wf: &domain.WorkflowDefinition{
			ID: "test", Steps: []domain.StepDefinition{
				{ID: "deploy", Name: "Deploy", Type: domain.StepTypeManual,
					Execution: &domain.ExecutionConfig{
						Mode:           domain.ExecModeHybrid,
						RequiredSkills: []string{"backend", "deployment"},
					}},
			},
		},
	}
	orch := &Orchestrator{store: st, wfLoader: wfLoader, events: &stubEventEmitter{}, blocking: bs}

	_, err := orch.ClaimStep(context.Background(), ClaimRequest{ActorID: "dev-1", ExecutionID: "exec-1"})
	if err == nil {
		t.Fatal("expected error for missing skills")
	}
	if !containsString(err.Error(), "deployment") {
		t.Errorf("expected error to mention missing skill 'deployment', got: %s", err.Error())
	}
}

func TestClaimStep_AtomicConflict(t *testing.T) {
	now := time.Now()
	st := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {RunID: "run-1", TaskPath: "tasks/t.md", WorkflowPath: "wf.yaml",
				WorkflowVersion: "v1", Status: domain.RunStatusActive, TraceID: "trace-123456789"},
		},
	}
	st.createdSteps = []*domain.StepExecution{
		{ExecutionID: "exec-1", RunID: "run-1", StepID: "execute",
			Status: domain.StepStatusWaiting, CreatedAt: now},
	}
	wfLoader := &claimTestWFLoader{
		wf: &domain.WorkflowDefinition{
			ID: "test", Steps: []domain.StepDefinition{
				{ID: "execute", Name: "Execute", Type: domain.StepTypeManual},
			},
		},
	}

	// Pre-populate an active assignment to simulate concurrent claim.
	as := newMemAssignmentStore()
	as.assignments["claim-exec-1-actor-2"] = &domain.Assignment{
		AssignmentID: "claim-exec-1-actor-2", ExecutionID: "exec-1",
		ActorID: "actor-2", Status: domain.AssignmentStatusActive, AssignedAt: now,
	}

	orch := &Orchestrator{store: st, wfLoader: wfLoader, events: &stubEventEmitter{}, assignments: as}

	_, err := orch.ClaimStep(context.Background(), ClaimRequest{ActorID: "actor-1", ExecutionID: "exec-1"})
	if err == nil {
		t.Fatal("expected conflict error for concurrent claim")
	}
	if !containsString(err.Error(), "already claimed") {
		t.Errorf("expected 'already claimed' in error, got: %s", err.Error())
	}
}

func TestClaimStep_DeprecatedSkillDoesNotCount(t *testing.T) {
	now := time.Now()
	bs := newClaimBlockingStore()
	bs.actors["dev-1"] = &domain.Actor{ActorID: "dev-1", Type: domain.ActorTypeHuman, Status: domain.ActorStatusActive}
	bs.skills["dev-1"] = []domain.Skill{
		{SkillID: "s1", Name: "review", Status: domain.SkillStatusDeprecated},
	}

	st := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {RunID: "run-1", TaskPath: "tasks/t.md", WorkflowPath: "wf.yaml",
				WorkflowVersion: "v1", Status: domain.RunStatusActive, TraceID: "trace-123456789"},
		},
	}
	st.createdSteps = []*domain.StepExecution{
		{ExecutionID: "exec-1", RunID: "run-1", StepID: "review",
			Status: domain.StepStatusWaiting, CreatedAt: now},
	}
	wfLoader := &claimTestWFLoader{
		wf: &domain.WorkflowDefinition{
			ID: "test", Steps: []domain.StepDefinition{
				{ID: "review", Name: "Review", Type: domain.StepTypeReview,
					Execution: &domain.ExecutionConfig{
						Mode:           domain.ExecModeHybrid,
						RequiredSkills: []string{"review"},
					}},
			},
		},
	}
	orch := &Orchestrator{store: st, wfLoader: wfLoader, events: &stubEventEmitter{}, blocking: bs}

	_, err := orch.ClaimStep(context.Background(), ClaimRequest{ActorID: "dev-1", ExecutionID: "exec-1"})
	if err == nil {
		t.Fatal("expected error — deprecated skill should not satisfy requirement")
	}
}

func containsString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// claimTestWFLoader returns a fixed workflow for any path/ref.
type claimTestWFLoader struct {
	wf *domain.WorkflowDefinition
}

func (s *claimTestWFLoader) LoadWorkflow(_ context.Context, _, _ string) (*domain.WorkflowDefinition, error) {
	return s.wf, nil
}
