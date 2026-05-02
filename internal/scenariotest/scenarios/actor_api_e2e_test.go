//go:build scenario

package scenarios_test

import (
	"fmt"
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/engine"
	scenarioEngine "github.com/bszymi/spine/internal/scenariotest/engine"
	"github.com/bszymi/spine/internal/scenariotest/harness"
)

// Shared workflow for human and AI agent actor API tests.
// The execute step accepts both human and ai_agent actors and requires
// the "execution" skill.
const actorAPIHybridWorkflowYAML = `id: task-actor-api-hybrid
name: Actor API Hybrid Test Workflow
version: "1.0"
status: Active
description: Workflow for actor API end-to-end scenario tests (human + AI eligible).
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
        - execution
    outcomes:
      - id: completed
        name: Done
        next_step: end
    timeout: "4h"
`

// Workflow for automated system actor tests.
// The execute step is automated_only — the engine auto-assigns it on run start.
const actorAPIAutomatedWorkflowYAML = `id: task-actor-api-automated
name: Actor API Automated Test Workflow
version: "1.0"
status: Active
description: Workflow for actor API end-to-end scenario tests (automated_system only).
applies_to:
  - Task
entry_step: execute
steps:
  - id: execute
    name: Execute Task
    type: automated
    execution:
      mode: automated_only
      eligible_actor_types:
        - automated_system
    outcomes:
      - id: completed
        name: Done
        next_step: end
    timeout: "4h"
`

// acknowledgeCurrentStep transitions the currently assigned step to in_progress.
// The actor must be the current assignee of the step.
func acknowledgeCurrentStep(actorID string) scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "acknowledge-step-" + actorID,
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			execID := sc.MustGet("current_execution_id").(string)
			_, err := sc.Runtime.Orchestrator.AcknowledgeStep(sc.Ctx, engine.AcknowledgeRequest{
				ActorID:     actorID,
				ExecutionID: execID,
			})
			if err != nil {
				return fmt.Errorf("acknowledge step: %w", err)
			}
			return nil
		},
	}
}

// assertListStepExecutionsCount verifies that ListStepExecutions for the given
// actor type returns exactly expectedCount steps.
func assertListStepExecutionsCount(actorType string, expectedCount int) scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: fmt.Sprintf("assert-list-steps-%s-count-%d", actorType, expectedCount),
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			steps, err := sc.Runtime.Orchestrator.ListStepExecutions(sc.Ctx, engine.StepExecutionQuery{
				ActorType: actorType,
				Limit:     10,
			})
			if err != nil {
				return fmt.Errorf("list step executions (actorType=%s): %w", actorType, err)
			}
			if len(steps) != expectedCount {
				sc.T.Errorf("ListStepExecutions(actorType=%s): expected %d steps, got %d",
					actorType, expectedCount, len(steps))
			}
			return nil
		},
	}
}

// TestActorAPI_HumanGoldenPath validates the complete polling loop for a human actor:
// register → start run → poll → claim → acknowledge → submit → run advances.
//
// Scenario: Human actor completes the full actor API cycle
//
//	Given a hybrid workflow (human + ai_agent eligible)
//	  And a human actor with the "execution" skill
//	When a run is started for a task
//	Then the step is visible via ListStepExecutions with actorType=human
//	When the actor claims the step
//	Then the step status is "assigned"
//	When the actor acknowledges the step
//	Then the step status is "in_progress"
//	When the actor submits "completed"
//	Then the run is completed
func TestActorAPI_HumanGoldenPath(t *testing.T) {
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "actor-api-human-golden-path",
		Description: "Human actor completes the full claim → acknowledge → submit cycle",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			seedWorkflow("task-actor-api-hybrid", actorAPIHybridWorkflowYAML),
			scenarioEngine.SeedHierarchy("INIT-801", "EPIC-801", "TASK-801"),
			scenarioEngine.SyncProjections(),

			registerSkill("sk-exec-801", "execution", "execution"),
			registerActor("actor-human-801", domain.ActorTypeHuman, domain.RoleContributor),
			assignSkillToActor("actor-human-801", "sk-exec-801"),

			scenarioEngine.StartRun("initiatives/init-801/epics/epic-801/tasks/task-801.md"),
			scenarioEngine.AssertRunStatus(domain.RunStatusActive),
			scenarioEngine.AssertCurrentStep("execute"),

			// Human actor polls and finds the waiting step.
			assertListStepExecutionsCount("human", 1),

			// Claim → step becomes assigned to the actor.
			claimCurrentStep("actor-human-801"),
			assertStepStatus("execute", domain.StepStatusAssigned),

			// Acknowledge → step transitions to in_progress.
			acknowledgeCurrentStep("actor-human-801"),
			assertStepStatus("execute", domain.StepStatusInProgress),

			// Submit result → run completes (next_step: end).
			scenarioEngine.SubmitStepResult("completed"),
			scenarioEngine.AssertRunCompleted(),
		},
	})
}

// TestActorAPI_AIAgentGoldenPath validates the complete polling loop for an AI agent actor.
// Also verifies that the same step is invisible to an automated_system poller (type filtering).
//
// Scenario: AI agent actor completes the full actor API cycle
//
//	Given a hybrid workflow (human + ai_agent eligible)
//	  And an ai_agent actor with the "execution" skill
//	When a run is started for a task
//	Then the step is visible via ListStepExecutions with actorType=ai_agent
//	And the step is NOT visible to an automated_system actor
//	When the actor claims, acknowledges, and submits
//	Then the run is completed
func TestActorAPI_AIAgentGoldenPath(t *testing.T) {
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "actor-api-ai-agent-golden-path",
		Description: "AI agent actor completes the full claim → acknowledge → submit cycle",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			seedWorkflow("task-actor-api-hybrid", actorAPIHybridWorkflowYAML),
			scenarioEngine.SeedHierarchy("INIT-802", "EPIC-802", "TASK-802"),
			scenarioEngine.SyncProjections(),

			registerSkill("sk-exec-802", "execution", "execution"),
			registerActor("actor-ai-802", domain.ActorTypeAIAgent, domain.RoleContributor),
			assignSkillToActor("actor-ai-802", "sk-exec-802"),

			scenarioEngine.StartRun("initiatives/init-802/epics/epic-802/tasks/task-802.md"),
			scenarioEngine.AssertRunStatus(domain.RunStatusActive),
			scenarioEngine.AssertCurrentStep("execute"),

			// AI agent sees the step; automated_system does not (type filtering).
			assertListStepExecutionsCount("ai_agent", 1),
			assertListStepExecutionsCount("automated_system", 0),

			claimCurrentStep("actor-ai-802"),
			assertStepStatus("execute", domain.StepStatusAssigned),

			acknowledgeCurrentStep("actor-ai-802"),
			assertStepStatus("execute", domain.StepStatusInProgress),

			scenarioEngine.SubmitStepResult("completed"),
			scenarioEngine.AssertRunCompleted(),
		},
	})
}

// TestActorAPI_AutomatedSystemGoldenPath validates the automated actor polling loop.
// The actor polls by type, finds the waiting step, claims it, and submits.
//
// NOTE: The test harness has actorSelector=nil. Per INIT-020/EPIC-001/TASK-004
// (Option B), automated_only steps with no resolvable actor stay in `waiting`
// rather than transitioning to a phantom-assigned state. The automated actor
// therefore polls with actorType=automated_system to discover and claim the
// waiting step before submitting.
//
// Scenario: Automated system actor claims and submits a waiting step
//
//	Given an automated_only workflow
//	  And an automated_system actor registered
//	  And no actor selector configured (test harness)
//	When a run is started
//	Then the step is left in `waiting` (no auto-claim without selector)
//	And the automated actor sees it via ListStepExecutions with actorType=automated_system
//	When the actor claims and submits "completed"
//	Then the run is completed
func TestActorAPI_AutomatedSystemGoldenPath(t *testing.T) {
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "actor-api-automated-golden-path",
		Description: "Automated actor claims a waiting step and submits the result",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			seedWorkflow("task-actor-api-automated", actorAPIAutomatedWorkflowYAML),
			scenarioEngine.SeedHierarchy("INIT-803", "EPIC-803", "TASK-803"),
			scenarioEngine.SyncProjections(),

			// Automated steps do not require skills.
			registerActor("actor-auto-803", domain.ActorTypeAutomated, domain.RoleContributor),

			scenarioEngine.StartRun("initiatives/init-803/epics/epic-803/tasks/task-803.md"),
			scenarioEngine.AssertRunStatus(domain.RunStatusActive),
			scenarioEngine.AssertCurrentStep("execute"),

			// Step stays in waiting because actorSelector is nil — Option B
			// keeps the step claimable rather than wedging it in an
			// unrecoverable phantom-assigned state.
			assertStepStatus("execute", domain.StepStatusWaiting),

			// Automated actor discovers the step via type-based polling.
			assertListStepExecutionsCount("automated_system", 1),

			// Automated actor claims, then submits — SubmitStepResult auto-
			// acknowledges assigned steps to in_progress before completing.
			claimCurrentStep("actor-auto-803"),
			assertStepStatus("execute", domain.StepStatusAssigned),
			scenarioEngine.SubmitStepResult("completed"),
			scenarioEngine.AssertRunCompleted(),
		},
	})
}

// TestActorAPI_ActorTypeFiltering verifies that ListStepExecutions enforces
// eligible_actor_types: a human/AI step is invisible to automated_system pollers.
//
// Scenario: Actor type filtering excludes incompatible step types from poll results
//
//	Given a hybrid workflow (human + ai_agent eligible)
//	  And actors of all three types registered
//	When a run is started
//	Then human sees 1 step, ai_agent sees 1 step, automated_system sees 0 steps
func TestActorAPI_ActorTypeFiltering(t *testing.T) {
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "actor-api-type-filtering",
		Description: "ListStepExecutions filters steps by eligible_actor_types",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			seedWorkflow("task-actor-api-hybrid", actorAPIHybridWorkflowYAML),
			scenarioEngine.SeedHierarchy("INIT-804", "EPIC-804", "TASK-804"),
			scenarioEngine.SyncProjections(),

			registerSkill("sk-exec-804", "execution", "execution"),
			registerActor("actor-human-804", domain.ActorTypeHuman, domain.RoleContributor),
			assignSkillToActor("actor-human-804", "sk-exec-804"),
			registerActor("actor-ai-804", domain.ActorTypeAIAgent, domain.RoleContributor),
			assignSkillToActor("actor-ai-804", "sk-exec-804"),
			registerActor("actor-auto-804", domain.ActorTypeAutomated, domain.RoleContributor),

			scenarioEngine.StartRun("initiatives/init-804/epics/epic-804/tasks/task-804.md"),
			scenarioEngine.AssertRunStatus(domain.RunStatusActive),

			// human and ai_agent are eligible: both see the step.
			assertListStepExecutionsCount("human", 1),
			assertListStepExecutionsCount("ai_agent", 1),

			// automated_system is not in eligible_actor_types: step is invisible.
			assertListStepExecutionsCount("automated_system", 0),
		},
	})
}

// TestActorAPI_AcknowledgeIdempotency verifies that acknowledging an already-in_progress
// step returns success without corrupting the step state.
//
// Scenario: Acknowledging an already-in_progress step is a safe no-op
//
//	Given actor claims and acknowledges the step (step is now in_progress)
//	When the actor acknowledges the step again
//	Then the second acknowledge returns success (not an error)
//	And the step status remains in_progress
func TestActorAPI_AcknowledgeIdempotency(t *testing.T) {
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "actor-api-acknowledge-idempotency",
		Description: "Acknowledging an already-in_progress step returns success (idempotent)",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			seedWorkflow("task-actor-api-hybrid", actorAPIHybridWorkflowYAML),
			scenarioEngine.SeedHierarchy("INIT-805", "EPIC-805", "TASK-805"),
			scenarioEngine.SyncProjections(),

			registerSkill("sk-exec-805", "execution", "execution"),
			registerActor("actor-human-805", domain.ActorTypeHuman, domain.RoleContributor),
			assignSkillToActor("actor-human-805", "sk-exec-805"),

			scenarioEngine.StartRun("initiatives/init-805/epics/epic-805/tasks/task-805.md"),
			scenarioEngine.AssertRunStatus(domain.RunStatusActive),

			// Claim and first acknowledge → step is now in_progress.
			claimCurrentStep("actor-human-805"),
			acknowledgeCurrentStep("actor-human-805"),
			assertStepStatus("execute", domain.StepStatusInProgress),

			// Second acknowledge on the same in_progress step must return success (idempotent no-op).
			acknowledgeCurrentStep("actor-human-805"),
			assertStepStatus("execute", domain.StepStatusInProgress),
		},
	})
}

// TestActorAPI_ReleaseAndReclaim verifies the full release → re-claim ownership transfer:
// actor-1 claims a step, releases it back to waiting, actor-2 claims and completes it.
//
// Scenario: Release and re-claim transfers step ownership to a new actor
//
//	Given actor-release-1 claims the step
//	When actor-release-1 releases the step with reason "passing to actor-2"
//	Then the step returns to status "waiting"
//	And the step is visible again via ListStepExecutions
//	When actor-release-2 claims, acknowledges, and submits "completed"
//	Then the run is completed
func TestActorAPI_ReleaseAndReclaim(t *testing.T) {
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "actor-api-release-and-reclaim",
		Description: "Actor releases step; different actor claims and completes it",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			seedWorkflow("task-actor-api-hybrid", actorAPIHybridWorkflowYAML),
			scenarioEngine.SeedHierarchy("INIT-806", "EPIC-806", "TASK-806"),
			scenarioEngine.SyncProjections(),

			registerSkill("sk-exec-806", "execution", "execution"),
			registerActor("actor-release-1", domain.ActorTypeHuman, domain.RoleContributor),
			assignSkillToActor("actor-release-1", "sk-exec-806"),
			registerActor("actor-release-2", domain.ActorTypeHuman, domain.RoleContributor),
			assignSkillToActor("actor-release-2", "sk-exec-806"),

			scenarioEngine.StartRun("initiatives/init-806/epics/epic-806/tasks/task-806.md"),
			scenarioEngine.AssertRunStatus(domain.RunStatusActive),
			scenarioEngine.AssertCurrentStep("execute"),

			// Actor-1 claims the step.
			claimCurrentStep("actor-release-1"),
			assertStepStatus("execute", domain.StepStatusAssigned),

			// Actor-1 releases back to pool.
			releaseCurrentAssignment("actor-release-1", "passing to actor-2"),
			assertStepStatus("execute", domain.StepStatusWaiting),

			// Step is visible again after release.
			assertListStepExecutionsCount("human", 1),

			// Actor-2 claims, acknowledges, and completes.
			claimCurrentStep("actor-release-2"),
			assertStepStatus("execute", domain.StepStatusAssigned),
			acknowledgeCurrentStep("actor-release-2"),
			assertStepStatus("execute", domain.StepStatusInProgress),
			scenarioEngine.SubmitStepResult("completed"),
			scenarioEngine.AssertRunCompleted(),
		},
	})
}
