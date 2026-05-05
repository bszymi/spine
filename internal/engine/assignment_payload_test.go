package engine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bszymi/spine/internal/actor"
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

// TestBuildAssignmentRequest_PopulatesWorkspaceID_AnchorsTASK005 pins
// the TASK-005 contract: when the orchestrator is wired with a
// workspace ID via WithWorkspaceID, the assignment payload publishes
// it as the `workspace_id` field. The same value pairs with the
// CloneURL the runner targets, so the gateway's git HTTP router never
// sees a workspace_id-vs-CloneURL mismatch.
func TestBuildAssignmentRequest_PopulatesWorkspaceID_AnchorsTASK005(t *testing.T) {
	o := &Orchestrator{store: &mockRunStore{}, workspaceID: "ws-1"}
	exec := &domain.StepExecution{ExecutionID: "x", StepID: "s", RepositoryID: "spine"}
	stepDef := &domain.StepDefinition{ID: "s", Outcomes: []domain.OutcomeDefinition{{ID: "o", NextStep: "end"}}}
	run := &domain.Run{RunID: "r", TraceID: "t", BranchName: "spine/run/x"}

	req := o.buildAssignmentRequest(context.Background(), exec, stepDef, run)

	if got := req.Context.WorkspaceID; got != "ws-1" {
		t.Errorf("WorkspaceID: got %q, want ws-1", got)
	}
}

// TestBuildAssignmentRequest_OmitsWorkspaceIDWhenUnwired pins the
// JSON-omitempty contract: when no workspace ID is wired (tests,
// embedded mode), the assignment payload's `workspace_id` field is
// omitted from JSON entirely rather than serialized as a misleading
// blank.
func TestBuildAssignmentRequest_OmitsWorkspaceIDWhenUnwired(t *testing.T) {
	o := &Orchestrator{store: &mockRunStore{}}
	exec := &domain.StepExecution{ExecutionID: "x", StepID: "s", RepositoryID: "spine"}
	stepDef := &domain.StepDefinition{ID: "s", Outcomes: []domain.OutcomeDefinition{{ID: "o", NextStep: "end"}}}
	run := &domain.Run{RunID: "r", TraceID: "t", BranchName: "spine/run/x"}

	req := o.buildAssignmentRequest(context.Background(), exec, stepDef, run)
	body, err := json.Marshal(req.Context)
	if err != nil {
		t.Fatalf("marshal context: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := decoded["workspace_id"]; present {
		t.Errorf("workspace_id should be omitted when unwired; got payload %s", body)
	}
}

// TestBuildAssignmentRequest_PopulatesCommitBaseline_AnchorsTASK005
// pins the TASK-005 contract: when the run carries a per-repo baseline
// in RepositoryBaselines (captured by createRunBranches at run start),
// the assignment payload exposes it as `commit_baseline` for the
// resolved RepositoryID.
func TestBuildAssignmentRequest_PopulatesCommitBaseline_AnchorsTASK005(t *testing.T) {
	o := &Orchestrator{store: &mockRunStore{}}
	exec := &domain.StepExecution{ExecutionID: "x", StepID: "s", RepositoryID: "payments-service"}
	stepDef := &domain.StepDefinition{ID: "s", Outcomes: []domain.OutcomeDefinition{{ID: "o", NextStep: "end"}}}
	run := &domain.Run{
		RunID:      "r",
		TraceID:    "t",
		BranchName: "spine/run/x",
		RepositoryBaselines: map[string]string{
			"spine":            "ffffffffffffffffffffffffffffffffffffffff",
			"payments-service": "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		},
	}

	req := o.buildAssignmentRequest(context.Background(), exec, stepDef, run)

	if got := req.Context.CommitBaseline; got != "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef" {
		t.Errorf("CommitBaseline: got %q, want deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", got)
	}
}

// TestBuildAssignmentRequest_OmitsCommitBaselineWhenUncaptured pins
// the omitempty contract for legacy runs (created before TASK-005 ran
// the baseline capture) and best-effort failures (e.g. Head() lookup
// errored): the assignment payload omits `commit_baseline` from JSON
// entirely rather than emitting an empty string.
func TestBuildAssignmentRequest_OmitsCommitBaselineWhenUncaptured(t *testing.T) {
	o := &Orchestrator{store: &mockRunStore{}}
	exec := &domain.StepExecution{ExecutionID: "x", StepID: "s", RepositoryID: "spine"}
	stepDef := &domain.StepDefinition{ID: "s", Outcomes: []domain.OutcomeDefinition{{ID: "o", NextStep: "end"}}}
	run := &domain.Run{RunID: "r", TraceID: "t", BranchName: "spine/run/x"}

	req := o.buildAssignmentRequest(context.Background(), exec, stepDef, run)
	body, err := json.Marshal(req.Context)
	if err != nil {
		t.Fatalf("marshal context: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := decoded["commit_baseline"]; present {
		t.Errorf("commit_baseline should be omitted when uncaptured; got payload %s", body)
	}
}

// TestBuildAssignmentRequest_NonExecutionStepTolerance pins the
// "existing actor clients tolerate missing clone context for
// non-execution steps" AC: a review-type step on a run that has no
// useful clone target (no branch, no baseline) still produces a
// well-formed AssignmentContext that round-trips through JSON. Empty
// optional fields drop out via omitempty; required ones carry empty
// strings rather than panicking the marshaller.
func TestBuildAssignmentRequest_NonExecutionStepTolerance(t *testing.T) {
	o := &Orchestrator{store: &mockRunStore{}}
	exec := &domain.StepExecution{ExecutionID: "x", StepID: "s", RepositoryID: ""}
	stepDef := &domain.StepDefinition{
		ID:       "review",
		Name:     "Review",
		Type:     domain.StepTypeManual,
		Outcomes: []domain.OutcomeDefinition{{ID: "approved", NextStep: "end"}},
	}
	run := &domain.Run{RunID: "r", TraceID: "t"}

	req := o.buildAssignmentRequest(context.Background(), exec, stepDef, run)

	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	var roundTrip actor.AssignmentRequest
	if err := json.Unmarshal(body, &roundTrip); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if roundTrip.Context.RepositoryID != domain.PrimaryRepositoryID {
		t.Errorf("RepositoryID round-trip: got %q, want %q",
			roundTrip.Context.RepositoryID, domain.PrimaryRepositoryID)
	}
	if roundTrip.Context.WorkspaceID != "" || roundTrip.Context.CommitBaseline != "" {
		t.Errorf("expected omitempty fields blank, got workspace_id=%q commit_baseline=%q",
			roundTrip.Context.WorkspaceID, roundTrip.Context.CommitBaseline)
	}
}
