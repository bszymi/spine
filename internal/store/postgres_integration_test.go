//go:build integration

package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/store"
)

func TestPing(t *testing.T) {
	s := store.NewTestStore(t)
	ctx := context.Background()

	if err := s.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestRunCRUD(t *testing.T) {
	s := store.NewTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)
	run := &domain.Run{
		RunID:           "run-test-001",
		TaskPath:        "initiatives/INIT-001/tasks/TASK-001.md",
		WorkflowPath:    "workflows/task-execution.yaml",
		WorkflowID:      "task-execution",
		WorkflowVersion: "abc123",
		Status:          domain.RunStatusPending,
		TraceID:         "trace-001",
		CreatedAt:       now,
	}

	// Create
	if err := s.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	// Read
	got, err := s.GetRun(ctx, "run-test-001")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.RunID != run.RunID {
		t.Errorf("expected run_id %s, got %s", run.RunID, got.RunID)
	}
	if got.Status != domain.RunStatusPending {
		t.Errorf("expected status pending, got %s", got.Status)
	}

	// Update status
	if err := s.UpdateRunStatus(ctx, "run-test-001", domain.RunStatusActive); err != nil {
		t.Fatalf("UpdateRunStatus: %v", err)
	}

	got, _ = s.GetRun(ctx, "run-test-001")
	if got.Status != domain.RunStatusActive {
		t.Errorf("expected status active, got %s", got.Status)
	}

	// List by task
	runs, err := s.ListRunsByTask(ctx, "initiatives/INIT-001/tasks/TASK-001.md")
	if err != nil {
		t.Fatalf("ListRunsByTask: %v", err)
	}
	if len(runs) != 1 {
		t.Errorf("expected 1 run, got %d", len(runs))
	}

	// Not found
	_, err = s.GetRun(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent run")
	}

	// Cleanup
	s.CleanupTestData(ctx, t)
}

func TestStepExecutionCRUD(t *testing.T) {
	s := store.NewTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)

	// Create parent run first
	run := &domain.Run{
		RunID:           "run-step-test",
		TaskPath:        "tasks/test.md",
		WorkflowPath:    "workflows/test.yaml",
		WorkflowID:      "test",
		WorkflowVersion: "abc",
		Status:          domain.RunStatusActive,
		TraceID:         "trace-step",
		CreatedAt:       now,
	}
	if err := s.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	exec := &domain.StepExecution{
		ExecutionID: "exec-001",
		RunID:       "run-step-test",
		StepID:      "assign",
		Status:      domain.StepStatusWaiting,
		Attempt:     1,
		CreatedAt:   now,
	}

	// Create
	if err := s.CreateStepExecution(ctx, exec); err != nil {
		t.Fatalf("CreateStepExecution: %v", err)
	}

	// Read
	got, err := s.GetStepExecution(ctx, "exec-001")
	if err != nil {
		t.Fatalf("GetStepExecution: %v", err)
	}
	if got.Status != domain.StepStatusWaiting {
		t.Errorf("expected status waiting, got %s", got.Status)
	}

	// Update
	exec.Status = domain.StepStatusAssigned
	exec.ActorID = "actor-001"
	if err := s.UpdateStepExecution(ctx, exec); err != nil {
		t.Fatalf("UpdateStepExecution: %v", err)
	}

	got, _ = s.GetStepExecution(ctx, "exec-001")
	if got.Status != domain.StepStatusAssigned {
		t.Errorf("expected status assigned, got %s", got.Status)
	}
	if got.ActorID != "actor-001" {
		t.Errorf("expected actor_id actor-001, got %s", got.ActorID)
	}

	// List by run
	execs, err := s.ListStepExecutionsByRun(ctx, "run-step-test")
	if err != nil {
		t.Fatalf("ListStepExecutionsByRun: %v", err)
	}
	if len(execs) != 1 {
		t.Errorf("expected 1 execution, got %d", len(execs))
	}

	// Cleanup
	s.CleanupTestData(ctx, t)
}

func TestArtifactProjection(t *testing.T) {
	s := store.NewTestStore(t)
	ctx := context.Background()

	proj := &store.ArtifactProjection{
		ArtifactPath: "governance/test.md",
		ArtifactID:   "TEST-001",
		ArtifactType: "Governance",
		Title:        "Test Document",
		Status:       "Living Document",
		Metadata:     []byte(`{"type":"Governance"}`),
		Content:      "# Test",
		Links:        []byte(`[]`),
		SourceCommit: "abc123",
		ContentHash:  "hash123",
	}

	// Upsert (insert)
	if err := s.UpsertArtifactProjection(ctx, proj); err != nil {
		t.Fatalf("UpsertArtifactProjection: %v", err)
	}

	// Read
	got, err := s.GetArtifactProjection(ctx, "governance/test.md")
	if err != nil {
		t.Fatalf("GetArtifactProjection: %v", err)
	}
	if got.Title != "Test Document" {
		t.Errorf("expected title 'Test Document', got %q", got.Title)
	}

	// Upsert (update)
	proj.Title = "Updated Title"
	if err := s.UpsertArtifactProjection(ctx, proj); err != nil {
		t.Fatalf("UpsertArtifactProjection (update): %v", err)
	}
	got, _ = s.GetArtifactProjection(ctx, "governance/test.md")
	if got.Title != "Updated Title" {
		t.Errorf("expected 'Updated Title', got %q", got.Title)
	}

	// Query
	result, err := s.QueryArtifacts(ctx, store.ArtifactQuery{Type: "Governance", Limit: 10})
	if err != nil {
		t.Fatalf("QueryArtifacts: %v", err)
	}
	if len(result.Items) < 1 {
		t.Error("expected at least 1 result")
	}

	// Cleanup
	s.CleanupTestData(ctx, t)
}

func TestWithTx(t *testing.T) {
	s := store.NewTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)

	// Successful transaction
	err := s.WithTx(ctx, func(tx store.Tx) error {
		return tx.CreateRun(ctx, &domain.Run{
			RunID:           "run-tx-test",
			TaskPath:        "tasks/tx.md",
			WorkflowPath:    "workflows/test.yaml",
			WorkflowID:      "test",
			WorkflowVersion: "abc",
			Status:          domain.RunStatusPending,
			TraceID:         "trace-tx",
			CreatedAt:       now,
		})
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}

	got, err := s.GetRun(ctx, "run-tx-test")
	if err != nil {
		t.Fatalf("GetRun after tx: %v", err)
	}
	if got.RunID != "run-tx-test" {
		t.Error("run not found after commit")
	}

	// Cleanup
	s.CleanupTestData(ctx, t)
}

func TestMigrationApplied(t *testing.T) {
	s := store.NewTestStore(t)
	ctx := context.Background()

	applied, err := s.IsMigrationApplied(ctx, "001_initial_schema")
	if err != nil {
		t.Fatalf("IsMigrationApplied: %v", err)
	}
	if !applied {
		t.Error("expected migration 001_initial_schema to be applied")
	}
}
