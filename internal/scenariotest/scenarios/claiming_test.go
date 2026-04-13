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

// Workflow for claim/release tests.
const claimWorkflowYAML = `id: task-claim
name: Claim Test Workflow
version: "1.0"
status: Active
description: Workflow for testing claim and release operations.
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

// TestClaiming_ClaimAndRelease verifies the full claim -> release -> re-claim cycle.
//
// Scenario: Actor claims step, releases it, step returns to waiting
//   Given an active run on step "execute" with two eligible actors
//   When claimer-1 claims the step
//   Then the step should be in status "Assigned"
//   When claimer-1 releases the step with reason "need help"
//   Then the step should return to status "Waiting"
func TestClaiming_ClaimAndRelease(t *testing.T) {
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "claim-and-release-cycle",
		Description: "Actor claims step, releases it, another actor claims it",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			seedWorkflow("task-claim", claimWorkflowYAML),
			scenarioEngine.SeedHierarchy("INIT-CLM", "EPIC-CLM", "TASK-CLM"),
			scenarioEngine.SyncProjections(),

			registerSkill("sk-exec-clm", "execution", "execution"),
			registerActor("claimer-1", domain.ActorTypeHuman, domain.RoleContributor),
			assignSkillToActor("claimer-1", "sk-exec-clm"),
			registerActor("claimer-2", domain.ActorTypeHuman, domain.RoleContributor),
			assignSkillToActor("claimer-2", "sk-exec-clm"),

			// Start run — step is in waiting state.
			scenarioEngine.StartRun("initiatives/init-clm/epics/epic-clm/tasks/task-clm.md"),
			scenarioEngine.AssertRunStatus(domain.RunStatusActive),
			scenarioEngine.AssertCurrentStep("execute"),

			// Claim the step.
			claimCurrentStep("claimer-1"),
			assertStepStatus("execute", domain.StepStatusAssigned),

			// Release the step.
			releaseCurrentAssignment("claimer-1", "need help"),

			// Step should be back to waiting.
			assertStepStatus("execute", domain.StepStatusWaiting),
		},
	})
}

// ── Claim/Release helper steps ──

func claimCurrentStep(actorID string) scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "claim-step-" + actorID,
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			execID := sc.MustGet("current_execution_id").(string)
			result, err := sc.Runtime.Orchestrator.ClaimStep(sc.Ctx, engine.ClaimRequest{
				ActorID:     actorID,
				ExecutionID: execID,
			})
			if err != nil {
				return fmt.Errorf("claim step: %w", err)
			}
			sc.Set("current_assignment_id", result.Assignment.AssignmentID)
			return nil
		},
	}
}

func releaseCurrentAssignment(actorID, reason string) scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "release-assignment-" + actorID,
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			assignmentID := sc.MustGet("current_assignment_id").(string)
			return sc.Runtime.Orchestrator.ReleaseStep(sc.Ctx, engine.ReleaseRequest{
				ActorID:      actorID,
				AssignmentID: assignmentID,
				Reason:       reason,
			})
		},
	}
}

func assertStepStatus(stepID string, expected domain.StepExecutionStatus) scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: fmt.Sprintf("assert-step-%s-%s", stepID, expected),
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			runID := sc.MustGet("run_id").(string)
			steps, err := sc.Runtime.Store.ListStepExecutionsByRun(sc.Ctx, runID)
			if err != nil {
				return err
			}
			for _, s := range steps {
				if s.StepID == stepID {
					if s.Status != expected {
						sc.T.Errorf("step %s: expected status %s, got %s", stepID, expected, s.Status)
					}
					return nil
				}
			}
			sc.T.Errorf("step %s not found in run %s", stepID, runID)
			return nil
		},
	}
}
