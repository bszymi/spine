package engine

import (
	"context"
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/git"
)

// workflowLifecycleDef mirrors the seeded workflow-lifecycle.yaml for unit
// testing: draft → review → approved (commit: merge=true) or needs_rework.
// This is the minimum shape the orchestrator needs to treat the approved
// outcome as a commit-and-merge — the actual YAML file has more fields.
func workflowLifecycleDef() *domain.WorkflowDefinition {
	return &domain.WorkflowDefinition{
		ID:        "workflow-lifecycle",
		EntryStep: "draft",
		Steps: []domain.StepDefinition{
			{
				ID:   "draft",
				Name: "Draft",
				Type: domain.StepTypeManual,
				Outcomes: []domain.OutcomeDefinition{
					{ID: "submitted", Name: "Submitted", NextStep: "review"},
				},
			},
			{
				ID:   "review",
				Name: "Review",
				Type: domain.StepTypeReview,
				Outcomes: []domain.OutcomeDefinition{
					{
						ID:       "approved",
						Name:     "Approved",
						NextStep: "end",
						Commit:   map[string]string{"merge": "true"},
					},
					{ID: "needs_rework", Name: "Needs Rework", NextStep: "draft"},
				},
			},
		},
	}
}

// TestWorkflowLifecycle_ApprovedOutcomeMergesBranch exercises the approve →
// merge chain end-to-end at the orchestrator level (TASK-005 review finding):
// the scenario test only runs under the `scenario` build tag, so without this
// unit test an approval/merge regression would slip through the normal suite.
func TestWorkflowLifecycle_ApprovedOutcomeMergesBranch(t *testing.T) {
	ctx := context.Background()

	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-wf-1": {
				RunID:         "run-wf-1",
				Status:        domain.RunStatusActive,
				Mode:          domain.RunModePlanning,
				TaskPath:      "workflows/new-flow.yaml",
				WorkflowPath:  "workflows/workflow-lifecycle.yaml",
				WorkflowID:    "workflow-lifecycle",
				CurrentStepID: "review",
				BranchName:    "spine/plan/new-flow-abc",
				TraceID:       "trace-1234567890ab",
			},
		},
		createdSteps: []*domain.StepExecution{
			{
				ExecutionID: "run-wf-1-review-1",
				RunID:       "run-wf-1",
				StepID:      "review",
				Status:      domain.StepStatusInProgress,
				Attempt:     1,
			},
		},
	}
	events := &mockEventEmitter{}
	gitOp := &mockGitOperator{mergeResult: git.MergeResult{SHA: "merge-sha"}}
	orch := &Orchestrator{
		workflows: &mockWorkflowResolver{},
		store:     store,
		actors:    &mockActorAssigner{},
		artifacts: &mockArtifactReader{},
		events:    events,
		git:       gitOp,
		wfLoader:  &mockWorkflowLoader{wfDef: workflowLifecycleDef()},
	}

	// Submit the approved outcome. CompleteRun triggers MergeRunBranch
	// synchronously (run.go:378-386) for planning runs with commit, so by
	// the time this returns the run has gone active → committing →
	// completed and the branch has been merged.
	if err := orch.SubmitStepResult(ctx, "run-wf-1-review-1", StepResult{OutcomeID: "approved"}); err != nil {
		t.Fatalf("SubmitStepResult: %v", err)
	}
	if store.runs["run-wf-1"].CommitMeta["merge"] != "true" {
		t.Errorf("expected CommitMeta[merge]=true to be persisted for the merge trigger, got %v",
			store.runs["run-wf-1"].CommitMeta)
	}
	if got := store.runs["run-wf-1"].Status; got != domain.RunStatusCompleted {
		t.Errorf("after approved, expected completed, got %s", got)
	}
	if len(gitOp.deleted) != 1 || gitOp.deleted[0] != "spine/plan/new-flow-abc" {
		t.Errorf("expected merged branch to be cleaned up, got %v", gitOp.deleted)
	}
	// The transitions must go through committing, not skip straight to
	// completed — that's how audit/events see the merge phase.
	sawCommitting := false
	for _, c := range store.statusCalls {
		if c.runID == "run-wf-1" && c.status == domain.RunStatusCommitting {
			sawCommitting = true
			break
		}
	}
	if !sawCommitting {
		t.Error("expected run to pass through committing state before completed")
	}
}

// TestWorkflowLifecycle_NeedsReworkDoesNotMerge asserts that the non-approval
// outcome does not transition the run to committing (so no merge is
// triggered). Complements the golden-path test.
func TestWorkflowLifecycle_NeedsReworkDoesNotMerge(t *testing.T) {
	ctx := context.Background()

	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-wf-2": {
				RunID:         "run-wf-2",
				Status:        domain.RunStatusActive,
				Mode:          domain.RunModePlanning,
				TaskPath:      "workflows/new-flow.yaml",
				WorkflowPath:  "workflows/workflow-lifecycle.yaml",
				WorkflowID:    "workflow-lifecycle",
				CurrentStepID: "review",
				BranchName:    "spine/plan/new-flow-xyz",
				TraceID:       "trace-xyz456789abc",
			},
		},
		createdSteps: []*domain.StepExecution{
			{
				ExecutionID: "run-wf-2-review-1",
				RunID:       "run-wf-2",
				StepID:      "review",
				Status:      domain.StepStatusInProgress,
				Attempt:     1,
			},
		},
	}
	orch := &Orchestrator{
		workflows: &mockWorkflowResolver{},
		store:     store,
		actors:    &mockActorAssigner{},
		artifacts: &mockArtifactReader{},
		events:    &mockEventEmitter{},
		git:       &stubGitOperator{},
		wfLoader:  &mockWorkflowLoader{wfDef: workflowLifecycleDef()},
	}

	if err := orch.SubmitStepResult(ctx, "run-wf-2-review-1", StepResult{OutcomeID: "needs_rework"}); err != nil {
		t.Fatalf("SubmitStepResult: %v", err)
	}
	if got := store.runs["run-wf-2"].Status; got != domain.RunStatusActive {
		t.Errorf("needs_rework should keep run active, got %s", got)
	}
	if store.runs["run-wf-2"].CommitMeta != nil {
		t.Errorf("needs_rework must not set CommitMeta, got %v", store.runs["run-wf-2"].CommitMeta)
	}
}

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
