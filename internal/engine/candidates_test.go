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

func TestFindExecutionCandidates_FilterBySkills(t *testing.T) {
	bs := newFakeBlockingStore()
	cs := &fakeCandidateStore{
		fakeBlockingStore: bs,
		artifacts: &store.ArtifactQueryResult{
			Items: []store.ArtifactProjection{
				{ArtifactPath: "tasks/backend.md", ArtifactID: "T1", Title: "Backend", Status: string(domain.StatusPending),
					Metadata: []byte(`{"required_skills":["backend"]}`)},
				{ArtifactPath: "tasks/frontend.md", ArtifactID: "T2", Title: "Frontend", Status: string(domain.StatusPending),
					Metadata: []byte(`{"required_skills":["frontend"]}`)},
			},
		},
	}
	orch := &Orchestrator{blocking: cs}

	// Actor has backend skill — should only see backend task.
	candidates, err := orch.FindExecutionCandidates(context.Background(), ExecutionCandidateFilter{
		Skills: []string{"backend"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate filtered by skills, got %d", len(candidates))
	}
	if candidates[0].TaskPath != "tasks/backend.md" {
		t.Errorf("expected backend task, got %s", candidates[0].TaskPath)
	}
}

func TestFindExecutionCandidates_FilterByActorType(t *testing.T) {
	bs := newFakeBlockingStore()
	cs := &fakeCandidateStore{
		fakeBlockingStore: bs,
		artifacts: &store.ArtifactQueryResult{
			Items: []store.ArtifactProjection{
				{ArtifactPath: "tasks/human-only.md", ArtifactID: "T1", Title: "Human Only", Status: string(domain.StatusPending),
					Metadata: []byte(`{"required_skills":["review"],"eligible_actor_types":["human"]}`)},
				{ArtifactPath: "tasks/any.md", ArtifactID: "T2", Title: "Any", Status: string(domain.StatusPending),
					Metadata: []byte(`{"required_skills":["execution"]}`)},
			},
		},
	}
	orch := &Orchestrator{blocking: cs}

	// AI agent should not see human-only task.
	candidates, err := orch.FindExecutionCandidates(context.Background(), ExecutionCandidateFilter{
		ActorType: "ai_agent",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate for ai_agent, got %d", len(candidates))
	}
	if candidates[0].TaskPath != "tasks/any.md" {
		t.Errorf("expected any task, got %s", candidates[0].TaskPath)
	}
}

func TestExtractRequiredSkills(t *testing.T) {
	skills := extractRequiredSkills([]byte(`{"required_skills":["backend","review"]}`))
	if len(skills) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(skills))
	}

	// Nil/empty metadata.
	if s := extractRequiredSkills(nil); s != nil {
		t.Errorf("expected nil for nil metadata, got %v", s)
	}
	if s := extractRequiredSkills([]byte(`{}`)); s != nil {
		t.Errorf("expected nil for empty metadata, got %v", s)
	}

	// Invalid JSON.
	if s := extractRequiredSkills([]byte(`not json`)); s != nil {
		t.Errorf("expected nil for invalid JSON, got %v", s)
	}
}

func TestExtractAllowedActorTypes(t *testing.T) {
	types := extractAllowedActorTypes([]byte(`{"eligible_actor_types":["human","ai_agent"]}`))
	if len(types) != 2 {
		t.Fatalf("expected 2 types, got %d", len(types))
	}

	if types := extractAllowedActorTypes(nil); types != nil {
		t.Errorf("expected nil for nil metadata")
	}
	if types := extractAllowedActorTypes([]byte(`{}`)); types != nil {
		t.Errorf("expected nil for empty metadata")
	}
}

func TestContainsStr(t *testing.T) {
	if !containsStr([]string{"a", "b", "c"}, "b") {
		t.Error("expected true for present item")
	}
	if containsStr([]string{"a", "b"}, "c") {
		t.Error("expected false for absent item")
	}
	if containsStr(nil, "a") {
		t.Error("expected false for nil slice")
	}
}
