package engine_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/actor"
	"github.com/bszymi/spine/internal/branchprotect"
	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/engine"
	"github.com/bszymi/spine/internal/git"
	"github.com/bszymi/spine/internal/workflow"
)

// ── In-memory implementations for integration testing ──

// memStore implements engine.RunStore with full in-memory state tracking.
type memStore struct {
	runs  map[string]*domain.Run
	steps map[string]*domain.StepExecution
}

func newMemStore() *memStore {
	return &memStore{
		runs:  make(map[string]*domain.Run),
		steps: make(map[string]*domain.StepExecution),
	}
}

func (s *memStore) CreateRun(_ context.Context, run *domain.Run) error {
	s.runs[run.RunID] = run
	return nil
}

func (s *memStore) GetRun(_ context.Context, runID string) (*domain.Run, error) {
	r, ok := s.runs[runID]
	if !ok {
		return nil, domain.NewError(domain.ErrNotFound, "run not found")
	}
	return r, nil
}

func (s *memStore) UpdateRunStatus(_ context.Context, runID string, status domain.RunStatus) error {
	r, ok := s.runs[runID]
	if !ok {
		return domain.NewError(domain.ErrNotFound, "run not found")
	}
	r.Status = status
	return nil
}

func (s *memStore) TransitionRunStatus(_ context.Context, runID string, fromStatus, toStatus domain.RunStatus) (bool, error) {
	r, ok := s.runs[runID]
	if !ok {
		return false, domain.NewError(domain.ErrNotFound, "run not found")
	}
	if r.Status != fromStatus {
		return false, nil
	}
	r.Status = toStatus
	return true, nil
}

func (s *memStore) UpdateCurrentStep(_ context.Context, runID, stepID string) error {
	r, ok := s.runs[runID]
	if !ok {
		return domain.NewError(domain.ErrNotFound, "run not found")
	}
	r.CurrentStepID = stepID
	return nil
}

func (s *memStore) SetCommitMeta(_ context.Context, runID string, meta map[string]string) error {
	r, ok := s.runs[runID]
	if !ok {
		return domain.NewError(domain.ErrNotFound, "run not found")
	}
	r.CommitMeta = meta
	return nil
}

func (s *memStore) CreateStepExecution(_ context.Context, exec *domain.StepExecution) error {
	s.steps[exec.ExecutionID] = exec
	return nil
}

func (s *memStore) GetStepExecution(_ context.Context, executionID string) (*domain.StepExecution, error) {
	e, ok := s.steps[executionID]
	if !ok {
		return nil, domain.NewError(domain.ErrNotFound, "step execution not found")
	}
	return e, nil
}

func (s *memStore) UpdateStepExecution(_ context.Context, exec *domain.StepExecution) error {
	s.steps[exec.ExecutionID] = exec
	return nil
}

func (s *memStore) ListStepExecutionsByRun(_ context.Context, runID string) ([]domain.StepExecution, error) {
	var result []domain.StepExecution
	for _, e := range s.steps {
		if e.RunID == runID {
			result = append(result, *e)
		}
	}
	return result, nil
}

func (s *memStore) CreateDivergenceContext(_ context.Context, _ *domain.DivergenceContext) error {
	return nil
}
func (s *memStore) UpdateDivergenceContext(_ context.Context, _ *domain.DivergenceContext) error {
	return nil
}
func (s *memStore) GetDivergenceContext(_ context.Context, _ string) (*domain.DivergenceContext, error) {
	return nil, nil
}
func (s *memStore) CreateBranch(_ context.Context, _ *domain.Branch) error { return nil }
func (s *memStore) UpdateBranch(_ context.Context, _ *domain.Branch) error { return nil }
func (s *memStore) GetBranch(_ context.Context, _ string) (*domain.Branch, error) {
	return nil, domain.NewError(domain.ErrNotFound, "branch not found")
}
func (s *memStore) ListBranchesByDivergence(_ context.Context, _ string) ([]domain.Branch, error) {
	return nil, nil
}
func (s *memStore) UpsertRepositoryMergeOutcome(_ context.Context, _ *domain.RepositoryMergeOutcome) error {
	return nil
}
func (s *memStore) GetRepositoryMergeOutcome(_ context.Context, _, _ string) (*domain.RepositoryMergeOutcome, error) {
	return nil, domain.NewError(domain.ErrNotFound, "merge outcome not found")
}
func (s *memStore) ListRepositoryMergeOutcomes(_ context.Context, _ string) ([]domain.RepositoryMergeOutcome, error) {
	return []domain.RepositoryMergeOutcome{}, nil
}

// memArtifactReader returns a predefined artifact.
type memArtifactReader struct {
	artifacts map[string]*domain.Artifact
}

func (r *memArtifactReader) Read(_ context.Context, path, _ string) (*domain.Artifact, error) {
	a, ok := r.artifacts[path]
	if !ok {
		return nil, domain.NewError(domain.ErrNotFound, fmt.Sprintf("artifact %s not found", path))
	}
	return a, nil
}

// memWorkflowResolver returns a fixed workflow binding.
type memWorkflowResolver struct {
	wfDef *domain.WorkflowDefinition
}

func (r *memWorkflowResolver) ResolveWorkflow(_ context.Context, _, _ string) (*workflow.BindingResult, error) {
	return &workflow.BindingResult{
		Workflow:     r.wfDef,
		CommitSHA:    "integration-test-sha",
		VersionLabel: r.wfDef.Version,
	}, nil
}

func (r *memWorkflowResolver) ResolveWorkflowForMode(_ context.Context, _, _, _ string) (*workflow.BindingResult, error) {
	return &workflow.BindingResult{
		Workflow:     r.wfDef,
		CommitSHA:    "integration-test-sha",
		VersionLabel: r.wfDef.Version,
	}, nil
}

// memWorkflowLoader returns a fixed workflow definition.
type memWorkflowLoader struct {
	wfDef *domain.WorkflowDefinition
}

func (l *memWorkflowLoader) LoadWorkflow(_ context.Context, _, _ string) (*domain.WorkflowDefinition, error) {
	return l.wfDef, nil
}

// memEventEmitter collects emitted events.
type memEventEmitter struct {
	events []domain.Event
}

func (e *memEventEmitter) Emit(_ context.Context, event domain.Event) error {
	e.events = append(e.events, event)
	return nil
}

// fixedActorSelector returns the same actor for every selection request.
// Sufficient for end-to-end tests that just need the auto-claim path to
// produce a non-empty actor_id.
type fixedActorSelector struct {
	actor *domain.Actor
}

func (s *fixedActorSelector) SelectActor(_ context.Context, _ actor.SelectionRequest) (*domain.Actor, error) {
	return s.actor, nil
}

// mockActorGateway records assignments and allows simulating actor responses.
type mockActorGateway struct {
	assignments []actor.AssignmentRequest
}

func (g *mockActorGateway) DeliverAssignment(_ context.Context, req actor.AssignmentRequest) error {
	g.assignments = append(g.assignments, req)
	return nil
}

func (g *mockActorGateway) ProcessResult(_ context.Context, _ actor.AssignmentRequest, _ actor.AssignmentResult) error {
	return nil
}

// ── Integration test ──

// TestEndToEndExecution validates the complete execution loop:
// task → workflow → run → step activation → actor result → next step → completion.
// This is the Phase 0 "first working slice" — proof that all components integrate.
//
// Both steps declare an automated execution mode + eligible actor types so
// the orchestrator's actor selector can resolve a concrete actor at
// activation time. Without this, Option B (TASK-004) keeps the steps in
// `waiting` and no assignment is delivered.
func TestEndToEndExecution(t *testing.T) {
	// 1. Define a two-step workflow: "implement" → "review" → done.
	wfDef := &domain.WorkflowDefinition{
		ID:        "wf-task-lifecycle",
		Name:      "Task Lifecycle",
		Version:   "1.0.0",
		Status:    domain.WorkflowStatusActive,
		AppliesTo: []string{"Task"},
		EntryStep: "implement",
		Path:      "workflows/task-lifecycle.yaml",
		Steps: []domain.StepDefinition{
			{
				ID:   "implement",
				Name: "Implement",
				Type: domain.StepTypeAutomated,
				Execution: &domain.ExecutionConfig{
					Mode:               domain.ExecModeAutomatedOnly,
					EligibleActorTypes: []string{"automated_system"},
				},
				Outcomes: []domain.OutcomeDefinition{
					{ID: "completed", Name: "Implementation Complete", NextStep: "review"},
				},
			},
			{
				ID:   "review",
				Name: "Review",
				Type: domain.StepTypeReview,
				Execution: &domain.ExecutionConfig{
					Mode:               domain.ExecModeAutomatedOnly,
					EligibleActorTypes: []string{"automated_system"},
				},
				Outcomes: []domain.OutcomeDefinition{
					{ID: "accepted", Name: "Accepted"}, // terminal — no NextStep
				},
			},
		},
	}

	// 2. Create the task artifact.
	artifacts := &memArtifactReader{
		artifacts: map[string]*domain.Artifact{
			"tasks/my-task.md": {
				Path:   "tasks/my-task.md",
				ID:     "TASK-001",
				Type:   "Task",
				Title:  "My Task",
				Status: "Pending",
			},
		},
	}

	// 3. Wire up all components.
	store := newMemStore()
	events := &memEventEmitter{}
	actors := &mockActorGateway{}

	orch, err := engine.New(
		&memWorkflowResolver{wfDef: wfDef},
		store,
		actors,
		artifacts,
		events,
		&noopGitOperator{},
		&memWorkflowLoader{wfDef: wfDef},
	)
	if err != nil {
		t.Fatalf("failed to create orchestrator: %v", err)
	}
	orch.WithBranchProtectPolicy(branchprotect.NewPermissive())
	orch.WithActorSelector(&fixedActorSelector{
		actor: &domain.Actor{ActorID: "bot-1", Type: domain.ActorTypeAutomated, Status: domain.ActorStatusActive},
	})

	ctx := context.Background()

	// 4. Start the run.
	result, err := orch.StartRun(ctx, "tasks/my-task.md")
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}

	runID := result.Run.RunID
	t.Logf("Run started: %s (status=%s)", runID, result.Run.Status)

	// Verify: run is active, entry step was activated, assignment delivered.
	if result.Run.Status != domain.RunStatusActive {
		t.Fatalf("expected run active, got %s", result.Run.Status)
	}
	if len(actors.assignments) != 1 {
		t.Fatalf("expected 1 assignment after StartRun, got %d", len(actors.assignments))
	}
	if actors.assignments[0].StepID != "implement" {
		t.Fatalf("expected assignment for implement, got %s", actors.assignments[0].StepID)
	}

	// 5. Simulate actor completing "implement" step.
	implementExecID := actors.assignments[0].AssignmentID
	t.Logf("Actor completing step: implement (execution=%s)", implementExecID)

	err = orch.SubmitStepResult(ctx, implementExecID, engine.StepResult{
		OutcomeID: "completed",
	})
	if err != nil {
		t.Fatalf("SubmitStepResult for implement failed: %v", err)
	}

	// Verify: "implement" step completed, "review" step created and activated.
	implExec, _ := store.GetStepExecution(ctx, implementExecID)
	if implExec.Status != domain.StepStatusCompleted {
		t.Fatalf("expected implement step completed, got %s", implExec.Status)
	}

	if len(actors.assignments) != 2 {
		t.Fatalf("expected 2 assignments total, got %d", len(actors.assignments))
	}
	if actors.assignments[1].StepID != "review" {
		t.Fatalf("expected assignment for review, got %s", actors.assignments[1].StepID)
	}

	// Run should still be active (review step pending).
	run, _ := store.GetRun(ctx, runID)
	if run.Status != domain.RunStatusActive {
		t.Fatalf("expected run active after step progression, got %s", run.Status)
	}
	if run.CurrentStepID != "review" {
		t.Fatalf("expected current step review, got %s", run.CurrentStepID)
	}

	// 6. Simulate actor completing "review" step (terminal).
	reviewExecID := actors.assignments[1].AssignmentID
	t.Logf("Actor completing step: review (execution=%s)", reviewExecID)

	err = orch.SubmitStepResult(ctx, reviewExecID, engine.StepResult{
		OutcomeID: "accepted",
	})
	if err != nil {
		t.Fatalf("SubmitStepResult for review failed: %v", err)
	}

	// 7. Verify: review step completed, run completed.
	reviewExec, _ := store.GetStepExecution(ctx, reviewExecID)
	if reviewExec.Status != domain.StepStatusCompleted {
		t.Fatalf("expected review step completed, got %s", reviewExec.Status)
	}

	run, _ = store.GetRun(ctx, runID)
	if run.Status != domain.RunStatusCompleted {
		t.Fatalf("expected run completed, got %s", run.Status)
	}

	// 8. Verify event trail.
	eventTypes := make([]domain.EventType, len(events.events))
	for i, e := range events.events {
		eventTypes[i] = e.Type
	}
	t.Logf("Event trail: %v", eventTypes)

	expectedEvents := map[domain.EventType]bool{
		domain.EventRunStarted:    false,
		domain.EventStepAssigned:  false,
		domain.EventStepCompleted: false,
		domain.EventRunCompleted:  false,
	}
	for _, e := range events.events {
		if _, ok := expectedEvents[e.Type]; ok {
			expectedEvents[e.Type] = true
		}
	}
	for et, found := range expectedEvents {
		if !found {
			t.Errorf("expected event %s not found in trail", et)
		}
	}

	// 9. Verify all steps tracked.
	allSteps, _ := store.ListStepExecutionsByRun(ctx, runID)
	if len(allSteps) != 2 {
		t.Fatalf("expected 2 step executions, got %d", len(allSteps))
	}

	t.Logf("Integration test passed: task executed end-to-end through 2-step workflow")
}

// TestEndToEnd_CancelMidExecution verifies that a run can be cancelled
// while a step is in progress.
func TestEndToEnd_CancelMidExecution(t *testing.T) {
	wfDef := &domain.WorkflowDefinition{
		ID:        "wf-cancel",
		EntryStep: "start",
		Path:      "workflows/cancel.yaml",
		Steps: []domain.StepDefinition{
			{
				ID:   "start",
				Name: "Start",
				Type: domain.StepTypeAutomated,
				Outcomes: []domain.OutcomeDefinition{
					{ID: "done", Name: "Done"},
				},
			},
		},
	}

	store := newMemStore()
	actors := &mockActorGateway{}
	orch, _ := engine.New(
		&memWorkflowResolver{wfDef: wfDef},
		store,
		actors,
		&memArtifactReader{artifacts: map[string]*domain.Artifact{
			"tasks/t.md": {Type: "Task", Path: "tasks/t.md"},
		}},
		&memEventEmitter{},
		&noopGitOperator{},
		&memWorkflowLoader{wfDef: wfDef},
	)

	ctx := context.Background()
	result, err := orch.StartRun(ctx, "tasks/t.md")
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}

	// Cancel while step is assigned.
	err = orch.CancelRun(ctx, result.Run.RunID)
	if err != nil {
		t.Fatalf("CancelRun failed: %v", err)
	}

	run, _ := store.GetRun(ctx, result.Run.RunID)
	if run.Status != domain.RunStatusCancelled {
		t.Fatalf("expected cancelled, got %s", run.Status)
	}
}

// TestEndToEnd_FailOnPermanentError verifies that a run fails when
// FailRun is called.
func TestEndToEnd_FailOnPermanentError(t *testing.T) {
	wfDef := &domain.WorkflowDefinition{
		ID:        "wf-fail",
		EntryStep: "start",
		Path:      "workflows/fail.yaml",
		Steps: []domain.StepDefinition{
			{
				ID:   "start",
				Name: "Start",
				Type: domain.StepTypeAutomated,
				Outcomes: []domain.OutcomeDefinition{
					{ID: "done", Name: "Done"},
				},
			},
		},
	}

	store := newMemStore()
	orch, _ := engine.New(
		&memWorkflowResolver{wfDef: wfDef},
		store,
		&mockActorGateway{},
		&memArtifactReader{artifacts: map[string]*domain.Artifact{
			"tasks/t.md": {Type: "Task", Path: "tasks/t.md"},
		}},
		&memEventEmitter{},
		&noopGitOperator{},
		&memWorkflowLoader{wfDef: wfDef},
	)

	ctx := context.Background()
	result, _ := orch.StartRun(ctx, "tasks/t.md")

	err := orch.FailRun(ctx, result.Run.RunID, "actor crashed")
	if err != nil {
		t.Fatalf("FailRun failed: %v", err)
	}

	run, _ := store.GetRun(ctx, result.Run.RunID)
	if run.Status != domain.RunStatusFailed {
		t.Fatalf("expected failed, got %s", run.Status)
	}
}

// noopGitOperator satisfies engine.GitOperator for integration tests.
type noopGitOperator struct{}

func (g *noopGitOperator) Commit(_ context.Context, _ git.CommitOpts) (git.CommitResult, error) {
	return git.CommitResult{}, nil
}
func (g *noopGitOperator) Merge(_ context.Context, _ git.MergeOpts) (git.MergeResult, error) {
	return git.MergeResult{}, nil
}
func (g *noopGitOperator) CreateBranch(_ context.Context, _, _ string) error { return nil }
func (g *noopGitOperator) DeleteBranch(_ context.Context, _ string) error    { return nil }
func (g *noopGitOperator) Diff(_ context.Context, _, _ string) ([]git.FileDiff, error) {
	return nil, nil
}
func (g *noopGitOperator) MergeBase(_ context.Context, a, _ string) (string, error) { return a, nil }
func (g *noopGitOperator) Head(_ context.Context) (string, error)                   { return "integration-test", nil }
func (g *noopGitOperator) Push(_ context.Context, _, _ string) error               { return nil }
func (g *noopGitOperator) PushBranch(_ context.Context, _, _ string) error         { return nil }
func (g *noopGitOperator) DeleteRemoteBranch(_ context.Context, _, _ string) error { return nil }
func (g *noopGitOperator) ReadFile(_ context.Context, _, _ string) ([]byte, error) {
	return nil, nil
}
func (g *noopGitOperator) Checkout(_ context.Context, _ string) error              { return nil }
func (g *noopGitOperator) WriteAndStageFile(_ context.Context, _, _ string) error  { return nil }

var _ = time.Now // used in memStore
