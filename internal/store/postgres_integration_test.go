//go:build integration

package store_test

import (
	"context"
	"testing"
	"time"

	spinecrypto "github.com/bszymi/spine/internal/crypto"
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

func TestRunModePlanningPersistence(t *testing.T) {
	s := store.NewTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)
	run := &domain.Run{
		RunID:           "run-mode-planning",
		TaskPath:        "tasks/test.md",
		WorkflowPath:    "workflows/test.yaml",
		WorkflowID:      "test",
		WorkflowVersion: "abc",
		Status:          domain.RunStatusPending,
		Mode:            domain.RunModePlanning,
		TraceID:         "trace-mode-planning",
		CreatedAt:       now,
	}

	if err := s.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	got, err := s.GetRun(ctx, "run-mode-planning")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Mode != domain.RunModePlanning {
		t.Errorf("expected mode %q, got %q", domain.RunModePlanning, got.Mode)
	}

	s.CleanupTestData(ctx, t)
}

func TestRunModeDefaultStandard(t *testing.T) {
	s := store.NewTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)
	run := &domain.Run{
		RunID:           "run-mode-default",
		TaskPath:        "tasks/test.md",
		WorkflowPath:    "workflows/test.yaml",
		WorkflowID:      "test",
		WorkflowVersion: "abc",
		Status:          domain.RunStatusPending,
		TraceID:         "trace-mode-default",
		CreatedAt:       now,
	}

	if err := s.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	got, err := s.GetRun(ctx, "run-mode-default")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Mode != domain.RunModeStandard {
		t.Errorf("expected mode %q, got %q", domain.RunModeStandard, got.Mode)
	}

	s.CleanupTestData(ctx, t)
}

func TestListRunsByStatusIncludesMode(t *testing.T) {
	s := store.NewTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)

	for _, r := range []*domain.Run{
		{RunID: "run-list-std", TaskPath: "tasks/a.md", WorkflowPath: "workflows/test.yaml", WorkflowID: "test", WorkflowVersion: "abc", Status: domain.RunStatusPending, Mode: domain.RunModeStandard, TraceID: "t1", CreatedAt: now},
		{RunID: "run-list-plan", TaskPath: "tasks/b.md", WorkflowPath: "workflows/test.yaml", WorkflowID: "test", WorkflowVersion: "abc", Status: domain.RunStatusPending, Mode: domain.RunModePlanning, TraceID: "t2", CreatedAt: now.Add(time.Second)},
	} {
		if err := s.CreateRun(ctx, r); err != nil {
			t.Fatalf("CreateRun %s: %v", r.RunID, err)
		}
	}

	runs, err := s.ListRunsByStatus(ctx, domain.RunStatusPending)
	if err != nil {
		t.Fatalf("ListRunsByStatus: %v", err)
	}

	modes := map[string]domain.RunMode{}
	for _, r := range runs {
		if r.RunID == "run-list-std" || r.RunID == "run-list-plan" {
			modes[r.RunID] = r.Mode
		}
	}

	if modes["run-list-std"] != domain.RunModeStandard {
		t.Errorf("expected standard mode for run-list-std, got %q", modes["run-list-std"])
	}
	if modes["run-list-plan"] != domain.RunModePlanning {
		t.Errorf("expected planning mode for run-list-plan, got %q", modes["run-list-plan"])
	}

	s.CleanupTestData(ctx, t)
}

func TestRunModeDatabaseDefault(t *testing.T) {
	s := store.NewTestStore(t)
	ctx := context.Background()

	// Insert a run via raw SQL without the mode column to test the database DEFAULT.
	err := s.ExecRaw(ctx, `
		INSERT INTO runtime.runs (run_id, task_path, workflow_path, workflow_id, workflow_version, status, trace_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, now())`,
		"run-db-default", "tasks/test.md", "workflows/test.yaml", "test", "abc", "pending", "trace-db-default",
	)
	if err != nil {
		t.Fatalf("raw insert: %v", err)
	}

	got, err := s.GetRun(ctx, "run-db-default")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Mode != domain.RunModeStandard {
		t.Errorf("expected database DEFAULT mode %q, got %q", domain.RunModeStandard, got.Mode)
	}

	s.CleanupTestData(ctx, t)
}

func TestRunAffectedRepositoriesRoundTrip(t *testing.T) {
	s := store.NewTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)
	run := &domain.Run{
		RunID:                "run-multi-repo",
		TaskPath:             "tasks/multi.md",
		WorkflowPath:         "workflows/test.yaml",
		WorkflowID:           "test",
		WorkflowVersion:      "abc",
		Status:               domain.RunStatusPending,
		BranchName:           "spine/run/multi",
		AffectedRepositories: []string{"spine", "payments-service", "api-gateway"},
		PrimaryRepository:    true,
		RepositoryBranches: map[string]string{
			"payments-service": "spine/run/multi-payments",
		},
		TraceID:   "trace-multi",
		CreatedAt: now,
	}

	if err := s.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	got, err := s.GetRun(ctx, "run-multi-repo")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if want := []string{"spine", "payments-service", "api-gateway"}; !equalStrings(got.AffectedRepositories, want) {
		t.Errorf("affected_repositories: got %v, want %v", got.AffectedRepositories, want)
	}
	if !got.PrimaryRepository {
		t.Errorf("primary_repository: got false, want true")
	}
	if got.RepositoryBranches["payments-service"] != "spine/run/multi-payments" {
		t.Errorf("repository_branches[payments-service]: got %q, want spine/run/multi-payments",
			got.RepositoryBranches["payments-service"])
	}

	s.CleanupTestData(ctx, t)
}

// TestRunAffectedRepositoriesFallback covers the missing-metadata fallback
// (INIT-014 EPIC-004 TASK-001 acceptance: "Missing task repository metadata
// produces [spine]"). A caller that constructs a Run literal without the new
// fields — e.g. legacy harness code — must still persist as primary-repo-only.
func TestRunAffectedRepositoriesFallback(t *testing.T) {
	s := store.NewTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)
	run := &domain.Run{
		RunID:           "run-no-repos",
		TaskPath:        "tasks/none.md",
		WorkflowPath:    "workflows/test.yaml",
		WorkflowID:      "test",
		WorkflowVersion: "abc",
		Status:          domain.RunStatusPending,
		TraceID:         "trace-none",
		CreatedAt:       now,
	}

	if err := s.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	got, err := s.GetRun(ctx, "run-no-repos")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if want := []string{domain.PrimaryRepositoryID}; !equalStrings(got.AffectedRepositories, want) {
		t.Errorf("affected_repositories fallback: got %v, want %v", got.AffectedRepositories, want)
	}
	if !got.PrimaryRepository {
		t.Errorf("primary_repository fallback: got false, want true")
	}

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

func TestEventSubscription_SigningSecretEncryption(t *testing.T) {
	s := store.NewTestStore(t)
	ctx := context.Background()
	s.CleanupTestData(ctx, t)

	key := make([]byte, spinecrypto.EncryptionKeySize)
	for i := range key {
		key[i] = byte(i + 1)
	}
	cipher, err := spinecrypto.NewSecretCipher(key)
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	s.SetSecretCipher(cipher)

	plaintext := "supersecret-webhook-key"
	now := time.Now().UTC().Truncate(time.Microsecond)
	sub := &store.EventSubscription{
		SubscriptionID: "sub-enc-001",
		Name:           "enc-test",
		TargetType:     "webhook",
		TargetURL:      "https://example.com/hook",
		EventTypes:     []string{"step.assigned"},
		SigningSecret:  plaintext,
		Status:         "active",
		Metadata:       []byte("{}"),
		CreatedBy:      "system",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.CreateSubscription(ctx, sub); err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}

	// Raw column must be ciphertext — a DB compromise should not
	// surrender the signing key.
	var rawSecret string
	if err := s.QueryRowSecret(ctx, sub.SubscriptionID, &rawSecret); err != nil {
		t.Fatalf("QueryRowSecret: %v", err)
	}
	if !spinecrypto.IsEncrypted(rawSecret) {
		t.Fatalf("expected ciphertext at rest, got %q", rawSecret)
	}
	if rawSecret == plaintext {
		t.Fatal("stored secret is plaintext — encryption did not apply")
	}

	// Decrypted read path returns the original plaintext.
	got, err := s.GetSubscription(ctx, sub.SubscriptionID)
	if err != nil {
		t.Fatalf("GetSubscription: %v", err)
	}
	if got.SigningSecret != plaintext {
		t.Fatalf("GetSubscription roundtrip mismatch: got %q want %q", got.SigningSecret, plaintext)
	}

	// Legacy plaintext rows (written before the key was deployed)
	// must still decrypt to themselves — this is what makes the
	// migration transparent.
	legacyID := "sub-enc-legacy"
	if err := s.ExecRaw(ctx,
		`INSERT INTO runtime.event_subscriptions
		  (subscription_id, workspace_id, name, target_type, target_url, event_types,
		   signing_secret, status, metadata, created_by, created_at, updated_at)
		 VALUES ($1, NULL, $2, 'webhook', 'https://x', ARRAY['step.assigned']::text[],
		   $3, 'active', '{}'::jsonb, 'system', $4, $4)`,
		legacyID, "legacy-test", "legacy-plaintext", now,
	); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
	legacy, err := s.GetSubscription(ctx, legacyID)
	if err != nil {
		t.Fatalf("GetSubscription(legacy): %v", err)
	}
	if legacy.SigningSecret != "legacy-plaintext" {
		t.Fatalf("legacy passthrough failed: got %q", legacy.SigningSecret)
	}

	// Update re-encrypts; the on-disk row must now be ciphertext.
	if err := s.UpdateSubscription(ctx, legacy); err != nil {
		t.Fatalf("UpdateSubscription: %v", err)
	}
	var afterUpdate string
	if err := s.QueryRowSecret(ctx, legacyID, &afterUpdate); err != nil {
		t.Fatalf("QueryRowSecret: %v", err)
	}
	if !spinecrypto.IsEncrypted(afterUpdate) {
		t.Fatalf("legacy row not re-encrypted on update: %q", afterUpdate)
	}

	s.CleanupTestData(ctx, t)
}

func TestBranchProtectionRulesProjection(t *testing.T) {
	s := store.NewTestStore(t)
	ctx := context.Background()

	// Reset to a known state. The migration seeds bootstrap defaults,
	// but a shared test DB may carry state from prior tests — so
	// explicitly install the bootstrap row and assert from there.
	bootstrap := []store.BranchProtectionRuleProjection{
		{BranchPattern: "main", RuleOrder: 0, Protections: []byte(`["no-delete","no-direct-write"]`)},
	}
	if err := s.UpsertBranchProtectionRules(ctx, bootstrap, "bootstrap"); err != nil {
		t.Fatalf("seed bootstrap: %v", err)
	}
	rows, err := s.ListBranchProtectionRules(ctx)
	if err != nil {
		t.Fatalf("ListBranchProtectionRules: %v", err)
	}
	if len(rows) != 1 || rows[0].BranchPattern != "main" || rows[0].SourceCommit != "bootstrap" {
		t.Fatalf("bootstrap state wrong: %+v", rows)
	}

	// Upsert a three-rule config with a real source commit.
	rules := []store.BranchProtectionRuleProjection{
		{BranchPattern: "main", RuleOrder: 0, Protections: []byte(`["no-delete","no-direct-write"]`)},
		{BranchPattern: "staging", RuleOrder: 1, Protections: []byte(`["no-delete"]`)},
		{BranchPattern: "release/*", RuleOrder: 2, Protections: []byte(`["no-delete","no-direct-write"]`)},
	}
	if err := s.UpsertBranchProtectionRules(ctx, rules, "abc123def456"); err != nil {
		t.Fatalf("UpsertBranchProtectionRules: %v", err)
	}

	rows, err = s.ListBranchProtectionRules(ctx)
	if err != nil {
		t.Fatalf("ListBranchProtectionRules (after upsert): %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
	if rows[0].BranchPattern != "main" || rows[1].BranchPattern != "staging" || rows[2].BranchPattern != "release/*" {
		t.Fatalf("row order wrong: %+v", rows)
	}
	if rows[0].SourceCommit != "abc123def456" {
		t.Fatalf("source_commit = %q, want abc123def456", rows[0].SourceCommit)
	}

	// An atomic swap: upsert a smaller ruleset. The old `staging` and
	// `release/*` rows must be gone, not shadowed by the old rows.
	shorter := []store.BranchProtectionRuleProjection{
		{BranchPattern: "main", RuleOrder: 0, Protections: []byte(`["no-delete"]`)},
	}
	if err := s.UpsertBranchProtectionRules(ctx, shorter, "def789"); err != nil {
		t.Fatalf("UpsertBranchProtectionRules (shorter): %v", err)
	}
	rows, _ = s.ListBranchProtectionRules(ctx)
	if len(rows) != 1 || rows[0].BranchPattern != "main" {
		t.Fatalf("after shrink: %+v", rows)
	}

	// Explicit empty ruleset — author opts out entirely.
	if err := s.UpsertBranchProtectionRules(ctx, nil, "ghi000"); err != nil {
		t.Fatalf("UpsertBranchProtectionRules (empty): %v", err)
	}
	rows, _ = s.ListBranchProtectionRules(ctx)
	if len(rows) != 0 {
		t.Fatalf("empty upsert left %d rows", len(rows))
	}

	s.CleanupTestData(ctx, t)
}

// TestArtifactProjection_RepositoriesRoundTrip pins the TASK-005
// acceptance criterion that repository metadata is queryable through
// the projection store. It exercises both the read-by-path and the
// list/query code paths so a JSONB marshalling regression on either
// side fails the test rather than silently dropping the field.
func TestArtifactProjection_RepositoriesRoundTrip(t *testing.T) {
	s := store.NewTestStore(t)
	ctx := context.Background()
	s.CleanupTestData(ctx, t)
	defer s.CleanupTestData(ctx, t)

	cases := []struct {
		name string
		path string
		in   []string
	}{
		{name: "no repositories", path: "initiatives/INIT-X/epics/EPIC-X/tasks/TASK-001.md", in: nil},
		{name: "single repo", path: "initiatives/INIT-X/epics/EPIC-X/tasks/TASK-002.md", in: []string{"payments-service"}},
		{name: "multi repo", path: "initiatives/INIT-X/epics/EPIC-X/tasks/TASK-003.md", in: []string{"spine", "payments-service", "api-gateway"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proj := &store.ArtifactProjection{
				ArtifactPath: tc.path,
				ArtifactID:   "TASK-XYZ",
				ArtifactType: string(domain.ArtifactTypeTask),
				Title:        "Repositories round-trip",
				Status:       string(domain.StatusPending),
				Metadata:     []byte(`{}`),
				Content:      "# x",
				Links:        []byte(`[]`),
				Repositories: tc.in,
				SourceCommit: "deadbeef",
				ContentHash:  "hash",
			}
			if err := s.UpsertArtifactProjection(ctx, proj); err != nil {
				t.Fatalf("UpsertArtifactProjection: %v", err)
			}

			got, err := s.GetArtifactProjection(ctx, tc.path)
			if err != nil {
				t.Fatalf("GetArtifactProjection: %v", err)
			}
			if !equalStrings(got.Repositories, tc.in) {
				t.Errorf("GetArtifactProjection.Repositories: got %v, want %v", got.Repositories, tc.in)
			}

			res, err := s.QueryArtifacts(ctx, store.ArtifactQuery{Type: string(domain.ArtifactTypeTask), Limit: 100})
			if err != nil {
				t.Fatalf("QueryArtifacts: %v", err)
			}
			var found *store.ArtifactProjection
			for i := range res.Items {
				if res.Items[i].ArtifactPath == tc.path {
					found = &res.Items[i]
					break
				}
			}
			if found == nil {
				t.Fatalf("QueryArtifacts did not return %s", tc.path)
			}
			if !equalStrings(found.Repositories, tc.in) {
				t.Errorf("QueryArtifacts row Repositories: got %v, want %v", found.Repositories, tc.in)
			}
		})
	}
}

// TestExecutionProjection_RepositoriesRoundTrip mirrors the artifact
// round-trip test for the execution_projections table. The Task-only
// Repositories column drives operational dashboards (which repos a
// pending task touches), so a regression silently breaks ops queries.
func TestExecutionProjection_RepositoriesRoundTrip(t *testing.T) {
	s := store.NewTestStore(t)
	ctx := context.Background()
	s.CleanupTestData(ctx, t)
	defer s.CleanupTestData(ctx, t)

	cases := []struct {
		name string
		path string
		in   []string
	}{
		{name: "no repositories", path: "initiatives/INIT-Y/epics/EPIC-Y/tasks/TASK-001.md", in: nil},
		{name: "multi repo", path: "initiatives/INIT-Y/epics/EPIC-Y/tasks/TASK-002.md", in: []string{"spine", "payments-service"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := &store.ExecutionProjection{
				TaskPath:         tc.path,
				TaskID:           "TASK-001",
				Title:            "Execution round-trip",
				Status:           string(domain.StatusPending),
				AssignmentStatus: "unassigned",
				Repositories:     tc.in,
			}
			if err := s.UpsertExecutionProjection(ctx, exec); err != nil {
				t.Fatalf("UpsertExecutionProjection: %v", err)
			}

			got, err := s.GetExecutionProjection(ctx, tc.path)
			if err != nil {
				t.Fatalf("GetExecutionProjection: %v", err)
			}
			if !equalStrings(got.Repositories, tc.in) {
				t.Errorf("GetExecutionProjection.Repositories: got %v, want %v", got.Repositories, tc.in)
			}

			rows, err := s.QueryExecutionProjections(ctx, store.ExecutionProjectionQuery{Limit: 100})
			if err != nil {
				t.Fatalf("QueryExecutionProjections: %v", err)
			}
			var found *store.ExecutionProjection
			for i := range rows {
				if rows[i].TaskPath == tc.path {
					found = &rows[i]
					break
				}
			}
			if found == nil {
				t.Fatalf("QueryExecutionProjections did not return %s", tc.path)
			}
			if !equalStrings(found.Repositories, tc.in) {
				t.Errorf("QueryExecutionProjections row Repositories: got %v, want %v", found.Repositories, tc.in)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return len(a) == 0 && len(b) == 0
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestDeleteAllProjectionsPurgesExecutionProjections(t *testing.T) {
	s := store.NewTestStore(t)
	ctx := context.Background()
	s.CleanupTestData(ctx, t)

	// Seed an execution_projections row whose task_path no longer
	// exists in projection.artifacts — the orphan case from the
	// 2026-04-21 dogfooding session.
	orphan := &store.ExecutionProjection{
		TaskPath:         "initiatives/INIT-stale/epics/EPIC-gone/tasks/TASK-001.md",
		TaskID:           "TASK-001",
		Title:            "Stale orphan",
		Status:           "Pending",
		AssignmentStatus: "unassigned",
	}
	if err := s.UpsertExecutionProjection(ctx, orphan); err != nil {
		t.Fatalf("seed UpsertExecutionProjection: %v", err)
	}
	if _, err := s.GetExecutionProjection(ctx, orphan.TaskPath); err != nil {
		t.Fatalf("precondition: GetExecutionProjection: %v", err)
	}

	if err := s.DeleteAllProjections(ctx); err != nil {
		t.Fatalf("DeleteAllProjections: %v", err)
	}

	if _, err := s.GetExecutionProjection(ctx, orphan.TaskPath); err == nil {
		t.Fatalf("orphan execution_projection row survived rebuild")
	}

	s.CleanupTestData(ctx, t)
}
