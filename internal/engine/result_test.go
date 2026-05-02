package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/bszymi/spine/internal/domain"
)

func resultTestOrchestrator(store *mockRunStore, wfLoader *mockWorkflowLoader) *Orchestrator {
	return &Orchestrator{
		workflows: &mockWorkflowResolver{},
		store:     store,
		actors:    &stubActorAssigner{},
		artifacts: &mockArtifactReader{},
		events:    &mockEventEmitter{},
		git:       &stubGitOperator{},
		wfLoader:  wfLoader,
	}
}

func resultTestWorkflow() *domain.WorkflowDefinition {
	return &domain.WorkflowDefinition{
		ID:        "wf-test",
		EntryStep: "start",
		Steps: []domain.StepDefinition{
			{
				ID:              "start",
				Name:            "Start",
				Type:            domain.StepTypeAutomated,
				RequiredOutputs: []string{"output.md"},
				Outcomes: []domain.OutcomeDefinition{
					{ID: "done", Name: "Done"},
				},
			},
			{
				ID:   "no-outputs",
				Name: "No Outputs Required",
				Type: domain.StepTypeAutomated,
				Outcomes: []domain.OutcomeDefinition{
					{ID: "done", Name: "Done"},
				},
			},
		},
	}
}

func TestIngestResult_HappyPath(t *testing.T) {
	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {
				RunID:           "run-1",
				Status:          domain.RunStatusActive,
				TraceID:         "trace-1234567890ab",
				WorkflowPath:    "workflows/test.yaml",
				WorkflowVersion: "abc",
			},
		},
		createdSteps: []*domain.StepExecution{
			{
				ExecutionID: "run-1-start-1",
				RunID:       "run-1",
				StepID:      "start",
				Status:      domain.StepStatusInProgress,
				Attempt:     1,
				ActorID:     "test-actor",
			},
		},
	}
	loader := &mockWorkflowLoader{wfDef: resultTestWorkflow()}
	orch := resultTestOrchestrator(store, loader)

	resp, err := orch.IngestResult(context.Background(), SubmitRequest{
		ExecutionID:       "run-1-start-1",
		OutcomeID:         "done",
		ArtifactsProduced: []string{"output.md"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != domain.StepStatusCompleted {
		t.Errorf("expected completed, got %s", resp.Status)
	}
	if resp.OutcomeID != "done" {
		t.Errorf("expected done, got %s", resp.OutcomeID)
	}
}

func TestIngestResult_MissingRequiredOutputs(t *testing.T) {
	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {
				RunID:           "run-1",
				Status:          domain.RunStatusActive,
				TraceID:         "trace-1234567890ab",
				WorkflowPath:    "workflows/test.yaml",
				WorkflowVersion: "abc",
			},
		},
		createdSteps: []*domain.StepExecution{
			{
				ExecutionID: "run-1-start-1",
				RunID:       "run-1",
				StepID:      "start",
				Status:      domain.StepStatusInProgress,
				Attempt:     1,
				ActorID:     "test-actor",
			},
		},
	}
	loader := &mockWorkflowLoader{wfDef: resultTestWorkflow()}
	orch := resultTestOrchestrator(store, loader)

	_, err := orch.IngestResult(context.Background(), SubmitRequest{
		ExecutionID: "run-1-start-1",
		OutcomeID:   "done",
		// Missing output.md
	})
	if err == nil {
		t.Fatal("expected error for missing required outputs")
	}

	// Step should be failed with invalid_result classification.
	step, _ := store.GetStepExecution(context.Background(), "run-1-start-1")
	if step.Status != domain.StepStatusFailed {
		t.Errorf("expected failed, got %s", step.Status)
	}
	if step.ErrorDetail == nil {
		t.Fatal("expected error detail")
	}
	if step.ErrorDetail.Classification != domain.FailureInvalidResult {
		t.Errorf("expected invalid_result, got %s", step.ErrorDetail.Classification)
	}
}

func TestIngestResult_InvalidOutcome(t *testing.T) {
	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {
				RunID:           "run-1",
				Status:          domain.RunStatusActive,
				TraceID:         "trace-1234567890ab",
				WorkflowPath:    "workflows/test.yaml",
				WorkflowVersion: "abc",
			},
		},
		createdSteps: []*domain.StepExecution{
			{
				ExecutionID: "run-1-start-1",
				RunID:       "run-1",
				StepID:      "start",
				Status:      domain.StepStatusInProgress,
				Attempt:     1,
				ActorID:     "test-actor",
			},
		},
	}
	loader := &mockWorkflowLoader{wfDef: resultTestWorkflow()}
	orch := resultTestOrchestrator(store, loader)

	_, err := orch.IngestResult(context.Background(), SubmitRequest{
		ExecutionID:       "run-1-start-1",
		OutcomeID:         "nonexistent",
		ArtifactsProduced: []string{"output.md"},
	})
	if err == nil {
		t.Fatal("expected error for invalid outcome")
	}
}

func TestIngestResult_Idempotent(t *testing.T) {
	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {
				RunID:           "run-1",
				Status:          domain.RunStatusCompleted,
				TraceID:         "trace-1234567890ab",
				WorkflowPath:    "workflows/test.yaml",
				WorkflowVersion: "abc",
			},
		},
		createdSteps: []*domain.StepExecution{
			{
				ExecutionID: "run-1-start-1",
				RunID:       "run-1",
				StepID:      "start",
				Status:      domain.StepStatusCompleted,
				OutcomeID:   "done",
				Attempt:     1,
			},
		},
	}
	loader := &mockWorkflowLoader{wfDef: resultTestWorkflow()}
	orch := resultTestOrchestrator(store, loader)

	// Re-submit same result — should succeed idempotently.
	resp, err := orch.IngestResult(context.Background(), SubmitRequest{
		ExecutionID:       "run-1-start-1",
		OutcomeID:         "done",
		ArtifactsProduced: []string{"output.md"},
	})
	if err != nil {
		t.Fatalf("expected idempotent success, got error: %v", err)
	}
	if resp.Status != domain.StepStatusCompleted {
		t.Errorf("expected completed, got %s", resp.Status)
	}
}

func TestIngestResult_EmptyExecutionID(t *testing.T) {
	orch := resultTestOrchestrator(&mockRunStore{}, &mockWorkflowLoader{})
	_, err := orch.IngestResult(context.Background(), SubmitRequest{OutcomeID: "done"})
	if err == nil {
		t.Fatal("expected error for empty execution ID")
	}
}

func TestIngestResult_EmptyOutcomeID(t *testing.T) {
	orch := resultTestOrchestrator(&mockRunStore{}, &mockWorkflowLoader{})
	_, err := orch.IngestResult(context.Background(), SubmitRequest{ExecutionID: "e-1"})
	if err == nil {
		t.Fatal("expected error for empty outcome ID")
	}
}

// TestIngestResult_FromWaiting_ReturnsConflict locks in the Option B state
// guard: a result submitted while the step is in `waiting` (no actor
// claimed/assigned) returns ErrConflict and does NOT mutate the
// execution. The previous behaviour silently routed through
// validateRequiredOutputs and could fail the step into retry —
// observed in the dogfooding bug report on TASK-004.
func TestIngestResult_FromWaiting_ReturnsConflict(t *testing.T) {
	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {
				RunID:           "run-1",
				Status:          domain.RunStatusActive,
				TraceID:         "trace-1234567890ab",
				WorkflowPath:    "workflows/test.yaml",
				WorkflowVersion: "abc",
			},
		},
		createdSteps: []*domain.StepExecution{
			{
				ExecutionID: "run-1-start-1",
				RunID:       "run-1",
				StepID:      "start",
				Status:      domain.StepStatusWaiting,
				Attempt:     1,
			},
		},
	}
	loader := &mockWorkflowLoader{wfDef: resultTestWorkflow()}
	orch := resultTestOrchestrator(store, loader)

	_, err := orch.IngestResult(context.Background(), SubmitRequest{
		ExecutionID:       "run-1-start-1",
		OutcomeID:         "done",
		ArtifactsProduced: []string{"output.md"},
	})
	if err == nil {
		t.Fatal("expected ErrConflict for submit-from-waiting")
	}
	var spineErr *domain.SpineError
	if !errors.As(err, &spineErr) || spineErr.Code != domain.ErrConflict {
		t.Errorf("expected ErrConflict, got %v", err)
	}

	// Crucially: the step must remain in waiting, not fail-with-retry.
	step, _ := store.GetStepExecution(context.Background(), "run-1-start-1")
	if step.Status != domain.StepStatusWaiting {
		t.Errorf("expected step to remain waiting, got %s", step.Status)
	}
	if step.ErrorDetail != nil {
		t.Errorf("expected no error detail, got %+v", step.ErrorDetail)
	}
}

// TestIngestResult_PhantomAssigned_ReturnsConflict locks in the actor_id
// half of the Option B guard (TASK-004): a step persisted as
// `assigned`/`in_progress` with `actor_id=""` (the phantom state from
// pre-fix in-flight rows) is rejected with ErrConflict instead of
// silently routing through validateRequiredOutputs and fail-with-retry-
// ing.
func TestIngestResult_PhantomAssigned_ReturnsConflict(t *testing.T) {
	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {
				RunID:           "run-1",
				Status:          domain.RunStatusActive,
				TraceID:         "trace-1234567890ab",
				WorkflowPath:    "workflows/test.yaml",
				WorkflowVersion: "abc",
			},
		},
		createdSteps: []*domain.StepExecution{
			{
				ExecutionID: "run-1-start-1",
				RunID:       "run-1",
				StepID:      "start",
				Status:      domain.StepStatusAssigned,
				Attempt:     1,
				ActorID:     "", // phantom — assigned but no actor bound
			},
		},
	}
	loader := &mockWorkflowLoader{wfDef: resultTestWorkflow()}
	orch := resultTestOrchestrator(store, loader)

	_, err := orch.IngestResult(context.Background(), SubmitRequest{
		ExecutionID:       "run-1-start-1",
		OutcomeID:         "done",
		ArtifactsProduced: []string{"output.md"},
	})
	if err == nil {
		t.Fatal("expected ErrConflict for submit on phantom-assigned step")
	}
	var spineErr *domain.SpineError
	if !errors.As(err, &spineErr) || spineErr.Code != domain.ErrConflict {
		t.Errorf("expected ErrConflict, got %v", err)
	}

	step, _ := store.GetStepExecution(context.Background(), "run-1-start-1")
	if step.Status != domain.StepStatusAssigned {
		t.Errorf("expected step to remain assigned (no mutation), got %s", step.Status)
	}
	if step.ErrorDetail != nil {
		t.Errorf("expected no error detail, got %+v", step.ErrorDetail)
	}
}

// TestIngestResult_FromBlocked_ReturnsConflict mirrors the waiting guard
// for the other non-claimable status; the production state machine
// also rejects step.submit from blocked.
func TestIngestResult_FromBlocked_ReturnsConflict(t *testing.T) {
	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {
				RunID:           "run-1",
				Status:          domain.RunStatusActive,
				TraceID:         "trace-1234567890ab",
				WorkflowPath:    "workflows/test.yaml",
				WorkflowVersion: "abc",
			},
		},
		createdSteps: []*domain.StepExecution{
			{
				ExecutionID: "run-1-start-1",
				RunID:       "run-1",
				StepID:      "start",
				Status:      domain.StepStatusBlocked,
				Attempt:     1,
			},
		},
	}
	loader := &mockWorkflowLoader{wfDef: resultTestWorkflow()}
	orch := resultTestOrchestrator(store, loader)

	_, err := orch.IngestResult(context.Background(), SubmitRequest{
		ExecutionID:       "run-1-start-1",
		OutcomeID:         "done",
		ArtifactsProduced: []string{"output.md"},
	})
	if err == nil {
		t.Fatal("expected ErrConflict for submit-from-blocked")
	}
	var spineErr *domain.SpineError
	if !errors.As(err, &spineErr) || spineErr.Code != domain.ErrConflict {
		t.Errorf("expected ErrConflict, got %v", err)
	}
}

func TestIngestResult_NoRequiredOutputs(t *testing.T) {
	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {
				RunID:           "run-1",
				Status:          domain.RunStatusActive,
				TraceID:         "trace-1234567890ab",
				WorkflowPath:    "workflows/test.yaml",
				WorkflowVersion: "abc",
			},
		},
		createdSteps: []*domain.StepExecution{
			{
				ExecutionID: "run-1-no-outputs-1",
				RunID:       "run-1",
				StepID:      "no-outputs",
				Status:      domain.StepStatusInProgress,
				Attempt:     1,
				ActorID:     "test-actor",
			},
		},
	}
	loader := &mockWorkflowLoader{wfDef: resultTestWorkflow()}
	orch := resultTestOrchestrator(store, loader)

	// Step with no required_outputs should accept any result.
	resp, err := orch.IngestResult(context.Background(), SubmitRequest{
		ExecutionID: "run-1-no-outputs-1",
		OutcomeID:   "done",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status != domain.StepStatusCompleted {
		t.Errorf("expected completed, got %s", resp.Status)
	}
}

// ── validateRequiredOutputs tests ──

func TestValidateRequiredOutputs_AllPresent(t *testing.T) {
	err := validateRequiredOutputs([]string{"a.md", "b.md"}, []string{"a.md", "b.md", "c.md"})
	if err != nil {
		t.Fatalf("expected pass, got: %v", err)
	}
}

func TestValidateRequiredOutputs_Missing(t *testing.T) {
	err := validateRequiredOutputs([]string{"a.md", "b.md"}, []string{"a.md"})
	if err == nil {
		t.Fatal("expected error for missing output")
	}
}

func TestValidateRequiredOutputs_Empty(t *testing.T) {
	err := validateRequiredOutputs(nil, nil)
	if err != nil {
		t.Fatalf("expected pass with no requirements, got: %v", err)
	}
}
