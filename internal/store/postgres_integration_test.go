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

func TestDiscussionThreadCRUD(t *testing.T) {
	s := store.NewTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)

	thread := &domain.DiscussionThread{
		ThreadID:   "thread-test-001",
		AnchorType: domain.AnchorTypeArtifact,
		AnchorID:   "initiatives/INIT-001/tasks/TASK-001.md",
		TopicKey:   "acceptance-criteria",
		Title:      "Clarify acceptance criteria",
		Status:     domain.ThreadStatusOpen,
		CreatedBy:  "actor-001",
		CreatedAt:  now,
	}

	// Create
	if err := s.CreateThread(ctx, thread); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	// Read
	got, err := s.GetThread(ctx, "thread-test-001")
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if got.ThreadID != thread.ThreadID {
		t.Errorf("expected thread_id %s, got %s", thread.ThreadID, got.ThreadID)
	}
	if got.AnchorType != domain.AnchorTypeArtifact {
		t.Errorf("expected anchor_type artifact, got %s", got.AnchorType)
	}
	if got.TopicKey != "acceptance-criteria" {
		t.Errorf("expected topic_key acceptance-criteria, got %s", got.TopicKey)
	}
	if got.Status != domain.ThreadStatusOpen {
		t.Errorf("expected status open, got %s", got.Status)
	}

	// Update — resolve thread
	resolved := time.Now().UTC().Truncate(time.Microsecond)
	got.Status = domain.ThreadStatusResolved
	got.ResolvedAt = &resolved
	got.ResolutionType = domain.ResolutionArtifactUpdated
	got.ResolutionRefs = []byte(`["commit-abc123"]`)
	if err := s.UpdateThread(ctx, got); err != nil {
		t.Fatalf("UpdateThread: %v", err)
	}

	got2, _ := s.GetThread(ctx, "thread-test-001")
	if got2.Status != domain.ThreadStatusResolved {
		t.Errorf("expected status resolved, got %s", got2.Status)
	}
	if got2.ResolutionType != domain.ResolutionArtifactUpdated {
		t.Errorf("expected resolution_type artifact_updated, got %s", got2.ResolutionType)
	}
	if got2.ResolvedAt == nil {
		t.Error("expected resolved_at to be set")
	}

	// Update — reopen thread
	got2.Status = domain.ThreadStatusOpen
	got2.ResolvedAt = nil
	got2.ResolutionType = ""
	got2.ResolutionRefs = nil
	if err := s.UpdateThread(ctx, got2); err != nil {
		t.Fatalf("UpdateThread (reopen): %v", err)
	}

	got3, _ := s.GetThread(ctx, "thread-test-001")
	if got3.Status != domain.ThreadStatusOpen {
		t.Errorf("expected status open after reopen, got %s", got3.Status)
	}

	// List by anchor
	threads, err := s.ListThreads(ctx, domain.AnchorTypeArtifact, "initiatives/INIT-001/tasks/TASK-001.md")
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(threads) != 1 {
		t.Errorf("expected 1 thread, got %d", len(threads))
	}

	// Not found
	_, err = s.GetThread(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent thread")
	}

	// Update not found
	err = s.UpdateThread(ctx, &domain.DiscussionThread{ThreadID: "nonexistent", Status: domain.ThreadStatusArchived})
	if err == nil {
		t.Fatal("expected error for updating nonexistent thread")
	}

	// Cleanup
	s.CleanupTestData(ctx, t)
}

func TestCommentCRUD(t *testing.T) {
	s := store.NewTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)

	// Create parent thread
	thread := &domain.DiscussionThread{
		ThreadID:   "thread-comment-test",
		AnchorType: domain.AnchorTypeStepExecution,
		AnchorID:   "exec-001",
		Status:     domain.ThreadStatusOpen,
		CreatedBy:  "actor-001",
		CreatedAt:  now,
	}
	if err := s.CreateThread(ctx, thread); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	// Create root comment
	comment1 := &domain.Comment{
		CommentID:  "comment-001",
		ThreadID:   "thread-comment-test",
		AuthorID:   "actor-001",
		AuthorType: "human",
		Content:    "First comment",
		Metadata:   []byte(`{"source":"cli"}`),
		CreatedAt:  now,
	}
	if err := s.CreateComment(ctx, comment1); err != nil {
		t.Fatalf("CreateComment: %v", err)
	}

	// Create reply comment (nested)
	comment2 := &domain.Comment{
		CommentID:       "comment-002",
		ThreadID:        "thread-comment-test",
		ParentCommentID: "comment-001",
		AuthorID:        "actor-002",
		AuthorType:      "ai_agent",
		Content:         "Reply to first comment",
		CreatedAt:       now.Add(time.Second),
	}
	if err := s.CreateComment(ctx, comment2); err != nil {
		t.Fatalf("CreateComment (reply): %v", err)
	}

	// List comments
	comments, err := s.ListComments(ctx, "thread-comment-test")
	if err != nil {
		t.Fatalf("ListComments: %v", err)
	}
	if len(comments) != 2 {
		t.Fatalf("expected 2 comments, got %d", len(comments))
	}

	// Verify ordering (ASC by created_at)
	if comments[0].CommentID != "comment-001" {
		t.Errorf("expected first comment comment-001, got %s", comments[0].CommentID)
	}
	if comments[1].CommentID != "comment-002" {
		t.Errorf("expected second comment comment-002, got %s", comments[1].CommentID)
	}

	// Verify nested reply
	if comments[1].ParentCommentID != "comment-001" {
		t.Errorf("expected parent_comment_id comment-001, got %s", comments[1].ParentCommentID)
	}

	// Verify author types
	if comments[0].AuthorType != "human" {
		t.Errorf("expected author_type human, got %s", comments[0].AuthorType)
	}
	if comments[1].AuthorType != "ai_agent" {
		t.Errorf("expected author_type ai_agent, got %s", comments[1].AuthorType)
	}

	// Empty thread returns no comments
	emptyComments, err := s.ListComments(ctx, "nonexistent-thread")
	if err != nil {
		t.Fatalf("ListComments (empty): %v", err)
	}
	if len(emptyComments) != 0 {
		t.Errorf("expected 0 comments for nonexistent thread, got %d", len(emptyComments))
	}

	// Cleanup
	s.CleanupTestData(ctx, t)
}

func TestThreadWithoutOptionalFields(t *testing.T) {
	s := store.NewTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)

	// Create thread with no optional fields
	thread := &domain.DiscussionThread{
		ThreadID:   "thread-minimal",
		AnchorType: domain.AnchorTypeRun,
		AnchorID:   "run-001",
		Status:     domain.ThreadStatusOpen,
		CreatedBy:  "actor-001",
		CreatedAt:  now,
	}
	if err := s.CreateThread(ctx, thread); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	got, err := s.GetThread(ctx, "thread-minimal")
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if got.TopicKey != "" {
		t.Errorf("expected empty topic_key, got %s", got.TopicKey)
	}
	if got.Title != "" {
		t.Errorf("expected empty title, got %s", got.Title)
	}
	if got.ResolvedAt != nil {
		t.Error("expected nil resolved_at")
	}
	if got.ResolutionType != "" {
		t.Errorf("expected empty resolution_type, got %s", got.ResolutionType)
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
