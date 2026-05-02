//go:build scenario

package scenarios_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/engine"
	scenarioEngine "github.com/bszymi/spine/internal/scenariotest/engine"
	"github.com/bszymi/spine/internal/scenariotest/harness"
)

// entryStepLifecycleWorkflowYAML mirrors the shape of task-default's
// entry → execute progression (ai_only entry feeding into a hybrid step)
// without the cross-step preconditions that depend on a Git merge.
// Sufficient to exercise the Option B contract from
// INIT-020/EPIC-001/TASK-004 end-to-end without making the test
// dependent on the merge pipeline.
const entryStepLifecycleWorkflowYAML = `id: task-entry-lifecycle
name: Entry Step Lifecycle Test Workflow
version: "1.0"
status: Active
description: Two-step hybrid workflow for exercising the assign-before-submit contract.
applies_to:
  - Task
entry_step: draft
steps:
  - id: draft
    name: Draft Setup
    type: automated
    execution:
      mode: ai_only
      eligible_actor_types:
        - ai_agent
      required_skills:
        - execution
    outcomes:
      - id: ready
        name: Ready for Execution
        next_step: execute
    timeout: "30m"

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

// TestEntryStepLifecycle_AssignBeforeSubmit exercises the Option B
// contract from INIT-020/EPIC-001/TASK-004 against a task-default-shaped
// workflow:
//
//   - The entry step is delivered by the engine with status=`waiting`,
//     not `assigned` with an empty actor_id (the prior phantom state).
//   - /submit on a waiting step fails fast with ErrConflict instead of
//     silently mutating the step into a failed-with-retry state.
//   - /assign cleanly transitions the step to `assigned` and binds the
//     actor, after which /submit advances the run to the next step.
//   - The run progresses with exactly one `execute` execution row,
//     proving no phantom retry was spawned along the way.
//
// Pinning all four invariants in one scenariotest keeps the
// assign-before-submit lifecycle from regressing as the engine's
// activation path is refactored.
func TestEntryStepLifecycle_AssignBeforeSubmit(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	scenarioEngine.RunScenario(t, scenarioEngine.Scenario{
		Name:        "entry-step-lifecycle-assign-before-submit",
		Description: "task-default-shaped lifecycle: waiting → assigned → submit → next step, with no phantom retries",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []scenarioEngine.Step{
			seedWorkflow("task-entry-lifecycle", entryStepLifecycleWorkflowYAML),
			scenarioEngine.SeedHierarchy("INIT-901", "EPIC-901", "TASK-901"),
			scenarioEngine.SyncProjections(),

			registerSkill("sk-exec-901", "execution", "execution"),
			registerActor("actor-ai-901", domain.ActorTypeAIAgent, domain.RoleContributor),
			assignSkillToActor("actor-ai-901", "sk-exec-901"),
			registerActor("actor-human-901", domain.ActorTypeHuman, domain.RoleContributor),
			assignSkillToActor("actor-human-901", "sk-exec-901"),

			scenarioEngine.StartRun("initiatives/init-901/epics/epic-901/tasks/task-901.md"),
			scenarioEngine.AssertRunStatus(domain.RunStatusActive),
			scenarioEngine.AssertCurrentStep("draft"),

			// Option B invariant: ai_only entry step stays in waiting when
			// no actor selector is configured (harness default), instead of
			// becoming phantom-assigned with actor_id=NULL.
			assertStepStatus("draft", domain.StepStatusWaiting),
			assertStepActorIDEmpty("draft"),

			// Submit-from-waiting is rejected with ErrConflict and does not
			// mutate the step. This is the "/submit returned 200 but failed
			// silently" path from the bug report — now a typed error.
			assertSubmitFromWaitingFails("ready"),
			assertStepStatus("draft", domain.StepStatusWaiting),

			// /assign transitions waiting → assigned cleanly because the
			// state machine accepts step.assign from waiting.
			assignStep("draft", "actor-ai-901"),
			assertStepStatus("draft", domain.StepStatusAssigned),

			// Now /submit succeeds and advances the run to execute.
			scenarioEngine.SubmitStepResult("ready"),
			scenarioEngine.AssertCurrentStep("execute"),

			// The hybrid execute step also lands in waiting.
			assertStepStatus("execute", domain.StepStatusWaiting),
			assertStepActorIDEmpty("execute"),

			// Assign to a human and complete to reach the terminal step.
			assignStep("execute", "actor-human-901"),
			assertStepStatus("execute", domain.StepStatusAssigned),
			scenarioEngine.SubmitStepResult("completed"),
			scenarioEngine.AssertRunCompleted(),

			// No phantom retry execution should have been spawned.
			assertStepExecutionCount("execute", 1),
		},
	})
}

// assignStep returns a scenariotest Step that binds an actor to the named
// step via the orchestrator's AssignStep entry point — the same code path
// used by POST /assign. Asserts no error so a regression in the state
// machine surfaces immediately.
func assignStep(stepID, actorID string) scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: fmt.Sprintf("assign-step-%s-to-%s", stepID, actorID),
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			runID := sc.MustGet("run_id").(string)
			_, err := sc.Runtime.Orchestrator.AssignStep(sc.Ctx, engine.AssignRequest{
				RunID:   runID,
				StepID:  stepID,
				ActorID: actorID,
			})
			if err != nil {
				return fmt.Errorf("assign step %s to %s: %w", stepID, actorID, err)
			}
			return nil
		},
	}
}

// assertStepActorIDEmpty asserts that the (non-terminal) execution for the
// given step has no actor bound. Used together with assertStepStatus to
// confirm the step is in `waiting` AND not in the historical phantom
// state (status=assigned, actor_id=NULL).
func assertStepActorIDEmpty(stepID string) scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "assert-step-" + stepID + "-actor-empty",
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			runID := sc.MustGet("run_id").(string)
			steps, err := sc.Runtime.Store.ListStepExecutionsByRun(sc.Ctx, runID)
			if err != nil {
				return err
			}
			for _, s := range steps {
				if s.StepID == stepID && !s.Status.IsTerminal() {
					if s.ActorID != "" {
						sc.T.Errorf("step %s: expected empty actor_id, got %q", stepID, s.ActorID)
					}
					return nil
				}
			}
			sc.T.Errorf("step %s: no non-terminal execution found in run %s", stepID, runID)
			return nil
		},
	}
}

// assertSubmitFromWaitingFails calls IngestResult against the current
// (waiting) step and asserts it returns ErrConflict. This pins the
// engine-side state guard added in TASK-004.
func assertSubmitFromWaitingFails(outcomeID string) scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: "assert-submit-from-waiting-fails-" + outcomeID,
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			execID := sc.MustGet("current_execution_id").(string)
			_, err := sc.Runtime.Orchestrator.IngestResult(sc.Ctx, engine.SubmitRequest{
				ExecutionID: execID,
				OutcomeID:   outcomeID,
			})
			if err == nil {
				sc.T.Errorf("expected ErrConflict for submit-from-waiting on %s, got nil", execID)
				return nil
			}
			var spineErr *domain.SpineError
			if !errors.As(err, &spineErr) || spineErr.Code != domain.ErrConflict {
				sc.T.Errorf("expected ErrConflict, got %v", err)
			}
			return nil
		},
	}
}

// assertStepExecutionCount asserts the total number of step execution
// rows for stepID across the run. Pins the "exactly one execute row"
// invariant from TASK-004 acceptance criteria — a phantom retry would
// show up here.
func assertStepExecutionCount(stepID string, expected int) scenarioEngine.Step {
	return scenarioEngine.Step{
		Name: fmt.Sprintf("assert-step-%s-count-%d", stepID, expected),
		Action: func(sc *scenarioEngine.ScenarioContext) error {
			runID := sc.MustGet("run_id").(string)
			steps, err := sc.Runtime.Store.ListStepExecutionsByRun(sc.Ctx, runID)
			if err != nil {
				return err
			}
			count := 0
			for _, s := range steps {
				if s.StepID == stepID {
					count++
				}
			}
			if count != expected {
				sc.T.Errorf("step %s: expected %d execution row(s), got %d", stepID, expected, count)
			}
			return nil
		},
	}
}
