package engine

// INIT-023 TASK-007 — binding checks of the step-entry policy (AC 2-7,
// 16, 23, 27). Each test inverts an audit proof of concept: with the
// policy on, the bind the PoC demonstrated is refused and nothing is
// written.

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/domain"
)

// ── AC 2 ──

// From poc/engine-authz/poc_assign_bypass_test.go, policy on.
// Mutation: deleting any one of checks 3-6 in checkStepEntryActor fails the
// matching row (each row is built so that exactly one check refuses it,
// and the error text is asserted as well as the code).
func TestStepEntry_AssignRefusesIneligibleActor(t *testing.T) {
	type row struct {
		name     string
		actor    string
		step     string   // default "review"
		stored   []string // stored allow-list; default [alice-reviewer]
		reqList  []string // AssignRequest.EligibleActorIDs
		want     domain.ErrorCode
		msg      string
		wantList []string // stored list after a successful bind
	}
	rows := []row{
		{name: "ai_agent on a human_only step", actor: "mallory", want: domain.ErrConflict, msg: "human_only"},
		{name: "actor that does not exist", actor: "no-such-actor", want: domain.ErrNotFound, msg: "no-such-actor"},
		{name: "suspended human", actor: "sam-suspended", stored: []string{"alice-reviewer", "sam-suspended"},
			want: domain.ErrForbidden, msg: "suspended"},
		{name: "reviewer outside the allow-list", actor: "bob-reviewer", want: domain.ErrForbidden, msg: "eligible actor list"},
		{name: "allow-listed human without the review skill", actor: "carol-noskill",
			stored: []string{"alice-reviewer", "carol-noskill"}, want: domain.ErrConflict, msg: "missing required skills"},
		{name: "allow-listed reviewer", actor: "alice-reviewer", wantList: []string{"alice-reviewer"}},
		// Design-check row (Owner question 5 default): the assigner may
		// replace the stored list; the replacement becomes the effective
		// list. Flipping to narrow-only must change this row deliberately.
		{name: "assigner replaces the stored list with itself", actor: "bob-reviewer",
			reqList: []string{"bob-reviewer"}, wantList: []string{"bob-reviewer"}},
		// Design-check row: a step absent from the pinned workflow fails closed.
		{name: "step absent from the pinned workflow", actor: "alice-reviewer", step: "ghost-step",
			want: domain.ErrNotFound, msg: "ghost-step"},
	}

	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			ctx := context.Background()
			stepID := r.step
			if stepID == "" {
				stepID = "review"
			}
			stored := r.stored
			if stored == nil {
				stored = []string{"alice-reviewer"}
			}
			st := &mockRunStore{runs: map[string]*domain.Run{"run-1": runFixture("run-1", domain.RunStatusActive)}}
			st.createdSteps = []*domain.StepExecution{{
				ExecutionID: "run-1-" + stepID + "-1", RunID: "run-1", StepID: stepID,
				Status: domain.StepStatusWaiting, Attempt: 1, CreatedAt: time.Now(),
				EligibleActorIDs: append([]string(nil), stored...),
			}}
			bs := newClaimBlockingStore()
			bs.actors["mallory"] = activeActor("mallory", domain.ActorTypeAIAgent)
			bs.actors["alice-reviewer"] = activeActor("alice-reviewer", domain.ActorTypeHuman)
			bs.actors["bob-reviewer"] = activeActor("bob-reviewer", domain.ActorTypeHuman)
			bs.actors["carol-noskill"] = activeActor("carol-noskill", domain.ActorTypeHuman)
			bs.actors["sam-suspended"] = &domain.Actor{ActorID: "sam-suspended", Type: domain.ActorTypeHuman, Status: domain.ActorStatusSuspended}
			for _, id := range []string{"mallory", "alice-reviewer", "bob-reviewer", "sam-suspended"} {
				bs.skills[id] = skillSet("review")
			}
			gitOp := &recordingMergeGit{}
			orch := &Orchestrator{
				store: st, wfLoader: &claimTestWFLoader{wf: legacyReviewWorkflow()},
				events: &mockEventEmitter{}, git: gitOp, actors: &stubActorAssigner{},
				artifacts: &stubArtifactReader{}, blocking: bs, policy: branchprotect.NewPermissive(),
			}
			orch.WithStepEntryPolicy(StrictStepEntryPolicy())
			before := snapshotExecs(st)

			res, err := orch.AssignStep(ctx, AssignRequest{RunID: "run-1", StepID: stepID, ActorID: r.actor, EligibleActorIDs: r.reqList})
			if r.want != "" {
				requireCode(t, err, r.want, "AssignStep("+r.actor+")")
				if !strings.Contains(err.Error(), r.msg) {
					t.Fatalf("error %q does not mention %q", err.Error(), r.msg)
				}
				requireExecsUnchanged(t, st, before, "refused assign")
				if e := st.createdSteps[0]; e.Status != domain.StepStatusWaiting || e.ActorID != "" {
					t.Fatalf("refused assign left exec %s/%q", e.Status, e.ActorID)
				}
			} else {
				if err != nil {
					t.Fatalf("AssignStep(%s): %v", r.actor, err)
				}
				if res.Exec.ActorID != r.actor || res.Exec.Status != domain.StepStatusAssigned {
					t.Fatalf("bound exec = %s/%s", res.Exec.ActorID, res.Exec.Status)
				}
				if got := st.createdSteps[0].EligibleActorIDs; !reflect.DeepEqual(got, r.wantList) {
					t.Fatalf("stored allow-list = %v, want %v", got, r.wantList)
				}
			}
			if len(gitOp.merges) != 0 {
				t.Fatalf("a bind must never reach the merge path, got %d merges", len(gitOp.merges))
			}
		})
	}
}

// ── AC 3 ──

// From poc/engine-authz/poc_precondition_bypass_test.go, policy on.
// Mutation: removing the checkStepEntryPreconditions call from AssignStep
// or ClaimStep fails the matching sub-test.
func TestStepEntry_PreconditionFailedStepNotBindable(t *testing.T) {
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
	type fixture struct {
		st   *mockRunStore
		as   *memAssignmentStore
		art  *mockArtifactReader
		disc *fakeDiscussions
		orch *Orchestrator
	}
	setup := func(status domain.ArtifactStatus, open bool) fixture {
		f := fixture{
			st:   &mockRunStore{runs: map[string]*domain.Run{"run-1": runFixture("run-1", domain.RunStatusActive)}},
			as:   newMemAssignmentStore(),
			art:  &mockArtifactReader{artifact: &domain.Artifact{Path: "tasks/t.md", Status: status}},
			disc: &fakeDiscussions{open: open},
		}
		f.st.createdSteps = []*domain.StepExecution{{ExecutionID: "run-1-execute-1", RunID: "run-1", StepID: "execute",
			Status: domain.StepStatusWaiting, Attempt: 1, CreatedAt: time.Now()}}
		bs := newClaimBlockingStore()
		bs.actors["bob"] = activeActor("bob", domain.ActorTypeHuman)
		f.orch = &Orchestrator{store: f.st, wfLoader: &claimTestWFLoader{wf: wf}, events: &mockEventEmitter{},
			git: &stubGitOperator{}, actors: &stubActorAssigner{}, blocking: bs, artifacts: f.art,
			assignments: f.as, discussions: f.disc}
		f.orch.WithStepEntryPolicy(StepEntryPolicy{EnforcePreconditions: true})
		return f
	}
	ctx := context.Background()

	t.Run("draft task refuses claim", func(t *testing.T) {
		f := setup("Draft", false)
		before := snapshotExecs(f.st)
		_, err := f.orch.ClaimStep(ctx, ClaimRequest{ActorID: "bob", ExecutionID: "run-1-execute-1"})
		requireCode(t, err, domain.ErrPrecondition, "ClaimStep on a Draft task")
		if len(f.as.assignments) != 0 {
			t.Fatalf("a refused claim must not create an assignment row, got %d", len(f.as.assignments))
		}
		requireExecsUnchanged(t, f.st, before, "refused claim")
	})
	t.Run("draft task refuses assign", func(t *testing.T) {
		f := setup("Draft", false)
		before := snapshotExecs(f.st)
		_, err := f.orch.AssignStep(ctx, AssignRequest{RunID: "run-1", StepID: "execute", ActorID: "bob"})
		requireCode(t, err, domain.ErrPrecondition, "AssignStep on a Draft task")
		requireExecsUnchanged(t, f.st, before, "refused assign")
	})
	t.Run("open discussion refuses claim and assign", func(t *testing.T) {
		f := setup("Pending", true)
		before := snapshotExecs(f.st)
		_, err := f.orch.ClaimStep(ctx, ClaimRequest{ActorID: "bob", ExecutionID: "run-1-execute-1"})
		requireCode(t, err, domain.ErrPrecondition, "ClaimStep with an open thread")
		_, err = f.orch.AssignStep(ctx, AssignRequest{RunID: "run-1", StepID: "execute", ActorID: "bob"})
		requireCode(t, err, domain.ErrPrecondition, "AssignStep with an open thread")
		requireExecsUnchanged(t, f.st, before, "refused binds")
		if len(f.as.assignments) != 0 {
			t.Fatalf("refused binds must not create assignment rows")
		}
	})
	t.Run("pending task with resolved threads is claimable", func(t *testing.T) {
		f := setup("Pending", false)
		if _, err := f.orch.ClaimStep(ctx, ClaimRequest{ActorID: "bob", ExecutionID: "run-1-execute-1"}); err != nil {
			t.Fatalf("ClaimStep once preconditions hold: %v", err)
		}
		if e := f.st.createdSteps[0]; e.ActorID != "bob" || e.Status != domain.StepStatusAssigned {
			t.Fatalf("claimed exec = %s/%s", e.ActorID, e.Status)
		}
	})
}

// ── AC 4 ──

// From poc/verify-r1-7: SMP's funnel (LookupStepDef →
// validation.EvaluatePreconditions → AssignStep) with EnforcePreconditions.
// The helper still answers "passed" — it is unchanged — and the engine
// refuses the bind instead.
func TestStepEntry_R17_SMPFunnelRefusesDraftTask(t *testing.T) {
	ctx := context.Background()
	st := &mockRunStore{runs: map[string]*domain.Run{
		"run-1": {RunID: "run-1", TaskPath: "initiatives/INIT-001/epics/EPIC-001/tasks/TASK-001.md",
			WorkflowPath: "workflows/task-default.yaml", WorkflowVersion: "v1",
			Status: domain.RunStatusActive, TraceID: "trace-1234567890ab", BranchName: "spine/run/x"},
	}}
	st.createdSteps = []*domain.StepExecution{
		{ExecutionID: "run-1-execute-1", RunID: "run-1", StepID: "execute", Status: domain.StepStatusWaiting, Attempt: 1, CreatedAt: time.Now()},
		{ExecutionID: "run-1-review-1", RunID: "run-1", StepID: "review", Status: domain.StepStatusWaiting, Attempt: 1, CreatedAt: time.Now()},
	}
	reader := &legacyRefReader{byRef: map[string][]byte{"HEAD": legacyTaskMarkdown("Draft")}}
	o := &Orchestrator{store: st, wfLoader: &claimTestWFLoader{wf: legacyR17Workflow()}, events: &mockEventEmitter{},
		git: &stubGitOperator{}, actors: &stubActorAssigner{}, blocking: newClaimBlockingStore(), artifacts: reader}
	o.WithStepEntryPolicy(StepEntryPolicy{EnforcePreconditions: true})
	before := snapshotExecs(st)

	for _, step := range []string{"execute", "review"} {
		helper, err := legacySMPAssign(ctx, t, o, "run-1", step, "mallory")
		if helper != "passed" {
			t.Fatalf("validation.EvaluatePreconditions(%s) = %q, want passed (helper must stay unchanged)", step, helper)
		}
		requireCode(t, err, domain.ErrPrecondition, "SMP funnel AssignStep("+step+") on a Draft task")
	}
	requireExecsUnchanged(t, st, before, "refused SMP funnel binds")
}

// ── AC 5 ──

// From poc/engine-authz/poc_spine_only_claim_test.go.
// Mutation: deleting check 2 (engine-owned steps) fails the first sub-test.
func TestStepEntry_SpineOnlyStepNotBindable(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	wf := &domain.WorkflowDefinition{ID: "task-default", EntryStep: "publish", Steps: []domain.StepDefinition{{
		ID: "publish", Name: "Publish", Type: domain.StepTypeInternal,
		Execution: &domain.ExecutionConfig{Mode: domain.ExecModeSpineOnly, Handler: "merge"},
		Outcomes: []domain.OutcomeDefinition{
			{ID: "published", Name: "Published", NextStep: "end"},
			{ID: "merge_failed", Name: "Failed", NextStep: "publish"},
		},
	}}}
	setup := func(p StepEntryPolicy) (*mockRunStore, *recordingMergeGit, *Orchestrator) {
		st := &mockRunStore{runs: map[string]*domain.Run{"run-1": runFixture("run-1", domain.RunStatusActive)}}
		st.createdSteps = []*domain.StepExecution{{ExecutionID: "run-1-publish-1", RunID: "run-1", StepID: "publish",
			Status: domain.StepStatusWaiting, Attempt: 1, CreatedAt: time.Now()}}
		bs := newClaimBlockingStore()
		bs.actors["carol"] = activeActor("carol", domain.ActorTypeHuman)
		gitOp := &recordingMergeGit{}
		o := &Orchestrator{store: st, wfLoader: &claimTestWFLoader{wf: wf}, events: &mockEventEmitter{},
			git: gitOp, actors: &stubActorAssigner{}, blocking: bs, policy: branchprotect.NewPermissive(),
			artifacts: &stubArtifactReader{}}
		o.WithStepEntryPolicy(p)
		return st, gitOp, o
	}
	ctx := context.Background()

	t.Run("claim and assign refused under EnforceExecutionMode", func(t *testing.T) {
		st, gitOp, o := setup(StepEntryPolicy{EnforceExecutionMode: true})
		before := snapshotExecs(st)
		_, err := o.ClaimStep(ctx, ClaimRequest{ActorID: "carol", ExecutionID: "run-1-publish-1"})
		requireCode(t, err, domain.ErrConflict, "ClaimStep on a spine_only step")
		_, err = o.AssignStep(ctx, AssignRequest{RunID: "run-1", StepID: "publish", ActorID: "carol"})
		requireCode(t, err, domain.ErrConflict, "AssignStep on a spine_only step")
		requireExecsUnchanged(t, st, before, "refused binds")
		if st.runs["run-1"].Status != domain.RunStatusActive {
			t.Fatalf("run status = %s, want active", st.runs["run-1"].Status)
		}
		if len(gitOp.merges) != 0 {
			t.Fatalf("merges = %d, want 0", len(gitOp.merges))
		}
	})
	t.Run("engine recovery through ActivateStep still merges under the strict policy", func(t *testing.T) {
		_, gitOp, o := setup(StrictStepEntryPolicy())
		if err := o.ActivateStep(ctx, "run-1", "publish"); err != nil {
			t.Fatalf("ActivateStep(publish): %v", err)
		}
		if len(gitOp.merges) != 1 {
			t.Fatalf("merges = %d, want exactly 1 (engine-step progression unaffected)", len(gitOp.merges))
		}
	})
}

// ── AC 6 ──

// TestStepEntry_ModeTable walks Design §5 for both binding operations.
// Mutation: changing any entry of modeActorType fails a row.
func TestStepEntry_ModeTable(t *testing.T) {
	steps := []domain.StepDefinition{
		{ID: "spine-only", Name: "s", Type: domain.StepTypeInternal, Execution: &domain.ExecutionConfig{Mode: domain.ExecModeSpineOnly, Handler: "merge"}},
		{ID: "internal-hybrid", Name: "s", Type: domain.StepTypeInternal, Execution: &domain.ExecutionConfig{Mode: domain.ExecModeHybrid}},
		{ID: "spine-only-manual", Name: "s", Type: domain.StepTypeManual, Execution: &domain.ExecutionConfig{Mode: domain.ExecModeSpineOnly}},
		{ID: "automated-only", Name: "s", Type: domain.StepTypeAutomated, Execution: &domain.ExecutionConfig{Mode: domain.ExecModeAutomatedOnly}},
		{ID: "ai-only", Name: "s", Type: domain.StepTypeManual, Execution: &domain.ExecutionConfig{Mode: domain.ExecModeAIOnly}},
		{ID: "human-only", Name: "s", Type: domain.StepTypeManual, Execution: &domain.ExecutionConfig{Mode: domain.ExecModeHumanOnly}},
		{ID: "hybrid", Name: "s", Type: domain.StepTypeManual, Execution: &domain.ExecutionConfig{Mode: domain.ExecModeHybrid}},
		{ID: "mode-unset", Name: "s", Type: domain.StepTypeManual, Execution: &domain.ExecutionConfig{}},
		{ID: "no-execution-block", Name: "s", Type: domain.StepTypeManual},
	}
	for i := range steps {
		steps[i].Outcomes = []domain.OutcomeDefinition{{ID: "done", Name: "Done", NextStep: "end"}}
	}
	wf := &domain.WorkflowDefinition{ID: "modes", EntryStep: "hybrid", Steps: steps}

	actors := map[domain.ActorType]string{
		domain.ActorTypeHuman:     "a-human",
		domain.ActorTypeAIAgent:   "an-agent",
		domain.ActorTypeAutomated: "a-runner",
	}
	allowed := func(stepID string, at domain.ActorType) bool {
		switch stepID {
		case "spine-only", "internal-hybrid", "spine-only-manual":
			return false
		case "automated-only":
			return at == domain.ActorTypeAutomated
		case "ai-only":
			return at == domain.ActorTypeAIAgent
		case "human-only":
			return at == domain.ActorTypeHuman
		default:
			return true
		}
	}

	type op string
	const (
		opAssign op = "assign"
		opClaim  op = "claim"
	)
	run := func(t *testing.T, stepID, actorID string, o op) error {
		t.Helper()
		st := &mockRunStore{runs: map[string]*domain.Run{"run-1": runFixture("run-1", domain.RunStatusActive)}}
		st.createdSteps = []*domain.StepExecution{{ExecutionID: "run-1-" + stepID + "-1", RunID: "run-1", StepID: stepID,
			Status: domain.StepStatusWaiting, Attempt: 1, CreatedAt: time.Now()}}
		bs := newClaimBlockingStore()
		for at, id := range actors {
			bs.actors[id] = activeActor(id, at)
		}
		orch := &Orchestrator{store: st, wfLoader: &claimTestWFLoader{wf: wf}, events: &mockEventEmitter{},
			git: &stubGitOperator{}, actors: &stubActorAssigner{}, blocking: bs, artifacts: &stubArtifactReader{}}
		orch.WithStepEntryPolicy(StepEntryPolicy{EnforceExecutionMode: true})
		before := snapshotExecs(st)
		var err error
		if o == opAssign {
			_, err = orch.AssignStep(context.Background(), AssignRequest{RunID: "run-1", StepID: stepID, ActorID: actorID})
		} else {
			_, err = orch.ClaimStep(context.Background(), ClaimRequest{ActorID: actorID, ExecutionID: "run-1-" + stepID + "-1"})
		}
		if err != nil {
			requireExecsUnchanged(t, st, before, "refused "+string(o))
		} else if e := st.createdSteps[0]; e.ActorID != actorID || e.Status != domain.StepStatusAssigned {
			t.Fatalf("bound exec = %s/%s", e.ActorID, e.Status)
		}
		return err
	}

	for _, s := range steps {
		for at, actorID := range actors {
			for _, o := range []op{opAssign, opClaim} {
				s, at, actorID, o := s, at, actorID, o
				t.Run(string(o)+"/"+s.ID+"/"+string(at), func(t *testing.T) {
					err := run(t, s.ID, actorID, o)
					if allowed(s.ID, at) {
						if err != nil {
							t.Fatalf("%s of %s on %s: want bound, got %v", o, at, s.ID, err)
						}
						return
					}
					requireCode(t, err, domain.ErrConflict, string(o)+" of "+string(at)+" on "+s.ID)
				})
			}
		}
	}
	for _, o := range []op{opAssign, opClaim} {
		o := o
		t.Run(string(o)+"/human-only/unknown actor", func(t *testing.T) {
			requireCode(t, run(t, "human-only", "no-such-actor", o), domain.ErrNotFound, string(o)+" of an unknown actor")
		})
	}
}

// ── AC 7 ──

// sodWorkflow: execute (manual) → verify (review) → review (review), a
// divergence-branch manual step, and an ai_only review step for the
// auto-selection rows. No execution blocks on the human steps so only the
// separation-of-duties rule can refuse a bind.
func sodWorkflow() *domain.WorkflowDefinition {
	return &domain.WorkflowDefinition{
		ID: "sod", EntryStep: "execute",
		Steps: []domain.StepDefinition{
			{ID: "execute", Name: "Execute", Type: domain.StepTypeManual,
				Outcomes: []domain.OutcomeDefinition{{ID: "completed", Name: "Done", NextStep: "verify"}}},
			{ID: "verify", Name: "Verify", Type: domain.StepTypeReview,
				Outcomes: []domain.OutcomeDefinition{{ID: "ok", Name: "OK", NextStep: "review"}}},
			{ID: "review", Name: "Review", Type: domain.StepTypeReview,
				Outcomes: []domain.OutcomeDefinition{
					{ID: "accepted", Name: "Accepted", NextStep: "end"},
					{ID: "needs_rework", Name: "Rework", NextStep: "execute"},
				}},
			{ID: "branch-work", Name: "Branch work", Type: domain.StepTypeManual,
				Outcomes: []domain.OutcomeDefinition{{ID: "done", Name: "Done", NextStep: "end"}}},
			{ID: "ai-review", Name: "AI review", Type: domain.StepTypeReview,
				Execution: &domain.ExecutionConfig{Mode: domain.ExecModeAIOnly},
				Outcomes:  []domain.OutcomeDefinition{{ID: "accepted", Name: "Accepted", NextStep: "end"}}},
		},
	}
}

type sodFixture struct {
	st   *mockRunStore
	as   *memAssignmentStore
	gw   *countingActorAssigner
	orch *Orchestrator
}

func newSoDFixture(p StepEntryPolicy, execs ...*domain.StepExecution) sodFixture {
	f := sodFixture{
		st: &mockRunStore{runs: map[string]*domain.Run{"run-1": runFixture("run-1", domain.RunStatusActive)}},
		as: newMemAssignmentStore(),
		gw: &countingActorAssigner{},
	}
	f.st.createdSteps = execs
	f.orch = &Orchestrator{store: f.st, wfLoader: &claimTestWFLoader{wf: sodWorkflow()}, events: &mockEventEmitter{},
		git: &stubGitOperator{}, actors: f.gw, blocking: newClaimBlockingStore(), artifacts: &stubArtifactReader{},
		assignments: f.as}
	f.orch.WithStepEntryPolicy(p)
	return f
}

func sodExec(id, stepID string, status domain.StepExecutionStatus, actorID string) *domain.StepExecution {
	return &domain.StepExecution{ExecutionID: id, RunID: "run-1", StepID: stepID, Status: status,
		ActorID: actorID, Attempt: 1, CreatedAt: time.Now()}
}

// From poc/skeptic-selfreview, with only SeparationOfDuties.
// Mutation: dropping the Type == review condition fails "executor binds
// the rework execute"; dropping the manual/automated author filter fails
// "verify-only actor may review"; removing the symmetric guard fails
// "reviewer holding a live review cannot take a branch step".
func TestStepEntry_SeparationOfDuties(t *testing.T) {
	sod := StepEntryPolicy{SeparationOfDuties: true}
	ctx := context.Background()

	bindBoth := func(t *testing.T, f sodFixture, stepID, execID, actorID string) (assignErr, claimErr error) {
		t.Helper()
		before := snapshotExecs(f.st)
		_, assignErr = f.orch.AssignStep(ctx, AssignRequest{RunID: "run-1", StepID: stepID, ActorID: actorID})
		if assignErr != nil {
			requireExecsUnchanged(t, f.st, before, "refused assign")
		} else {
			// Reset the bind so the claim sees the same waiting row.
			e := execByID(f.st, execID)
			e.Status, e.ActorID = domain.StepStatusWaiting, ""
		}
		_, claimErr = f.orch.ClaimStep(ctx, ClaimRequest{ActorID: actorID, ExecutionID: execID})
		if claimErr != nil {
			requireExecsUnchanged(t, f.st, before, "refused claim")
		}
		return assignErr, claimErr
	}

	t.Run("executor of a completed execute is refused on review", func(t *testing.T) {
		f := newSoDFixture(sod,
			sodExec("run-1-execute-1", "execute", domain.StepStatusCompleted, "member-1"),
			sodExec("run-1-review-1", "review", domain.StepStatusWaiting, ""))
		aErr, cErr := bindBoth(t, f, "review", "run-1-review-1", "member-1")
		for _, err := range []error{aErr, cErr} {
			requireCode(t, err, domain.ErrForbidden, "self-review")
			if !strings.Contains(err.Error(), `"execute"`) {
				t.Fatalf("SoD refusal %q must name the executed step", err.Error())
			}
		}
	})
	t.Run("actor of a failed execute attempt is refused on review", func(t *testing.T) {
		f := newSoDFixture(sod,
			sodExec("run-1-execute-1", "execute", domain.StepStatusFailed, "failer"),
			sodExec("run-1-execute-2", "execute", domain.StepStatusCompleted, "member-1"),
			sodExec("run-1-review-1", "review", domain.StepStatusWaiting, ""))
		aErr, cErr := bindBoth(t, f, "review", "run-1-review-1", "failer")
		requireCode(t, aErr, domain.ErrForbidden, "assign of a failed-attempt author")
		requireCode(t, cErr, domain.ErrForbidden, "claim of a failed-attempt author")
	})
	t.Run("verify-only actor may review", func(t *testing.T) {
		f := newSoDFixture(sod,
			sodExec("run-1-execute-1", "execute", domain.StepStatusCompleted, "member-1"),
			sodExec("run-1-verify-1", "verify", domain.StepStatusCompleted, "verifier"),
			sodExec("run-1-review-1", "review", domain.StepStatusWaiting, ""))
		aErr, cErr := bindBoth(t, f, "review", "run-1-review-1", "verifier")
		if aErr != nil || cErr != nil {
			t.Fatalf("an actor who only executed a review-type step may review: assign=%v claim=%v", aErr, cErr)
		}
	})
	t.Run("independent member may review", func(t *testing.T) {
		f := newSoDFixture(sod,
			sodExec("run-1-execute-1", "execute", domain.StepStatusCompleted, "member-1"),
			sodExec("run-1-review-1", "review", domain.StepStatusWaiting, ""))
		aErr, cErr := bindBoth(t, f, "review", "run-1-review-1", "member-2")
		if aErr != nil || cErr != nil {
			t.Fatalf("member-2: assign=%v claim=%v", aErr, cErr)
		}
	})
	t.Run("executor binds the rework execute", func(t *testing.T) {
		f := newSoDFixture(sod,
			sodExec("run-1-execute-1", "execute", domain.StepStatusCompleted, "member-1"),
			sodExec("run-1-review-1", "review", domain.StepStatusCompleted, "member-2"),
			sodExec("run-1-execute-2", "execute", domain.StepStatusWaiting, ""))
		aErr, cErr := bindBoth(t, f, "execute", "run-1-execute-2", "member-1")
		if aErr != nil || cErr != nil {
			t.Fatalf("rework by the executor: assign=%v claim=%v", aErr, cErr)
		}
	})
	t.Run("reviewer holding a live review cannot take a branch step", func(t *testing.T) {
		f := newSoDFixture(sod,
			sodExec("run-1-execute-1", "execute", domain.StepStatusCompleted, "member-1"),
			sodExec("run-1-review-1", "review", domain.StepStatusInProgress, "erin"),
			&domain.StepExecution{ExecutionID: "run-1-b1-branch-work-1", RunID: "run-1", StepID: "branch-work",
				BranchID: "b1", Status: domain.StepStatusWaiting, Attempt: 1, CreatedAt: time.Now()})
		aErr, cErr := bindBoth(t, f, "branch-work", "run-1-b1-branch-work-1", "erin")
		requireCode(t, aErr, domain.ErrForbidden, "assign of a live reviewer to an author step")
		requireCode(t, cErr, domain.ErrForbidden, "claim of a live reviewer to an author step")
	})
	t.Run("reviewer who sent work back may execute the rework", func(t *testing.T) {
		rev := sodExec("run-1-review-1", "review", domain.StepStatusCompleted, "erin")
		rev.OutcomeID = "needs_rework"
		f := newSoDFixture(sod,
			sodExec("run-1-execute-1", "execute", domain.StepStatusCompleted, "member-1"),
			rev,
			sodExec("run-1-execute-2", "execute", domain.StepStatusWaiting, ""))
		aErr, cErr := bindBoth(t, f, "execute", "run-1-execute-2", "erin")
		if aErr != nil || cErr != nil {
			t.Fatalf("rework by the former reviewer: assign=%v claim=%v", aErr, cErr)
		}
	})
	for _, tc := range []struct {
		name      string
		selected  string
		wantActor string
	}{
		{name: "auto-selection of the author leaves the review waiting", selected: "member-1", wantActor: ""},
		{name: "auto-selection of a non-author binds", selected: "agent-2", wantActor: "agent-2"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			f := newSoDFixture(sod,
				sodExec("run-1-execute-1", "execute", domain.StepStatusCompleted, "member-1"),
				sodExec("run-1-ai-review-1", "ai-review", domain.StepStatusWaiting, ""))
			f.orch.actorSelector = &mockActorSelector{actor: activeActor(tc.selected, domain.ActorTypeAIAgent)}
			if err := f.orch.ActivateStep(ctx, "run-1", "ai-review"); err != nil {
				t.Fatalf("ActivateStep: %v", err)
			}
			e := execByID(f.st, "run-1-ai-review-1")
			if e.ActorID != tc.wantActor {
				t.Fatalf("ai-review actor = %q, want %q", e.ActorID, tc.wantActor)
			}
			if tc.wantActor == "" {
				if e.Status != domain.StepStatusWaiting || len(f.gw.delivered) != 0 {
					t.Fatalf("author auto-selection must leave the step waiting with no dispatch: status=%s deliveries=%d",
						e.Status, len(f.gw.delivered))
				}
			} else if e.Status != domain.StepStatusAssigned || len(f.gw.delivered) != 1 {
				t.Fatalf("non-author auto-selection: status=%s deliveries=%d", e.Status, len(f.gw.delivered))
			}
		})
	}
}

// ── AC 23 ──

// TestStepEntry_SoDAuthorLedger is the committed form of the design-check
// probe TestT007_ReleaseErasesExecutorFromStepExecutions. Rebinding reuses
// the execution row, so a released executor survives only in
// actor_assignments; the SoD rule must read that ledger.
// Mutation: reading only step_executions fails the first sub-test.
func TestStepEntry_SoDAuthorLedger(t *testing.T) {
	ctx := context.Background()
	// carol claims execute, releases, dave claims and completes it; the
	// review execution is created directly (no submit routing).
	prepare := func(t *testing.T, p StepEntryPolicy, wrap func(*memAssignmentStore) AssignmentStore) sodFixture {
		t.Helper()
		f := newSoDFixture(p, sodExec("run-1-execute-1", "execute", domain.StepStatusWaiting, ""))
		if wrap != nil {
			f.orch.assignments = wrap(f.as)
		}
		cr, err := f.orch.ClaimStep(ctx, ClaimRequest{ActorID: "carol", ExecutionID: "run-1-execute-1"})
		if err != nil {
			t.Fatalf("claim carol: %v", err)
		}
		if err := f.orch.ReleaseStep(ctx, ReleaseRequest{ActorID: "carol", AssignmentID: cr.Assignment.AssignmentID}); err != nil {
			t.Fatalf("release carol: %v", err)
		}
		if _, err := f.orch.ClaimStep(ctx, ClaimRequest{ActorID: "dave", ExecutionID: "run-1-execute-1"}); err != nil {
			t.Fatalf("claim dave: %v", err)
		}
		e := execByID(f.st, "run-1-execute-1")
		now := time.Now()
		e.Status, e.OutcomeID, e.CompletedAt = domain.StepStatusCompleted, "completed", &now
		for _, s := range f.st.createdSteps {
			if s.ActorID == "carol" {
				t.Fatalf("precondition of the probe: carol must be erased from step_executions")
			}
		}
		f.st.createdSteps = append(f.st.createdSteps, sodExec("run-1-review-1", "review", domain.StepStatusWaiting, ""))
		return f
	}

	t.Run("released executor is refused on review", func(t *testing.T) {
		f := prepare(t, StepEntryPolicy{SeparationOfDuties: true}, nil)
		before := snapshotExecs(f.st)
		_, err := f.orch.AssignStep(ctx, AssignRequest{RunID: "run-1", StepID: "review", ActorID: "carol"})
		requireCode(t, err, domain.ErrForbidden, "assign of the released executor")
		if !strings.Contains(err.Error(), `"execute"`) {
			t.Fatalf("refusal %q must name the executed step", err.Error())
		}
		_, err = f.orch.ClaimStep(ctx, ClaimRequest{ActorID: "carol", ExecutionID: "run-1-review-1"})
		requireCode(t, err, domain.ErrForbidden, "claim of the released executor")
		requireExecsUnchanged(t, f.st, before, "refused binds")
		// dave, the completing executor, is refused through step_executions.
		_, err = f.orch.AssignStep(ctx, AssignRequest{RunID: "run-1", StepID: "review", ActorID: "dave"})
		requireCode(t, err, domain.ErrForbidden, "assign of the completing executor")
	})
	t.Run("assignment store without the ledger fails closed", func(t *testing.T) {
		f := prepare(t, StepEntryPolicy{SeparationOfDuties: true}, func(m *memAssignmentStore) AssignmentStore {
			return historylessAssignments{inner: m}
		})
		_, err := f.orch.AssignStep(ctx, AssignRequest{RunID: "run-1", StepID: "review", ActorID: "carol"})
		requireCode(t, err, domain.ErrUnavailable, "assign without an assignment ledger")
		_, err = f.orch.ClaimStep(ctx, ClaimRequest{ActorID: "carol", ExecutionID: "run-1-review-1"})
		requireCode(t, err, domain.ErrUnavailable, "claim without an assignment ledger")
	})
	t.Run("ledger read error fails closed", func(t *testing.T) {
		f := prepare(t, StepEntryPolicy{SeparationOfDuties: true}, func(m *memAssignmentStore) AssignmentStore {
			return failingHistoryAssignments{memAssignmentStore: m}
		})
		before := snapshotExecs(f.st)
		_, err := f.orch.AssignStep(ctx, AssignRequest{RunID: "run-1", StepID: "review", ActorID: "carol"})
		requireCode(t, err, domain.ErrUnavailable, "assign with a failing ledger")
		requireExecsUnchanged(t, f.st, before, "refused assign")
	})
	// Review round 1: the engine prefers the run-scoped ledger read and
	// never loads the actor's whole history when it is offered.
	t.Run("run-scoped ledger read is preferred", func(t *testing.T) {
		var narrow *narrowLedgerAssignments
		f := prepare(t, StepEntryPolicy{SeparationOfDuties: true}, func(m *memAssignmentStore) AssignmentStore {
			narrow = &narrowLedgerAssignments{memAssignmentStore: m}
			return narrow
		})
		_, err := f.orch.AssignStep(ctx, AssignRequest{RunID: "run-1", StepID: "review", ActorID: "carol"})
		requireCode(t, err, domain.ErrForbidden, "assign of the released executor (run-scoped ledger)")
		if !strings.Contains(err.Error(), `"execute"`) {
			t.Fatalf("refusal %q must name the executed step", err.Error())
		}
		_, err = f.orch.ClaimStep(ctx, ClaimRequest{ActorID: "carol", ExecutionID: "run-1-review-1"})
		requireCode(t, err, domain.ErrForbidden, "claim of the released executor (run-scoped ledger)")
		if len(narrow.calls) == 0 || narrow.calls[0] != [2]string{"run-1", "carol"} {
			t.Fatalf("run-scoped ledger calls = %v, want (run-1, carol)", narrow.calls)
		}
		if narrow.wideCalls != 0 {
			t.Fatalf("the whole-history read ran %d times although the run-scoped port exists", narrow.wideCalls)
		}
	})
	t.Run("run-scoped ledger read error fails closed", func(t *testing.T) {
		f := prepare(t, StepEntryPolicy{SeparationOfDuties: true}, func(m *memAssignmentStore) AssignmentStore {
			return &narrowLedgerAssignments{memAssignmentStore: m, failNarrow: true}
		})
		before := snapshotExecs(f.st)
		_, err := f.orch.AssignStep(ctx, AssignRequest{RunID: "run-1", StepID: "review", ActorID: "carol"})
		requireCode(t, err, domain.ErrUnavailable, "assign with a failing run-scoped ledger")
		requireExecsUnchanged(t, f.st, before, "refused assign")
	})
	t.Run("zero value binds the released executor as today", func(t *testing.T) {
		f := prepare(t, StepEntryPolicy{}, nil)
		res, err := f.orch.AssignStep(ctx, AssignRequest{RunID: "run-1", StepID: "review", ActorID: "carol"})
		if err != nil {
			t.Fatalf("zero-value assign: %v", err)
		}
		if res.Exec.ActorID != "carol" {
			t.Fatalf("bound %q", res.Exec.ActorID)
		}
	})
}

// ── AC 16 ──

// TestStepEntry_SMPTaskDefaultContract pins why SMP must fix its
// task-default workflow before opting in (SMP TASK-011 step 1): SMP's
// v8.0 review precondition `field_value status: In Progress` reads the
// run-start status on main, which is still Pending mid-run, so under
// EnforcePreconditions every review bind is refused while execute binds
// fine.
func TestStepEntry_SMPTaskDefaultContract(t *testing.T) {
	ctx := context.Background()
	wf := &domain.WorkflowDefinition{
		ID: "task-default", Version: "8.0", EntryStep: "execute",
		Steps: []domain.StepDefinition{
			{ID: "execute", Name: "Execute Task", Type: domain.StepTypeManual,
				Execution: &domain.ExecutionConfig{Mode: domain.ExecModeHybrid,
					EligibleActorTypes: []string{"ai_agent", "human"}, RequiredSkills: []string{"implementation"}},
				Preconditions:   []domain.Precondition{{Type: "artifact_status", Config: map[string]string{"status": "Pending"}}},
				RequiredOutputs: []string{"deliverable"},
				Outcomes: []domain.OutcomeDefinition{
					{ID: "completed", Name: "Implementation Complete", NextStep: "validate", Commit: map[string]string{"status": "In Progress"}},
					{ID: "blocked", Name: "Blocked on Dependency", NextStep: "execute"},
				}},
			{ID: "review", Name: "Code Review", Type: domain.StepTypeReview,
				Execution: &domain.ExecutionConfig{Mode: domain.ExecModeHybrid,
					EligibleActorTypes: []string{"human", "ai_agent"}, RequiredSkills: []string{"review"}},
				Preconditions:  []domain.Precondition{{Type: "field_value", Config: map[string]string{"field": "status", "value": "In Progress"}}},
				RequiredInputs: []string{"deliverable"},
				Outcomes: []domain.OutcomeDefinition{
					{ID: "accepted", Name: "Review Passed", NextStep: "publish"},
					{ID: "needs_rework", Name: "Needs Rework", NextStep: "execute"},
				}},
		},
	}
	st := &mockRunStore{runs: map[string]*domain.Run{"run-1": runFixture("run-1", domain.RunStatusActive)}}
	st.runs["run-1"].TaskPath = "initiatives/INIT-001/epics/EPIC-001/tasks/TASK-001.md"
	st.createdSteps = []*domain.StepExecution{sodExec("run-1-execute-1", "execute", domain.StepStatusWaiting, "")}
	bs := newClaimBlockingStore()
	reader := &legacyRefReader{byRef: map[string][]byte{"HEAD": legacyTaskMarkdown("Pending")}}
	o := &Orchestrator{store: st, wfLoader: &claimTestWFLoader{wf: wf}, events: &mockEventEmitter{},
		git: &stubGitOperator{}, actors: &stubActorAssigner{}, blocking: bs, artifacts: reader,
		assignments: newMemAssignmentStore(), discussions: &fakeDiscussions{}}
	o.WithStepEntryPolicy(smpShapedPolicy(bs))

	const executor = "human-0f9a7c1e-1111-4a3b-9c2d-000000000001"
	const reviewer = "human-0f9a7c1e-2222-4a3b-9c2d-000000000002"
	if _, err := o.AssignStep(ctx, AssignRequest{RunID: "run-1", StepID: "execute", ActorID: executor}); err != nil {
		t.Fatalf("SMP task-default execute assign of a skill-less human handle must succeed: %v", err)
	}
	e := execByID(st, "run-1-execute-1")
	now := time.Now()
	e.Status, e.OutcomeID, e.CompletedAt = domain.StepStatusCompleted, "completed", &now
	st.createdSteps = append(st.createdSteps, sodExec("run-1-review-1", "review", domain.StepStatusWaiting, ""))
	before := snapshotExecs(st)

	_, err := o.AssignStep(ctx, AssignRequest{RunID: "run-1", StepID: "review", ActorID: reviewer})
	requireCode(t, err, domain.ErrPrecondition, "SMP v8.0 review assign under EnforcePreconditions")
	requireExecsUnchanged(t, st, before, "refused review assign")
}

// ── AC 27 ──

// TestStepEntry_ResolverContract pins the Actors resolver contract the
// engine enforces on behalf of every embedder.
// Mutation: removing the ActorID equality check fails the first sub-test.
func TestStepEntry_ResolverContract(t *testing.T) {
	ctx := context.Background()
	wf := &domain.WorkflowDefinition{ID: "wf", EntryStep: "execute", Steps: []domain.StepDefinition{{
		ID: "execute", Name: "Execute", Type: domain.StepTypeManual,
		Execution: &domain.ExecutionConfig{Mode: domain.ExecModeHybrid},
		Outcomes:  []domain.OutcomeDefinition{{ID: "done", Name: "Done", NextStep: "end"}},
	}}}
	setup := func(p StepEntryPolicy) (*mockRunStore, *memAssignmentStore, *Orchestrator) {
		st := &mockRunStore{runs: map[string]*domain.Run{"run-1": runFixture("run-1", domain.RunStatusActive)}}
		st.createdSteps = []*domain.StepExecution{sodExec("run-1-execute-1", "execute", domain.StepStatusWaiting, "")}
		as := newMemAssignmentStore()
		o := &Orchestrator{store: st, wfLoader: &claimTestWFLoader{wf: wf}, events: &mockEventEmitter{},
			git: &stubGitOperator{}, actors: &stubActorAssigner{}, blocking: newClaimBlockingStore(),
			artifacts: &stubArtifactReader{}, assignments: as}
		o.WithStepEntryPolicy(p)
		return st, as, o
	}
	bind := func(o *Orchestrator, actorID string) (assignErr, claimErr error) {
		_, assignErr = o.AssignStep(ctx, AssignRequest{RunID: "run-1", StepID: "execute", ActorID: actorID})
		_, claimErr = o.ClaimStep(ctx, ClaimRequest{ActorID: actorID, ExecutionID: "run-1-execute-1"})
		return assignErr, claimErr
	}

	aliasing := fakeEntryResolver(func(_ context.Context, id string) (*domain.Actor, error) {
		return activeActor(strings.ToUpper(id[:1])+id[1:], domain.ActorTypeHuman), nil
	})
	for _, p := range []StepEntryPolicy{
		{EnforceEligibility: true, Actors: aliasing},
		{Actors: aliasing}, // resolver only: ClaimStep's actor lookup uses it too
	} {
		p := p
		t.Run("resolver answering a different ActorID is not found", func(t *testing.T) {
			st, as, o := setup(p)
			before := snapshotExecs(st)
			if p.EnforceEligibility {
				_, err := o.AssignStep(ctx, AssignRequest{RunID: "run-1", StepID: "execute", ActorID: "alice"})
				requireCode(t, err, domain.ErrNotFound, "AssignStep with an aliasing resolver")
			}
			_, err := o.ClaimStep(ctx, ClaimRequest{ActorID: "alice", ExecutionID: "run-1-execute-1"})
			requireCode(t, err, domain.ErrNotFound, "ClaimStep with an aliasing resolver")
			requireExecsUnchanged(t, st, before, "refused binds")
			if len(as.assignments) != 0 {
				t.Fatalf("a refused claim must not create an assignment row")
			}
		})
	}
	t.Run("typed resolver errors keep their code", func(t *testing.T) {
		_, _, o := setup(StepEntryPolicy{EnforceEligibility: true, Actors: fakeEntryResolver(
			func(context.Context, string) (*domain.Actor, error) {
				return nil, domain.NewError(domain.ErrUnavailable, "member directory unavailable")
			})})
		aErr, cErr := bind(o, "alice")
		requireCode(t, aErr, domain.ErrUnavailable, "AssignStep with an unavailable resolver")
		requireCode(t, cErr, domain.ErrUnavailable, "ClaimStep with an unavailable resolver")
	})
	t.Run("untyped resolver errors are internal", func(t *testing.T) {
		_, _, o := setup(StepEntryPolicy{EnforceEligibility: true, Actors: fakeEntryResolver(
			func(context.Context, string) (*domain.Actor, error) {
				return nil, errors.New("boom")
			})})
		aErr, cErr := bind(o, "alice")
		requireCode(t, aErr, domain.ErrInternal, "AssignStep with a failing resolver")
		requireCode(t, cErr, domain.ErrInternal, "ClaimStep with a failing resolver")
	})
	t.Run("gating policy without a workflow loader does not panic", func(t *testing.T) {
		for _, o := range []*Orchestrator{
			{},
			{store: &mockRunStore{runs: map[string]*domain.Run{"run-1": runFixture("run-1", domain.RunStatusActive)},
				createdSteps: []*domain.StepExecution{sodExec("run-1-execute-1", "execute", domain.StepStatusWaiting, "")}},
				events: &mockEventEmitter{}},
		} {
			o.WithStepEntryPolicy(StepEntryPolicy{SeparationOfDuties: true})
			_, err := o.AssignStep(ctx, AssignRequest{RunID: "run-1", StepID: "execute", ActorID: "alice"})
			requireCode(t, err, domain.ErrUnavailable, "AssignStep on a partially wired orchestrator")
		}
	})
}
