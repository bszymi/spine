package engine

import (
	"context"
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/store"
)

type fakeCandidateStore struct {
	*fakeBlockingStore
	artifacts *store.ArtifactQueryResult
}

func (f *fakeCandidateStore) QueryArtifacts(_ context.Context, _ store.ArtifactQuery) (*store.ArtifactQueryResult, error) {
	return f.artifacts, nil
}

func TestFindExecutionCandidates_ExcludesBlocked(t *testing.T) {
	bs := newFakeBlockingStore()
	cs := &fakeCandidateStore{
		fakeBlockingStore: bs,
		artifacts: &store.ArtifactQueryResult{
			Items: []store.ArtifactProjection{
				{ArtifactPath: "tasks/ready.md", ArtifactID: "TASK-1", Title: "Ready Task", Status: string(domain.StatusPending)},
				{ArtifactPath: "tasks/blocked.md", ArtifactID: "TASK-2", Title: "Blocked Task", Status: string(domain.StatusPending)},
			},
		},
	}

	// tasks/blocked.md is blocked by tasks/blocker.md (pending)
	bs.links["tasks/blocked.md"] = []store.ArtifactLink{
		{SourcePath: "tasks/blocked.md", TargetPath: "tasks/blocker.md", LinkType: "blocked_by"},
	}
	bs.projections["tasks/blocker.md"] = &store.ArtifactProjection{
		ArtifactPath: "tasks/blocker.md",
		Status:       string(domain.StatusPending),
	}

	orch := &Orchestrator{blocking: cs}

	candidates, err := orch.FindExecutionCandidates(context.Background(), ExecutionCandidateFilter{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate (blocked excluded), got %d", len(candidates))
	}
	if candidates[0].TaskPath != "tasks/ready.md" {
		t.Errorf("expected ready task, got %s", candidates[0].TaskPath)
	}
}

func TestFindExecutionCandidates_IncludeBlocked(t *testing.T) {
	bs := newFakeBlockingStore()
	cs := &fakeCandidateStore{
		fakeBlockingStore: bs,
		artifacts: &store.ArtifactQueryResult{
			Items: []store.ArtifactProjection{
				{ArtifactPath: "tasks/ready.md", ArtifactID: "TASK-1", Title: "Ready Task", Status: string(domain.StatusPending)},
				{ArtifactPath: "tasks/blocked.md", ArtifactID: "TASK-2", Title: "Blocked Task", Status: string(domain.StatusPending)},
			},
		},
	}

	bs.links["tasks/blocked.md"] = []store.ArtifactLink{
		{SourcePath: "tasks/blocked.md", TargetPath: "tasks/blocker.md", LinkType: "blocked_by"},
	}
	bs.projections["tasks/blocker.md"] = &store.ArtifactProjection{
		ArtifactPath: "tasks/blocker.md",
		Status:       string(domain.StatusPending),
	}

	orch := &Orchestrator{blocking: cs}

	candidates, err := orch.FindExecutionCandidates(context.Background(), ExecutionCandidateFilter{
		IncludeBlocked: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("expected 2 candidates (blocked included), got %d", len(candidates))
	}

	// Verify blocked task has correct blocking info
	for _, c := range candidates {
		if c.TaskPath == "tasks/blocked.md" {
			if !c.Blocked {
				t.Error("expected blocked=true for blocked task")
			}
			if len(c.BlockedBy) != 1 || c.BlockedBy[0] != "tasks/blocker.md" {
				t.Errorf("expected blocked_by [tasks/blocker.md], got %v", c.BlockedBy)
			}
		}
	}
}

func TestFindExecutionCandidates_NilBlockingStore(t *testing.T) {
	orch := &Orchestrator{} // no blocking store

	_, err := orch.FindExecutionCandidates(context.Background(), ExecutionCandidateFilter{})
	if err == nil {
		t.Error("expected error when blocking store is nil")
	}
}
