package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/workflow"
)

// ── Test stubs with configurable behavior ──

type mockArtifactReader struct {
	artifact *domain.Artifact
	err      error
}

func (m *mockArtifactReader) Read(_ context.Context, _, _ string) (*domain.Artifact, error) {
	return m.artifact, m.err
}

type mockWorkflowResolver struct {
	result *workflow.BindingResult
	err    error
}

func (m *mockWorkflowResolver) ResolveWorkflow(_ context.Context, _, _ string) (*workflow.BindingResult, error) {
	return m.result, m.err
}

type mockRunStore struct {
	stubRunStore // embed for default no-op implementations

	createdRun         *domain.Run
	createdSteps       []*domain.StepExecution
	runs               map[string]*domain.Run
	branches           []*domain.Branch
	divergenceContexts map[string]*domain.DivergenceContext
	statusCalls        []statusCall
	createRunErr       error
	updateErr          error
}

type statusCall struct {
	runID  string
	status domain.RunStatus
}

func (m *mockRunStore) CreateRun(_ context.Context, run *domain.Run) error {
	if m.createRunErr != nil {
		return m.createRunErr
	}
	m.createdRun = run
	if m.runs == nil {
		m.runs = make(map[string]*domain.Run)
	}
	m.runs[run.RunID] = run
	return nil
}

func (m *mockRunStore) GetRun(_ context.Context, runID string) (*domain.Run, error) {
	if m.runs != nil {
		if r, ok := m.runs[runID]; ok {
			return r, nil
		}
	}
	return nil, domain.NewError(domain.ErrNotFound, "run not found")
}

func (m *mockRunStore) UpdateRunStatus(_ context.Context, runID string, status domain.RunStatus) error {
	if m.updateErr != nil {
		return m.updateErr
	}
	m.statusCalls = append(m.statusCalls, statusCall{runID, status})
	if m.runs != nil {
		if r, ok := m.runs[runID]; ok {
			r.Status = status
		}
	}
	return nil
}

func (m *mockRunStore) UpdateCurrentStep(_ context.Context, runID, stepID string) error {
	if m.runs != nil {
		if r, ok := m.runs[runID]; ok {
			r.CurrentStepID = stepID
		}
	}
	return nil
}

func (m *mockRunStore) CreateStepExecution(_ context.Context, exec *domain.StepExecution) error {
	m.createdSteps = append(m.createdSteps, exec)
	return nil
}

func (m *mockRunStore) GetStepExecution(_ context.Context, executionID string) (*domain.StepExecution, error) {
	for _, s := range m.createdSteps {
		if s.ExecutionID == executionID {
			return s, nil
		}
	}
	return nil, domain.NewError(domain.ErrNotFound, "step execution not found")
}

func (m *mockRunStore) UpdateStepExecution(_ context.Context, exec *domain.StepExecution) error {
	for i, s := range m.createdSteps {
		if s.ExecutionID == exec.ExecutionID {
			m.createdSteps[i] = exec
			return nil
		}
	}
	return nil
}

func (m *mockRunStore) ListStepExecutionsByRun(_ context.Context, runID string) ([]domain.StepExecution, error) {
	var result []domain.StepExecution
	for _, s := range m.createdSteps {
		if s.RunID == runID {
			result = append(result, *s)
		}
	}
	return result, nil
}

func (m *mockRunStore) GetBranch(_ context.Context, branchID string) (*domain.Branch, error) {
	for _, b := range m.branches {
		if b.BranchID == branchID {
			return b, nil
		}
	}
	return nil, domain.NewError(domain.ErrNotFound, "branch not found")
}

func (m *mockRunStore) ListBranchesByDivergence(_ context.Context, divergenceID string) ([]domain.Branch, error) {
	var result []domain.Branch
	for _, b := range m.branches {
		if b.DivergenceID == divergenceID {
			result = append(result, *b)
		}
	}
	return result, nil
}

func (m *mockRunStore) GetDivergenceContext(_ context.Context, divergenceID string) (*domain.DivergenceContext, error) {
	if m.divergenceContexts != nil {
		if d, ok := m.divergenceContexts[divergenceID]; ok {
			return d, nil
		}
	}
	return nil, domain.NewError(domain.ErrNotFound, "divergence context not found")
}

func (m *mockRunStore) UpdateBranch(_ context.Context, branch *domain.Branch) error {
	for i, b := range m.branches {
		if b.BranchID == branch.BranchID {
			m.branches[i] = branch
			return nil
		}
	}
	return domain.NewError(domain.ErrNotFound, "branch not found")
}

type mockEventEmitter struct {
	events []domain.Event
}

func (m *mockEventEmitter) Emit(_ context.Context, event domain.Event) error {
	m.events = append(m.events, event)
	return nil
}

// ── Helper to build orchestrator with mocks ──

func testOrchestrator(
	artifacts ArtifactReader,
	wfResolver WorkflowResolver,
	store *mockRunStore,
	events *mockEventEmitter,
) *Orchestrator {
	return &Orchestrator{
		workflows: wfResolver,
		store:     store,
		actors:    &stubActorAssigner{},
		artifacts: artifacts,
		events:    events,
		git:       &stubGitOperator{},
		wfLoader:  &stubWorkflowLoader{},
	}
}

// ── StartRun tests ──

func TestStartRun_HappyPath(t *testing.T) {
	artifacts := &mockArtifactReader{
		artifact: &domain.Artifact{Type: "task", Path: "tasks/my-task.md"},
	}
	resolver := &mockWorkflowResolver{
		result: &workflow.BindingResult{
			Workflow: &domain.WorkflowDefinition{
				ID:        "wf-task",
				Path:      "workflows/task.yaml",
				Version:   "1.0.0",
				EntryStep: "start",
				Steps: []domain.StepDefinition{
					{ID: "start", Name: "Start"},
				},
			},
			CommitSHA:    "abc123",
			VersionLabel: "1.0.0",
		},
	}
	store := &mockRunStore{}
	events := &mockEventEmitter{}
	orch := testOrchestrator(artifacts, resolver, store, events)

	result, err := orch.StartRun(context.Background(), "tasks/my-task.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Run should be active.
	if result.Run.Status != domain.RunStatusActive {
		t.Errorf("expected status active, got %s", result.Run.Status)
	}

	// Run fields should be populated.
	if result.Run.TaskPath != "tasks/my-task.md" {
		t.Errorf("expected task path tasks/my-task.md, got %s", result.Run.TaskPath)
	}
	if result.Run.WorkflowID != "wf-task" {
		t.Errorf("expected workflow ID wf-task, got %s", result.Run.WorkflowID)
	}
	if result.Run.WorkflowVersion != "abc123" {
		t.Errorf("expected workflow version abc123, got %s", result.Run.WorkflowVersion)
	}
	if result.Run.CurrentStepID != "start" {
		t.Errorf("expected current step start, got %s", result.Run.CurrentStepID)
	}
	if result.Run.TraceID == "" {
		t.Error("expected non-empty trace ID")
	}

	// Entry step should be created in waiting status.
	if result.EntryStep == nil {
		t.Fatal("expected non-nil entry step")
	}
	if result.EntryStep.Status != domain.StepStatusWaiting {
		t.Errorf("expected step status waiting, got %s", result.EntryStep.Status)
	}
	if result.EntryStep.StepID != "start" {
		t.Errorf("expected step ID start, got %s", result.EntryStep.StepID)
	}
	if result.EntryStep.Attempt != 1 {
		t.Errorf("expected attempt 1, got %d", result.EntryStep.Attempt)
	}

	// Store should have received create + activate calls.
	if store.createdRun == nil {
		t.Fatal("expected run to be created in store")
	}
	if len(store.statusCalls) != 1 {
		t.Fatalf("expected 1 status update, got %d", len(store.statusCalls))
	}
	if store.statusCalls[0].status != domain.RunStatusActive {
		t.Errorf("expected status update to active, got %s", store.statusCalls[0].status)
	}
	if len(store.createdSteps) != 1 {
		t.Fatalf("expected 1 step created, got %d", len(store.createdSteps))
	}

	// Events should include run_started and step_assigned.
	if len(events.events) < 1 {
		t.Fatal("expected at least 1 event")
	}
	if events.events[0].Type != domain.EventRunStarted {
		t.Errorf("expected first event run_started, got %s", events.events[0].Type)
	}
}

func TestStartRun_SetsRunTimeout(t *testing.T) {
	artifacts := &mockArtifactReader{
		artifact: &domain.Artifact{Type: "task", Path: "tasks/my-task.md"},
	}
	resolver := &mockWorkflowResolver{
		result: &workflow.BindingResult{
			Workflow: &domain.WorkflowDefinition{
				ID:        "wf-task",
				Path:      "workflows/task.yaml",
				Version:   "1.0.0",
				EntryStep: "start",
				Timeout:   "24h",
				Steps: []domain.StepDefinition{
					{ID: "start", Name: "Start"},
				},
			},
			CommitSHA:    "abc123",
			VersionLabel: "1.0.0",
		},
	}
	store := &mockRunStore{}
	events := &mockEventEmitter{}
	orch := testOrchestrator(artifacts, resolver, store, events)

	result, err := orch.StartRun(context.Background(), "tasks/my-task.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Run.TimeoutAt == nil {
		t.Fatal("expected timeout_at to be set")
	}
	// TimeoutAt should be approximately 24h from now.
	expected := time.Now().Add(24 * time.Hour)
	diff := result.Run.TimeoutAt.Sub(expected)
	if diff < -5*time.Second || diff > 5*time.Second {
		t.Errorf("expected timeout_at ~24h from now, got diff %v", diff)
	}
}

func TestStartRun_NoTimeoutWithoutConfig(t *testing.T) {
	artifacts := &mockArtifactReader{
		artifact: &domain.Artifact{Type: "task", Path: "tasks/my-task.md"},
	}
	resolver := &mockWorkflowResolver{
		result: &workflow.BindingResult{
			Workflow: &domain.WorkflowDefinition{
				ID:        "wf-task",
				Path:      "workflows/task.yaml",
				Version:   "1.0.0",
				EntryStep: "start",
				Steps: []domain.StepDefinition{
					{ID: "start", Name: "Start"},
				},
			},
			CommitSHA:    "abc123",
			VersionLabel: "1.0.0",
		},
	}
	store := &mockRunStore{}
	events := &mockEventEmitter{}
	orch := testOrchestrator(artifacts, resolver, store, events)

	result, err := orch.StartRun(context.Background(), "tasks/my-task.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Run.TimeoutAt != nil {
		t.Error("expected timeout_at to be nil when no timeout configured")
	}
}

func TestStartRun_EmptyTaskPath(t *testing.T) {
	orch := testOrchestrator(
		&mockArtifactReader{},
		&mockWorkflowResolver{},
		&mockRunStore{},
		&mockEventEmitter{},
	)

	_, err := orch.StartRun(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty task path")
	}
	var spineErr *domain.SpineError
	if !errors.As(err, &spineErr) {
		t.Fatalf("expected SpineError, got %T", err)
	}
	if spineErr.Code != domain.ErrInvalidParams {
		t.Errorf("expected error code invalid_params, got %s", spineErr.Code)
	}
}

func TestStartRun_ArtifactReadFails(t *testing.T) {
	artifacts := &mockArtifactReader{err: errors.New("file not found")}
	orch := testOrchestrator(
		artifacts,
		&mockWorkflowResolver{},
		&mockRunStore{},
		&mockEventEmitter{},
	)

	_, err := orch.StartRun(context.Background(), "tasks/missing.md")
	if err == nil {
		t.Fatal("expected error when artifact read fails")
	}
}

func TestStartRun_WorkflowResolveFails(t *testing.T) {
	artifacts := &mockArtifactReader{
		artifact: &domain.Artifact{Type: "task"},
	}
	resolver := &mockWorkflowResolver{
		err: domain.NewError(domain.ErrWorkflowNotFound, "no workflow found"),
	}
	orch := testOrchestrator(
		artifacts,
		resolver,
		&mockRunStore{},
		&mockEventEmitter{},
	)

	_, err := orch.StartRun(context.Background(), "tasks/task.md")
	if err == nil {
		t.Fatal("expected error when workflow resolve fails")
	}
}

func TestStartRun_NoEntryStep(t *testing.T) {
	artifacts := &mockArtifactReader{
		artifact: &domain.Artifact{Type: "task"},
	}
	resolver := &mockWorkflowResolver{
		result: &workflow.BindingResult{
			Workflow:  &domain.WorkflowDefinition{ID: "wf-task", EntryStep: ""},
			CommitSHA: "abc",
		},
	}
	orch := testOrchestrator(
		artifacts,
		resolver,
		&mockRunStore{},
		&mockEventEmitter{},
	)

	_, err := orch.StartRun(context.Background(), "tasks/task.md")
	if err == nil {
		t.Fatal("expected error for missing entry step")
	}
}

func TestStartRun_StoreCreateFails(t *testing.T) {
	artifacts := &mockArtifactReader{
		artifact: &domain.Artifact{Type: "task"},
	}
	resolver := &mockWorkflowResolver{
		result: &workflow.BindingResult{
			Workflow:  &domain.WorkflowDefinition{ID: "wf-task", EntryStep: "start"},
			CommitSHA: "abc",
		},
	}
	store := &mockRunStore{createRunErr: errors.New("db error")}
	orch := testOrchestrator(artifacts, resolver, store, &mockEventEmitter{})

	_, err := orch.StartRun(context.Background(), "tasks/task.md")
	if err == nil {
		t.Fatal("expected error when store create fails")
	}
}

// ── CompleteRun tests ──

func TestCompleteRun_HappyPath(t *testing.T) {
	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {
				RunID:   "run-1",
				Status:  domain.RunStatusActive,
				TraceID: "trace-1234567890ab",
			},
		},
	}
	events := &mockEventEmitter{}
	orch := testOrchestrator(&mockArtifactReader{}, &mockWorkflowResolver{}, store, events)

	err := orch.CompleteRun(context.Background(), "run-1", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(store.statusCalls) != 1 {
		t.Fatalf("expected 1 status update, got %d", len(store.statusCalls))
	}
	if store.statusCalls[0].status != domain.RunStatusCompleted {
		t.Errorf("expected completed, got %s", store.statusCalls[0].status)
	}

	if len(events.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events.events))
	}
	if events.events[0].Type != domain.EventRunCompleted {
		t.Errorf("expected run_completed, got %s", events.events[0].Type)
	}
}

func TestCompleteRun_InvalidState(t *testing.T) {
	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {
				RunID:   "run-1",
				Status:  domain.RunStatusCompleted,
				TraceID: "trace-1234567890ab",
			},
		},
	}
	orch := testOrchestrator(&mockArtifactReader{}, &mockWorkflowResolver{}, store, &mockEventEmitter{})

	err := orch.CompleteRun(context.Background(), "run-1", false)
	if err == nil {
		t.Fatal("expected error for already-completed run")
	}
}

func TestCompleteRun_NotFound(t *testing.T) {
	store := &mockRunStore{}
	orch := testOrchestrator(&mockArtifactReader{}, &mockWorkflowResolver{}, store, &mockEventEmitter{})

	err := orch.CompleteRun(context.Background(), "run-missing", false)
	if err == nil {
		t.Fatal("expected error for missing run")
	}
}

func TestCompleteRun_WithCommit(t *testing.T) {
	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {
				RunID:   "run-1",
				Status:  domain.RunStatusActive,
				TraceID: "trace-1234567890ab",
			},
		},
	}
	events := &mockEventEmitter{}
	orch := testOrchestrator(&mockArtifactReader{}, &mockWorkflowResolver{}, store, events)

	err := orch.CompleteRun(context.Background(), "run-1", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if store.statusCalls[0].status != domain.RunStatusCommitting {
		t.Errorf("expected committing, got %s", store.statusCalls[0].status)
	}
}

// ── FailRun tests ──

func TestFailRun_HappyPath(t *testing.T) {
	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {
				RunID:   "run-1",
				Status:  domain.RunStatusActive,
				TraceID: "trace-1234567890ab",
			},
		},
	}
	events := &mockEventEmitter{}
	orch := testOrchestrator(&mockArtifactReader{}, &mockWorkflowResolver{}, store, events)

	err := orch.FailRun(context.Background(), "run-1", "step timed out")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(store.statusCalls) != 1 {
		t.Fatalf("expected 1 status update, got %d", len(store.statusCalls))
	}
	if store.statusCalls[0].status != domain.RunStatusFailed {
		t.Errorf("expected failed, got %s", store.statusCalls[0].status)
	}

	if len(events.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events.events))
	}
	if events.events[0].Type != domain.EventRunFailed {
		t.Errorf("expected run_failed, got %s", events.events[0].Type)
	}
	if events.events[0].Payload == nil {
		t.Error("expected event payload with reason")
	}
}

func TestFailRun_InvalidState(t *testing.T) {
	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {
				RunID:   "run-1",
				Status:  domain.RunStatusFailed,
				TraceID: "trace-1234567890ab",
			},
		},
	}
	orch := testOrchestrator(&mockArtifactReader{}, &mockWorkflowResolver{}, store, &mockEventEmitter{})

	err := orch.FailRun(context.Background(), "run-1", "reason")
	if err == nil {
		t.Fatal("expected error for already-failed run")
	}
}

func TestFailRun_FromPending(t *testing.T) {
	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {
				RunID:   "run-1",
				Status:  domain.RunStatusPending,
				TraceID: "trace-1234567890ab",
			},
		},
	}
	orch := testOrchestrator(&mockArtifactReader{}, &mockWorkflowResolver{}, store, &mockEventEmitter{})

	err := orch.FailRun(context.Background(), "run-1", "reason")
	if err == nil {
		t.Fatal("expected error for pending run (step.failed_permanently invalid for pending)")
	}
}

// ── CancelRun tests ──

func TestCancelRun_FromActive(t *testing.T) {
	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {
				RunID:   "run-1",
				Status:  domain.RunStatusActive,
				TraceID: "trace-1234567890ab",
			},
		},
	}
	events := &mockEventEmitter{}
	orch := testOrchestrator(&mockArtifactReader{}, &mockWorkflowResolver{}, store, events)

	err := orch.CancelRun(context.Background(), "run-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if store.statusCalls[0].status != domain.RunStatusCancelled {
		t.Errorf("expected cancelled, got %s", store.statusCalls[0].status)
	}
	if events.events[0].Type != domain.EventRunCancelled {
		t.Errorf("expected run_cancelled, got %s", events.events[0].Type)
	}
}

func TestCancelRun_FromPaused(t *testing.T) {
	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {
				RunID:   "run-1",
				Status:  domain.RunStatusPaused,
				TraceID: "trace-1234567890ab",
			},
		},
	}
	events := &mockEventEmitter{}
	orch := testOrchestrator(&mockArtifactReader{}, &mockWorkflowResolver{}, store, events)

	err := orch.CancelRun(context.Background(), "run-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if store.statusCalls[0].status != domain.RunStatusCancelled {
		t.Errorf("expected cancelled, got %s", store.statusCalls[0].status)
	}
}

func TestCancelRun_InvalidState(t *testing.T) {
	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {
				RunID:   "run-1",
				Status:  domain.RunStatusCompleted,
				TraceID: "trace-1234567890ab",
			},
		},
	}
	orch := testOrchestrator(&mockArtifactReader{}, &mockWorkflowResolver{}, store, &mockEventEmitter{})

	err := orch.CancelRun(context.Background(), "run-1")
	if err == nil {
		t.Fatal("expected error for completed run")
	}
}

func TestCancelRun_NotFound(t *testing.T) {
	store := &mockRunStore{}
	orch := testOrchestrator(&mockArtifactReader{}, &mockWorkflowResolver{}, store, &mockEventEmitter{})

	err := orch.CancelRun(context.Background(), "run-missing")
	if err == nil {
		t.Fatal("expected error for missing run")
	}
}

// ── findStepDef tests ──

func TestFindStepDef(t *testing.T) {
	wf := &domain.WorkflowDefinition{
		Steps: []domain.StepDefinition{
			{ID: "start", Name: "Start Step"},
			{ID: "review", Name: "Review Step"},
		},
	}

	step := findStepDef(wf, "review")
	if step == nil {
		t.Fatal("expected to find step")
	}
	if step.Name != "Review Step" {
		t.Errorf("expected Review Step, got %s", step.Name)
	}

	missing := findStepDef(wf, "nonexistent")
	if missing != nil {
		t.Error("expected nil for missing step")
	}
}
