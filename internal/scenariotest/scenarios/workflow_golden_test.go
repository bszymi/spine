//go:build scenario

package scenarios_test

import (
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/scenariotest/assert"
	"github.com/bszymi/spine/internal/scenariotest/engine"
	"github.com/bszymi/spine/internal/scenariotest/harness"
)

// Golden path workflow definitions for testing.
// These mirror the production workflow step structure (same IDs, outcomes,
// and required_outputs) but omit preconditions that require artifact status
// changes in git between steps. Precondition testing is covered separately
// in workflow_transition_test.go (TASK-003).

const goldenTaskDefaultYAML = `id: task-default
name: Default Task Workflow
version: "1.0"
status: Active
description: Golden path test workflow for task-default step progression.
applies_to:
  - Task
entry_step: draft
steps:
  - id: draft
    name: Draft Setup
    type: automated
    outcomes:
      - id: ready
        name: Ready for Execution
        next_step: execute
    timeout: "30m"

  - id: execute
    name: Execute Task
    type: manual
    required_outputs:
      - deliverable
    outcomes:
      - id: completed
        name: Implementation Complete
        next_step: review
      - id: blocked
        name: Blocked on Dependency
        next_step: draft
    timeout: "4h"

  - id: review
    name: Review Deliverable
    type: review
    outcomes:
      - id: accepted
        name: Accepted
        next_step: commit
      - id: needs_rework
        name: Needs Rework
        next_step: execute
    timeout: "24h"

  - id: commit
    name: Commit Outcomes
    type: automated
    outcomes:
      - id: committed
        name: Changes Committed
        next_step: end
        commit:
          status: Completed
    timeout: "10m"
`

const goldenTaskSpikeYAML = `id: task-spike
name: Spike Investigation Workflow
version: "1.0"
status: Active
description: Golden path test workflow for task-spike step progression.
applies_to:
  - Task
entry_step: investigate
steps:
  - id: investigate
    name: Investigate
    type: manual
    required_outputs:
      - findings
    outcomes:
      - id: findings_ready
        name: Findings Ready
        next_step: summarize
      - id: inconclusive
        name: Inconclusive
        next_step: end
    timeout: "8h"

  - id: summarize
    name: Summarize Findings
    type: automated
    required_outputs:
      - summary
    outcomes:
      - id: summarized
        name: Summary Complete
        next_step: review
    timeout: "1h"

  - id: review
    name: Review Summary
    type: review
    outcomes:
      - id: accepted
        name: Accepted
        next_step: end
        commit:
          status: Completed
      - id: needs_more_investigation
        name: Needs More Investigation
        next_step: investigate
    timeout: "24h"
`

const goldenEpicLifecycleYAML = `id: epic-lifecycle
name: Epic Lifecycle Workflow
version: "1.0"
status: Active
description: Golden path test workflow for epic-lifecycle step progression.
applies_to:
  - Epic
entry_step: plan
steps:
  - id: plan
    name: Plan Epic
    type: manual
    required_outputs:
      - task_breakdown
    outcomes:
      - id: planned
        name: Epic Planned
        next_step: execute
    timeout: "72h"

  - id: execute
    name: Execute Tasks
    type: manual
    outcomes:
      - id: tasks_complete
        name: All Tasks Complete
        next_step: review
      - id: blocked
        name: Blocked
        next_step: plan
    timeout: "168h"

  - id: review
    name: Review Epic
    type: review
    outcomes:
      - id: completed
        name: Epic Complete
        next_step: end
        commit:
          status: Completed
      - id: needs_more_work
        name: Needs More Work
        next_step: execute
    timeout: "48h"
`

// seedWorkflow returns a step that writes a workflow YAML file to the test
// repo and commits it. Use this instead of harness.WithWorkflows() when
// the test needs a custom or simplified workflow definition.
func seedWorkflow(name, yamlContent string) engine.Step {
	return engine.WriteAndCommit(
		"workflows/"+name+".yaml",
		yamlContent,
		"seed workflow "+name,
	)
}

// TestWorkflow_TaskDefaultGoldenPath validates the complete task-default
// workflow lifecycle: draft -> execute -> review -> commit -> completed.
func TestWorkflow_TaskDefaultGoldenPath(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "task-default-golden-path",
		Description: "Verify task-default workflow progresses through all steps to completion",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []engine.Step{
			seedWorkflow("task-default", goldenTaskDefaultYAML),
			engine.SeedHierarchy("INIT-001", "EPIC-001", "TASK-001"),
			engine.SyncProjections(),

			// Start the workflow run.
			engine.StartRun("initiatives/init-001/epics/epic-001/tasks/task-001.md"),
			engine.AssertRunStatus(domain.RunStatusActive),
			engine.AssertCurrentStep("draft"),

			// Step 1: Draft -> ready -> execute.
			engine.SubmitStepResult("ready"),
			engine.AssertCurrentStep("execute"),

			// Step 2: Execute -> completed (with required output) -> review.
			engine.SubmitStepResult("completed", "deliverable"),
			engine.AssertCurrentStep("review"),

			// Step 3: Review -> accepted -> commit.
			engine.SubmitStepResult("accepted"),
			engine.AssertCurrentStep("commit"),

			// Step 4: Commit -> committed -> end (run completes).
			engine.SubmitStepResult("committed"),
			engine.AssertRunCompleted(),

			// Verify step count: draft + execute + review + commit = 4.
			assertStepCount(4),
		},
	})
}

// TestWorkflow_TaskSpikeGoldenPath validates the task-spike workflow
// lifecycle: investigate -> summarize -> review -> completed.
func TestWorkflow_TaskSpikeGoldenPath(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "task-spike-golden-path",
		Description: "Verify task-spike workflow progresses through all steps to completion",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []engine.Step{
			// Seed only the spike workflow (not task-default, to avoid ambiguity).
			seedWorkflow("task-spike", goldenTaskSpikeYAML),
			engine.SeedHierarchy("INIT-002", "EPIC-002", "TASK-002"),
			engine.SyncProjections(),

			// Start the workflow run.
			engine.StartRun("initiatives/init-002/epics/epic-002/tasks/task-002.md"),
			engine.AssertRunStatus(domain.RunStatusActive),
			engine.AssertCurrentStep("investigate"),

			// Step 1: Investigate -> findings_ready (with required output) -> summarize.
			engine.SubmitStepResult("findings_ready", "findings"),
			engine.AssertCurrentStep("summarize"),

			// Step 2: Summarize -> summarized (with required output) -> review.
			engine.SubmitStepResult("summarized", "summary"),
			engine.AssertCurrentStep("review"),

			// Step 3: Review -> accepted -> end (run completes).
			engine.SubmitStepResult("accepted"),
			engine.AssertRunCompleted(),

			// Verify step count: investigate + summarize + review = 3.
			assertStepCount(3),
		},
	})
}

// TestWorkflow_EpicLifecycleGoldenPath validates the epic-lifecycle workflow
// lifecycle: plan -> execute -> review -> completed.
func TestWorkflow_EpicLifecycleGoldenPath(t *testing.T) {
	engine.RunScenario(t, engine.Scenario{
		Name:        "epic-lifecycle-golden-path",
		Description: "Verify epic-lifecycle workflow progresses through all steps to completion",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []engine.Step{
			seedWorkflow("epic-lifecycle", goldenEpicLifecycleYAML),
			engine.SeedHierarchy("INIT-003", "EPIC-003", "TASK-003"),
			engine.SyncProjections(),

			// Start the workflow run for the epic (not the task).
			engine.StartRun("initiatives/init-003/epics/epic-003/epic.md"),
			engine.AssertRunStatus(domain.RunStatusActive),
			engine.AssertCurrentStep("plan"),

			// Step 1: Plan -> planned (with required output) -> execute.
			engine.SubmitStepResult("planned", "task_breakdown"),
			engine.AssertCurrentStep("execute"),

			// Step 2: Execute -> tasks_complete -> review.
			engine.SubmitStepResult("tasks_complete"),
			engine.AssertCurrentStep("review"),

			// Step 3: Review -> completed -> end (run completes).
			engine.SubmitStepResult("completed"),
			engine.AssertRunCompleted(),

			// Verify step count: plan + execute + review = 3.
			assertStepCount(3),
		},
	})
}

// assertStepCount returns a step that asserts the total number of step
// executions for the current run.
func assertStepCount(expected int) engine.Step {
	return engine.Step{
		Name: "assert-step-count",
		Action: func(sc *engine.ScenarioContext) error {
			runID := sc.MustGet("run_id").(string)
			assert.StepCount(sc.T, sc.DB, sc.Ctx, runID, expected)
			return nil
		},
	}
}
