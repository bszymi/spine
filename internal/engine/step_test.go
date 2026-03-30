package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/bszymi/spine/internal/actor"
	"github.com/bszymi/spine/internal/domain"
)

// ── Validator mock ──

type mockValidator struct {
	result domain.ValidationResult
}

func (m *mockValidator) Validate(_ context.Context, _ string) domain.ValidationResult {
	return m.result
}

// ── Step-specific mocks ──

type mockWorkflowLoader struct {
	wfDef *domain.WorkflowDefinition
	err   error
}

func (m *mockWorkflowLoader) LoadWorkflow(_ context.Context, _, _ string) (*domain.WorkflowDefinition, error) {
	return m.wfDef, m.err
}

type mockActorAssigner struct {
	delivered []actor.AssignmentRequest
	err       error
}

func (m *mockActorAssigner) DeliverAssignment(_ context.Context, req actor.AssignmentRequest) error {
	m.delivered = append(m.delivered, req)
	return m.err
}

func (m *mockActorAssigner) ProcessResult(_ context.Context, _ actor.AssignmentRequest, _ actor.AssignmentResult) error {
	return nil
}

// Helper to build an orchestrator for step tests.
func stepTestOrchestrator(
	store *mockRunStore,
	events *mockEventEmitter,
	wfLoader *mockWorkflowLoader,
	artifacts ArtifactReader,
	actors *mockActorAssigner,
) *Orchestrator {
	if artifacts == nil {
		artifacts = &mockArtifactReader{}
	}
	if actors == nil {
		actors = &mockActorAssigner{}
	}
	return &Orchestrator{
		workflows: &mockWorkflowResolver{},
		store:     store,
		actors:    actors,
		artifacts: artifacts,
		events:    events,
		git:       &stubGitOperator{},
		wfLoader:  wfLoader,
	}
}

func testWorkflow() *domain.WorkflowDefinition {
	return &domain.WorkflowDefinition{
		ID:        "wf-test",
		EntryStep: "start",
		Steps: []domain.StepDefinition{
			{
				ID:   "start",
				Name: "Start",
				Type: domain.StepTypeAutomated,
				Outcomes: []domain.OutcomeDefinition{
					{ID: "approved", Name: "Approved", NextStep: "review"},
					{ID: "done", Name: "Done"},
				},
			},
			{
				ID:   "review",
				Name: "Review",
				Type: domain.StepTypeReview,
				Outcomes: []domain.OutcomeDefinition{
					{ID: "accepted", Name: "Accepted"},
					{ID: "rejected", Name: "Rejected", NextStep: "start"},
				},
			},
		},
	}
}

func testRunWithStep() (*mockRunStore, string) {
	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {
				RunID:           "run-1",
				Status:          domain.RunStatusActive,
				TraceID:         "trace-1234567890ab",
				TaskPath:        "tasks/task.md",
				WorkflowPath:    "workflows/test.yaml",
				WorkflowVersion: "abc123",
				WorkflowID:      "wf-test",
				CurrentStepID:   "start",
			},
		},
	}
	// Pre-create a step execution.
	store.createdSteps = []*domain.StepExecution{
		{
			ExecutionID: "run-1-start-1",
			RunID:       "run-1",
			StepID:      "start",
			Status:      domain.StepStatusWaiting,
			Attempt:     1,
		},
	}
	return store, "run-1"
}

// ── ActivateStep tests ──

func TestActivateStep_HappyPath(t *testing.T) {
	store, runID := testRunWithStep()
	events := &mockEventEmitter{}
	actors := &mockActorAssigner{}
	loader := &mockWorkflowLoader{wfDef: testWorkflow()}

	orch := stepTestOrchestrator(store, events, loader, nil, actors)
	err := orch.ActivateStep(context.Background(), runID, "start")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Step should be assigned.
	if store.createdSteps[0].Status != domain.StepStatusAssigned {
		t.Errorf("expected assigned, got %s", store.createdSteps[0].Status)
	}

	// Actor should have received assignment.
	if len(actors.delivered) != 1 {
		t.Fatalf("expected 1 assignment, got %d", len(actors.delivered))
	}
	if actors.delivered[0].StepID != "start" {
		t.Errorf("expected step ID start, got %s", actors.delivered[0].StepID)
	}
	if len(actors.delivered[0].Constraints.ExpectedOutcomes) != 2 {
		t.Errorf("expected 2 outcomes, got %d", len(actors.delivered[0].Constraints.ExpectedOutcomes))
	}

	// Event should be emitted.
	if len(events.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events.events))
	}
	if events.events[0].Type != domain.EventStepAssigned {
		t.Errorf("expected step_assigned, got %s", events.events[0].Type)
	}
}

func TestActivateStep_PreconditionFails(t *testing.T) {
	store, runID := testRunWithStep()
	events := &mockEventEmitter{}

	// Workflow with a precondition that will fail.
	wf := testWorkflow()
	wf.Steps[0].Preconditions = []domain.Precondition{
		{Type: "artifact_status", Config: map[string]string{"status": "Completed"}},
	}
	loader := &mockWorkflowLoader{wfDef: wf}

	// Artifact reader returns a task with status "Pending" (not "Completed").
	artifacts := &mockArtifactReader{
		artifact: &domain.Artifact{
			Type:   "task",
			Status: "Pending",
			Path:   "tasks/task.md",
		},
	}

	orch := stepTestOrchestrator(store, events, loader, artifacts, nil)
	err := orch.ActivateStep(context.Background(), runID, "start")
	if err == nil {
		t.Fatal("expected precondition error")
	}

	var spineErr *domain.SpineError
	if !errors.As(err, &spineErr) {
		t.Fatalf("expected SpineError, got %T", err)
	}
	if spineErr.Code != domain.ErrPrecondition {
		t.Errorf("expected precondition_failed, got %s", spineErr.Code)
	}

	// Step should still be waiting (not transitioned).
	if store.createdSteps[0].Status != domain.StepStatusWaiting {
		t.Errorf("expected step to remain waiting, got %s", store.createdSteps[0].Status)
	}
}

func TestActivateStep_WorkflowLoadFails(t *testing.T) {
	store, runID := testRunWithStep()
	loader := &mockWorkflowLoader{err: errors.New("git error")}

	orch := stepTestOrchestrator(store, &mockEventEmitter{}, loader, nil, nil)
	err := orch.ActivateStep(context.Background(), runID, "start")
	if err == nil {
		t.Fatal("expected error when workflow load fails")
	}
}

func TestActivateStep_StepNotFound(t *testing.T) {
	store, runID := testRunWithStep()
	loader := &mockWorkflowLoader{wfDef: testWorkflow()}

	orch := stepTestOrchestrator(store, &mockEventEmitter{}, loader, nil, nil)
	err := orch.ActivateStep(context.Background(), runID, "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing step")
	}
}

func TestActivateStep_RunNotFound(t *testing.T) {
	store := &mockRunStore{}
	loader := &mockWorkflowLoader{wfDef: testWorkflow()}

	orch := stepTestOrchestrator(store, &mockEventEmitter{}, loader, nil, nil)
	err := orch.ActivateStep(context.Background(), "missing-run", "start")
	if err == nil {
		t.Fatal("expected error for missing run")
	}
}

// ── SubmitStepResult tests ──

func TestSubmitStepResult_HappyPath_NextStep(t *testing.T) {
	store, _ := testRunWithStep()
	// Set step to in_progress for submission.
	store.createdSteps[0].Status = domain.StepStatusInProgress
	events := &mockEventEmitter{}
	loader := &mockWorkflowLoader{wfDef: testWorkflow()}

	orch := stepTestOrchestrator(store, events, loader, nil, nil)
	err := orch.SubmitStepResult(context.Background(), "run-1-start-1", StepResult{
		OutcomeID: "approved",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Step should be completed.
	if store.createdSteps[0].Status != domain.StepStatusCompleted {
		t.Errorf("expected completed, got %s", store.createdSteps[0].Status)
	}
	if store.createdSteps[0].OutcomeID != "approved" {
		t.Errorf("expected outcome approved, got %s", store.createdSteps[0].OutcomeID)
	}

	// Next step (review) should be created.
	if len(store.createdSteps) != 2 {
		t.Fatalf("expected 2 steps total, got %d", len(store.createdSteps))
	}
	if store.createdSteps[1].StepID != "review" {
		t.Errorf("expected next step review, got %s", store.createdSteps[1].StepID)
	}

	// Step completed event should be emitted.
	found := false
	for _, evt := range events.events {
		if evt.Type == domain.EventStepCompleted {
			found = true
		}
	}
	if !found {
		t.Error("expected step_completed event")
	}
}

func TestSubmitStepResult_Terminal(t *testing.T) {
	store, _ := testRunWithStep()
	store.createdSteps[0].Status = domain.StepStatusInProgress
	events := &mockEventEmitter{}
	loader := &mockWorkflowLoader{wfDef: testWorkflow()}

	orch := stepTestOrchestrator(store, events, loader, nil, nil)
	// "done" outcome has no NextStep → terminal.
	err := orch.SubmitStepResult(context.Background(), "run-1-start-1", StepResult{
		OutcomeID: "done",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Run should be completed.
	if store.runs["run-1"].Status != domain.RunStatusCompleted {
		t.Errorf("expected run completed, got %s", store.runs["run-1"].Status)
	}
}

func TestSubmitStepResult_AutoAcknowledge(t *testing.T) {
	store, _ := testRunWithStep()
	// Step is assigned (not yet in_progress).
	store.createdSteps[0].Status = domain.StepStatusAssigned
	loader := &mockWorkflowLoader{wfDef: testWorkflow()}

	orch := stepTestOrchestrator(store, &mockEventEmitter{}, loader, nil, nil)
	err := orch.SubmitStepResult(context.Background(), "run-1-start-1", StepResult{
		OutcomeID: "done",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Step should have been auto-acknowledged then completed.
	if store.createdSteps[0].Status != domain.StepStatusCompleted {
		t.Errorf("expected completed, got %s", store.createdSteps[0].Status)
	}
	if store.createdSteps[0].StartedAt == nil {
		t.Error("expected StartedAt to be set from auto-acknowledge")
	}
}

func TestSubmitStepResult_InvalidOutcome(t *testing.T) {
	store, _ := testRunWithStep()
	store.createdSteps[0].Status = domain.StepStatusInProgress
	loader := &mockWorkflowLoader{wfDef: testWorkflow()}

	orch := stepTestOrchestrator(store, &mockEventEmitter{}, loader, nil, nil)
	err := orch.SubmitStepResult(context.Background(), "run-1-start-1", StepResult{
		OutcomeID: "invalid_outcome",
	})
	if err == nil {
		t.Fatal("expected error for invalid outcome")
	}
}

func TestSubmitStepResult_ExecutionNotFound(t *testing.T) {
	store := &mockRunStore{}
	loader := &mockWorkflowLoader{wfDef: testWorkflow()}

	orch := stepTestOrchestrator(store, &mockEventEmitter{}, loader, nil, nil)
	err := orch.SubmitStepResult(context.Background(), "missing-exec", StepResult{
		OutcomeID: "done",
	})
	if err == nil {
		t.Fatal("expected error for missing execution")
	}
}

// ── Precondition tests ──

func TestCheckArtifactStatus(t *testing.T) {
	artifacts := &mockArtifactReader{
		artifact: &domain.Artifact{Status: "Completed"},
	}
	orch := &Orchestrator{artifacts: artifacts}
	run := &domain.Run{TaskPath: "tasks/task.md"}

	// Match.
	if !orch.checkArtifactStatus(context.Background(), map[string]string{"status": "Completed"}, run) {
		t.Error("expected precondition to pass")
	}

	// Mismatch.
	if orch.checkArtifactStatus(context.Background(), map[string]string{"status": "Pending"}, run) {
		t.Error("expected precondition to fail")
	}
}

func TestCheckFieldPresent(t *testing.T) {
	artifacts := &mockArtifactReader{
		artifact: &domain.Artifact{Metadata: map[string]string{"owner": "alice"}},
	}
	orch := &Orchestrator{artifacts: artifacts}
	run := &domain.Run{TaskPath: "tasks/task.md"}

	if !orch.checkFieldPresent(context.Background(), map[string]string{"field": "owner"}, run) {
		t.Error("expected present field to pass")
	}
	if orch.checkFieldPresent(context.Background(), map[string]string{"field": "missing"}, run) {
		t.Error("expected missing field to fail")
	}
}

func TestCheckFieldValue(t *testing.T) {
	artifacts := &mockArtifactReader{
		artifact: &domain.Artifact{Metadata: map[string]string{"priority": "high"}},
	}
	orch := &Orchestrator{artifacts: artifacts}
	run := &domain.Run{TaskPath: "tasks/task.md"}

	if !orch.checkFieldValue(context.Background(), map[string]string{"field": "priority", "value": "high"}, run) {
		t.Error("expected matching value to pass")
	}
	if orch.checkFieldValue(context.Background(), map[string]string{"field": "priority", "value": "low"}, run) {
		t.Error("expected mismatched value to fail")
	}
}

func TestCheckLinksExist(t *testing.T) {
	artifacts := &mockArtifactReader{
		artifact: &domain.Artifact{
			Links: []domain.Link{
				{Type: domain.LinkTypeParent, Target: "/epics/e1.md"},
			},
		},
	}
	orch := &Orchestrator{artifacts: artifacts}
	run := &domain.Run{TaskPath: "tasks/task.md"}

	if !orch.checkLinksExist(context.Background(), map[string]string{"link_type": "parent"}, run) {
		t.Error("expected existing link to pass")
	}
	if orch.checkLinksExist(context.Background(), map[string]string{"link_type": "blocked_by"}, run) {
		t.Error("expected missing link type to fail")
	}
}

func TestEvaluatePreconditions_NoPreconditions(t *testing.T) {
	orch := &Orchestrator{artifacts: &mockArtifactReader{}}
	step := &domain.StepDefinition{}
	run := &domain.Run{TaskPath: "tasks/task.md"}

	passed, _ := orch.evaluatePreconditions(context.Background(), step, run)
	if !passed {
		t.Error("expected pass with no preconditions")
	}
}

func TestEvaluatePreconditions_UnknownType(t *testing.T) {
	orch := &Orchestrator{artifacts: &mockArtifactReader{}}
	step := &domain.StepDefinition{
		Preconditions: []domain.Precondition{
			{Type: "future_type", Config: map[string]string{}},
		},
	}
	run := &domain.Run{TaskPath: "tasks/task.md"}

	// Unknown types are skipped — should pass.
	passed, _ := orch.evaluatePreconditions(context.Background(), step, run)
	if !passed {
		t.Error("expected pass with unknown precondition type")
	}
}

func TestEvaluatePreconditions_AllTypes(t *testing.T) {
	artifacts := &mockArtifactReader{
		artifact: &domain.Artifact{
			Status:   "Completed",
			Metadata: map[string]string{"owner": "alice", "priority": "high"},
			Links:    []domain.Link{{Type: domain.LinkTypeParent, Target: "/epics/e1.md"}},
		},
	}
	orch := &Orchestrator{artifacts: artifacts}
	run := &domain.Run{TaskPath: "tasks/task.md"}

	step := &domain.StepDefinition{
		Preconditions: []domain.Precondition{
			{Type: "artifact_status", Config: map[string]string{"status": "Completed"}},
			{Type: "field_present", Config: map[string]string{"field": "owner"}},
			{Type: "field_value", Config: map[string]string{"field": "priority", "value": "high"}},
			{Type: "links_exist", Config: map[string]string{"link_type": "parent"}},
		},
	}

	passed, _ := orch.evaluatePreconditions(context.Background(), step, run)
	if !passed {
		t.Error("expected all preconditions to pass")
	}
}

func TestEvaluatePreconditions_FieldPresentFails(t *testing.T) {
	artifacts := &mockArtifactReader{
		artifact: &domain.Artifact{Metadata: map[string]string{}},
	}
	orch := &Orchestrator{artifacts: artifacts}
	run := &domain.Run{TaskPath: "tasks/task.md"}

	step := &domain.StepDefinition{
		Preconditions: []domain.Precondition{
			{Type: "field_present", Config: map[string]string{"field": "missing"}},
		},
	}

	passed, _ := orch.evaluatePreconditions(context.Background(), step, run)
	if passed {
		t.Error("expected field_present precondition to fail")
	}
}

func TestEvaluatePreconditions_FieldValueFails(t *testing.T) {
	artifacts := &mockArtifactReader{
		artifact: &domain.Artifact{Metadata: map[string]string{"priority": "low"}},
	}
	orch := &Orchestrator{artifacts: artifacts}
	run := &domain.Run{TaskPath: "tasks/task.md"}

	step := &domain.StepDefinition{
		Preconditions: []domain.Precondition{
			{Type: "field_value", Config: map[string]string{"field": "priority", "value": "high"}},
		},
	}

	passed, _ := orch.evaluatePreconditions(context.Background(), step, run)
	if passed {
		t.Error("expected field_value precondition to fail")
	}
}

func TestEvaluatePreconditions_LinksExistFails(t *testing.T) {
	artifacts := &mockArtifactReader{
		artifact: &domain.Artifact{Links: []domain.Link{}},
	}
	orch := &Orchestrator{artifacts: artifacts}
	run := &domain.Run{TaskPath: "tasks/task.md"}

	step := &domain.StepDefinition{
		Preconditions: []domain.Precondition{
			{Type: "links_exist", Config: map[string]string{"link_type": "parent"}},
		},
	}

	passed, _ := orch.evaluatePreconditions(context.Background(), step, run)
	if passed {
		t.Error("expected links_exist precondition to fail")
	}
}

func TestCheckArtifactStatus_WithExplicitPath(t *testing.T) {
	artifacts := &mockArtifactReader{
		artifact: &domain.Artifact{Status: "Active"},
	}
	orch := &Orchestrator{artifacts: artifacts}
	run := &domain.Run{TaskPath: "tasks/task.md"}

	if !orch.checkArtifactStatus(context.Background(), map[string]string{"path": "other.md", "status": "Active"}, run) {
		t.Error("expected pass with explicit path")
	}
}

func TestCheckArtifactStatus_ReadError(t *testing.T) {
	artifacts := &mockArtifactReader{err: errors.New("not found")}
	orch := &Orchestrator{artifacts: artifacts}
	run := &domain.Run{TaskPath: "tasks/task.md"}

	if orch.checkArtifactStatus(context.Background(), map[string]string{"status": "Active"}, run) {
		t.Error("expected fail on read error")
	}
}

// ── Cross-artifact validation precondition tests ──

func TestActivateStep_CrossArtifactValid_Passes(t *testing.T) {
	store, runID := testRunWithStep()
	events := &mockEventEmitter{}
	actors := &mockActorAssigner{}

	wf := testWorkflow()
	wf.Steps[0].Preconditions = []domain.Precondition{
		{Type: "cross_artifact_valid", Config: map[string]string{}},
	}
	loader := &mockWorkflowLoader{wfDef: wf}

	validator := &mockValidator{
		result: domain.ValidationResult{Status: "passed"},
	}

	orch := stepTestOrchestrator(store, events, loader, nil, actors)
	orch.WithValidator(validator)

	err := orch.ActivateStep(context.Background(), runID, "start")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Step should be assigned (precondition passed).
	if store.createdSteps[0].Status != domain.StepStatusAssigned {
		t.Errorf("expected assigned, got %s", store.createdSteps[0].Status)
	}
}

func TestActivateStep_CrossArtifactValid_Fails(t *testing.T) {
	store, runID := testRunWithStep()
	events := &mockEventEmitter{}

	wf := testWorkflow()
	wf.Steps[0].Preconditions = []domain.Precondition{
		{Type: "cross_artifact_valid", Config: map[string]string{}},
	}
	loader := &mockWorkflowLoader{wfDef: wf}

	validator := &mockValidator{
		result: domain.ValidationResult{
			Status: "failed",
			Errors: []domain.ValidationError{
				{RuleID: "LC-001", Classification: domain.ViolationStructuralError, ArtifactPath: "tasks/task.md", Severity: "error", Message: "broken parent link"},
			},
		},
	}

	orch := stepTestOrchestrator(store, events, loader, nil, nil)
	orch.WithValidator(validator)

	err := orch.ActivateStep(context.Background(), runID, "start")
	if err == nil {
		t.Fatal("expected precondition error")
	}

	var spineErr *domain.SpineError
	if !errors.As(err, &spineErr) {
		t.Fatalf("expected SpineError, got %T", err)
	}
	if spineErr.Code != domain.ErrPrecondition {
		t.Errorf("expected precondition_failed, got %s", spineErr.Code)
	}

	// Error detail should include validation result.
	if spineErr.Detail == nil {
		t.Fatal("expected error detail with validation result")
	}

	// Step should remain waiting.
	if store.createdSteps[0].Status != domain.StepStatusWaiting {
		t.Errorf("expected step to remain waiting, got %s", store.createdSteps[0].Status)
	}

	// Validation errors should be persisted on step execution.
	if store.createdSteps[0].ErrorDetail == nil {
		t.Fatal("expected error detail on step execution")
	}
	if store.createdSteps[0].ErrorDetail.Classification != domain.FailureValidation {
		t.Errorf("expected validation_failed classification, got %s", store.createdSteps[0].ErrorDetail.Classification)
	}
	if store.createdSteps[0].ErrorDetail.Message != "broken parent link" {
		t.Errorf("expected error message, got %s", store.createdSteps[0].ErrorDetail.Message)
	}

	// Violations should be persisted with category classification.
	if len(store.createdSteps[0].ErrorDetail.Violations) != 1 {
		t.Fatalf("expected 1 violation, got %d", len(store.createdSteps[0].ErrorDetail.Violations))
	}
	v := store.createdSteps[0].ErrorDetail.Violations[0]
	if v.Classification != domain.ViolationStructuralError {
		t.Errorf("expected classification %s, got %s", domain.ViolationStructuralError, v.Classification)
	}
	if v.RuleID != "LC-001" {
		t.Errorf("expected rule_id LC-001, got %s", v.RuleID)
	}
}

func TestActivateStep_CrossArtifactValid_NoValidator(t *testing.T) {
	store, runID := testRunWithStep()
	events := &mockEventEmitter{}
	actors := &mockActorAssigner{}

	wf := testWorkflow()
	wf.Steps[0].Preconditions = []domain.Precondition{
		{Type: "cross_artifact_valid", Config: map[string]string{}},
	}
	loader := &mockWorkflowLoader{wfDef: wf}

	// No validator configured — should skip the precondition.
	orch := stepTestOrchestrator(store, events, loader, nil, actors)

	err := orch.ActivateStep(context.Background(), runID, "start")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if store.createdSteps[0].Status != domain.StepStatusAssigned {
		t.Errorf("expected assigned (skipped validation), got %s", store.createdSteps[0].Status)
	}
}

func TestActivateStep_CrossArtifactValid_CustomPath(t *testing.T) {
	store, runID := testRunWithStep()
	events := &mockEventEmitter{}
	actors := &mockActorAssigner{}

	wf := testWorkflow()
	wf.Steps[0].Preconditions = []domain.Precondition{
		{Type: "cross_artifact_valid", Config: map[string]string{"artifact_path": "epics/epic.md"}},
	}
	loader := &mockWorkflowLoader{wfDef: wf}

	var validatedPath string
	validator := &pathCapturingValidator{
		result: domain.ValidationResult{Status: "passed"},
		capture: func(path string) {
			validatedPath = path
		},
	}

	orch := stepTestOrchestrator(store, events, loader, nil, actors)
	orch.WithValidator(validator)

	err := orch.ActivateStep(context.Background(), runID, "start")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if validatedPath != "epics/epic.md" {
		t.Errorf("expected validation on epics/epic.md, got %s", validatedPath)
	}
}

func TestActivateStep_CrossArtifactValid_WarningsPass(t *testing.T) {
	store, runID := testRunWithStep()
	events := &mockEventEmitter{}
	actors := &mockActorAssigner{}

	wf := testWorkflow()
	wf.Steps[0].Preconditions = []domain.Precondition{
		{Type: "cross_artifact_valid", Config: map[string]string{}},
	}
	loader := &mockWorkflowLoader{wfDef: wf}

	validator := &mockValidator{
		result: domain.ValidationResult{
			Status: "warnings",
			Warnings: []domain.ValidationError{
				{RuleID: "SC-003", Severity: "warning", Message: "recommended field missing"},
			},
		},
	}

	orch := stepTestOrchestrator(store, events, loader, nil, actors)
	orch.WithValidator(validator)

	err := orch.ActivateStep(context.Background(), runID, "start")
	if err != nil {
		t.Fatalf("unexpected error: warnings should not block: %v", err)
	}

	if store.createdSteps[0].Status != domain.StepStatusAssigned {
		t.Errorf("expected assigned (warnings don't block), got %s", store.createdSteps[0].Status)
	}
}

func TestActivateStep_CrossArtifactValid_ClearsErrorOnRetry(t *testing.T) {
	store, runID := testRunWithStep()
	events := &mockEventEmitter{}
	actors := &mockActorAssigner{}

	wf := testWorkflow()
	wf.Steps[0].Preconditions = []domain.Precondition{
		{Type: "cross_artifact_valid", Config: map[string]string{}},
	}
	loader := &mockWorkflowLoader{wfDef: wf}

	// First attempt: validation fails, ErrorDetail is set.
	failValidator := &mockValidator{
		result: domain.ValidationResult{
			Status: "failed",
			Errors: []domain.ValidationError{
				{RuleID: "LC-001", Message: "broken parent link"},
			},
		},
	}
	orch := stepTestOrchestrator(store, events, loader, nil, actors)
	orch.WithValidator(failValidator)

	err := orch.ActivateStep(context.Background(), runID, "start")
	if err == nil {
		t.Fatal("expected precondition error on first attempt")
	}
	if store.createdSteps[0].ErrorDetail == nil {
		t.Fatal("expected error detail after failed validation")
	}

	// Second attempt: validation passes, ErrorDetail should be cleared.
	passValidator := &mockValidator{
		result: domain.ValidationResult{Status: "passed"},
	}
	orch.WithValidator(passValidator)

	err = orch.ActivateStep(context.Background(), runID, "start")
	if err != nil {
		t.Fatalf("unexpected error on retry: %v", err)
	}
	if store.createdSteps[0].ErrorDetail != nil {
		t.Error("expected ErrorDetail to be cleared after successful activation")
	}
	if store.createdSteps[0].Status != domain.StepStatusAssigned {
		t.Errorf("expected assigned, got %s", store.createdSteps[0].Status)
	}
}

func TestEvaluatePreconditions_CrossArtifactValid_MultipleErrors(t *testing.T) {
	validator := &mockValidator{
		result: domain.ValidationResult{
			Status: "failed",
			Errors: []domain.ValidationError{
				{RuleID: "LC-001", Message: "broken parent link"},
				{RuleID: "SI-002", Message: "invalid path structure"},
			},
		},
	}
	orch := &Orchestrator{artifacts: &mockArtifactReader{}, validator: validator}
	run := &domain.Run{TaskPath: "tasks/task.md"}

	step := &domain.StepDefinition{
		Preconditions: []domain.Precondition{
			{Type: "cross_artifact_valid", Config: map[string]string{}},
		},
	}

	passed, result := orch.evaluatePreconditions(context.Background(), step, run)
	if passed {
		t.Error("expected precondition to fail")
	}
	if result == nil {
		t.Fatal("expected validation result")
	}
	if len(result.Errors) != 2 {
		t.Errorf("expected 2 errors, got %d", len(result.Errors))
	}
}

func TestSummarizeValidationErrors(t *testing.T) {
	// Single error.
	single := summarizeValidationErrors([]domain.ValidationError{
		{Message: "broken link"},
	})
	if single != "broken link" {
		t.Errorf("expected single error message, got %s", single)
	}

	// Multiple errors.
	multi := summarizeValidationErrors([]domain.ValidationError{
		{Message: "broken link"},
		{Message: "invalid path"},
	})
	if multi != "2 validation errors: broken link; invalid path" {
		t.Errorf("unexpected summary: %s", multi)
	}

	// Empty.
	empty := summarizeValidationErrors(nil)
	if empty != "validation failed" {
		t.Errorf("expected default message, got %s", empty)
	}
}

// pathCapturingValidator records which path was validated.
type pathCapturingValidator struct {
	result  domain.ValidationResult
	capture func(string)
}

func (v *pathCapturingValidator) Validate(_ context.Context, path string) domain.ValidationResult {
	if v.capture != nil {
		v.capture(path)
	}
	return v.result
}

func TestFindStepExecution_NoMatch(t *testing.T) {
	store := &mockRunStore{
		createdSteps: []*domain.StepExecution{
			{ExecutionID: "e1", RunID: "run-1", StepID: "start", Status: domain.StepStatusCompleted},
		},
	}
	orch := &Orchestrator{store: store}

	_, err := orch.findStepExecution(context.Background(), "run-1", "start")
	if err == nil {
		t.Fatal("expected error for no active execution")
	}
}

func TestNewGitWorkflowLoader(t *testing.T) {
	gc := &stubGitClient{headSHA: "abc"}
	loader := NewGitWorkflowLoader(gc)
	if loader.gitClient != gc {
		t.Error("expected gitClient to be stored")
	}
}

func TestFindOutcome(t *testing.T) {
	step := &domain.StepDefinition{
		Outcomes: []domain.OutcomeDefinition{
			{ID: "approved", Name: "Approved"},
			{ID: "rejected", Name: "Rejected"},
		},
	}

	o := findOutcome(step, "approved")
	if o == nil || o.Name != "Approved" {
		t.Error("expected to find approved outcome")
	}

	o = findOutcome(step, "missing")
	if o != nil {
		t.Error("expected nil for missing outcome")
	}
}

// ── Discussion precondition tests ──

type mockDiscussionChecker struct {
	hasOpen bool
	err     error
}

func (m *mockDiscussionChecker) HasOpenThreads(_ context.Context, _ domain.AnchorType, _ string) (bool, error) {
	return m.hasOpen, m.err
}

func TestEvaluatePreconditions_DiscussionsResolved_Pass(t *testing.T) {
	orch := &Orchestrator{
		artifacts:   &mockArtifactReader{},
		discussions: &mockDiscussionChecker{hasOpen: false},
	}
	step := &domain.StepDefinition{
		Preconditions: []domain.Precondition{
			{Type: "discussions_resolved", Config: map[string]string{}},
		},
	}
	run := &domain.Run{TaskPath: "tasks/task.md"}

	passed, _ := orch.evaluatePreconditions(context.Background(), step, run)
	if !passed {
		t.Error("expected discussions_resolved to pass when no open threads")
	}
}

func TestEvaluatePreconditions_DiscussionsResolved_Fail(t *testing.T) {
	orch := &Orchestrator{
		artifacts:   &mockArtifactReader{},
		discussions: &mockDiscussionChecker{hasOpen: true},
	}
	step := &domain.StepDefinition{
		Preconditions: []domain.Precondition{
			{Type: "discussions_resolved", Config: map[string]string{}},
		},
	}
	run := &domain.Run{TaskPath: "tasks/task.md"}

	passed, _ := orch.evaluatePreconditions(context.Background(), step, run)
	if passed {
		t.Error("expected discussions_resolved to fail when open threads exist")
	}
}

func TestEvaluatePreconditions_DiscussionsResolved_NoChecker(t *testing.T) {
	orch := &Orchestrator{
		artifacts: &mockArtifactReader{},
		// discussions is nil
	}
	step := &domain.StepDefinition{
		Preconditions: []domain.Precondition{
			{Type: "discussions_resolved", Config: map[string]string{}},
		},
	}
	run := &domain.Run{TaskPath: "tasks/task.md"}

	// Should pass (skip) when no checker configured
	passed, _ := orch.evaluatePreconditions(context.Background(), step, run)
	if !passed {
		t.Error("expected discussions_resolved to pass when no checker configured")
	}
}

func TestEvaluatePreconditions_DiscussionsResolved_Error(t *testing.T) {
	orch := &Orchestrator{
		artifacts:   &mockArtifactReader{},
		discussions: &mockDiscussionChecker{err: errors.New("db error")},
	}
	step := &domain.StepDefinition{
		Preconditions: []domain.Precondition{
			{Type: "discussions_resolved", Config: map[string]string{}},
		},
	}
	run := &domain.Run{TaskPath: "tasks/task.md"}

	passed, _ := orch.evaluatePreconditions(context.Background(), step, run)
	if passed {
		t.Error("expected discussions_resolved to fail on error")
	}
}

func TestEvaluatePreconditions_DiscussionsResolved_CustomAnchor(t *testing.T) {
	checker := &mockDiscussionChecker{hasOpen: false}
	orch := &Orchestrator{
		artifacts:   &mockArtifactReader{},
		discussions: checker,
	}
	step := &domain.StepDefinition{
		Preconditions: []domain.Precondition{
			{Type: "discussions_resolved", Config: map[string]string{
				"anchor_type": "run",
				"path":        "run-001",
			}},
		},
	}
	run := &domain.Run{TaskPath: "tasks/task.md"}

	passed, _ := orch.evaluatePreconditions(context.Background(), step, run)
	if !passed {
		t.Error("expected discussions_resolved to pass with custom anchor")
	}
}

// ── resolveReadRef tests ──

func TestResolveReadRef_StandardRun(t *testing.T) {
	run := &domain.Run{Mode: domain.RunModeStandard, BranchName: "spine/run/abc"}
	if ref := resolveReadRef(run); ref != "HEAD" {
		t.Errorf("expected HEAD for standard run, got %s", ref)
	}
}

func TestResolveReadRef_ZeroMode(t *testing.T) {
	run := &domain.Run{BranchName: "spine/run/abc"}
	if ref := resolveReadRef(run); ref != "HEAD" {
		t.Errorf("expected HEAD for zero mode, got %s", ref)
	}
}

func TestResolveReadRef_PlanningRun(t *testing.T) {
	run := &domain.Run{Mode: domain.RunModePlanning, BranchName: "spine/run/plan-123"}
	if ref := resolveReadRef(run); ref != "spine/run/plan-123" {
		t.Errorf("expected branch name, got %s", ref)
	}
}

func TestResolveReadRef_PlanningRunNoBranch(t *testing.T) {
	run := &domain.Run{Mode: domain.RunModePlanning}
	if ref := resolveReadRef(run); ref != "HEAD" {
		t.Errorf("expected HEAD when planning run has no branch, got %s", ref)
	}
}
