//go:build scenario

package scenarios_test

import (
	"context"
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/scenariotest/engine"
	"github.com/bszymi/spine/internal/scenariotest/harness"
	"github.com/bszymi/spine/internal/workflow"
)

// Workflow with required_skills on all actor-assigned steps.
const skillWorkflowYAML = `id: task-with-skills
name: Task Workflow With Skills
version: "1.0"
status: Active
description: Workflow where every actor-assigned step requires skills.
applies_to:
  - Task
entry_step: execute
steps:
  - id: execute
    name: Execute Task
    type: manual
    execution:
      mode: hybrid
      eligible_actor_types:
        - human
        - ai_agent
      required_skills:
        - backend_development
    required_outputs:
      - deliverable
    outcomes:
      - id: completed
        name: Implementation Complete
        next_step: review
    timeout: "4h"

  - id: review
    name: Review Deliverable
    type: review
    execution:
      mode: human_only
      eligible_actor_types:
        - human
      required_skills:
        - code_review
    outcomes:
      - id: accepted
        name: Accepted
        next_step: end
    timeout: "24h"
`

// TestSkill_WorkflowWithSkillsGoldenPath verifies a workflow with required_skills
// progresses through all steps to completion.
func TestSkill_WorkflowWithSkillsGoldenPath(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "skill-workflow-golden-path",
		Description: "Verify workflow with required_skills runs to completion",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []engine.Step{
			seedWorkflow("task-with-skills", skillWorkflowYAML),
			engine.SeedHierarchy("INIT-SK1", "EPIC-SK1", "TASK-SK1"),
			engine.SyncProjections(),

			// Register skills and actors in the database.
			registerSkill("sk-backend", "backend_development", "development"),
			registerSkill("sk-review", "code_review", "review"),
			registerActor("dev-1", domain.ActorTypeHuman, domain.RoleContributor),
			assignSkillToActor("dev-1", "sk-backend"),
			registerActor("reviewer-1", domain.ActorTypeHuman, domain.RoleReviewer),
			assignSkillToActor("reviewer-1", "sk-review"),

			// Start the workflow run.
			engine.StartRun("initiatives/init-sk1/epics/epic-sk1/tasks/task-sk1.md"),
			engine.AssertRunStatus(domain.RunStatusActive),
			engine.AssertCurrentStep("execute"),

			// Execute -> completed -> review.
			engine.SubmitStepResult("completed", "deliverable"),
			engine.AssertCurrentStep("review"),

			// Review -> accepted -> end.
			engine.SubmitStepResult("accepted"),
			engine.AssertRunCompleted(),
		},
	})
}

// TestSkill_WorkflowValidationRejectsMissingSkills verifies that schema
// validation rejects a workflow where actor-assigned steps lack required_skills.
func TestSkill_WorkflowValidationRejectsMissingSkills(t *testing.T) {
	wf := &domain.WorkflowDefinition{
		ID: "test-no-skills", Name: "Test No Skills", Version: "1.0",
		Status: domain.WorkflowStatusActive,
		Description: "Workflow missing required_skills on actor step",
		AppliesTo:   []string{"Task"},
		EntryStep:   "execute",
		Steps: []domain.StepDefinition{
			{
				ID: "execute", Name: "Execute", Type: domain.StepTypeManual,
				Execution: &domain.ExecutionConfig{Mode: domain.ExecModeHybrid},
				Outcomes:  []domain.OutcomeDefinition{{ID: "done", Name: "Done", NextStep: "end"}},
			},
		},
	}

	errors := workflow.ValidateSchema(wf)
	found := false
	for _, e := range errors {
		if e.Field == "steps[0].execution.required_skills" {
			found = true
		}
	}
	if !found {
		t.Error("expected validation error for missing required_skills on hybrid step")
	}
}

// TestSkill_AutomatedStepDoesNotRequireSkills verifies that automated_only
// steps pass validation without required_skills.
func TestSkill_AutomatedStepDoesNotRequireSkills(t *testing.T) {
	wf := &domain.WorkflowDefinition{
		ID: "test-automated", Name: "Test Automated", Version: "1.0",
		Status: domain.WorkflowStatusActive,
		Description: "Workflow with automated step and no skills",
		AppliesTo:   []string{"Task"},
		EntryStep:   "commit",
		Steps: []domain.StepDefinition{
			{
				ID: "commit", Name: "Commit", Type: domain.StepTypeAutomated,
				Execution: &domain.ExecutionConfig{Mode: domain.ExecModeAutomatedOnly},
				Outcomes:  []domain.OutcomeDefinition{{ID: "done", Name: "Done", NextStep: "end"}},
			},
		},
	}

	errors := workflow.ValidateSchema(wf)
	for _, e := range errors {
		if e.Field == "steps[0].execution.required_skills" {
			t.Errorf("automated_only step should not require skills, got error: %s", e.Message)
		}
	}
}

// TestSkill_ActorSkillRegistrationAndQuery verifies that skills can be
// registered, assigned to actors, and queried from the database.
func TestSkill_ActorSkillRegistrationAndQuery(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "skill-registration-and-query",
		Description: "Verify skill CRUD and actor-skill assignment via database",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []engine.Step{
			// Create skills.
			registerSkill("sk-1", "backend_development", "development"),
			registerSkill("sk-2", "frontend_development", "development"),
			registerSkill("sk-3", "code_review", "review"),

			// Create actors and assign skills.
			registerActor("fullstack-1", domain.ActorTypeHuman, domain.RoleContributor),
			assignSkillToActor("fullstack-1", "sk-1"),
			assignSkillToActor("fullstack-1", "sk-2"),
			assignSkillToActor("fullstack-1", "sk-3"),

			registerActor("backend-1", domain.ActorTypeHuman, domain.RoleContributor),
			assignSkillToActor("backend-1", "sk-1"),

			// Verify actor skills.
			assertActorSkillCount("fullstack-1", 3),
			assertActorSkillCount("backend-1", 1),

			// Query actors by skills — AND matching.
			assertEligibleActorCount([]string{"backend_development", "code_review"}, 1), // only fullstack
			assertEligibleActorCount([]string{"backend_development"}, 2),                // both
			assertEligibleActorCount([]string{"frontend_development"}, 1),               // only fullstack
		},
	})
}

// ── Skill helper steps ──

func registerSkill(skillID, name, category string) engine.Step {
	return engine.Step{
		Name: "register-skill-" + name,
		Action: func(sc *engine.ScenarioContext) error {
			return sc.Runtime.Store.CreateSkill(sc.Ctx, &domain.Skill{
				SkillID:  skillID,
				Name:     name,
				Category: category,
				Status:   domain.SkillStatusActive,
			})
		},
	}
}

func registerActor(actorID string, actorType domain.ActorType, role domain.ActorRole) engine.Step {
	return engine.Step{
		Name: "register-actor-" + actorID,
		Action: func(sc *engine.ScenarioContext) error {
			return sc.Runtime.Store.CreateActor(sc.Ctx, &domain.Actor{
				ActorID: actorID,
				Type:    actorType,
				Name:    actorID,
				Role:    role,
				Status:  domain.ActorStatusActive,
			})
		},
	}
}

func assignSkillToActor(actorID, skillID string) engine.Step {
	return engine.Step{
		Name: "assign-skill-" + skillID + "-to-" + actorID,
		Action: func(sc *engine.ScenarioContext) error {
			return sc.Runtime.Store.AddSkillToActor(sc.Ctx, actorID, skillID)
		},
	}
}

func assertActorSkillCount(actorID string, expected int) engine.Step {
	return engine.Step{
		Name: "assert-actor-skill-count-" + actorID,
		Action: func(sc *engine.ScenarioContext) error {
			skills, err := sc.Runtime.Store.ListActorSkills(sc.Ctx, actorID)
			if err != nil {
				return err
			}
			if len(skills) != expected {
				sc.T.Errorf("actor %s: expected %d skills, got %d", actorID, expected, len(skills))
			}
			return nil
		},
	}
}

func assertEligibleActorCount(skillNames []string, expected int) engine.Step {
	return engine.Step{
		Name: "assert-eligible-actors-" + skillNames[0],
		Action: func(sc *engine.ScenarioContext) error {
			actors, err := sc.Runtime.Store.ListActorsBySkills(context.Background(), skillNames)
			if err != nil {
				return err
			}
			if len(actors) != expected {
				sc.T.Errorf("skills %v: expected %d eligible actors, got %d", skillNames, expected, len(actors))
			}
			return nil
		},
	}
}
