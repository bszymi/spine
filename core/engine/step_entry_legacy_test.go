package engine

// INIT-023 TASK-007 AC 1 — legacy pins.
//
// These are the audit's step-entry proofs of concept (engine-authz
// assign/precondition/spine_only, skeptic-selfreview, verify-r1-7),
// committed with NO step-entry policy installed. They pin today's
// permissive binding behaviour for every embedder that has not opted in
// (SMP until its TASK-011): the zero-value StepEntryPolicy must leave
// AssignStep and ClaimStep exactly as they were.
//
// Only the binding assertions are kept — the bind succeeds, the bound
// ActorID, the execution status after the bind. Merge mechanics (TASK-005)
// and post-submit routing (TASK-008) are deliberately not asserted here so
// those sibling tasks cannot turn these pins red for unrelated reasons.
//
// Mutation: making any step-entry check run while its flag is false (for
// example gatesBinding() returning true for the zero value) fails at least
// one of these tests — TestStepEntryLegacy_SelfReviewAssignWithoutWorkflow
// has no workflow loader at all, and the ghost-step bind has no step
// definition.

import (
	"context"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/git"
	"github.com/bszymi/spine/core/artifact"
	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/validation"
)

// recordingMergeGit counts governed merges so step-entry tests can assert
// that a refused bind never reaches the merge path.
type recordingMergeGit struct {
	stubGitOperator
	merges []git.MergeOpts
}

func (g *recordingMergeGit) Merge(_ context.Context, opts git.MergeOpts) (git.MergeResult, error) {
	g.merges = append(g.merges, opts)
	return git.MergeResult{SHA: "merge-sha"}, nil
}

// legacyReviewWorkflow is the audit's review → publish fixture: review is
// human_only, [human], requires the `review` skill; publish is the
// engine-owned spine_only merge step.
func legacyReviewWorkflow() *domain.WorkflowDefinition {
	return &domain.WorkflowDefinition{
		ID:        "task-default",
		EntryStep: "review",
		Steps: []domain.StepDefinition{
			{
				ID: "review", Name: "Code Review", Type: domain.StepTypeReview,
				Execution: &domain.ExecutionConfig{
					Mode:               domain.ExecModeHumanOnly,
					EligibleActorTypes: []string{"human"},
					RequiredSkills:     []string{"review"},
				},
				Outcomes: []domain.OutcomeDefinition{
					{ID: "accepted", Name: "Accepted", NextStep: "publish"},
					{ID: "needs_rework", Name: "Rework", NextStep: "review"},
				},
			},
			{
				ID: "publish", Name: "Publish", Type: domain.StepTypeInternal,
				Execution: &domain.ExecutionConfig{Mode: domain.ExecModeSpineOnly, Handler: "merge"},
				Outcomes: []domain.OutcomeDefinition{
					{ID: "published", Name: "Published", NextStep: "end",
						Commit: map[string]string{"status": "Completed"}},
					{ID: "merge_failed", Name: "Merge failed", NextStep: "review"},
				},
			},
		},
	}
}

// From poc/engine-authz/poc_assign_bypass_test.go.
func TestStepEntryLegacy_AssignSkipsEligibility(t *testing.T) {
	ctx := context.Background()
	st := &mockRunStore{runs: map[string]*domain.Run{
		"run-1": {RunID: "run-1", TaskPath: "tasks/t.md", WorkflowPath: "workflows/task-default.yaml",
			WorkflowVersion: "v1", Status: domain.RunStatusActive, BranchName: "spine/run/t-1",
			TraceID: "trace-1234567890ab"},
	}}
	st.createdSteps = []*domain.StepExecution{{
		ExecutionID: "run-1-review-1", RunID: "run-1", StepID: "review",
		Status: domain.StepStatusWaiting, Attempt: 1, CreatedAt: time.Now(),
		EligibleActorIDs: []string{"alice-reviewer"},
	}}

	bs := newClaimBlockingStore()
	bs.actors["mallory"] = &domain.Actor{ActorID: "mallory", Type: domain.ActorTypeAIAgent, Status: domain.ActorStatusActive}
	bs.actors["bob-human"] = &domain.Actor{ActorID: "bob-human", Type: domain.ActorTypeHuman, Status: domain.ActorStatusActive}

	orch := &Orchestrator{
		store: st, wfLoader: &claimTestWFLoader{wf: legacyReviewWorkflow()},
		events: &mockEventEmitter{}, git: &recordingMergeGit{}, actors: &stubActorAssigner{},
		artifacts: &stubArtifactReader{}, blocking: bs, policy: branchprotect.NewPermissive(),
	}

	// The pull path already refuses mallory (allow-list), and with no
	// allow-list refuses her on type and bob on skills.
	if _, err := orch.ClaimStep(ctx, ClaimRequest{ActorID: "mallory", ExecutionID: "run-1-review-1"}); err == nil {
		t.Fatal("ClaimStep should reject an actor outside the allow-list")
	}
	st.createdSteps = append(st.createdSteps, &domain.StepExecution{
		ExecutionID: "ctl-review", RunID: "run-1", StepID: "review", Status: domain.StepStatusWaiting, Attempt: 1})
	if _, err := orch.ClaimStep(ctx, ClaimRequest{ActorID: "mallory", ExecutionID: "ctl-review"}); err == nil {
		t.Fatal("ClaimStep should reject an ai_agent on a [human] step")
	}
	if _, err := orch.ClaimStep(ctx, ClaimRequest{ActorID: "bob-human", ExecutionID: "ctl-review"}); err == nil {
		t.Fatal("ClaimStep should reject a human without the review skill")
	}
	st.createdSteps = st.createdSteps[:1]

	// Zero value: AssignStep binds mallory with no eligibility check.
	res, err := orch.AssignStep(ctx, AssignRequest{RunID: "run-1", StepID: "review", ActorID: "mallory"})
	if err != nil {
		t.Fatalf("zero-value AssignStep must bind mallory as before, got %v", err)
	}
	if res.Exec.ActorID != "mallory" || res.Exec.Status != domain.StepStatusAssigned {
		t.Fatalf("bound exec = %s/%s, want mallory/assigned", res.Exec.ActorID, res.Exec.Status)
	}

	// Zero value: an actor ID that does not exist, on a step id that is
	// not in the workflow, is bound too.
	st.createdSteps = append(st.createdSteps, &domain.StepExecution{
		ExecutionID: "run-1-ghost-1", RunID: "run-1", StepID: "ghost-step", Status: domain.StepStatusWaiting, Attempt: 1})
	ghost, err := orch.AssignStep(ctx, AssignRequest{RunID: "run-1", StepID: "ghost-step", ActorID: "no-such-actor"})
	if err != nil {
		t.Fatalf("zero-value AssignStep must bind a ghost actor as before, got %v", err)
	}
	if ghost.Exec.ActorID != "no-such-actor" || ghost.Exec.Status != domain.StepStatusAssigned {
		t.Fatalf("ghost exec = %s/%s, want no-such-actor/assigned", ghost.Exec.ActorID, ghost.Exec.Status)
	}
}

// From poc/engine-authz/poc_precondition_bypass_test.go.
func TestStepEntryLegacy_PreconditionFailedStepStillClaimable(t *testing.T) {
	ctx := context.Background()
	wf := &domain.WorkflowDefinition{
		ID: "task-default", EntryStep: "execute",
		Steps: []domain.StepDefinition{{
			ID: "execute", Name: "Execute", Type: domain.StepTypeManual,
			Execution: &domain.ExecutionConfig{Mode: domain.ExecModeHybrid, EligibleActorTypes: []string{"human"}},
			Preconditions: []domain.Precondition{
				{Type: "artifact_status", Config: map[string]string{"status": "Pending"}},
				{Type: "discussions_resolved"},
			},
			Outcomes: []domain.OutcomeDefinition{{ID: "completed", Name: "Done", NextStep: "end"}},
		}},
	}
	st := &mockRunStore{runs: map[string]*domain.Run{
		"run-1": {RunID: "run-1", TaskPath: "tasks/t.md", WorkflowPath: "w.yaml", WorkflowVersion: "v1",
			Status: domain.RunStatusActive, TraceID: "trace-1234567890ab"},
	}}
	st.createdSteps = []*domain.StepExecution{{ExecutionID: "run-1-execute-1", RunID: "run-1", StepID: "execute",
		Status: domain.StepStatusWaiting, Attempt: 1, CreatedAt: time.Now()}}
	bs := newClaimBlockingStore()
	bs.actors["bob"] = &domain.Actor{ActorID: "bob", Type: domain.ActorTypeHuman, Status: domain.ActorStatusActive}

	orch := &Orchestrator{
		store: st, wfLoader: &claimTestWFLoader{wf: wf}, events: &mockEventEmitter{},
		git: &stubGitOperator{}, actors: &stubActorAssigner{}, blocking: bs,
		artifacts: &mockArtifactReader{artifact: &domain.Artifact{Path: "tasks/t.md", Status: "Draft"}},
	}

	if err := orch.ActivateStep(ctx, "run-1", "execute"); err == nil {
		t.Fatal("expected the artifact_status precondition to fail at activation")
	}
	if st.createdSteps[0].Status != domain.StepStatusWaiting {
		t.Fatalf("activation failure must leave the step waiting, got %s", st.createdSteps[0].Status)
	}

	// The callers' helper passes: it evaluates only cross_artifact_valid.
	if res := validation.EvaluatePreconditions(ctx, nil, wf.Steps[0], "tasks/t.md"); res.Status != "passed" {
		t.Fatalf("validation.EvaluatePreconditions = %q, want passed (helper unchanged)", res.Status)
	}

	// Zero value: ClaimStep binds the precondition-failed step.
	cr, err := orch.ClaimStep(ctx, ClaimRequest{ActorID: "bob", ExecutionID: "run-1-execute-1"})
	if err != nil {
		t.Fatalf("zero-value ClaimStep must bind as before, got %v", err)
	}
	if cr.StepID != "execute" || st.createdSteps[0].ActorID != "bob" || st.createdSteps[0].Status != domain.StepStatusAssigned {
		t.Fatalf("claimed exec = %s/%s, want bob/assigned", st.createdSteps[0].ActorID, st.createdSteps[0].Status)
	}
}

// From poc/engine-authz/poc_spine_only_claim_test.go.
func TestStepEntryLegacy_SpineOnlyStepClaimable(t *testing.T) {
	ctx := context.Background()
	wf := &domain.WorkflowDefinition{ID: "task-default", EntryStep: "publish", Steps: []domain.StepDefinition{{
		ID: "publish", Name: "Publish", Type: domain.StepTypeInternal,
		Execution: &domain.ExecutionConfig{Mode: domain.ExecModeSpineOnly, Handler: "merge"},
		Outcomes: []domain.OutcomeDefinition{
			{ID: "published", Name: "Published", NextStep: "end"},
			{ID: "merge_failed", Name: "Failed", NextStep: "publish"},
		},
	}}}
	st := &mockRunStore{runs: map[string]*domain.Run{"run-1": {RunID: "run-1", TaskPath: "t.md",
		WorkflowPath: "w", WorkflowVersion: "v", Status: domain.RunStatusActive, BranchName: "spine/run/x",
		TraceID: "trace-1234567890ab"}}}
	st.createdSteps = []*domain.StepExecution{{ExecutionID: "run-1-publish-1", RunID: "run-1", StepID: "publish",
		Status: domain.StepStatusWaiting, Attempt: 1, CreatedAt: time.Now()}}
	bs := newClaimBlockingStore()
	bs.actors["carol"] = &domain.Actor{ActorID: "carol", Type: domain.ActorTypeHuman, Status: domain.ActorStatusActive}
	orch := &Orchestrator{store: st, wfLoader: &claimTestWFLoader{wf: wf}, events: &mockEventEmitter{},
		git: &recordingMergeGit{}, actors: &stubActorAssigner{}, blocking: bs, policy: branchprotect.NewPermissive()}

	if _, err := orch.ClaimStep(ctx, ClaimRequest{ActorID: "carol", ExecutionID: "run-1-publish-1"}); err != nil {
		t.Fatalf("zero-value ClaimStep must bind the spine_only step as before, got %v", err)
	}
	if st.createdSteps[0].ActorID != "carol" || st.createdSteps[0].Status != domain.StepStatusAssigned {
		t.Fatalf("claimed exec = %s/%s, want carol/assigned", st.createdSteps[0].ActorID, st.createdSteps[0].Status)
	}
}

// From poc/skeptic-selfreview/poc_selfreview_test.go: no workflow loader
// and no blocking store are wired, and the zero-value AssignStep consults
// neither.
func TestStepEntryLegacy_SelfReviewAssignWithoutWorkflow(t *testing.T) {
	store := &mockRunStore{
		runs: map[string]*domain.Run{
			"run-1": {RunID: "run-1", WorkflowPath: "workflows/task-default.yaml", WorkflowVersion: "v1",
				Status: domain.RunStatusActive, TraceID: "trace-1234567890ab"},
		},
	}
	store.createdSteps = []*domain.StepExecution{
		{ExecutionID: "exec-x", RunID: "run-1", StepID: "execute", Status: domain.StepStatusCompleted, ActorID: "member-1", Attempt: 1},
		{ExecutionID: "exec-r", RunID: "run-1", StepID: "review", Status: domain.StepStatusWaiting, Attempt: 1},
	}
	orch := &Orchestrator{store: store, events: &mockEventEmitter{}}
	res, err := orch.AssignStep(context.Background(), AssignRequest{RunID: "run-1", StepID: "review", ActorID: "member-1"})
	if err != nil {
		t.Fatalf("zero-value AssignStep must bind the executor to review as before, got %v", err)
	}
	if res.Exec.ActorID != "member-1" || res.Exec.Status != domain.StepStatusAssigned {
		t.Fatalf("bound exec = %s/%s, want member-1/assigned", res.Exec.ActorID, res.Exec.Status)
	}
}

// legacyRefReader parses real Markdown per ref so precondition checks run
// on genuine parser output.
type legacyRefReader struct{ byRef map[string][]byte }

func (r *legacyRefReader) Read(_ context.Context, path, ref string) (*domain.Artifact, error) {
	b, ok := r.byRef[ref]
	if !ok {
		return nil, domain.NewError(domain.ErrNotFound, "no such ref "+ref)
	}
	return artifact.Parse(path, b)
}

func legacyTaskMarkdown(status string) []byte {
	return []byte("---\nid: TASK-001\ntype: Task\ntitle: T\nstatus: " + status +
		"\nepic: /initiatives/INIT-001/epics/EPIC-001/epic.md\ninitiative: /initiatives/INIT-001/initiative.md\n" +
		"links:\n  - type: parent\n    target: /initiatives/INIT-001/epics/EPIC-001/epic.md\n---\n# T\n")
}

// legacySMPAssign mirrors SMP's embeddedRuns.AssignStep for hybrid and
// human_only steps: LookupStepDef → validation.EvaluatePreconditions →
// AssignStep.
func legacySMPAssign(ctx context.Context, t *testing.T, o *Orchestrator, runID, stepID, actor string) (string, error) {
	t.Helper()
	def, run := o.LookupStepDef(ctx, runID, stepID)
	if def == nil || run == nil {
		t.Fatalf("LookupStepDef(%s, %s) found nothing", runID, stepID)
	}
	res := validation.EvaluatePreconditions(ctx, nil, *def, run.TaskPath)
	if res.Status == "failed" {
		return res.Status, domain.NewError(domain.ErrPrecondition, "step precondition failed")
	}
	_, err := o.AssignStep(ctx, AssignRequest{RunID: runID, StepID: stepID, ActorID: actor})
	return res.Status, err
}

// legacyR17Workflow is the hybrid task-default shape the r1-7 PoC used.
func legacyR17Workflow() *domain.WorkflowDefinition {
	hybrid := &domain.ExecutionConfig{Mode: domain.ExecModeHybrid, EligibleActorTypes: []string{"human"}}
	return &domain.WorkflowDefinition{
		ID: "task-default", EntryStep: "execute",
		Steps: []domain.StepDefinition{
			{ID: "execute", Name: "Execute", Type: domain.StepTypeManual, Execution: hybrid,
				Preconditions: []domain.Precondition{{Type: "artifact_status", Config: map[string]string{"status": "Pending"}}},
				Outcomes:      []domain.OutcomeDefinition{{ID: "completed", Name: "Done", NextStep: "review"}}},
			{ID: "review", Name: "Review", Type: domain.StepTypeReview, Execution: hybrid,
				Preconditions: []domain.Precondition{{Type: "field_value", Config: map[string]string{"field": "status", "value": "In Progress"}}},
				Outcomes:      []domain.OutcomeDefinition{{ID: "accepted", Name: "Accepted", NextStep: "end"}}},
		},
	}
}

// From poc/verify-r1-7/poc_r17_precondition_test.go: SMP's assign funnel
// binds both execute and review of a task that is still Draft on main.
func TestStepEntryLegacy_R17_DraftTaskBoundViaSMPFunnel(t *testing.T) {
	ctx := context.Background()
	st := &mockRunStore{runs: map[string]*domain.Run{
		"run-1": {RunID: "run-1", TaskPath: "initiatives/INIT-001/epics/EPIC-001/tasks/TASK-001.md",
			WorkflowPath: "workflows/task-default.yaml", WorkflowVersion: "v1",
			Status: domain.RunStatusActive, TraceID: "trace-1234567890ab", BranchName: "spine/run/x"},
	}}
	st.createdSteps = []*domain.StepExecution{{ExecutionID: "run-1-execute-1", RunID: "run-1", StepID: "execute",
		Status: domain.StepStatusWaiting, Attempt: 1, CreatedAt: time.Now()}}

	reader := &legacyRefReader{byRef: map[string][]byte{"HEAD": legacyTaskMarkdown("Draft")}}
	o := &Orchestrator{store: st, wfLoader: &claimTestWFLoader{wf: legacyR17Workflow()}, events: &mockEventEmitter{},
		git: &stubGitOperator{}, actors: &stubActorAssigner{}, blocking: newClaimBlockingStore(), artifacts: reader}

	if err := o.ActivateStep(ctx, "run-1", "execute"); err == nil {
		t.Fatal("expected the artifact_status precondition to fail at activation")
	}

	status, err := legacySMPAssign(ctx, t, o, "run-1", "execute", "mallory")
	if status != "passed" || err != nil {
		t.Fatalf("SMP funnel on execute: helper=%q err=%v, want passed/nil", status, err)
	}
	if st.createdSteps[0].ActorID != "mallory" || st.createdSteps[0].Status != domain.StepStatusAssigned {
		t.Fatalf("execute exec = %s/%s, want mallory/assigned", st.createdSteps[0].ActorID, st.createdSteps[0].Status)
	}

	// field_value on `status` can never pass at the zero value: the parser
	// keeps `status` out of Metadata, and the zero-value evaluator reads
	// Metadata. Pinned so the strict typed-field fix stays gated.
	st.createdSteps = append(st.createdSteps, &domain.StepExecution{ExecutionID: "run-1-review-1", RunID: "run-1",
		StepID: "review", Status: domain.StepStatusWaiting, Attempt: 1, CreatedAt: time.Now()})
	reader.byRef["HEAD"] = legacyTaskMarkdown("In Progress")
	if err := o.ActivateStep(ctx, "run-1", "review"); err == nil {
		t.Fatal("zero-value field_value status must still fail even with status In Progress at HEAD")
	}
	reader.byRef["HEAD"] = legacyTaskMarkdown("Draft")

	status, err = legacySMPAssign(ctx, t, o, "run-1", "review", "mallory")
	if status != "passed" || err != nil {
		t.Fatalf("SMP funnel on review: helper=%q err=%v, want passed/nil", status, err)
	}
	if st.createdSteps[1].ActorID != "mallory" || st.createdSteps[1].Status != domain.StepStatusAssigned {
		t.Fatalf("review exec = %s/%s, want mallory/assigned", st.createdSteps[1].ActorID, st.createdSteps[1].Status)
	}
}
