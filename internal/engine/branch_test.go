package engine

import (
	"context"
	"testing"

	"github.com/bszymi/spine/internal/domain"
)

func TestRunBranch_ReturnsName(t *testing.T) {
	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {RunID: "run-1", BranchName: "spine/run/run-1", TraceID: "t-1234567890ab"},
		},
	}
	orch := &Orchestrator{store: store}

	branch := orch.RunBranch(context.Background(), "run-1")
	if branch != "spine/run/run-1" {
		t.Errorf("expected spine/run/run-1, got %s", branch)
	}
}

func TestRunBranch_EmptyWhenNotSet(t *testing.T) {
	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {RunID: "run-1", TraceID: "t-1234567890ab"},
		},
	}
	orch := &Orchestrator{store: store}

	branch := orch.RunBranch(context.Background(), "run-1")
	if branch != "" {
		t.Errorf("expected empty, got %s", branch)
	}
}

func TestRunBranch_EmptyWhenNotFound(t *testing.T) {
	store := &mockRunStore{}
	orch := &Orchestrator{store: store}

	branch := orch.RunBranch(context.Background(), "missing")
	if branch != "" {
		t.Errorf("expected empty for missing run, got %s", branch)
	}
}

func TestCleanupRunBranch_NoBranch(t *testing.T) {
	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {RunID: "run-1", TraceID: "t-1234567890ab"},
		},
	}
	orch := &Orchestrator{store: store, git: &stubGitOperator{}}

	err := orch.CleanupRunBranch(context.Background(), "run-1")
	if err != nil {
		t.Fatalf("expected no error for run without branch, got %v", err)
	}
}

func TestCleanupRunBranch_NotFound(t *testing.T) {
	store := &mockRunStore{}
	orch := &Orchestrator{store: store, git: &stubGitOperator{}}

	err := orch.CleanupRunBranch(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error for missing run")
	}
}
