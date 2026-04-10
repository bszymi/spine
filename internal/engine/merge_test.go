package engine

import (
	"context"
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/git"
)

type mockGitOperator struct {
	stubGitOperator
	mergeErr    error
	mergeResult git.MergeResult
	pushErr     error
	deleted     []string
}

func (m *mockGitOperator) Merge(_ context.Context, _ git.MergeOpts) (git.MergeResult, error) {
	return m.mergeResult, m.mergeErr
}

func (m *mockGitOperator) Push(_ context.Context, _, _ string) error {
	if m.pushErr != nil {
		return m.pushErr
	}
	return nil
}

func (m *mockGitOperator) DeleteBranch(_ context.Context, name string) error {
	m.deleted = append(m.deleted, name)
	return nil
}

func TestMergeRunBranch_HappyPath(t *testing.T) {
	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {
				RunID:      "run-1",
				Status:     domain.RunStatusCommitting,
				BranchName: "spine/run/run-1",
				TraceID:    "trace-1234567890ab",
			},
		},
	}
	gitOp := &mockGitOperator{mergeResult: git.MergeResult{SHA: "merge-sha"}}
	events := &mockEventEmitter{}

	orch := &Orchestrator{
		store:    store,
		git:      gitOp,
		events:   events,
		wfLoader: &stubWorkflowLoader{},
	}

	err := orch.MergeRunBranch(context.Background(), "run-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Run should be completed.
	if store.runs["run-1"].Status != domain.RunStatusCompleted {
		t.Errorf("expected completed, got %s", store.runs["run-1"].Status)
	}

	// Branch should be cleaned up.
	if len(gitOp.deleted) != 1 || gitOp.deleted[0] != "spine/run/run-1" {
		t.Errorf("expected branch cleanup, got %v", gitOp.deleted)
	}

	// Completed event should be emitted.
	found := false
	for _, e := range events.events {
		if e.Type == domain.EventRunCompleted {
			found = true
		}
	}
	if !found {
		t.Error("expected run_completed event")
	}
}

func TestMergeRunBranch_PermanentFailure(t *testing.T) {
	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {
				RunID:      "run-1",
				Status:     domain.RunStatusCommitting,
				BranchName: "spine/run/run-1",
				TraceID:    "trace-1234567890ab",
			},
		},
	}
	gitOp := &mockGitOperator{
		mergeErr: &git.GitError{Kind: git.ErrKindPermanent, Op: "merge", Message: "conflict"},
	}
	events := &mockEventEmitter{}

	orch := &Orchestrator{
		store:    store,
		git:      gitOp,
		events:   events,
		wfLoader: &stubWorkflowLoader{},
	}

	err := orch.MergeRunBranch(context.Background(), "run-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Run should be failed.
	if store.runs["run-1"].Status != domain.RunStatusFailed {
		t.Errorf("expected failed, got %s", store.runs["run-1"].Status)
	}

	// Branch preserved for debugging — NOT cleaned up.
	if len(gitOp.deleted) != 0 {
		t.Errorf("expected no branch cleanup on failure, got %v", gitOp.deleted)
	}
}

func TestMergeRunBranch_TransientFailure(t *testing.T) {
	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {
				RunID:      "run-1",
				Status:     domain.RunStatusCommitting,
				BranchName: "spine/run/run-1",
				TraceID:    "trace-1234567890ab",
			},
		},
	}
	gitOp := &mockGitOperator{
		mergeErr: &git.GitError{Kind: git.ErrKindTransient, Op: "merge", Message: "locked"},
	}

	orch := &Orchestrator{
		store:    store,
		git:      gitOp,
		events:   &mockEventEmitter{},
		wfLoader: &stubWorkflowLoader{},
	}

	err := orch.MergeRunBranch(context.Background(), "run-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Run should stay committing for retry.
	if store.runs["run-1"].Status != domain.RunStatusCommitting {
		t.Errorf("expected committing (retry), got %s", store.runs["run-1"].Status)
	}
}

func TestMergeRunBranch_NoBranch(t *testing.T) {
	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {
				RunID:   "run-1",
				Status:  domain.RunStatusCommitting,
				TraceID: "trace-1234567890ab",
			},
		},
	}
	events := &mockEventEmitter{}

	orch := &Orchestrator{
		store:    store,
		git:      &stubGitOperator{},
		events:   events,
		wfLoader: &stubWorkflowLoader{},
	}

	err := orch.MergeRunBranch(context.Background(), "run-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should still complete (no branch = skip merge).
	if store.runs["run-1"].Status != domain.RunStatusCompleted {
		t.Errorf("expected completed, got %s", store.runs["run-1"].Status)
	}
}

func TestMergeRunBranch_WrongState(t *testing.T) {
	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {
				RunID:   "run-1",
				Status:  domain.RunStatusActive,
				TraceID: "trace-1234567890ab",
			},
		},
	}

	orch := &Orchestrator{store: store, git: &stubGitOperator{}, events: &mockEventEmitter{}}

	err := orch.MergeRunBranch(context.Background(), "run-1")
	if err == nil {
		t.Fatal("expected error for wrong state")
	}
}

func TestMergeRunBranch_NotFound(t *testing.T) {
	store := &mockRunStore{}
	orch := &Orchestrator{store: store, git: &stubGitOperator{}, events: &mockEventEmitter{}}

	err := orch.MergeRunBranch(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error for missing run")
	}
}

func TestMergeRunBranch_PushAuthFailure(t *testing.T) {
	// When push fails with an auth error (permanent), the run should fail
	// immediately — not stay in committing for infinite retries.
	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {
				RunID:      "run-1",
				Status:     domain.RunStatusCommitting,
				BranchName: "spine/run/run-1",
				TraceID:    "trace-1234567890ab",
			},
		},
	}
	gitOp := &mockGitOperator{
		mergeResult: git.MergeResult{SHA: "merge-sha"},
		pushErr:     &git.GitError{Kind: git.ErrKindPermanent, Op: "push", Message: "authentication failed"},
	}
	events := &mockEventEmitter{}

	orch := &Orchestrator{
		store:    store,
		git:      gitOp,
		events:   events,
		wfLoader: &stubWorkflowLoader{},
	}

	t.Setenv("SPINE_GIT_PUSH_ENABLED", "true")

	err := orch.MergeRunBranch(context.Background(), "run-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Run should be failed, not stuck in committing.
	if store.runs["run-1"].Status != domain.RunStatusFailed {
		t.Errorf("expected failed on auth error, got %s", store.runs["run-1"].Status)
	}
}

func TestMergeRunBranch_PushTransientFailure(t *testing.T) {
	// When push fails with a transient error (network), the run should stay
	// in committing for scheduler retry.
	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {
				RunID:      "run-1",
				Status:     domain.RunStatusCommitting,
				BranchName: "spine/run/run-1",
				TraceID:    "trace-1234567890ab",
			},
		},
	}
	gitOp := &mockGitOperator{
		mergeResult: git.MergeResult{SHA: "merge-sha"},
		pushErr:     &git.GitError{Kind: git.ErrKindTransient, Op: "push", Message: "network error"},
	}

	orch := &Orchestrator{
		store:    store,
		git:      gitOp,
		events:   &mockEventEmitter{},
		wfLoader: &stubWorkflowLoader{},
	}

	t.Setenv("SPINE_GIT_PUSH_ENABLED", "true")

	err := orch.MergeRunBranch(context.Background(), "run-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Run should stay committing for retry.
	if store.runs["run-1"].Status != domain.RunStatusCommitting {
		t.Errorf("expected committing (retry), got %s", store.runs["run-1"].Status)
	}
}
