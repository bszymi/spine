package engine

import (
	"context"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/store"
)

// stepListingBlockingStore is a combined fake that satisfies both stepListingStore
// and the actorLoader interface used by loadActor.
type stepListingBlockingStore struct {
	execs  []domain.StepExecution
	runs   map[string]*domain.Run
	actors map[string]*domain.Actor
}

func (s *stepListingBlockingStore) ListActiveStepExecutions(_ context.Context) ([]domain.StepExecution, error) {
	return s.execs, nil
}

func (s *stepListingBlockingStore) GetRun(_ context.Context, runID string) (*domain.Run, error) {
	r, ok := s.runs[runID]
	if !ok {
		return nil, domain.NewError(domain.ErrNotFound, "run not found")
	}
	return r, nil
}

func (s *stepListingBlockingStore) GetActor(_ context.Context, actorID string) (*domain.Actor, error) {
	a, ok := s.actors[actorID]
	if !ok {
		return nil, domain.NewError(domain.ErrNotFound, "actor not found")
	}
	return a, nil
}

// BlockingStore stubs — not exercised by step execution tests.
func (s *stepListingBlockingStore) QueryArtifactLinks(_ context.Context, _ string) ([]store.ArtifactLink, error) {
	return nil, nil
}
func (s *stepListingBlockingStore) QueryArtifactLinksByTarget(_ context.Context, _ string) ([]store.ArtifactLink, error) {
	return nil, nil
}
func (s *stepListingBlockingStore) GetArtifactProjection(_ context.Context, _ string) (*store.ArtifactProjection, error) {
	return nil, domain.NewError(domain.ErrNotFound, "not found")
}

// stepListingWFLoader returns a fixed workflow definition.
type stepListingWFLoader struct {
	wf *domain.WorkflowDefinition
}

func (l *stepListingWFLoader) LoadWorkflow(_ context.Context, _, _ string) (*domain.WorkflowDefinition, error) {
	return l.wf, nil
}

func newStepListingOrch(bs *stepListingBlockingStore, wf *domain.WorkflowDefinition) *Orchestrator {
	return &Orchestrator{
		blocking: bs,
		wfLoader: &stepListingWFLoader{wf: wf},
	}
}

func makeRun(runID string) *domain.Run {
	return &domain.Run{
		RunID:           runID,
		TaskPath:        "tasks/test.md",
		WorkflowPath:    "workflows/test.yaml",
		WorkflowVersion: "v1",
		Status:          domain.RunStatusActive,
	}
}

func makeWaitingExec(execID, runID, stepID string) domain.StepExecution {
	return domain.StepExecution{
		ExecutionID: execID,
		RunID:       runID,
		StepID:      stepID,
		Status:      domain.StepStatusWaiting,
		Attempt:     1,
		CreatedAt:   time.Now(),
	}
}

// TestListStepExecutions_ActorTypeDerivedFromActorID verifies that when actor_id
// is provided the actor's type is looked up and used to filter steps by
// eligible_actor_types in the workflow definition.
func TestListStepExecutions_ActorTypeDerivedFromActorID(t *testing.T) {
	// Given: one human-only step and one unrestricted step.
	bs := &stepListingBlockingStore{
		execs: []domain.StepExecution{
			makeWaitingExec("exec-review", "run-1", "review"),
			makeWaitingExec("exec-validate", "run-1", "validate"),
		},
		runs: map[string]*domain.Run{
			"run-1": makeRun("run-1"),
		},
		actors: map[string]*domain.Actor{
			"bot-1": {ActorID: "bot-1", Type: domain.ActorTypeAutomated, Status: domain.ActorStatusActive},
		},
	}
	wf := &domain.WorkflowDefinition{
		ID: "test",
		Steps: []domain.StepDefinition{
			{
				ID:   "review",
				Name: "Review",
				Type: domain.StepTypeReview,
				Execution: &domain.ExecutionConfig{
					EligibleActorTypes: []string{"human"},
				},
			},
			{
				ID:   "validate",
				Name: "Validate",
				Type: domain.StepTypeManual,
				// No EligibleActorTypes — visible to all.
			},
		},
	}
	orch := newStepListingOrch(bs, wf)

	// When: automated_system actor queries for its steps.
	steps, err := orch.ListStepExecutions(context.Background(), StepExecutionQuery{
		ActorID: "bot-1",
		Status:  "waiting",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Then: only the unrestricted validate step is returned.
	if len(steps) != 1 {
		t.Fatalf("expected 1 step, got %d: %v", len(steps), steps)
	}
	if steps[0].StepID != "validate" {
		t.Errorf("expected step_id validate, got %s", steps[0].StepID)
	}
}

// TestListStepExecutions_AutomatedSystemExcludedFromHumanOnlySteps verifies the
// explicit AC: automated_system actors do not see human-only or ai_agent-only steps.
func TestListStepExecutions_AutomatedSystemExcludedFromHumanOnlySteps(t *testing.T) {
	bs := &stepListingBlockingStore{
		execs: []domain.StepExecution{
			makeWaitingExec("exec-human", "run-1", "human-step"),
			makeWaitingExec("exec-ai", "run-1", "ai-step"),
			makeWaitingExec("exec-auto", "run-1", "auto-step"),
		},
		runs: map[string]*domain.Run{
			"run-1": makeRun("run-1"),
		},
		actors: map[string]*domain.Actor{
			"bot-1": {ActorID: "bot-1", Type: domain.ActorTypeAutomated, Status: domain.ActorStatusActive},
		},
	}
	wf := &domain.WorkflowDefinition{
		ID: "test",
		Steps: []domain.StepDefinition{
			{ID: "human-step", Execution: &domain.ExecutionConfig{EligibleActorTypes: []string{"human"}}},
			{ID: "ai-step", Execution: &domain.ExecutionConfig{EligibleActorTypes: []string{"ai_agent"}}},
			{ID: "auto-step", Execution: &domain.ExecutionConfig{EligibleActorTypes: []string{"automated_system"}}},
		},
	}
	orch := newStepListingOrch(bs, wf)

	steps, err := orch.ListStepExecutions(context.Background(), StepExecutionQuery{ActorID: "bot-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(steps) != 1 {
		t.Fatalf("expected 1 step, got %d: %v", len(steps), steps)
	}
	if steps[0].StepID != "auto-step" {
		t.Errorf("expected auto-step, got %s", steps[0].StepID)
	}
}

// TestListStepExecutions_NoEligibleActorTypesVisibleToAll verifies that steps
// without eligible_actor_types restrictions are returned for any actor type.
func TestListStepExecutions_NoEligibleActorTypesVisibleToAll(t *testing.T) {
	bs := &stepListingBlockingStore{
		execs: []domain.StepExecution{
			makeWaitingExec("exec-open", "run-1", "open-step"),
		},
		runs: map[string]*domain.Run{
			"run-1": makeRun("run-1"),
		},
		actors: map[string]*domain.Actor{
			"bot-1":   {ActorID: "bot-1", Type: domain.ActorTypeAutomated, Status: domain.ActorStatusActive},
			"human-1": {ActorID: "human-1", Type: domain.ActorTypeHuman, Status: domain.ActorStatusActive},
		},
	}
	wf := &domain.WorkflowDefinition{
		ID: "test",
		Steps: []domain.StepDefinition{
			{ID: "open-step"}, // No Execution config — unrestricted.
		},
	}

	for _, actorID := range []string{"bot-1", "human-1"} {
		orch := newStepListingOrch(bs, wf)
		steps, err := orch.ListStepExecutions(context.Background(), StepExecutionQuery{ActorID: actorID})
		if err != nil {
			t.Fatalf("actor %s: unexpected error: %v", actorID, err)
		}
		if len(steps) != 1 {
			t.Errorf("actor %s: expected 1 step (unrestricted), got %d", actorID, len(steps))
		}
	}
}

// TestListStepExecutions_ActorIDFilterWorksAlongsideTypeFilter verifies that
// actor_id based eligible_actor_ids filtering continues to work alongside the
// type-based filter.
func TestListStepExecutions_ActorIDFilterWorksAlongsideTypeFilter(t *testing.T) {
	bs := &stepListingBlockingStore{
		execs: []domain.StepExecution{
			// exec-1: restricted to bot-1 by actor_id AND automated_system by type
			{ExecutionID: "exec-1", RunID: "run-1", StepID: "auto-step",
				Status: domain.StepStatusWaiting, Attempt: 1, CreatedAt: time.Now(),
				EligibleActorIDs: []string{"bot-1"},
			},
			// exec-2: open actor_ids, but human-only by type — bot should not see it
			{ExecutionID: "exec-2", RunID: "run-1", StepID: "human-step",
				Status: domain.StepStatusWaiting, Attempt: 1, CreatedAt: time.Now(),
			},
		},
		runs: map[string]*domain.Run{
			"run-1": makeRun("run-1"),
		},
		actors: map[string]*domain.Actor{
			"bot-1": {ActorID: "bot-1", Type: domain.ActorTypeAutomated, Status: domain.ActorStatusActive},
		},
	}
	wf := &domain.WorkflowDefinition{
		ID: "test",
		Steps: []domain.StepDefinition{
			{ID: "auto-step", Execution: &domain.ExecutionConfig{EligibleActorTypes: []string{"automated_system"}}},
			{ID: "human-step", Execution: &domain.ExecutionConfig{EligibleActorTypes: []string{"human"}}},
		},
	}
	orch := newStepListingOrch(bs, wf)

	steps, err := orch.ListStepExecutions(context.Background(), StepExecutionQuery{ActorID: "bot-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(steps) != 1 {
		t.Fatalf("expected 1 step, got %d: %v", len(steps), steps)
	}
	if steps[0].ExecutionID != "exec-1" {
		t.Errorf("expected exec-1, got %s", steps[0].ExecutionID)
	}
}

// TestListStepExecutions_UnknownActorFallsBackToNoTypeFilter verifies that when
// actor lookup fails (unknown actor_id) the type filter is skipped so all
// steps remain accessible rather than returning nothing unexpectedly.
func TestListStepExecutions_UnknownActorFallsBackToNoTypeFilter(t *testing.T) {
	bs := &stepListingBlockingStore{
		execs: []domain.StepExecution{
			makeWaitingExec("exec-1", "run-1", "human-step"),
		},
		runs: map[string]*domain.Run{
			"run-1": makeRun("run-1"),
		},
		actors: map[string]*domain.Actor{}, // no actors registered
	}
	wf := &domain.WorkflowDefinition{
		ID: "test",
		Steps: []domain.StepDefinition{
			{ID: "human-step", Execution: &domain.ExecutionConfig{EligibleActorTypes: []string{"human"}}},
		},
	}
	orch := newStepListingOrch(bs, wf)

	// When actor_id is unknown, actor type cannot be derived → q.ActorType fallback (empty) → no type filter.
	steps, err := orch.ListStepExecutions(context.Background(), StepExecutionQuery{ActorID: "unknown-bot"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Step is returned because no type filter is applied (safe fallback).
	if len(steps) != 1 {
		t.Errorf("expected 1 step (no type filter on unknown actor), got %d", len(steps))
	}
}
