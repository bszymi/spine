package engine

import (
	"context"
	"testing"

	"github.com/bszymi/spine/internal/domain"
)

// TestBuildAssignmentRequest_PopulatesADR015Fields pins the assignment
// payload contract from ADR-015: every assignment carries the resolved
// RepositoryID, the workspace-built CloneURL (always the workspace's
// git HTTP endpoint, never the runtime binding's external URL), and
// the run BranchName as single-value fields.
func TestBuildAssignmentRequest_PopulatesADR015Fields(t *testing.T) {
	o := &Orchestrator{
		store: &mockRunStore{},
		cloneURLBuilder: func(repoID string) string {
			return "https://spine.test/git/ws-1/" + repoID
		},
	}
	exec := &domain.StepExecution{
		ExecutionID:  "run-1-build-1",
		RunID:        "run-1",
		StepID:       "build",
		ActorID:      "actor-1",
		RepositoryID: "payments-service",
	}
	stepDef := &domain.StepDefinition{
		ID:       "build",
		Name:     "Build",
		Outcomes: []domain.OutcomeDefinition{{ID: "done", Name: "Done", NextStep: "end"}},
	}
	run := &domain.Run{
		RunID:      "run-1",
		TraceID:    "trace-abc",
		BranchName: "spine/run/task-042-build-abc",
		TaskPath:   "tasks/task-042.md",
	}

	req := o.buildAssignmentRequest(context.Background(), exec, stepDef, run)

	if got := req.Context.RepositoryID; got != "payments-service" {
		t.Errorf("RepositoryID: got %q, want payments-service", got)
	}
	if got := req.Context.CloneURL; got != "https://spine.test/git/ws-1/payments-service" {
		t.Errorf("CloneURL: got %q, want https://spine.test/git/ws-1/payments-service", got)
	}
	if got := req.Context.BranchName; got != run.BranchName {
		t.Errorf("BranchName: got %q, want %q", got, run.BranchName)
	}
}

// TestBuildAssignmentRequest_FallsBackToSpineWhenRepoIDEmpty pins the
// safety net for legacy step execution rows that don't carry a
// repository_id (pre-migration, or test fixtures): the assignment
// payload still resolves to spine rather than emitting an empty
// RepositoryID that would confuse runners.
func TestBuildAssignmentRequest_FallsBackToSpineWhenRepoIDEmpty(t *testing.T) {
	captured := ""
	o := &Orchestrator{
		store: &mockRunStore{},
		cloneURLBuilder: func(repoID string) string {
			captured = repoID
			return "https://spine.test/git/ws-1/" + repoID
		},
	}
	exec := &domain.StepExecution{ExecutionID: "x", StepID: "s", RepositoryID: ""}
	stepDef := &domain.StepDefinition{ID: "s", Outcomes: []domain.OutcomeDefinition{{ID: "o", NextStep: "end"}}}
	run := &domain.Run{RunID: "r", TraceID: "t"}

	req := o.buildAssignmentRequest(context.Background(), exec, stepDef, run)

	if got := req.Context.RepositoryID; got != domain.PrimaryRepositoryID {
		t.Errorf("RepositoryID fallback: got %q, want %q", got, domain.PrimaryRepositoryID)
	}
	if captured != domain.PrimaryRepositoryID {
		t.Errorf("CloneURL builder received %q, want %q", captured, domain.PrimaryRepositoryID)
	}
}

// TestBuildAssignmentRequest_NoCloneURLBuilderLeavesURLEmpty pins the
// behaviour when the orchestrator hasn't been wired with a
// CloneURLBuilder (test setup, single-process embedded mode): the
// assignment payload exposes RepositoryID and BranchName but a blank
// CloneURL — never a server-local filesystem path.
func TestBuildAssignmentRequest_NoCloneURLBuilderLeavesURLEmpty(t *testing.T) {
	o := &Orchestrator{store: &mockRunStore{}}
	exec := &domain.StepExecution{ExecutionID: "x", StepID: "s", RepositoryID: "shared-libs"}
	stepDef := &domain.StepDefinition{ID: "s", Outcomes: []domain.OutcomeDefinition{{ID: "o", NextStep: "end"}}}
	run := &domain.Run{RunID: "r", TraceID: "t", BranchName: "spine/run/x"}

	req := o.buildAssignmentRequest(context.Background(), exec, stepDef, run)

	if req.Context.CloneURL != "" {
		t.Errorf("expected empty CloneURL when no builder wired, got %q", req.Context.CloneURL)
	}
	if req.Context.RepositoryID != "shared-libs" {
		t.Errorf("RepositoryID still surfaces: got %q, want shared-libs", req.Context.RepositoryID)
	}
	if req.Context.BranchName != "spine/run/x" {
		t.Errorf("BranchName still surfaces: got %q", req.Context.BranchName)
	}
}
