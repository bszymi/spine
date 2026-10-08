package engine_test

// INIT-023 TASK-007 — end-to-end step-entry tests through the public API
// (AC 8, AC 14).

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/core/artifact"
	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/engine"
	"github.com/bszymi/spine/core/workflow"
)

// ledgerAssignments is an engine.AssignmentStore that also offers the
// optional ListAssignmentsByActor ledger read the SoD rule consults.
type ledgerAssignments struct {
	rows map[string]*domain.Assignment
}

func newLedgerAssignments() *ledgerAssignments {
	return &ledgerAssignments{rows: map[string]*domain.Assignment{}}
}

func (l *ledgerAssignments) CreateAssignment(_ context.Context, a *domain.Assignment) error {
	for _, r := range l.rows {
		if r.ExecutionID == a.ExecutionID && r.Status == domain.AssignmentStatusActive {
			return errors.New("duplicate active assignment")
		}
	}
	cp := *a
	l.rows[a.AssignmentID] = &cp
	return nil
}

func (l *ledgerAssignments) UpdateAssignmentStatus(_ context.Context, id string, st domain.AssignmentStatus, at *time.Time) error {
	r, ok := l.rows[id]
	if !ok || r.Status != domain.AssignmentStatusActive {
		return domain.NewError(domain.ErrNotFound, "assignment not found")
	}
	r.Status, r.RespondedAt = st, at
	return nil
}

func (l *ledgerAssignments) GetAssignment(_ context.Context, id string) (*domain.Assignment, error) {
	r, ok := l.rows[id]
	if !ok {
		return nil, domain.NewError(domain.ErrNotFound, "assignment not found")
	}
	cp := *r
	return &cp, nil
}

func (l *ledgerAssignments) ListExpiredAssignments(context.Context, time.Time) ([]domain.Assignment, error) {
	return nil, nil
}

func (l *ledgerAssignments) ListAssignmentsByActor(_ context.Context, actorID string, status *domain.AssignmentStatus) ([]domain.Assignment, error) {
	var out []domain.Assignment
	for _, r := range l.rows {
		if r.ActorID == actorID && (status == nil || r.Status == *status) {
			out = append(out, *r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AssignmentID < out[j].AssignmentID })
	return out, nil
}

type extResolver func(ctx context.Context, actorID string) (*domain.Actor, error)

func (f extResolver) ResolveEntryActor(ctx context.Context, actorID string) (*domain.Actor, error) {
	return f(ctx, actorID)
}

func requireErrCode(t *testing.T, err error, want domain.ErrorCode, what string) {
	t.Helper()
	var se *domain.SpineError
	if err == nil || !errors.As(err, &se) || se.Code != want {
		t.Fatalf("%s: expected %s, got %v", what, want, err)
	}
}

func liveExecID(t *testing.T, st *memStore, runID, stepID string) string {
	t.Helper()
	for _, e := range st.steps {
		if e.RunID == runID && e.StepID == stepID && !e.Status.IsTerminal() {
			return e.ExecutionID
		}
	}
	t.Fatalf("no live execution of %s in run %s", stepID, runID)
	return ""
}

// ── AC 8 ──

// TestStepEntry_GovernanceSelfApprovalRefused replays the audit's F3
// amplifier (poc/governance/assign_bypass_test.go, poc/verify-r1-6): the
// executor of a run assigns themselves to its review.
// Mutation: bypassing the resolver (entryActor → loadActor) fails (b).
func TestStepEntry_GovernanceSelfApprovalRefused(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	wfDef := &domain.WorkflowDefinition{
		ID: "wf", Version: "1", Status: domain.WorkflowStatusActive, AppliesTo: []string{"Task"},
		EntryStep: "execute", Path: "workflows/wf.yaml",
		Steps: []domain.StepDefinition{
			{ID: "execute", Name: "Execute", Type: domain.StepTypeManual,
				Execution: &domain.ExecutionConfig{Mode: domain.ExecModeHybrid, EligibleActorTypes: []string{"human", "ai_agent"}, RequiredSkills: []string{"execution"}},
				Outcomes:  []domain.OutcomeDefinition{{ID: "completed", Name: "done", NextStep: "review"}}},
			{ID: "review", Name: "Review", Type: domain.StepTypeReview,
				Execution: &domain.ExecutionConfig{Mode: domain.ExecModeHybrid, EligibleActorTypes: []string{"human"}, RequiredSkills: []string{"review"}},
				Outcomes:  []domain.OutcomeDefinition{{ID: "accepted", Name: "Accepted", Commit: map[string]string{"status": "Completed"}}}},
		},
	}
	build := func(t *testing.T, p engine.StepEntryPolicy, blocking *entryBlocking) (*memStore, *engine.Orchestrator) {
		t.Helper()
		st := newMemStore()
		orch, err := engine.New(&memWorkflowResolver{wfDef: wfDef}, st, &mockActorGateway{},
			&memArtifactReader{artifacts: map[string]*domain.Artifact{"tasks/t.md": {Path: "tasks/t.md", ID: "TASK-1", Type: "Task", Status: "Pending"}}},
			&memEventEmitter{}, &noopGitOperator{}, &memWorkflowLoader{wfDef: wfDef})
		if err != nil {
			t.Fatal(err)
		}
		orch.WithBranchProtectPolicy(branchprotect.NewPermissive())
		orch.WithBlockingStore(blocking)
		orch.WithAssignmentStore(newLedgerAssignments())
		orch.WithStepEntryPolicy(p)
		return st, orch
	}
	ctx := context.Background()

	t.Run("strict policy", func(t *testing.T) {
		st, orch := build(t, engine.StrictStepEntryPolicy(), &entryBlocking{
			actors: map[string]*domain.Actor{"mallory": {ActorID: "mallory", Type: domain.ActorTypeHuman, Role: domain.RoleContributor, Status: domain.ActorStatusActive}},
			skills: map[string][]domain.Skill{"mallory": activeSkills("execution", "review")},
		})
		res, err := orch.StartRun(ctx, "tasks/t.md")
		if err != nil {
			t.Fatal(err)
		}
		runID := res.Run.RunID
		if _, err := orch.AssignStep(ctx, engine.AssignRequest{RunID: runID, StepID: "execute", ActorID: "mallory"}); err != nil {
			t.Fatalf("assign execute: %v", err)
		}
		if err := orch.SubmitStepResult(ctx, runID+"-execute-1", engine.StepResult{OutcomeID: "completed"}); err != nil {
			t.Fatalf("submit execute: %v", err)
		}
		_, err = orch.AssignStep(ctx, engine.AssignRequest{RunID: runID, StepID: "review", ActorID: "mallory"})
		requireErrCode(t, err, domain.ErrForbidden, "executor self-assigning review (strict)")
		if e := st.steps[liveExecID(t, st, runID, "review")]; e.ActorID != "" || e.Status != domain.StepStatusWaiting {
			t.Fatalf("refused self-review left review %s/%q", e.Status, e.ActorID)
		}
	})

	t.Run("SMP-shaped policy", func(t *testing.T) {
		const executor = "human-6b1d1f3e-0000-4000-8000-00000000000a"
		const reviewer = "human-6b1d1f3e-0000-4000-8000-00000000000b"
		// A CANONICAL resolver (the StepEntryPolicy.Actors contract): only
		// the members' exact handles resolve. Review round 1: a resolver
		// that echoed any "human-*" string let a case, whitespace or brace
		// variant of the executor's handle bind the review.
		members := map[string]bool{executor: true, reviewer: true}
		p := engine.StrictStepEntryPolicy()
		p.RequireSkillsOnAssign = false
		p.Actors = extResolver(func(_ context.Context, id string) (*domain.Actor, error) {
			if members[id] {
				return &domain.Actor{ActorID: id, Type: domain.ActorTypeHuman, Status: domain.ActorStatusActive}, nil
			}
			return nil, domain.NewError(domain.ErrNotFound, "actor not found")
		})
		// The Spine actor table knows none of SMP's human handles, and SMP
		// humans hold no skills.
		st, orch := build(t, p, &entryBlocking{actors: map[string]*domain.Actor{}, skills: map[string][]domain.Skill{}})

		res, err := orch.StartRun(ctx, "tasks/t.md")
		if err != nil {
			t.Fatal(err)
		}
		runID := res.Run.RunID
		if _, err := orch.AssignStep(ctx, engine.AssignRequest{RunID: runID, StepID: "execute", ActorID: executor}); err != nil {
			t.Fatalf("a skill-less SMP human must be assignable to execute through the resolver: %v", err)
		}
		if err := orch.SubmitStepResult(ctx, runID+"-execute-1", engine.StepResult{OutcomeID: "completed"}); err != nil {
			t.Fatalf("submit execute: %v", err)
		}
		_, err = orch.AssignStep(ctx, engine.AssignRequest{RunID: runID, StepID: "review", ActorID: executor})
		requireErrCode(t, err, domain.ErrForbidden, "executor self-assigning review (SMP-shaped)")
		// Variants of the executor's handle are not a second identity:
		// the canonical resolver answers not found, so none binds the
		// review (each would pass the engine's exact-echo check had the
		// resolver accepted it).
		for _, variant := range []string{
			strings.ToUpper(executor[:6]) + executor[6:],
			"human-6B1D1F3E-0000-4000-8000-00000000000A",
			executor + " ",
			"human-{6b1d1f3e-0000-4000-8000-00000000000a}",
		} {
			_, err = orch.AssignStep(ctx, engine.AssignRequest{RunID: runID, StepID: "review", ActorID: variant})
			requireErrCode(t, err, domain.ErrNotFound, "assign of executor-handle variant "+strconv.Quote(variant))
			if e := st.steps[liveExecID(t, st, runID, "review")]; e.ActorID != "" || e.Status != domain.StepStatusWaiting {
				t.Fatalf("variant %q bound the review: %s/%q", variant, e.Status, e.ActorID)
			}
		}
		if _, err := orch.AssignStep(ctx, engine.AssignRequest{RunID: runID, StepID: "review", ActorID: reviewer}); err != nil {
			t.Fatalf("an independent member must be assignable to review: %v", err)
		}
		if err := orch.SubmitStepResult(ctx, runID+"-review-1", engine.StepResult{OutcomeID: "accepted"}); err != nil {
			t.Fatalf("submit review: %v", err)
		}
		run, _ := st.GetRun(ctx, runID)
		if run.Status != domain.RunStatusCompleted && run.Status != domain.RunStatusCommitting {
			t.Fatalf("run status = %s, want committing or completed", run.Status)
		}
	})
}

// ── AC 14 ──

const refTaskPath = "initiatives/INIT-001/epics/EPIC-001/tasks/TASK-001.md"
const refEpicPath = "initiatives/INIT-001/epics/EPIC-001/epic.md"
const refADRPath = "architecture/adr/ADR-001-reference.md"

func refArtifacts(t *testing.T) map[string]*domain.Artifact {
	t.Helper()
	docs := map[string]string{
		refTaskPath: "---\nid: TASK-001\ntype: Task\ntitle: Reference task\nstatus: Pending\n" +
			"epic: /" + refEpicPath + "\ninitiative: /initiatives/INIT-001/initiative.md\n" +
			"links:\n  - type: parent\n    target: /" + refEpicPath + "\n---\n# Task\n",
		refEpicPath: "---\nid: EPIC-001\ntype: Epic\ntitle: Reference epic\nstatus: Pending\n" +
			"initiative: /initiatives/INIT-001/initiative.md\n" +
			"links:\n  - type: parent\n    target: /initiatives/INIT-001/initiative.md\n---\n# Epic\n",
		refADRPath: "---\nid: ADR-001\ntype: ADR\ntitle: Reference decision\nstatus: Proposed\n" +
			"date: 2026-10-07\ndecision_makers: bszymi\n" +
			"links:\n  - type: parent\n    target: /initiatives/INIT-001/initiative.md\n---\n# ADR\n",
	}
	out := map[string]*domain.Artifact{}
	for path, md := range docs {
		a, err := artifact.Parse(path, []byte(md))
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		out[path] = a
	}
	return out
}

func loadReferenceWorkflow(t *testing.T, name string) *domain.WorkflowDefinition {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("..", "..", "workflows", name))
	if err != nil {
		t.Fatalf("read workflow %s: %v", name, err)
	}
	wf, err := workflow.Parse("workflows/"+name, content)
	if err != nil {
		t.Fatalf("parse workflow %s: %v", name, err)
	}
	return wf
}

// refStep binds one step and submits its outcome. refused lists actors
// whose bind must be refused with ErrForbidden (separation of duties)
// before binder is bound.
type refStep struct {
	step    string
	refused []string
	binder  string
	outcome string
}

// TestStepEntry_ReferenceWorkflowsUnderStrictPolicy drives Spine's own
// task-default, epic-lifecycle and adr workflows to completion under
// StrictStepEntryPolicy(), with distinct executor and reviewer actors
// holding only skills the workflow declares. The executors also hold
// `review`, so the only check that can refuse them on a review step is
// separation of duties (asserted as ErrForbidden).
// Mutation: reverting the epic-lifecycle edit (execute's
// `artifact_status: In Progress`) fails epic-lifecycle at execute with
// ErrPrecondition; removing the strict `status` field resolution fails
// task-default at execute (`field_value status: Pending`).
func TestStepEntry_ReferenceWorkflowsUnderStrictPolicy(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	human := func(id string) *domain.Actor {
		return &domain.Actor{ActorID: id, Type: domain.ActorTypeHuman, Role: domain.RoleContributor, Status: domain.ActorStatusActive}
	}
	cases := []struct {
		file   string
		path   string
		actors map[string]*domain.Actor
		skills map[string][]domain.Skill
		script []refStep
	}{
		{
			file: "task-default.yaml", path: refTaskPath,
			actors: map[string]*domain.Actor{
				"drafter":  {ActorID: "drafter", Type: domain.ActorTypeAIAgent, Role: domain.RoleContributor, Status: domain.ActorStatusActive},
				"executor": human("executor"),
				"reviewer": human("reviewer"),
			},
			skills: map[string][]domain.Skill{
				"drafter": activeSkills("execution", "review"), "executor": activeSkills("execution", "review"), "reviewer": activeSkills("review"),
			},
			script: []refStep{
				{step: "draft", binder: "drafter", outcome: "ready"},
				{step: "execute", binder: "executor", outcome: "completed"},
				{step: "review", refused: []string{"executor", "drafter"}, binder: "reviewer", outcome: "accepted"},
			},
		},
		{
			file: "epic-lifecycle.yaml", path: refEpicPath,
			actors: map[string]*domain.Actor{"executor": human("executor"), "reviewer": human("reviewer")},
			skills: map[string][]domain.Skill{"executor": activeSkills("planning", "execution", "review"), "reviewer": activeSkills("review")},
			script: []refStep{
				{step: "plan", binder: "executor", outcome: "planned"},
				{step: "execute", binder: "executor", outcome: "tasks_complete"},
				{step: "review", refused: []string{"executor"}, binder: "reviewer", outcome: "completed"},
			},
		},
		{
			file: "adr.yaml", path: refADRPath,
			actors: map[string]*domain.Actor{"executor": human("executor"), "reviewer": human("reviewer")},
			skills: map[string][]domain.Skill{"executor": activeSkills("planning", "review"), "reviewer": activeSkills("review")},
			script: []refStep{
				{step: "propose", binder: "executor", outcome: "proposed"},
				{step: "evaluate", refused: []string{"executor"}, binder: "reviewer", outcome: "accepted"},
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.file, func(t *testing.T) {
			ctx := context.Background()
			wf := loadReferenceWorkflow(t, tc.file)
			st := newMemStore()
			orch, err := engine.New(&memWorkflowResolver{wfDef: wf}, st, &mockActorGateway{},
				&memArtifactReader{artifacts: refArtifacts(t)}, &memEventEmitter{}, &noopGitOperator{}, &memWorkflowLoader{wfDef: wf})
			if err != nil {
				t.Fatal(err)
			}
			orch.WithBranchProtectPolicy(branchprotect.NewPermissive())
			orch.WithBlockingStore(&entryBlocking{actors: tc.actors, skills: tc.skills})
			orch.WithAssignmentStore(newLedgerAssignments())
			orch.WithStepEntryPolicy(engine.StrictStepEntryPolicy())

			res, err := orch.StartRun(ctx, tc.path)
			if err != nil {
				t.Fatalf("StartRun: %v", err)
			}
			runID := res.Run.RunID
			for _, s := range tc.script {
				for _, actorID := range s.refused {
					_, err := orch.AssignStep(ctx, engine.AssignRequest{RunID: runID, StepID: s.step, ActorID: actorID})
					requireErrCode(t, err, domain.ErrForbidden, tc.file+": "+actorID+" binding "+s.step)
				}
				if _, err := orch.AssignStep(ctx, engine.AssignRequest{RunID: runID, StepID: s.step, ActorID: s.binder}); err != nil {
					t.Fatalf("%s: assign %s to %s: %v", tc.file, s.step, s.binder, err)
				}
				execID := liveExecID(t, st, runID, s.step)
				if err := orch.SubmitStepResult(ctx, execID, engine.StepResult{OutcomeID: s.outcome}); err != nil {
					t.Fatalf("%s: submit %s/%s: %v", tc.file, s.step, s.outcome, err)
				}
			}
			run, _ := st.GetRun(ctx, runID)
			if run.Status != domain.RunStatusCompleted {
				t.Fatalf("%s: run status = %s, want completed", tc.file, run.Status)
			}
		})
	}
}
