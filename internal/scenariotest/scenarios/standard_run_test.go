//go:build scenario

package scenarios_test

import (
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/scenariotest/engine"
	"github.com/bszymi/spine/internal/scenariotest/harness"
)

// Workflow for standard run merge testing. The terminal outcome has commit
// metadata so the run enters the committing state and triggers MergeRunBranch.
const standardRunMergeWorkflowYAML = `id: task-default
name: Default Task Workflow
version: "1.0"
status: Active
description: Standard run merge test workflow.
applies_to:
  - Task
entry_step: execute
steps:
  - id: execute
    name: Execute Task
    type: manual
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
    outcomes:
      - id: accepted
        name: Accepted
        next_step: end
        commit:
          status: Completed
      - id: needs_rework
        name: Needs Rework
        next_step: execute
    timeout: "24h"
`

// TestStandardRun_BranchCreationAndMerge validates the complete standard run
// lifecycle with Git branch isolation: start → branch created → work on branch
// → complete → merge to main → branch cleaned up.
func TestStandardRun_BranchCreationAndMerge(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	engine.RunScenario(t, engine.Scenario{
		Name:        "standard-run-branch-and-merge",
		Description: "Verify standard run creates a branch, merges to main on completion, and cleans up",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []engine.Step{
			// Setup: seed workflow, hierarchy, and sync.
			engine.WriteAndCommit(
				"workflows/task-default.yaml",
				standardRunMergeWorkflowYAML,
				"seed task-default workflow",
			),
			engine.SeedHierarchy("INIT-001", "EPIC-001", "TASK-001"),
			engine.SyncProjections(),

			// Start the standard run — branch should be created.
			engine.StartRun("initiatives/init-001/epics/epic-001/tasks/task-001.md"),
			engine.AssertRunStatus(domain.RunStatusActive),
			engine.AssertBranchExists(),

			// Simulate work on the branch: write a deliverable file.
			engine.WriteOnBranch(
				"initiatives/init-001/epics/epic-001/tasks/task-001-deliverable.md",
				"# Deliverable\nTask implementation output.\n",
				"Add task deliverable",
			),

			// Execute → completed (with required output) → review.
			engine.SubmitStepResult("completed", "deliverable"),
			engine.AssertCurrentStep("review"),

			// Review → accepted → end (triggers commit/merge).
			engine.SubmitStepResult("accepted"),
			engine.AssertRunCompleted(),

			// Verify the deliverable from the branch is on main.
			engine.AssertFileExists("initiatives/init-001/epics/epic-001/tasks/task-001-deliverable.md"),

			// Verify the branch was cleaned up.
			engine.AssertBranchNotExists(),
		},
	})
}

// TestStandardRun_BranchCreatedOnStart validates that StartRun creates a
// Git branch and stores it on the run record.
func TestStandardRun_BranchCreatedOnStart(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")
	engine.RunScenario(t, engine.Scenario{
		Name:        "standard-run-branch-on-start",
		Description: "Verify StartRun creates a Git branch stored on the run record",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []engine.Step{
			engine.WriteAndCommit(
				"workflows/task-default.yaml",
				standardRunMergeWorkflowYAML,
				"seed task-default workflow",
			),
			engine.SeedHierarchy("INIT-002", "EPIC-002", "TASK-002"),
			engine.SyncProjections(),

			engine.StartRun("initiatives/init-002/epics/epic-002/tasks/task-002.md"),
			engine.AssertRunStatus(domain.RunStatusActive),
			engine.AssertBranchExists(),
		},
	})
}
