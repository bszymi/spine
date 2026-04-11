//go:build scenario

package scenarios_test

import (
	"testing"

	"github.com/bszymi/spine/internal/domain"
	"github.com/bszymi/spine/internal/scenariotest/engine"
	"github.com/bszymi/spine/internal/scenariotest/harness"
)

// ---------- Workflow for standard run status rewrite testing ----------
// Mirrors the production task-default workflow's commit step, which sets
// status to Completed on the terminal outcome.
const statusRewriteWorkflowYAML = `id: task-default
name: Default Task Workflow
version: "1.0"
status: Active
description: Standard run status rewrite test workflow.
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

// TestStandardRunWorkflow_PendingToCompleted validates that a standard
// execution run rewrites the task artifact's status from Pending to
// Completed when the terminal outcome declares commit.status: Completed.
func TestStandardRunWorkflow_PendingToCompleted(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")

	const taskPath = "initiatives/init-001/epics/epic-001/tasks/task-001.md"

	engine.RunScenario(t, engine.Scenario{
		Name:        "standard-run-pending-to-completed",
		Description: "Task executed via standard run transitions from Pending to Completed on merge",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []engine.Step{
			// --- Setup: seed workflow, hierarchy, and sync ---
			engine.WriteAndCommit(
				"workflows/task-default.yaml",
				statusRewriteWorkflowYAML,
				"seed task-default workflow",
			),
			engine.SeedHierarchy("INIT-001", "EPIC-001", "TASK-001"),
			engine.SyncProjections(),

			// Confirm task starts as Pending.
			engine.AssertProjection(taskPath, "Status", "Pending"),

			// --- Start standard run ---
			engine.StartRun(taskPath),
			engine.AssertRunStatus(domain.RunStatusActive),
			engine.AssertCurrentStep("execute"),
			engine.AssertBranchExists(),

			// --- Execute: simulate work on branch, submit deliverable ---
			engine.WriteOnBranch(
				"initiatives/init-001/epics/epic-001/tasks/task-001-deliverable.md",
				"# Deliverable\nTask implementation output.\n",
				"Add task deliverable",
			),
			engine.SubmitStepResult("completed", "deliverable"),
			engine.AssertCurrentStep("review"),

			// --- Review: approve → triggers merge with status: Completed ---
			engine.SubmitStepResult("accepted"),
			engine.AssertRunCompleted(),

			// --- Verify: artifact file on main has status Completed ---
			engine.AssertFileExists(taskPath),
			engine.AssertFileContains(taskPath, "status: Completed"),

			// --- Verify: deliverable from branch is on main ---
			engine.AssertFileExists("initiatives/init-001/epics/epic-001/tasks/task-001-deliverable.md"),

			// --- Verify: branch cleaned up ---
			engine.AssertBranchNotExists(),

			// --- Verify: projection reflects Completed ---
			engine.SyncProjections(),
			engine.AssertProjection(taskPath, "Status", "Completed"),
		},
	})
}

// TestArtifactCreationWorkflow_DraftToPending validates the full artifact
// creation workflow end-to-end: a task starts as Draft, progresses through
// draft → validate → review, and on approval the branch auto-merges to main
// with the artifact status updated to Pending.
func TestArtifactCreationWorkflow_DraftToPending(t *testing.T) {
	t.Setenv("SPINE_GIT_AUTO_PUSH", "false")

	const taskPath = "initiatives/init-001-test/epics/epic-001-test/tasks/task-010-scenario.md"
	const taskContent = `---
id: TASK-010
type: Task
title: Scenario Workflow Task
status: Draft
epic: /initiatives/init-001-test/epics/epic-001-test/epic.md
initiative: /initiatives/init-001-test/initiative.md
created: 2026-01-01
last_updated: 2026-01-01
links:
  - type: parent
    target: /initiatives/init-001-test/epics/epic-001-test/epic.md
---

# TASK-010 — Scenario Workflow Task

A task created to validate the artifact creation workflow transitions.
`

	engine.RunScenario(t, engine.Scenario{
		Name:        "artifact-creation-draft-to-pending",
		Description: "Task created as Draft progresses through creation workflow and lands on main as Pending",
		EnvOpts: []harness.EnvOption{
			harness.WithGovernance(),
			harness.WithRuntimeOrchestrator(),
		},
		Steps: []engine.Step{
			// --- Setup: seed parent artifacts on main ---
			engine.WriteAndCommit(
				"initiatives/init-001-test/initiative.md",
				"---\nid: INIT-001\ntype: Initiative\ntitle: Test Initiative\nstatus: Pending\ncreated: 2026-01-01\nlast_updated: 2026-01-01\n---\n# INIT-001 — Test Initiative\n",
				"seed initiative",
			),
			engine.WriteAndCommit(
				"initiatives/init-001-test/epics/epic-001-test/epic.md",
				"---\nid: EPIC-001\ntype: Epic\ntitle: Test Epic\nstatus: Pending\ninitiative: /initiatives/init-001-test/initiative.md\ncreated: 2026-01-01\nlast_updated: 2026-01-01\nlinks:\n  - type: parent\n    target: /initiatives/init-001-test/initiative.md\n---\n# EPIC-001 — Test Epic\n",
				"seed epic",
			),
			seedCreationWorkflow(),
			engine.SyncProjections(),

			// --- Stage 1: Start planning run — task enters draft step ---
			engine.StartPlanningRun(taskPath, taskContent),
			engine.AssertRunStatus(domain.RunStatusActive),
			engine.AssertCurrentStep("draft"),
			engine.AssertBranchExists(),

			// --- Stage 2: Finish drafting — submit and move to validation ---
			engine.SubmitStepResult("ready_for_review", "artifact_content"),
			engine.AssertCurrentStep("validate"),

			// --- Stage 3: Validation passes — move to review ---
			engine.SubmitStepResult("valid"),
			engine.AssertCurrentStep("review"),

			// --- Stage 4: Approve review — auto-merge to main with status Pending ---
			engine.SubmitStepResult("approved"),
			engine.AssertRunCompleted(),

			// --- Verify: artifact exists on main after auto-merge ---
			engine.AssertFileExists(taskPath),

			// --- Verify: branch cleaned up after merge ---
			engine.AssertBranchNotExists(),

			// --- Verify: artifact status rewritten from Draft to Pending ---
			engine.AssertFileContains(taskPath, "status: Pending"),

			// --- Verify: projection reflects Pending status ---
			engine.SyncProjections(),
			engine.AssertProjection(taskPath, "Status", "Pending"),
		},
	})
}
