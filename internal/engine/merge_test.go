package engine

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/bszymi/spine/internal/branchprotect"
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
				ActorID:     "test-actor",
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
		policy:    branchprotect.NewPermissive(),
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
				ActorID:     "test-actor",
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
		policy:   branchprotect.NewPermissive(),
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
		policy:   branchprotect.NewPermissive(),
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
		policy:   branchprotect.NewPermissive(),
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
		policy:   branchprotect.NewPermissive(),
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
		policy:   branchprotect.NewPermissive(),
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
		policy:   branchprotect.NewPermissive(),
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

// cascadeGitOperator captures the read/write/commit calls applyCommitStatus
// makes so the cascade test can assert that all branch-added artifacts land
// in a single commit (TASK-016).
type cascadeGitOperator struct {
	stubGitOperator
	branchFiles map[string]string // key: "ref:path"
	diffs       []git.FileDiff
	mergeBase   string
	writes      map[string]string
	writeOrder  []string
	commits     []git.CommitOpts
}

func (c *cascadeGitOperator) ReadFile(_ context.Context, ref, path string) ([]byte, error) {
	content, ok := c.branchFiles[ref+":"+path]
	if !ok {
		return nil, fmt.Errorf("cascadeGit: no file at %s:%s", ref, path)
	}
	return []byte(content), nil
}

func (c *cascadeGitOperator) Diff(_ context.Context, _, _ string) ([]git.FileDiff, error) {
	return c.diffs, nil
}

func (c *cascadeGitOperator) MergeBase(_ context.Context, _, _ string) (string, error) {
	if c.mergeBase != "" {
		return c.mergeBase, nil
	}
	return "merge-base-sha", nil
}

func (c *cascadeGitOperator) WriteAndStageFile(_ context.Context, path, content string) error {
	if c.writes == nil {
		c.writes = map[string]string{}
	}
	c.writes[path] = content
	c.writeOrder = append(c.writeOrder, path)
	return nil
}

func (c *cascadeGitOperator) Commit(_ context.Context, opts git.CommitOpts) (git.CommitResult, error) {
	c.commits = append(c.commits, opts)
	return git.CommitResult{SHA: "cascade-sha"}, nil
}

// TestApplyCommitStatus_CascadesToBranchAddedArtifacts verifies that when a
// planning run adds a parent artifact plus two child artifacts on its
// branch, applyCommitStatus rewrites the status on all three and lands the
// rewrites in a single commit (TASK-016 acceptance criterion).
func TestApplyCommitStatus_CascadesToBranchAddedArtifacts(t *testing.T) {
	const branch = "spine/plan/epic-042"
	const parentPath = "initiatives/init-001/epics/epic-042/epic.md"
	const child1Path = "initiatives/init-001/epics/epic-042/tasks/task-001.md"
	const child2Path = "initiatives/init-001/epics/epic-042/tasks/task-002.md"

	parentContent := "---\nid: EPIC-042\ntype: Epic\nstatus: Draft\n---\n# EPIC-042\n"
	child1Content := "---\nid: TASK-001\ntype: Task\nstatus: Draft\n---\n# TASK-001\n"
	child2Content := "---\nid: TASK-002\ntype: Task\nstatus: Draft\n---\n# TASK-002\n"

	gitOp := &cascadeGitOperator{
		branchFiles: map[string]string{
			branch + ":" + parentPath: parentContent,
			branch + ":" + child1Path: child1Content,
			branch + ":" + child2Path: child2Content,
		},
		diffs: []git.FileDiff{
			{Path: parentPath, Status: "added"},
			{Path: child1Path, Status: "added"},
			{Path: child2Path, Status: "added"},
		},
	}

	orch := &Orchestrator{git: gitOp}
	run := &domain.Run{
		RunID:      "run-cascade-1",
		BranchName: branch,
		TaskPath:   parentPath,
		TraceID:    "trace-cascade-1",
	}

	if err := orch.applyCommitStatus(context.Background(), run, "Pending"); err != nil {
		t.Fatalf("applyCommitStatus: %v", err)
	}

	if len(gitOp.writes) != 3 {
		t.Fatalf("expected 3 staged writes, got %d: %v", len(gitOp.writes), gitOp.writeOrder)
	}
	for _, p := range []string{parentPath, child1Path, child2Path} {
		got, ok := gitOp.writes[p]
		if !ok {
			t.Errorf("expected write for %s", p)
			continue
		}
		if !strings.Contains(got, "status: Pending") {
			t.Errorf("%s: expected status Pending, got:\n%s", p, got)
		}
		if strings.Contains(got, "status: Draft") {
			t.Errorf("%s: Draft status should be gone, got:\n%s", p, got)
		}
	}

	if len(gitOp.commits) != 1 {
		t.Fatalf("expected 1 commit for cascade, got %d", len(gitOp.commits))
	}
	if !strings.Contains(gitOp.commits[0].Message, "Pending") {
		t.Errorf("commit message should mention target status, got: %s", gitOp.commits[0].Message)
	}
	if gitOp.commits[0].Trailers["Run-ID"] != "run-cascade-1" {
		t.Errorf("commit trailer Run-ID mismatch: %v", gitOp.commits[0].Trailers)
	}
}

// TestApplyCommitStatus_SingleArtifactUnchanged asserts that when the branch
// only adds run.TaskPath, the cascade degenerates to the original
// single-file rewrite (one write, one commit) — preserving prior behaviour.
func TestApplyCommitStatus_SingleArtifactUnchanged(t *testing.T) {
	const branch = "spine/plan/solo"
	const taskPath = "initiatives/init-001/epics/epic-001/tasks/task-solo.md"
	taskContent := "---\nid: TASK-SOLO\ntype: Task\nstatus: Draft\n---\n# TASK-SOLO\n"

	gitOp := &cascadeGitOperator{
		branchFiles: map[string]string{branch + ":" + taskPath: taskContent},
		diffs:       []git.FileDiff{{Path: taskPath, Status: "added"}},
	}

	orch := &Orchestrator{git: gitOp}
	run := &domain.Run{
		RunID:      "run-solo-1",
		BranchName: branch,
		TaskPath:   taskPath,
		TraceID:    "trace-solo-1",
	}

	if err := orch.applyCommitStatus(context.Background(), run, "Pending"); err != nil {
		t.Fatalf("applyCommitStatus: %v", err)
	}

	if len(gitOp.writes) != 1 {
		t.Fatalf("expected 1 write for single-artifact path, got %d", len(gitOp.writes))
	}
	if len(gitOp.commits) != 1 {
		t.Fatalf("expected 1 commit, got %d", len(gitOp.commits))
	}
}

// TestApplyCommitStatus_SkipsNonArtifactMarkdown asserts that an incidental
// Markdown file with its own YAML front matter (but not a Spine artifact
// type) is not rewritten by the cascade. Without this guard, commit.status
// from the workflow would clobber the status field of deliverables and
// other supporting docs a run happens to add on the branch.
func TestApplyCommitStatus_SkipsNonArtifactMarkdown(t *testing.T) {
	const branch = "spine/run/run-deliv"
	const taskPath = "initiatives/init-001/epics/epic-001/tasks/task-001.md"
	const delivPath = "initiatives/init-001/epics/epic-001/tasks/task-001-deliverable.md"

	taskContent := "---\nid: TASK-001\ntype: Task\nstatus: Pending\n---\n# TASK-001\n"
	// Incidental deliverable with its own front matter but no artifact type.
	delivContent := "---\nauthor: bot\nstatus: Draft\n---\n# Deliverable\n"

	gitOp := &cascadeGitOperator{
		branchFiles: map[string]string{
			branch + ":" + taskPath:  taskContent,
			branch + ":" + delivPath: delivContent,
		},
		diffs: []git.FileDiff{
			{Path: taskPath, Status: "added"},
			{Path: delivPath, Status: "added"},
		},
	}

	orch := &Orchestrator{git: gitOp}
	run := &domain.Run{
		RunID:      "run-deliv-1",
		BranchName: branch,
		TaskPath:   taskPath,
		TraceID:    "trace-deliv-1",
	}

	if err := orch.applyCommitStatus(context.Background(), run, "Completed"); err != nil {
		t.Fatalf("applyCommitStatus: %v", err)
	}

	// Only the primary task should be rewritten; the deliverable's status
	// must be left intact.
	if _, touched := gitOp.writes[delivPath]; touched {
		t.Errorf("deliverable at %s must not be rewritten by cascade", delivPath)
	}
	if _, got := gitOp.writes[taskPath]; !got {
		t.Errorf("expected primary task %s to be rewritten", taskPath)
	}
}

// TestApplyCommitStatus_DiffsAgainstMergeBase asserts that cascade
// enumeration scopes the diff to the run-branch's merge-base with main,
// not to main's current tip. Otherwise a concurrent deletion on main would
// surface here as a phantom "added" file on the run branch, and the
// cascade would resurrect it on the merge.
func TestApplyCommitStatus_DiffsAgainstMergeBase(t *testing.T) {
	const branch = "spine/run/run-mb"
	const taskPath = "initiatives/init-001/epics/epic-001/tasks/task-001.md"
	taskContent := "---\nid: TASK-001\ntype: Task\nstatus: Draft\n---\n# TASK-001\n"

	gitOp := &mergeBaseCaptureGitOperator{
		cascadeGitOperator: cascadeGitOperator{
			branchFiles: map[string]string{branch + ":" + taskPath: taskContent},
			diffs:       []git.FileDiff{{Path: taskPath, Status: "added"}},
			mergeBase:   "mb-sha-42",
		},
	}

	orch := &Orchestrator{git: gitOp}
	run := &domain.Run{
		RunID:      "run-mb-1",
		BranchName: branch,
		TaskPath:   taskPath,
		TraceID:    "trace-mb-1",
	}

	if err := orch.applyCommitStatus(context.Background(), run, "Pending"); err != nil {
		t.Fatalf("applyCommitStatus: %v", err)
	}

	if gitOp.diffFrom != "mb-sha-42" {
		t.Errorf("expected Diff.from to be the merge-base SHA, got %q", gitOp.diffFrom)
	}
	if gitOp.diffTo != branch {
		t.Errorf("expected Diff.to to be the run branch, got %q", gitOp.diffTo)
	}
}

// mergeBaseCaptureGitOperator extends cascadeGitOperator to record the
// arguments passed to Diff, so a test can assert the caller uses the
// merge-base instead of the authoritative branch tip.
type mergeBaseCaptureGitOperator struct {
	cascadeGitOperator
	diffFrom string
	diffTo   string
}

func (m *mergeBaseCaptureGitOperator) Diff(ctx context.Context, from, to string) ([]git.FileDiff, error) {
	m.diffFrom = from
	m.diffTo = to
	return m.cascadeGitOperator.Diff(ctx, from, to)
}

// TestApplyCommitStatus_NoOpWhenStatusMatches asserts the existing
// no-rewrite-when-status-already-matches contract survives the cascade
// refactor: if every branch-added artifact already has the target status,
// there is no checkout or commit on the branch.
func TestApplyCommitStatus_NoOpWhenStatusMatches(t *testing.T) {
	const branch = "spine/plan/noop"
	const taskPath = "initiatives/init-001/epics/epic-001/tasks/task-noop.md"
	taskContent := "---\nid: TASK-NOOP\ntype: Task\nstatus: Pending\n---\n# TASK-NOOP\n"

	gitOp := &cascadeGitOperator{
		branchFiles: map[string]string{branch + ":" + taskPath: taskContent},
		diffs:       []git.FileDiff{{Path: taskPath, Status: "added"}},
	}

	orch := &Orchestrator{git: gitOp}
	run := &domain.Run{
		RunID:      "run-noop-1",
		BranchName: branch,
		TaskPath:   taskPath,
		TraceID:    "trace-noop-1",
	}

	if err := orch.applyCommitStatus(context.Background(), run, "Pending"); err != nil {
		t.Fatalf("applyCommitStatus: %v", err)
	}

	if len(gitOp.writes) != 0 {
		t.Errorf("expected no writes when status already matches, got %d", len(gitOp.writes))
	}
	if len(gitOp.commits) != 0 {
		t.Errorf("expected no commits when status already matches, got %d", len(gitOp.commits))
	}
}
