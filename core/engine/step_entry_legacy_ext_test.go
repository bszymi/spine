package engine_test

// INIT-023 TASK-007 AC 1 — legacy pins through the public API.
//
// The audit's governance and verify-r1-6 proofs of concept, committed with
// no step-entry policy installed: a zero-value engine.Orchestrator still
// lets AssignStep bind an actor that ClaimStep refuses, binds the
// executor of a run to its review, and binds unknown actor IDs. Only the
// binding assertions are kept; the review execution is created directly
// in the store rather than by submitting the execute step, so the
// submit-side changes of TASK-008 cannot turn these pins red.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bszymi/spine/adapters/store"
	"github.com/bszymi/spine/core/branchprotect"
	"github.com/bszymi/spine/core/domain"
	"github.com/bszymi/spine/core/engine"
)

// entryBlocking satisfies engine.BlockingStore plus the duck-typed
// GetActor / ListActorSkills extensions ClaimStep and the step-entry
// policy consult.
type entryBlocking struct {
	actors map[string]*domain.Actor
	skills map[string][]domain.Skill
}

func (b *entryBlocking) QueryArtifactLinks(context.Context, string) ([]store.ArtifactLink, error) {
	return nil, nil
}
func (b *entryBlocking) QueryArtifactLinksByTarget(context.Context, string) ([]store.ArtifactLink, error) {
	return nil, nil
}
func (b *entryBlocking) GetArtifactProjection(context.Context, string) (*store.ArtifactProjection, error) {
	return nil, domain.NewError(domain.ErrNotFound, "no projection")
}
func (b *entryBlocking) GetActor(_ context.Context, id string) (*domain.Actor, error) {
	if a, ok := b.actors[id]; ok {
		return a, nil
	}
	return nil, domain.NewError(domain.ErrNotFound, "actor not found")
}
func (b *entryBlocking) ListActorSkills(_ context.Context, id string) ([]domain.Skill, error) {
	return b.skills[id], nil
}

func activeSkills(names ...string) []domain.Skill {
	out := make([]domain.Skill, 0, len(names))
	for _, n := range names {
		out = append(out, domain.Skill{Name: n, Status: domain.SkillStatusActive})
	}
	return out
}

// legacyMarkStepDone moves a run's live execution of stepID to completed
// and creates the next step's waiting execution directly in the store, so
// the legacy pins do not depend on submit routing.
func legacyMarkStepDone(t *testing.T, st *memStore, runID, stepID, actorID, nextStepID string) {
	t.Helper()
	for _, e := range st.steps {
		if e.RunID == runID && e.StepID == stepID && !e.Status.IsTerminal() {
			now := time.Now()
			e.Status = domain.StepStatusCompleted
			e.ActorID = actorID
			e.CompletedAt = &now
		}
	}
	id := runID + "-" + nextStepID + "-1"
	st.steps[id] = &domain.StepExecution{ExecutionID: id, RunID: runID, StepID: nextStepID,
		Status: domain.StepStatusWaiting, Attempt: 1, CreatedAt: time.Now()}
}

// From poc/governance/assign_bypass_test.go.
func TestStepEntryLegacy_GovernanceAssignBypass(t *testing.T) {
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
	st := newMemStore()
	orch, err := engine.New(&memWorkflowResolver{wfDef: wfDef}, st, &mockActorGateway{},
		&memArtifactReader{artifacts: map[string]*domain.Artifact{"tasks/t.md": {Path: "tasks/t.md", ID: "TASK-1", Type: "Task", Status: "Pending"}}},
		&memEventEmitter{}, &noopGitOperator{}, &memWorkflowLoader{wfDef: wfDef})
	if err != nil {
		t.Fatal(err)
	}
	orch.WithBranchProtectPolicy(branchprotect.New(branchprotect.StaticRules(branchprotect.BootstrapDefaults())))
	// mallory is a contributor holding only the execution skill.
	orch.WithBlockingStore(&entryBlocking{
		actors: map[string]*domain.Actor{"mallory": {ActorID: "mallory", Type: domain.ActorTypeHuman, Role: domain.RoleContributor, Status: domain.ActorStatusActive}},
		skills: map[string][]domain.Skill{"mallory": activeSkills("execution")},
	})

	ctx := context.Background()
	res, err := orch.StartRun(ctx, "tasks/t.md")
	if err != nil {
		t.Fatal(err)
	}
	runID := res.Run.RunID

	if _, err := orch.AssignStep(ctx, engine.AssignRequest{RunID: runID, StepID: "execute", ActorID: "mallory"}); err != nil {
		t.Fatalf("assign execute: %v", err)
	}
	legacyMarkStepDone(t, st, runID, "execute", "mallory", "review")

	reviewExec := runID + "-review-1"
	// The pull path refuses mallory on review (missing review skill)...
	if _, err := orch.ClaimStep(ctx, engine.ClaimRequest{ActorID: "mallory", ExecutionID: reviewExec}); err == nil ||
		!strings.Contains(err.Error(), "review") {
		t.Fatalf("ClaimStep must refuse the missing review skill, got %v", err)
	}
	// ...while the zero-value AssignStep binds the executor to her own review.
	out, err := orch.AssignStep(ctx, engine.AssignRequest{RunID: runID, StepID: "review", ActorID: "mallory"})
	if err != nil {
		t.Fatalf("zero-value AssignStep(review, executor) must bind as before, got %v", err)
	}
	if out.Exec.ActorID != "mallory" || out.Exec.Status != domain.StepStatusAssigned {
		t.Fatalf("review exec = %s/%s, want mallory/assigned", out.Exec.ActorID, out.Exec.Status)
	}

	// A non-existent actor id is bound too.
	res2, err := orch.StartRun(ctx, "tasks/t.md")
	if err != nil {
		t.Fatal(err)
	}
	ghost, err := orch.AssignStep(ctx, engine.AssignRequest{RunID: res2.Run.RunID, StepID: "execute", ActorID: "no-such-actor"})
	if err != nil {
		t.Fatalf("zero-value AssignStep must bind a ghost actor as before, got %v", err)
	}
	if ghost.Exec.ActorID != "no-such-actor" {
		t.Fatalf("ghost exec bound %q", ghost.Exec.ActorID)
	}
}

// From poc/verify-r1-6/assign_skeptic_engine_test.go: SMP's artifact
// creation shapes (draft hybrid [human, ai_agent] [planning]; review
// human_only [human] [review]) with a skill-less SMP human.
func TestStepEntryLegacy_AssignSkipsEligibility_SMPShapes(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	wfDef := &domain.WorkflowDefinition{
		ID: "artifact-creation", Version: "1", Status: domain.WorkflowStatusActive, AppliesTo: []string{"Task"},
		EntryStep: "draft", Path: "workflows/artifact-creation.yaml",
		Steps: []domain.StepDefinition{
			{ID: "draft", Name: "Draft", Type: domain.StepTypeManual,
				Execution: &domain.ExecutionConfig{Mode: domain.ExecModeHybrid, EligibleActorTypes: []string{"human", "ai_agent"}, RequiredSkills: []string{"planning"}},
				Outcomes:  []domain.OutcomeDefinition{{ID: "ready_for_review", Name: "Ready", NextStep: "review"}}},
			{ID: "review", Name: "Review", Type: domain.StepTypeReview,
				Execution: &domain.ExecutionConfig{Mode: domain.ExecModeHumanOnly, EligibleActorTypes: []string{"human"}, RequiredSkills: []string{"review"}},
				Outcomes:  []domain.OutcomeDefinition{{ID: "approved", Name: "Approved", Commit: map[string]string{"status": "Pending"}}}},
		},
	}
	st := newMemStore()
	orch, err := engine.New(&memWorkflowResolver{wfDef: wfDef}, st, &mockActorGateway{},
		&memArtifactReader{artifacts: map[string]*domain.Artifact{"tasks/t.md": {Path: "tasks/t.md", ID: "TASK-1", Type: "Task", Status: "Draft"}}},
		&memEventEmitter{}, &noopGitOperator{}, &memWorkflowLoader{wfDef: wfDef})
	if err != nil {
		t.Fatal(err)
	}
	orch.WithBranchProtectPolicy(branchprotect.New(branchprotect.StaticRules(branchprotect.BootstrapDefaults())))
	orch.WithBlockingStore(&entryBlocking{
		actors: map[string]*domain.Actor{
			"member-1": {ActorID: "member-1", Type: domain.ActorTypeHuman, Role: domain.RoleContributor, Status: domain.ActorStatusActive},
			"agent-x":  {ActorID: "agent-x", Type: domain.ActorTypeAIAgent, Role: domain.RoleContributor, Status: domain.ActorStatusActive},
		},
		skills: map[string][]domain.Skill{}, // SMP humans hold no skills
	})
	ctx := context.Background()

	res, err := orch.StartRun(ctx, "tasks/t.md")
	if err != nil {
		t.Fatal(err)
	}
	runID := res.Run.RunID

	// The enforced pull path refuses every skill-less SMP human on draft...
	if _, err := orch.ClaimStep(ctx, engine.ClaimRequest{ActorID: "member-1", ExecutionID: runID + "-draft-1"}); err == nil {
		t.Fatal("ClaimStep must refuse a skill-less human on the draft step")
	}
	// ...and SMP's flows only work because the zero-value AssignStep skips skills.
	if _, err := orch.AssignStep(ctx, engine.AssignRequest{RunID: runID, StepID: "draft", ActorID: "member-1"}); err != nil {
		t.Fatalf("zero-value AssignStep(draft) must bind as before, got %v", err)
	}
	legacyMarkStepDone(t, st, runID, "draft", "member-1", "review")

	if _, err := orch.ClaimStep(ctx, engine.ClaimRequest{ActorID: "member-1", ExecutionID: runID + "-review-1"}); err == nil {
		t.Fatal("ClaimStep must refuse a skill-less human on review")
	}
	out, err := orch.AssignStep(ctx, engine.AssignRequest{RunID: runID, StepID: "review", ActorID: "member-1"})
	if err != nil {
		t.Fatalf("zero-value AssignStep(review, executor) must bind as before, got %v", err)
	}
	if out.Exec.ActorID != "member-1" || out.Exec.Status != domain.StepStatusAssigned {
		t.Fatalf("review exec = %s/%s, want member-1/assigned", out.Exec.ActorID, out.Exec.Status)
	}

	res2, err := orch.StartRun(ctx, "tasks/t.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := orch.AssignStep(ctx, engine.AssignRequest{RunID: res2.Run.RunID, StepID: "draft", ActorID: "no-such-actor"}); err != nil {
		t.Fatalf("zero-value AssignStep must bind a ghost actor as before, got %v", err)
	}
}
